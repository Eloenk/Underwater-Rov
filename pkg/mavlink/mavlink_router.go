package mavlink

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"companion/pkg/config"
	"companion/pkg/logging"
	"companion/pkg/supervisor"
)

const (
	MavlinkV1Magic = 0xFE
	MavlinkV2Magic = 0xFD

	MsgIDHeartbeat = 0
)

type LinkStatus struct {
	Connected     bool      `json:"connected"`
	Port          string    `json:"port"`
	BaudRate      int       `json:"baud_rate"`
	SystemID      uint8     `json:"system_id"`
	ComponentID   uint8     `json:"component_id"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	HeartbeatAge  string    `json:"heartbeat_age"`
	PacketsRx     uint64    `json:"packets_rx"`
	PacketsTx     uint64    `json:"packets_tx"`
}

type MAVLinkModule struct {
	cfg          config.MAVLinkConfig
	logger       *logging.LogManager
	mu           sync.RWMutex
	serialMu     sync.Mutex
	status       LinkStatus
	udpConns     []*net.UDPConn
	activeSerial io.ReadWriteCloser
	serialPort   string
	packetsRx    uint64
	packetsTx    uint64
	broadcastCh  chan []byte
	gcsAddrs     map[string]*net.UDPAddr
}

func NewMAVLinkModule(cfg config.MAVLinkConfig, logger *logging.LogManager) *MAVLinkModule {
	return &MAVLinkModule{
		cfg:         cfg,
		logger:      logger,
		broadcastCh: make(chan []byte, 256),
		gcsAddrs:    make(map[string]*net.UDPAddr),
	}
}

func (m *MAVLinkModule) Name() string {
	return "mavlink"
}

func (m *MAVLinkModule) Start(ctx context.Context) error {
	m.logger.Log("mavlink", logging.LevelInfo, "Initializing Bi-Directional MAVLink Router & Packet Parser...")

	for _, ep := range m.cfg.UDPEndpoints {
		raddr, err := net.ResolveUDPAddr("udp", ep)
		if err != nil {
			m.logger.Log("mavlink", logging.LevelWarn, fmt.Sprintf("Invalid MAVLink UDP endpoint %s: %v", ep, err))
			continue
		}
		conn, err := net.DialUDP("udp", nil, raddr)
		if err != nil {
			m.logger.Log("mavlink", logging.LevelWarn, fmt.Sprintf("Failed to dial MAVLink UDP endpoint %s: %v", ep, err))
			continue
		}
		m.udpConns = append(m.udpConns, conn)
		m.logger.Log("mavlink", logging.LevelInfo, fmt.Sprintf("Bi-directional MAVLink link active on UDP endpoint %s", ep))
		go m.udpReadLoop(ctx, conn)
	}

	go m.udpBroadcastLoop(ctx)

	timeout := m.cfg.HeartbeatTimeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.Stop()
			return nil
		case <-ticker.C:
			m.checkOrScanLinks(ctx, timeout)
		}
	}
}

func (m *MAVLinkModule) udpReadLoop(ctx context.Context, conn *net.UDPConn) {
	buf := make([]byte, 2048)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}

		if n > 0 {
			if raddr != nil {
				m.mu.Lock()
				m.gcsAddrs[raddr.String()] = raddr
				m.mu.Unlock()
			}
			m.writeToSerial(buf[:n])
		}
	}
}

func (m *MAVLinkModule) writeToSerial(data []byte) {
	m.serialMu.Lock()
	defer m.serialMu.Unlock()

	m.mu.RLock()
	ser := m.activeSerial
	m.mu.RUnlock()

	if ser == nil {
		return
	}

	n, err := ser.Write(data)
	if err == nil && n > 0 {
		m.mu.Lock()
		m.packetsTx++
		m.status.PacketsTx = m.packetsTx
		m.mu.Unlock()
	}
}

func (m *MAVLinkModule) udpBroadcastLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case pkt, ok := <-m.broadcastCh:
			if !ok {
				return
			}
			m.mu.RLock()
			for _, conn := range m.udpConns {
				_, _ = conn.Write(pkt)
			}
			m.mu.RUnlock()
		}
	}
}

func (m *MAVLinkModule) checkOrScanLinks(ctx context.Context, timeout time.Duration) {
	m.mu.Lock()
	if m.status.Connected {
		if time.Since(m.status.LastHeartbeat) > timeout {
			m.status.Connected = false
			m.logger.RecordEvent("mavlink", "MAVLINK_DISCONNECT", fmt.Sprintf("MAVLink link timeout on %s", m.status.Port))
			if m.activeSerial != nil {
				_ = m.activeSerial.Close()
				m.activeSerial = nil
			}
		}
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	var candidates []string
	patterns := []string{
		"/dev/serial/by-id/*",
		"/dev/ttyACM*",
		"/dev/ttyUSB*",
		"/dev/ttyS*",
	}
	for _, pat := range patterns {
		matches, err := filepath.Glob(pat)
		if err == nil {
			candidates = append(candidates, matches...)
		}
	}

	for _, port := range candidates {
		for _, baud := range m.cfg.BaudRates {
			f, err := openSerialPort(port, baud)
			if err != nil {
				continue
			}

			m.logger.Log("mavlink", logging.LevelInfo, fmt.Sprintf("Opened serial candidate %s @ %d baud. Listening for MAVLink heartbeats...", port, baud))

			go m.handleSerialStream(ctx, f, port, baud)
			return
		}
	}
}

func (m *MAVLinkModule) handleSerialStream(ctx context.Context, rw io.ReadWriteCloser, port string, baud int) {
	defer func() {
		_ = rw.Close()
		m.mu.Lock()
		if m.serialPort == port {
			m.status.Connected = false
			m.activeSerial = nil
		}
		m.mu.Unlock()
	}()

	m.mu.Lock()
	m.activeSerial = rw
	m.serialPort = port
	m.mu.Unlock()

	buf := make([]byte, 2048)
	packetBuf := make([]byte, 0, 4096)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := rw.Read(buf)
		if err != nil {
			if err != io.EOF {
				m.logger.Log("mavlink", logging.LevelWarn, fmt.Sprintf("Serial read error on %s: %v", port, err))
			}
			break
		}

		if n > 0 {
			packetBuf = append(packetBuf, buf[:n]...)
			packetBuf = m.processMAVLinkBuffer(packetBuf, port, baud)
		}
	}
}

func (m *MAVLinkModule) processMAVLinkBuffer(buf []byte, port string, baud int) []byte {
	i := 0
	for i < len(buf) {
		magic := buf[i]
		if magic != MavlinkV1Magic && magic != MavlinkV2Magic {
			i++
			continue
		}

		var payloadLen int
		var headerLen int

		if magic == MavlinkV1Magic {
			if i+6 > len(buf) {
				break
			}
			payloadLen = int(buf[i+1])
			headerLen = 6
			totalLen := headerLen + payloadLen + 2
			if i+totalLen > len(buf) {
				break
			}

			sysID := buf[i+3]
			compID := buf[i+4]
			msgID := buf[i+5]

			packetData := buf[i : i+totalLen]
			m.onMAVLinkPacket(magic, sysID, compID, uint32(msgID), packetData, port, baud)
			i += totalLen
			continue

		} else if magic == MavlinkV2Magic {
			if i+10 > len(buf) {
				break
			}
			payloadLen = int(buf[i+1])
			headerLen = 10
			totalLen := headerLen + payloadLen + 2
			if i+totalLen > len(buf) {
				break
			}

			sysID := buf[i+5]
			compID := buf[i+6]
			msgID := uint32(buf[i+7]) | (uint32(buf[i+8]) << 8) | (uint32(buf[i+9]) << 16)

			packetData := buf[i : i+totalLen]
			m.onMAVLinkPacket(magic, sysID, compID, msgID, packetData, port, baud)
			i += totalLen
			continue
		}
	}

	if i < len(buf) {
		return buf[i:]
	}
	return buf[:0]
}

func (m *MAVLinkModule) onMAVLinkPacket(magic byte, sysID, compID uint8, msgID uint32, packet []byte, port string, baud int) {
	m.mu.Lock()
	m.packetsRx++
	now := time.Now()

	firstConnect := !m.status.Connected
	m.status.Connected = true
	m.status.Port = port
	m.status.BaudRate = baud
	m.status.SystemID = sysID
	m.status.ComponentID = compID
	m.status.LastHeartbeat = now
	m.status.PacketsRx = m.packetsRx
	m.mu.Unlock()

	if firstConnect {
		m.logger.RecordEvent("mavlink", "MAVLINK_CONNECT",
			fmt.Sprintf("Autopilot MAVLink v%d link established on %s @ %d baud (System ID: %d, Comp ID: %d)",
				map[byte]int{MavlinkV1Magic: 1, MavlinkV2Magic: 2}[magic], port, baud, sysID, compID))
	}

	select {
	case m.broadcastCh <- packet:
	default:
	}
}

func openSerialPort(port string, baud int) (io.ReadWriteCloser, error) {
	f, err := os.OpenFile(port, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (m *MAVLinkModule) GetStatus() LinkStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	st := m.status
	if st.Connected {
		st.HeartbeatAge = time.Since(st.LastHeartbeat).Truncate(time.Second).String()
	} else {
		st.HeartbeatAge = "N/A"
	}
	return st
}

func (m *MAVLinkModule) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.activeSerial != nil {
		_ = m.activeSerial.Close()
		m.activeSerial = nil
	}
	for _, conn := range m.udpConns {
		_ = conn.Close()
	}
	m.udpConns = nil
	m.status.Connected = false
	m.logger.Log("mavlink", logging.LevelInfo, "MAVLink router stopped")
	return nil
}

func (m *MAVLinkModule) Status() supervisor.ModuleStatus {
	return supervisor.ModuleStatus{
		Name:  m.Name(),
		State: supervisor.StateRunning,
	}
}

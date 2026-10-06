package mavlink

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"companion/pkg/config"
	"companion/pkg/logging"
	"companion/pkg/supervisor"
)

const (
	MavlinkV1Magic = 0xFE
	MavlinkV2Magic = 0xFD

	MsgIDHeartbeat = 0

	gcsPort = 14550

	peerTTL = 24 * time.Hour
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
	sock         *net.UDPConn
	activeSerial io.ReadWriteCloser
	serialPort   string
	packetsRx    uint64
	packetsTx    uint64

	static     []*net.UDPAddr
	peers      map[string]*peer
	bcast      []*net.UDPAddr
	localIPs   map[string]bool
	lastErrLog time.Time
}

type peer struct {
	addr     *net.UDPAddr
	lastSeen time.Time
}

func NewMAVLinkModule(cfg config.MAVLinkConfig, logger *logging.LogManager) *MAVLinkModule {
	return &MAVLinkModule{
		cfg:      cfg,
		logger:   logger,
		peers:    make(map[string]*peer),
		localIPs: make(map[string]bool),
	}
}

func (m *MAVLinkModule) Name() string {
	return "mavlink"
}

func (m *MAVLinkModule) Start(ctx context.Context) error {
	m.logger.Log("mavlink", logging.LevelInfo, "Initializing MAVLink router...")

	laddr := &net.UDPAddr{IP: net.IPv4zero, Port: gcsPort}
	sock, err := net.ListenUDP("udp4", laddr)
	if err != nil {
		m.logger.Log("mavlink", logging.LevelError, fmt.Sprintf("Cannot bind UDP port %d: %v", gcsPort, err))
		return fmt.Errorf("bind udp %d: %w", gcsPort, err)
	}
	m.mu.Lock()
	m.sock = sock
	m.mu.Unlock()

	for _, ep := range m.cfg.UDPEndpoints {
		raddr, err := net.ResolveUDPAddr("udp4", ep)
		if err != nil {
			m.logger.Log("mavlink", logging.LevelWarn, fmt.Sprintf("Invalid MAVLink UDP endpoint %s: %v", ep, err))
			continue
		}
		if raddr.IP.IsLoopback() || m.isLocalIP(raddr.IP) {
			continue
		}
		m.static = append(m.static, raddr)
	}

	m.refreshBroadcast()

	go m.udpReadLoop(ctx, sock)

	timeout := m.cfg.HeartbeatTimeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	tick := 0

	for {
		select {
		case <-ctx.Done():
			m.Stop()
			return nil
		case <-ticker.C:
			tick++
			m.prunePeers()
			if tick%5 == 0 {
				m.refreshBroadcast()
			}
			m.checkOrScanLinks(ctx, timeout)
		}
	}
}

func (m *MAVLinkModule) isLocalIP(ip net.IP) bool {
	if ip.IsLoopback() {
		return true
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.localIPs[ip4.String()]
}

func (m *MAVLinkModule) refreshBroadcast() {
	var out []*net.UDPAddr
	localMap := make(map[string]bool)
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, ifc := range ifaces {
			if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := ifc.Addrs()
			if err != nil {
				continue
			}
			for _, a := range addrs {
				ipn, ok := a.(*net.IPNet)
				if !ok {
					continue
				}
				ip4 := ipn.IP.To4()
				if ip4 != nil {
					localMap[ip4.String()] = true
				}
				mask := ipn.Mask
				if ip4 == nil || len(mask) != net.IPv4len {
					continue
				}
				ones, bits := mask.Size()
				if bits != 32 || ones >= 31 {
					continue
				}
				b := make(net.IP, 4)
				for i := 0; i < 4; i++ {
					b[i] = ip4[i] | ^mask[i]
				}
				out = append(out, &net.UDPAddr{IP: b, Port: gcsPort})
			}
		}
	}

	m.mu.Lock()
	m.bcast = out
	m.localIPs = localMap
	m.mu.Unlock()
}

func (m *MAVLinkModule) prunePeers() {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, p := range m.peers {
		if now.Sub(p.lastSeen) > peerTTL {
			delete(m.peers, k)
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

		if n > 0 && raddr != nil {
			if m.isLocalIP(raddr.IP) {
				continue
			}
			key := raddr.String()
			now := time.Now()
			m.mu.Lock()
			p, known := m.peers[key]
			if !known {
				p = &peer{addr: raddr}
				m.peers[key] = p
				m.logger.RecordEvent("mavlink", "GCS_CONNECT", fmt.Sprintf("Ground station connected from %s", key))
			}
			p.lastSeen = now
			m.mu.Unlock()

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

func (m *MAVLinkModule) sendUDP(data []byte) {
	m.mu.RLock()
	sock := m.sock
	if sock == nil {
		m.mu.RUnlock()
		return
	}

	dests := make([]*net.UDPAddr, 0, len(m.static)+len(m.peers)+len(m.bcast))
	dests = append(dests, m.static...)
	for _, p := range m.peers {
		dests = append(dests, p.addr)
	}

	if len(m.peers) == 0 && len(m.static) == 0 {
		dests = append(dests, m.bcast...)
	}
	m.mu.RUnlock()

	for _, d := range dests {
		if _, err := sock.WriteToUDP(data, d); err != nil {
			m.logSendErr(d, err)
		}
	}
}

func (m *MAVLinkModule) logSendErr(d *net.UDPAddr, err error) {
	m.mu.Lock()
	due := time.Since(m.lastErrLog) > 10*time.Second
	if due {
		m.lastErrLog = time.Now()
	}
	m.mu.Unlock()
	if due {
		m.logger.Log("mavlink", logging.LevelWarn, fmt.Sprintf("UDP send to %s failed: %v", d, err))
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
		"/dev/ttyAMA*",
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

			hb := []byte{0xFE, 0x09, 0x00, 0xFF, 0xBE, 0x00, 0x00, 0x00, 0x00, 0x00, 0x06, 0x08, 0x00, 0x00, 0x03, 0x0A, 0x76}
			_, _ = f.Write(hb)

			packetBuf, ok := m.probeSerialForMAVLink(ctx, f)
			if !ok {
				_ = f.Close()
				continue
			}

			m.logger.Log("mavlink", logging.LevelInfo, fmt.Sprintf("Detected MAVLink stream on %s @ %d baud", port, baud))
			go m.handleSerialStream(ctx, f, port, baud, packetBuf)
			return
		}
	}
}

func (m *MAVLinkModule) probeSerialForMAVLink(ctx context.Context, f io.ReadWriteCloser) ([]byte, bool) {
	probeStart := time.Now()
	buf := make([]byte, 512)
	packetBuf := make([]byte, 0, 2048)

	for time.Since(probeStart) < 2*time.Second {
		select {
		case <-ctx.Done():
			return nil, false
		default:
		}

		n, err := f.Read(buf)
		if err != nil && err != io.EOF {
			return nil, false
		}
		if n > 0 {
			packetBuf = append(packetBuf, buf[:n]...)
			for i := 0; i < len(packetBuf); i++ {
				if packetBuf[i] == MavlinkV1Magic || packetBuf[i] == MavlinkV2Magic {
					return packetBuf, true
				}
			}
		}
	}
	return nil, false
}

func (m *MAVLinkModule) handleSerialStream(ctx context.Context, rw io.ReadWriteCloser, port string, baud int, initialBuf []byte) {
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

	if len(initialBuf) > 0 {
		m.sendUDP(initialBuf)
		packetBuf = append(packetBuf, initialBuf...)
		packetBuf = m.processMAVLinkBuffer(packetBuf, port, baud)
	}

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
			m.sendUDP(buf[:n])
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
			if buf[i+2]&0x01 != 0 {
				totalLen += 13
			}
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
}

func openSerialPort(port string, baud int) (io.ReadWriteCloser, error) {
	f, err := os.OpenFile(port, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}

	fd := f.Fd()

	var termios syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&termios))); errno == 0 {
		termios.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON | syscall.IXOFF
		termios.Oflag &^= syscall.OPOST
		termios.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN

		baudConst := uint32(syscall.B115200)
		switch baud {
		case 57600:
			baudConst = syscall.B57600
		case 115200:
			baudConst = syscall.B115200
		case 230400:
			baudConst = syscall.B230400
		case 460800:
			baudConst = syscall.B460800
		case 921600:
			baudConst = syscall.B921600
		}

		termios.Cflag &^= syscall.CSIZE | syscall.PARENB | 07777
		termios.Cflag |= syscall.CS8 | syscall.CLOCAL | syscall.CREAD | baudConst
		termios.Ispeed = baudConst
		termios.Ospeed = baudConst

		termios.Cc[syscall.VMIN] = 0
		termios.Cc[syscall.VTIME] = 5

		_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(&termios)))
	}

	bits := int(syscall.TIOCM_DTR | syscall.TIOCM_RTS)
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCMBIS), uintptr(unsafe.Pointer(&bits)))

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
	if m.sock != nil {
		_ = m.sock.Close()
		m.sock = nil
	}
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

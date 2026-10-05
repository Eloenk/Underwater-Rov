package video

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"companion/pkg/config"
	"companion/pkg/logging"
	"companion/pkg/supervisor"
)

type Format struct {
	FourCC string `json:"fourcc"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	MaxFPS int    `json:"max_fps"`
}

type DeviceInfo struct {
	Path        string   `json:"path"`
	USBPath     string   `json:"usb_path"`
	Name        string   `json:"name"`
	Formats     []Format `json:"formats"`
	Width       int      `json:"width"`
	Height      int      `json:"height"`
	FPS         int      `json:"fps"`
	Quality     int      `json:"quality"`
	Brightness  int      `json:"brightness"`
	Contrast    int      `json:"contrast"`
	HFlip       bool     `json:"hflip"`
	VFlip       bool     `json:"vflip"`
	IsStreaming bool     `json:"is_streaming"`
}

type rtspClient struct {
	conn       net.Conn
	isTCP      bool
	udpAddr    *net.UDPAddr
	clientPort string
}

type VideoModule struct {
	cfg          config.VideoConfig
	logger       *logging.LogManager
	mu           sync.RWMutex
	devices      map[string]*DeviceInfo
	activeDev    string
	server       *http.Server
	subscribers  map[chan []byte]struct{}
	rtspClients  map[string]*rtspClient
	rtspSeq      uint16
	rtspTS       uint32
	listener     net.Listener
	activeWidth  int
	activeHeight int
	activeFPS    int
	activeQual   int
	brightness   int
	contrast     int
	hflip        bool
	vflip        bool
}

func NewVideoModule(cfg config.VideoConfig, logger *logging.LogManager) *VideoModule {
	return &VideoModule{
		cfg:         cfg,
		logger:      logger,
		devices:     make(map[string]*DeviceInfo),
		subscribers: make(map[chan []byte]struct{}),
		rtspClients: make(map[string]*rtspClient),
		activeWidth: 1280,
		activeHeight: 720,
		activeFPS:   30,
		activeQual:  85,
		brightness:  128,
		contrast:    128,
		hflip:       false,
		vflip:       false,
	}
}

func (v *VideoModule) Name() string {
	return "video"
}

func (v *VideoModule) Start(ctx context.Context) error {
	v.logger.Log("video", logging.LevelInfo, "Starting Video module & V4L2 hardware camera pipeline...")

	v.scanDevices()

	go v.watchHotplug(ctx)

	port := v.cfg.StreamPort
	if port <= 0 {
		port = 8081
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/stream", v.handleMJPEGStream)
	mux.HandleFunc("/devices", v.handleDevicesList)
	mux.HandleFunc("/config", v.handleConfigUpdate)

	serverAddr := fmt.Sprintf("0.0.0.0:%d", port)
	v.server = &http.Server{
		Addr:    serverAddr,
		Handler: mux,
	}

	ln, err := net.Listen("tcp", serverAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on video stream port %d: %w", port, err)
	}
	v.listener = ln

	v.logger.RecordEvent("video", "STREAM_START", fmt.Sprintf("HTTP MJPEG video stream listening on http://%s/stream", serverAddr))

	go func() {
		if err := v.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			v.logger.Log("video", logging.LevelError, fmt.Sprintf("Stream server error: %v", err))
		}
	}()

	go v.startRTSPServer(ctx, 8554)

	go v.frameProducerLoop(ctx)

	<-ctx.Done()
	return v.Stop()
}

func (v *VideoModule) watchHotplug(ctx context.Context) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		v.logger.Log("video", logging.LevelWarn, fmt.Sprintf("fsnotify watcher init failed: %v", err))
		return
	}
	defer watcher.Close()

	_ = watcher.Add("/dev")

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			base := filepath.Base(ev.Name)
			if len(base) > 5 && base[:5] == "video" {
				if ev.Op&fsnotify.Create != 0 {
					v.logger.RecordEvent("video", "CAMERA_ATTACH", fmt.Sprintf("V4L2 camera attached: %s", ev.Name))
					v.scanDevices()
				} else if ev.Op&fsnotify.Remove != 0 {
					v.logger.RecordEvent("video", "CAMERA_DETACH", fmt.Sprintf("V4L2 camera detached: %s", ev.Name))
					v.scanDevices()
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			v.logger.Log("video", logging.LevelWarn, fmt.Sprintf("fsnotify error: %v", err))
		}
	}
}

func (v *VideoModule) scanDevices() {
	v.mu.Lock()
	defer v.mu.Unlock()

	matches, err := filepath.Glob("/dev/video*")
	if err != nil {
		return
	}

	currentSet := make(map[string]bool)
	for _, m := range matches {
		currentSet[m] = true
	}

	for devPath := range v.devices {
		if !currentSet[devPath] {
			delete(v.devices, devPath)
		}
	}

	for _, devPath := range matches {
		if _, exists := v.devices[devPath]; exists {
			continue
		}

		usbPath, err := resolveUSBPath(devPath)
		if err != nil {
			usbPath = devPath
		}
		name, formats, err := queryV4L2Device(devPath)
		if err != nil || len(formats) == 0 {
			continue
		}

		v.devices[devPath] = &DeviceInfo{
			Path:        devPath,
			USBPath:     usbPath,
			Name:        name,
			Formats:     formats,
			Width:       v.activeWidth,
			Height:      v.activeHeight,
			FPS:         v.activeFPS,
			Quality:     v.activeQual,
			Brightness:  v.brightness,
			Contrast:    v.contrast,
			HFlip:       v.hflip,
			VFlip:       v.vflip,
			IsStreaming: true,
		}
	}
}

func (v *VideoModule) ConfigureDevice(devPath string, width, height, fps, quality, brightness, contrast int, hflip, vflip bool) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if width > 0 {
		v.activeWidth = width
	}
	if height > 0 {
		v.activeHeight = height
	}
	if fps > 0 {
		v.activeFPS = fps
	}
	if quality >= 1 && quality <= 100 {
		v.activeQual = quality
	}
	if brightness >= 0 {
		v.brightness = brightness
	}
	if contrast >= 0 {
		v.contrast = contrast
	}
	v.hflip = hflip
	v.vflip = vflip

	for _, d := range v.devices {
		if devPath == "" || d.Path == devPath {
			d.Width = v.activeWidth
			d.Height = v.activeHeight
			d.FPS = v.activeFPS
			d.Quality = v.activeQual
			d.Brightness = v.brightness
			d.Contrast = v.contrast
			d.HFlip = v.hflip
			d.VFlip = v.vflip
		}
	}

	v.logger.RecordEvent("video", "CAMERA_CONFIG_UPDATE",
		fmt.Sprintf("Camera settings updated: %dx%d @ %d FPS, JPEG Quality: %d%%, Brightness: %d, Contrast: %d, HFlip: %v, VFlip: %v",
			v.activeWidth, v.activeHeight, v.activeFPS, v.activeQual, v.brightness, v.contrast, v.hflip, v.vflip))
}

func (v *VideoModule) frameProducerLoop(ctx context.Context) {
	var cam *V4L2Camera
	defer func() {
		if cam != nil {
			cam.Close()
		}
	}()

	errCount := 0

	for {
		select {
		case <-ctx.Done():
			return
		default:
			v.mu.RLock()
			targetDev := v.activeDev
			if targetDev == "" {
				targetDev = v.cfg.DefaultDevice
			}
			if targetDev == "" || v.devices[targetDev] == nil {
				for p := range v.devices {
					targetDev = p
					break
				}
			}
			if targetDev == "" {
				targetDev = "/dev/video0"
			}
			w := v.activeWidth
			h := v.activeHeight
			fps := v.activeFPS
			b := v.brightness
			c := v.contrast
			hf := v.hflip
			vf := v.vflip
			v.mu.RUnlock()

			if cam == nil {
				var err error
				cam, err = OpenV4L2Camera(targetDev, w, h, fps)
				if err != nil {
					time.Sleep(1 * time.Second)
					continue
				}
				errCount = 0
				_ = cam.SetControl(v4l2CidBrightness, int32(b))
				_ = cam.SetControl(v4l2CidContrast, int32(c))
				var hval int32
				if hf {
					hval = 1
				}
				var vval int32
				if vf {
					vval = 1
				}
				_ = cam.SetControl(v4l2CidHFlip, hval)
				_ = cam.SetControl(v4l2CidVFlip, vval)
			}

			frameJPEG, err := cam.ReadFrame()
			if err != nil || len(frameJPEG) == 0 {
				errCount++
				if errCount < 3 {
					time.Sleep(10 * time.Millisecond)
					continue
				}
				errCount = 0
				cam.Close()
				cam = nil
				time.Sleep(100 * time.Millisecond)
				continue
			}
			errCount = 0

			v.mu.RLock()
			for ch := range v.subscribers {
				select {
				case ch <- frameJPEG:
				default:
				}
			}
			v.mu.RUnlock()

			v.broadcastRTPFrame(frameJPEG, w, h)
		}
	}
}

func (v *VideoModule) broadcastRTPFrame(frame []byte, w, h int) {
	if len(frame) < 4 {
		return
	}

	v.mu.RLock()
	if len(v.rtspClients) == 0 {
		v.mu.RUnlock()
		return
	}
	clients := make([]*rtspClient, 0, len(v.rtspClients))
	for _, c := range v.rtspClients {
		clients = append(clients, c)
	}
	v.mu.RUnlock()

	startIdx := 0
	if frame[0] == 0xFF && frame[1] == 0xD8 {
		startIdx = 2
	}
	endIdx := len(frame)
	if endIdx > 2 && frame[endIdx-2] == 0xFF && frame[endIdx-1] == 0xD9 {
		endIdx -= 2
	}

	payload := frame[startIdx:endIdx]
	maxPayloadSize := 1400

	numPkts := (len(payload) + maxPayloadSize - 1) / maxPayloadSize
	for i := 0; i < numPkts; i++ {
		offset := i * maxPayloadSize
		chunkLen := maxPayloadSize
		if offset+chunkLen > len(payload) {
			chunkLen = len(payload) - offset
		}

		isLast := (i == numPkts-1)

		pkt := make([]byte, 12+8+chunkLen)

		pkt[0] = 0x80
		if isLast {
			pkt[1] = 0x80 | 26
		} else {
			pkt[1] = 26
		}

		v.rtspSeq++
		pkt[2] = byte(v.rtspSeq >> 8)
		pkt[3] = byte(v.rtspSeq)

		v.rtspTS += 3000
		pkt[4] = byte(v.rtspTS >> 24)
		pkt[5] = byte(v.rtspTS >> 16)
		pkt[6] = byte(v.rtspTS >> 8)
		pkt[7] = byte(v.rtspTS)

		pkt[8] = 0x12
		pkt[9] = 0x34
		pkt[10] = 0x56
		pkt[11] = 0x78

		pkt[12] = 0
		pkt[13] = byte(offset >> 16)
		pkt[14] = byte(offset >> 8)
		pkt[15] = byte(offset)
		pkt[16] = 1
		pkt[17] = 255
		pkt[18] = byte(w / 8)
		pkt[19] = byte(h / 8)

		copy(pkt[20:], payload[offset:offset+chunkLen])

		for _, client := range clients {
			if client.isTCP {
				header := []byte{0x24, 0x00, byte(len(pkt) >> 8), byte(len(pkt))}
				_ = client.conn.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
				_, _ = client.conn.Write(append(header, pkt...))
			} else if client.udpAddr != nil {
				conn, err := net.DialUDP("udp", nil, client.udpAddr)
				if err == nil {
					_ = conn.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
					_, _ = conn.Write(pkt)
					_ = conn.Close()
				}
			}
		}
	}
}

func (v *VideoModule) startRTSPServer(ctx context.Context, port int) {
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		v.logger.Log("video", logging.LevelWarn, fmt.Sprintf("RTSP server listen error on port %d: %v", port, err))
		return
	}
	v.logger.RecordEvent("video", "RTSP_START", fmt.Sprintf("RTSP video stream server active on rtsp://0.0.0.0:%d/live", port))

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				continue
			}
		}
		go v.handleRTSPClient(ctx, conn)
	}
}

func (v *VideoModule) handleRTSPClient(ctx context.Context, conn net.Conn) {
	reader := bufio.NewReader(conn)
	remoteKey := conn.RemoteAddr().String()
	var client *rtspClient

	defer func() {
		v.mu.Lock()
		delete(v.rtspClients, remoteKey)
		v.mu.Unlock()
		conn.Close()
	}()

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Split(line, " ")
		if len(parts) < 2 {
			continue
		}
		method := parts[0]

		cseq := "1"
		transportHeader := ""
		for {
			hLine, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			hLine = strings.TrimSpace(hLine)
			if hLine == "" {
				break
			}
			if strings.HasPrefix(hLine, "CSeq:") {
				cseq = strings.TrimSpace(strings.TrimPrefix(hLine, "CSeq:"))
			} else if strings.HasPrefix(hLine, "Transport:") {
				transportHeader = strings.TrimSpace(strings.TrimPrefix(hLine, "Transport:"))
			}
		}

		switch method {
		case "OPTIONS":
			resp := fmt.Sprintf("RTSP/1.0 200 OK\r\nCSeq: %s\r\nPublic: OPTIONS, DESCRIBE, SETUP, PLAY, TEARDOWN\r\n\r\n", cseq)
			_, _ = conn.Write([]byte(resp))

		case "DESCRIBE":
			sdp := "v=0\r\no=- 0 0 IN IP4 0.0.0.0\r\ns=ROV Video Stream\r\nc=IN IP4 0.0.0.0\r\nt=0 0\r\nm=video 0 RTP/AVP 26\r\na=rtpmap:26 JPEG/90000\r\na=control:streamid=0\r\n"
			resp := fmt.Sprintf("RTSP/1.0 200 OK\r\nCSeq: %s\r\nContent-Type: application/sdp\r\nContent-Length: %d\r\n\r\n%s", cseq, len(sdp), sdp)
			_, _ = conn.Write([]byte(resp))

		case "SETUP":
			client = &rtspClient{conn: conn}
			transportResp := ""

			if strings.Contains(transportHeader, "TCP") || strings.Contains(transportHeader, "interleaved") {
				client.isTCP = true
				transportResp = "RTP/AVP/TCP;unicast;interleaved=0-1"
			} else {
				client.isTCP = false
				clientPort := "5000-5001"
				if idx := strings.Index(transportHeader, "client_port="); idx != -1 {
					pStr := transportHeader[idx+len("client_port="):]
					if pEnd := strings.IndexAny(pStr, ";\r\n"); pEnd != -1 {
						pStr = pStr[:pEnd]
					}
					clientPort = pStr
				}
				client.clientPort = clientPort

				portStr := clientPort
				if dashIdx := strings.Index(portStr, "-"); dashIdx != -1 {
					portStr = portStr[:dashIdx]
				}
				if rtpPort, err := strconv.Atoi(portStr); err == nil {
					host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
					if addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", host, rtpPort)); err == nil {
						client.udpAddr = addr
					}
				}
				transportResp = fmt.Sprintf("RTP/AVP;unicast;client_port=%s;server_port=8554-8555", clientPort)
			}

			resp := fmt.Sprintf("RTSP/1.0 200 OK\r\nCSeq: %s\r\nTransport: %s\r\nSession: 12345678\r\n\r\n", cseq, transportResp)
			_, _ = conn.Write([]byte(resp))

		case "PLAY":
			if client != nil {
				v.mu.Lock()
				v.rtspClients[remoteKey] = client
				v.mu.Unlock()
			}
			resp := fmt.Sprintf("RTSP/1.0 200 OK\r\nCSeq: %s\r\nSession: 12345678\r\n\r\n", cseq)
			_, _ = conn.Write([]byte(resp))

		case "TEARDOWN":
			v.mu.Lock()
			delete(v.rtspClients, remoteKey)
			v.mu.Unlock()
			resp := fmt.Sprintf("RTSP/1.0 200 OK\r\nCSeq: %s\r\n\r\n", cseq)
			_, _ = conn.Write([]byte(resp))
			return
		}
	}
}

func (v *VideoModule) handleMJPEGStream(w http.ResponseWriter, r *http.Request) {
	mimeWriter := multipart.NewWriter(w)
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+mimeWriter.Boundary())
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Connection", "close")

	ch := make(chan []byte, 10)
	v.mu.Lock()
	v.subscribers[ch] = struct{}{}
	v.mu.Unlock()

	defer func() {
		v.mu.Lock()
		delete(v.subscribers, ch)
		v.mu.Unlock()
		close(ch)
	}()

	for {
		select {
		case <-r.Context().Done():
			return
		case frame, ok := <-ch:
			if !ok {
				return
			}
			header := make(textproto.MIMEHeader)
			header.Set("Content-Type", "image/jpeg")
			header.Set("Content-Length", fmt.Sprintf("%d", len(frame)))

			partWriter, err := mimeWriter.CreatePart(header)
			if err != nil {
				return
			}
			if _, err := partWriter.Write(frame); err != nil {
				return
			}
		}
	}
}

func (v *VideoModule) handleDevicesList(w http.ResponseWriter, r *http.Request) {
	v.scanDevices()
	v.mu.RLock()
	defer v.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	var list []DeviceInfo
	for _, dev := range v.devices {
		list = append(list, *dev)
	}
	_ = json.NewEncoder(w).Encode(list)
}

func (v *VideoModule) handleConfigUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Path       string `json:"path"`
		Width      int    `json:"width"`
		Height     int    `json:"height"`
		FPS        int    `json:"fps"`
		Quality    int    `json:"quality"`
		Brightness int    `json:"brightness"`
		Contrast   int    `json:"contrast"`
		HFlip      bool   `json:"hflip"`
		VFlip      bool   `json:"vflip"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	v.ConfigureDevice(req.Path, req.Width, req.Height, req.FPS, req.Quality, req.Brightness, req.Contrast, req.HFlip, req.VFlip)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (v *VideoModule) Stop() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = v.server.Shutdown(ctx)
		v.server = nil
	}
	v.logger.Log("video", logging.LevelInfo, "Video module stopped cleanly")
	return nil
}

func (v *VideoModule) Status() supervisor.ModuleStatus {
	return supervisor.ModuleStatus{
		Name:  v.Name(),
		State: supervisor.StateRunning,
	}
}

func (v *VideoModule) GetDevices() []DeviceInfo {
	v.scanDevices()
	v.mu.RLock()
	defer v.mu.RUnlock()

	var list []DeviceInfo
	for _, dev := range v.devices {
		list = append(list, *dev)
	}
	return list
}

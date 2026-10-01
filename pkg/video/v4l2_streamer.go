package video

import (
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"path/filepath"
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
	IsStreaming bool     `json:"is_streaming"`
}

type VideoModule struct {
	cfg          config.VideoConfig
	logger       *logging.LogManager
	mu           sync.RWMutex
	devices      map[string]*DeviceInfo
	activeDev    string
	server       *http.Server
	subscribers  map[chan []byte]struct{}
	listener     net.Listener
	activeWidth  int
	activeHeight int
	activeFPS    int
	activeQual   int
	brightness   int
	contrast     int
}

func NewVideoModule(cfg config.VideoConfig, logger *logging.LogManager) *VideoModule {
	return &VideoModule{
		cfg:          cfg,
		logger:       logger,
		devices:      make(map[string]*DeviceInfo),
		subscribers:  make(map[chan []byte]struct{}),
		activeWidth:  1280,
		activeHeight: 720,
		activeFPS:    30,
		activeQual:   85,
		brightness:   128,
		contrast:     128,
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

	v.devices = make(map[string]*DeviceInfo)

	matches, err := filepath.Glob("/dev/video*")
	if err != nil {
		return
	}

	for _, devPath := range matches {
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
			IsStreaming: true,
		}
	}

	v.logger.Log("video", logging.LevelInfo, fmt.Sprintf("Discovered %d active V4L2 video devices", len(v.devices)))
}

func (v *VideoModule) ConfigureDevice(devPath string, width, height, fps, quality, brightness, contrast int) {
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

	for _, d := range v.devices {
		if devPath == "" || d.Path == devPath {
			d.Width = v.activeWidth
			d.Height = v.activeHeight
			d.FPS = v.activeFPS
			d.Quality = v.activeQual
			d.Brightness = v.brightness
			d.Contrast = v.contrast
		}
	}

	v.logger.RecordEvent("video", "CAMERA_CONFIG_UPDATE",
		fmt.Sprintf("Camera settings updated: %dx%d @ %d FPS, JPEG Quality: %d%%, Brightness: %d, Contrast: %d",
			v.activeWidth, v.activeHeight, v.activeFPS, v.activeQual, v.brightness, v.contrast))
}

func (v *VideoModule) frameProducerLoop(ctx context.Context) {
	targetDev := v.cfg.DefaultDevice
	if targetDev == "" {
		targetDev = "/dev/video0"
	}

	var cam *V4L2Camera
	defer func() {
		if cam != nil {
			cam.Close()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
			v.mu.RLock()
			w := v.activeWidth
			h := v.activeHeight
			fps := v.activeFPS
			v.mu.RUnlock()

			if cam == nil {
				var err error
				cam, err = OpenV4L2Camera(targetDev, w, h, fps)
				if err != nil {
					time.Sleep(1 * time.Second)
					continue
				}
			}

			frameJPEG, err := cam.ReadFrame()
			if err != nil || len(frameJPEG) == 0 {
				cam.Close()
				cam = nil
				time.Sleep(100 * time.Millisecond)
				continue
			}

			v.mu.RLock()
			for ch := range v.subscribers {
				select {
				case ch <- frameJPEG:
				default:
				}
			}
			v.mu.RUnlock()
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
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	v.ConfigureDevice(req.Path, req.Width, req.Height, req.FPS, req.Quality, req.Brightness, req.Contrast)

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



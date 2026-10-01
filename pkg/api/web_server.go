package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"companion/pkg/config"
	"companion/pkg/health"
	"companion/pkg/logging"
	"companion/pkg/mavlink"
	"companion/pkg/supervisor"
	"companion/pkg/video"
)

type Server struct {
	cfg        config.ServerConfig
	sup        *supervisor.Supervisor
	logger     *logging.LogManager
	healthMod  *health.HealthModule
	videoMod   *video.VideoModule
	mavlinkMod *mavlink.MAVLinkModule
	httpServer *http.Server
	listener   net.Listener
	mu         sync.Mutex
}

func NewServer(
	cfg config.ServerConfig,
	sup *supervisor.Supervisor,
	logger *logging.LogManager,
	healthMod *health.HealthModule,
	videoMod *video.VideoModule,
	mavlinkMod *mavlink.MAVLinkModule,
) *Server {
	return &Server{
		cfg:        cfg,
		sup:        sup,
		logger:     logger,
		healthMod:  healthMod,
		videoMod:   videoMod,
		mavlinkMod: mavlinkMod,
	}
}

func (s *Server) Start(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	if s.cfg.Port <= 0 {
		addr = "0.0.0.0:8080"
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/modules", s.handleModules)
	mux.HandleFunc("/api/v1/modules/action", s.handleModuleAction)
	mux.HandleFunc("/api/v1/cameras", s.handleCameras)
	mux.HandleFunc("/api/v1/cameras/config", s.handleCameraConfig)
	mux.HandleFunc("/api/v1/mavlink", s.handleMAVLink)
	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/api/v1/logs", s.handleLogs)
	mux.HandleFunc("/api/v1/events", s.handleEvents)
	mux.HandleFunc("/api/v1/system/reboot", s.handleReboot)
	mux.HandleFunc("/api/v1/system/shutdown", s.handleShutdown)

	mux.HandleFunc("/", s.handleDashboard)

	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to start REST API on %s: %w", addr, err)
	}
	s.listener = ln

	s.logger.RecordEvent("api", "SERVER_START", fmt.Sprintf("Web UI & REST API listening on http://%s", addr))

	go func() {
		if err := s.httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.logger.Log("api", logging.LevelError, fmt.Sprintf("API server error: %v", err))
		}
	}()

	<-ctx.Done()
	return s.Stop()
}

func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(ctx)
		s.httpServer = nil
	}
	return nil
}

func (s *Server) handleModules(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	status := s.sup.GetStatus()
	_ = json.NewEncoder(w).Encode(status)
}

func (s *Server) handleModuleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	module := r.URL.Query().Get("module")
	action := r.URL.Query().Get("action")

	var err error
	switch action {
	case "start":
		err = s.sup.StartModule(module)
	case "stop":
		err = s.sup.StopModule(module)
	case "restart":
		err = s.sup.RestartModule(module)
	default:
		http.Error(w, "Invalid action (use start, stop, restart)", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "action": action, "module": module})
}

func (s *Server) handleCameras(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	devs := s.videoMod.GetDevices()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"devices": devs,
		"stream":  "http://0.0.0.0:8081/stream",
	})
}

func (s *Server) handleCameraConfig(w http.ResponseWriter, r *http.Request) {
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

	s.videoMod.ConfigureDevice(req.Path, req.Width, req.Height, req.FPS, req.Quality, req.Brightness, req.Contrast)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleMAVLink(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	st := s.mavlinkMod.GetStatus()
	_ = json.NewEncoder(w).Encode(st)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	st := s.healthMod.GetStats()
	_ = json.NewEncoder(w).Encode(st)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	nStr := r.URL.Query().Get("n")
	n := 100
	if nStr != "" {
		if val, err := strconv.Atoi(nStr); err == nil {
			n = val
		}
	}
	mod := r.URL.Query().Get("module")
	var entries []logging.LogEntry
	if mod != "" {
		entries = s.logger.TailModule(mod, n)
	} else {
		entries = s.logger.TailGlobal(n)
	}
	_ = json.NewEncoder(w).Encode(entries)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	evs := s.logger.GetEvents()
	_ = json.NewEncoder(w).Encode(evs)
}

func (s *Server) handleReboot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "rebooting"})
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = s.healthMod.TriggerReboot()
	}()
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "shutting_down"})
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = s.healthMod.TriggerShutdown()
	}()
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(embeddedHTML))
}

const embeddedHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>ROV Companion Computer</title>
    <style>
        :root {
            --bg: #f6f7f9;
            --surface: #ffffff;
            --inset: #f8f9fa;
            --line: #e4e7ec;
            --line-soft: #f3f5f7;
            --text: #1f2328;
            --body: #333a42;
            --dim: #6a737d;
            --faint: #8b949e;
            --accent: #0969da;
            --sidebar-bg: #0969da;
            --green: #1a7f37;
            --red: #d1242f;
        }
        * { box-sizing: border-box; }
        html { scroll-behavior: smooth; }
        body {
            margin: 0;
            font-family: -apple-system, 'Segoe UI', Inter, Roboto, sans-serif;
            background: var(--bg);
            color: var(--body);
            font-size: 14px;
            font-variant-numeric: tabular-nums;
            display: flex;
            min-height: 100vh;
        }
        .sidebar {
            width: 230px;
            flex-shrink: 0;
            background: var(--sidebar-bg);
            color: #ffffff;
            padding: 18px 12px;
            position: sticky;
            top: 0;
            height: 100vh;
            overflow-y: auto;
            box-shadow: 2px 0 8px rgba(0,0,0,0.05);
        }
        .brand {
            font-size: 14px;
            font-weight: 700;
            color: #ffffff;
            padding: 6px 10px 16px;
            border-bottom: 1px solid rgba(255,255,255,0.2);
            margin-bottom: 6px;
        }
        .brand span { display: block; font-weight: 400; font-size: 11px; color: #bfdbfe; margin-top: 2px; }
        .nav-label {
            font-size: 10px;
            font-weight: 700;
            letter-spacing: 0.08em;
            text-transform: uppercase;
            color: #93c5fd;
            padding: 14px 10px 5px;
        }
        .nav a {
            display: flex;
            align-items: center;
            padding: 8px 10px;
            border-radius: 7px;
            color: #e0f2fe;
            font-size: 13px;
            text-decoration: none;
            cursor: pointer;
            transition: background 0.15s ease, color 0.15s ease;
        }
        .nav a:hover { background: rgba(255, 255, 255, 0.15); color: #ffffff; }
        .nav a.on { background: rgba(255, 255, 255, 0.25); color: #ffffff; font-weight: 600; }
        .main { flex: 1; padding: 22px 26px; min-width: 0; }
        .topbar {
            display: flex;
            justify-content: space-between;
            align-items: center;
            flex-wrap: wrap;
            gap: 12px;
            margin-bottom: 18px;
        }
        .topbar h1 { font-size: 17px; font-weight: 700; margin: 0; color: var(--text); }
        .topmeta { display: flex; gap: 14px; align-items: center; font-size: 12.5px; color: var(--dim); }
        .topmeta b { color: var(--text); font-weight: 600; }
        
        .pill {
            display: inline-flex;
            align-items: center;
            font-size: 11px;
            font-weight: 700;
            padding: 4px 12px;
            border-radius: 6px;
            background: var(--green);
            color: #ffffff;
            letter-spacing: 0.03em;
        }
        .pill.lost { background: var(--red); color: #ffffff; }

        .grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(340px, 1fr));
            gap: 14px;
        }
        .card {
            background: var(--surface);
            border: 1px solid var(--line);
            border-radius: 10px;
            padding: 16px 18px;
            scroll-margin-top: 20px;
        }
        .card h3 {
            margin: 0 0 12px 0;
            font-size: 11px;
            font-weight: 700;
            letter-spacing: 0.07em;
            text-transform: uppercase;
            color: var(--dim);
        }
        table { width: 100%; border-collapse: collapse; font-size: 13px; }
        th {
            text-align: left;
            font-size: 11px;
            font-weight: 600;
            color: var(--faint);
            padding: 6px 8px;
            border-bottom: 1px solid var(--line-soft);
        }
        td { padding: 9px 8px; border-bottom: 1px solid var(--line-soft); }
        tr:last-child td { border-bottom: none; }
        .btn {
            font: inherit; font-size: 12px; font-weight: 600;
            color: #1e293b; background: var(--surface);
            border: 1px solid #cbd5e1; border-radius: 7px;
            padding: 6px 12px; cursor: pointer;
            transition: background 0.15s ease;
        }
        .btn:hover { background: #f1f5f9; }
        .btn-icon {
            display: inline-flex; align-items: center; justify-content: center;
            width: 32px; height: 32px; border-radius: 6px; border: 1px solid var(--line);
            background: var(--surface); cursor: pointer; font-size: 14px;
            transition: all 0.15s ease;
        }
        .btn-icon:hover { background: #f1f5f9; border-color: var(--accent); }
        .btn-icon.active { background: var(--accent); color: #ffffff; border-color: var(--accent); }
        .btn-danger { color: #ffffff; background: var(--red); border: none; }
        .btn-danger:hover { background: #b91c1c; }
        .btn-primary { color: #ffffff; background: var(--accent); border: none; }
        .btn-primary:hover { background: #1d4ed8; }
        
        .cam-widget {
            background: var(--surface);
            border: 1px solid var(--line);
            border-radius: 10px;
            padding: 16px;
            margin-top: 14px;
        }
        .cam-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            padding-bottom: 12px;
            border-bottom: 1px solid var(--line-soft);
        }
        .cam-title { font-weight: 700; font-size: 14px; color: var(--text); }
        .cam-sub { font-size: 11px; color: var(--dim); margin-top: 2px; }
        .cam-actions { display: flex; gap: 8px; }
        
        .panel-drawer {
            display: none;
            background: var(--inset);
            border: 1px solid var(--line-soft);
            border-radius: 8px;
            padding: 14px;
            margin-top: 12px;
        }
        .panel-drawer.open { display: block; }
        
        .log-box {
            background: var(--inset); border: 1px solid var(--line-soft); border-radius: 8px;
            height: 240px; overflow-y: auto; padding: 10px 12px;
            font-size: 11.5px; line-height: 1.8; color: #334155;
            font-family: ui-monospace, 'SF Mono', monospace; white-space: pre-wrap;
        }
        .wide { grid-column: span 2; }
        @media (max-width: 900px) {
            .sidebar { display: none; }
            .wide { grid-column: span 1; }
            .main { padding: 16px; }
        }
    </style>
</head>
<body>
    <aside class="sidebar">
        <div class="brand">ROV Companion<span>Orange Pi 4 Pro • Native Linux</span></div>
        <nav class="nav">
            <div class="nav-label">Monitor</div>
            <a class="on" onclick="scrollToSec('top')">Overview</a>
            <a onclick="scrollToSec('sec-camera')">Camera Stream &amp; Settings</a>
            <a onclick="scrollToSec('sec-telemetry')">Telemetry</a>
            <div class="nav-label">System</div>
            <a onclick="scrollToSec('sec-modules')">Modules</a>
            <a onclick="scrollToSec('sec-logs')">Logs &amp; Events</a>
            <div class="nav-label">Power</div>
            <a onclick="rebootSystem()">Reboot</a>
            <a onclick="shutdownSystem()">Shutdown</a>
        </nav>
    </aside>

    <main class="main" id="top">
        <div class="topbar">
            <h1>ROV Companion System Dashboard</h1>
            <div class="topmeta">
                <span>SoC Temp: <b id="sys-temp">--.-°C</b></span>
                <span class="pill" id="hdr-pill">LINK CHECKING</span>
                <button class="btn btn-danger" onclick="rebootSystem()">Reboot ROV</button>
            </div>
        </div>

        <div class="grid">
            <div class="card" id="sec-modules">
                <h3>Supervised Modules</h3>
                <table id="modules-table">
                    <thead>
                        <tr><th>Module</th><th>State</th><th>Uptime</th><th>Action</th></tr>
                    </thead>
                    <tbody>
                        <tr><td colspan="4">Loading modules...</td></tr>
                    </tbody>
                </table>
            </div>

            <div class="card" id="sec-telemetry">
                <h3>System Health &amp; Telemetry</h3>
                <p><strong>CPU Load:</strong> <span id="cpu-load">--</span></p>
                <p><strong>Memory Usage:</strong> <span id="mem-used">--</span> MB / <span id="mem-total">--</span> MB (<span id="mem-pct">--</span>%)</p>
                <p><strong>MAVLink Link:</strong> <span id="mavlink-status">Checking...</span></p>
                <p><strong>Autopilot Port:</strong> <span id="mavlink-port">--</span></p>
            </div>

            <div class="card wide" id="sec-camera">
                <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px;">
                    <h3>Live Camera Inspection Stream</h3>
                    <div>
                        <button class="btn btn-primary" onclick="switchStream(8081)">Raw Feed (8081)</button>
                        <button class="btn" style="background: #8b5cf6; color: white; margin-left: 8px;" onclick="switchStream(8082)">AI Overlay (8082)</button>
                    </div>
                </div>
                <div style="background: #0f172a; height: 340px; border-radius: 8px; display: flex; align-items: center; justify-content: center; overflow: hidden; margin-bottom: 14px;">
                    <img id="stream-preview" style="max-height: 100%; max-width: 100%; border-radius: 8px;" alt="Video Stream Preview" />
                </div>

                <div style="display: flex; justify-content: space-between; align-items: center; margin-top: 18px; margin-bottom: 6px;">
                    <h3 style="margin:0;">Discovered V4L2 Camera Hardware Widgets</h3>
                    <button class="btn" onclick="fetchCameraDevices()">Scan Hardware</button>
                </div>

                <div id="camera-widgets-container">
                    <p style="color: var(--dim);">Scanning V4L2 USB camera hardware...</p>
                </div>
            </div>

            <div class="card wide" id="sec-logs">
                <h3>System Logs (Live Ring-Buffer)</h3>
                <div class="log-box" id="log-box">Fetching logs...</div>
            </div>
        </div>
    </main>

    <script>
        let cachedDevices = [];
        let activeTabMap = {};

        function scrollToSec(id) {
            const el = document.getElementById(id);
            if (el) el.scrollIntoView({ behavior: 'smooth' });
        }

        function switchStream(port) {
            document.getElementById('stream-preview').src = 'http://' + window.location.hostname + ':' + port + '/stream';
        }
        switchStream(8081);

        async function fetchCameraDevices() {
            try {
                const res = await fetch('/api/v1/cameras');
                const data = await res.json();
                cachedDevices = data.devices || [];
                renderCameraWidgets();
            } catch(e) {
                console.error('Failed to fetch camera devices:', e);
            }
        }

        function renderCameraWidgets() {
            const container = document.getElementById('camera-widgets-container');
            if (!container) return;

            if (cachedDevices.length === 0) {
                container.innerHTML = '<p style="color: var(--dim); padding: 10px 0;">No active V4L2 camera devices detected on /dev/video*</p>';
                return;
            }

            let html = '';
            cachedDevices.forEach((dev, idx) => {
                const devId = 'cam-' + idx;
                const fmtOptions = ['MJPG', 'H264', 'YUYV'].map(f => '<option value="' + f + '">' + f + '</option>').join('');
                
                let resOptions = '';
                if (dev.formats && dev.formats.length > 0) {
                    const resMap = new Map();
                    dev.formats.forEach(f => {
                        const key = f.width + 'x' + f.height;
                        if (!resMap.has(key)) resMap.set(key, { w: f.width, h: f.height });
                    });
                    resMap.forEach((val, key) => {
                        const sel = (val.w === dev.width && val.h === dev.height) ? 'selected' : '';
                        resOptions += '<option value="' + key + '" ' + sel + '>' + key + '</option>';
                    });
                } else {
                    resOptions = '<option value="1280x720">1280x720</option><option value="1920x1080">1920x1080</option><option value="640x480">640x480</option>';
                }

                const fpsOptions = [15, 24, 30, 60].map(fps => {
                    const sel = (fps === dev.fps) ? 'selected' : '';
                    return '<option value="' + fps + '" ' + sel + '>' + fps + ' FPS</option>';
                }).join('');

                html += '<div class="cam-widget">' +
                    '<div class="cam-header">' +
                        '<div>' +
                            '<div class="cam-title">' + (dev.name || 'V4L2 Camera') + '</div>' +
                            '<div class="cam-sub">Device Node: <strong>' + dev.path + '</strong> &bull; USB Topology: ' + (dev.usb_path || 'Direct') + '</div>' +
                        '</div>' +
                        '<div class="cam-actions">' +
                            '<button class="btn-icon" title="Camera Format & Resolution Settings" id="btn-set-' + devId + '" onclick="togglePanel(\'' + devId + '\', \'settings\')">⚙️</button>' +
                            '<button class="btn-icon" title="Image Adjustments & Sliders" id="btn-adj-' + devId + '" onclick="togglePanel(\'' + devId + '\', \'adjust\')">🎛️</button>' +
                        '</div>' +
                    '</div>' +

                    '<div class="panel-drawer" id="panel-settings-' + devId + '">' +
                        '<h4 style="margin:0 0 10px 0; font-size:12px; color:var(--text); text-transform:uppercase;">Format &amp; Resolution Settings</h4>' +
                        '<div style="display:grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap:12px; margin-bottom:12px;">' +
                            '<div>' +
                                '<label style="display:block; font-size:11px; font-weight:600; color:var(--dim); margin-bottom:4px;">Stream Format</label>' +
                                '<select id="fmt-' + devId + '" class="btn" style="width:100%; text-align:left;">' + fmtOptions + '</select>' +
                            '</div>' +
                            '<div>' +
                                '<label style="display:block; font-size:11px; font-weight:600; color:var(--dim); margin-bottom:4px;">Hardware Resolution</label>' +
                                '<select id="res-' + devId + '" class="btn" style="width:100%; text-align:left;">' + resOptions + '</select>' +
                            '</div>' +
                            '<div>' +
                                '<label style="display:block; font-size:11px; font-weight:600; color:var(--dim); margin-bottom:4px;">Framerate</label>' +
                                '<select id="fps-' + devId + '" class="btn" style="width:100%; text-align:left;">' + fpsOptions + '</select>' +
                            '</div>' +
                            '<div>' +
                                '<label style="display:block; font-size:11px; font-weight:600; color:var(--dim); margin-bottom:4px;">JPEG Quality: <span id="v-qual-' + devId + '">' + (dev.quality || 85) + '</span>%</label>' +
                                '<input type="range" id="qual-' + devId + '" min="1" max="100" value="' + (dev.quality || 85) + '" style="width:100%;" oninput="document.getElementById(\'v-qual-' + devId + '\').innerText = this.value">' +
                            '</div>' +
                        '</div>' +
                        '<button class="btn btn-primary" onclick="applyCameraSettings(\'' + dev.path + '\', \'' + devId + '\')">Apply Format Settings</button>' +
                    '</div>' +

                    '<div class="panel-drawer" id="panel-adjust-' + devId + '">' +
                        '<h4 style="margin:0 0 10px 0; font-size:12px; color:var(--text); text-transform:uppercase;">Image Adjustments &amp; Controls</h4>' +
                        '<div style="display:grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap:12px; margin-bottom:12px;">' +
                            '<div>' +
                                '<label style="display:block; font-size:11px; font-weight:600; color:var(--dim); margin-bottom:4px;">Brightness: <span id="v-bright-' + devId + '">' + (dev.brightness !== undefined ? dev.brightness : 128) + '</span></label>' +
                                '<input type="range" id="bright-' + devId + '" min="0" max="255" value="' + (dev.brightness !== undefined ? dev.brightness : 128) + '" style="width:100%;" oninput="document.getElementById(\'v-bright-' + devId + '\').innerText = this.value">' +
                            '</div>' +
                            '<div>' +
                                '<label style="display:block; font-size:11px; font-weight:600; color:var(--dim); margin-bottom:4px;">Contrast: <span id="v-contrast-' + devId + '">' + (dev.contrast !== undefined ? dev.contrast : 128) + '</span></label>' +
                                '<input type="range" id="contrast-' + devId + '" min="0" max="255" value="' + (dev.contrast !== undefined ? dev.contrast : 128) + '" style="width:100%;" oninput="document.getElementById(\'v-contrast-' + devId + '\').innerText = this.value">' +
                            '</div>' +
                        '</div>' +
                        '<button class="btn btn-primary" onclick="applyCameraAdjustments(\'' + dev.path + '\', \'' + devId + '\')">Apply Adjustments</button>' +
                    '</div>' +

                '</div>';
            });

            container.innerHTML = html;
        }

        function togglePanel(devId, panelType) {
            const pSet = document.getElementById('panel-settings-' + devId);
            const pAdj = document.getElementById('panel-adjust-' + devId);
            const bSet = document.getElementById('btn-set-' + devId);
            const bAdj = document.getElementById('btn-adj-' + devId);

            if (panelType === 'settings') {
                const isOpen = pSet.classList.contains('open');
                pSet.classList.toggle('open', !isOpen);
                pAdj.classList.remove('open');
                bSet.classList.toggle('active', !isOpen);
                bAdj.classList.remove('active');
            } else if (panelType === 'adjust') {
                const isOpen = pAdj.classList.contains('open');
                pAdj.classList.toggle('open', !isOpen);
                pSet.classList.remove('open');
                bAdj.classList.toggle('active', !isOpen);
                bSet.classList.remove('active');
            }
        }

        async function applyCameraSettings(devPath, devId) {
            const resVal = document.getElementById('res-' + devId).value;
            const [w, h] = resVal.split('x').map(v => parseInt(v));
            const fps = parseInt(document.getElementById('fps-' + devId).value);
            const quality = parseInt(document.getElementById('qual-' + devId).value);

            const payload = {
                path: devPath,
                width: w,
                height: h,
                fps: fps,
                quality: quality,
                brightness: parseInt(document.getElementById('bright-' + devId).value),
                contrast: parseInt(document.getElementById('contrast-' + devId).value)
            };

            try {
                const res = await fetch('/api/v1/cameras/config', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(payload)
                });
                const data = await res.json();
                if (data.status === 'ok') {
                    alert('Camera format and resolution updated successfully.');
                    fetchCameraDevices();
                }
            } catch(e) {
                alert('Failed to apply camera settings: ' + e);
            }
        }

        async function applyCameraAdjustments(devPath, devId) {
            const resVal = document.getElementById('res-' + devId).value;
            const [w, h] = resVal.split('x').map(v => parseInt(v));
            const fps = parseInt(document.getElementById('fps-' + devId).value);
            const quality = parseInt(document.getElementById('qual-' + devId).value);
            const brightness = parseInt(document.getElementById('bright-' + devId).value);
            const contrast = parseInt(document.getElementById('contrast-' + devId).value);

            const payload = {
                path: devPath,
                width: w,
                height: h,
                fps: fps,
                quality: quality,
                brightness: brightness,
                contrast: contrast
            };

            try {
                const res = await fetch('/api/v1/cameras/config', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(payload)
                });
                const data = await res.json();
                if (data.status === 'ok') {
                    alert('Camera image adjustments applied.');
                    fetchCameraDevices();
                }
            } catch(e) {
                alert('Failed to apply image adjustments: ' + e);
            }
        }

        async function updateDashboard() {
            try {
                const modRes = await fetch('/api/v1/modules');
                const mods = await modRes.json();
                let modHtml = '';
                for (let k in mods) {
                    let m = mods[k];
                    let running = (m.state === 'RUNNING' || m.state === 'running');
                    modHtml += '<tr>' +
                        '<td><strong>' + m.name + '</strong></td>' +
                        '<td><span class="pill ' + (running ? '' : 'lost') + '">' + m.state + '</span></td>' +
                        '<td>' + m.uptime + '</td>' +
                        '<td>' +
                            '<button class="btn" onclick="controlModule(\'' + m.name + '\', \'restart\')">Restart</button>' +
                        '</td>' +
                    '</tr>';
                }
                const tbody = document.querySelector('#modules-table tbody');
                if (tbody) tbody.innerHTML = modHtml;

                const hRes = await fetch('/api/v1/health');
                const h = await hRes.json();
                document.getElementById('sys-temp').innerText = h.temperature.toFixed(1) + '°C';
                document.getElementById('cpu-load').innerText = h.cpu_load.toFixed(2);
                document.getElementById('mem-used').innerText = h.memory_used_mb;
                document.getElementById('mem-total').innerText = h.memory_total_mb;
                document.getElementById('mem-pct').innerText = h.memory_percent.toFixed(1);

                const mRes = await fetch('/api/v1/mavlink');
                const m = await mRes.json();
                const ms = document.getElementById('mavlink-status');
                if (ms) {
                    ms.innerText = m.connected ? 'CONNECTED' : 'DISCONNECTED';
                    ms.style.color = m.connected ? 'var(--green)' : 'var(--red)';
                    ms.style.fontWeight = 'bold';
                }
                
                const pill = document.getElementById('hdr-pill');
                if (pill) {
                    if (m.connected) {
                        pill.className = 'pill';
                        pill.innerText = 'LINK OK';
                    } else {
                        pill.className = 'pill lost';
                        pill.innerText = 'LINK LOST';
                    }
                }
                document.getElementById('mavlink-port').innerText = m.port || 'None';

                const logRes = await fetch('/api/v1/logs?n=40');
                const logs = await logRes.json();
                let logText = logs.map(l => '[' + (l.timestamp ? l.timestamp.substring(11,19) : '') + '] [' + l.module + '] [' + l.level + '] ' + l.message).join('\n');
                const logBox = document.getElementById('log-box');
                if (logBox) {
                    logBox.innerText = logText;
                    logBox.scrollTop = logBox.scrollHeight;
                }
            } catch(e) {
                console.error('Dashboard poll error:', e);
            }
        }

        async function controlModule(name, action) {
            await fetch('/api/v1/modules/action?module=' + name + '&action=' + action, { method: 'POST' });
            updateDashboard();
        }

        async function rebootSystem() {
            if (confirm('Are you sure you want to reboot the ROV companion computer?')) {
                await fetch('/api/v1/system/reboot', { method: 'POST' });
                alert('Reboot signal sent.');
            }
        }

        async function shutdownSystem() {
            if (confirm('Are you sure you want to shut down the ROV companion computer?')) {
                await fetch('/api/v1/system/shutdown', { method: 'POST' });
                alert('Shutdown signal sent.');
            }
        }

        fetchCameraDevices();
        setInterval(updateDashboard, 2000);
        updateDashboard();
    </script>
</body>
</html>`

func jsonEncode(w http.ResponseWriter, v interface{}) error {
	return json.NewEncoder(w).Encode(v)
}

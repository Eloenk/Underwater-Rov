# ROV Companion Computer Service & AI Detection System
### Native Go BlueOS Replacement Daemon for Orange Pi 4 Pro (Allwinner A733 NPU)

This repository contains a high-performance, single-binary companion computer daemon written in **Go** designed to replace BlueOS on non-Raspberry Pi Single Board Computers (SBCs), alongside a **Python NPU-accelerated AI Defect & Crack Detection Pipeline**.

---

## 🎯 Architecture & Single Hardware Ownership Model

To eliminate resource contention, udev mount failures, port collisions, and camera device locks, the system strictly enforces a **Single Hardware Ownership Architecture**:

```
+-----------------------------------------------------------------------------------+
|                        GO COMPANION DAEMON (HOST OS)                             |
|                                                                                   |
|   +-------------------+    +--------------------+    +------------------------+   |
|   | V4L2 Camera Engine|    |  MAVLink Router    |    | Health & Supervision   |   |
|   | /dev/video*       |    |  /dev/ttyACM*      |    | Ring Buffer & SQLite   |   |
|   +---------+---------+    +---------+----------+    +------------------------+   |
|             |                        |                                            |
|             | MJPEG Stream           | UDP (127.0.0.1:14550)                      |
|             v                        v                                            |
|   http://127.0.0.1:8081      UDP MAVLink Forwarding                                |
+-------------+------------------------+--------------------------------------------+
              |                        |
              v                        v
+-----------------------------------------------------------------------------------+
|                   PYTHON NPU AI DETECTION MODULE (PORT 8082)                     |
|  - Consumes Raw Stream from 127.0.0.1:8081/stream                                 |
|  - Executes Vivante VIP9000 NPU / ONNX crack width & depth calculations           |
|  - Outputs AI Overlay MJPEG stream on http://127.0.0.1:8082/stream                |
|  - Sends MAVLink DEFECT alerts to UDP 127.0.0.1:14550                              |
+-----------------------------------------------------------------------------------+
```

1. **Go Companion Service (`cmd/companion`)**:
   - **Exclusive Hardware Owner**: Owns physical USB video capture devices (`/dev/video*`) and USB serial ports (`/dev/ttyACM*`, `/dev/ttyUSB*`).
   - **V4L2 Kernel Ioctl Engine**: Executes `VIDIOC_QUERYCAP`, `VIDIOC_ENUM_FMT`, `VIDIOC_ENUM_FRAMESIZES`, and `VIDIOC_ENUM_FRAMEINTERVALS` to query hardware capabilities directly from the Linux kernel.
   - **fsnotify Hot-Plug Detection**: Monitors `/dev` in real-time to detect USB camera attachment/removal and re-scan devices automatically.
   - **USB Symlink Identity Tracking**: Resolves `/sys/class/video4linux/videoX/device` to track physical USB port topology and prevent camera index swapping across reboots.
   - **MAVLink Router**: Auto-detects Pixhawk/ArduSub autopilots over USB serial, parses heartbeat telemetry, and routes packets bi-directionally to UDP endpoints (`127.0.0.1:14550`).
   - **System Supervisor & Health**: Bounded ring-buffer logger (capped at 1000 items in RAM), persistent event storage (`events.db`), Linux `syscall.Reboot`, and systemd journal rate limiting.
   - **Control Center Web UI**: Embedded dashboard on port 8080 with deep blue navigation (`#0969da`), solid engraved status badges (`LINK OK` / `LINK LOST`), live video stream switcher (Raw 8081 vs AI Overlay 8082), and dynamic camera controls.

2. **Python NPU AI Detection Module (`detection/`)**:
   - **Consumer Pipeline**: Connects to Go's HTTP MJPEG stream (`http://127.0.0.1:8081/stream`) and UDP MAVLink socket (`127.0.0.1:14550`).
   - **NPU Acceleration**: Runs crack measurement model on the **3 TOPS Vivante VIP9000 NPU** (or ONNX Runtime fallback).
   - **AI Overlay Stream**: Exposes processed inspection video with defect bounding boxes and measurements on port **8082** (`http://127.0.0.1:8082/stream`).

---

## 🛠️ Repository Directory Structure

```
ROV/
├── cmd/
│   └── companion/
│       └── main.go                 # Companion service entrypoint
├── pkg/
│   ├── api/                        # REST API endpoints & embedded Web UI dashboard
│   ├── config/                     # YAML configuration parser
│   ├── health/                     # SoC health metrics, /dev/watchdog, syscall.Reboot
│   ├── logging/                    # Bounded ring-buffer logger & SQLite events storage
│   ├── mavlink/                    # Packet parser, heartbeat tracker & UDP router
│   ├── npu/                        # Python NPU subprocess supervisor
│   ├── supervisor/                 # Exponential backoff module supervisor
│   └── video/                      # V4L2 ioctls, fsnotify hotplug, MJPEG HTTP server
├── companion.yaml                  # System configuration file
├── companion.service               # Linux systemd service unit file with log rate-limiting
├── detection/                      # Python AI defect & crack detection engine
│   ├── main.py                     # AI vision process entrypoint
│   ├── config.yaml                 # AI parameters (Camera source: http://127.0.0.1:8081/stream)
│   ├── analysis/                   # Morphological crack width/length algorithms
│   ├── engines/                    # Multi-backend hardware inference (NPU/ONNX/OpenCV)
│   ├── integration/                # MAVLink status and alert emitter
│   └── stream/                     # AI overlay stream server (Port 8082)
```

---

## 🚀 Compiling & Deployment

### 1. Local Compilation (Windows / Linux)
```bash
go build -v -o companion.exe ./cmd/companion
```

### 2. Cross-Compiling for Orange Pi 4 Pro (Linux ARM64)
From Windows PowerShell:
```powershell
$env:GOOS="linux"; $env:GOARCH="arm64"; go build -v -o companion ./cmd/companion; Remove-Item Env:\GOOS; Remove-Item Env:\GOARCH
```
From Linux / macOS Terminal:
```bash
GOOS=linux GOARCH=arm64 go build -v -o companion ./cmd/companion
```

### 3. Orange Pi Host OS Installation (`systemd`)
```bash
sudo mkdir -p /opt/companion
sudo cp companion companion.yaml /opt/companion/
sudo cp -r detection /opt/companion/

sudo cp companion.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now companion
```

---

## 🌐 Web Dashboard & Dynamic Camera Settings

- **Control Center URL**: `http://<ORANGE_PI_IP>:8080`
- **Raw Camera MJPEG Stream**: `http://<ORANGE_PI_IP>:8081/stream`
- **AI Defect Detection Overlay**: `http://<ORANGE_PI_IP>:8082/stream`

### Features & Dynamic Property Control
- **Blue Navigation Sidebar**: Responsive menu with smooth scrolling sections (`#0969da`).
- **Solid Engraved Status Badges**: High contrast badges (`LINK OK` / `LINK LOST`) without distracting glowing lamps or emojis.
- **Stream Switcher**: One-click toggling between Raw Camera Feed (Port 8081) and AI Crack Overlay (Port 8082).
- **Dynamic Camera Settings**:
  - **Resolution `<select>`**: Populated dynamically from V4L2 kernel format enumeration (`VIDIOC_ENUM_FRAMESIZES`).
  - **Framerate `<select>`**: Populated dynamically from V4L2 frame interval enumeration (`VIDIOC_ENUM_FRAMEINTERVALS`).
  - **Quality Slider**: Real-time JPEG compression adjustment ($1\% - 100\%$).
  - **Brightness & Contrast Sliders**: Dynamic camera image adjustments ($0 - 255$).

---

## 📡 REST API Reference

- `GET /api/v1/modules` - Supervised module status and uptime
- `POST /api/v1/modules/action?module=<name>&action=<start|stop|restart>` - Module control
- `GET /api/v1/cameras` - Enumerate active V4L2 cameras, formats, and properties
- `POST /api/v1/cameras/config` - Dynamic camera resolution, FPS, quality, and controls update
- `GET /api/v1/mavlink` - MAVLink connection status, heartbeat age, and telemetry stats
- `GET /api/v1/health` - CPU load, memory usage, and SoC temperature
- `GET /api/v1/logs?n=50` - Tail ring-buffer log entries
- `POST /api/v1/system/reboot` - Trigger Linux `syscall.Reboot`
- `POST /api/v1/system/shutdown` - Trigger Linux system shutdown


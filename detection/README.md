# Real-Time Pipe Crack Detection System
### Optimized for Orange Pi 4 Pro (Allwinner A733 NPU) & BlueOS

A lightweight, high-performance edge AI software package designed for real-time pipe defect and crack inspection on **Orange Pi 4 Pro** operating inside a **BlueOS** ROV (Remotely Operated Vehicle) environment.

---

## 🚀 Key Hardware & Platform Features

- **Hardware Platform:** Orange Pi 4 Pro (Allwinner A733 Octa-Core: 2x Cortex-A76 + 6x Cortex-A55).
- **AI Acceleration Support:**
  - **Vivante VIP9000 NPU:** Up to **3 TOPS** AI compute via Vivante ACUITY `.nbg` graph compilation.
  - **ARM CPU (NEON Vectorized):** Optimized INT8 / FP16 ONNX Runtime execution provider.
  - **OpenCV DNN Module:** Zero-dependency native fallback execution.
- **BlueOS & ROV Ecosystem Integration:**
  - Direct USB Camera capture (`/dev/video0` via V4L2 MJPEG compression).
  - Built-in HTTP MJPEG Live Stream server (`http://<orangepi-ip>:8080/stream`) for embedding into BlueOS web dashboards or custom iframe extensions.
  - Optional MAVLink `STATUSTEXT` telemetry alerts sent directly to QGroundControl / BlueOS.

---

## 📂 Project Architecture

```
ROV/
├── README.md                           # System documentation and setup guide
├── config.yaml                         # Central configuration file
├── requirements.txt                    # Python dependencies
├── main.py                             # Main CLI application runner
├── model_exporter.py                   # Model export utility & ACUITY NPU compilation commands
├── create_sample_model.py              # Generates sample ONNX model for initial test runs
├── engines/                            # Multi-backend hardware inference layer
│   ├── base_engine.py                  # Abstract base engine (Letterboxing, NMS post-processing)
│   ├── onnx_engine.py                  # ONNX Runtime engine (ARM NEON / OpenCL)
│   ├── opencv_engine.py                # OpenCV DNN engine (Zero extra dependencies)
│   ├── tflite_engine.py                # TensorFlow Lite INT8 quantized engine
│   └── npu_viplite_engine.py           # Allwinner A733 Vivante VIP9000 NPU engine (.nbg)
├── analysis/                           # Morphological & physical severity measurement
│   └── crack_analyzer.py              # Calculates Crack Width (mm), Length (mm), Area, & Severity Level
├── stream/                             # Multi-threaded video input & HTTP MJPEG server
│   ├── camera_reader.py                # Low-latency USB camera reader
│   └── mjpeg_server.py                 # Embedded HTTP live video stream server
└── integration/                        # Telemetry connector
    └── mavlink_telemetry.py            # Transmits MAVLink alerts to BlueOS / QGroundControl
```

---

## 🛠️ Quick Start Guide

### 1. Installation

Install the required Python dependencies:
```bash
pip install -r requirements.txt
```

### 2. Generate Initial Sample Model (For Instant Testing)

To test the system immediately without waiting for custom dataset training:
```bash
python create_sample_model.py
```
This generates a starter ONNX model at `models/yolov8n_crack.onnx`.

### 3. Run Crack Detection Pipeline

Start the detection pipeline with default settings:
```bash
python main.py
```

Options:
- `--config config.yaml` : Specify custom YAML config.
- `--source 0` : Specify USB camera ID (`0` for `/dev/video0`) or RTSP URL.
- `--backend onnx` : Force specific backend (`onnx`, `opencv`, `tflite`, `npu_viplite`, `auto`).

### 4. View Live Inspection Feed in BlueOS

Open your browser and navigate to:
```
http://<ORANGE_PI_IP>:8080/stream
```
You can embed this URL into BlueOS Cockpit / QGroundControl video stream widgets!

---

## 🧠 Allwinner A733 (VIP9000 NPU) Model Compilation

To run your custom trained YOLOv8 / YOLOv11 model on the **3 TOPS Vivante VIP9000 NPU**:

1. Export PyTorch weights to ONNX:
   ```bash
   python model_exporter.py --weights path/to/best.pt
   ```

2. Using Vivante **ACUITY Pegasus Toolchain**, convert ONNX to `.nbg`:
   ```bash
   # 1. Import ONNX
   pegasus importonnx --model models/yolov8n_crack.onnx --output-data models/yolov8n_crack.data --output-model models/yolov8n_crack.json

   # 2. Quantize to INT8
   pegasus quantize --model models/yolov8n_crack.json --model-data models/yolov8n_crack.data --device npu --quantizer asymmetric_affine --qtype int8 --rebuild

   # 3. Export NBG
   pegasus exportovxlib --model models/yolov8n_crack.json --model-data models/yolov8n_crack.data --dtype quantized --output-path models/yolov8n_crack.nbg --pack-nbg-unify
   ```

3. Place `models/yolov8n_crack.nbg` into the `models/` directory and set `backend: "npu_viplite"` in `config.yaml`.

---

## ⚙️ Configuration (`config.yaml`)

- **`camera.source`**: USB camera index (`0`) or RTSP network URL.
- **`analysis.mm_per_pixel`**: Calibration ratio (millimeters per camera pixel).
- **`analysis.severity`**: Thresholds for Low, Medium, and Critical severity categorization.
- **`stream_server.port`**: Web stream port (`8080`).
- **`mavlink.enabled`**: Set to `true` to send real-time MAVLink STATUSTEXT warnings.

---

## 🐳 BlueOS Docker Deployment

To run as a background Docker container service inside BlueOS:

```dockerfile
FROM python:3.10-slim

RUN apt-get update && apt-get install -y \
    libgl1-mesa-glx \
    libglib2.0-0 \
    v4l-utils \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY . /app
RUN pip install --no-cache-dir -r requirements.txt

CMD ["python3", "main.py"]
```

#!/usr/bin/env python3

import os
import sys
import time
import yaml
import logging
import argparse
import cv2
import numpy as np

logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s [%(levelname)s] %(name)s: %(message)s',
    handlers=[logging.StreamHandler(sys.stdout)]
)
logger = logging.getLogger("CrackDetector")

from engines import ONNXEngine, OpenCVEngine, TFLiteEngine, VIPLiteNPUEngine
from analysis import PipeCrackAnalyzer
from stream import CameraReader, MJPEGServer
from integration import MAVLinkTelemetry

def load_config(config_path="config.yaml"):
    if not os.path.exists(config_path):
        logger.warning(f"Config file '{config_path}' not found. Using default settings.")
        return {}
    with open(config_path, "r") as f:
        return yaml.safe_load(f)

def select_engine(cfg):
    backend = cfg.get("backend", "auto").lower()
    model_cfg = cfg.get("model", {})
    input_size = tuple(model_cfg.get("input_size", [640, 640]))
    conf_thresh = model_cfg.get("confidence_threshold", 0.40)
    nms_thresh = model_cfg.get("nms_threshold", 0.45)

    onnx_path = model_cfg.get("onnx_path", "models/yolov8n_crack.onnx")
    tflite_path = model_cfg.get("tflite_path", "models/yolov8n_crack_int8.tflite")
    nbg_path = model_cfg.get("nbg_path", "models/yolov8n_crack.nbg")

    if backend == "npu_viplite":
        logger.info("Initializing Allwinner A733 VIP9000 NPU Backend...")
        return VIPLiteNPUEngine(nbg_path, input_size, conf_thresh, nms_thresh)

    elif backend == "tflite":
        logger.info("Initializing TensorFlow Lite Backend...")
        return TFLiteEngine(tflite_path, input_size, conf_thresh, nms_thresh)

    elif backend == "onnx":
        logger.info("Initializing ONNX Runtime Backend...")
        return ONNXEngine(onnx_path, input_size, conf_thresh, nms_thresh)

    elif backend == "opencv":
        logger.info("Initializing OpenCV DNN Backend...")
        return OpenCVEngine(onnx_path, input_size, conf_thresh, nms_thresh)

    elif backend == "auto":
        logger.info("Auto-selecting best available hardware backend...")
        
        if os.path.exists(nbg_path) and os.path.exists("/dev/vipcore"):
            try:
                return VIPLiteNPUEngine(nbg_path, input_size, conf_thresh, nms_thresh)
            except Exception as e:
                logger.warning(f"NPU initialization failed: {e}. Trying ONNX...")

        if os.path.exists(onnx_path):
            try:
                return ONNXEngine(onnx_path, input_size, conf_thresh, nms_thresh)
            except Exception as e:
                logger.warning(f"ONNX Engine initialization failed: {e}. Trying OpenCV DNN...")
            
            try:
                return OpenCVEngine(onnx_path, input_size, conf_thresh, nms_thresh)
            except Exception as e:
                logger.warning(f"OpenCV DNN initialization failed: {e}.")

        logger.warning("No trained ML model file found! Falling back to Classical Adaptive Edge Analysis Mode.")
        return None

    else:
        raise ValueError(f"Unknown backend type: {backend}")

def main():
    parser = argparse.ArgumentParser(description="Pipe Crack Detection System for Orange Pi 4 Pro")
    parser.add_argument("--config", type=str, default="config.yaml", help="Path to config file")
    parser.add_argument("--source", type=str, default=None, help="Camera device index (0) or RTSP URL")
    parser.add_argument("--backend", type=str, default=None, help="Override inference backend (onnx, opencv, tflite, npu_viplite)")
    args = parser.parse_args()

    cfg = load_config(args.config)
    if args.backend:
        cfg["backend"] = args.backend

    cam_cfg = cfg.get("camera", {})
    source = args.source if args.source is not None else cam_cfg.get("source", 0)
    
    camera = CameraReader(
        source=source,
        width=cam_cfg.get("width", 1280),
        height=cam_cfg.get("height", 720),
        fps=cam_cfg.get("fps", 30),
        fourcc=cam_cfg.get("fourcc", "MJPG")
    )
    camera.start()

    ana_cfg = cfg.get("analysis", {})
    sev_cfg = ana_cfg.get("severity", {})
    analyzer = PipeCrackAnalyzer(
        mm_per_pixel=ana_cfg.get("mm_per_pixel", 0.25),
        minor_max_mm=sev_cfg.get("minor_max_mm", 1.0),
        moderate_max_mm=sev_cfg.get("moderate_max_mm", 2.5)
    )

    try:
        engine = select_engine(cfg)
    except Exception as e:
        logger.error(f"Failed to load inference engine: {e}")
        engine = None

    mav_cfg = cfg.get("mavlink", {})
    mavlink = None
    if mav_cfg.get("enabled", False):
        mavlink = MAVLinkTelemetry(
            connection_string=mav_cfg.get("connection_string", "udpout:127.0.0.1:14550"),
            alert_interval=mav_cfg.get("alert_interval_sec", 2.0)
        )
        mavlink.connect()

    stream_cfg = cfg.get("stream_server", {})
    stream_server = None
    latest_processed_frame = None

    if stream_cfg.get("enabled", True):
        stream_server = MJPEGServer(
            host=stream_cfg.get("host", "0.0.0.0"),
            port=stream_cfg.get("port", 8080),
            quality=stream_cfg.get("quality", 80)
        )
        stream_server.start(lambda: latest_processed_frame)

    logger.info("System fully initialized and running. Press Ctrl+C to stop.")

    fps_smooth = 0.0
    t_last = time.perf_counter()

    try:
        while True:
            frame = camera.get_frame()
            if frame is None:
                time.sleep(0.01)
                continue

            detections = []
            infer_time_ms = 0.0

            if engine is not None:
                try:
                    detections, infer_time_ms = engine.predict(frame)
                except Exception as e:
                    logger.error(f"Inference error: {e}")

            analysis_results = []
            critical_alerts = []

            for det in detections:
                res = analyzer.analyze_detection(frame, det)
                analysis_results.append(res)
                if res['severity'] in ['Medium', 'Critical']:
                    critical_alerts.append(res)

            if mavlink and critical_alerts:
                mavlink.send_crack_alert(critical_alerts)

            dt = time.perf_counter() - t_last
            t_last = time.perf_counter()
            current_fps = 1.0 / dt if dt > 0 else 30.0
            fps_smooth = 0.9 * fps_smooth + 0.1 * current_fps if fps_smooth > 0 else current_fps

            annotated_frame = analyzer.render_overlay(frame, analysis_results, fps=fps_smooth, infer_time_ms=infer_time_ms)
            latest_processed_frame = annotated_frame

            time.sleep(0.005)

    except KeyboardInterrupt:
        logger.info("Stopping pipeline...")

    finally:
        camera.stop()
        if stream_server:
            stream_server.stop()
        logger.info("Cleanup complete. Pipeline stopped.")

if __name__ == "__main__":
    main()


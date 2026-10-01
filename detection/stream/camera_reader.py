import os
import sys
import glob
import time
import threading
import logging
import cv2

logger = logging.getLogger("PipeDetector.CameraReader")

class CameraReader:
    def __init__(self, source="http://127.0.0.1:8081/stream", width=1280, height=720, fps=30, fourcc="MJPG", enable_enhancer=False):
        self.source = source
        self.width = width
        self.height = height
        self.fps = fps
        self.fourcc = fourcc
        self.enable_enhancer = enable_enhancer

        self.cap = None
        self.running = False
        self.thread = None
        self.latest_frame = None
        self.lock = threading.Lock()
        self.frame_count = 0
        self.fps_calc = 0.0
        self.actual_source = None
        self.device_info = "Disconnected"

        self.clahe = cv2.createCLAHE(clipLimit=2.0, tileGridSize=(8, 8))

    @staticmethod
    def scan_usb_cameras(max_indices=8):
        devices = []
        if sys.platform.startswith("linux"):
            video_nodes = sorted(glob.glob("/dev/video*"))
            for node in video_nodes:
                try:
                    cap = cv2.VideoCapture(node, cv2.CAP_V4L2)
                    if cap.isOpened():
                        ret, _ = cap.read()
                        w = int(cap.get(cv2.CAP_PROP_FRAME_WIDTH))
                        h = int(cap.get(cv2.CAP_PROP_FRAME_HEIGHT))
                        cap.release()
                        if ret or (w > 0 and h > 0):
                            devices.append({"id": node, "name": f"USB Camera ({node})", "res": f"{w}x{h}"})
                except Exception:
                    continue
        else:
            for idx in range(max_indices):
                try:
                    cap = cv2.VideoCapture(idx, cv2.CAP_DSHOW if sys.platform.startswith("win") else cv2.CAP_ANY)
                    if cap.isOpened():
                        ret, _ = cap.read()
                        w = int(cap.get(cv2.CAP_PROP_FRAME_WIDTH))
                        h = int(cap.get(cv2.CAP_PROP_FRAME_HEIGHT))
                        cap.release()
                        if ret or (w > 0 and h > 0):
                            devices.append({"id": idx, "name": f"USB Camera (Index {idx})", "res": f"{w}x{h}"})
                except Exception:
                    continue

        return devices

    def start(self):
        self.running = True
        self.thread = threading.Thread(target=self._update_loop, daemon=True)
        self.thread.start()
        logger.info("Camera capture worker thread started.")

    def _resolve_source(self):
        if self.source == "auto" or self.source is None:
            cams = self.scan_usb_cameras()
            if cams:
                logger.info(f"Auto-selected USB camera: {cams[0]['name']}")
                return cams[0]['id']
            logger.warning("No USB camera discovered automatically! Retrying index 0...")
            return 0

        if isinstance(self.source, str) and self.source.isdigit():
            return int(self.source)

        return self.source

    def _open_capture(self):
        target_src = self._resolve_source()
        self.actual_source = target_src
        logger.info(f"Opening camera source: {target_src}")

        if isinstance(target_src, int) or (isinstance(target_src, str) and target_src.isdigit()):
            src_idx = int(target_src)
            if sys.platform.startswith("linux"):
                self.cap = cv2.VideoCapture(src_idx, cv2.CAP_V4L2)
            elif sys.platform.startswith("win"):
                self.cap = cv2.VideoCapture(src_idx, cv2.CAP_DSHOW)
            else:
                self.cap = cv2.VideoCapture(src_idx)

            if not self.cap.isOpened():
                self.cap = cv2.VideoCapture(src_idx)

            if self.fourcc:
                self.cap.set(cv2.CAP_PROP_FOURCC, cv2.VideoWriter_fourcc(*self.fourcc))
            self.cap.set(cv2.CAP_PROP_FRAME_WIDTH, self.width)
            self.cap.set(cv2.CAP_PROP_FRAME_HEIGHT, self.height)
            self.cap.set(cv2.CAP_PROP_FPS, self.fps)
            self.cap.set(cv2.CAP_PROP_BUFFERSIZE, 1)

        elif isinstance(target_src, str) and target_src.startswith("/dev/video"):
            self.cap = cv2.VideoCapture(target_src, cv2.CAP_V4L2)
            if self.fourcc:
                self.cap.set(cv2.CAP_PROP_FOURCC, cv2.VideoWriter_fourcc(*self.fourcc))
            self.cap.set(cv2.CAP_PROP_FRAME_WIDTH, self.width)
            self.cap.set(cv2.CAP_PROP_FRAME_HEIGHT, self.height)
            self.cap.set(cv2.CAP_PROP_FPS, self.fps)
            self.cap.set(cv2.CAP_PROP_BUFFERSIZE, 1)

        else:
            self.cap = cv2.VideoCapture(str(target_src))
            self.cap.set(cv2.CAP_PROP_BUFFERSIZE, 1)

        if self.cap and self.cap.isOpened():
            act_w = int(self.cap.get(cv2.CAP_PROP_FRAME_WIDTH))
            act_h = int(self.cap.get(cv2.CAP_PROP_FRAME_HEIGHT))
            self.device_info = f"Source: {target_src} ({act_w}x{act_h})"
            logger.info(f"Camera opened successfully: {self.device_info}")
            return True
        else:
            self.device_info = "Disconnected"
            logger.error(f"Failed to open camera source: {target_src}")
            return False

    def _update_loop(self):
        t0 = time.time()
        frames_counter = 0

        while self.running:
            if not self.cap or not self.cap.isOpened():
                logger.warning("Camera disconnected. Retrying stream connection in 2.0s...")
                time.sleep(2.0)
                self._open_capture()
                continue

            ret, frame = self.cap.read()
            if not ret or frame is None:
                time.sleep(0.01)
                if self.cap and not self.cap.isOpened():
                    self.device_info = "Disconnected"
                continue

            if self.enable_enhancer:
                frame = self.enhance_underwater_frame(frame)

            with self.lock:
                self.latest_frame = frame
                self.frame_count += 1
                self.device_info = f"Active: {self.actual_source}"

            frames_counter += 1
            if time.time() - t0 >= 1.0:
                self.fps_calc = round(frames_counter / (time.time() - t0), 1)
                frames_counter = 0
                t0 = time.time()

    def enhance_underwater_frame(self, frame):
        try:
            lab = cv2.cvtColor(frame, cv2.COLOR_BGR2LAB)
            l, a, b = cv2.split(lab)
            l_enhanced = self.clahe.apply(l)
            enhanced_lab = cv2.merge((l_enhanced, a, b))
            return cv2.cvtColor(enhanced_lab, cv2.COLOR_LAB2BGR)
        except Exception:
            return frame

    def get_frame(self):
        with self.lock:
            if self.latest_frame is None:
                return None
            return self.latest_frame.copy()

    def get_fps(self):
        return self.fps_calc

    def get_device_info(self):
        return self.device_info

    def set_enhancer(self, enabled: bool):
        self.enable_enhancer = enabled

    def stop(self):
        self.running = False
        if self.thread and self.thread.is_alive():
            self.thread.join(timeout=1.0)
        if self.cap:
            self.cap.release()
        logger.info("Camera capture stopped.")

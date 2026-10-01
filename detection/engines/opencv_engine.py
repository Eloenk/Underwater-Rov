import os
import cv2
import time
import numpy as np
import logging
from engines.base_engine import BaseEngine

logger = logging.getLogger("PipeDetector.OpenCVEngine")

class OpenCVEngine(BaseEngine):
    def __init__(self, model_path="models/yolov8n_crack.onnx", input_size=(640, 640), conf_thresh=0.40, nms_thresh=0.45):
        super().__init__(input_size, conf_thresh, nms_thresh)
        self.model_path = model_path
        self.net = None
        self._init_net()

    def _init_net(self):
        if not os.path.exists(self.model_path):
            raise FileNotFoundError(f"ONNX model file not found: {self.model_path}")

        logger.info(f"Loading OpenCV DNN network from: {self.model_path}")
        self.net = cv2.dnn.readNetFromONNX(self.model_path)
        
        try:
            self.net.setPreferableBackend(cv2.dnn.DNN_BACKEND_OPENCV)
            self.net.setPreferableTarget(cv2.dnn.DNN_TARGET_CPU)
        except Exception as e:
            logger.warning(f"Could not set OpenCV DNN backend target: {e}")

    def predict(self, frame):
        if self.net is None:
            return [], 0.0

        t0 = time.perf_counter()
        tensor, r, pad = self.preprocess(frame)
        self.net.setInput(tensor)
        outputs = self.net.forward()
        infer_time_ms = (time.perf_counter() - t0) * 1000.0

        results = self.postprocess(outputs, frame.shape, r, pad)
        return results, round(infer_time_ms, 2)

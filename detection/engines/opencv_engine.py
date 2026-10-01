import logging
import cv2
import numpy as np
from .base_engine import BaseEngine

logger = logging.getLogger("CrackDetector.OpenCVEngine")

class OpenCVEngine(BaseEngine):
    """
    OpenCV DNN module backend (Zero extra dependencies required).
    """

    def __init__(self, model_path, input_size=(640, 640), conf_thresh=0.40, nms_thresh=0.45):
        super().__init__(model_path, input_size, conf_thresh, nms_thresh)
        self.net = None
        self.load_model()

    def load_model(self):
        logger.info(f"Loading model with OpenCV DNN from: {self.model_path}")
        self.net = cv2.dnn.readNetFromONNX(self.model_path)
        
        # Prefer CPU target with ARM NEON / OpenCL
        try:
            self.net.setPreferableBackend(cv2.dnn.DNN_BACKEND_OPENCV)
            self.net.setPreferableTarget(cv2.dnn.DNN_TARGET_CPU)
        except Exception as e:
            logger.warning(f"Could not set OpenCV DNN targets: {e}")

        logger.info("OpenCV DNN network initialized successfully.")

    def infer_raw(self, input_tensor):
        # input_tensor is [1, 3, 640, 640] normalized [0, 1]
        self.net.setInput(input_tensor)
        output = self.net.forward()
        return output

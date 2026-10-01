import os
import time
import numpy as np
import logging
from engines.base_engine import BaseEngine

logger = logging.getLogger("PipeDetector.ONNXEngine")

class ONNXEngine(BaseEngine):
    def __init__(self, model_path="models/yolov8n_crack.onnx", input_size=(640, 640), conf_thresh=0.40, nms_thresh=0.45):
        super().__init__(input_size, conf_thresh, nms_thresh)
        self.model_path = model_path
        self.session = None
        self._init_session()

    def _init_session(self):
        if not os.path.exists(self.model_path):
            raise FileNotFoundError(f"ONNX model file not found: {self.model_path}")

        import onnxruntime as ort
        available_providers = ort.get_available_providers()
        providers = ['CPUExecutionProvider']

        if 'ACLExecutionProvider' in available_providers:
            providers.insert(0, 'ACLExecutionProvider')
        elif 'OpenCLExecutionProvider' in available_providers:
            providers.insert(0, 'OpenCLExecutionProvider')

        opts = ort.SessionOptions()
        opts.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
        opts.intra_op_num_threads = 4

        logger.info(f"Loading ONNX model using providers: {providers}")
        self.session = ort.InferenceSession(self.model_path, sess_options=opts, providers=providers)
        self.input_name = self.session.get_inputs()[0].name
        self.output_names = [o.name for o in self.session.get_outputs()]

    def predict(self, frame):
        if self.session is None:
            return [], 0.0

        t0 = time.perf_counter()
        tensor, r, pad = self.preprocess(frame)
        outputs = self.session.run(self.output_names, {self.input_name: tensor})
        infer_time_ms = (time.perf_counter() - t0) * 1000.0

        predictions = outputs[0]
        results = self.postprocess(predictions, frame.shape, r, pad)
        return results, round(infer_time_ms, 2)

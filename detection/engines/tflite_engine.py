import os
import time
import numpy as np
import logging
from engines.base_engine import BaseEngine

logger = logging.getLogger("PipeDetector.TFLiteEngine")

class TFLiteEngine(BaseEngine):
    def __init__(self, model_path="models/yolov8n_crack_int8.tflite", input_size=(640, 640), conf_thresh=0.40, nms_thresh=0.45):
        super().__init__(input_size, conf_thresh, nms_thresh)
        self.model_path = model_path
        self.interpreter = None
        self.input_details = None
        self.output_details = None
        self.load_model()

    def load_model(self):
        if not os.path.exists(self.model_path):
            raise FileNotFoundError(f"TFLite model file not found: {self.model_path}")

        try:
            import tflite_runtime.interpreter as tflite
        except ImportError:
            try:
                import tensorflow.lite as tflite
            except ImportError:
                raise ImportError("Neither tflite_runtime nor tensorflow is installed.")

        logger.info(f"Loading TFLite model from: {self.model_path}")
        self.interpreter = tflite.Interpreter(model_path=self.model_path, num_threads=4)
        self.interpreter.allocate_tensors()

        self.input_details = self.interpreter.get_input_details()
        self.output_details = self.interpreter.get_output_details()
        logger.info("TFLite Interpreter successfully loaded.")

    def predict(self, frame):
        if self.interpreter is None:
            return [], 0.0

        t0 = time.perf_counter()
        tensor, r, pad = self.preprocess(frame)
        
        input_shape = self.input_details[0]['shape']
        if len(input_shape) == 4 and input_shape[3] == 3:
            t_input = np.transpose(tensor, (0, 2, 3, 1))
        else:
            t_input = tensor

        if self.input_details[0]['dtype'] in [np.uint8, np.int8]:
            scale, zero_point = self.input_details[0]['quantization']
            t_input = (t_input / scale + zero_point).astype(self.input_details[0]['dtype'])

        self.interpreter.set_tensor(self.input_details[0]['index'], t_input)
        self.interpreter.invoke()

        output = self.interpreter.get_tensor(self.output_details[0]['index'])

        if self.output_details[0]['dtype'] in [np.uint8, np.int8]:
            scale, zero_point = self.output_details[0]['quantization']
            output = (output.astype(np.float32) - zero_point) * scale

        infer_time_ms = (time.perf_counter() - t0) * 1000.0
        results = self.postprocess(output, frame.shape, r, pad)
        return results, round(infer_time_ms, 2)

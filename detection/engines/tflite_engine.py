import logging
import numpy as np
from .base_engine import BaseEngine

logger = logging.getLogger("CrackDetector.TFLiteEngine")

class TFLiteEngine(BaseEngine):
    """
    TensorFlow Lite Engine for quantized INT8 / FP16 model execution.
    """

    def __init__(self, model_path, input_size=(640, 640), conf_thresh=0.40, nms_thresh=0.45):
        super().__init__(model_path, input_size, conf_thresh, nms_thresh)
        self.interpreter = None
        self.input_details = None
        self.output_details = None
        self.load_model()

    def load_model(self):
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

    def infer_raw(self, input_tensor):
        # Convert layout if TFLite expects NHWC [1, 640, 640, 3] instead of NCHW [1, 3, 640, 640]
        input_shape = self.input_details[0]['shape']
        if len(input_shape) == 4 and input_shape[3] == 3:
            tensor = np.transpose(input_tensor, (0, 2, 3, 1))
        else:
            tensor = input_tensor

        # Handle INT8 quantization if required by tensor dtype
        if self.input_details[0]['dtype'] == np.uint8 or self.input_details[0]['dtype'] == np.int8:
            scale, zero_point = self.input_details[0]['quantization']
            tensor = (tensor / scale + zero_point).astype(self.input_details[0]['dtype'])

        self.interpreter.set_tensor(self.input_details[0]['index'], tensor)
        self.interpreter.invoke()

        output = self.interpreter.get_tensor(self.output_details[0]['index'])

        # Dequantize output if needed
        if self.output_details[0]['dtype'] == np.uint8 or self.output_details[0]['dtype'] == np.int8:
            scale, zero_point = self.output_details[0]['quantization']
            output = (output.astype(np.float32) - zero_point) * scale

        return output

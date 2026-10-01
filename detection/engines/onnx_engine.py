import logging
import numpy as np
from .base_engine import BaseEngine

logger = logging.getLogger("CrackDetector.ONNXEngine")

class ONNXEngine(BaseEngine):
    """
    ONNX Runtime backend for ARM CPU (NEON accelerated) & OpenCL GPU execution.
    """

    def __init__(self, model_path, input_size=(640, 640), conf_thresh=0.40, nms_thresh=0.45):
        super().__init__(model_path, input_size, conf_thresh, nms_thresh)
        self.session = None
        self.input_name = None
        self.output_names = None
        self.load_model()

    def load_model(self):
        try:
            import onnxruntime as ort
        except ImportError:
            raise ImportError("onnxruntime package is not installed. Install with `pip install onnxruntime`.")

        logger.info(f"Loading ONNX model from: {self.model_path}")
        
        # Check available providers
        available_providers = ort.get_available_providers()
        logger.info(f"Available ONNX Providers: {available_providers}")

        providers = []
        if 'OpenCLExecutionProvider' in available_providers:
            providers.append('OpenCLExecutionProvider')
        if 'ACLExecutionProvider' in available_providers:  # ARM Compute Library
            providers.append('ACLExecutionProvider')
        providers.append('CPUExecutionProvider')

        # Session options optimization
        opts = ort.SessionOptions()
        opts.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
        opts.execution_mode = ort.ExecutionMode.ORT_SEQUENTIAL
        opts.intra_op_num_threads = 4  # Utilize ARM Cortex cores efficiently

        self.session = ort.InferenceSession(self.model_path, sess_options=opts, providers=providers)
        self.input_name = self.session.get_inputs()[0].name
        self.output_names = [o.name for o in self.session.get_outputs()]

        logger.info(f"ONNX Session initialized with active provider: {self.session.get_providers()[0]}")

    def infer_raw(self, input_tensor):
        outputs = self.session.run(self.output_names, {self.input_name: input_tensor})
        return outputs[0]

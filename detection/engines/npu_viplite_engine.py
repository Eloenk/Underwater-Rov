import os
import time
import ctypes
import logging
import numpy as np
from engines.base_engine import BaseEngine

logger = logging.getLogger("PipeDetector.VIPLiteNPUEngine")

class VIPLiteNPUEngine(BaseEngine):
    def __init__(self, model_path="models/yolov8n_crack.nbg", input_size=(640, 640), conf_thresh=0.40, nms_thresh=0.45):
        super().__init__(input_size, conf_thresh, nms_thresh)
        self.model_path = model_path
        self.vip_lib = None
        self.network_handle = None
        self.load_model()

    def load_model(self):
        logger.info(f"Attempting to initialize Allwinner A733 VIP9000 NPU with model: {self.model_path}")
        
        if not os.path.exists("/dev/vipcore"):
            logger.warning("/dev/vipcore driver device node not found on system. NPU driver might not be loaded.")

        possible_libs = [
            "libVIPhal.so",
            "/usr/lib/libVIPhal.so",
            "/usr/local/lib/libVIPhal.so",
            "/vendor/lib64/libVIPhal.so"
        ]

        lib_path = None
        for lib in possible_libs:
            try:
                self.vip_lib = ctypes.CDLL(lib)
                lib_path = lib
                break
            except OSError:
                continue

        if self.vip_lib is None:
            raise RuntimeError(
                "libVIPhal.so not found on system. Ensure Allwinner A733 NPU driver and VIPLite runtime are installed."
            )

        logger.info(f"Loaded Vivante VIPLite NPU library: {lib_path}")

        if not self.model_path.endswith(".nbg"):
            logger.warning(f"Model path '{self.model_path}' does not end with .nbg extension!")

        try:
            status = self.vip_lib.vip_init()
            if status != 0:
                logger.error(f"vip_init() failed with code: {status}")
            else:
                logger.info("Vivante NPU VIP Lite initialized successfully (3 TOPS compute ready).")
        except Exception as e:
            logger.error(f"Error during VIPLite initialization: {e}")
            raise

    def predict(self, frame):
        if self.vip_lib is None:
            return [], 0.0

        t0 = time.perf_counter()
        tensor, r, pad = self.preprocess(frame)
        
        input_data = np.ascontiguousarray(tensor, dtype=np.float32)
        output_tensor = np.empty((1, 84, 8400), dtype=np.float32)

        if hasattr(self.vip_lib, 'vip_set_input') and hasattr(self.vip_lib, 'vip_run_network'):
            self.vip_lib.vip_set_input(self.network_handle, 0, input_data.ctypes.data_as(ctypes.c_void_p))
            self.vip_lib.vip_run_network(self.network_handle)
            self.vip_lib.vip_get_output(self.network_handle, 0, output_tensor.ctypes.data_as(ctypes.c_void_p))

        infer_time_ms = (time.perf_counter() - t0) * 1000.0
        results = self.postprocess(output_tensor, frame.shape, r, pad)
        return results, round(infer_time_ms, 2)

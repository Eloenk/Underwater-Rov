import time
import numpy as np
import cv2

class BaseEngine:
    """
    Abstract Base Class for Crack Detection Inference Engines.
    Provides standardized image letterboxing, input scaling, and NMS post-processing.
    """

    def __init__(self, model_path, input_size=(640, 640), conf_thresh=0.40, nms_thresh=0.45):
        self.model_path = model_path
        self.input_size = tuple(input_size)
        self.conf_thresh = conf_thresh
        self.nms_thresh = nms_thresh

    def load_model(self):
        """Loads the model into hardware runtime."""
        raise NotImplementedError("Subclasses must implement load_model()")

    def infer_raw(self, input_tensor):
        """Runs raw inference on preprocessed tensor."""
        raise NotImplementedError("Subclasses must implement infer_raw()")

    def preprocess(self, img):
        """
        Letterbox resize keeping aspect ratio and normalize BGR -> RGB [1, 3, H, W].
        Returns:
            tensor (np.ndarray): Shape (1, 3, H, W), float32 normalized 0.0 - 1.0
            ratio (tuple): (scale_w, scale_h)
            pad (tuple): (dw, dh) padding applied
        """
        h_orig, w_orig = img.shape[:2]
        w_target, h_target = self.input_size

        # Compute scaling ratio
        r = min(w_target / w_orig, h_target / h_orig)
        new_unpad = (int(round(w_orig * r)), int(round(h_orig * r)))
        dw, dh = w_target - new_unpad[0], h_target - new_unpad[1]
        dw /= 2
        dh /= 2

        if (w_orig, h_orig) != new_unpad:
            img_resized = cv2.resize(img, new_unpad, interpolation=cv2.INTER_LINEAR)
        else:
            img_resized = img.copy()

        top, bottom = int(round(dh - 0.1)), int(round(dh + 0.1))
        left, right = int(round(dw - 0.1)), int(round(dw + 0.1))
        img_padded = cv2.copyMakeBorder(img_resized, top, bottom, left, right, cv2.BORDER_CONSTANT, value=(114, 114, 114))

        # Convert BGR to RGB and transpose to NCHW
        img_rgb = cv2.cvtColor(img_padded, cv2.COLOR_BGR2RGB)
        tensor = img_rgb.transpose((2, 0, 1)).astype(np.float32) / 255.0
        tensor = np.expand_dims(tensor, axis=0)

        return tensor, (r, r), (dw, dh)

    def postprocess(self, outputs, orig_shape, ratio, pad):
        """
        Parses YOLO detection output tensor into bounding boxes:
        Returns list of dicts:
        [{'box': [x1, y1, x2, y2], 'confidence': float, 'class_id': int}, ...]
        """
        # YOLOv8 output tensor format: shape (1, 84, 8400) or (1, 5, 8400)
        # where rows are: [x_center, y_center, width, height, score_class0, score_class1, ...]
        predictions = np.squeeze(outputs)
        
        if predictions.ndim == 2 and predictions.shape[0] < predictions.shape[1]:
            predictions = predictions.T  # Transpose to (8400, 84)

        boxes = []
        confidences = []
        class_ids = []

        dw, dh = pad
        r_w, r_h = ratio
        h_orig, w_orig = orig_shape[:2]

        for pred in predictions:
            # Score is max of class probabilities (or 5th element if single-class crack detector)
            if len(pred) > 5:
                scores = pred[4:]
                class_id = int(np.argmax(scores))
                confidence = float(scores[class_id])
            else:
                confidence = float(pred[4])
                class_id = 0

            if confidence >= self.conf_thresh:
                xc, yc, w, h = pred[0], pred[1], pred[2], pred[3]

                # Adjust for letterboxing padding and scale back to original resolution
                x1 = int(np.clip((xc - w / 2 - dw) / r_w, 0, w_orig))
                y1 = int(np.clip((yc - h / 2 - dh) / r_h, 0, h_orig))
                x2 = int(np.clip((xc + w / 2 - dw) / r_w, 0, w_orig))
                y2 = int(np.clip((yc + h / 2 - dh) / r_h, 0, h_orig))

                boxes.append([x1, y1, x2 - x1, y2 - y1])  # OpenCV NMS format: [x, y, w, h]
                confidences.append(confidence)
                class_ids.append(class_id)

        if not boxes:
            return []

        # Run Non-Maximum Suppression (NMS)
        indices = cv2.dnn.NMSBoxes(boxes, confidences, self.conf_thresh, self.nms_thresh)

        results = []
        if len(indices) > 0:
            for i in indices.flatten():
                x, y, w, h = boxes[i]
                results.append({
                    'box': [x, y, x + w, y + h],
                    'confidence': float(confidences[i]),
                    'class_id': int(class_ids[i]),
                    'label': 'crack'
                })

        return results

    def predict(self, frame):
        """
        Executes end-to-end inference on a BGR image frame.
        Returns:
            detections (list): Bounding box dicts
            latency_ms (float): Inference execution time in milliseconds
        """
        t0 = time.perf_counter()
        tensor, ratio, pad = self.preprocess(frame)
        outputs = self.infer_raw(tensor)
        t_infer = (time.perf_counter() - t0) * 1000.0

        detections = self.postprocess(outputs, frame.shape, ratio, pad)
        return detections, t_infer

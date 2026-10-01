import cv2
import numpy as np

class BaseEngine:
    def __init__(self, input_size=(640, 640), conf_thresh=0.40, nms_thresh=0.45):
        self.input_size = input_size
        self.conf_thresh = conf_thresh
        self.nms_thresh = nms_thresh

    def preprocess(self, img):
        h, w = img.shape[:2]
        target_w, target_h = self.input_size

        r = min(target_w / w, target_h / h)
        new_unpad = (int(round(w * r)), int(round(h * r)))
        dw, dh = target_w - new_unpad[0], target_h - new_unpad[1]

        dw /= 2
        dh /= 2

        if (w, h) != new_unpad:
            img_resized = cv2.resize(img, new_unpad, interpolation=cv2.INTER_LINEAR)
        else:
            img_resized = img

        top, bottom = int(round(dh - 0.1)), int(round(dh + 0.1))
        left, right = int(round(dw - 0.1)), int(round(dw + 0.1))

        img_padded = cv2.copyMakeBorder(img_resized, top, bottom, left, right, cv2.BORDER_CONSTANT, value=(114, 114, 114))

        img_rgb = cv2.cvtColor(img_padded, cv2.COLOR_BGR2RGB)
        tensor = img_rgb.transpose((2, 0, 1)).astype(np.float32) / 255.0
        tensor = np.expand_dims(tensor, axis=0)

        return tensor, r, (left, top)

    def postprocess(self, predictions, orig_shape, r, pad):
        orig_h, orig_w = orig_shape[:2]
        left, top = pad

        if len(predictions.shape) == 3 and predictions.shape[1] < predictions.shape[2]:
            predictions = predictions[0].T

        boxes = []
        confidences = []
        class_ids = []

        for pred in predictions:
            scores = pred[4:]
            class_id = np.argmax(scores)
            confidence = scores[class_id]

            if confidence >= self.conf_thresh:
                cx, cy, w, h = pred[:4]
                x1 = int((cx - w / 2 - left) / r)
                y1 = int((cy - h / 2 - top) / r)
                x2 = int((cx + w / 2 - left) / r)
                y2 = int((cy + h / 2 - top) / r)

                x1 = max(0, min(orig_w, x1))
                y1 = max(0, min(orig_h, y1))
                x2 = max(0, min(orig_w, x2))
                y2 = max(0, min(orig_h, y2))

                boxes.append([x1, y1, x2 - x1, y2 - y1])
                confidences.append(float(confidence))
                class_ids.append(int(class_id))

        indices = cv2.dnn.NMSBoxes(boxes, confidences, self.conf_thresh, self.nms_thresh)
        results = []

        if len(indices) > 0:
            for idx in indices.flatten():
                box = boxes[idx]
                x, y, w, h = box
                results.append({
                    'box': [x, y, x + w, y + h],
                    'confidence': confidences[idx],
                    'class_id': class_ids[idx],
                    'label': 'Pipe Defect'
                })

        return results

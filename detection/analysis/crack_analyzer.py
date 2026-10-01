import cv2
import numpy as np

class PipeCrackAnalyzer:
    def __init__(self, confidence_threshold=0.40):
        self.confidence_threshold = confidence_threshold

    def analyze_detection(self, frame, detection):
        x1, y1, x2, y2 = detection['box']
        h_frame, w_frame = frame.shape[:2]

        x1, y1 = max(0, x1), max(0, y1)
        x2, y2 = min(w_frame, x2), min(h_frame, y2)

        label = detection.get('label', 'Pipe Defect')
        conf = detection.get('confidence', 0.0)

        if conf >= 0.75:
            color = (0, 0, 255)
            severity = "High"
        elif conf >= 0.50:
            color = (0, 165, 255)
            severity = "Medium"
        else:
            color = (0, 255, 255)
            severity = "Low"

        return {
            'box': [x1, y1, x2, y2],
            'label': label,
            'confidence': conf,
            'severity': severity,
            'color': color
        }

    def render_overlay(self, frame, analysis_results, fps=0.0, infer_time_ms=0.0):
        out_frame = frame.copy()
        total_defects = len(analysis_results)

        for res in analysis_results:
            x1, y1, x2, y2 = res['box']
            color = res['color']
            label_text = f"{res['label']} ({int(res['confidence'] * 100)}%)"

            cv2.rectangle(out_frame, (x1, y1), (x2, y2), color, 2)

            (w_txt, h_txt), _ = cv2.getTextSize(label_text, cv2.FONT_HERSHEY_SIMPLEX, 0.5, 1)
            cv2.rectangle(out_frame, (x1, max(0, y1 - 20)), (x1 + w_txt + 10, y1), color, -1)
            cv2.putText(out_frame, label_text, (x1 + 5, y1 - 5), cv2.FONT_HERSHEY_SIMPLEX, 0.5, (0, 0, 0), 1)

        hud_bg = out_frame.copy()
        cv2.rectangle(hud_bg, (0, 0), (out_frame.shape[1], 40), (20, 20, 20), -1)
        out_frame = cv2.addWeighted(out_frame, 0.7, hud_bg, 0.3, 0)

        hud_text = f"PIPE DEFECT DETECTION | FPS: {fps:.1f} ({infer_time_ms:.1f}ms) | Defects Detected: {total_defects}"
        status_color = (0, 0, 255) if total_defects > 0 else (0, 255, 0)
        cv2.putText(out_frame, hud_text, (15, 25), cv2.FONT_HERSHEY_SIMPLEX, 0.6, (255, 255, 255), 2)
        cv2.circle(out_frame, (out_frame.shape[1] - 30, 20), 8, status_color, -1)

        return out_frame

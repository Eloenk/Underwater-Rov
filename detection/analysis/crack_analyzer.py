import math
import cv2
import numpy as np

class PipeCrackAnalyzer:
    """
    Analyzes detected crack ROI to extract physical geometric measurements:
    Length (mm), Max Width (mm), Mean Width (mm), Orientation, and Severity level.
    """

    def __init__(self, mm_per_pixel=0.25, minor_max_mm=1.0, moderate_max_mm=2.5):
        self.mm_per_pixel = mm_per_pixel
        self.minor_max_mm = minor_max_mm
        self.moderate_max_mm = moderate_max_mm

    def analyze_detection(self, frame, detection):
        """
        Processes a single detection box in the frame.
        Returns detailed measurement dictionary and mask overlay.
        """
        x1, y1, x2, y2 = detection['box']
        h_frame, w_frame = frame.shape[:2]

        # Bound coordinates to frame dimensions
        x1, y1 = max(0, x1), max(0, y1)
        x2, y2 = min(w_frame, x2), min(h_frame, y2)

        if (x2 - x1) < 5 or (y2 - y1) < 5:
            return self._empty_analysis(detection)

        # Crop ROI
        roi = frame[y1:y2, x1:x2]
        gray = cv2.cvtColor(roi, cv2.COLOR_BGR2GRAY)

        # Enhance contrast (CLAHE)
        clahe = cv2.createCLAHE(clipLimit=2.0, tileGridSize=(8, 8))
        enhanced = clahe.apply(gray)

        # Gaussian blur + Adaptive Thresholding / Otsu for dark crack features on pipe surface
        blurred = cv2.GaussianBlur(enhanced, (5, 5), 0)
        
        # Adaptive thresholding targeting dark crack lines
        thresh = cv2.adaptiveThreshold(
            blurred, 255, cv2.ADAPTIVE_THRESH_GAUSSIAN_C, cv2.THRESH_BINARY_INV, 11, 2
        )

        # Morphological opening to eliminate noise
        kernel = cv2.getStructuringElement(cv2.MORPH_RECT, (3, 3))
        cleaned = cv2.morphologyEx(thresh, cv2.MORPH_OPEN, kernel, iterations=1)

        # Find largest contour in ROI
        contours, _ = cv2.findContours(cleaned, cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_SIMPLE)
        if not contours:
            return self._empty_analysis(detection)

        # Filter out tiny noise contours
        valid_contours = [c for c in contours if cv2.contourArea(c) > 10]
        if not valid_contours:
            return self._empty_analysis(detection)

        # Merge or pick main crack contour
        main_contour = max(valid_contours, key=cv2.contourArea)

        # Measure orientation angle using minimum area rectangle
        rect = cv2.minAreaRect(main_contour)
        (cx, cy), (width_px, height_px), angle = rect

        # Length is major axis, width is minor axis
        major_axis_px = max(width_px, height_px)
        minor_axis_px = min(width_px, height_px)

        # Distance Transform to find maximum true thickness (width) along crack backbone
        dist_transform = cv2.distanceTransform(cleaned, cv2.DIST_L2, 5)
        max_thickness_px = np.max(dist_transform) * 2.0  # Diameter = 2 * radius

        # Convert measurements to physical millimeters
        length_mm = round(major_axis_px * self.mm_per_pixel, 2)
        max_width_mm = round(max_thickness_px * self.mm_per_pixel, 2)
        mean_width_mm = round(minor_axis_px * self.mm_per_pixel, 2)
        area_mm2 = round(cv2.contourArea(main_contour) * (self.mm_per_pixel ** 2), 2)

        # Determine crack orientation type (Axial / Longitudinal vs Circumferential / Transverse)
        if width_px < height_px:
            angle = 90.0 + angle
        angle = angle % 180.0
        
        if 45.0 <= angle <= 135.0:
            orientation = "Vertical / Axial"
        else:
            orientation = "Horizontal / Circumferential"

        # Determine severity score
        if max_width_mm < self.minor_max_mm:
            severity = "Low"
            color = (0, 255, 0)      # Green
        elif max_width_mm <= self.moderate_max_mm:
            severity = "Medium"
            color = (0, 255, 255)    # Yellow
        else:
            severity = "Critical"
            color = (0, 0, 255)      # Red

        analysis_result = {
            'box': [x1, y1, x2, y2],
            'confidence': detection['confidence'],
            'length_mm': length_mm,
            'max_width_mm': max_width_mm,
            'mean_width_mm': mean_width_mm,
            'area_mm2': area_mm2,
            'orientation': orientation,
            'angle': round(angle, 1),
            'severity': severity,
            'color': color,
            'contour_roi': main_contour + np.array([x1, y1])  # Shift contour to global image space
        }

        return analysis_result

    def _empty_analysis(self, detection):
        return {
            'box': detection['box'],
            'confidence': detection['confidence'],
            'length_mm': 0.0,
            'max_width_mm': 0.0,
            'mean_width_mm': 0.0,
            'area_mm2': 0.0,
            'orientation': 'Unknown',
            'angle': 0.0,
            'severity': 'Low',
            'color': (0, 255, 0),
            'contour_roi': None
        }

    def render_overlay(self, frame, analysis_results, fps=0.0, infer_time_ms=0.0):
        """
        Draws bounding boxes, crack contour highlights, measurement overlays,
        and HUD status bar onto the frame.
        """
        out_frame = frame.copy()
        
        critical_count = 0
        total_cracks = len(analysis_results)

        for res in analysis_results:
            x1, y1, x2, y2 = res['box']
            color = res['color']

            if res['severity'] == 'Critical':
                critical_count += 1

            # Draw crack ROI bounding box
            cv2.rectangle(out_frame, (x1, y1), (x2, y2), color, 2)

            # Draw crack contour if available
            if res['contour_roi'] is not None:
                cv2.drawContours(out_frame, [res['contour_roi']], -1, (255, 0, 255), 2)

            # Label overlay text
            label = f"Crack ({res['severity']}): W={res['max_width_mm']}mm L={res['length_mm']}mm"
            
            # Label background box
            (w_txt, h_txt), _ = cv2.getTextSize(label, cv2.FONT_HERSHEY_SIMPLEX, 0.5, 1)
            cv2.rectangle(out_frame, (x1, max(0, y1 - 20)), (x1 + w_txt + 10, y1), color, -1)
            cv2.putText(out_frame, label, (x1 + 5, y1 - 5), cv2.FONT_HERSHEY_SIMPLEX, 0.5, (0, 0, 0), 1)

        # Top HUD Status Bar
        hud_bg = out_frame.copy()
        cv2.rectangle(hud_bg, (0, 0), (out_frame.shape[1], 40), (20, 20, 20), -1)
        out_frame = cv2.addWeighted(out_frame, 0.7, hud_bg, 0.3, 0)

        # HUD Text Metrics
        hud_text = f"PIPE CRACK INSPECTION | FPS: {fps:.1f} ({infer_time_ms:.1f}ms) | Cracks: {total_cracks} | Critical: {critical_count}"
        status_color = (0, 0, 255) if critical_count > 0 else (0, 255, 0)
        cv2.putText(out_frame, hud_text, (15, 25), cv2.FONT_HERSHEY_SIMPLEX, 0.6, (255, 255, 255), 2)
        cv2.circle(out_frame, (out_frame.shape[1] - 30, 20), 8, status_color, -1)

        return out_frame

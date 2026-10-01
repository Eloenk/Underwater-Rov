import os
import csv
import json
import time
import datetime
import logging
import cv2

logger = logging.getLogger("CrackDetector.MissionLogger")

class MissionLogger:
    """
    Manages ROV inspection logs, crack detection snapshots, and telemetry recording.
    Saves snapshots (.jpg) and detailed metadata (.json & .csv) when critical cracks are found.
    """

    def __init__(self, output_dir="snapshots", auto_snapshot_critical=True, cooldown_sec=3.0):
        self.output_dir = output_dir
        self.auto_snapshot_critical = auto_snapshot_critical
        self.cooldown_sec = cooldown_sec
        self.last_snapshot_time = 0.0
        self.detections_log = []

        os.makedirs(self.output_dir, exist_ok=True)
        self.csv_path = os.path.join(self.output_dir, "mission_log.csv")
        self.json_path = os.path.join(self.output_dir, "mission_log.json")
        self._init_csv_file()

    def _init_csv_file(self):
        """Creates CSV header if file does not exist."""
        if not os.path.exists(self.csv_path):
            try:
                with open(self.csv_path, 'w', newline='', encoding='utf-8') as f:
                    writer = csv.writer(f)
                    writer.writerow([
                        "Timestamp", "Severity", "MaxWidth_mm", "Length_mm", 
                        "Area_mm2", "Orientation", "Depth_m", "Heading_deg", "Snapshot_File"
                    ])
            except Exception as e:
                logger.error(f"Error initializing CSV log file: {e}")

    def log_event(self, frame, critical_results, telemetry=None):
        """
        Logs a crack detection event, saves snapshot image, and updates mission log files.
        """
        if not critical_results:
            return None

        now = time.time()
        if now - self.last_snapshot_time < self.cooldown_sec:
            return None  # Cooldown active

        self.last_snapshot_time = now
        timestamp_str = datetime.datetime.now().strftime("%Y%m%d_%H%M%S_%f")[:-3]
        formatted_time = datetime.datetime.now().strftime("%Y-%m-%d %H:%M:%S")

        max_crack = max(critical_results, key=lambda x: x['max_width_mm'])
        severity = max_crack['severity']

        # File name for snapshot image
        filename = f"crack_{severity.lower()}_{timestamp_str}.jpg"
        filepath = os.path.join(self.output_dir, filename)

        # Save snapshot frame to disk
        try:
            cv2.imwrite(filepath, frame)
            logger.info(f"Saved crack snapshot: {filepath}")
        except Exception as e:
            logger.error(f"Failed to save snapshot frame: {e}")
            filename = "N/A"

        telemetry = telemetry or {}
        depth = telemetry.get('depth_m', 0.0)
        heading = telemetry.get('heading_deg', 0.0)

        record = {
            'timestamp': formatted_time,
            'severity': severity,
            'max_width_mm': max_crack['max_width_mm'],
            'length_mm': max_crack['length_mm'],
            'area_mm2': max_crack['area_mm2'],
            'orientation': max_crack['orientation'],
            'depth_m': depth,
            'heading_deg': heading,
            'snapshot_file': filename,
            'snapshot_path': filepath
        }

        self.detections_log.append(record)

        # Append to CSV
        try:
            with open(self.csv_path, 'a', newline='', encoding='utf-8') as f:
                writer = csv.writer(f)
                writer.writerow([
                    formatted_time, severity, max_crack['max_width_mm'], max_crack['length_mm'],
                    max_crack['area_mm2'], max_crack['orientation'], depth, heading, filename
                ])
        except Exception as e:
            logger.error(f"Error appending to CSV log: {e}")

        # Update JSON log
        try:
            with open(self.json_path, 'w', encoding='utf-8') as f:
                json.dump(self.detections_log[-100:], f, indent=2)  # Keep recent 100 entries
        except Exception as e:
            logger.error(f"Error updating JSON log: {e}")

        return record

    def manual_snapshot(self, frame, telemetry=None):
        """Manually captures frame snapshot regardless of severity."""
        timestamp_str = datetime.datetime.now().strftime("%Y%m%d_%H%M%S_%f")[:-3]
        filename = f"manual_snap_{timestamp_str}.jpg"
        filepath = os.path.join(self.output_dir, filename)

        try:
            cv2.imwrite(filepath, frame)
            logger.info(f"Saved manual snapshot: {filepath}")
            return {"status": "success", "file": filename, "path": filepath}
        except Exception as e:
            logger.error(f"Failed manual snapshot: {e}")
            return {"status": "error", "message": str(e)}

    def get_recent_logs(self, limit=50):
        """Returns list of logged detection events."""
        return self.detections_log[-limit:]

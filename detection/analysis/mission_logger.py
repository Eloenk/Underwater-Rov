import os
import csv
import json
import time
import datetime
import logging
import cv2

logger = logging.getLogger("PipeDetector.MissionLogger")

class MissionLogger:
    def __init__(self, output_dir="snapshots", auto_snapshot=True, cooldown_sec=3.0):
        self.output_dir = output_dir
        self.auto_snapshot = auto_snapshot
        self.cooldown_sec = cooldown_sec
        self.last_snapshot_time = 0.0
        self.detections_log = []

        os.makedirs(self.output_dir, exist_ok=True)
        self.csv_path = os.path.join(self.output_dir, "mission_log.csv")
        self.json_path = os.path.join(self.output_dir, "mission_log.json")
        self._init_csv_file()

    def _init_csv_file(self):
        if not os.path.exists(self.csv_path):
            try:
                with open(self.csv_path, 'w', newline='', encoding='utf-8') as f:
                    writer = csv.writer(f)
                    writer.writerow([
                        "Timestamp", "Label", "Confidence", "Severity", 
                        "Depth_m", "Heading_deg", "Snapshot_File"
                    ])
            except Exception as e:
                logger.error(f"Error initializing CSV log file: {e}")

    def log_event(self, frame, defect_results, telemetry=None):
        if not defect_results:
            return None

        now = time.time()
        if now - self.last_snapshot_time < self.cooldown_sec:
            return None

        self.last_snapshot_time = now
        timestamp_str = datetime.datetime.now().strftime("%Y%m%d_%H%M%S_%f")[:-3]
        formatted_time = datetime.datetime.now().strftime("%Y-%m-%d %H:%M:%S")

        primary_defect = defect_results[0]
        severity = primary_defect['severity']
        label = primary_defect['label']
        conf = primary_defect['confidence']

        filename = f"defect_{label.lower().replace(' ', '_')}_{timestamp_str}.jpg"
        filepath = os.path.join(self.output_dir, filename)

        try:
            cv2.imwrite(filepath, frame)
            logger.info(f"Saved defect snapshot: {filepath}")
        except Exception as e:
            logger.error(f"Failed to save snapshot frame: {e}")
            filename = "N/A"

        telemetry = telemetry or {}
        depth = telemetry.get('depth_m', 0.0)
        heading = telemetry.get('heading_deg', 0.0)

        record = {
            'timestamp': formatted_time,
            'label': label,
            'confidence': conf,
            'severity': severity,
            'depth_m': depth,
            'heading_deg': heading,
            'snapshot_file': filename,
            'snapshot_path': filepath
        }

        self.detections_log.append(record)

        try:
            with open(self.csv_path, 'a', newline='', encoding='utf-8') as f:
                writer = csv.writer(f)
                writer.writerow([
                    formatted_time, label, conf, severity, depth, heading, filename
                ])
        except Exception as e:
            logger.error(f"Error appending to CSV log: {e}")

        try:
            with open(self.json_path, 'w', encoding='utf-8') as f:
                json.dump(self.detections_log[-100:], f, indent=2)
        except Exception as e:
            logger.error(f"Error updating JSON log: {e}")

        return record

    def manual_snapshot(self, frame, telemetry=None):
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
        return self.detections_log[-limit:]

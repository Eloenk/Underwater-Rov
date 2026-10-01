import os
import time
import glob
import sys
import threading
import logging

logger = logging.getLogger("PipeDetector.MAVLinkTelemetry")

class MAVLinkTelemetry:
    DEFAULT_BAUDS = [115200, 57600, 921600, 460800, 38400]

    def __init__(self, connection_string="auto", alert_interval=2.0, baud=None):
        self.connection_string = connection_string
        self.alert_interval = alert_interval
        self.baud = baud
        self.mav_connection = None
        self.last_alert_time = 0.0
        self.running = False
        self.thread = None
        self.lock = threading.Lock()

        self.telemetry = {
            'depth_m': 0.0,
            'heading_deg': 0.0,
            'pitch_deg': 0.0,
            'roll_deg': 0.0,
            'battery_v': 0.0,
            'mode': 'UNKNOWN',
            'armed': False,
            'connected': False,
            'port_info': 'Disconnected'
        }

    @staticmethod
    def scan_usb_ports():
        ports = []
        try:
            import serial.tools.list_ports
            for p in serial.tools.list_ports.comports():
                if "USB" in p.description or "Pixhawk" in p.description or "Ardu" in p.description or "ACM" in p.device or "USB" in p.device or "COM" in p.device:
                    ports.append(p.device)
        except Exception:
            if sys.platform.startswith("linux"):
                ports = glob.glob("/dev/ttyACM*") + glob.glob("/dev/ttyUSB*")
            elif sys.platform.startswith("win"):
                ports = [f"COM{i}" for i in range(1, 33)]

        return ports

    def connect(self):
        self.running = True
        self.thread = threading.Thread(target=self._connection_loop, daemon=True)
        self.thread.start()
        logger.info("MAVLink telemetry background worker started.")

    def _connection_loop(self):
        while self.running:
            if self.mav_connection is None:
                conn_str = self._resolve_connection()
                if conn_str:
                    try:
                        from pymavlink import mavutil
                        logger.info(f"Connecting MAVLink endpoint: {conn_str}")
                        if "COM" in conn_str or "/dev/tty" in conn_str:
                            baud = self.baud if self.baud else 115200
                            conn = mavutil.mavlink_connection(conn_str, baud=baud)
                        else:
                            conn = mavutil.mavlink_connection(conn_str)

                        hb = conn.wait_heartbeat(timeout=3.0)
                        if hb:
                            logger.info(f"MAVLink Heartbeat detected from System {conn.target_system}, Component {conn.target_component}")
                            self.mav_connection = conn
                            with self.lock:
                                self.telemetry['connected'] = True
                                self.telemetry['port_info'] = conn_str
                        else:
                            logger.warning(f"No heartbeat received on {conn_str}")
                            conn.close()
                    except ImportError:
                        logger.error("pymavlink not installed.")
                        time.sleep(5.0)
                    except Exception as e:
                        logger.warning(f"Connection attempt failed on {conn_str}: {e}")

            if self.mav_connection is not None:
                self._read_telemetry_step()
            else:
                time.sleep(2.0)

    def _resolve_connection(self):
        if self.connection_string and self.connection_string.lower() != "auto":
            return self.connection_string

        usb_ports = self.scan_usb_ports()
        if not usb_ports:
            return "udpout:127.0.0.1:14550"

        for port in usb_ports:
            bauds_to_try = [self.baud] if self.baud else self.DEFAULT_BAUDS
            for baud in bauds_to_try:
                try:
                    from pymavlink import mavutil
                    conn = mavutil.mavlink_connection(port, baud=baud)
                    hb = conn.wait_heartbeat(timeout=1.5)
                    if hb:
                        return f"{port}:{baud}"
                    conn.close()
                except Exception:
                    continue

        return "udpout:127.0.0.1:14550"

    def _read_telemetry_step(self):
        try:
            msg = self.mav_connection.recv_match(blocking=False)
            if not msg:
                time.sleep(0.01)
                return

            msg_type = msg.get_type()
            with self.lock:
                if msg_type == 'VFR_HUD':
                    self.telemetry['depth_m'] = round(abs(float(msg.alt)), 2)
                    self.telemetry['heading_deg'] = round(float(msg.heading), 1)

                elif msg_type == 'ATTITUDE':
                    import math
                    self.telemetry['roll_deg'] = round(math.degrees(msg.roll), 1)
                    self.telemetry['pitch_deg'] = round(math.degrees(msg.pitch), 1)
                    yaw_deg = math.degrees(msg.yaw)
                    if yaw_deg < 0:
                        yaw_deg += 360.0
                    self.telemetry['heading_deg'] = round(yaw_deg, 1)

                elif msg_type == 'SYS_STATUS':
                    self.telemetry['battery_v'] = round(float(msg.voltage_battery) / 1000.0, 2)

                elif msg_type == 'HEARTBEAT':
                    from pymavlink import mavutil
                    mode_str = mavutil.mode_string_v10(msg)
                    self.telemetry['mode'] = mode_str
                    self.telemetry['armed'] = (msg.base_mode & mavutil.mavlink.MAV_MODE_FLAG_SAFETY_ARMED) > 0
                    self.telemetry['connected'] = True

        except Exception as e:
            logger.error(f"Error reading MAVLink packet: {e}")
            with self.lock:
                self.telemetry['connected'] = False
            self.mav_connection = None

    def get_telemetry(self):
        with self.lock:
            return dict(self.telemetry)

    def send_defect_alert(self, defect_results):
        if not self.mav_connection or not defect_results:
            return

        now = time.time()
        if now - self.last_alert_time < self.alert_interval:
            return

        self.last_alert_time = now
        defect_count = len(defect_results)
        depth_str = f" Depth={self.telemetry['depth_m']}m" if self.telemetry['connected'] else ""
        
        msg_text = f"PIPE DEFECT ALERT: {defect_count} Abnormality(s) Detected (Crack/Hole){depth_str}"
        logger.info(f"Sending MAVLink STATUSTEXT: {msg_text}")

        try:
            from pymavlink import mavutil
            severity_flag = mavutil.mavlink.MAV_SEVERITY_WARNING

            self.mav_connection.mav.statustext_send(
                severity_flag,
                msg_text.encode('utf-8')
            )

            name_bytes = b"DEFECT_CNT\x00"[:10]
            self.mav_connection.mav.named_value_float_send(
                int(time.time() * 1000) & 0xFFFFFFFF,
                name_bytes,
                float(defect_count)
            )
        except Exception as e:
            logger.error(f"Error sending MAVLink telemetry alert: {e}")

    def stop(self):
        self.running = False
        if self.thread and self.thread.is_alive():
            self.thread.join(timeout=1.0)
        if self.mav_connection:
            try:
                self.mav_connection.close()
            except Exception:
                pass
        logger.info("MAVLink telemetry stopped.")

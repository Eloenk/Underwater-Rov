import time
import threading
import logging
from http.server import HTTPServer, BaseHTTPRequestHandler
from socketserver import ThreadingMixIn
import cv2

logger = logging.getLogger("CrackDetector.MJPEGServer")

# Global reference to active stream frame producer
_active_frame_provider = None
_jpeg_quality = 80

class ThreadedHTTPServer(ThreadingMixIn, HTTPServer):
    """Multi-threaded HTTP server for concurrent MJPEG clients."""
    daemon_threads = True

class MJPEGHandler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        pass  # Suppress default HTTP request logging spam

    def do_GET(self):
        if self.path == '/stream' or self.path == '/':
            self.send_response(200)
            self.send_header('Content-type', 'multipart/x-mixed-replace; boundary=--jpgboundary')
            self.end_headers()

            while True:
                try:
                    if _active_frame_provider is None:
                        time.sleep(0.05)
                        continue

                    frame = _active_frame_provider()
                    if frame is None:
                        time.sleep(0.03)
                        continue

                    ret, jpeg = cv2.imencode('.jpg', frame, [cv2.IMWRITE_JPEG_QUALITY, _jpeg_quality])
                    if not ret:
                        continue

                    self.wfile.write(b"--jpgboundary\r\n")
                    self.send_header('Content-type', 'image/jpeg')
                    self.send_header('Content-length', str(len(jpeg)))
                    self.end_headers()
                    self.wfile.write(jpeg.tobytes())
                    self.wfile.write(b"\r\n")

                    time.sleep(0.03)  # ~30 FPS streaming target
                except (ConnectionResetError, BrokenPipeError):
                    break
                except Exception as e:
                    logger.debug(f"MJPEG client disconnected: {e}")
                    break
        else:
            self.send_error(404)
            self.end_headers()

class MJPEGServer:
    """
    Lightweight HTTP MJPEG Live Stream Server for BlueOS Interface.
    """

    def __init__(self, host="0.0.0.0", port=8080, quality=80):
        self.host = host
        self.port = port
        global _jpeg_quality
        _jpeg_quality = quality
        
        self.server = None
        self.thread = None

    def start(self, frame_provider_func):
        global _active_frame_provider
        _active_frame_provider = frame_provider_func

        try:
            self.server = ThreadedHTTPServer((self.host, self.port), MJPEGHandler)
            self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
            self.thread.start()
            logger.info(f"MJPEG Live Stream Server running at http://{self.host}:{self.port}/stream")
        except Exception as e:
            logger.error(f"Failed to start MJPEG Stream Server on port {self.port}: {e}")

    def stop(self):
        if self.server:
            self.server.shutdown()
            self.server.server_close()
        logger.info("MJPEG Stream Server stopped.")

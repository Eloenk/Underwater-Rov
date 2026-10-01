import time
import logging
from http.server import HTTPServer, BaseHTTPRequestHandler
from socketserver import ThreadingMixIn
import cv2

logger = logging.getLogger("PipeDetector.MJPEGServer")

_global_frame_provider = None

class ThreadedHTTPServer(ThreadingMixIn, HTTPServer):
    daemon_threads = True

class MJPEGStreamHandler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        pass

    def do_GET(self):
        global _global_frame_provider
        if self.path == '/stream' or self.path == '/':
            self.send_response(200)
            self.send_header('Content-Type', 'multipart/x-mixed-replace; boundary=frame')
            self.send_header('Cache-Control', 'no-cache, no-store, must-revalidate')
            self.send_header('Pragma', 'no-cache')
            self.send_header('Expires', '0')
            self.send_header('Connection', 'close')
            self.end_headers()

            _jpeg_quality = getattr(self.server, 'quality', 80)

            while True:
                try:
                    if _global_frame_provider is None:
                        time.sleep(0.05)
                        continue

                    frame = _global_frame_provider()
                    if frame is None:
                        time.sleep(0.01)
                        continue

                    ret, jpeg = cv2.imencode('.jpg', frame, [cv2.IMWRITE_JPEG_QUALITY, _jpeg_quality])
                    if not ret:
                        continue

                    buf = jpeg.tobytes()
                    self.wfile.write(b'--frame\r\n')
                    self.send_header('Content-Type', 'image/jpeg')
                    self.send_header('Content-Length', str(len(buf)))
                    self.end_headers()
                    self.wfile.write(buf)
                    self.wfile.write(b'\r\n')
                    time.sleep(0.03)

                except (BrokenPipeError, ConnectionResetError):
                    break
                except Exception as e:
                    logger.debug(f"Stream client disconnected: {e}")
                    break
        else:
            self.send_error(404)
            self.end_headers()

class MJPEGServer:
    def __init__(self, host="0.0.0.0", port=8082, quality=80):
        self.host = host
        self.port = port
        self.quality = quality
        self.server = None
        self.thread = None

    def start(self, frame_provider):
        global _global_frame_provider
        _global_frame_provider = frame_provider

        self.server = ThreadedHTTPServer((self.host, self.port), MJPEGStreamHandler)
        self.server.quality = self.quality
        
        import threading
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        logger.info(f"MJPEG Stream Server listening on http://{self.host}:{self.port}/stream")

    def stop(self):
        if self.server:
            self.server.shutdown()
            self.server.server_close()
        logger.info("MJPEG Stream Server stopped.")

"""
Video capture and HTTP MJPEG streaming package.
"""

from .camera_reader import CameraReader
from .mjpeg_server import MJPEGServer

__all__ = ['CameraReader', 'MJPEGServer']

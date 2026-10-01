#!/usr/bin/env python3
"""
Generates a minimal valid ONNX YOLO model file for instant testing.
"""

import os
import torch
import torch.nn as nn

class DummyYOLOv8CrackModel(nn.Module):
    def __init__(self):
        super().__init__()
        self.conv = nn.Sequential(
            nn.Conv2d(3, 16, kernel_size=3, stride=2, padding=1),
            nn.BatchNorm2d(16),
            nn.SiLU(),
            nn.AdaptiveAvgPool2d((8400, 84))
        )
    def forward(self, x):
        # Returns output tensor with shape [1, 84, 8400] matching YOLOv8 output
        bs = x.shape[0]
        out = torch.zeros(bs, 84, 8400, dtype=torch.float32)
        return out

def main():
    os.makedirs("models", exist_ok=True)
    model = DummyYOLOv8CrackModel()
    model.eval()

    dummy_input = torch.randn(1, 3, 640, 640)
    onnx_path = "models/yolov8n_crack.onnx"

    torch.onnx.export(
        model,
        dummy_input,
        onnx_path,
        export_params=True,
        opset_version=12,
        do_constant_folding=True,
        input_names=['images'],
        output_names=['output0']
    )
    print(f"Sample ONNX model successfully generated at: {onnx_path}")

if __name__ == "__main__":
    main()

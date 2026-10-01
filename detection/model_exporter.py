#!/usr/bin/env python3
"""
Model Exporter & NPU Graph Compiler helper for Orange Pi 4 Pro.
Converts PyTorch YOLOv8 models to ONNX, TFLite (INT8), and prints ACUITY NBG instructions.
"""

import os
import argparse
import logging

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger("ModelExporter")

def export_yolov8(weights="yolov8n.pt", output_dir="models", imgsz=640):
    """
    Exports a YOLOv8 PyTorch checkpoint to ONNX format.
    """
    os.makedirs(output_dir, exist_ok=True)
    logger.info(f"Loading YOLOv8 weights: {weights}")
    
    try:
        from ultralytics import YOLO
        model = YOLO(weights)
        
        # Export to ONNX format
        onnx_path = model.export(format="onnx", imgsz=imgsz, dynamic=False, opset=12)
        logger.info(f"Successfully exported ONNX model to: {onnx_path}")
        
        # Move to models directory
        target_onnx = os.path.join(output_dir, "yolov8n_crack.onnx")
        os.rename(onnx_path, target_onnx)
        logger.info(f"Saved ONNX model as: {target_onnx}")
        return target_onnx
    except ImportError:
        logger.error("ultralytics package is required for export. Install via `pip install ultralytics`.")
        return None

def print_acuity_npu_instructions(onnx_path="models/yolov8n_crack.onnx"):
    """
    Prints step-by-step commands to convert ONNX model to .nbg for Vivante VIP9000 NPU (Allwinner A733).
    """
    print("\n" + "="*80)
    print(" ALLWINNER A733 (VIVANTE VIP9000) NPU COMPILATION GUIDE (ACUITY TOOLKIT)")
    print("="*80)
    print(f"""
To accelerate crack detection on the Orange Pi 4 Pro 3 TOPS NPU, compile your ONNX model
to an NBG (Network Binary Graph) file using the Vivante ACUITY toolchain:

Step 1: Convert ONNX Model to ACUITY Internal Format (.json / .data)
---------------------------------------------------------------------
$ pegasus importonnx \\
    --model {onnx_path} \\
    --output-data models/yolov8n_crack.data \\
    --output-model models/yolov8n_crack.json

Step 2: Generate INT8 Quantization Dataset Table
------------------------------------------------
$ pegasus generateinputs \\
    --model models/yolov8n_crack.json \\
    --output-dataset dataset.txt

Step 3: Quantize Model (INT8/FP16 for 3 TOPS acceleration)
---------------------------------------------------------
$ pegasus quantize \\
    --model models/yolov8n_crack.json \\
    --model-data models/yolov8n_crack.data \\
    --device npu \\
    --quantizer asymmetric_affine \\
    --qtype int8 \\
    --rebuild

Step 4: Export to Vivante .nbg Binary Executable Graph
------------------------------------------------------
$ pegasus exportovxlib \\
    --model models/yolov8n_crack.json \\
    --model-data models/yolov8n_crack.data \\
    --dtype quantized \\
    --output-path models/yolov8n_crack.nbg \\
    --pack-nbg-unify

Place the generated `models/yolov8n_crack.nbg` in your project folder!
Set `backend: "npu_viplite"` in `config.yaml` to run on the NPU hardware.
""")
    print("="*80 + "\n")

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Export YOLOv8 model for Orange Pi 4 Pro")
    parser.add_argument("--weights", type=str, default="yolov8n.pt", help="Path to input PyTorch .pt weights")
    parser.add_argument("--outdir", type=str, default="models", help="Output directory")
    args = parser.parse_args()

    exported_onnx = export_yolov8(weights=args.weights, output_dir=args.outdir)
    if exported_onnx:
        print_acuity_npu_instructions(exported_onnx)

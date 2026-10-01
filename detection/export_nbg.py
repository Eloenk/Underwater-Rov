#!/usr/bin/env python3

import os
import sys
import argparse
import subprocess
import logging

logging.basicConfig(level=logging.INFO, format='%(asctime)s [%(levelname)s] %(message)s')
logger = logging.getLogger("NBGExporter")

def download_and_export_onnx(model_name="yolov8n.pt", output_dir="detection/models"):
    os.makedirs(output_dir, exist_ok=True)
    target_onnx = os.path.join(output_dir, "yolov8n_crack.onnx")

    logger.info(f"Downloading/loading base YOLO model: {model_name}")
    try:
        from ultralytics import YOLO
        model = YOLO(model_name)
        
        logger.info("Exporting PyTorch model to ONNX format (opset 12, imgsz 640)...")
        exported_path = model.export(format="onnx", imgsz=640, dynamic=False, opset=12)
        
        if os.path.exists(exported_path) and exported_path != target_onnx:
            os.rename(exported_path, target_onnx)
            
        logger.info(f"ONNX model ready at: {target_onnx}")
        return target_onnx
    except Exception as e:
        logger.error(f"Failed to export ONNX model: {e}")
        return None

def compile_nbg(onnx_path="detection/models/yolov8n_crack.onnx", output_dir="detection/models"):
    target_data = os.path.join(output_dir, "yolov8n_crack.data")
    target_json = os.path.join(output_dir, "yolov8n_crack.json")
    dataset_txt = os.path.join(output_dir, "dataset.txt")
    target_nbg = os.path.join(output_dir, "yolov8n_crack.nbg")

    logger.info("Checking ACUITY Pegasus toolchain availability...")
    pegasus_status = subprocess.call(["which", "pegasus"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if pegasus_status != 0:
        logger.warning("'pegasus' CLI tool not found in PATH. Ensure ACUITY Toolkit environment is loaded.")
        print("\nManual Pegasus Compilation Commands:")
        print(f"1. pegasus importonnx --model {onnx_path} --output-data {target_data} --output-model {target_json}")
        print(f"2. pegasus generateinputs --model {target_json} --output-dataset {dataset_txt}")
        print(f"3. pegasus quantize --model {target_json} --model-data {target_data} --device npu --quantizer asymmetric_affine --qtype int8 --rebuild")
        print(f"4. pegasus exportovxlib --model {target_json} --model-data {target_data} --dtype quantized --output-path {target_nbg} --pack-nbg-unify\n")
        return False

    cmd1 = ["pegasus", "importonnx", "--model", onnx_path, "--output-data", target_data, "--output-model", target_json]
    logger.info("Step 1: Running pegasus importonnx...")
    subprocess.check_call(cmd1)

    cmd2 = ["pegasus", "generateinputs", "--model", target_json, "--output-dataset", dataset_txt]
    logger.info("Step 2: Running pegasus generateinputs...")
    subprocess.check_call(cmd2)

    cmd3 = ["pegasus", "quantize", "--model", target_json, "--model-data", target_data, "--device", "npu", "--quantizer", "asymmetric_affine", "--qtype", "int8", "--rebuild"]
    logger.info("Step 3: Running pegasus quantize...")
    subprocess.check_call(cmd3)

    cmd4 = ["pegasus", "exportovxlib", "--model", target_json, "--model-data", target_data, "--dtype", "quantized", "--output-path", target_nbg, "--pack-nbg-unify"]
    logger.info("Step 4: Running pegasus exportovxlib...")
    subprocess.check_call(cmd4)

    logger.info(f"NPU compilation successfully completed: {target_nbg}")
    return True

def main():
    parser = argparse.ArgumentParser(description="Automated NBG Exporter for Linux Server")
    parser.add_argument("--model", type=str, default="yolov8n.pt", help="PyTorch model weights or HuggingFace ID")
    parser.add_argument("--outdir", type=str, default="detection/models", help="Output directory")
    args = parser.parse_args()

    onnx_file = download_and_export_onnx(model_name=args.model, output_dir=args.outdir)
    if onnx_file:
        compile_nbg(onnx_path=onnx_file, output_dir=args.outdir)

if __name__ == "__main__":
    main()

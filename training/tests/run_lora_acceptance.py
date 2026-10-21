"""Fedora-only real LoRA workload acceptance, without control-plane substitution."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "finetuning"))
from configuration import canonical, digest, projection

VECTORS = {
    "A": (64, projection("0.0001", 20)),
    "B": (64, projection("0.0002", 10)),
    "C": (
        17,
        projection(
            "0.0003",
            7,
            per_device_train_batch_size=2,
            gradient_accumulation_steps=3,
            cutoff_len=96,
            lora_rank=4,
            lora_alpha=7,
            lora_dropout="0.125",
            seed=7,
        ),
    ),
}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--materials", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--image", required=True)
    parser.add_argument("--vector", choices=VECTORS, required=True)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=False)
    rows, configuration = VECTORS[args.vector]
    workspace = args.output / "workspace"
    data_dir = workspace / "inputs/data"
    data_dir.mkdir(parents=True)
    (workspace / "inputs/model").mkdir()
    base_manifest = canonical(json.loads((args.materials / "base_manifest.json").read_bytes()))
    (workspace / "inputs/base_manifest.json").write_bytes(base_manifest)
    dataset = json.loads((args.materials / "finetune_data.json").read_bytes())[:rows]
    if args.vector == "C":
        dataset[0]["input"] = (
            "A longer input to exercise real tokenizer truncation. " * 100
        )
    data = canonical(dataset)
    (data_dir / "finetune_data.json").write_bytes(data)
    shutil.copyfile(
        args.materials / "dataset_info.json", data_dir / "dataset_info.json"
    )
    (workspace / "inputs/finetune_configuration.json").write_bytes(
        canonical(configuration)
    )
    if not re.fullmatch(r"(?:sha256:)?[a-f0-9]{64}", args.image):
        parser.error("--image must be an already verified immutable image ID")
    image = args.image
    source = {
        str(p.relative_to(args.source)): hashlib.sha256(p.read_bytes()).hexdigest()
        for root in (args.source / "training", args.source / "contract/cpup01")
        for p in sorted(root.rglob("*"))
        if p.is_file() and "__pycache__" not in p.parts
    }
    record = {
        "vector": args.vector,
        "runtime_image_id": image,
        "source_files": source,
        "configuration": configuration,
        "configuration_sha256": digest(configuration),
        "input_sha256": hashlib.sha256(data).hexdigest(),
        "input_bytes": len(data),
        "source_samples": rows,
        "base_manifest_sha256": hashlib.sha256(base_manifest).hexdigest(),
        "roles": {},
    }
    common = [
        "podman",
        "run",
        "--rm",
        "--network=none",
        "--read-only",
        "--cpus=2",
        "--memory=7g",
        "--security-opt",
        "label=disable",
        "--userns=keep-id",
        "--user",
        "1000:1000",
        "--tmpfs",
        "/tmp:rw,size=512m",
        "-e",
        "OMP_NUM_THREADS=2",
        "-e",
        "MKL_NUM_THREADS=2",
        "-v",
        f"{args.source}:/source:ro",
        "-v",
        f"{workspace}:/workspace:rw",
        "-v",
        f"{args.materials / 'qwen2.5-0.5b-instruct'}:/workspace/inputs/model:ro",
        "--entrypoint",
        "/opt/venv/bin/python",
        image,
        "-I",
        "/source/training/finetuning/finetune_entry.py",
    ]
    for role in ("train", "export", "verify"):
        command = common + [
            role,
            "--configuration",
            "/workspace/inputs/finetune_configuration.json",
            "--workspace",
            "/workspace",
            "--expected-input-sha256",
            record["input_sha256"],
            "--expected-input-bytes",
            str(len(data)),
            "--expected-base-manifest-sha256",
            record["base_manifest_sha256"],
        ]
        print(f"{args.vector}: {role}", flush=True)
        with (args.output / f"{role}.log").open("w") as log:
            result = subprocess.run(
                command, stdout=log, stderr=subprocess.STDOUT, timeout=3600, check=False
            )
        record["roles"][role] = {"argv": command, "exit_code": result.returncode}
        (args.output / "record.json").write_text(json.dumps(record, indent=2) + "\n")
        if result.returncode:
            print((args.output / f"{role}.log").read_text()[-5000:], flush=True)
            return result.returncode
    detached = args.output / "detached"
    subprocess.run(
        ["cp", "-a", "--reflink=auto", str(workspace / "export/merged"), str(detached)],
        check=True,
    )
    command = [
        "podman",
        "run",
        "--rm",
        "--network=none",
        "--read-only",
        "--cpus=2",
        "--memory=4g",
        "--security-opt",
        "label=disable",
        "--userns=keep-id",
        "--user",
        "1000:1000",
        "--tmpfs",
        "/tmp:rw,size=256m",
        "-v",
        f"{args.source}:/source:ro",
        "-v",
        f"{detached}:/model:ro",
        "--entrypoint",
        "/opt/venv/bin/python",
        image,
        "-I",
        "/source/training/tests/reload_merged.py",
        "/model",
    ]
    with (args.output / "detached-reload.log").open("w") as log:
        result = subprocess.run(
            command, stdout=log, stderr=subprocess.STDOUT, timeout=300, check=False
        )
    record["roles"]["detached_reload"] = {
        "argv": command,
        "exit_code": result.returncode,
    }
    record["train_summary"] = json.loads(
        (workspace / "reports/train/summary.json").read_bytes()
    )
    record["merged_manifest"] = json.loads(
        (workspace / "reports/export/merged_manifest.json").read_bytes()
    )
    record["verify"] = json.loads(
        (workspace / "reports/verify/verify.json").read_bytes()
    )
    (args.output / "record.json").write_text(json.dumps(record, indent=2) + "\n")
    print(f"{args.vector}: exit {result.returncode}", flush=True)
    return result.returncode


if __name__ == "__main__":
    sys.exit(main())

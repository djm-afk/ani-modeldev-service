"""Fedora-only persistent MLP workload and detached-consumer evidence."""

import argparse
import csv
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys

from dataset_fixture import manifest_arguments
from image_runtime import PYTHON, TRAINING, podman_command, work_arguments


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--image", required=True)
    parser.add_argument("--nondefault", action="store_true")
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=False)
    if not re.fullmatch(r"(?:sha256:)?[a-f0-9]{64}", args.image):
        parser.error("--image must be an already verified immutable image ID")
    image = args.image
    podman = podman_command()
    common = podman + [
        "run",
        "--rm",
        "--network=none",
        "--read-only",
        "--read-only-tmpfs=false",
        "--cpus",
        "1" if args.nondefault else "2",
        "--memory=3g",
        "--security-opt",
        "label=disable",
        "--userns=keep-id:uid=10001,gid=10001",
        "--user",
        "10001:10001",
        "--passwd=false",
        "-v",
        f"{args.source}:/source:ro",
        "-v",
        f"{args.output}:/work:rw",
        "--entrypoint",
        PYTHON,
        image,
        "-I",
    ]
    index = common.index("--entrypoint")
    common = common[:index] + work_arguments(args.output / "work/train", "train") + common[index:]
    if args.nondefault:
        with (args.output / "input.csv").open("w", newline="") as stream:
            writer = csv.writer(stream)
            writer.writerow([f"x{i}" for i in range(16)] + ["label"])
            writer.writerows([[0] * 16 + [i % 2] for i in range(17)])
    else:
        subprocess.run(
            common + ["/source/training/make_data.py", "--output", "/work/input.csv"],
            check=True,
        )
    inputs = args.output / "inputs"
    inputs.mkdir()
    data = inputs / "data.csv"
    (args.output / "input.csv").replace(data)
    manifest_args = manifest_arguments(data)
    manifest_sha = manifest_args[-1]
    index = common.index("--entrypoint")
    training_common = common[:index] + ["-v", f"{inputs}:/work/inputs:ro,Z"] + common[index:]
    command = training_common + [
        TRAINING + "/train_mlp.py", "--dataset-manifest", "/work/inputs/dataset-manifest.json",
        "--expected-dataset-manifest-sha256", manifest_sha, "--work-root", "/workspace/work/train",
        "--output", "/work/result",
    ]
    if args.nondefault:
        command += [
            "--epochs",
            "2",
            "--batch-size",
            "6",
            "--learning-rate",
            "1000",
            "--seed",
            "7",
        ]
    with (args.output / "training.log").open("w") as log:
        result = subprocess.run(
            command, stdout=log, stderr=subprocess.STDOUT, timeout=180
        )
    record = {
        "source": {
            str(p.relative_to(args.source)): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in sorted((args.source / "training").rglob("*"))
            if p.is_file() and "__pycache__" not in p.parts
        },
        "runtime_image_id": image,
        "argv": command,
        "exit_code": result.returncode,
    }
    if result.returncode == 0:
        record["summary"] = json.loads(
            (args.output / "result/summary.json").read_bytes()
        )
        record["candidate"] = json.loads(
            (args.output / "result/result.json").read_bytes()
        )
        subprocess.run(
            [
                "cp",
                "-a",
                "--reflink=auto",
                str(args.output / "result"),
                str(args.output / "detached"),
            ],
            check=True,
        )
        detached_command = podman + [
            "run",
            "--rm",
            "--network=none",
            "--read-only",
            "--read-only-tmpfs=false",
            "--cpus=1",
            "--memory=2g",
            "--security-opt",
            "label=disable",
            "--userns=keep-id:uid=10001,gid=10001",
            "--user",
            "10001:10001",
            "--passwd=false",
            "-v",
            f"{args.source}:/source:ro",
            "-v",
            f"{args.output / 'detached'}:/detached:ro",
            "--entrypoint",
            PYTHON,
            image,
            "-I",
            "/source/training/tests/reload_checkpoint.py",
            "/detached",
        ]
        index = detached_command.index("--entrypoint")
        detached_command = detached_command[:index] + work_arguments(args.output / "detached-work", "verify") + detached_command[index:]
        reload = subprocess.run(
            detached_command, capture_output=True, text=True, timeout=180
        )
        record["detached_reload"] = {
            "argv": detached_command,
            "exit_code": reload.returncode,
            "stdout": reload.stdout,
            "stderr": reload.stderr,
        }
        result.returncode = reload.returncode
    (args.output / "record.json").write_text(json.dumps(record, indent=2) + "\n")
    print(
        json.dumps(
            {
                "output": str(args.output),
                "exit_code": result.returncode,
                "summary": record.get("summary"),
            }
        )
    )
    return result.returncode


if __name__ == "__main__":
    sys.exit(main())

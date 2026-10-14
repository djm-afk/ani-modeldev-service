#!/usr/bin/env python3
"""Fedora M0 driver: execute each role in the actual locked image with limits."""

import argparse
import json
from pathlib import Path
import subprocess
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--image", required=True)
    parser.add_argument("--vector", choices=("A", "B"), required=True)
    args = parser.parse_args()
    materials = json.loads(
        (args.directory / "materials/candidate-materials.json").read_text()
    )
    workspace = args.directory / "results" / args.vector
    image_id = subprocess.check_output(
        ["podman", "image", "inspect", args.image, "--format", "{{.Id}}"], text=True
    ).strip()
    result = {
        "scope": "WORKLOAD",
        "host": "fedora",
        "image_id": image_id,
        "vector": args.vector,
        "roles": [],
    }
    for role, seconds in (("train", 3600), ("export", 1800), ("verify", 900)):
        command = [
            "podman",
            "run",
            "--rm",
            "--network=none",
            "--read-only",
            "--cap-drop=ALL",
            "--security-opt=no-new-privileges",
            "--userns=keep-id:uid=10001,gid=10001",
            "--memory=8g",
            "--cpus=4",
            "--tmpfs=/tmp:rw,size=4g",
            "-v",
            f"{workspace}:/workspace:rw,Z",
            image_id,
            role,
            "--configuration",
            "/workspace/inputs/finetune_configuration.json",
            "--workspace",
            "/workspace",
            "--expected-input-sha256",
            materials["input_sha256"],
            "--expected-input-bytes",
            materials["input_bytes"],
            "--expected-base-manifest-sha256",
            materials["base_manifest_sha256"],
        ]
        started = time.monotonic()
        with (args.directory / "results" / f"{args.vector}-{role}.log").open(
            "x"
        ) as log:
            completed = subprocess.run(
                command,
                stdout=log,
                stderr=subprocess.STDOUT,
                timeout=seconds,
                check=False,
            )
        result["roles"].append(
            {
                "role": role,
                "command": command,
                "exit_code": completed.returncode,
                "elapsed_seconds": time.monotonic() - started,
            }
        )
        (args.directory / "results" / f"{args.vector}-workload.json").write_text(
            json.dumps(result, indent=2) + "\n"
        )
        if completed.returncode:
            return completed.returncode
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

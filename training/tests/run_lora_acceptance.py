"""Fedora CPU workload proof using current facts from the real Prepare chain."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys

from image_runtime import PYTHON, TRAINING, podman_command, work_arguments


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--prepared-workspace", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--image", required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", args.image):
        parser.error("--image must be an immutable local image ID")
    if not args.output.is_absolute() or args.output.exists():
        parser.error("--output must be a new absolute directory")
    prepared = args.prepared_workspace.resolve(strict=True)
    required = ("dataset-manifest.json", "base_manifest.json", "finetune_configuration.json",
                "template-plan.json", "runtime-capabilities.json")
    for name in required:
        path = prepared / "inputs" / name
        if not path.is_file() or path.is_symlink():
            parser.error("a current Prepare workspace with frozen facts is required")
    configuration = json.loads((prepared / "inputs/finetune_configuration.json").read_bytes())
    if configuration["managed_settings"]["device"] != "cpu":
        parser.error("this Fedora driver proves CPU workload execution; use the platform for CUDA")
    args.output.mkdir(mode=0o700)
    workspace = args.output / "workspace"
    subprocess.run(["cp", "-a", "--reflink=always", str(prepared), str(workspace)], check=True)
    for name in ("training", "export", "reports/train", "reports/export", "reports/verify"):
        path = workspace / name
        path.mkdir(parents=True, exist_ok=True)
        if any(path.iterdir()):
            raise ValueError("Prepare workspace must contain no previous workload outputs")
    frozen = {name: hashlib.sha256((workspace / "inputs" / name).read_bytes()).hexdigest()
              for name in required}
    podman = podman_command()
    common = podman + ["run", "--rm", "--pull=never", "--network=none",
        "--read-only", "--read-only-tmpfs=false", "--cpus=2", "--memory=7g",
        "--pids-limit=256", "--cap-drop=all", "--security-opt=no-new-privileges",
        "--userns=keep-id:uid=10001,gid=10001", "--user=10001:10001", "--passwd=false"]
    record = {"runtime_image_id": args.image, "frozen_file_sha256": frozen,
              "prepared_workspace": str(prepared), "roles": {},
              "scope": "CPU workload and detached reload; service acceptance is separate"}
    for role in ("train", "export", "verify"):
        arguments = common + ["-v", f"{workspace}:/workspace:ro,Z",
            "-v", f"{workspace / 'reports' / role}:/workspace/reports/{role}:rw,Z"]
        if role in ("train", "export"):
            writable = "training" if role == "train" else "export"
            arguments += ["-v", f"{workspace / writable}:/workspace/{writable}:rw,Z"]
        arguments += work_arguments(workspace / "work" / role, role)
        arguments += ["--entrypoint", PYTHON, args.image, "-I",
            TRAINING + "/finetuning/finetune_entry.py", role,
            "--configuration", "/workspace/inputs/finetune_configuration.json",
            "--workspace", "/workspace", "--dataset-manifest", "/workspace/inputs/dataset-manifest.json",
            "--expected-dataset-manifest-sha256", frozen["dataset-manifest.json"],
            "--expected-base-manifest-sha256", frozen["base_manifest.json"],
            "--expected-template-plan-sha256", frozen["template-plan.json"]]
        print(role, flush=True)
        with (args.output / (role + ".log")).open("w") as output:
            result = subprocess.run(arguments, stdout=output, stderr=subprocess.STDOUT,
                                    timeout=3600, check=False)
        record["roles"][role] = {"argv": arguments, "exit_code": result.returncode}
        (args.output / "record.json").write_text(json.dumps(record, indent=2) + "\n")
        if result.returncode:
            return result.returncode
    detached = args.output / "detached"
    subprocess.run(["cp", "-a", "--reflink=always", str(workspace / "export/merged"),
                    str(detached)], check=True)
    command = common + work_arguments(args.output / "detached-work", "verify") + [
        "-v", f"{args.source / 'training/tests/reload_merged.py'}:/tool/reload_merged.py:ro,Z",
        "-v", f"{detached}:/model:ro,Z", "--entrypoint", PYTHON, args.image,
        "-I", "/tool/reload_merged.py", "/model"]
    with (args.output / "detached-reload.log").open("w") as output:
        result = subprocess.run(command, stdout=output, stderr=subprocess.STDOUT,
                                timeout=300, check=False)
    record["roles"]["detached_reload"] = {"argv": command, "exit_code": result.returncode}
    for role, relative in (("train_summary", "reports/train/summary.json"),
                           ("merged_manifest", "reports/export/merged_manifest.json"),
                           ("verify", "reports/verify/verify.json")):
        record[role] = json.loads((workspace / relative).read_bytes())
    for name, expected in frozen.items():
        if hashlib.sha256((workspace / "inputs" / name).read_bytes()).hexdigest() != expected:
            raise ValueError("frozen Prepare input changed")
    (args.output / "record.json").write_text(json.dumps(record, indent=2) + "\n")
    return result.returncode


if __name__ == "__main__":
    sys.exit(main())

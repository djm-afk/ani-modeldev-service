#!/usr/bin/env python3
"""Fedora process-boundary SIGTERM probe; does not stand in for biz Stop LIVE."""

import argparse
import json
from pathlib import Path
import shutil
import subprocess
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--image", required=True)
    args = parser.parse_args()
    directory = args.directory
    materials = json.loads(
        (directory / "materials/candidate-materials.json").read_text()
    )
    workspace = directory / "results/STOP"
    if workspace.exists():
        raise FileExistsError("refusing to overwrite an existing stop attempt")
    (workspace / "inputs/model").mkdir(parents=True)
    (workspace / "inputs/data").mkdir()
    for path in (directory / "results/A/inputs/model").iterdir():
        (workspace / "inputs/model" / path.name).hardlink_to(path)
    for filename in ("base_manifest.json", "finetune_configuration.json"):
        shutil.copyfile(
            directory / "results/A/inputs" / filename, workspace / "inputs" / filename
        )
    for filename in ("finetune_data.json", "dataset_info.json"):
        shutil.copyfile(
            directory / "results/A/inputs/data" / filename,
            workspace / "inputs/data" / filename,
        )
    image_id = subprocess.check_output(
        ["podman", "image", "inspect", args.image, "--format", "{{.Id}}"], text=True
    ).strip()
    name = "ani-ft-workload-partial-20261014"
    command = [
        "podman",
        "run",
        "--rm",
        "--name",
        name,
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
        "train",
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
    signaled = False
    requested_after_step = None
    with (directory / "results/STOP-train.log").open("x") as log:
        process = subprocess.Popen(command, stdout=log, stderr=subprocess.STDOUT)
        metrics = workspace / "reports/train/metrics.jsonl"
        while process.poll() is None:
            if time.monotonic() - started > 180:
                subprocess.run(
                    ["podman", "kill", "--signal", "TERM", name], check=False
                )
                process.wait(timeout=30)
                raise TimeoutError("probe did not reach three real metric events")
            if metrics.exists():
                rows = [
                    json.loads(line)
                    for line in metrics.read_text().splitlines()
                    if line
                ]
                if rows and rows[-1]["step"] >= 3:
                    requested_after_step = rows[-1]["step"]
                    subprocess.run(
                        ["podman", "kill", "--signal", "TERM", name], check=True
                    )
                    signaled = True
                    break
            time.sleep(0.2)
        exit_code = process.wait(timeout=30)
    summary = json.loads((workspace / "reports/train/summary.json").read_text())
    result = {
        "scope": "WORKLOAD",
        "boundary": "SIGTERM_PROCESS_ONLY",
        "does_not_cover": "BIZ_STOP_LIVE",
        "image_id": image_id,
        "command": command,
        "signal": "SIGTERM",
        "requested_after_metric_step": requested_after_step,
        "exit_code": exit_code,
        "elapsed_seconds": time.monotonic() - started,
        "summary": summary,
    }
    (directory / "results/STOP-workload.json").write_text(
        json.dumps(result, indent=2) + "\n"
    )
    if (
        not signaled
        or exit_code != 143
        or summary["status"] != "CANCELLED"
        or not 0 < summary["optimizer_steps"] < 20
    ):
        raise ValueError("actual process termination/partial optimizer facts disagree")
    print(
        json.dumps(
            {
                "scope": "WORKLOAD",
                "boundary": result["boundary"],
                "exit_code": exit_code,
                "actual_optimizer_steps": summary["optimizer_steps"],
                "requested_after_metric_step": requested_after_step,
            }
        )
    )


if __name__ == "__main__":
    main()

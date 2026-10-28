"""Observe the official baseline and both final images without training weights."""

import argparse
import json
from pathlib import Path
import re
import subprocess

from image_runtime import PYTHON, TRAINING, podman_command, work_arguments

BEFORE = "c596efb447fb50fc915f6aa5c4f228d5409fba83808e23015a1bcf3387a3ffbf"
AFTER = "45af44a2a832ddcb144017fcf49916ca6b8e48d6e00f2208a763bd83e346d52a"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline-image", required=True)
    parser.add_argument("--cpu-image", required=True)
    parser.add_argument("--gpu-image", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    images = {"baseline": args.baseline_image, "cpu": args.cpu_image, "gpu": args.gpu_image}
    if any(not re.fullmatch(r"sha256:[a-f0-9]{64}", value) for value in images.values()):
        parser.error("all images must be immutable local image IDs")
    if not args.output.is_absolute() or args.output.exists():
        parser.error("--output must be a new absolute directory")
    source = Path(__file__).resolve().parents[2]
    revision = subprocess.check_output(["git", "-C", str(source), "rev-parse", "HEAD"], text=True).strip()
    podman = podman_command()
    for mode in ("cpu", "gpu"):
        image = json.loads(subprocess.check_output(podman + ["image", "inspect", images[mode]]))[0]
        labels = image["Config"]["Labels"]
        if labels["org.opencontainers.image.revision"] != revision or labels["ani.modeldev.device_mode"] != mode:
            raise ValueError("final image source or mode differs from this test source")
    args.output.mkdir(mode=0o700)
    tool = source / "training/tests/arrow_mechanism.py"
    common = podman + ["run", "--rm", "--pull=never", "--network=none",
        "--read-only", "--read-only-tmpfs=false", "--cpus=2", "--memory=5g",
        "--memory-swap=5g", "--pids-limit=256", "--cap-drop=all",
        "--security-opt=no-new-privileges", "--userns=keep-id:uid=10001,gid=10001",
        "--user=10001:10001", "--passwd=false", "--http-proxy=false",
        "-v", f"{tool}:/tool/arrow_mechanism.py:ro,Z"]
    records = {"source_revision": revision, "scope": "native conversion and tokenization mechanism; no optimizer or live acceptance", "runs": {}}
    for mode, image in images.items():
        workspace = args.output / mode
        workspace.mkdir()
        work = workspace / "work/train"
        mounting = work_arguments(work, "train")
        # Only the unmodified official baseline receives the platform module
        # as a read-only test instrument. Final images use their baked modules.
        platform = ["-v", f"{source / 'training'}:{TRAINING}:ro,Z"] if mode == "baseline" else []
        create = common + ["-v", f"{workspace}:/workspace:rw,Z"] + mounting + platform + [
            "--entrypoint", PYTHON, image, "-I", "/tool/arrow_mechanism.py", "--create-fixture"]
        # Parent first, writable work child second, so measurement cannot alter
        # the fixture while retaining the production role-cache layout.
        measure = common + ["-v", f"{workspace}:/workspace:ro,Z"] + mounting + platform + [
            "--entrypoint", PYTHON, image, "-I", "/tool/arrow_mechanism.py",
            "--expected-converter-sha256", BEFORE if mode == "baseline" else AFTER]
        for label, command in (("fixture", create), ("measure", measure)):
            with (args.output / f"{mode}-{label}.log").open("w") as output:
                result = subprocess.run(command, stdout=output, stderr=subprocess.STDOUT, timeout=600, check=False)
            if label == "fixture" and result.returncode:
                raise RuntimeError(f"{mode} fixture failed with exit {result.returncode}")
        report = json.loads((work / "arrow-mechanism.json").read_bytes())
        if mode == "baseline":
            valid = (result.returncode != 0 and report["state"] == "FAIL"
                and report["converter_sha256"] == BEFORE
                and report["conversion_peak_pending_records"] == 2
                and report["native_preprocessing"]["samples"] == 2)
        else:
            valid = result.returncode == 0 and report["state"] == "PASS" and report["converter_sha256"] == AFTER
        records["runs"][mode] = {"image_id": image, "exit_code": result.returncode,
            "expected_behavior_observed": valid, "measurement": report}
        (args.output / "result.json").write_text(json.dumps(records, indent=2) + "\n")
        if not valid:
            raise ValueError(f"{mode} did not demonstrate the expected Arrow writer behavior")
    records["state"] = "PASS"
    (args.output / "result.json").write_text(json.dumps(records, indent=2) + "\n")
    print("Actual baseline RED and both final-image mechanisms GREEN")


if __name__ == "__main__":
    main()

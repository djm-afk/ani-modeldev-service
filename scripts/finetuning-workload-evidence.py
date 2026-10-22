#!/usr/bin/env python3
"""Collect the native Fedora M0 records without replacing any workload fact."""

import argparse
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path


def read(path):
    return json.loads(path.read_text())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    args = parser.parse_args()
    results = args.directory / "results"
    evidence = {
        "scope": "WORKLOAD",
        "host": "fedora",
        "recorded_at": datetime.now(timezone.utc).isoformat(),
        "materials": read(args.directory / "materials/candidate-materials.json"),
        "vectors": {},
        "failed_attempts": [
            read(path) for path in sorted(results.glob("A-failed-*-workload.json"))
        ],
    }
    for vector in ("A", "B"):
        workspace = results / vector
        evidence["vectors"][vector] = {
            "driver": read(results / f"{vector}-workload.json"),
            "train": read(workspace / "reports/train/summary.json"),
            "adapter_manifest": read(workspace / "reports/train/adapter_manifest.json"),
            "export": read(workspace / "reports/export/export.json"),
            "merged_manifest": read(workspace / "reports/export/merged_manifest.json"),
            "verify": read(workspace / "reports/verify/verify.json"),
            "workspace_logical_bytes": sum(
                path.stat().st_size for path in workspace.rglob("*") if path.is_file()
            ),
        }
    a, b = evidence["vectors"]["A"], evidence["vectors"]["B"]
    evidence["assertions"] = {
        "same_immutable_image": a["driver"]["image_id"] == b["driver"]["image_id"],
        "all_roles_exit_zero": all(
            role["exit_code"] == 0 for v in (a, b) for role in v["driver"]["roles"]
        ),
        "actual_optimizer_limits": a["train"]["optimizer_steps"] == 20
        and b["train"]["optimizer_steps"] == 10,
        "actual_effective_samples": all(
            v["train"]["samples"] == v["train"]["source_samples"] == 64 for v in (a, b)
        ),
        "frozen_equals_framework_configuration": all(
            v["train"]["resolved_configuration_sha256"]
            == v["train"]["effective_configuration_sha256"]
            for v in (a, b)
        ),
        "same_fixed_input_and_base": a["train"]["input_sha256"]
        == b["train"]["input_sha256"]
        and a["train"]["base_manifest_sha256"] == b["train"]["base_manifest_sha256"],
        "float32_each_stage": all(
            v["train"]["train_dtypes"] == ["float32"]
            and v["train"]["adapter_dtype"]
            == v["export"]["dtype"]
            == v["verify"]["dtype"]
            == "float32"
            for v in (a, b)
        ),
        "actual_reload_and_text": all(
            v["verify"]["status"] == "PASSED"
            and v["verify"]["finite_logits"]
            and v["verify"]["generated_text"].strip()
            for v in (a, b)
        ),
        "same_output_structure": [f["path"] for f in a["merged_manifest"]["files"]]
        == [f["path"] for f in b["merged_manifest"]["files"]],
    }
    source = args.directory / "source"
    evidence["source_sha256"] = {
        path.relative_to(source).as_posix(): hashlib.sha256(
            path.read_bytes()
        ).hexdigest()
        for path in sorted((source / "training/finetuning").glob("*.py"))
    }
    (results / "workload-evidence.json").write_text(
        json.dumps(evidence, indent=2) + "\n"
    )
    print(
        json.dumps(
            {"scope": evidence["scope"], "assertions": evidence["assertions"]}, indent=2
        )
    )
    return 0 if all(evidence["assertions"].values()) else 1


if __name__ == "__main__":
    raise SystemExit(main())

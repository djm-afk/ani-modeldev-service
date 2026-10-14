#!/usr/bin/env python3
"""Prepare the deterministic M0 candidate on Fedora; never used for admission."""

import argparse
import hashlib
import json
from pathlib import Path
import shutil
import sys
import tarfile
import urllib.request

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "training/finetuning"))
from configuration import canonical, projection

REVISION = "7ae557604adf67be50417f59c2c2f167def9a775"
MODEL_FILES = (
    "LICENSE",
    "config.json",
    "generation_config.json",
    "merges.txt",
    "model.safetensors",
    "tokenizer.json",
    "tokenizer_config.json",
    "vocab.json",
)


def sha(path):
    result = hashlib.sha256()
    with path.open("rb") as source:
        while block := source.read(1024 * 1024):
            result.update(block)
    return result.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    args = parser.parse_args()
    materials = args.directory / "materials"
    base = materials / "qwen2.5-0.5b-instruct"
    base.mkdir(parents=True, exist_ok=True)
    for name in MODEL_FILES:
        target = base / name
        if target.exists():
            continue
        temporary = target.with_suffix(target.suffix + ".part")
        url = f"https://huggingface.co/Qwen/Qwen2.5-0.5B-Instruct/resolve/{REVISION}/{name}"
        print(f"download {name}", flush=True)
        with (
            urllib.request.urlopen(url, timeout=120) as source,
            temporary.open("wb") as output,
        ):
            shutil.copyfileobj(source, output, length=1024 * 1024)
        temporary.rename(target)
    if (
        sha(base / "model.safetensors")
        != "fdf756fa7fcbe7404d5c60e26bff1a0c8b8aa1f72ced49e7dd0210fe288fb7fe"
    ):
        raise ValueError("Qwen upstream model SHA mismatch")
    files = [
        {"path": path.name, "bytes": str(path.stat().st_size), "sha256": sha(path)}
        for path in sorted(base.iterdir())
    ]
    manifest = canonical({"schema": "ani.finetune.base-manifest.v1", "files": files})
    (materials / "base_manifest.json").write_bytes(manifest)
    archive = materials / "qwen2.5-0.5b-instruct.tar"
    if not archive.exists():
        with tarfile.open(archive, "w", format=tarfile.USTAR_FORMAT) as bundle:
            for path in sorted(base.iterdir()):
                info = bundle.gettarinfo(str(path), arcname=path.name)
                info.uid = info.gid = info.mtime = 0
                info.uname = info.gname = ""
                with path.open("rb") as source:
                    bundle.addfile(info, source)
    rows = [
        {
            "instruction": "Compute the sum and answer with the number.",
            "input": f"{i % 16} + {i // 16}",
            "output": str(i % 16 + i // 16),
        }
        for i in range(64)
    ]
    data = canonical(rows)
    (materials / "finetune_data.json").write_bytes(data)
    dataset_info = canonical(
        {
            "ani_cpu_smoke": {
                "file_name": "finetune_data.json",
                "formatting": "alpaca",
                "columns": {
                    "prompt": "instruction",
                    "query": "input",
                    "response": "output",
                },
            }
        }
    )
    (materials / "dataset_info.json").write_bytes(dataset_info)
    for name, rate, steps in (("A", "0.0001", 20), ("B", "0.0002", 10)):
        workspace = args.directory / "results" / name
        (workspace / "inputs/data").mkdir(parents=True, exist_ok=True)
        (workspace / "inputs/model").mkdir(parents=True, exist_ok=True)
        for filename, contents in (
            ("finetune_data.json", data),
            ("dataset_info.json", dataset_info),
        ):
            (workspace / "inputs/data" / filename).write_bytes(contents)
        (workspace / "inputs/base_manifest.json").write_bytes(manifest)
        (workspace / "inputs/finetune_configuration.json").write_bytes(
            canonical(projection(rate, steps))
        )
        for path in base.iterdir():
            target = workspace / "inputs/model" / path.name
            if not target.exists():
                target.hardlink_to(path)
    record = {
        "model_id": "Qwen/Qwen2.5-0.5B-Instruct",
        "revision": REVISION,
        "base_manifest_sha256": hashlib.sha256(manifest).hexdigest(),
        "base_archive_sha256": sha(archive),
        "base_archive_bytes": str(archive.stat().st_size),
        "input_sha256": hashlib.sha256(data).hexdigest(),
        "input_bytes": str(len(data)),
        "row_count": len(rows),
        "base_files": files,
    }
    (materials / "candidate-materials.json").write_text(
        json.dumps(record, indent=2) + "\n"
    )
    print(json.dumps(record), flush=True)


if __name__ == "__main__":
    main()

"""Image-test commands for the pinned official runtime and task-owned storage."""

import os
from pathlib import Path

PYTHON = "/opt/conda/bin/python"
TRAINING = "/opt/ani-modeldev/training"


def podman_command():
    paths = []
    for name in ("ANI_IMAGE_STORAGE_ROOT", "ANI_IMAGE_RUNROOT", "ANI_IMAGE_TMPDIR"):
        value = Path(os.environ[name])
        if not value.is_absolute() or not value.is_dir() or value.is_symlink():
            raise ValueError(f"{name} must name an existing task-owned directory")
        paths.append(str(value))
    return ["podman", "--root", paths[0], "--runroot", paths[1],
            "--storage-driver", "overlay", "--tmpdir", paths[2]]


def work_arguments(directory, role):
    if role not in ("train", "export", "verify"):
        raise ValueError("unknown managed role")
    for relative in ("tmp", "hf/datasets", "matplotlib"):
        (directory / relative).mkdir(parents=True, exist_ok=True)
    target = f"/workspace/work/{role}"
    return ["-v", f"{directory}:{target}:rw,Z",
            "--env", f"TMPDIR={target}/tmp", "--env", f"HF_HOME={target}/hf",
            "--env", f"HF_DATASETS_CACHE={target}/hf/datasets",
            "--env", f"MPLCONFIGDIR={target}/matplotlib"]

#!/usr/bin/env python3
"""Render one explicit content limit into both responsible service configs."""
import argparse
import json
import os
from pathlib import Path
import re
import shutil
import sys
import tempfile


LIMIT_ENV = "MODELDEV_MAX_CONTENT_BYTES"


def render(modeldev, provider, container_name, raw_limit):
    if not isinstance(raw_limit, str) or not re.fullmatch(r"[1-9][0-9]{0,18}", raw_limit):
        raise ValueError("explicit positive int64 content limit required")
    if int(raw_limit) > 9223372036854775807:
        raise ValueError("explicit positive int64 content limit required")
    if not isinstance(modeldev, dict) or not isinstance(provider, dict):
        raise ValueError("complete JSON config and Deployment required")
    runtime = modeldev.get("runtime")
    limits = runtime.get("limits") if isinstance(runtime, dict) else None
    if not isinstance(limits, dict) or "max_content_bytes" not in limits:
        raise ValueError("current runtime limits config required")
    if provider.get("apiVersion") != "apps/v1" or provider.get("kind") != "Deployment":
        raise ValueError("actual Model provider Deployment required")
    metadata = provider.get("metadata")
    if not isinstance(metadata, dict) or not metadata.get("name") or not metadata.get("namespace"):
        raise ValueError("explicit provider Deployment identity required")
    try:
        containers = provider["spec"]["template"]["spec"]["containers"]
    except (KeyError, TypeError):
        raise ValueError("provider containers required") from None
    if not isinstance(containers, list) or any(not isinstance(value, dict) for value in containers):
        raise ValueError("provider containers required")
    selected = [value for value in containers if value.get("name") == container_name]
    if not container_name or len(selected) != 1:
        raise ValueError("exactly one explicitly selected provider container required")
    container = selected[0]
    environment = container.get("env", [])
    if not isinstance(environment, list) or any(not isinstance(value, dict) or not isinstance(value.get("name"), str) for value in environment):
        raise ValueError("explicit provider environment entries required")
    positions = [index for index, value in enumerate(environment) if value["name"] == LIMIT_ENV]
    if len(positions) > 1:
        raise ValueError("duplicate provider content limits rejected")
    limits["max_content_bytes"] = raw_limit
    value = {"name": LIMIT_ENV, "value": raw_limit}
    if positions:
        environment[positions[0]] = value
    else:
        environment.append(value)
    container["env"] = environment
    return {
        "managed-runtime.yaml": (json.dumps(modeldev, indent=2) + "\n").encode(),
        "model-provider.json": (json.dumps(provider, indent=2) + "\n").encode(),
    }


def sync_directory(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def publish(output, files):
    if output.exists() or output.is_symlink() or not output.parent.is_dir():
        raise ValueError("new output directory and existing parent required")
    staging = Path(tempfile.mkdtemp(prefix=".modeldev-content-limits-", dir=output.parent))
    try:
        for name, content in files.items():
            descriptor = os.open(staging / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(descriptor, "wb") as stream:
                stream.write(content)
                stream.flush()
                os.fsync(stream.fileno())
        sync_directory(staging)
        if output.exists() or output.is_symlink():
            raise ValueError("output directory already exists")
        os.rename(staging, output)
        sync_directory(output.parent)
    finally:
        if staging.exists():
            shutil.rmtree(staging)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--modeldev-config", type=Path, required=True)
    parser.add_argument("--model-provider-deployment", type=Path, required=True)
    parser.add_argument("--model-provider-container", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        modeldev = json.loads(args.modeldev_config.read_bytes())
        provider = json.loads(args.model_provider_deployment.read_bytes())
        files = render(modeldev, provider, args.model_provider_container, os.environ.get(LIMIT_ENV))
        publish(args.output, files)
    except (OSError, ValueError, TypeError):
        # Configs can contain credentials. Do not print input bytes, exception
        # values, or environment values on validation or filesystem failures.
        print("content-limit rendering failed: check required value, JSON inputs, container and new output directory", file=sys.stderr)
        return 1
    print(json.dumps({"rendered": True, "deployed": False, "max_content_bytes": os.environ[LIMIT_ENV]}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

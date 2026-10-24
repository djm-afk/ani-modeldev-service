"""Train the built-in single-device MLP from a selected CSV.

The result is a workload candidate. This command has no control-plane clients
and does not mark an Execution successful or publish files to object storage.
"""

import argparse
import csv
from datetime import datetime, timezone
from decimal import Decimal
import hashlib
import io
import json
import math
import os
from pathlib import Path
import re
import signal
import stat
import sys
import time
import struct

# -I removes this image-owned script directory from sys.path.
sys.path.insert(0, str(Path(__file__).resolve().parent))
from implementation import defaults, implementation

RULE = implementation("builtin.mlp")


def checked_path(value):
    path = Path(os.path.abspath(value))
    for component in (*reversed(path.parents), path):
        if component.is_symlink():
            raise ValueError("symbolic links are not allowed in workload paths")
    return path


def learning_rate(value):
    if len(value) > RULE["decimal_max_characters"] or not re.fullmatch(
        r"(0|[1-9][0-9]*)(\.[0-9]+)?", value
    ):
        raise ValueError(
            "learning rate must be a non-exponent decimal with at most 32 characters"
        )
    decimal = Decimal(value)
    try:
        converted = struct.unpack("f", struct.pack("f", float(decimal)))[0]
    except (OverflowError, ValueError):
        converted = float("inf")
    if not math.isfinite(converted) or converted <= 0:
        raise ValueError("learning rate must remain positive and finite in float32")
    canonical = format(decimal, "f")
    if "." in canonical:
        canonical = canonical.rstrip("0").rstrip(".")
    return canonical, float(decimal)


def integer(value, *, minimum=1, maximum=int(RULE["integer_max"])):
    if (
        not re.fullmatch(r"0|[1-9][0-9]*", value)
        or not minimum <= int(value) <= maximum
    ):
        raise ValueError("integer is outside the implementation representation range")
    return int(value)


def check_output(path):
    if path.exists() and (not path.is_dir() or any(path.iterdir())):
        raise FileExistsError(
            "refusing to overwrite a nonempty or non-directory output path"
        )


def load_selected_input(path, expected_sha256, expected_bytes):
    if not re.fullmatch(r"[0-9a-f]{64}", expected_sha256):
        raise ValueError(
            "expected input SHA256 must be 64 lowercase hexadecimal characters"
        )
    if not 0 < expected_bytes <= 32 * 1024 * 1024:
        raise ValueError("expected input size must be between 1 byte and 32 MiB")
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, "rb") as stream:
        identity = os.fstat(stream.fileno())
        if not stat.S_ISREG(identity.st_mode):
            raise ValueError("selected input must be a regular file")
        if identity.st_size != expected_bytes:
            raise ValueError("selected input size does not match the frozen input")
        contents = stream.read(expected_bytes + 1)
    digest = hashlib.sha256(contents).hexdigest()
    if len(contents) != expected_bytes or digest != expected_sha256:
        raise ValueError("selected input bytes do not match the frozen input")

    rows = list(
        csv.reader(io.StringIO(contents.decode("utf-8"), newline=""), strict=True)
    )
    if len(rows) < 2:
        raise ValueError("MLP input requires a header and at least one sample")
    if rows[0] != [f"x{i}" for i in range(16)] + ["label"]:
        raise ValueError("fixed CPU input requires exactly x0 through x15 and label")
    features, labels = [], []
    for row in rows[1:]:
        if len(row) != 17 or row[-1] not in ("0", "1"):
            raise ValueError(
                "each sample requires 16 features and a binary integer label"
            )
        values = [float(value) for value in row[:-1]]
        if any(
            not math.isfinite(value) or abs(value) > 3.4028234663852886e38
            for value in values
        ):
            raise ValueError("features must be finite float32 values")
        features.append(values)
        labels.append(int(row[-1]))
    return contents, digest, features, labels


def write_json(path, value):
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")
    os.replace(temporary, path)


def recipe_deadline(_signal, _frame):
    raise TimeoutError("CPU03_RECIPE_DEADLINE: 180-second training budget expired")


def terminate(signum, _frame):
    # The container's PID 1 must explicitly handle SIGTERM. SystemExit unwinds
    # the open metrics file and reports nonzero termination to the owning runtime.
    raise SystemExit(128 + signum)


def main():
    signal.signal(signal.SIGTERM, terminate)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--data", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--expected-input-sha256", required=True)
    parser.add_argument("--expected-input-bytes", required=True, type=int)
    implementation_defaults = defaults("builtin.mlp")
    parser.add_argument(
        "--learning-rate", default=implementation_defaults["learning_rate"]
    )
    parser.add_argument("--epochs", default=str(implementation_defaults["epochs"]))
    parser.add_argument(
        "--batch-size", default=str(implementation_defaults["batch_size"])
    )
    parser.add_argument("--seed", default=str(implementation_defaults["seed"]))
    parser.add_argument("--device", choices=("cpu", "cuda"), default="cpu")
    parser.add_argument(
        "--recipe",
        choices=("success", "fail", "slow-stop"),
        default="success",
        help="registered managed recipe; never an ordinary user training parameter",
    )
    args = parser.parse_args()

    canonical_rate, optimizer_rate = learning_rate(args.learning_rate)
    epochs, batch_size = integer(args.epochs), integer(args.batch_size)
    seed = integer(args.seed, minimum=0, maximum=int(RULE["seed_max"]))
    if os.environ.get("WORLD_SIZE", "1") != "1":
        raise ValueError("the built-in MLP requires WORLD_SIZE=1")
    data = checked_path(args.data)
    output = checked_path(args.output)
    check_output(output)
    input_bytes, input_sha256, feature_values, label_values = load_selected_input(
        data,
        args.expected_input_sha256,
        args.expected_input_bytes,
    )
    if args.recipe == "slow-stop":
        signal.signal(signal.SIGALRM, recipe_deadline)
        signal.alarm(180)

    # Validate the entire selected input before importing the training runtime,
    # constructing tensors, or creating any output directory.
    import torch
    from torch import nn

    if args.device == "cuda" and (
        not torch.cuda.is_available() or torch.cuda.device_count() != 1
    ):
        raise ValueError("CUDA execution requires exactly one available CUDA device")
    features = torch.tensor(feature_values, dtype=torch.float32, device=args.device)
    labels = torch.tensor(label_values, dtype=torch.long, device=args.device)
    output.mkdir(parents=True, exist_ok=True)
    checked_path(output)
    check_output(output)

    torch.set_num_threads(1)
    torch.manual_seed(seed)
    if args.device == "cuda":
        torch.cuda.manual_seed_all(seed)
    model = nn.Sequential(nn.Linear(16, 32), nn.ReLU(), nn.Linear(32, 2)).to(
        args.device
    )
    optimizer = torch.optim.Adam(model.parameters(), lr=optimizer_rate)
    criterion = nn.CrossEntropyLoss()
    with torch.no_grad():
        initial_loss = float(criterion(model(features), labels))

    if args.recipe == "slow-stop":
        print(
            json.dumps(
                {
                    "schema": "ani.cpu03.recipe.v1",
                    "recipe": "slow-stop",
                    "max_steps": epochs
                    * ((len(features) + batch_size - 1) // batch_size),
                    "step_delay_seconds": 2,
                    "deadline_seconds": 180,
                }
            ),
            flush=True,
        )

    step = 0
    status, error, final_loss = "FAILED", None, None
    try:
        if not math.isfinite(initial_loss):
            raise ValueError("non-finite initial loss")
        with (output / "metrics.jsonl").open(
            "x", encoding="utf-8", buffering=1
        ) as metrics:
            for _ in range(epochs):
                for indices in torch.randperm(len(features), device=args.device).split(
                    batch_size
                ):
                    optimizer.zero_grad(set_to_none=True)
                    loss = criterion(model(features[indices]), labels[indices])
                    if not torch.isfinite(loss):
                        raise ValueError("non-finite training loss")
                    loss.backward()
                    optimizer.step()
                    step += 1
                    entry = {
                        "schema": "ani.metric.v1",
                        "step": step,
                        "name": "train.loss",
                        "value": float(loss.detach()),
                        "rank": 0,
                        "timestamp": datetime.now(timezone.utc).isoformat(),
                    }
                    line = json.dumps(entry)
                    metrics.write(line + "\n")
                    print(line, flush=True)
                    if args.recipe == "fail" and step == 5:
                        raise RuntimeError(
                            "CPU03_RECIPE_FAILURE: failed after five real optimizer steps"
                        )
                    if args.recipe == "slow-stop":
                        time.sleep(2)
        model.eval()
        with torch.no_grad():
            final_loss = float(criterion(model(features), labels))
        if not math.isfinite(final_loss) or any(
            not torch.isfinite(p).all() for p in model.parameters()
        ):
            raise ValueError("non-finite final loss or model weights")
        checkpoint_temporary = output / "model.pt.tmp"
        torch.save(model.state_dict(), checkpoint_temporary)
        os.replace(checkpoint_temporary, output / "model.pt")
        write_json(
            output / "model_config.json",
            {
                "architecture": "mlp-16-32-2",
                "input_dim": 16,
                "hidden_dim": 32,
                "output_dim": 2,
                "dtype": "float32",
            },
        )
        status = "SUCCEEDED"
    except BaseException as failure:
        error = f"{type(failure).__name__}: {failure}"
        if isinstance(failure, SystemExit):
            status = "CANCELLED"
        raise
    finally:
        write_json(
            output / "summary.json",
            {
                "status": status,
                "error": error,
                "samples": len(feature_values),
                "epochs": epochs,
                "batch_size": batch_size,
                "learning_rate": canonical_rate,
                "seed": seed,
                "steps": step,
                "world_size": 1,
                "device": next(model.parameters()).device.type,
                "torch_version": torch.__version__,
                "input_sha256": input_sha256,
                "input_bytes": str(len(input_bytes)),
                "initial_loss": initial_loss if math.isfinite(initial_loss) else None,
                "final_loss": final_loss
                if final_loss is None or math.isfinite(final_loss)
                else None,
            },
        )
    files = []
    for name in ("model.pt", "model_config.json", "metrics.jsonl", "summary.json"):
        contents = (output / name).read_bytes()
        files.append(
            {
                "path": name,
                "bytes": str(len(contents)),
                "sha256": hashlib.sha256(contents).hexdigest(),
            }
        )
    write_json(
        output / "result.json",
        {
            "schema": "ani.output-candidate.v1",
            "input_sha256": input_sha256,
            "deployable": False,
            "files": files,
        },
    )
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as error:
        print(f"CPU training failed: {type(error).__name__}: {error}", file=sys.stderr)
        sys.exit(1)

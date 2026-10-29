"""Train the built-in single-device MLP from a selected CSV.

The result is a workload candidate. This command has no control-plane clients
and does not mark an Execution successful or publish files to object storage.
"""

import argparse
from contextlib import contextmanager, ExitStack
import csv
from datetime import datetime, timezone
from decimal import Decimal
import hashlib
import json
import math
import mmap
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
from dataset_manifest import (
    load_dataset_manifest as load_frozen_dataset_manifest,
    MAX_RECORD_BYTES, MAX_MANIFEST_BYTES,
)
DECIMAL_FEATURE = re.compile(r"[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?")


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


def unique_fields(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError(f"duplicate manifest field: {key}")
        value[key] = item
    return value


def regular_file(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    stream = os.fdopen(descriptor, "rb")
    if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
        stream.close()
        raise ValueError("workload inputs must be regular files")
    return stream


def load_dataset_manifest(path, expected_sha256):
    return load_frozen_dataset_manifest(path, expected_sha256, "CSV")


class CSVRecords:
    """Bound physical reads while csv.reader recognizes a logical record."""

    def __init__(self, stream):
        self.stream = stream
        self.digest = hashlib.sha256()
        self.size = 0
        self.record_bytes = 0

    def __iter__(self):
        return self

    def __next__(self):
        raw = self.stream.readline(MAX_RECORD_BYTES - self.record_bytes + 1)
        if not raw:
            raise StopIteration
        self.record_bytes += len(raw)
        if self.record_bytes > MAX_RECORD_BYTES:
            raise ValueError(f"MaxRecordBytes exceeded: actual={self.record_bytes} limit={MAX_RECORD_BYTES}")
        self.size += len(raw)
        self.digest.update(raw)
        return raw.decode("utf-8")


def checked_payload(rows, width):
    if rows > int(RULE["integer_max"]) // width:
        raise ValueError("training mmap byte count overflows int64")
    return rows * width


@contextmanager
def selected_input_maps(path, manifest, work):
    rows = int(manifest["row_count"])
    sizes = {"features.f32": checked_payload(rows, 64),
             "labels.i64": checked_payload(rows, 8),
             "order.i64": checked_payload(rows, 8)}
    if sys.byteorder != "little":
        raise ValueError("the current MLP runtime requires little-endian storage")
    work.mkdir(mode=0o700, parents=True, exist_ok=True)
    checked_path(work)
    paths = []
    try:
        with ExitStack() as stack:
            maps = {}
            for name, size in sizes.items():
                target = work / name
                descriptor = os.open(target, os.O_RDWR | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
                paths.append(target)
                stream = stack.enter_context(os.fdopen(descriptor, "r+b"))
                stream.truncate(size)
                maps[name] = stack.enter_context(mmap.mmap(stream.fileno(), size))
            with regular_file(path) as stream:
                if os.fstat(stream.fileno()).st_size != int(manifest["prepared_size_bytes"]):
                    raise ValueError("selected input size does not match the frozen input")
                lines = CSVRecords(stream)
                csv.field_size_limit(MAX_RECORD_BYTES)
                records = csv.reader(lines, strict=True)
                header = next(records, None)
                if header != [f"x{i}" for i in range(16)] + ["label"]:
                    raise ValueError("fixed CPU input requires exactly x0 through x15 and label")
                count = 0
                while True:
                    lines.record_bytes = 0
                    row = next(records, None)
                    if row is None:
                        break
                    if len(row) != 17 or row[-1] not in ("0", "1"):
                        raise ValueError("each sample requires 16 features and a binary integer label")
                    if any("\r" in value or "\n" in value for value in row):
                        raise ValueError("CSV numeric fields cannot contain CR or LF")
                    if any(not DECIMAL_FEATURE.fullmatch(value.strip()) for value in row[:-1]):
                        raise ValueError("CSV features must use the current decimal numeric grammar")
                    values = [float(value) for value in row[:-1]]
                    if any(not math.isfinite(value) or abs(value) > 3.4028234663852886e38 for value in values):
                        raise ValueError("features must be finite float32 values")
                    if count >= rows:
                        raise ValueError("selected input row count exceeds the frozen input")
                    struct.pack_into("<16f", maps["features.f32"], count * 64, *values)
                    struct.pack_into("<q", maps["labels.i64"], count * 8, int(row[-1]))
                    count += 1
                if (count != rows or lines.size != int(manifest["prepared_size_bytes"])
                    or lines.digest.hexdigest() != manifest["prepared_sha256"]):
                    raise ValueError("selected input facts do not match the frozen input")
            yield maps
    finally:
        for target in paths:
            target.unlink()


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


def evaluate(model, features, labels, batch_size, device, torch, criterion):
    total = 0.0
    with torch.no_grad():
        for start in range(0, len(labels), batch_size):
            batch_features = features[start:start + batch_size].to(device)
            batch_labels = labels[start:start + batch_size].to(device)
            total += float(criterion(model(batch_features), batch_labels))
    return total / len(labels)


def main():
    signal.signal(signal.SIGTERM, terminate)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True)
    parser.add_argument("--dataset-manifest", required=True)
    parser.add_argument("--work-root", required=True)
    parser.add_argument("--expected-dataset-manifest-sha256", required=True)
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
    output = checked_path(args.output)
    check_output(output)
    manifest_path = checked_path(args.dataset_manifest)
    manifest = load_dataset_manifest(manifest_path, args.expected_dataset_manifest_sha256)
    data = checked_path(manifest_path.parent / "data.csv")
    work = checked_path(args.work_root)
    with selected_input_maps(data, manifest, work) as maps:
        return train(args, output, manifest, maps, canonical_rate, optimizer_rate,
                     epochs, batch_size, seed)


def train(args, output, manifest, maps, canonical_rate, optimizer_rate,
          epochs, batch_size, seed):
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
    rows = int(manifest["row_count"])
    features = torch.frombuffer(maps["features.f32"], dtype=torch.float32).reshape(rows, 16)
    labels = torch.frombuffer(maps["labels.i64"], dtype=torch.int64)
    order = torch.frombuffer(maps["order.i64"], dtype=torch.int64)
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
    evaluation_criterion = nn.CrossEntropyLoss(reduction="sum")
    initial_loss = evaluate(model, features, labels, batch_size, args.device,
                            torch, evaluation_criterion)
    generator = torch.Generator(device="cpu")
    generator.manual_seed(seed)
    metrics_digest = hashlib.sha256()
    metrics_bytes = 0

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
        with (output / "metrics.jsonl").open("xb") as metrics:
            for _ in range(epochs):
                torch.randperm(rows, generator=generator, out=order)
                for start in range(0, rows, batch_size):
                    indices = order[start:start + batch_size]
                    batch_features = features[indices].to(args.device)
                    batch_labels = labels[indices].to(args.device)
                    optimizer.zero_grad(set_to_none=True)
                    loss = criterion(model(batch_features), batch_labels)
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
                    raw = (line + "\n").encode("utf-8")
                    metrics.write(raw)
                    metrics.flush()
                    metrics_digest.update(raw)
                    metrics_bytes += len(raw)
                    print(line, flush=True)
                    if args.recipe == "fail" and step == 5:
                        raise RuntimeError(
                            "CPU03_RECIPE_FAILURE: failed after five real optimizer steps"
                        )
                    if args.recipe == "slow-stop":
                        time.sleep(2)
        model.eval()
        final_loss = evaluate(model, features, labels, batch_size, args.device,
                              torch, evaluation_criterion)
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
                "samples": rows,
                "source_samples": rows,
                "epochs": epochs,
                "batch_size": batch_size,
                "learning_rate": canonical_rate,
                "seed": seed,
                "steps": step,
                "world_size": 1,
                "device": next(model.parameters()).device.type,
                "torch_version": torch.__version__,
                "input_version_id": manifest["input_version_id"],
                "input_sha256": manifest["source_sha256"],
                "input_bytes": manifest["source_size_bytes"],
                "prepared_input_sha256": manifest["prepared_sha256"],
                "prepared_input_bytes": manifest["prepared_size_bytes"],
                "dataset_manifest_sha256": args.expected_dataset_manifest_sha256,
                "initial_loss": initial_loss if math.isfinite(initial_loss) else None,
                "final_loss": final_loss
                if final_loss is None or math.isfinite(final_loss)
                else None,
            },
        )
    files = []
    for name in ("model.pt", "model_config.json", "metrics.jsonl", "summary.json"):
        if name == "metrics.jsonl":
            size, digest = metrics_bytes, metrics_digest.hexdigest()
        else:
            contents = (output / name).read_bytes()
            size, digest = len(contents), hashlib.sha256(contents).hexdigest()
        files.append(
            {
                "path": name,
                "bytes": str(size),
                "sha256": digest,
            }
        )
    write_json(
        output / "result.json",
        {
            "schema": "ani.output-candidate.v1",
            "input_sha256": manifest["source_sha256"],
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

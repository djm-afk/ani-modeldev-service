"""Frozen configuration; defaults and bounds come from the Go implementation source."""

from decimal import Decimal
import hashlib
import json
import math
from pathlib import Path
import re
import struct
import sys

implementation_source = Path(__file__).resolve().parents[1]
if not (implementation_source / "implementation.py").is_file():
    implementation_source = Path("/opt/implementation")
sys.path.insert(0, str(implementation_source))
from implementation import implementation

RULE = implementation("builtin.sft_lora")
CONTRACT = RULE["parameter_contract_version"]
DEFAULTS = RULE["defaults"]
# Declaration order mirrors FinetuningManagedSettings canonical JSON order.
MANAGED = {
    "stage": "sft",
    "finetuning_type": "lora",
    "lora_target": "all",
    "lora_rank": DEFAULTS["lora_rank"],
    "lora_alpha": DEFAULTS["lora_alpha"],
    "cutoff_len": DEFAULTS["cutoff_len"],
    "per_device_train_batch_size": DEFAULTS["per_device_train_batch_size"],
    "gradient_accumulation_steps": DEFAULTS["gradient_accumulation_steps"],
    "seed": DEFAULTS["seed"],
    "data_seed": DEFAULTS["seed"],
    "device": "cpu",
    "fp16": False,
    "bf16": False,
    "flash_attn": "disabled",
    "report_to": "none",
    "save_strategy": "no",
    "template": "qwen",
    "train_dtype": "float32",
    "export_dtype": "float32",
    "verify_dtype": "float32",
    "lr_scheduler_type": "linear",
    "warmup_ratio": "0",
    "weight_decay": "0",
    "lora_dropout": DEFAULTS["lora_dropout"],
    "num_train_epochs": 3,
    "optim": "adamw_torch",
    "logging_steps": 1,
}


def canonical(value):
    return json.dumps(
        value,
        sort_keys=True,
        ensure_ascii=False,
        allow_nan=False,
        separators=(",", ":"),
    ).encode("utf-8")


def digest(value):
    return hashlib.sha256(canonical(value)).hexdigest()


def decimal_text(value):
    result = format(Decimal(str(value)), "f")
    return result.rstrip("0").rstrip(".") if "." in result else result


def projection(
    rate=None, steps=None, managed=None, parameters=None, device="cpu", **overrides
):
    values = dict(DEFAULTS)
    values.update(parameters or {})
    if rate is not None:
        values["learning_rate"] = decimal_text(rate)
    if steps is not None:
        values["max_steps"] = steps
    values.update(overrides)
    settings = dict(MANAGED)
    for key in values:
        if key in settings:
            settings[key] = values[key]
    settings["data_seed"], settings["device"] = values["seed"], device
    if managed is not None:
        settings = dict(managed)
    return {
        "parameter_contract_version": CONTRACT,
        "parameters": [
            {
                "name": key,
                "type": "DECIMAL" if isinstance(DEFAULTS[key], str) else "INTEGER",
                "value": str(values[key]),
            }
            for key in sorted(DEFAULTS)
        ],
        "managed_settings": settings,
    }


def load_configuration(contents):
    def unique_pairs(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError(f"duplicate configuration key: {key}")
            result[key] = value
        return result

    value = json.loads(contents, object_pairs_hook=unique_pairs)
    if not isinstance(value, dict) or set(value) != {
        "parameter_contract_version",
        "parameters",
        "managed_settings",
    }:
        raise ValueError("configuration must have exactly the safe projection fields")
    if value["parameter_contract_version"] != CONTRACT:
        raise ValueError("unsupported parameter contract")
    parameters = value["parameters"]
    if not isinstance(parameters, list) or len(parameters) != len(DEFAULTS):
        raise ValueError(
            "configuration must contain every resolved implementation parameter"
        )
    values = {}
    for parameter, name in zip(parameters, sorted(DEFAULTS)):
        kind = "DECIMAL" if isinstance(DEFAULTS[name], str) else "INTEGER"
        if (
            not isinstance(parameter, dict)
            or set(parameter) != {"name", "type", "value"}
            or parameter["name"] != name
            or parameter["type"] != kind
            or type(parameter["value"]) is not str
        ):
            raise ValueError("parameters must be ordered, typed canonical values")
        raw = parameter["value"]
        if kind == "INTEGER":
            maximum = int(RULE["seed_max"] if name == "seed" else RULE["integer_max"])
            minimum = 0 if name == "seed" else 1
            if (
                len(raw) > 19
                or not re.fullmatch(r"0|[1-9][0-9]*", raw)
                or not minimum <= int(raw) <= maximum
            ):
                raise ValueError(f"invalid integer: {name}")
            values[name] = int(raw)
        else:
            if (
                len(raw) > RULE["decimal_max_characters"]
                or not re.fullmatch(r"(?:0|[1-9][0-9]*)(?:\.[0-9]+)?", raw)
                or decimal_text(raw) != raw
            ):
                raise ValueError(f"invalid canonical decimal: {name}")
            number = Decimal(raw)
            if name == "lora_dropout":
                if not 0 <= number < 1 or not 0 <= float(number) < 1:
                    raise ValueError("lora_dropout must be in [0,1)")
            else:
                try:
                    converted = struct.unpack("f", struct.pack("f", float(number)))[0]
                except (OverflowError, ValueError):
                    converted = float("inf")
                if not math.isfinite(converted) or converted <= 0:
                    raise ValueError(
                        "learning_rate must remain positive and finite in float32"
                    )
            values[name] = raw
    managed = value["managed_settings"]
    if not isinstance(managed, dict) or set(managed) != set(MANAGED):
        raise ValueError("managed configuration fields do not match the implementation")
    device = managed["device"]
    if device not in ("cpu", "cuda"):
        raise ValueError("unsupported training device")
    expected = projection(parameters=values, device=device)
    for key, setting in expected["managed_settings"].items():
        if type(managed[key]) is not type(setting) or managed[key] != setting:
            raise ValueError(
                f"managed setting differs from the resolved implementation: {key}"
            )
    return expected

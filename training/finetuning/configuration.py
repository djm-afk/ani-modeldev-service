"""Frozen, safe configuration projection shared with the Go contract codec."""

from decimal import Decimal
import hashlib
import json
import re


CONTRACT = "ani.finetune.lora.parameters.v1"
# Declaration order is the Go FinetuningManagedSettings canonical JSON order.
MANAGED = {
    "stage": "sft",
    "finetuning_type": "lora",
    "lora_target": "all",
    "lora_rank": 8,
    "lora_alpha": 16,
    "cutoff_len": 128,
    "per_device_train_batch_size": 1,
    "gradient_accumulation_steps": 1,
    "seed": 42,
    "data_seed": 42,
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
    "lora_dropout": "0",
    "num_train_epochs": 3,
    "optim": "adamw_torch",
    "logging_steps": 1,
}


def canonical(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def digest(value):
    return hashlib.sha256(canonical(value)).hexdigest()


def decimal_text(value):
    result = format(Decimal(str(value)), "f")
    return result.rstrip("0").rstrip(".") if "." in result else result


def projection(rate, steps, managed=None):
    return {
        "parameter_contract_version": CONTRACT,
        "parameters": [
            {"name": "learning_rate", "type": "DECIMAL", "value": decimal_text(rate)},
            {"name": "max_steps", "type": "INTEGER", "value": str(steps)},
        ],
        "managed_settings": dict(MANAGED if managed is None else managed),
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
    if set(value) != {"parameter_contract_version", "parameters", "managed_settings"}:
        raise ValueError("configuration must have exactly the safe projection fields")
    if value["parameter_contract_version"] != CONTRACT:
        raise ValueError("unsupported parameter contract")
    managed = value["managed_settings"]
    if set(managed) != set(MANAGED):
        raise ValueError("managed configuration fields do not match the locked recipe")
    for key, expected in MANAGED.items():
        if type(managed[key]) is not type(expected) or managed[key] != expected:
            raise ValueError(f"managed setting cannot be overridden: {key}")
    parameters = value["parameters"]
    if not isinstance(parameters, list) or len(parameters) != 2:
        raise ValueError("exactly learning_rate and max_steps are required")
    for parameter, name, kind in zip(
        parameters, ("learning_rate", "max_steps"), ("DECIMAL", "INTEGER")
    ):
        if (
            set(parameter) != {"name", "type", "value"}
            or parameter["name"] != name
            or parameter["type"] != kind
        ):
            raise ValueError(
                "parameters must be ordered, typed learning_rate and max_steps"
            )
        if type(parameter["value"]) is not str:
            raise ValueError("typed parameter value must be a canonical string")
    rate, steps = parameters[0]["value"], parameters[1]["value"]
    if len(rate) > 32 or not re.fullmatch(r"(?:0|[1-9][0-9]*)(?:\.[0-9]+)?", rate):
        raise ValueError("learning_rate must be a non-exponent decimal")
    if not 0 < Decimal(rate) <= 1 or decimal_text(rate) != rate:
        raise ValueError("learning_rate must be canonical and in (0,1]")
    if not re.fullmatch(r"[1-9][0-9]*", steps) or not 1 <= int(steps) <= 200:
        raise ValueError("max_steps must be canonical and in [1,200]")
    return projection(rate, int(steps))

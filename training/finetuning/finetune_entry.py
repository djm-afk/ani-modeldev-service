"""Synchronous CPU LoRA train/export/verify using the pinned LLaMA-Factory.

The process consumes a frozen safe projection. It has no control-plane clients
and cannot publish or mark an execution successful. Optimizer facts and metric
events come exclusively from the real transformers Trainer callback.
"""

import argparse
from datetime import datetime, timezone
import hashlib
import importlib.metadata
import json
import math
import os
from pathlib import Path
import re
import resource
import signal
import shutil
import stat
import sys
import tempfile
import time

# -I deliberately removes the script directory from sys.path. Only this image-
# owned directory is added, never any workspace/user-controlled Python path.
sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from dataset_manifest import (
    load_dataset_manifest, verify_prepared_dataset, MAX_MANIFEST_BYTES,
)
from configuration import (
    MANAGED,
    canonical,
    decimal_text,
    digest,
    load_configuration,
    projection,
)
from phase_measurement import PhaseMeasurements

from template_runtime import (
    PACKAGES, RuntimeFailure, configure_work_directory, resolve_context_limit,
    load_runtime_plan, verify_context_facts, resolve_effective_template,
    encoding_probes, token_facts, assert_effective_tokenizer, consume_effective_template,
)



def checked_path(path):
    path = Path(os.path.abspath(path))
    for component in (*reversed(path.parents), path):
        if component.is_symlink():
            raise ValueError("symbolic links are forbidden in workload paths")
    return path


def read_regular(path, maximum):
    path = checked_path(path)
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, "rb") as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or not 0 < info.st_size <= maximum:
            raise ValueError(f"invalid regular input size: {path}")
        contents = source.read(maximum + 1)
    if len(contents) != info.st_size:
        raise ValueError("input changed while being read")
    return contents


def file_digest(path):
    descriptor = os.open(
        checked_path(path), os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK
    )
    sha, size = hashlib.sha256(), 0
    with os.fdopen(descriptor, "rb") as source:
        if not stat.S_ISREG(os.fstat(source.fileno()).st_mode):
            raise ValueError("model members must be regular files")
        while block := source.read(1024 * 1024):
            size += len(block)
            sha.update(block)
    return {"bytes": str(size), "sha256": sha.hexdigest()}


def inventory(path):
    path = checked_path(path)
    files = []
    for member in sorted(path.rglob("*")):
        if member.is_symlink():
            raise ValueError("symbolic links are forbidden in model inventories")
        if not member.is_dir():
            files.append(
                {"path": member.relative_to(path).as_posix(), **file_digest(member)}
            )
    return files


def write_json(path, value):
    path = checked_path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    with temporary.open("x", encoding="utf-8") as output:
        output.write(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    os.replace(temporary, path)


def write_canonical(path, raw):
    path = checked_path(path)
    temporary = path.with_suffix(path.suffix + ".tmp")
    with temporary.open("xb") as output:
        output.write(raw)
    os.replace(temporary, path)


def empty_output(path):
    path = checked_path(path)
    if path.exists() and (not path.is_dir() or any(path.iterdir())):
        raise FileExistsError(f"refusing to overwrite nonempty workload output: {path}")
    path.mkdir(parents=True, exist_ok=True)
    return path


def runtime(device="cpu"):
    versions = {package: importlib.metadata.version(package) for package in PACKAGES}
    if os.environ.get("WORLD_SIZE", "1") != "1":
        raise ValueError("LoRA requires one process")
    if device == "cpu":
        os.environ["CUDA_VISIBLE_DEVICES"] = ""
    import torch

    if device == "cuda" and (
        not torch.cuda.is_available() or torch.cuda.device_count() != 1
    ):
        raise ValueError("CUDA LoRA requires exactly one available CUDA device")
    torch.set_num_threads(2)
    return versions


def inputs(args, *, verify_data=True):
    workspace = checked_path(args.workspace)
    configuration = load_configuration(read_regular(args.configuration, MAX_MANIFEST_BYTES))
    manifest_path = checked_path(args.dataset_manifest)
    if manifest_path != workspace / "inputs/dataset-manifest.json":
        raise ValueError("dataset manifest must belong to this execution workspace")
    dataset = load_dataset_manifest(manifest_path, args.expected_dataset_manifest_sha256, "JSON")
    if verify_data:
        verify_prepared_dataset(workspace, dataset)
        dataset_info = json.loads(read_regular(workspace / "inputs/data/dataset_info.json", MAX_MANIFEST_BYTES))
        expected_info = {
            "ani_cpu_smoke": {
                "file_name": "finetune_data.jsonl", "formatting": "alpaca",
                "columns": {"prompt": "instruction", "query": "input", "response": "output"},
            }
        }
        if dataset_info != expected_info:
            raise ValueError("dataset_info differs from the frozen JSONL Alpaca mapping")
    manifest_contents = read_regular(workspace / "inputs/base_manifest.json", MAX_MANIFEST_BYTES)
    if hashlib.sha256(manifest_contents).hexdigest() != args.expected_base_manifest_sha256:
        raise ValueError("base manifest does not match the frozen identity")
    manifest = json.loads(manifest_contents)
    if (manifest.get("schema") != "ani.finetune.base-manifest.v3"
            or manifest.get("files") != inventory(workspace / "inputs/model")):
        raise ValueError("base files do not match the verified manifest")
    return workspace, configuration, dataset


def measured_inputs(args, role, *, verify_data=True):
    measurements = PhaseMeasurements(args.workspace, role)
    args.phase_measurements = measurements
    with measurements.phase('input_validate'):
        workspace, configuration, dataset = inputs(args, verify_data=verify_data)
    return workspace, configuration, dataset, measurements


def factory_arguments(workspace, configuration, output):
    p = {item["name"]: item["value"] for item in configuration["parameters"]}
    managed = configuration["managed_settings"]
    excluded = {
        "device",
        "train_dtype",
        "export_dtype",
        "verify_dtype",
        "warmup_ratio",
        "weight_decay",
        "lora_dropout",
    }
    arguments = {key: value for key, value in managed.items() if key not in excluded}
    arguments.update(
        {
            key: float(managed[key])
            for key in ("warmup_ratio", "weight_decay", "lora_dropout")
        }
    )
    arguments.update(
        {
            "model_name_or_path": str(workspace / "inputs/model"),
            "dataset_dir": str(workspace / "inputs/data"),
            "dataset": "ani_cpu_smoke",
            "output_dir": str(output),
            "learning_rate": float(p["learning_rate"]),
            "max_steps": int(p["max_steps"]),
            "do_train": True,
            "use_cpu": managed["device"] == "cpu",
            "overwrite_output_dir": False,
            "trust_remote_code": False,
            "preprocessing_num_workers": 1,
            "preprocessing_batch_size": 1,
            "streaming": False,
            "overwrite_cache": True,
            "cache_dir": os.environ["HF_DATASETS_CACHE"],
            "dataloader_num_workers": 0,
            "disable_tqdm": True,
            "gradient_checkpointing": True,
            "plot_loss": False,
            "packing": False,
        }
    )
    return arguments


def effective_projection(parsed):
    model, data, training, finetuning, _ = parsed
    actual = dict(MANAGED)
    for key in ("stage", "finetuning_type", "lora_rank", "lora_alpha"):
        actual[key] = getattr(finetuning, key)
    actual["lora_target"] = ",".join(finetuning.lora_target)
    actual["cutoff_len"] = data.cutoff_len
    for key in (
        "per_device_train_batch_size",
        "gradient_accumulation_steps",
        "seed",
        "data_seed",
        "fp16",
        "bf16",
        "logging_steps",
    ):
        actual[key] = getattr(training, key)
    actual["device"] = training.device.type
    actual["flash_attn"] = model.flash_attn
    actual["report_to"] = (
        "none" if training.report_to == [] else ",".join(training.report_to)
    )
    actual["save_strategy"] = training.save_strategy.value
    actual["lr_scheduler_type"] = training.lr_scheduler_type.value
    actual["optim"] = training.optim.value
    actual["num_train_epochs"] = int(training.num_train_epochs)
    for key in ("warmup_ratio", "weight_decay"):
        actual[key] = decimal_text(getattr(training, key))
    actual["lora_dropout"] = decimal_text(finetuning.lora_dropout)
    actual["train_dtype"] = str(model.compute_dtype).removeprefix("torch.")
    # export/verify have their own separately enforced float32 loaders.
    parameters = {
        key: actual[key]
        for key in actual
        if key in MANAGED
        and key
        in {
            "per_device_train_batch_size",
            "gradient_accumulation_steps",
            "cutoff_len",
            "lora_rank",
            "lora_alpha",
            "lora_dropout",
            "seed",
        }
    }
    return projection(
        training.learning_rate,
        training.max_steps,
        actual,
        parameters=parameters,
        device=actual["device"],
    )


def train(args):
    work = configure_work_directory(args.workspace, "train")
    workspace, configuration, dataset, measurements = measured_inputs(args, 'train')
    plan, capabilities = load_runtime_plan(workspace, args.expected_template_plan_sha256, read_regular)
    source_samples = int(dataset["row_count"])
    versions = runtime(configuration["managed_settings"]["device"])
    reports = empty_output(workspace / "reports/train")
    adapter = empty_output(workspace / "training/adapter")
    import torch
    from transformers import TrainerCallback
    from llamafactory.hparams import get_train_args
    from llamafactory.model.loader import load_config, load_tokenizer
    from llamafactory.model.patcher import patch_config
    from native_stages import run_managed_sft

    started, steps, samples, dtypes, losses = time.monotonic(), 0, None, [], []
    effective_token_count, samples_at_cutoff, max_effective_tokens = 0, 0, 0
    actual_device = None
    truncated_samples, truncated_tokens = 0, 0
    effective = None
    status, error = "FAILED", None
    with tempfile.TemporaryDirectory(prefix="ani-finetune-", dir=work / "tmp") as factory_output:
        parsed = get_train_args(
            factory_arguments(workspace, configuration, factory_output)
        )
        # Pin the validated managed dtype before the framework can infer a
        # different precision from the base config and hardware capabilities.
        # The real patcher and strict projection check still run; the native
        # stages receive these objects before loading model weights.
        parsed[0].compute_dtype = getattr(
            torch, configuration["managed_settings"]["train_dtype"]
        )
        tokenizer_module = load_tokenizer(parsed[0])
        tokenizer = tokenizer_module["tokenizer"]
        initial_vocab_size = len(tokenizer)
        template, effective_template, template_body = resolve_effective_template(
            workspace, plan, tokenizer, parsed[1], read_regular
        )
        parsed[0].resize_vocab = len(tokenizer) != initial_vocab_size
        effective_template_sha256 = hashlib.sha256(canonical(effective_template)).hexdigest()
        write_canonical(reports / "effective-template.json", canonical(effective_template))
        write_canonical(reports / "effective-template.jinja", template_body)
        model_configuration = load_config(parsed[0])
        cutoff = configuration["managed_settings"]["cutoff_len"]
        base_manifest = json.loads(read_regular(workspace / "inputs/base_manifest.json", MAX_MANIFEST_BYTES))
        context_limit = verify_context_facts(workspace, base_manifest["facts"], capabilities, read_regular)
        if cutoff > context_limit:
            raise RuntimeFailure("CONTEXT_LIMIT_EXCEEDED", "cutoff_len exceeds frozen context")
        patch_config(model_configuration, tokenizer, parsed[0], {}, is_trainable=True)
        effective = effective_projection(parsed)
        if canonical(effective) != canonical(configuration):
            raise ValueError(
                "framework parsed settings differ from the frozen projection"
            )
        with (reports / "metrics.jsonl").open(
            "x", encoding="utf-8", buffering=1
        ) as metrics:

            class Callback(TrainerCallback):
                def on_train_begin(
                    self,
                    args,
                    state,
                    control,
                    model=None,
                    train_dataloader=None,
                    **kwargs,
                ):
                    nonlocal samples, dtypes, actual_device
                    dtypes = sorted(
                        {
                            str(p.dtype).removeprefix("torch.")
                            for p in model.parameters()
                        }
                    )
                    devices = sorted({p.device.type for p in model.parameters()})
                    if devices != [configuration["managed_settings"]["device"]]:
                        raise ValueError("actual model parameters are on a different device")
                    actual_device = devices[0]
                    if not samples or dtypes != [
                        configuration["managed_settings"]["train_dtype"]
                    ]:
                        raise ValueError(
                            "no effective samples or model dtype differs from the frozen implementation"
                        )

                def on_optimizer_step(self, args, state, control, **kwargs):
                    nonlocal steps
                    steps += 1

                def on_step_end(self, args, state, control, **kwargs):
                    if state.global_step != steps:
                        raise ValueError(
                            "optimizer callback facts disagree with Trainer global_step"
                        )

                def on_log(self, args, state, control, logs=None, **kwargs):
                    if not logs or "loss" not in logs:
                        return
                    loss = float(logs["loss"])
                    if (
                        not math.isfinite(loss)
                        or state.global_step != steps
                        or steps < 1
                    ):
                        raise ValueError("invalid real training metric")
                    losses.append(loss)
                    metric = {
                        "schema": "ani.metric.v1",
                        "step": steps,
                        "name": "train.loss",
                        "value": loss,
                        "rank": 0,
                        "timestamp": datetime.now(timezone.utc).isoformat(),
                    }
                    line = json.dumps(metric, separators=(",", ":"))
                    metrics.write(line + "\n")
                    print(line, flush=True)

            try:
                def observe_dataset(statistics):
                    nonlocal samples, effective_token_count, samples_at_cutoff, max_effective_tokens
                    nonlocal truncated_samples, truncated_tokens
                    samples = statistics["samples"]
                    effective_token_count = statistics["effective_token_count"]
                    samples_at_cutoff = statistics["samples_at_cutoff"]
                    max_effective_tokens = statistics["max_effective_tokens"]
                    truncated_samples = statistics["truncated_samples"]
                    truncated_tokens = statistics["truncated_tokens"]

                run_managed_sft(parsed, callbacks=[Callback()], tokenizer_module=tokenizer_module,
                                template=template, observe_dataset=observe_dataset, measurements=measurements)
                if steps != parsed[2].max_steps or not losses:
                    raise ValueError(
                        "training did not reach its approved optimizer limit or produced no metrics"
                    )
                for name in ("adapter_config.json", "adapter_model.safetensors"):
                    source = Path(factory_output) / name
                    identity = file_digest(source)
                    temporary = adapter / (name + ".tmp")
                    with (
                        source.open("rb") as incoming,
                        temporary.open("xb") as outgoing,
                    ):
                        shutil.copyfileobj(incoming, outgoing, length=1024 * 1024)
                    if file_digest(temporary) != identity:
                        raise ValueError("adapter changed during the bounded copy")
                    os.replace(temporary, adapter / name)
                from safetensors import safe_open

                with safe_open(
                    adapter / "adapter_model.safetensors", framework="pt", device="cpu"
                ) as tensors:
                    adapter_dtypes = sorted(
                        {
                            str(tensors.get_tensor(key).dtype).removeprefix("torch.")
                            for key in tensors.keys()
                        }
                    )
                if adapter_dtypes != ["float32"]:
                    raise ValueError(
                        "actual adapter dtype differs from locked save precision"
                    )
                tokenizer.save_pretrained(adapter)
                from transformers import AutoTokenizer
                reloaded_tokenizer = AutoTokenizer.from_pretrained(str(adapter), local_files_only=True, trust_remote_code=False)
                assert_effective_tokenizer(reloaded_tokenizer, effective_template, template_body)
                encoding_probes(reloaded_tokenizer, template)
                status = "SUCCEEDED"
            except BaseException as failure:
                error = f"{type(failure).__name__}: {failure}"
                status = "CANCELLED" if isinstance(failure, SystemExit) else "FAILED"
                raise
            finally:
                summary = {
                    "schema": "ani.finetune.summary.v1",
                    "status": status,
                    "samples": samples,
                    "source_samples": source_samples,
                    "dropped_samples": None
                    if samples is None
                    else source_samples - samples,
                    "effective_token_count": effective_token_count,
                    "samples_at_cutoff": samples_at_cutoff,
                    "max_effective_tokens": max_effective_tokens,
                    "truncated_samples": truncated_samples,
                    "truncated_tokens": truncated_tokens,
                    "optimizer_steps": steps,
                    "steps": steps,
                    "metric_samples": len(losses),
                    "input_sha256": dataset["source_sha256"],
                    "dataset_manifest_sha256": args.expected_dataset_manifest_sha256,
                    "prepared_input_sha256": dataset["prepared_sha256"],
                    "prepared_input_bytes": dataset["prepared_size_bytes"],
                    "input_bytes": dataset["source_size_bytes"],
                    "base_manifest_sha256": args.expected_base_manifest_sha256,
                    "parameter_contract_version": configuration[
                        "parameter_contract_version"
                    ],
                    "resolved_configuration_sha256": digest(configuration),
                    "effective_configuration": effective,
                    "effective_template_sha256": effective_template_sha256,
                    "effective_configuration_sha256": digest(effective),
                    "device": actual_device,
                    "train_dtypes": dtypes,
                    "adapter_dtype": "float32" if status == "SUCCEEDED" else None,
                    "runtime_versions": versions,
                    "elapsed_seconds": time.monotonic() - started,
                    "peak_process_rss_bytes": str(
                        resource.getrusage(resource.RUSAGE_SELF).ru_maxrss * 1024
                    ),
                    "phase_measurements": measurements.report(),
                    "error": error,
                }
                effective_parameters = {
                    p["name"]: p["value"] for p in effective["parameters"]
                }
                summary["learning_rate"] = effective_parameters["learning_rate"]
                summary["max_steps"] = int(effective_parameters["max_steps"])
                summary["recipe_sha256"] = digest(configuration["managed_settings"])
                summary["adapter_files"] = (
                    inventory(adapter) if status == "SUCCEEDED" else []
                )
                write_json(reports / "summary.json", summary)
    write_json(
        reports / "adapter_manifest.json",
        {
            "schema": "ani.finetune.adapter-manifest.v3",
            "effective_template_sha256": effective_template_sha256,
            "input_sha256": dataset["source_sha256"],
            "dataset_manifest_sha256": args.expected_dataset_manifest_sha256,
            "prepared_input_sha256": dataset["prepared_sha256"],
            "prepared_input_bytes": dataset["prepared_size_bytes"],
            "base_manifest_sha256": args.expected_base_manifest_sha256,
            "resolved_configuration_sha256": digest(configuration),
            "files": inventory(adapter),
        },
    )


def export_arguments(workspace, merged, *, export_size):
    if type(export_size) is not int or export_size <= 0:
        raise ValueError('export_size must be a positive integer number of GB')
    return {
        "model_name_or_path": str(workspace / "inputs/model"),
        "adapter_name_or_path": str(workspace / "training/adapter"),
        "finetuning_type": "lora",
        "export_dir": str(merged),
        "export_size": export_size,
        "export_device": "cpu",
        "infer_dtype": "float32",
        "trust_remote_code": False,
        "resize_vocab": True,
        "cache_dir": os.environ["HF_HOME"],
    }


def export(args, *, export_size=3):
    configure_work_directory(args.workspace, "export")
    workspace = checked_path(args.workspace)
    arguments = export_arguments(workspace, workspace / 'export/merged', export_size=export_size)
    started = time.monotonic()
    workspace, configuration, dataset, measurements = measured_inputs(args, 'export', verify_data=False)
    runtime()
    plan, capabilities = load_runtime_plan(workspace, args.expected_template_plan_sha256, read_regular)
    from transformers import AutoTokenizer
    tokenizer = AutoTokenizer.from_pretrained(str(workspace / "training/adapter"), local_files_only=True, trust_remote_code=False)
    template, effective_template, template_body = consume_effective_template(workspace, plan, tokenizer, read_regular)
    effective_template_sha256 = hashlib.sha256(canonical(effective_template)).hexdigest()
    summary = json.loads(read_regular(workspace / "reports/train/summary.json", MAX_MANIFEST_BYTES))
    association = json.loads(
        read_regular(workspace / "reports/train/adapter_manifest.json", MAX_MANIFEST_BYTES)
    )
    if (
        summary["status"] != "SUCCEEDED"
        or summary["effective_template_sha256"] != effective_template_sha256
        or association["effective_template_sha256"] != effective_template_sha256
        or summary["resolved_configuration_sha256"] != digest(configuration)
        or summary["input_sha256"] != dataset["source_sha256"]
        or association["input_sha256"] != dataset["source_sha256"]
        or summary["dataset_manifest_sha256"] != args.expected_dataset_manifest_sha256
        or association["dataset_manifest_sha256"] != args.expected_dataset_manifest_sha256
        or association["resolved_configuration_sha256"] != digest(configuration)
        or summary["adapter_files"] != inventory(workspace / "training/adapter")
        or association["files"] != inventory(workspace / "training/adapter")
        or association["base_manifest_sha256"] != args.expected_base_manifest_sha256
    ):
        raise ValueError(
            "export input is not a verified result of this frozen configuration"
        )
    merged = empty_output(workspace / "export/merged")
    reports = empty_output(workspace / "reports/export")
    from native_stages import export_managed

    with measurements.phase('export'):
        export_managed(
            arguments, tokenizer_module={"tokenizer": tokenizer, "processor": None}, effective_template=effective_template,
        )
    exported_tokenizer = AutoTokenizer.from_pretrained(str(merged), local_files_only=True, trust_remote_code=False)
    assert_effective_tokenizer(exported_tokenizer, effective_template, template_body)
    encoding_probes(exported_tokenizer, template)
    from safetensors import safe_open

    weight_files = sorted(merged.glob("*.safetensors"))
    if not weight_files:
        raise ValueError("export generated no safetensors weights")
    dtypes = set()
    for path in weight_files:
        with safe_open(path, framework="pt", device="cpu") as tensors:
            dtypes.update(
                str(tensors.get_tensor(key).dtype).removeprefix("torch.")
                for key in tensors.keys()
            )
    if dtypes != {"float32"}:
        raise ValueError("export precision differs from frozen configuration")
    write_json(
        reports / "merged_manifest.json",
        {
            "schema": "ani.finetune.merged-manifest.v3",
            "effective_template_sha256": effective_template_sha256,
            "input_sha256": dataset["source_sha256"],
            "dataset_manifest_sha256": args.expected_dataset_manifest_sha256,
            "prepared_input_sha256": dataset["prepared_sha256"],
            "prepared_input_bytes": dataset["prepared_size_bytes"],
            "base_manifest_sha256": args.expected_base_manifest_sha256,
            "adapter_manifest_sha256": file_digest(
                workspace / "reports/train/adapter_manifest.json"
            )["sha256"],
            "adapter_files": inventory(workspace / "training/adapter"),
            "resolved_configuration_sha256": digest(configuration),
            "dtype": "float32",
            "files": inventory(merged),
        },
    )
    write_json(
        reports / "export.json",
        {
            "schema": "ani.finetune.export.v1",
            "effective_template_sha256": effective_template_sha256,
            "status": "SUCCEEDED",
            "dtype": "float32",
            "elapsed_seconds": time.monotonic() - started,
            "peak_process_rss_bytes": str(
                resource.getrusage(resource.RUSAGE_SELF).ru_maxrss * 1024
            ),
            "phase_measurements": measurements.report(),
        },
    )


def verify(args):
    configure_work_directory(args.workspace, "verify")
    started = time.monotonic()
    workspace, configuration, dataset, measurements = measured_inputs(args, 'verify', verify_data=False)
    runtime()
    plan, capabilities = load_runtime_plan(workspace, args.expected_template_plan_sha256, read_regular)
    merged = workspace / "export/merged"
    manifest = json.loads(
        read_regular(workspace / "reports/export/merged_manifest.json", MAX_MANIFEST_BYTES)
    )
    if (
        manifest["files"] != inventory(merged)
        or manifest["resolved_configuration_sha256"] != digest(configuration)
        or manifest["input_sha256"] != dataset["source_sha256"]
        or manifest["dataset_manifest_sha256"] != args.expected_dataset_manifest_sha256
        or manifest["base_manifest_sha256"] != args.expected_base_manifest_sha256
        or manifest["adapter_manifest_sha256"]
        != file_digest(workspace / "reports/train/adapter_manifest.json")["sha256"]
        or manifest["adapter_files"] != inventory(workspace / "training/adapter")
    ):
        raise ValueError("merged files differ from this execution's export evidence")
    reports = empty_output(workspace / "reports/verify")
    import torch
    from transformers import AutoModelForCausalLM, AutoTokenizer

    with measurements.phase('verify'):
        tokenizer = AutoTokenizer.from_pretrained(
            str(merged), local_files_only=True, trust_remote_code=False
        )
        template, effective_template, template_body = consume_effective_template(workspace, plan, tokenizer, read_regular)
        effective_template_sha256 = hashlib.sha256(canonical(effective_template)).hexdigest()
        if manifest["effective_template_sha256"] != effective_template_sha256:
            raise RuntimeFailure("TEMPLATE_ENCODING_MISMATCH", "export template identity differs")
        model = (
            AutoModelForCausalLM.from_pretrained(
                str(merged),
                torch_dtype=torch.float32,
                local_files_only=True,
                trust_remote_code=False,
                attn_implementation="eager",
            )
            .to("cpu")
            .eval()
        )
        if {p.dtype for p in model.parameters()} != {torch.float32}:
            raise ValueError("verify model precision differs from locked loader")
        prompt = tokenizer.apply_chat_template(
            [{"role": "user", "content": "What is two plus two?"}],
            tokenize=False,
            add_generation_prompt=True,
        )
        encoded = tokenizer(prompt, return_tensors="pt")
        with torch.inference_mode():
            logits = model(**encoded).logits
            if not torch.isfinite(logits).all():
                raise ValueError("merged model produced non-finite logits")
            output = model.generate(**encoded, max_new_tokens=8, do_sample=False)
        generated = output[0, encoded["input_ids"].shape[-1] :].tolist()
        generated_text = tokenizer.decode(generated, skip_special_tokens=True)
        if not generated or not generated_text.strip():
            raise ValueError("merged model generated no nonempty text")
    write_json(
        reports / "verify.json",
        {
            "schema": "ani.finetune.verify.v1",
            "effective_template_sha256": effective_template_sha256,
            "status": "PASSED",
            "dtype": "float32",
            "device": "cpu",
            "resolved_configuration_sha256": digest(configuration),
            "merged_manifest_sha256": file_digest(
                workspace / "reports/export/merged_manifest.json"
            )["sha256"],
            "finite_logits": True,
            "generated_token_ids": generated,
            "generated_text": generated_text,
            "elapsed_seconds": time.monotonic() - started,
            "peak_process_rss_bytes": str(
                resource.getrusage(resource.RUSAGE_SELF).ru_maxrss * 1024
            ),
            "phase_measurements": measurements.report(),
        },
    )


def terminate(signum, _frame):
    raise SystemExit(128 + signum)


def main():
    signal.signal(signal.SIGTERM, terminate)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("role", choices=("train", "export", "verify"))
    parser.add_argument("--configuration", required=True)
    parser.add_argument("--workspace", required=True)
    parser.add_argument("--dataset-manifest", required=True)
    parser.add_argument("--expected-dataset-manifest-sha256", required=True)
    parser.add_argument("--expected-base-manifest-sha256", required=True)
    parser.add_argument("--expected-template-plan-sha256", required=True)
    args = parser.parse_args()
    for sha in (args.expected_dataset_manifest_sha256, args.expected_base_manifest_sha256, args.expected_template_plan_sha256):
        if not re.fullmatch(r"[0-9a-f]{64}", sha):
            raise ValueError("expected SHA256 must be lowercase hexadecimal")
    try:
        configure_work_directory(args.workspace, args.role)
        globals()[args.role](args)
    except BaseException as failure:
        # Early failures have a separate finite fact, never a normal training
        # summary. Failure to write diagnostics must preserve the real error.
        try:
            directory = checked_path(Path(args.workspace) / "reports" / args.role)
            directory.mkdir(parents=True, exist_ok=True)
            steps = 0
            summary_path = directory / "summary.json"
            if summary_path.exists():
                summary = json.loads(read_regular(summary_path, MAX_MANIFEST_BYTES))
                observed = summary.get("optimizer_steps")
                if type(observed) is int and observed >= 0:
                    steps = observed
            write_json(directory / "failure.json", {
                "schema": "ani.finetune.failure.v1", "phase": args.role,
                "reason": getattr(failure, "reason", "RUNTIME_EXECUTION_FAILED"),
                "diagnostic": type(failure).__name__, "optimizer_steps": steps,
                "dataset_manifest_sha256": args.expected_dataset_manifest_sha256,
                "base_manifest_sha256": args.expected_base_manifest_sha256,
                "template_plan_sha256": args.expected_template_plan_sha256,
                "phase_measurements": args.phase_measurements.report() if hasattr(args, 'phase_measurements') else None,
            })
        except BaseException:
            pass
        raise
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as error:
        print(f"finetuning failed: {type(error).__name__}: {error}", file=sys.stderr)
        sys.exit(1)

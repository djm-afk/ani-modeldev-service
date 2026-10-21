# Built-in LoRA workload

`finetune_entry.py` is the synchronous, single-process CPU/CUDA managed entry for the
`ani.sft-lora.parameters.v2.1` contract. It calls LLaMA-Factory 0.9.3 for SFT
and Adapter merge/export, then independently reloads the merged model with
transformers for CPU verification. It does not admit, publish, register, or close
an execution. Those remain ModelDev application responsibilities.

The entry accepts `train`, `export`, or `verify`, with the same fixed argv:

```text
--configuration /workspace/inputs/finetune_configuration.json
--workspace /workspace
--expected-input-sha256 <frozen JSON SHA>
--expected-input-bytes <frozen JSON length>
--expected-base-manifest-sha256 <canonical base-manifest SHA>
```

The configuration is the canonical Go `FinetuningConfiguration` projection.
Its nine algorithm fields come from the shared machine contract in
`contract/cpup01/implementations.json`; every value is read back from the actual
framework parser before training. `data_seed` derives from `seed`. Fixed safety
settings remain implementation owned. Canonical JSON sorts object keys.
It contains only the contract version, sorted typed parameters, and the finite
managed settings in `configuration.py`. Neither arbitrary YAML nor a second
environment/default parameter channel is supported. Before training, the entry
uses LLaMA-Factory argument parsing and its actual model config resolver to
check the effective projection. CPU/CUDA training and Adapter save enforce float32. Export and independent
verify always run on CPU in float32. CUDA training requires one visible device.

The sole metric source is transformers 4.49 `TrainerCallback.on_log(loss)` with
`logging_steps=1`; its loss maps to the unchanged `ani.metric.v1` train.loss
record. `on_optimizer_step` separately counts actual optimizer updates and is
checked against Trainer global_step. The locked transformers version does not
populate the callback's train_dataloader. The entry observes LLaMA-Factory's
actual get_dataset return and passes that same object unchanged to its Trainer;
source and effective counts, actual cutoff/token facts, and truncation are
recorded without a fixed row count. No training logic or dataset is
replaced. Failure/SIGTERM summaries retain the actual partial step count.

Prepare supplies the raw Alpaca JSON, the `ani_cpu_smoke` dataset_info mapping,
and verified base members under inputs/model. Base-manifest canonical bytes
have the shape `{schema,files:[{path,bytes,sha256}]}`, with members sorted by
path, bytes encoded as strings, and no trailing newline. The entry checks both
the manifest identity and each actual member. The temporary LLaMA-Factory
output is copied in bounded blocks into training/adapter and verified before
atomic publication on the workspace filesystem.

Training writes only the final Adapter weight/config and reports/train
metrics.jsonl, summary.json, adapter_manifest.json. Export writes export/merged
and reports/export/merged_manifest.json plus export.json. Verify reads the
frozen configuration and merged model evidence, writes reports/verify/verify.json,
and requires finite logits and nonempty deterministic generated text. Adapter,
export and verification manifests retain the current input, base, configuration
and Adapter inventories so a different training result cannot be substituted. Collect
must validate these reports and inventory; it cannot generate missing metrics.

The separate Dockerfile preserves the old MLP workload. The direct runtime
versions are pinned, including PyArrow 20.0.0 for NumPy 1.26 compatibility; the
complete installed package inventory is recorded in
`training/finetuning-runtime-lock.json`. The actual OCI digest is authoritative for an execution snapshot.

Run generation, builds, checks, and workloads only on Fedora:

```sh
podman build -f training/finetuning.Dockerfile -t localhost/ani-finetuning:candidate .
python3 scripts/finetuning-workload-materials.py /absolute/task-directory
python3 scripts/finetuning-workload-run.py /absolute/task-directory --image localhost/ani-finetuning:candidate --vector A
python3 scripts/finetuning-workload-run.py /absolute/task-directory --image localhost/ani-finetuning:candidate --vector B
```

The M0 driver pins the actual image ID for all roles, uses a read-only root,
offline network, one process, four CPUs, and an 8 GiB memory limit. This is
WORKLOAD evidence only. ModelDev admission, database, KFP/Trainer, S3 publication,
detached retrieval, and stop/close must be exercised by the actual CORE_FLOW
driver separately.

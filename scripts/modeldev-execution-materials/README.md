# Installed execution materials

Run this finite renderer on Fedora after the actual immutable image and confirmed PipelineVersion receipts exist:

```sh
go run ./scripts/modeldev-execution-materials --facts /absolute/task/material-input.json --entry training/train_mlp.py --output /absolute/task/builtin.mlp.cpu.json
```

The strict input schema is `ani.modeldev.material-input.v3`. Supply `implementation_id` (`builtin.mlp` or `builtin.sft_lora`), `device_mode` (`cpu` or `gpu`), `training_image` (`image_id`, decimal-string `image_version`, `image_scope` (`platform` or `tenant`), full registry `digest`, actual image `entry_sha256`, and actual absolute `python_executable`), `pipeline` (`phase: CONFIRMED`, `pipeline_id`, `pipeline_version_id`, `pipeline_ir_sha256`), and explicit `workspace_storage_class` / `workspace_capacity_bytes`. Supply `component_resources` for every actual component role: `workspace-name`, `prepare`, `train-wait`, `collect`, `publish`, `close`, `finalize-close`, plus `export`, `verify`, `register` for SFT. Each role requires `cpu_request`, `cpu_limit`, `memory_request`, `memory_limit`, `ephemeral_storage_request`, `ephemeral_storage_limit` as positive Kubernetes quantities with request ≤ limit. Pipeline/profile use the same values; training resources continue to come from ComputeOrigin. Workspace work is fixed to the separate `work` subpath. Use real receipt values; this directory supplies no deployment identities or enabled fixtures.

LoRA also requires `finetuning` with `export_image`, `verify_image`, actual `mapping_sha256`, current `runtime_capabilities`, and `model_template_associations` (an empty list is valid). The capability image digest and platform entry SHA must match the real final image receipt; train/export/verify use that same digest. Pass its exact `--entry training/finetuning/finetune_entry.py` and optional `--mapping` source. Parameter defaults and contract versions come exclusively from the embedded `contract/cpup01/implementations.json`; workload A/B observations do not approve parameter combinations.

The output is canonical `ani.modeldev.implementation-material.v3` software provenance. Existing output files are preserved. Install the file under its exact implementation/device filename in the configured private materials directory. Tenant scopes, actual environment proof, compute selection, execution timing and the per-execution Runtime plan are supplied by their existing owners during Prepare/Finalize. Rendering does not establish live readiness or enable an implementation.

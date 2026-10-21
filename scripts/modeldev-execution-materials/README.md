# Installed execution materials

Run this finite renderer on Fedora after the actual immutable image and confirmed PipelineVersion receipts exist:

```sh
go run ./scripts/modeldev-execution-materials --facts /absolute/task/material-input.json --entry training/train_mlp.py --output /absolute/task/builtin.mlp.cpu.json
```

The strict input schema is `ani.modeldev.material-input.v2.1`. Supply `implementation_id` (`builtin.mlp` or `builtin.sft_lora`), `device_mode` (`cpu` or `gpu`), `training_image` (`version_id`, full registry `digest` and actual image `entry_sha256`), `pipeline` (`phase: CONFIRMED`, `pipeline_id`, `pipeline_version_id`, `pipeline_ir_sha256`), and explicit `workspace_storage_class` / `workspace_capacity_bytes`. Use real receipt values; this directory supplies no deployment identities or enabled fixtures.

LoRA also requires `finetuning` with `export_image`, `verify_image`, actual `mapping_sha256`, `max_sequence_length`, and `budgets` containing only `rpc_timeout_seconds`, `max_base_model_bytes`, `max_base_model_expanded_bytes`, and `max_spool_bytes`. Pass its exact `--entry training/finetuning/finetune_entry.py` and optional `--mapping` source. Parameter defaults and contract versions come exclusively from the embedded `contract/cpup01/implementations.json`; workload A/B observations do not approve parameter combinations.

The output is canonical `ani.modeldev.implementation-material.v2.1` software provenance. Existing output files are preserved. Install the file under its exact implementation/device filename in the configured private materials directory. Tenant scopes, actual environment proof, compute selection, execution timing and the per-execution Runtime plan are supplied by their existing owners during Prepare/Finalize. Rendering does not establish live readiness or enable an implementation.

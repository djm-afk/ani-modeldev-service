# Current SFT/LoRA workload

`finetune_entry.py` implements the synchronous single-process CPU/CUDA stages
for `ani.sft-lora.parameters.v3`. It consumes current frozen facts prepared by
ModelDev. It has no control-plane clients and cannot publish, register a model,
release compute, or mark an execution successful.

Train, export and verify use the same fixed argument identities:

```text
--configuration /workspace/inputs/finetune_configuration.json
--workspace /workspace
--dataset-manifest /workspace/inputs/dataset-manifest.json
--expected-dataset-manifest-sha256 <canonical dataset manifest SHA256>
--expected-base-manifest-sha256 <canonical base manifest SHA256>
--expected-template-plan-sha256 <canonical template plan SHA256>
```

Prepare supplies the complete model inventory and normalized facts in the v3
base manifest, the verified JSONL dataset and dataset_info mapping, canonical
configuration, template plan and runtime capabilities. Source JSON size/SHA256,
prepared JSONL size/SHA256, row count, transform ID and input identity remain
separate frozen facts. Export and verify check dataset metadata without opening
the large JSONL again.

Algorithm defaults and representation bounds come only from
`contract/cpup01/implementations.json`. The runtime checks the resolved
configuration against the actual native parser. It uses the actual CausalLM
mapping, finite model/tokenizer context sources, and single or indexed
safetensors inventory. Model names, architectures and descriptive dtype metadata
do not create a separate allowlist.

`template_runtime.py` consumes one frozen named or native template selection,
loads its exact body, and applies the actual native template/tokenizer rules.
Train freezes the effective Jinja body and canonical token/EOS facts once in
`reports/train/effective-template.jinja` and `effective-template.json`. Export
and verify reload and check those persisted facts instead of selecting again.
Real native formatting and tokenizer encoding probes must agree. All stage
reports retain the same effective-template manifest SHA256, which also binds
the body hash, plan, runtime capabilities and image.

`native_stages.py` explicitly composes the official dataset/model/collator and
CustomSeq2SeqTrainer functions. It keeps native format conversion, tokenization,
optimizer execution, LoRA save and merge behavior. The sole upstream change is
`align_dataset`'s conversion `writer_batch_size=1`, checked at image build by the
exact-source patch in `runtime-patches`. Incremental observations use the native
encoding and sequence-length functions; no full token table is collected.

Before importing the runtime, each stage selects its own volume work root:
`/workspace/work/<role>/{tmp,hf,hf/datasets,matplotlib}`. CPU training and CPU
export/verify use float32. CUDA training uses float32 on exactly one visible
CUDA device. The same selected image digest applies to train, export and verify.
The official package inventory and actual interpreter are extracted from each
final image by `inspect_runtime.py`; the image retains official dependencies.

Train writes the actual adapter inventory, metrics and summary under training
and reports/train. Export merges to export/merged and writes its actual dynamic
inventory under reports/export. Verify independently reloads those merged bytes
on CPU, checks persisted tokenizer/template facts, finite logits and generated
tokens, then writes reports/verify/verify.json. Failure evidence retains the real
partial optimizer count. Collect validates these outputs and their identities;
it cannot generate missing training facts.

The Fedora workload driver `training/tests/run_lora_acceptance.py` takes an
existing current Prepare workspace and copies it with a reflink. Its production
inputs remain read-only; only each role's work and output mounts are writable.
Its result proves CPU workload behavior and detached reload. Real platform
admission, persistence, KFP/Trainer, publication and owner cleanup require the
separate business acceptance flow.

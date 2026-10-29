# Managed training runtime

The current runtime implements `builtin.mlp` and `builtin.sft_lora` using the v3
contracts in `contract/cpup01`. This entry supersedes the historical CPU03 and
v2.1 image instructions; their qualification records describe their original
images only.

`finetuning.Dockerfile` adds platform programs to the immutable official
LLaMA-Factory image selected from the prepared platform registry. Its `FROM`
fixes the complete AMD64 digest. CPU and GPU images retain the same official
Python packages; the execution mode determines the actual training device.
Export and independent model verification run on CPU in float32.

The image contains one program tree at `/opt/ani-modeldev/training`, the static
`/ani-modeldev-step` process, and the fixed `modeldev` UID/GID 10001. It installs
nothing at startup. The only upstream source patch fixes the nonstreaming format
conversion map to `writer_batch_size=1`; `finetuning/runtime-patches` pins and
checks its exact before/after source hashes. Platform-owned composition uses
native dataset, model and Trainer functions without modifying their workflow.

## Build and identify the actual image

Generation, builds, image operations and tests run on Fedora. Supply the
existing task-owned Podman directories through `ANI_IMAGE_STORAGE_ROOT`,
`ANI_IMAGE_RUNROOT`, and `ANI_IMAGE_TMPDIR`, then run:

```sh
bash training/build-image.sh FULL_SOURCE_COMMIT cpu /absolute/new-cpu-build
bash training/build-image.sh FULL_SOURCE_COMMIT gpu /absolute/new-gpu-build
```

The builder archives that exact Git commit, builds the step binary from it,
checks that the official base is already present, and builds offline without
pulling or installing packages. Each new output directory retains the source
archive, source tree, binary hash, complete command, image identity and exit.
A local build does not prove registry publication or workload qualification.

After the final immutable image reference is known, run the baked
`finetuning/inspect_runtime.py --image-digest REGISTRY/PATH@sha256:DIGEST` using
its actual `/opt/conda/bin/python`. The output records installed package versions,
actual CausalLM mapping and template registry, imported converter hash,
entrypoint hash, Python executable and CUDA build version. Generate software
materials from those actual facts and the registered Resource ImageID,
ImageVersion and scope. The capabilities and all three SFT roles must bind the
same selected immutable image.

## Input, work and output

MLP accepts only the frozen dataset manifest and its SHA256, the explicit work
root, and the current resolved parameters. Prepare supplies `inputs/data.csv`;
MLP verifies its complete bytes, SHA256 and row count before importing Torch.
It writes little-endian feature, label and shuffle memory maps on the work
volume and holds only the active tensor batch on the training device. The
algorithm remains the single-process 16→32→2 binary classifier.

SFT consumes the current Prepare workspace described in
[finetuning/README.md](finetuning/README.md). Structure guards come from the single
generated `structure_limits.json`; content admission uses the execution's frozen
limit L. There is no second image content limit or legacy input CLI.

All roles run with a read-only root filesystem and writable volume paths under
`/workspace/work/<role>/{tmp,hf,hf/datasets,matplotlib}`. Set TMPDIR, HF_HOME,
HF_DATASETS_CACHE and MPLCONFIGDIR to those directories before library imports.
Input facts and selected model bytes remain read-only.

MLP success writes `model.pt`, `model_config.json`, `metrics.jsonl`,
`summary.json` and `result.json`. Real optimizer updates emit `ani.metric.v1`.
Failure and SIGTERM retain actual metrics and a FAILED or CANCELLED summary
without a successful candidate. SFT records real optimizer, token/template,
adapter, export and independent verification facts. These workload candidates
still require ModelDev collection, publication, optional registration and close.

## Qualification boundaries

`tests/image_smoke.py IMAGE_ID IMMUTABLE_REFERENCE NEW_RUN_DIRECTORY` checks the
actual packaged programs and runtime, fixed user, read-only filesystem, 48 real
MLP updates, failure, stop and detached checkpoint reload.
`tests/run_mlp_acceptance.py` also covers nondefault parameters and partial
batches. `tests/run_lora_acceptance.py` accepts a current workspace produced by
the real Prepare chain; it runs the image's baked stages and reloads detached
merged bytes. It does not synthesize a second model/template contract.

Both final images must separately pass the real native Arrow conversion/cache
mechanism check. `tests/run_arrow_mechanism.py --baseline-image OFFICIAL_IMAGE_ID
--cpu-image CPU_IMAGE_ID --gpu-image GPU_IMAGE_ID --output NEW_RUN_DIRECTORY`
first records the unmodified official writer's actual two-record buffer, then
requires both final images to keep the conversion buffer at one record and all
three native cache stages on the role work volume. Its hooks are test-only; it
stops before model loading and optimizer execution. Long-record training, real platform submission, publication,
registration, logs and resource release remain distinct acceptance gates. A
build, source check or offline workload result cannot establish those gates.

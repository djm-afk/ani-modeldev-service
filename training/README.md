# Built-in MLP workload

`train_mlp.py` runs the single-process, single-device 16→32→2 MLP. It consumes
one immutable CSV with header `x0` through `x15`, then `label`, at least one row,
finite float32 features, and binary integer labels. It checks the selected byte
length and SHA256 before importing Torch or creating output. Paths cannot be
symlinks, and existing nonempty output paths are preserved.

`--learning-rate`, `--epochs`, `--batch-size`, and `--seed` come from the one
machine contract in `contract/cpup01/implementations.json`; Go embeds that same
file and the workload images include its immutable bytes. Omitted values use
its implementation defaults. Integers and decimals follow its representation
bounds. `--device` is the platform-frozen `cpu` or `cuda`; CUDA requires exactly
one visible CUDA device. The GPU image leaves device visibility to the assigned
runtime and device plugin.

Every epoch shuffles using the selected seed and includes the final partial
batch. Actual optimizer updates are emitted as `ani.metric.v1`; a completed run
has `epochs × ceil(samples/batch_size)` steps. Finite loss and weights are
required. Improvement is only an assertion for known regression data.

Success emits `model.pt`, `model_config.json`, `metrics.jsonl`, `summary.json`,
and the candidate `result.json`. Failure and SIGTERM retain actual metrics and
a `FAILED` or `CANCELLED` partial summary, without a successful result manifest.
A result candidate does not prove publication or business success.

Internal acceptance recipes `fail` and `slow-stop` retain the controlled
five-update failure and two-second step delay. Their fixed fault timing is
restricted to acceptance, and ordinary training has no recipe alarm.

Run behavior tests and the persistent workload/independent reload drivers on
Fedora only. The drivers use private writable directories and a supplied,
immutable runtime image. `run_mlp_acceptance.py --nondefault` covers 17 rows,
batch 6, two epochs, seed 7, and a finite run whose loss increases. It copies
the result and reloads the checkpoint in a separate container without the
training workspace mount. These are workload facts; the service must still
prove authenticated admission, KFP/Trainer, publication, close and cleanup.

The following locked image qualification describes the historical CPU image.
Changed sources require a new image digest and corresponding verification.

## Locked image materials

`runtime-lock.json` pins the Chainguard Python image index
`sha256:1961420e5f93bd056d4b0b40eca12cdf01b3ed09177aa4d6ec71fab38cbf158f`
in both Dockerfile stages and records the selected linux/amd64 manifest
`sha256:654e728e98400ab15c6a6b6a06a0392d5871e801697629202309acaba57bb87e`.
Builds never resolve a discovery tag. The base contains
Python 3.14.8 for linux/amd64 with the ordinary cp314 ABI; PyTorch remains 2.10.0+cpu. Of the ten
locked wheels, only Torch and MarkupSafe changed from cp313 to cp314; dependency
versions remained unchanged by that ABI migration.

`requirements.lock` pins versions and SHA256, `wheelhouse.sha256` fixes filenames,
and `runtime-packages.lock.json` records all 29 APK package names and versions,
including the legacy OpenSSL provider. The package database is retained. Torch
uses its wheel's bundled libgomp and the image's libstdc++; no host library is
copied. Optional NumPy integration emits a warning because this workload uses
tensors directly without NumPy.

The base has no shell. Exec-form Python commands create `/opt/venv` and install
the verified wheels offline during the build. The final image uses UID/GID
10001:10001, read-only program files, and the workload itself as PID 1. It contains
no selected dataset, checkpoint, or credentials and installs nothing at startup.

## Fedora execution

All generation, builds, training, reloads and scanning run on Fedora against a
fixed committed source. After selecting the task-owned Podman storage containing
the qualified base digest, use a fresh output directory:

```sh
bash training/build-image.sh FULL_SOURCE_SHA \
  /home/chabking/workspace/cpu-p01-20260930-01/cpu03/qualification-1259691-04/wheelhouse \
  /home/chabking/workspace/cpu-p01-20260930-01/cpu03/build-NEW_ATTEMPT
python3 training/tests/image_smoke.py "$(cat /home/chabking/workspace/cpu-p01-20260930-01/cpu03/build-NEW_ATTEMPT/image.id)" \
  /home/chabking/workspace/cpu-p01-20260930-01/cpu03/smoke-NEW_ATTEMPT
```

The build script rejects existing output directories, checks the source revision,
archives that Git tree, and copies only hash-verified locked wheels. It uses no
network, proxy forwarding or image pulls, is limited to two CPUs / 2 GiB, and
records the source, command, exits and local image identity. It does not push images.

The smoke tests actual nonroot PID 1 behavior without an init wrapper. Containers
are offline and read-only except isolated output/tmpfs, limited to two CPUs /
2 GiB and bounded to 90 seconds. A separate container generates the selected
fixture. Success checks actual file bytes and digests and independently reloads
the checkpoint with `weights_only=True`; fail and stop checks observe real
optimization first. Stop cleanup targets only the returned container ID after
retaining evidence. The image does not contain the test sources. To run the six
unchanged module tests inside it, the verification runner mounts the fixed source
read-only at `/source/training` and invokes `/opt/venv/bin/python -I -m unittest
discover -s /source/training/tests -p test_training.py -v`, with a separate bounded
writable temporary directory and the runner's resource limits.

## Fixed verification result

On Fedora, source `4d56b93b045233c1325a362fe89d506210223d47` produced local image
`sha256:0972fac83ba343e4d24e22bc35198bddf5c3e9d5c8ab3337694395beead3bd04`.
The run on 2026-09-30 at 15:17:51–15:21:59 UTC passed offline build, all six module
tests (38.123 seconds), and complete image smoke. Smoke covered the complete APK
lock, normal ABI and venv, dependency imports, success48, independent reload,
failure after five steps, and SIGTERM exit 143 after three observed steps.

Syft 1.52.0 recorded 69 artifacts: 31 APK, 14 binary and 24 Python records.
Grype 0.119.0 with the validated v6.1.9 database built at
`2026-09-30T06:32:47Z` passed the original `--fail-on high` gate: zero Critical,
zero High, eight Medium and two Low findings. All findings remain in the raw
report. There were no path excludes, VEX documents, only-fixed filtering or
user-added ignore rules. The four built-in RPM/DEB kernel-header rules matched
the previous scan configuration; this image has no RPM/DEB artifacts and no
ignored matches. This result does not mean the image has no vulnerabilities.

The OCI archive SHA256 is
`be102bd1fe66aadee64e782decf08c97dc07c5d1337ee3563bc8f8eba40bca6b`.
It identifies archive bytes, not a registry manifest digest. The run evidence is
indexed in `.scratch/cpu-p01/runs/20260930-01/cpu03/remediation-4d56b93-01/result.md`;
large OCI/checkpoint files remain in the corresponding Fedora task directory.
Later documentation or Go commits do not change the source identity of this image.

Earlier failed packaging, PID 1 and scan attempts remain separate in Git and the
CPU03 run records. In particular, `472c080` produced a different image whose scan
failed with 59 High findings; the new result does not relabel that old scan.

Signature trust-chain verification and registry publication remain unverified.
The consumed ENV handoff supplies no authorized registry destination. Target
cluster Trainer/KFP/BFF/S3 execution, L1–L4 and business acceptance are not proven
by this Fedora image run. Repository-wide gates and independent code review are
tracked separately from these workload results.

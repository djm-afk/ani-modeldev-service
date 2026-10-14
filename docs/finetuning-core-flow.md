# CPU managed fine tuning execution slice

The implementation extends the existing execution, inbox, KFP assembly, Trainer
adapter, publication verifier and close worker. The public FINETUNING admission
composition remains disabled until the real upstream and Model contracts are
configured. A tagged, disposable application test supplies upstream delivery and
a read-only approved base-model boundary; it does not add a production listener.

The actual chain is JSON import/READY → typed intent and immutable snapshot →
durable inbox → KFP prepare → one Trainer TrainJob → KFP export → KFP verify →
collect → publish → register/publication confirmation → close. Failure and Stop
use the original durable close fence and independent writer proof. The register
component records the original target, business keys, tar version/size/checksum
and verification digest before returning MODEL_DEPENDENCY_UNAVAILABLE. This is
recoverable delivery content, not a Model receipt or a write permit.

The current approved parameter combinations are A=0.0001/20 and B=0.0002/10.
`finetuning` must be an object; `{}` uses Release defaults. Missing/null objects,
unknown fields and cross combinations fail before dispatch. Optional parameter
presence is preserved in the original intent. The query projection reads original
snapshot values; precise preset queries use Gov's Release ID, digest and binding
generation rather than a ModelDev current pointer.

Prepare preserves exact JSON object bytes and validates the frozen base tar and
every member before exposing read-only model input. Trainer receives its own
explicit read-only input and writable adapter/report mounts. Export/verify each
receive one first-start permit, tied to the original KFP Task/Pod UID, under the
same database identity lock as Stop. Replays cannot start another process. Their
deadline is fixed from that database admission. TRAIN and train-wait share the
original training reservation deadline. Process cancellation waits for the real
process group to terminate; late reports only record original observations.

Collection validates complete HF files, adapter and correlated train/export/
verify reports. Only HF members enter the USTAR model tar. Publication streams
through a bounded disk spool; register independently reads fixed S3 versions,
checks actual tar members and confirms uploader termination before PUBLISHED.
The Model consumer gap is recorded explicitly: the checked provider source at
`71c10de63f07c4ee2717751b75a7f71cc2aa60a2` still accepts only `storage_path` for
CreateModelVersion and has no original-business-key pending lookup. MR, BFF,
frontend and independent Inference acceptance remain separate NOT_RUN scopes.

## Reproduction and evidence

Run all generation, formatting, checks, image builds and workloads on Fedora.
The task's active spec and actual results are retained in
`.scratch/finetuning-cpu/`; they are intentionally ignored by Git. Actual records
are independent from the document generator's plans/templates.

```sh
GOMAXPROCS=2 GOFLAGS=-p=2 python3 scripts/with-test-postgres.py make verify
/absolute/task/pipeline-venv/bin/python -m unittest discover -s pipelines -v
/absolute/task/pipeline-venv/bin/python pipelines/cpu_p01.py \
  --config /absolute/task/frozen-finetuning-compile.json \
  --output /absolute/task/finetuning.yaml
go test -c -tags finetuning_core_flow \
  -ldflags '-X github.com/zhangzhe-ctrl/ani-modeldev-service/cmd/ani-modeldev-service.Version=FINAL_PUSHED_SOURCE_SHA' \
  -o /absolute/task/core-flow-driver ./cmd/ani-modeldev-service
```

The compiler configuration is the existing five deployment bindings plus
`"kind":"FINETUNING"`. Images, IR, Runtime content, environment UID and object
versions must be actual immutable bindings, never fixture digests. Final images
are built from the pushed source commit. No task runs one KFP step by manually
creating a Job; the production application dispatches one authoritative Run.

Pinned KFP 2.16.0 cannot encode read-only root/PVC mounts or ephemeral-storage
resource requests. The finite environment adapter under
`deploy/finetuning-core-flow/` supplies those fields and retained Pod exit facts.
It checks the original namespace UID, live Workflow UID/run, role, images and
mounts. Its renderer only performs server dry-run. Registering its new admission
configuration requires explicit authorization because goal.md excludes automatic
cluster-level policy changes. It never changes the existing shared webhook.

Live acceptance uses only the verified 172.16.101.10–12 cluster and the task's
isolated namespaces, database and S3 identities. WORKLOAD checks, PostgreSQL
module checks and compiled IR do not establish CORE_FLOW. CORE_FLOW requires
real A/B Runs, original-key replay, Stop, deadline and owner restart, real close
proof and detached persistent-object retrieval without the training PVC.

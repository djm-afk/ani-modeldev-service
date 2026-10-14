# ani-modeldev-service

Module: `github.com/zhangzhe-ctrl/ani-modeldev-service`

This repository was generated from ANI's pinned Kratos layout. It is an
independent source snapshot: builds and runtime do not require the layout.

CPU-P01 的已实现功能、架构与扩展方式、环境部署和用户操作见
[CPU-P01 功能、实现、扩展、部署与使用指南](docs/cpu-p01-guide.md)。

CPU 受管微调的执行主链、隔离验收装配和保留的上游依赖边界见
[CPU managed fine tuning execution slice](docs/finetuning-core-flow.md)。

通用训练与微调共用的正式 Kubernetes 准入组件、退出留证和自动步骤回收见
[ModelDev webhook](docs/modeldev-webhook.md)。组件独立部署，代码和发布由本仓库维护。

`THIRD_PARTY_NOTICES.go-kratos-layout.txt` preserves the upstream template's
MIT notice. This generated repository intentionally has no project `LICENSE`;
its owner must make that choice before publication.

## Runtime and verification commands

For CPU-P01, execute these commands only in the task's fixed Fedora checkout.
Starting the runtime shell there is module verification, not target-cluster
business acceptance.

The minimum Go toolchain is 1.26.9. Fedora verification and release builds use
this patched toolchain through task-scoped `GOTOOLCHAIN` selection.
CI reads this requirement from `go.mod`. The prior Go 1.25.7 toolchain failed the
standard-library vulnerability gate; use the declared minimum or a newer
supported toolchain and retain the vulnerability scan. See the
[official Go release history](https://go.dev/doc/devel/release).

```bash
make tools
python3 scripts/with-test-postgres.py make verify
go run ./cmd/ani-modeldev-service -conf ./configs
```

The verification wrapper creates one digest-pinned PostgreSQL test container
with loopback access, CPU/memory limits, temporary data, and a restricted runtime
role, then removes only that invocation's container. It requires Docker and
Python 3. To use an already prepared task-owned test database, provide protected
`CPU_P01_TEST_DATABASE_URL` and `CPU_P01_TEST_DATABASE_ADMIN_URL` environment
references and run `make verify` directly. Missing database conditions fail the
persistence tests; they are never silently skipped. These are module checks.

`make verify` also runs the explicit `revisionupgrade` lane. It builds a private
old-writer test binary from retained commit
`a827457f999646394518c640d7c21915e1321496`, seeds an isolated pre-0012 schema through
the real old repositories, and verifies the migration with the current writers.
Keep full Git history available, as CI does; missing historical source, build,
binary or database conditions fail this lane rather than skip it. The script
records the source and binary SHA256 and removes only its temporary export.
No developer scratch files or prebuilt binary are required. Plain `go test ./...`
excludes this tagged lane and does not establish upgrade verification.

After the initial source commit, run `make supply-chain-tools` and `make audit`;
review and commit `docs/scaffold/bom.cdx.json`. CI deliberately fails when that
runtime SBOM is missing or stale and reruns the vulnerability, secret,
notice-integrity, and license-evidence gates.

The committed listeners are loopback-only local defaults. Override them through
the typed `ANI` environment configuration when the deployment design is added.

## Runtime shell

- Kratos lifecycle with graceful shutdown.
- gRPC and a separate admin HTTP server.
- Structured redacted logs, tracing, metrics, and middleware.
- `/healthz` reports process liveness.
- `/readyz` remains HTTP 503 while the required business adapters are absent;
  starting listeners does not establish business readiness. Real dependency
  checks belong to the production composition when those adapters are added.
- `/metrics` exports the local Prometheus registry.

CPU-P01 domain, persistence and adapter slices are present. An explicit typed
`command` configuration assembles the Governance mTLS `AcceptExecution` and
`ApplyCloseIntent` handlers with a real PostgreSQL repository using mounted
connection/certificate references. Admission ACKs carry the committed aggregate
states and revision; they do not authorize an external resource creation.
Missing materials fail startup; omitting the block keeps the unready shell.
See [command delivery](docs/design/cpu-p01-command-delivery.md) for the trust,
receipt and configuration boundaries. An optional `command.admission_resolution`
block adds read-only snapshot resolution on that same mTLS listener using pinned
mounted facts, the immutable Release catalogue and the existing runtime pool.
See [admission configuration](docs/design/cpu-p01-admission-resolution.md#启动配置)
for material and readiness boundaries. An explicit `runtime` block additionally
assembles the durable dispatch worker, dedicated managed-step TLS listener,
current Kubernetes/KFP identity checks, Trainer, workspace and publication
verification. See [runtime configuration](configs/examples/managed-runtime.yaml).
The runtime also scans the configured tenant/environment for bound executions
with a durable close intent or an expired original deadline. It resumes closing
after restart, stops the verified KFP Run and TrainJob, and requires terminal
controller and writer evidence before recording CLOSED. Missing/uncertain
observations retain the creation fence and remain unresolved. Reconciliation
before a Run authority exists is not yet part of this worker.
Its `/readyz` reports whether these configured service entry points can serve;
it does not establish cluster or business acceptance. The same Governance mTLS
listener serves execution detail, published artifact listing and download grants.
Each query requires the BFF's current actor authorization, exact RPC delegation
and tenant-wide data scope; command delivery or the historical admission actor
does not grant read access. Other query capabilities remain unavailable.

Artifact metadata contains only the public filename, role, size, digest and
publication time. Downloads resolve the durable artifact ID to its verified,
fixed S3 VersionID and return a 60-second HTTPS GET URL. The current BFF applies
`Cache-Control: no-store`; do not persist or log a returned URL. Ordinary callers
use the existing BFF detail, artifacts and artifact-content routes, never the
internal command or managed-step ports. The query RPC delegation uses
`x-ani-authorized-method` and `x-ani-data-scope: tenant-all`, set only by the
authenticated Governance client after its current permission checks. Permissions
with narrower data scopes remain denied until that scope is implemented.

## Run the CPU main flow on Fedora

The explicit integration entry runs real ModelDev RPCs, authentication,
PostgreSQL transactions, component code and CPU PyTorch training. Only external
KFP, Kubernetes and S3 HTTP APIs are test substitutes. It checks one Run and
TrainJob creation, 48 optimizer steps, actual uploaded bytes, publication after
uploader exit, writer closure, durable CLOSED and independent checkpoint reload.
It also corrupts the fixed-version input at the S3 boundary and checks that the
real verifier rejects it, no TrainJob or publication is created, and the failed
execution is durably closed with explicitly skipped downstream tasks.
It is separate from default unit tests and does not establish BFF/LIVE acceptance.

On the authorized Fedora host, supply the existing protected
`CPU_P01_TEST_DATABASE_URL` and `CPU_P01_TEST_DATABASE_ADMIN_URL` references,
`CPU_P01_MLP_IMAGE` as a locally available CPU training image pinned by its
manifest digest, and `CPU_P01_MAINFLOW_OUTPUT` as a new absolute output directory.
Set `CONTAINERS_STORAGE_CONF` when that image uses task-specific Podman storage.
The image must provide `/opt/venv/bin/python` with CPU PyTorch. No image is pulled.

```sh
./scripts/test-main-flow
```

The focused owner-stop case uses the same Fedora database/image settings. It
waits for an actual slow-stop optimizer step, commits USER_STOP through mTLS,
kills its task-owned owner process after it reads the durable fence, starts a
new owner process, and checks automatic closure without creating another Run
or TrainJob:

```sh
go test -tags cpu_mainflow -count=1 -v -run '^TestMainFlowOwnerStop' ./internal/data/submittest
```

The related recovery cases check the original deadline before and after
dispatch, stop delivered before admission, lost Run/TrainJob create responses,
and retained ambiguity after discovering multiple Runs. They use real
persistence and original resource identities; unresolved creation remains
fenced for review. Run only the cases relevant to the change:

```sh
go test -tags cpu_mainflow -count=1 -v -run '^(TestMainFlowDeadline.*|TestMainFlowStopBefore.*|TestMainFlowLateRun.*|TestMainFlowLateTraining.*)$' ./internal/data/submittest
```

The complete successful flow also reads the actual training stdout through the
authenticated query service. Log reads resolve the recorded training Pod and
check its UID before and after reading, with limits of 1000 lines, 64 KiB and
five seconds. The execution-list case verifies tenant filtering before paging.

The output directory retains the independently downloaded checkpoint, model
configuration, metrics, summary, canonical manifest and inference result.
`make build` produces both service and single-step component executables.

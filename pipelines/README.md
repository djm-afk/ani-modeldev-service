# CPU-P01 KFP assembly

Run compilation and image construction from a fixed Fedora checkout. This is
the single assembly for `prepare → train-wait → collect → publish → close`.
Only `execution_id` and `spec_hash` are Run parameters. Tenant and infrastructure
bindings come from mounted owner configuration; the service resolves the original
operation only after checking the current workload and the frozen KFP Run.

The same compiler also accepts an optional `kind: FINETUNING` deployment binding.
It then assembles `prepare → train-wait → export → verify → collect → publish →
register → close` with the same failure finalizer. The register component first
confirms actual published object versions and uploader termination. Without a
configured Model provider it records the original registration intent and fails
with an explicit dependency error; the owner closes the real resources. This
does not enable a public creation mode or manufacture a Model READY receipt.

The fine tuning image uses `finetuning-step.Dockerfile` with the verified workload
OCI manifest as `WORKLOAD_IMAGE` and the static step binary from the final pushed
commit. Fine tuning has disk `/tmp`, the workspace capacity frozen in its installed
material, separate export/verify subpath mounts and the admitted finite timing limits. Pinned KFP 2.16.0 does not encode read-only
PVC mounts, root filesystem or ephemeral-storage resource requests. The formal
[ModelDev webhook](../deploy/modeldev-webhook/README.md) provides and validates
those fields for explicitly enrolled namespaces before live acceptance.
Both execution kinds require a frozen `admission_profile` binding. The compiler
labels every runnable step with `ani.modeldev/admission-profile` and
`ani.modeldev/step-role`, including the failure-close component. Newly compiled
IR rejects a missing or malformed profile. Published IR and canonical software
materials stay immutable. Publish a new PipelineVersion and installed material
after validating the shared webhook; parameter and public compute selections
are resolved for each execution without a combination approval. The compiler
does not register cluster objects.

```sh
python3 -m venv /absolute/task/kfp-venv
/absolute/task/kfp-venv/bin/pip install -r pipelines/requirements.txt
./scripts/build-step-image /absolute/task/new-image-evidence
/absolute/task/kfp-venv/bin/python pipelines/cpu_p01.py \
  --config /absolute/task/frozen-compile.json \
  --output /absolute/task/general-cpu.yaml
```

All `/absolute/task/...` paths and `${...}` values below are placeholders, not
executable bindings. The image build uses the existing task-private Podman
storage when `CONTAINERS_STORAGE_CONF` is set. It builds a static executable and
a scratch image, records its manifest digest and source SHA, and does not push.
An uploaded registry copy must retain the digest before an actual Run uses it.

`frozen-compile.json` requires exactly these explicit fields:

```json
{
  "component_image": "${PUBLISHED_COMPONENT_IMAGE_AT_SHA256}",
  "owner_config_map": "${OWNER_CONFIG_MAP}",
  "storage_class": "${ENV_STORAGE_CLASS}",
  "workspace_size": "${FROZEN_WORKSPACE_CAPACITY}",
  "workspace_access_mode": "${ENV_WORKSPACE_ACCESS_MODE}",
  "admission_profile": "${FROZEN_MODELDEV_ADMISSION_PROFILE_ID}",
  "execution_timeout_max_seconds": "${FINITE_DEPLOYMENT_MAXIMUM_M}",
  "component_resources": "${FROZEN_COMPONENT_ROLE_RESOURCES_OBJECT}"
}
```

Replace `component_resources` with the immutable material’s complete role map: every role has explicit CPU, memory and ephemeral-storage request/limit quantities. This same canonical role value is hashed into the compiled Pod annotation and checked by admission. KFP uses its role’s ephemeral-storage limit for control I/O under `/tmp`; application temporary files and HF/Arrow caches use the execution PVC at `/workspace/work/<role>`. Training uses only PVC work storage.

The owner ConfigMap mounts at `/etc/modeldev-step`. It contains `config.json`
and the trusted `modeldev-ca.pem`/`s3-ca.pem` files. `config.json` has this shape;
the path under `publication` must match the frozen workspace contract:

```json
{
  "tenant_id": "${FROZEN_TENANT_UUID}",
  "namespace_uid": "${ENV_NAMESPACE_UID}",
  "token_file": "/var/run/secrets/kubernetes.io/serviceaccount/token",
  "grpc_target": "${MODELDEV_STEP_HOST_AND_PORT}",
  "grpc_server_name": "${MODELDEV_STEP_CERTIFICATE_NAME}",
  "grpc_ca_file": "/etc/modeldev-step/modeldev-ca.pem",
  "workspace_directory": "/workspace",
  "inventory_file": "/workspace/publication/collected-output.json",
  "poll_interval_seconds": 2,
  "timeout_seconds": "${FINITE_OUTER_BUDGET_COVERING_FROZEN_EXECUTION}",
  "s3": {
    "endpoint": "${ENV_HTTPS_S3_ENDPOINT}",
    "region": "${ENV_S3_REGION}",
    "ca_file": "/etc/modeldev-step/s3-ca.pem"
  }
}
```

Replace both timeout placeholders with integers. `execution_timeout_max_seconds`
is the finite deployment maximum M and is required by the compiler. The outer component and
streaming budgets must cover the frozen role window; the business deadline
still limits every request. KFP task insurance uses the same deployment maximum
for every role.

Do not set `context_file` or a static `task_id` in this dynamic mode. KFP supplies
the actual Run ID and Downward API Pod identity. The service resolves the actual
task ID from the authenticated Run and Workflow and returns it to the component.
Each component reads its
current Pod through the Kubernetes API and checks its UID and Workflow controller
owner. The server independently verifies TokenReview, current objects and KFP's
own task association. No component creates TrainJobs.

Prepare and publish request execution-scoped STS credentials from ModelDev using
their current authenticated workload identity. The SDK refreshes them through
the same RPC. Only ModelDev control mounts the ordinary restricted RustFS IAM
user's long-lived credential; no storage Secret is mounted into these components
or training. Closing executions cannot obtain or renew sessions. Never put values
in the ConfigMap, IR, Git or command arguments. The shown token path uses the current bound ServiceAccount
token; its actual audience must match the server's configured TokenReview
audience. A different projected audience requires an ENV-managed injection and
the corresponding explicit file path; the compiler does not invent one.

The `workspace-name` component computes `ani-kfp-workspace-<execution UUID>`
without any API client or storage credential. KFP's official `CreatePVC` then
allocates that execution's retained workspace without a Workflow ownerReference.
Its `argostub/createpvc` marker is interpreted by KFP's backend, not run as a
container image. Prepare/collect/publish mount that PVC; train-wait and close do
not. Components and the CPU training image use UID/GID 10001. ENV must establish
PVC write access for that identity (for example through its managed Pod/volume
group policy) and the actual node/storage mount capability. A compiled IR does
not prove those permissions. Workspace class/capacity must equal the installed material and frozen
execution snapshot. This Pipeline has no DeletePVC step.

Preparation no longer publishes whole directories through
`renameat2(RENAME_NOREPLACE)`. A separate prepare Pod writes fixed inputs to the
execution's PVC, verifies actual content, saves the existing prepared manifest
and durable preparation record, and exits successfully before training uses
that same PVC with read-only inputs. Missing, partial or conflicting content is
not ready; replay retains original identities and bytes. The training entry must
verify actual inputs against the original manifest and frozen references before
model loading or computation. CSI supplies the mounted filesystem; this design
does not call storage-vendor APIs or require a Ceph-specific branch.

Preparation rejects an existing model tree without its original valid completion
manifest. Reusing an existing manifest requires verification of every actual input;
it cannot repair missing or conflicting bytes. Both RWO and RWX are compiler inputs;
actual volume access, persistence and sequential role mounts remain environment
acceptance checks for each installed image and workspace material.

Publish writes a small candidate to KFP's declared string `OutputPath`. Close
receives that string through `--candidate-json` into an independent temporary
file. Its empty default and `ignore_upstream_failure()` keep close runnable when
publication fails. Server-side remote byte verification and uploader termination
still precede PUBLISHED. All tasks disable cache and retries. The owner recovery
path remains necessary if the workflow or close Pod itself cannot run.

After verified publication, close persists a `NATURAL_TERMINAL` creation fence
and finishes on its durable `CLOSING` receipt. The finalizer also finishes after
delivering a valid fence. The owner waits for natural KFP completion and verifies
the original Run, completed Workflow and all historical workspace writers before
marking `CLOSED`. Stop, failure and deadline still terminate the original Run.
A pending natural close reaching its frozen deadline escalates through the same
database transaction without replacing its generation or first receipt time.

A complete Kubernetes `400/BadRequest` or `403/Forbidden` training-create
rejection is retained against the original request and namespace. Train-wait
then fails without another POST. Partial responses, conflicts and unavailable
lookups retain their uncertain outcome and cannot establish writer absence.

The selected ServiceAccount comes from ModelDev's frozen CreateRun request.
ENV must supply its minimal current-Pod read and workspace permissions, server
connectivity and distinct training identity. Compilation, module tests and a
locally built image are not KFP upload, real PipelineVersion binding or L2–L4
acceptance. A fixture binding cannot enable business acceptance.

The focused compiler test is `python -m unittest discover -s pipelines -v` using
the pinned virtual environment. It parses the emitted IR with KFP's protobuf
schema and checks the actual graph, failure-close policy, parameter types,
image pins, mounts, cache and retry settings.

Official pinned API sources: [runtime placeholders](https://github.com/kubeflow/pipelines/blob/2.16.0/sdk/python/kfp/dsl/__init__.py),
[PVC primitives](https://github.com/kubeflow/pipelines/blob/2.16.0/kubernetes_platform/python/kfp/kubernetes/volume.py).

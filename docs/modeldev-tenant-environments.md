# Dynamic tenant execution environments

ModelDev runs one shared service and webhook. An explicit, authorized
`EnsureTenantExecutionEnvironment` registers and advances one environment for
the trusted tenant and fixed platform cluster. `GetTenantExecutionEnvironment`
reads that registration. Business admission, dispatch and workload admission
never provision an environment as a side effect.

The existing Namespace must be exactly `tenant-<canonical nonzero tenant UUID>`,
Active, and owned by `network.ani.io/managed-by=ani-network-service` and
`network.ani.io/tenant-id=<tenant>`. ModelDev neither creates nor adopts it.
The original Namespace UID, three ServiceAccount UIDs, Experiment and sealed
configuration cannot be rebound. A replacement identity requires investigation.

## Authoritative inputs

| Input | Effective contract |
| --- | --- |
| `runtime.tenant_environment_file` and `tenant_environment_sha256` | One non-sensitive `ani.modeldev.tenant-environment-platform.v1` JSON specification and the SHA256 of its original bytes |
| `command.admission_resolution.catalogue_directory` | The four canonical material files from the same immutable bundle, with exact published Pipeline/Version IDs and raw IR hashes |
| Platform `ir_sources` | Four fixed original IR files, each checked against its raw SHA before complete structural comparison with KFP GetVersion |
| Shared manifest | `ani.modeldev.material-bundle.v1`, exactly four canonical materials and four original IRs; its raw digest is shared by the service and webhook |
| Webhook configuration | `ani.modeldev.webhook.v2`, global cluster/bundle/controller identities, driver and four profiles; no `bindings` field |
| Namespace projection | Immutable `modeldev-execution-environment`, exactly `binding.json` and `binding.sha256`; schema `ani.modeldev.tenant-execution-environment.v1` |
| Accepted execution | `ani.modeldev.execution-spec.v4`, including sealed environment, original Namespace/SA UIDs, dispatch owner and managed image access in SpecHash |

The platform specification contains public endpoints/CA material, file references,
storage connection and bucket templates, workspace/timing, managed registry
authority/project, exact trusted/controller principals, twelve observed bootstrap
object UID/digest pins, and four profile/material identities. It contains no
tenant list, binding/facts array, DSN, token or AK/SK values. Configuration shapes
are defined by `internal/data/tenantenvironment.Config` and
`internal/server/modeldevwebhook.Config`; unknown fields are rejected.

`binding_file`, `binding_sha256`, `facts_files`, `controlled_model_facts`,
`dispatch_batch_size`, Model `approved_prefix`, and credential `sts_bindings`
are retired. Supplying them is an error. There is no compatibility loader.
The example [managed runtime](../configs/examples/managed-runtime.yaml) supplies
the active field names and required catalogue/Resource connection blocks.

ConfigMaps project symlinks. The pinned webhook image's bounded
`--copy-materials-from/--copy-materials-to/--copy-materials-manifest-sha256`
init mode verifies the shared manifest and all eight file hashes before
installing regular read-only files. A separate
`--copy-platform-from/--copy-platform-to/--copy-platform-sha256` invocation
installs the independently pinned platform JSON. Runtime readers keep
O_NOFOLLOW; the platform JSON remains outside the manifest to avoid a digest
self-reference. Rotating token/credential files use their dedicated projected
volumes without subPath pinning.

## Registration and execution

The natural `(tenant_id, cluster_id)` key is unique in PostgreSQL. The persisted
environment ID is also BindingID. Each Ensure has a 25-second total budget,
upstream calls at most five seconds, and a dedicated connection advisory lock.
Busy calls return the persisted state. Typed create intents precede external
writes; retries recover the same names and original UIDs. KFP POST uncertainty
does not authorize an unproven duplicate Experiment. Get has a three-second
budget and never inserts a row.

Ensure verifies the existing Namespace, twelve fixed public objects and the
dedicated artifact Secret, exact bootstrap policies, four published definitions,
and complete finite authorization inventory. It seals canonical non-sensitive
binding bytes, publishes and reads back the immutable projection, then CASes
READY. The binding digest has no self-reference; the projection's own UID/RV,
Secret values and value hashes, and mutable environment state are excluded.

Prepare and Finalize read the tenant's READY sealed record, compare current
material identities, and verify live original Namespace/SA identities. Dispatch
does the same before sending the original accepted snapshot. Close uses the
original snapshot identity and existing close fence even if current environment
readiness fails. Reserve, charge ownership and original quota release remain
unchanged; [ADR 0002](adr/0002-owner-decides-quota-release.md) remains authoritative.

KFP roots contain exactly `execution_id`, `spec_hash`,
`environment_binding_id`, and `namespace_uid`. Both driver and business step
Pods must match the Workflow's original runtime-config and
`s3://<artifact bucket>/artifacts/<tenant>/<execution>` root. No user Namespace,
Pipeline, Experiment or replacement identity is accepted.

## Storage and image authority

The input scope is `inputs/<tenant>`, publication scope is
`executions/<tenant>`, and base-model scope is `<tenant>`, derived from the same
platform connection/bucket templates. Business STS reads the persisted v4
snapshot plus tenant and narrows each grant to its exact input/model object or
`executions/<tenant>/<execution>/` publication prefix. Its mounted ordinary IAM
JSON contains only `access_key_id` and `secret_access_key`; temporary credentials
and per-tenant STS maps are rejected.

KFP artifacts use an independent dedicated identity and bucket, exposed only as
the fixed two-key `modeldev-kfp-artifacts` Secret. Before its first write or
rotation, Ensure scans complete RoleBinding/ClusterRoleBinding pages and
referenced rules using read-only inventory permissions. Unknown/dangerous grant
edges or incomplete inventory block the Secret; existing grants are not silently
rewritten. Secret values or their hashes never enter receipts or projections.

Resource's existing dynamic EnsureTenantPullSecret supplies the one managed
authority reference. `modeldev-step` and `verifier` inherit exactly that reference;
`trainer-workload` has no inherited pull references and disables token automount.
Its training Pod writes the frozen managed references explicitly. Additional or
prefix-matching references do not satisfy exact inheritance.

## Policy, deployment and evidence

One global webhook uses an OR match over reserved SAs, metadata/finalizers,
workspace PVCs and artifact Secret references. Unknown matched environments
deny. Every matched CREATE reads current Namespace/projection/original SA;
there is no tenant cache, business DB access or outside-scope fallback for a
protected reference. Ordinary Pods skip the remote webhook entirely.

The native `modeldev-bootstrap-v1`, `modeldev-environment-config-v1` and
`modeldev-workload-guard-v1` policies and their Deny bindings must be installed and
verified before granting finite bootstrap CREATE/bind. They use exact observed
control, webhook, KFP, Argo, Trainer, JobSet, Job, persistence, maintenance and node
identities. They protect oldObject identity/templates, legacy token Secret
bypasses and retained workload updates while permitting the original terminate
and status paths. Native API-server typeChecking must observe the installed
generation with no expressionWarnings. Offline rendering does not prove this.

[Bundle publication/rendering](../deploy/modeldev-environment/README.md),
[native policies](../deploy/modeldev-tenant-policy/README.md), and
[webhook deployment](../deploy/modeldev-webhook/README.md) provide the concrete
commands. The test environment's only cluster entry is
`ssh kubercloud@100.64.128.12`. Verify the complete designated-cluster tuple before
each write window; apply updates through `http://192.168.25.40:9998/`.
Heavy generation/build/test/image work runs on Fedora, without packaging files
there for transfer. Local work is source editing, Git and file transfer.

Before switching, close old executions through the original fences, obtain the
original quota release acknowledgements, and clean the authorized old v3 data
and obsolete definitions. Then switch the shared service/webhook and explicitly
Ensure tenants A and B. PostgreSQL/handler/component tests, manifest rendering,
server dry-run, deployment readiness and actual A/B training/artifact isolation
are separate evidence layers. Every PASS requires its real recorded output;
unexecuted or blocked live checks remain NOT_RUN or BLOCKED. This document does
not assert a deployment or real business acceptance result.

# ModelDev webhook and retained compute exit cleanup

ModelDev owns one Pod admission component, built from `cmd/ani-modeldev-webhook`
and deployed independently of its business listeners. GENERAL_TRAINING and
FINETUNING share that component. The deployment material is in
[deploy/modeldev-webhook](../deploy/modeldev-webhook/README.md); accepted ownership
and migration choices are recorded in [ADR 0001](adr/0001-modeldev-webhook.md).

The webhook reads mounted, typed configuration and canonical SHA256-pinned
Release files. A binding enrolls a precise namespace UID, its management label,
trusted Argo creator, step ServiceAccount and finite profile IDs. The Pipeline
compiler requires the frozen profile ID and puts `ani.modeldev/admission-profile`
and `ani.modeldev/step-role` on every runnable component, including failure close.
The request cannot supply replacement policy, images or Release bytes.

KFP backend driver Pods share the KFP v2 label but do not inherit component
profile labels. An explicit binding separately pins the driver image, command and
support images. The service verifies its original Workflow and `--run_id`/`--type`
association and adds exit retention only. It does not apply computation mounts,
UID or resource mutations to that backend executable. This keeps driver exits in
the complete owner proof without rejecting the backend that creates components.

For matching CREATE Pod requests, the webhook reads the live namespace and
original Workflow, checks their identity and Run association, validates the
role's immutable image and mount contract, then returns a JSONPatch for necessary
security, read-only mount, temporary-storage and step exit-retention fields.
Unrelated fields and finalizers survive. It performs no business database writes,
object-store operations, training, execution close or resource deletion.

The process serves bounded HTTPS admission, liveness, dependency readiness and
metrics. Logs contain fixed decision categories, not request bodies, tokens or
upstream error text. Certificates reload on new TLS handshakes. Deployment
materials declare cert-manager renewal and multiple replicas; this does not prove
that the target cluster has the issuer or that renewal has been tested there.

## Exit facts, close and cleanup

The two finalizers have different owners and propagation paths:

| Object | Add retention | Record and release |
| --- | --- | --- |
| KFP step Pod | Shared webhook adds `modeldev.ani.io/step-exit-evidence` | ModelDev persists complete original Pod exit proof, then reconciles exact objects after CLOSED |
| Actual training Pod | Frozen Runtime Pod template contains `modeldev.ani.io/training-exit-evidence` | Existing Trainer adapter persists exit history and reobserves the original TrainJob/JobSet/Job/Pod chain before CAS removal |

Neither NotFound, parent completion, DELETE acceptance nor an admission patch
proves that a writer exited. All-container termination or the narrowly verified
initialization-abort proof and original object identities must be retained in the
real execution repository before releasing retention. The webhook only adds the
step mark; the business owner performs this reconciliation outside admission.

After CLOSED, the close worker also discovers executions with unfinished step
or compute cleanup. It first persists the complete post-close owner proof and cleanup
receipt, then tests UID, resourceVersion, ownerReferences and the finalizer set
before removing only the step mark. Deletion targets that same original UID and
resourceVersion, and a later GET confirms absence. A lost response or restarted
owner resumes from the receipt. A replaced UID or uncertain/active writer remains
unresolved; another controller's finalizer is preserved and never forcibly cleared.

After step disposal, the same owner freezes the original terminal training
Pod, TrainJob/JobSet/Job and Workflow identities in a durable compute cleanup
receipt. It captures a bounded training log tail before any training Pod DELETE.
Log queries retain their existing authorization and original identity checks and
can read that saved tail after the Pod is gone. The cleanup receipt preserves
the full original exit graph; an equivalent verifier checks the receipt and live
absence when an explicit later CleanupPlan needs that graph.

Controller removal tests original UID and resourceVersion and confirms absence
after each DELETE. Normal terminal controllers use orphan propagation. The
suspended Stop/deadline path additionally verifies the original suspend fence
and protects the retained PVC before background deletion of the original
TrainJob; observed in-progress deletion remains pending. Replacement identities,
unknown workspace writers and foreign finalizers block disposal. Attempts are
bounded and persist their unresolved result across owner restart. PVCs and
publications remain available; automatic compute cleanup never authorizes their
deletion.

## Verification and rollout boundary

Run source generation, formatting, builds, tests and images on Fedora only.
`make build` includes the standalone webhook executable; `make verify` is the
commit gate. Compile both Pipeline kinds with their actual frozen profile IDs and
inspect the real IR rather than synthesizing the DAG.

The focused integration case is `TestMainFlowUnifiedWebhookActualMLPToDurableExitCleanup`
in the `cpu_mainflow` lane. It uses the production HTTPS AdmissionReview handler
and applies its actual patch at the external Kubernetes boundary, then runs real
ModelDev authorization, PostgreSQL, components, CPU training, publication and
owner close. It verifies a durable cleanup receipt across reconnect and precise
step disposal while retaining training logs, volume and publication. KFP,
Kubernetes and S3 remain explicit external boundary substitutes in this lane;
it cannot establish LIVE or M3-A acceptance. Additional shared-retention cases
cover both execution kinds and recovery/CAS failures.

Render and review the service, typed binding/profile material,
certificate/issuer dependency, RBAC and separate registration manifest before
registration. Register only within the cluster owner's explicit authorized
scope; source checks do not establish that any cluster was changed. Keep the old registration untouched
while old Runs use it, verify new PipelineVersion/Release bytes and nonoverlapping
selectors, then migrate each enrolled namespace. Existing step/training finalizer
names remain compatible; no bulk finalizer deletion is part of migration.

Source/Fedora checks establish implementation readiness only. Target-cluster
admission, HA/renewal, CPU and fine-tuning business Runs, stop/deadline/restart,
detached artifact retrieval and M3-A remain separate real acceptance requirements.

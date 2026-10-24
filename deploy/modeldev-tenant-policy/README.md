The renderer emits a native TrainJob safety policy for one immutable namespace
scope. Supply the actual namespace name/UID and installed CPU/GPU image
digests. It does not approve executions or replace the production stored
Snapshot/Runtime/Pod verifier. There is no per-execution ConfigMap or resource
combination list.

Create the policy only after server dry-run; require its observed generation
and zero CEL type warnings, then create/read back its Deny binding. Validate
native admission using real TokenRequest credentials, never impersonation.
Only then exclude this namespace's immutable `kubernetes.io/metadata.name`
from the original legacy TrainJob binding using UID/resourceVersion JSON Patch
tests. Preserve every original selector, parameter reference and policy byte.

Before switching, save the complete original binding and native policy facts.
Recovery restores the saved legacy namespace selector with current UID/RV
tests before removing the task policy/binding; it must never expose an
unprotected interval. Retain the task policy while active task workloads
still depend on it.

Native CEL `namespaceObject.metadata` does not expose UID. Deployment verifies
the actual namespace UID before and after installing the controlled
`modeldev.ani.io/namespace-uid` label; the policy requires that marker as a
validation, never a match condition that could skip admission. The marker
defines scope. It cannot independently defeat an administrator copying an old
label onto a replacement namespace. Physical UID binding remains enforced by
the original stored Snapshot and Runtime/Pod verifier's independent namespace
GET; those checks and their replacement-UID negative cases remain required.

The formal owner may stop its original spec with `suspend=true`; it cannot
resume or change it. Trainer status/finalizer maintenance preserves spec and
identity metadata. Garbage collection may only remove the original `orphan`
finalizer after deletion. None of these branches permits a new workload.

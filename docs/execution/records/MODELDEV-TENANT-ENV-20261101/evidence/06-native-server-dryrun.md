# Native server schema dry-run

Outcome: **FAIL / deployment blocked**. The designated API server accepted19 of21 rendered objects and rejected two native policies. No policy or bootstrap object was installed or changed.

The complete designated SSH user/host, context, API server, configured cluster ID and kube-system UID were reverified immediately before requests; actual values/timestamp are in the JSON evidence. The same object was read before/after every dry-run, including UID/RV/full-object digest where present; all comparisons remained unchanged.

Render input SHA256: `c8bfb12bf36eaa8fcb181054481c45c17c8ee414f3e502deb0b62e5c21ee57ec`.
Rendered manifest SHA256: `c5366329d5c8e40e3505b03d986c5e05cd3290769995ca1d53db4d7356e896bf`.
Renderer source SHA256: `2a3f248ebdf9e128444ee7cf91f7ed1bd0c8aeba802c4a3c419169d3588e5ec2`.

Rendering ran on Fedora with the recorded render_input from native-principals.json. Each object was then passed only through `kubectl --context kubernetes-admin@ani-kcn-full --request-timeout=8s apply --dry-run=server -f - -o json` via ssh kubercloud@100.64.128.12.

| Kind | Name | Namespace | Exit | Server result |
| --- | --- | --- | ---: | --- |
| ValidatingAdmissionPolicy | modeldev-bootstrap-v1 | - | 1 | SERVER_DRYRUN_REJECTED |
| ValidatingAdmissionPolicyBinding | modeldev-bootstrap-v1-binding | - | 0 | SERVER_DRYRUN_ACCEPTED |
| ValidatingAdmissionPolicy | modeldev-environment-config-v1 | - | 0 | SERVER_DRYRUN_ACCEPTED |
| ValidatingAdmissionPolicyBinding | modeldev-environment-config-v1-binding | - | 0 | SERVER_DRYRUN_ACCEPTED |
| ValidatingAdmissionPolicy | modeldev-workload-guard-v1 | - | 1 | SERVER_DRYRUN_REJECTED |
| ValidatingAdmissionPolicyBinding | modeldev-workload-guard-v1-binding | - | 0 | SERVER_DRYRUN_ACCEPTED |
| ClusterRole | modeldev-runtime-v1 | - | 0 | SERVER_DRYRUN_ACCEPTED |
| ClusterRole | modeldev-step-v1 | - | 0 | SERVER_DRYRUN_ACCEPTED |
| ClusterRole | modeldev-webhook-read-v1 | - | 0 | SERVER_DRYRUN_ACCEPTED |
| ClusterRole | modeldev-kfp-api-v1 | - | 0 | SERVER_DRYRUN_ACCEPTED |
| ClusterRole | modeldev-kfp-access-v1 | - | 0 | SERVER_DRYRUN_ACCEPTED |
| ClusterRole | modeldev-environment-control-v1 | - | 0 | SERVER_DRYRUN_ACCEPTED |
| ClusterRoleBinding | modeldev-environment-control | - | 0 | SERVER_DRYRUN_ACCEPTED |
| ClusterRole | modeldev-webhook-namespace-v1 | - | 0 | SERVER_DRYRUN_ACCEPTED |
| ClusterRoleBinding | modeldev-webhook-namespace | - | 0 | SERVER_DRYRUN_ACCEPTED |
| ClusterRole | modeldev-rbac-inventory-global-v1 | - | 0 | SERVER_DRYRUN_ACCEPTED |
| ClusterRoleBinding | modeldev-rbac-inventory-global | - | 0 | SERVER_DRYRUN_ACCEPTED |
| ClusterRole | modeldev-kfp-platform-read-v1 | - | 0 | SERVER_DRYRUN_ACCEPTED |
| RoleBinding | modeldev-kfp-platform-read | ani-system | 0 | SERVER_DRYRUN_ACCEPTED |
| ClusterRole | modeldev-token-review-v1 | - | 0 | SERVER_DRYRUN_ACCEPTED |
| ClusterRoleBinding | modeldev-token-review | - | 0 | SERVER_DRYRUN_ACCEPTED |

## Actual validation errors

- `modeldev-bootstrap-v1`: `spec.variables[0].name` rejects `namespace`: `must be a valid CEL identifier`.
- `modeldev-workload-guard-v1`: the same reserved variable-name failure at `spec.variables[3].name`; additionally `newPods` and `oldPods` Workflow pseudo-PodSpec expressions fail CEL compilation at the heterogeneous map container-list value: `expected type dyn but found list(dyn)`.

These are source/CEL schema failures from the actual API server, rather than an observed rejection by an external old policy. Original bounded stderr is preserved verbatim per object in the log and JSON; no stderr was truncated. No source repair was made in this task. A candidate minimal repair is renaming that local variable/reference and explicitly normalizing heterogeneous pseudo-spec map value types; any repair still needs a fresh server dry-run.

## Evidence limits

The accepted environment-config VAP returned `status: {}`. There is no persisted object/controller-observed generation/typeChecking result for these dry-run objects. Successful schema requests do not prove native enforcement, expressionWarnings absence after installation, ordinary-workload coexistence, terminate/status behavior, AD01/AD02 or LIVE02. Those remain NOT_RUN until the ordered installation and positive/negative probes are permitted. The two rejected policies are FAIL, not PASS.

Files: `06-native-server-dryrun.input.json`, `.rendered.json`, `.runner.py`, `.json`, `.log`. The JSON records per-object input digest, return code, finite raw errors/warnings, response hashes and original-state comparisons.

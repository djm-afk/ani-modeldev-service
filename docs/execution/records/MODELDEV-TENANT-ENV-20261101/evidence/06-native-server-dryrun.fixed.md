# Native server schema dry-run after minimal CEL repair

**SERVER_SCHEMA_PASS**: all21 objects, including all three VAPs, returned exit0 from the designated API server; no stderr errors/warnings. Full identity was reverified immediately before this run. No policy or bootstrap object was installed, and all original before/after UID/RV/object-digest observations remained unchanged.

Fedora module validation: `python3 -m unittest discover -s deploy/modeldev-tenant-policy -p test_render.py -v`, three tests, exit0 (`06-native-server-dryrun.python-tests.log`).

Initial actual server validation failed for reserved variable `namespace` and heterogeneous Workflow pseudo-PodSpec map types. That first failing output remains in `06-native-server-dryrun.{json,log,md}`. The repair renames only the local variable/reference to `standardNamespace` and wraps each of the four heterogeneous map values with CEL `dyn`. All fields, branches, validation rules, matching scope, principals and permissions remain unchanged.

Changed rendered objects: ValidatingAdmissionPolicy/modeldev-bootstrap-v1, ValidatingAdmissionPolicy/modeldev-workload-guard-v1. The other19 objects compare structurally identical to the first rendered manifest.

Final renderer SHA256: `8e8f58725ec99b6f8e483543c5ed2cacd1ce4edc96a7abe063e3d02541c403d8`.
Final rendered manifest SHA256: `c0c0b90750be1817aa84c70636b77843d2296df018260b7233e87843903adbbe`.

| Kind | Name | Namespace | Exit | Original unchanged |
| --- | --- | --- | ---: | --- |
| ValidatingAdmissionPolicy | modeldev-bootstrap-v1 | - | 0 | True |
| ValidatingAdmissionPolicyBinding | modeldev-bootstrap-v1-binding | - | 0 | True |
| ValidatingAdmissionPolicy | modeldev-environment-config-v1 | - | 0 | True |
| ValidatingAdmissionPolicyBinding | modeldev-environment-config-v1-binding | - | 0 | True |
| ValidatingAdmissionPolicy | modeldev-workload-guard-v1 | - | 0 | True |
| ValidatingAdmissionPolicyBinding | modeldev-workload-guard-v1-binding | - | 0 | True |
| ClusterRole | modeldev-runtime-v1 | - | 0 | True |
| ClusterRole | modeldev-step-v1 | - | 0 | True |
| ClusterRole | modeldev-webhook-read-v1 | - | 0 | True |
| ClusterRole | modeldev-kfp-api-v1 | - | 0 | True |
| ClusterRole | modeldev-kfp-access-v1 | - | 0 | True |
| ClusterRole | modeldev-environment-control-v1 | - | 0 | True |
| ClusterRoleBinding | modeldev-environment-control | - | 0 | True |
| ClusterRole | modeldev-webhook-namespace-v1 | - | 0 | True |
| ClusterRoleBinding | modeldev-webhook-namespace | - | 0 | True |
| ClusterRole | modeldev-rbac-inventory-global-v1 | - | 0 | True |
| ClusterRoleBinding | modeldev-rbac-inventory-global | - | 0 | True |
| ClusterRole | modeldev-kfp-platform-read-v1 | - | 0 | True |
| RoleBinding | modeldev-kfp-platform-read | ani-system | 0 | True |
| ClusterRole | modeldev-token-review-v1 | - | 0 | True |
| ClusterRoleBinding | modeldev-token-review | - | 0 | True |

All requests were only `kubectl apply --dry-run=server -f - -o json` via the unique cluster SSH entry, using a complete matching target tuple. Per-object response/status/error hashes and state comparisons are in `06-native-server-dryrun.fixed.json`; bounded original command output is in `.fixed.log`.

**NOT_RUN**: persisted controller-observed generation/typeChecking and expressionWarnings, installed-policy positive/negative behavior, ordinary-workload isolation with webhook unavailable, terminate/status behavior, AD01/AD02 and LIVE02 full acceptance. All three dry-run VAPs returned `status: {}`; absence of controller typeChecking here is not an installed-policy PASS. No source guard or existing policy was relaxed.

# Native bootstrap read-only installation plan

Old worker was actually scaled 1→0 with UID/RV/image tests; original Pod is gone. Fresh runtime-role READONLY proof is 54/54 CLOSED, original Gov acceptance54 ACKED, active Create/Close leases0, old namespace19 Pods without active containers. See `old-worker-stopped-proof-20261101T024313Z.json`.

The fresh bounded inventory found none of the desired21 bootstrap objects installed, no ModelDev-named admission objects, and no original SA/deployment UID mismatch. The fixed manifest SHA is `c0c0b90750be1817aa84c70636b77843d2296df018260b7233e87843903adbbe`: three VAPs, three Bindings, ten ClusterRoles, four ClusterRoleBindings and one platform RoleBinding. Prior server dry-run accepted21/21 only at its recorded time/source; installed typeChecking, actual enforcement and A/B Ensure remain NOT_RUN.

Complete ordered external/DB cleanup while the old worker stays stopped. Revoke the six exact legacy ModelDev bindings listed in the JSON with fresh UID/RV/subjects/roleRef/rules checks. Only after references are zero remove the two old own ClusterRoles and three old tenant Roles. Shared `system:auth-delegator`, inherited platform discovery bindings and Accelerator's runtimeclass grant remain. The obsolete auth-delegator binding currently grants arbitrary SubjectAccessReview CREATE; the new finite role grants TokenReview CREATE only.

Install/read back three fixed Fail/Deny policies and bindings first, inspect actual API-server typeChecking, then grant the finite bootstrap CREATE/bind roles. Deploy pinned webhook/service under the parent's release sequence, perform real original-principal admission/SAR cases, and only then explicit Ensure A followed by B. RBAC denial does not prove admission denial. No policy, Role, binding or service install/replacement was performed for this plan.

The old namespace Roles/RoleBindings overlap Resource's external-cleanup scope: execute them once under the coordinated manifest. Parent owns applying the remaining cluster bindings/roles and release sequence. Source JSON records exact original identities and final role rules without credential values.

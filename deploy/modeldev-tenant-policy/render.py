#!/usr/bin/env python3
"""Offline fixed ModelDev native policies and bootstrap RBAC renderer."""
import argparse
import json
from pathlib import Path
import re

PREFIX = "modeldev.ani.io/"
NAMES = ["modeldev-bootstrap-v1", "modeldev-environment-config-v1", "modeldev-workload-guard-v1"]
SA = ["modeldev-step", "trainer-workload", "verifier"]
RB = ["modeldev-runtime", "modeldev-step", "modeldev-webhook-read", "modeldev-kfp-api", "modeldev-kfp-access"]
CM = ["modeldev-step-owner", "ani-kfp-ca", "kfp-launcher", "metadata-grpc-configmap", "modeldev-execution-environment"]
q = lambda value: json.dumps(value, separators=(",", ":"))
UUID = "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"


def optional(obj, field, fallback):
    return f"(has({obj}.{field}) ? {obj}.{field} : {fallback})"


def marker(obj):
    return f"({obj} != null && ((has({obj}.metadata.labels) && {obj}.metadata.labels.exists(k,k.startsWith('ani.modeldev/') || k.startsWith('modeldev.ani.io/'))) || (has({obj}.metadata.annotations) && {obj}.metadata.annotations.exists(k,k.startsWith('ani.modeldev/') || k.startsWith('modeldev.ani.io/'))) || (has({obj}.metadata.finalizers) && {obj}.metadata.finalizers.exists(f,f.startsWith('modeldev.ani.io/')))))"


def owned(obj):
    return f"({obj} != null && has({obj}.metadata.labels) && 'ani.io/managed-by' in {obj}.metadata.labels && {obj}.metadata.labels['ani.io/managed-by'] == 'ani-modeldev-service')"


def namespace_expression():
    labels = "namespaceObject.metadata.labels"
    return f"namespaceObject != null && has({labels}) && 'network.ani.io/tenant-id' in {labels} && {labels}['network.ani.io/tenant-id'].matches({q(UUID)}) && {labels}['network.ani.io/tenant-id'] != '00000000-0000-0000-0000-000000000000' && namespaceObject.metadata.name == 'tenant-' + {labels}['network.ani.io/tenant-id'] && 'network.ani.io/managed-by' in {labels} && {labels}['network.ani.io/managed-by'] == 'ani-network-service'"


def owner_expression():
    labels, annotations = "object.metadata.labels", "object.metadata.annotations"
    return f"has({labels}) && has({annotations}) && 'ani.io/managed-by' in {labels} && {labels}['ani.io/managed-by'] == 'ani-modeldev-service' && 'modeldev.ani.io/tenant-id' in {labels} && {labels}['modeldev.ani.io/tenant-id'] == namespaceObject.metadata.labels['network.ani.io/tenant-id'] && 'modeldev.ani.io/environment-id' in {labels} && {labels}['modeldev.ani.io/environment-id'].matches({q(UUID)}) && {labels}['modeldev.ani.io/environment-id'] != '00000000-0000-0000-0000-000000000000' && 'modeldev.ani.io/namespace-uid' in {annotations} && {annotations}['modeldev.ani.io/namespace-uid'].matches({q(UUID)}) && {annotations}['modeldev.ani.io/namespace-uid'] != '00000000-0000-0000-0000-000000000000'"


def resource_rule(groups, resources, operations=("CREATE", "UPDATE")):
    return {"apiGroups": groups, "apiVersions": ["*"], "operations": list(operations), "resources": resources, "scope": "Namespaced"}


def policy(name, rules, variables, checks, conditions=None):
    spec = {"failurePolicy": "Fail", "matchConstraints": {"matchPolicy": "Equivalent", "resourceRules": rules}, "variables": [{"name": n, "expression": e} for n, e in variables], "validations": [{"expression": e, "message": "ModelDev: " + message} for e, message in checks]}
    if conditions:
        spec["matchConditions"] = [{"name": n, "expression": e} for n, e in conditions]
    return [{"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicy", "metadata": {"name": name}, "spec": spec}, {"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicyBinding", "metadata": {"name": name + "-binding"}, "spec": {"policyName": name, "validationActions": ["Deny"]}}]


def bootstrap(p, ca_key):
    checks = [("variables.standardNamespace && variables.owner", "standard namespace and original owner identity required"), ("request.operation != 'UPDATE' || (has(oldObject.metadata.labels) && has(oldObject.metadata.annotations) && object.metadata.labels == oldObject.metadata.labels && object.metadata.annotations == oldObject.metadata.annotations)", "original managed identity metadata cannot change")]
    checks.append(("request.resource.resource != 'serviceaccounts' || (object.metadata.name in " + q(SA) + " && ((object.metadata.name == 'trainer-workload' && (!has(object.imagePullSecrets) || size(object.imagePullSecrets) == 0) && has(object.automountServiceAccountToken) && !object.automountServiceAccountToken) || (object.metadata.name != 'trainer-workload' && has(object.imagePullSecrets) && size(object.imagePullSecrets) == 1 && object.imagePullSecrets[0].name.matches('^ani-pull-[0-9a-f]{32}$'))))", "fixed SA and managed pull references required"))
    subjects = {"modeldev-runtime": p["control"], "modeldev-step": None, "modeldev-webhook-read": p["webhook"], "modeldev-kfp-api": p["kfp_api"], "modeldev-kfp-access": p["control"]}
    bindings = []
    for name, principal in subjects.items():
        ns, account = (None, "modeldev-step") if principal is None else principal.split(":")[2:]
        namespace = "request.namespace" if ns is None else q(ns)
        bindings.append(f"(object.metadata.name == {q(name)} && object.roleRef.apiGroup == 'rbac.authorization.k8s.io' && object.roleRef.kind == 'ClusterRole' && object.roleRef.name == {q(name+'-v1')} && has(object.subjects) && size(object.subjects) == 1 && object.subjects[0].kind == 'ServiceAccount' && object.subjects[0].name == {q(account)} && object.subjects[0].namespace == {namespace})")
    checks.append(("request.resource.resource != 'rolebindings' || (" + " || ".join(bindings) + ")", "five exact role references and subjects required"))
    checks.append(("request.resource.resource != 'serviceaccounts' || request.operation != 'UPDATE' || (" + optional("object", "imagePullSecrets", "[]") + " == " + optional("oldObject", "imagePullSecrets", "[]") + " && " + optional("object", "automountServiceAccountToken", "true") + " == " + optional("oldObject", "automountServiceAccountToken", "true") + ")", "original service account pull references and token policy cannot change"))
    maps = {"modeldev-step-owner": ["config.json", "modeldev-ca.pem", "s3-ca.pem"], "ani-kfp-ca": [ca_key], "kfp-launcher": ["providers", "defaultPipelineRoot"], "metadata-grpc-configmap": ["METADATA_GRPC_SERVICE_HOST", "METADATA_GRPC_SERVICE_PORT"], "modeldev-execution-environment": ["binding.json", "binding.sha256"]}
    conditions = []
    for name, keys in maps.items():
        exact = "size(object.data) == 1" if keys is None else f"size(object.data) == {len(keys)} && {q(keys)}.all(k,k in object.data && size(object.data[k]) > 0)"
        conditions.append(f"(object.metadata.name == {q(name)} && {exact})")
    checks.append(("request.resource.resource != 'configmaps' || (has(object.immutable) && object.immutable && has(object.data) && (!has(object.binaryData) || size(object.binaryData) == 0) && (" + " || ".join(conditions) + "))", "fixed immutable ConfigMaps and exact key sets required"))
    checks.append(("request.resource.resource != 'secrets' || (object.metadata.name == 'modeldev-kfp-artifacts' && object.type == 'Opaque' && has(object.data) && size(object.data) == 2 && ['access_key','secret_key'].all(k,k in object.data && size(object.data[k]) > 0) && (!has(object.stringData) || size(object.stringData) == 0) && size(object.metadata.labels) == 3 && size(object.metadata.annotations) == 1)", "dedicated two-key artifact Secret and identity-only metadata required"))
    return policy(NAMES[0], [resource_rule([""], ["serviceaccounts", "configmaps", "secrets"]), resource_rule(["rbac.authorization.k8s.io"], ["rolebindings"])], [("standardNamespace", namespace_expression()), ("owner", owner_expression())], checks, [("control", "request.userInfo.username == " + q(p["control"]))])


def configuration(p):
    def legacy(obj):
        return f"({obj} != null && has({obj}.type) && {obj}.type == 'kubernetes.io/service-account-token' && has({obj}.metadata.annotations) && 'kubernetes.io/service-account.name' in {obj}.metadata.annotations && {obj}.metadata.annotations['kubernetes.io/service-account.name'] in {q(SA)})"
    protected = "variables.current.metadata.name in ['modeldev-execution-environment','modeldev-kfp-artifacts'] || (request.resource.resource == 'serviceaccounts' && variables.current.metadata.name in " + q(SA) + ") || " + owned("object") + " || " + owned("oldObject")
    checks = [("request.resource.resource != 'secrets' || !(" + legacy("object") + " || " + legacy("oldObject") + ")", "reserved SAs never receive legacy token Secrets"), ("!variables.protected || request.userInfo.username in " + q([p["control"], p["maintenance"]]), "reserved configuration requires an exact platform writer"), ("request.userInfo.username != " + q(p["control"]) + " || request.operation != 'UPDATE' || " + owned("oldObject"), "control cannot adopt foreign configuration")]
    return policy(NAMES[1], [resource_rule([""], ["serviceaccounts", "configmaps", "secrets"], ("CREATE", "UPDATE", "DELETE")), resource_rule(["rbac.authorization.k8s.io"], ["rolebindings"], ("CREATE", "UPDATE", "DELETE"))], [("current", "request.operation == 'DELETE' ? oldObject : object"), ("protected", protected)], checks)


def podspecs(obj):
    job = f"(has({obj}.spec.template) && has({obj}.spec.template.spec) ? [{obj}.spec.template.spec] : [])"
    replicas = lambda path: f"(has({path}) ? {path}.filter(j,has(j.template) && has(j.template.spec) && has(j.template.spec.template) && has(j.template.spec.template.spec)).map(j,j.template.spec.template.spec) : [])"
    runtime = f"(has({obj}.spec.template) && has({obj}.spec.template.spec) ? {replicas(obj+'.spec.template.spec.replicatedJobs')} : [])"
    template_containers="(has(t.container) ? [t.container] : []) + (has(t.script) ? [t.script] : []) + (has(t.sidecars) ? t.sidecars : [])"
    # CEL requires homogeneous aggregate literal values. Preserve every field
    # and branch while explicitly typing this heterogeneous pseudo PodSpec map.
    workflow=f"[{obj}.spec] + (has({obj}.spec.templates) ? {obj}.spec.templates.map(t,{{'serviceAccountName':dyn(has(t.serviceAccountName) ? t.serviceAccountName : ''), 'volumes':dyn(has(t.volumes) ? t.volumes : []), 'containers':dyn({template_containers}), 'initContainers':dyn(has(t.initContainers) ? t.initContainers : [])}}) : [])"
    return f"({obj} == null || !has({obj}.spec) ? [] : request.resource.resource == 'pods' ? [{obj}.spec] : request.resource.resource == 'jobs' ? {job} : request.resource.resource == 'jobsets' ? {replicas(obj+'.spec.replicatedJobs')} : request.resource.resource == 'trainingruntimes' ? {runtime} : request.resource.resource == 'workflows' ? {workflow} : [])"


def protected_pods(name):
    volumes = "has(s.volumes) && s.volumes.exists(v,(has(v.persistentVolumeClaim) && v.persistentVolumeClaim.claimName.startsWith('ani-kfp-workspace-')) || (has(v.secret) && v.secret.secretName == 'modeldev-kfp-artifacts') || (has(v.projected) && has(v.projected.sources) && v.projected.sources.exists(x,has(x.secret) && x.secret.name == 'modeldev-kfp-artifacts')))"
    env = "(has(c.env) && c.env.exists(e,has(e.valueFrom) && has(e.valueFrom.secretKeyRef) && e.valueFrom.secretKeyRef.name == 'modeldev-kfp-artifacts')) || (has(c.envFrom) && c.envFrom.exists(e,has(e.secretRef) && e.secretRef.name == 'modeldev-kfp-artifacts'))"
    containers=" || ".join(f"(has(s.{field}) && s.{field}.exists(c,{env}))" for field in ["containers","initContainers","ephemeralContainers"])
    return f"{name}.exists(s,(has(s.serviceAccountName) && s.serviceAccountName in {q(SA)}) || ({volumes}) || {containers})"


def trusted_platform_principals(p, nodes, network_metadata_writer, network_status_writer, gc_writer, scheduler_writer, device_writer):
    """Existing platform identities explicitly trusted by the deployment owner."""
    return sorted(set(nodes + [network_metadata_writer, network_status_writer,
        gc_writer["username"], scheduler_writer["username"], device_writer["username"],
        p["trainer"], p["jobset"]]))


def workload(p, nodes, network_metadata_writer, network_status_writer, gc_finalizer_writer, scheduler_writer, device_plugin_writer):
    metadata = []
    for category in ["Labels", "Annotations"]:
        condition = "k.startsWith('ani.modeldev/') || k.startsWith('modeldev.ani.io/')" + (" || k == 'pipeline/runid'" if category == "Labels" else "")
        old, new = "variables.old" + category, "variables.new" + category
        metadata.extend([f"{old}.all(k,!({condition}) || (k in {new} && {old}[k] == {new}[k]))", f"{new}.all(k,!({condition}) || (k in {old} && {old}[k] == {new}[k]))"])
    metadata.append(optional("object", "metadata.ownerReferences", "[]") + " == (oldObject != null ? " + optional("oldObject", "metadata.ownerReferences", "[]") + " : [])")
    workflow_templates = "(request.resource.resource == 'workflows' && ((object != null && has(object.spec.templates) && object.spec.templates.exists(t,has(t.serviceAccountName) && t.serviceAccountName in " + q(SA) + ")) || (oldObject != null && has(oldObject.spec.templates) && oldObject.spec.templates.exists(t,has(t.serviceAccountName) && t.serviceAccountName in " + q(SA) + "))))"
    variables = [("newPods", podspecs("object")), ("oldPods", podspecs("oldObject")), ("protected", marker("object") + " || " + marker("oldObject") + " || " + protected_pods("variables.newPods") + " || " + protected_pods("variables.oldPods") + " || " + workflow_templates), ("standardNamespace", namespace_expression())]
    for category in ["Labels", "Annotations"]:
        field = "metadata." + category.lower()
        variables += [("new" + category, optional("object", field, "{}")), ("old" + category, "oldObject != null ? " + optional("oldObject", field, "{}") + " : {}")]
    variables += [("sameIdentity", " && ".join(metadata)), ("sameSpec", "oldObject != null && object.spec == oldObject.spec"), ("sameRetention", "oldObject == null || !has(oldObject.metadata.finalizers) || oldObject.metadata.finalizers.filter(f,f.startsWith('modeldev.ani.io/')).all(f,has(object.metadata.finalizers) && f in object.metadata.finalizers) || request.userInfo.username == " + q(p["control"])), ("suspendOnly", "oldObject != null && request.userInfo.username == " + q(p["control"]) + " && has(object.spec.suspend) && object.spec.suspend && object.spec.all(k,k == 'suspend' || (k in oldObject.spec && object.spec[k] == oldObject.spec[k])) && oldObject.spec.all(k,k == 'suspend' || (k in object.spec && object.spec[k] == oldObject.spec[k]))")]
    creators = {"workflows": [p["kfp_api"]], "trainjobs": [p["control"]], "trainingruntimes": [p["control"]], "jobsets": [p["trainer"]], "jobs": [p["jobset"]], "pods": [p["argo"], p["job_controller"]]}
    create = " || ".join(f"(request.resource.resource == {q(resource)} && request.userInfo.username in {q(users)})" for resource, users in creators.items())
    deadline = optional("object", "spec.activeDeadlineSeconds", "-1") + " == " + optional("oldObject", "spec.activeDeadlineSeconds", "-1")
    workflow_equal = "object.spec.templates == oldObject.spec.templates && " + optional("object", "spec.arguments", "{}") + " == " + optional("oldObject", "spec.arguments", "{}") + " && object.spec.entrypoint == oldObject.spec.entrypoint && object.spec.serviceAccountName == oldObject.spec.serviceAccountName && (" + deadline + " || (request.userInfo.username == " + q(p["kfp_api"]) + " && has(object.spec.activeDeadlineSeconds) && object.spec.activeDeadlineSeconds == 0))"
    updates = "request.userInfo.username in " + q(sorted(set(p.values()) - {p["persistence_agent"], p["trainer"], p["jobset"]})) + " && variables.sameIdentity && variables.sameRetention && (request.resource.resource == 'workflows' ? (" + workflow_equal + ") : request.resource.resource == 'trainjobs' ? (variables.sameSpec || variables.suspendOnly) : request.resource.resource in ['pods','trainingruntimes'] ? variables.sameSpec : variables.newPods == variables.oldPods)"
    pod_identity = "has(object.spec.serviceAccountName) && ((object.spec.serviceAccountName == 'modeldev-step' && (request.operation != 'CREATE' || request.userInfo.username == " + q(p["argo"]) + ")) || (object.spec.serviceAccountName == 'trainer-workload' && (request.operation != 'CREATE' || request.userInfo.username == " + q(p["job_controller"]) + ")))"
    checks = [("!variables.protected || variables.standardNamespace", "protected workload requires the standard owned namespace"), ("!variables.protected || request.operation != 'CREATE' || (" + create + ")", "only exact original controllers create protected resources"), ("!variables.protected || request.resource.resource != 'pods' || (" + pod_identity + ")", "reserved Secret, PVC or SA cannot be borrowed"), ("!variables.protected || request.operation != 'UPDATE' || (" + updates + ")", "original templates, four parameters, owner identity and retained Pod spec cannot change")]
    rules = [resource_rule(["argoproj.io"], ["workflows", "workflows/status"]), resource_rule(["trainer.kubeflow.org"], ["trainjobs", "trainjobs/status", "trainingruntimes"]), resource_rule(["jobset.x-k8s.io"], ["jobsets", "jobsets/status"]), resource_rule(["batch"], ["jobs", "jobs/status"]), resource_rule([""], ["pods", "pods/status", "pods/ephemeralcontainers"])]
    trusted = trusted_platform_principals(p, nodes, network_metadata_writer, network_status_writer, gc_finalizer_writer, scheduler_writer, device_plugin_writer)
    persistence_update = "request.userInfo.username == " + q(p["persistence_agent"]) + " && request.operation == 'UPDATE' && ((has(request.subResource) && request.subResource == 'status') || (oldObject != null && object != null && object.spec == oldObject.spec))"
    conditions = [("platform-controller-trust", "!(request.userInfo.username in " + q(trusted) + " || (" + persistence_update + "))")]
    return policy(NAMES[2], rules, variables, checks, conditions)


def rbac(p):
    def rule(groups, resources, verbs, names=None):
        result = {"apiGroups": groups, "resources": resources, "verbs": verbs}
        if names:
            result["resourceNames"] = names
        return result
    def role(name, rules):
        return {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": {"name": name}, "rules": rules}
    def binding(name, role_name, principal):
        ns, account = principal.split(":")[2:]
        return {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": {"name": name}, "roleRef": {"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": role_name}, "subjects": [{"kind": "ServiceAccount", "name": account, "namespace": ns}]}
    runtime = [rule([""], ["pods"], ["get", "list", "patch", "delete"]), rule([""], ["pods/log"], ["get"]), rule([""], ["persistentvolumeclaims"], ["get", "list", "delete"]), rule([""], ["serviceaccounts"], ["get"], SA), rule([""], ["events"], ["get", "list"]), rule(["argoproj.io"], ["workflows"], ["get", "list", "delete"]), rule(["trainer.kubeflow.org"], ["trainjobs"], ["create", "get", "list", "patch", "delete"]), rule(["trainer.kubeflow.org"], ["trainingruntimes"], ["create", "get", "delete"]), rule(["jobset.x-k8s.io"], ["jobsets"], ["get", "list", "delete"]), rule(["batch"], ["jobs"], ["get", "list", "delete"]), rule(["scheduling.volcano.sh"], ["podgroups"], ["get", "list", "delete"])]
    step = [rule([""], ["pods"], ["get"]), rule([""], ["persistentvolumeclaims"], ["create", "get"]), rule(["argoproj.io"], ["workflows"], ["get", "list", "patch", "update"]), rule(["argoproj.io"], ["workflowtaskresults"], ["create", "patch"]), rule([""], ["configmaps"], ["get"], ["kfp-launcher", "metadata-grpc-configmap", "ani-kfp-ca"]), rule([""], ["secrets"], ["get"], ["modeldev-kfp-artifacts"])]
    webhook = [rule(["argoproj.io"], ["workflows"], ["get"]), rule(["batch"], ["jobs"], ["get"]), rule(["jobset.x-k8s.io"], ["jobsets"], ["get"]), rule(["trainer.kubeflow.org"], ["trainjobs", "trainingruntimes"], ["get"]), rule([""], ["serviceaccounts"], ["get"], SA), rule([""], ["configmaps"], ["get"], ["modeldev-execution-environment"])]
    logical = [rule(["pipelines.kubeflow.org"], ["experiments"], ["create", "get", "list"]), rule(["pipelines.kubeflow.org"], ["runs"], ["create", "get", "list", "terminate", "delete"])]
    rows = [role("modeldev-runtime-v1", runtime), role("modeldev-step-v1", step), role("modeldev-webhook-read-v1", webhook), role("modeldev-kfp-api-v1", [rule(["argoproj.io"], ["workflows"], ["create", "patch", "delete"])]), role("modeldev-kfp-access-v1", logical)]
    control = [rule([""], ["namespaces"], ["get"]), rule([""], ["serviceaccounts", "configmaps", "secrets"], ["create"]), rule(["rbac.authorization.k8s.io"], ["rolebindings"], ["create"]), rule([""], ["serviceaccounts"], ["get", "patch"], SA), rule([""], ["configmaps"], ["get", "patch"], CM), rule([""], ["secrets"], ["get", "patch"], ["modeldev-kfp-artifacts"]), rule(["rbac.authorization.k8s.io"], ["rolebindings"], ["get", "patch"], RB), rule(["rbac.authorization.k8s.io"], ["clusterroles"], ["get", "bind"], [name + "-v1" for name in RB]), rule(["admissionregistration.k8s.io"], ["validatingadmissionpolicies"], ["get"], NAMES), rule(["admissionregistration.k8s.io"], ["validatingadmissionpolicybindings"], ["get"], [name + "-binding" for name in NAMES]), rule(["admissionregistration.k8s.io"], ["mutatingwebhookconfigurations"], ["get"], ["ani-modeldev-webhook"])]
    for name, rules, principal in [("modeldev-environment-control-v1", control, p["control"]), ("modeldev-webhook-namespace-v1", [rule([""], ["namespaces"], ["get"])], p["webhook"])]:
        rows += [role(name, rules), binding(name.removesuffix("-v1"), name, principal)]
    rows += [role("modeldev-kfp-platform-read-v1",[rule(["pipelines.kubeflow.org"],["pipelines"],["get"],["modeldev-mlp-cpu","modeldev-mlp-gpu","modeldev-sft-lora-cpu","modeldev-sft-lora-gpu"])]), {"apiVersion":"rbac.authorization.k8s.io/v1","kind":"RoleBinding","metadata":{"name":"modeldev-kfp-platform-read","namespace":"ani-system"},"roleRef":{"apiGroup":"rbac.authorization.k8s.io","kind":"ClusterRole","name":"modeldev-kfp-platform-read-v1"},"subjects":[{"kind":"ServiceAccount","name":"modeldev-control","namespace":"ani-system"}]},role("modeldev-token-review-v1",[rule(["authentication.k8s.io"],["tokenreviews"],["create"])]),binding("modeldev-token-review","modeldev-token-review-v1",p["control"])]
    return rows


def render(config):
    if set(config) != {"principals", "node_principals", "kfp_ca_key", "network_metadata_writer", "network_status_writer", "gc_finalizer_writer", "scheduler_writer", "device_plugin_writer"}:
        raise ValueError("verified exact principals and node identities required")
    p, nodes = config["principals"], config["node_principals"]
    if not isinstance(config["kfp_ca_key"],str) or not re.fullmatch(r"[A-Za-z0-9._-]+",config["kfp_ca_key"]):
        raise ValueError("actual CABUNDLE_KEY_NAME required")
    keys = {"control", "webhook", "kfp_api", "argo", "trainer", "jobset", "job_controller", "persistence_agent", "maintenance"}
    if set(p) != keys or any(not isinstance(v, str) or not v or "*" in v for v in p.values()):
        raise ValueError("missing exact platform principal")
    if p["control"] != "system:serviceaccount:ani-system:modeldev-control" or p["webhook"] != "system:serviceaccount:ani-system:ani-modeldev-webhook":
        raise ValueError("fixed ModelDev platform identities required")
    if any(not re.fullmatch(r"system:serviceaccount:[a-z0-9.-]+:[a-z0-9.-]+", value) for key, value in p.items() if key != "maintenance"):
        raise ValueError("verified controller ServiceAccount principals required")
    network_writer = config["network_metadata_writer"]
    if not isinstance(network_writer, str) or not re.fullmatch(r"system:serviceaccount:[a-z0-9.-]+:[a-z0-9.-]+", network_writer) or network_writer in p.values():
        raise ValueError("separate verified network metadata ServiceAccount required")
    status_writer = config["network_status_writer"]
    if not isinstance(status_writer, str) or not re.fullmatch(r"system:serviceaccount:[a-z0-9.-]+:[a-z0-9.-]+", status_writer) or status_writer in p.values() or status_writer == network_writer:
        raise ValueError("separate verified network status ServiceAccount required")
    gc_writer = config["gc_finalizer_writer"]
    if not isinstance(gc_writer, dict) or set(gc_writer) != {"username", "uid"} or gc_writer["username"] != "system:serviceaccount:kube-system:generic-garbage-collector" or gc_writer["username"] in p.values() or gc_writer["username"] in (network_writer, status_writer) or not isinstance(gc_writer["uid"], str) or not re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", gc_writer["uid"]) or gc_writer["uid"] == "00000000-0000-0000-0000-000000000000":
        raise ValueError("separate verified garbage collector ServiceAccount username and UID required")
    scheduler = config["scheduler_writer"]
    if not isinstance(scheduler, dict) or set(scheduler) != {"username", "uid"} or scheduler["username"] != "system:serviceaccount:volcano-system:ani-volcano-scheduler" or scheduler["username"] in p.values() or scheduler["username"] in (network_writer, status_writer, gc_writer["username"]) or not isinstance(scheduler["uid"], str) or not re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", scheduler["uid"]) or scheduler["uid"] == "00000000-0000-0000-0000-000000000000":
        raise ValueError("separate verified Volcano scheduler ServiceAccount username and UID required")
    device = config["device_plugin_writer"]
    if not isinstance(device, dict) or set(device) != {"username", "uid"} or device["username"] != "system:serviceaccount:kube-system:volcano-device-plugin" or device["username"] in p.values() or device["username"] in (network_writer, status_writer, gc_writer["username"], scheduler["username"]) or not isinstance(device["uid"], str) or not re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", device["uid"]) or device["uid"] == "00000000-0000-0000-0000-000000000000":
        raise ValueError("separate verified device plugin ServiceAccount username and UID required")
    if not isinstance(nodes, list) or not nodes or len(nodes) > 64 or len(set(nodes)) != len(nodes) or any(not re.fullmatch(r"system:node:[a-z0-9.-]+", n) for n in nodes):
        raise ValueError("exact verified node usernames required")
    return {"apiVersion": "v1", "kind": "List", "items": bootstrap(p,config["kfp_ca_key"]) + configuration(p) + workload(p, nodes, network_writer, status_writer, gc_writer, scheduler, device) + rbac(p)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    document = render(json.loads(args.config.read_text()))
    args.output.mkdir(parents=True, exist_ok=False)
    (args.output / "bootstrap.json").write_text(json.dumps(document, indent=2) + "\n")


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Render a namespace-UID-bound native TrainJob safety policy.

This coarse policy cannot replace the production stored-snapshot Runtime/Pod
verifier. It contains no execution approval list or mutable workload defaults.
"""
import argparse
import json
from pathlib import Path
import re
import uuid

NAME = "modeldev-v21-trainjob-01a15f47"
NAMESPACE_UID_LABEL = "modeldev.ani.io/namespace-uid"
CREATOR = "system:serviceaccount:ani-system:modeldev-control"
TRAINER = "system:serviceaccount:kubeflow-system:kubeflow-trainer-controller-manager"
GC = "system:serviceaccount:kube-system:generic-garbage-collector"
PREFIX = "modeldev.ani.io/"
UUID_KEYS = ["tenant-id", "execution-id", "create-operation-id", "workspace-uid", "runtime-uid"]
HASH_KEYS = ["execution-spec-sha256", "training-request-sha256", "runtime-spec-sha256"]
TIME_KEYS = ["training-admitted-at", "training-deadline-at"]
GPU_KEYS = ["volcano.sh/vgpu-number", "volcano.sh/vgpu-memory", "volcano.sh/vgpu-cores"]


def render(config):
    if set(config) != {"namespace", "namespace_uid", "cpu_image", "gpu_image"}:
        raise ValueError("explicit namespace UID and installed CPU/GPU image digests required")
    namespace, uid = config["namespace"], config["namespace_uid"]
    if not re.fullmatch(r"[a-z0-9]([-a-z0-9]*[a-z0-9])?", namespace) or len(namespace) > 63 or str(uuid.UUID(uid)) != uid:
        raise ValueError("canonical namespace name/UID required")
    if any(not re.fullmatch(r"[^\s@]+@sha256:[0-9a-f]{64}", config[k]) for k in ["cpu_image", "gpu_image"]):
        raise ValueError("installed immutable image digests required")
    quote = lambda value: json.dumps(value, separators=(",", ":"))
    no_overrides = "!has(object.spec.initializer) && !has(object.spec.labels) && !has(object.spec.annotations) && !has(object.spec.podTemplateOverrides)"
    meta_equal = "oldObject != null && (has(object.metadata.annotations) ? object.metadata.annotations : {}) == (has(oldObject.metadata.annotations) ? oldObject.metadata.annotations : {}) && (has(object.metadata.labels) ? object.metadata.labels : {}) == (has(oldObject.metadata.labels) ? oldObject.metadata.labels : {}) && (has(object.metadata.ownerReferences) ? object.metadata.ownerReferences : []) == (has(oldObject.metadata.ownerReferences) ? oldObject.metadata.ownerReferences : [])"
    variables = [
        {"name": "creator", "expression": "request.userInfo.username == " + quote(CREATOR)},
        {"name": "sameMetadata", "expression": meta_equal},
        {"name": "sameSpec", "expression": "oldObject != null && object.spec == oldObject.spec"},
        {"name": "suspendOnly", "expression": "oldObject != null && variables.creator && " + no_overrides + " && has(object.spec.suspend) && object.spec.suspend && object.spec.runtimeRef == oldObject.spec.runtimeRef && object.spec.managedBy == oldObject.spec.managedBy && object.spec.trainer == oldObject.spec.trainer"},
        {"name": "gcOrphanRemoval", "expression": "request.userInfo.username == " + quote(GC) + " && variables.sameSpec && (!has(request.subResource) || request.subResource == '') && has(oldObject.metadata.deletionTimestamp) && has(object.metadata.deletionTimestamp) && object.metadata.deletionTimestamp == oldObject.metadata.deletionTimestamp && has(oldObject.metadata.finalizers) && oldObject.metadata.finalizers.filter(f, f == 'orphan').size() == 1 && (has(object.metadata.finalizers) ? object.metadata.finalizers : []) == oldObject.metadata.finalizers.filter(f, f != 'orphan') && (has(object.status) ? object.status : {}) == (has(oldObject.status) ? oldObject.status : {})"},
        {"name": "maintenance", "expression": "request.operation == 'UPDATE' && variables.sameMetadata && ((variables.creator && (variables.sameSpec || variables.suspendOnly)) || (request.userInfo.username == " + quote(TRAINER) + " && variables.sameSpec) || variables.gcOrphanRemoval)"},
        {"name": "annotations", "expression": "has(object.metadata.annotations) ? object.metadata.annotations : {}"},
        {"name": "requests", "expression": "has(object.spec.trainer) && has(object.spec.trainer.resourcesPerNode) && has(object.spec.trainer.resourcesPerNode.requests) ? object.spec.trainer.resourcesPerNode.requests : {}"},
        {"name": "limits", "expression": "has(object.spec.trainer) && has(object.spec.trainer.resourcesPerNode) && has(object.spec.trainer.resourcesPerNode.limits) ? object.spec.trainer.resourcesPerNode.limits : {}"},
    ]
    # Kubernetes' namespaceObject CEL type deliberately does not expose UID.
    # The controlled label is a scope marker, not a physical UID proof. The
    # production Runtime/Pod verifier independently GETs the actual namespace.
    validations = [{"expression": "namespaceObject != null && namespaceObject.metadata.name == " + quote(namespace) + " && has(namespaceObject.metadata.labels) && " + quote(NAMESPACE_UID_LABEL) + " in namespaceObject.metadata.labels && namespaceObject.metadata.labels[" + quote(NAMESPACE_UID_LABEL) + "] == " + quote(uid), "message": "ModelDev v2 namespace identity scope marker changed"}]

    def require(expression, message):
        validations.append({"expression": "variables.maintenance || (" + expression + ")", "message": "ModelDev v2: " + message})

    require("request.operation == 'CREATE' && variables.creator", "only the formal owner creates TrainJobs; updates preserve original spec or stop")
    require(no_overrides, "initializer, labels, annotations and Pod overrides are prohibited")
    require("has(object.spec.managedBy) && object.spec.managedBy == 'trainer.kubeflow.org/trainjob-controller' && has(object.spec.suspend) && !object.spec.suspend", "installed controller and initially unsuspended workload required")
    require("has(object.spec.runtimeRef) && has(object.spec.runtimeRef.kind) && object.spec.runtimeRef.kind == 'TrainingRuntime' && object.spec.runtimeRef.apiGroup == 'trainer.kubeflow.org' && object.spec.runtimeRef.name.matches('^mdrt-[0-9a-f]{48}$')", "use the execution's namespaced TrainingRuntime")
    require("has(object.spec.trainer) && has(object.spec.trainer.numNodes) && object.spec.trainer.numNodes == 1 && has(object.spec.trainer.numProcPerNode) && type(object.spec.trainer.numProcPerNode) == int && object.spec.trainer.numProcPerNode == 1", "one node and one integer process required")
    require("has(object.spec.trainer.image) && object.spec.trainer.image in " + quote([config["cpu_image"], config["gpu_image"]]), "use an installed immutable CPU/GPU training image")
    require("has(object.spec.trainer.command) && size(object.spec.trainer.command) > 0 && has(object.spec.trainer.args) && !has(object.spec.trainer.env) && has(object.spec.trainer.resourcesPerNode) && !has(object.spec.trainer.resourcesPerNode.claims)", "complete command/args and resources without environment or credential claims required")
    keys = [PREFIX + key for key in UUID_KEYS + HASH_KEYS + TIME_KEYS]
    require(quote(keys) + ".all(k, k in variables.annotations)", "complete frozen execution annotations required")
    require(quote([PREFIX + key for key in UUID_KEYS]) + ".all(k, variables.annotations[k].matches('^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$') && variables.annotations[k] != '00000000-0000-0000-0000-000000000000')", "canonical nonzero original UUIDs required")
    require(quote([PREFIX + key for key in HASH_KEYS]) + ".all(k, variables.annotations[k].matches('^[0-9a-f]{64}$'))", "complete lowercase execution/request/runtime hashes required")
    require("object.metadata.name == 'md-' + variables.annotations['modeldev.ani.io/execution-id'] && has(namespaceObject.metadata.labels) && 'ani.io/resource-tenant-id' in namespaceObject.metadata.labels && variables.annotations['modeldev.ani.io/tenant-id'] == namespaceObject.metadata.labels['ani.io/resource-tenant-id']", "execution name and namespace tenant identity must agree")
    require("timestamp(variables.annotations['modeldev.ani.io/training-admitted-at']) < timestamp(variables.annotations['modeldev.ani.io/training-deadline-at'])", "original training time interval must be valid")
    require("(!has(object.metadata.labels) || size(object.metadata.labels) == 0) && (!has(object.metadata.ownerReferences) || size(object.metadata.ownerReferences) == 0)", "new workloads cannot acquire control labels or foreign ownership")
    allowed = ["cpu", "memory"] + GPU_KEYS
    require("variables.requests == variables.limits && 'cpu' in variables.requests && 'memory' in variables.requests && variables.requests.all(k, k in " + quote(allowed) + " && quantity(string(variables.requests[k])).compareTo(quantity('0')) > 0)", "positive complete requests must equal limits; only CPU/memory and installed Volcano keys allowed")
    require("size(variables.requests) == 2 || (size(variables.requests) == 5 && " + quote(GPU_KEYS) + ".all(k, k in variables.requests) && quantity(string(variables.requests['volcano.sh/vgpu-number'])).compareTo(quantity('1')) == 0 && quantity(string(variables.requests['volcano.sh/vgpu-cores'])).compareTo(quantity('100')) <= 0)", "GPU requests must contain the complete one-device Volcano resource vector")
    policy = {"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicy", "metadata": {"name": NAME}, "spec": {"failurePolicy": "Fail", "matchConstraints": {"matchPolicy": "Exact", "namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": namespace}}, "resourceRules": [{"apiGroups": ["trainer.kubeflow.org"], "apiVersions": ["v1alpha1"], "operations": ["CREATE", "UPDATE"], "resources": ["trainjobs", "trainjobs/status"], "scope": "Namespaced"}]}, "variables": variables, "validations": validations}}
    binding = {"apiVersion": policy["apiVersion"], "kind": "ValidatingAdmissionPolicyBinding", "metadata": {"name": NAME}, "spec": {"policyName": NAME, "validationActions": ["Deny"], "matchResources": {"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": namespace}}}}}
    return policy, binding


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    policy, binding = render(json.loads(args.config.read_text()))
    args.output.mkdir(parents=True, exist_ok=False)
    for name, document in [("policy.json", policy), ("binding.json", binding)]:
        (args.output / name).write_text(json.dumps(document, indent=2) + "\n")


if __name__ == "__main__":
    main()

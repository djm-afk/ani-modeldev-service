#!/usr/bin/env python3
"""Render immutable shared materials and exact deployment patches, offline.

Native policies/bootstrap are emitted by their owner renderer and installed
before granting ModelDev bootstrap create/bind. This tool never calls a cluster.
"""
import argparse
import json
from pathlib import Path
import re
from bundle import PROFILE_CONTRACT, image_authority
from publisher import PROFILES, canonical, read_json, sha

MOUNT = "/etc/modeldev-materials"
PLATFORM_MOUNT = "/etc/modeldev-platform"
ARTIFACT_MOUNT = "/var/run/modeldev-kfp-artifact-credentials"


def validate_bundle(directory):
    manifest_raw = (directory / "manifest.json").read_bytes()
    manifest = read_json(directory / "manifest.json")
    if manifest.get("schema") != "ani.modeldev.material-bundle.v1" or len(manifest.get("profiles", [])) != 4 or {p["id"] for p in manifest["profiles"]} != PROFILES or canonical(manifest) != manifest_raw:
        raise ValueError("canonical fixed material bundle required")
    expected = {"ir/" + name + ".yaml" for name in PROFILES} | {".".join(value) + ".json" for value in PROFILE_CONTRACT.values()}
    if set(manifest["files"]) != expected:
        raise ValueError("exact four IR and four canonical material files required")
    files = {}
    for filename, digest in manifest["files"].items():
        source = directory / filename
        if source.is_symlink() or not source.is_file():
            raise ValueError("regular immutable bundle file required")
        raw = source.read_bytes()
        if sha(raw) != digest:
            raise ValueError("immutable bundle byte digest mismatch")
        files[filename] = raw.decode()
    files["manifest.json"] = manifest_raw.decode()
    return manifest, sha(manifest_raw), files


def render(directory, platform, service_image, copy_image, artifact_source_secret, service_container, webhook_container, service_deployment="ani-modeldev", webhook_deployment="ani-modeldev-webhook"):
    def no_secret_values(value):
        if isinstance(value, dict):
            if set(value) & {"access_key", "secret_key", "password", "token", "dsn", "kubeconfig"}:
                raise ValueError("secret values must remain referenced files")
            for child in value.values():
                no_secret_values(child)
        elif isinstance(value, list):
            for child in value:
                no_secret_values(child)
    no_secret_values(platform)
    manifest, digest, files = validate_bundle(directory)
    if platform.get("material_bundle_digest") != digest or len(platform.get("profiles", [])) != 4 or {p["id"] for p in platform["profiles"]} != PROFILES:
        raise ValueError("platform must pin the same immutable bundle")
    public_profiles = {p["id"]: p for p in manifest["profiles"]}
    for profile in platform["profiles"]:
        public = public_profiles[profile["id"]]
        for key, expected_key in (("pipeline_id", "pipeline_id"), ("version_id", "version_id"), ("ir_sha256", "ir_sha256"), ("material_sha256", "material_sha256")):
            if profile[key] != public[expected_key]:
                raise ValueError("platform fixed profile differs from bundle")
    sources = sorted(platform.get("ir_sources", []), key=lambda p: p["profile_id"])
    if sources != [{"profile_id": p["id"], "file": p["ir_file"], "sha256": p["ir_sha256"]} for p in sorted(manifest["profiles"], key=lambda p: p["id"])]:
        raise ValueError("platform IR byte sources must refer to the same mounted bundle")
    if platform.get("kfp_token_file") != "/var/run/modeldev-kfp/token" or platform.get("artifact_access_key_file") != ARTIFACT_MOUNT + "/access_key" or platform.get("artifact_secret_key_file") != ARTIFACT_MOUNT + "/secret_key":
        raise ValueError("fixed projected-token/artifact file references required")
    anonymous = {p["reference"] for p in manifest["anonymous_images"]}
    for reference in (service_image, copy_image):
        image_authority(reference, manifest["registry_authority"], manifest["platform_project"], anonymous)
    if copy_image != manifest["support_images"]["material_copy"]:
        raise ValueError("material copy image must be bundle-pinned")
    if not re.fullmatch(r"[a-z0-9]([-a-z0-9]*[a-z0-9])?", artifact_source_secret) or any(not re.fullmatch(r"[a-z0-9]([-a-z0-9]*[a-z0-9])?", name) for name in (service_container, webhook_container, service_deployment, webhook_deployment)):
        raise ValueError("observed source Secret and container names required")
    config_name = "modeldev-materials-" + digest[:12]
    source_data = {name.replace("/", "__"): content for name, content in files.items()}
    config_map = {"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": config_name, "namespace": "ani-system", "labels": {"ani.io/managed-by": "ani-modeldev-service"}}, "immutable": True, "data": source_data}
    if len(canonical(config_map))>1<<20:
        raise ValueError("shared immutable material ConfigMap exceeds Kubernetes size limit")
    platform_raw = canonical(platform)
    platform_name = "modeldev-platform-" + sha(platform_raw)[:12]
    platform_map = {"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": platform_name, "namespace": "ani-system", "labels": {"ani.io/managed-by": "ani-modeldev-service"}}, "immutable": True, "data": {"platform.json": platform_raw.decode()}}
    initializer = {"name": "install-modeldev-materials", "image": copy_image, "imagePullPolicy": "IfNotPresent", "command": ["/usr/local/bin/ani-modeldev-webhook", "--copy-materials-from", "/source", "--copy-materials-to", "/materials", "--copy-materials-manifest-sha256", digest], "securityContext": {"runAsNonRoot": True, "runAsUser": 10001, "runAsGroup": 10001, "readOnlyRootFilesystem": True, "allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}}, "resources": {"requests": {"cpu": "50m", "memory": "32Mi"}, "limits": {"cpu": "500m", "memory": "128Mi"}}, "volumeMounts": [{"name": "modeldev-material-source", "mountPath": "/source", "readOnly": True}, {"name": "modeldev-materials", "mountPath": "/materials"}]}
    volumes = [{"name": "modeldev-material-source", "configMap": {"name": config_name, "defaultMode": 292}}, {"name": "modeldev-materials", "emptyDir": {}}]
    def deployment_patch(name, container, service):
        pod = {"securityContext": {"fsGroup": 10001}, "initContainers": [initializer], "volumes": list(volumes), "containers": [{"name": container, "volumeMounts": [{"name": "modeldev-materials", "mountPath": MOUNT, "readOnly": True}]}]}
        if service:
            pod.update(serviceAccountName="modeldev-control", automountServiceAccountToken=False, enableServiceLinks=False)
            pod["containers"][0]["image"] = service_image
            pod["initContainers"] += [{**initializer, "name": "install-modeldev-platform", "command": ["/usr/local/bin/ani-modeldev-webhook", "--copy-platform-from", "/source/platform.json", "--copy-platform-to", "/platform/platform.json", "--copy-platform-sha256", sha(platform_raw)], "volumeMounts": [{"name": "modeldev-platform-source", "mountPath": "/source", "readOnly": True}, {"name": "modeldev-platform-config", "mountPath": "/platform"}]}]
            pod["volumes"] += [{"name": "modeldev-kfp-token", "projected": {"defaultMode": 288, "sources": [{"serviceAccountToken": {"audience": "pipelines.kubeflow.org", "expirationSeconds": 3600, "path": "token"}}]}}, {"name": "modeldev-api-token", "projected": {"defaultMode": 288, "sources": [{"serviceAccountToken": {"audience": "https://kubernetes.default.svc.cluster.local", "expirationSeconds": 3600, "path": "token"}}, {"configMap": {"name": "kube-root-ca.crt", "items": [{"key": "ca.crt", "path": "ca.crt"}]}}, {"downwardAPI": {"items": [{"path": "namespace", "fieldRef": {"apiVersion": "v1", "fieldPath": "metadata.namespace"}}]}}]}}, {"name": "modeldev-platform-source", "configMap": {"name": platform_name, "defaultMode": 292}}, {"name": "modeldev-platform-config", "emptyDir": {}}, {"name": "modeldev-artifact-source", "secret": {"secretName": artifact_source_secret, "defaultMode": 288, "items": [{"key": "access_key", "path": "access_key"}, {"key": "secret_key", "path": "secret_key"}]}}]
            pod["containers"][0]["volumeMounts"] += [{"name": "modeldev-kfp-token", "mountPath": "/var/run/modeldev-kfp", "readOnly": True}, {"name": "modeldev-api-token", "mountPath": "/var/run/secrets/kubernetes.io/serviceaccount", "readOnly": True}, {"name": "modeldev-platform-config", "mountPath": PLATFORM_MOUNT, "readOnly": True}, {"name": "modeldev-artifact-source", "mountPath": ARTIFACT_MOUNT, "readOnly": True}]
        return {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": name, "namespace": "ani-system"}, "spec": {"template": {"metadata": {"annotations": {"app.kubernetes.io/material-bundle-sha256": digest}}, "spec": pod}}}
    return {"materials.configmap.json": config_map, "platform.configmap.json": platform_map, "service.patch.json": deployment_patch(service_deployment, service_container, True), "webhook-materials.patch.json": deployment_patch(webhook_deployment, webhook_container, False), "runtime-config-fragment.json": {"runtime": {"tenant_environment_file": PLATFORM_MOUNT + "/platform.json", "tenant_environment_sha256": sha(platform_raw)}, "command": {"admission_resolution": {"catalogue_directory": MOUNT}}}, "install-order.json": {"material_bundle_sha256": digest, "prepare_only": True, "ordered_steps": ["verify-complete-designated-cluster-identity", "install-native-policy-and-fixed-bootstrap-from-policy-owner", "verify-native-positive-negative-server-dry-run", "grant-control-bootstrap-create-bind", "publish-four-ani-system-definitions", "install-immutable-shared-materials", "finish-old-execution-close-cleanup-and-original-release-ack", "switch-one-shared-service-and-webhook", "explicitly-ensure-A-then-B", "run-real-A-B-acceptance"], "live_status": "NOT_RUN"}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--platform", type=Path, required=True)
    parser.add_argument("--service-image", required=True)
    parser.add_argument("--copy-image", required=True)
    parser.add_argument("--artifact-source-secret", required=True)
    parser.add_argument("--service-container", required=True)
    parser.add_argument("--webhook-container", required=True)
    parser.add_argument("--service-deployment", default="ani-modeldev")
    parser.add_argument("--webhook-deployment", default="ani-modeldev-webhook")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    objects = render(args.bundle, read_json(args.platform), args.service_image, args.copy_image, args.artifact_source_secret, args.service_container, args.webhook_container, args.service_deployment, args.webhook_deployment)
    args.output.mkdir(parents=True, exist_ok=False)
    for filename, value in objects.items():
        with (args.output / filename).open("xb") as output:
            output.write(canonical(value))


if __name__ == "__main__":
    main()

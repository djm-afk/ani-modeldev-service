#!/usr/bin/env python3
"""Offline formal service renderer; registration is a separate output file.

The production Go binary validates policy and canonical Releases. This script
owns deployment shape only, has no admission rules and never contacts K8s.
"""
import argparse
import hashlib
import ipaddress
import json
from pathlib import Path
import re
import subprocess

NAME = "ani-modeldev-webhook"


def render(config, namespace, image, issuer, issuer_kind, api_servers, kubernetes_service, api_server_port, kubernetes_service_port):
    if not re.fullmatch(r"[a-z0-9]([-a-z0-9]*[a-z0-9])?", namespace) or not re.fullmatch(r"[^\s@]+@sha256:[0-9a-f]{64}", image):
        raise ValueError("explicit namespace and immutable webhook image required")
    if issuer_kind not in {"Issuer", "ClusterIssuer"} or not re.fullmatch(r"[a-z0-9]([-a-z0-9.]*[a-z0-9])?", issuer):
        raise ValueError("explicit cert-manager issuer required")
    cidrs = [str(ipaddress.ip_network(value, strict=True)) for value in api_servers]
    if not cidrs or any(ipaddress.ip_network(value).prefixlen != ipaddress.ip_network(value).max_prefixlen for value in cidrs):
        raise ValueError("explicit API server individual addresses required")
    service = ipaddress.ip_network(kubernetes_service, strict=True)
    if service.prefixlen != service.max_prefixlen:
        raise ValueError("explicit Kubernetes Service individual address required")
    if any(type(port) is not int or not 1 <= port <= 65535 for port in (api_server_port, kubernetes_service_port)):
        raise ValueError("explicit observed API and Kubernetes Service ports required")
    if any(not binding.get("driver") for binding in config["bindings"]):
        raise ValueError("production KFP scope requires frozen backend driver bindings")
    config = json.loads(json.dumps(config))
    files = {}
    for profile in config["profiles"]:
        raw = Path(profile["material_file"]).read_bytes()
        if hashlib.sha256(raw).hexdigest() != profile["material_sha256"]:
            raise ValueError("validated Release bytes changed during assembly")
        filename = profile["id"] + ".json"
        files[filename] = raw.decode()
        profile["material_file"] = "/etc/modeldev-webhook/materials/" + filename
    config.update(listen_address=":8443", certificate_file="/tls/tls.crt", private_key_file="/tls/tls.key")
    data = {"config.json": json.dumps(config, separators=(",", ":")), **files}
    checksum = hashlib.sha256(json.dumps(data, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    config_name = NAME + "-" + checksum[:12]
    labels = {"app.kubernetes.io/name": NAME, "app.kubernetes.io/part-of": "ani-modeldev"}
    def obj(api, kind, name=NAME, ns=namespace, **fields):
        meta = {"name": name, "labels": labels.copy()}
        if ns:
            meta["namespace"] = ns
        return {"apiVersion": api, "kind": kind, "metadata": meta, **fields}
    subject = [{"kind": "ServiceAccount", "name": NAME, "namespace": namespace}]
    rows = [obj("v1", "ServiceAccount"), obj("v1", "ConfigMap", name=config_name, immutable=True, data=data),
            obj("rbac.authorization.k8s.io/v1", "ClusterRole", ns=None, rules=[{"apiGroups": [""], "resources": ["namespaces"], "resourceNames": sorted(b["namespace"] for b in config["bindings"]), "verbs": ["get"]}]),
            obj("rbac.authorization.k8s.io/v1", "ClusterRoleBinding", ns=None, roleRef={"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": NAME}, subjects=subject)]
    for binding in config["bindings"]:
        rows += [obj("rbac.authorization.k8s.io/v1", "Role", ns=binding["namespace"], rules=[{"apiGroups": ["argoproj.io"], "resources": ["workflows"], "verbs": ["get"]}]),
                 obj("rbac.authorization.k8s.io/v1", "RoleBinding", ns=binding["namespace"], roleRef={"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": NAME}, subjects=subject)]
        if binding.get("training"):
            rows[-2]["rules"] += [
                {"apiGroups": ["batch"], "resources": ["jobs"], "verbs": ["get"]},
                {"apiGroups": ["jobset.x-k8s.io"], "resources": ["jobsets"], "verbs": ["get"]},
                {"apiGroups": ["trainer.kubeflow.org"], "resources": ["trainjobs", "trainingruntimes"], "verbs": ["get"]},
            ]
    rows += [obj("cert-manager.io/v1", "Certificate", name=NAME+"-serving", spec={"secretName": NAME+"-tls", "issuerRef": {"name": issuer, "kind": issuer_kind, "group": "cert-manager.io"}, "dnsNames": [NAME+"."+namespace+".svc", NAME+"."+namespace+".svc.cluster.local"], "duration": "2160h", "renewBefore": "360h", "privateKey": {"rotationPolicy": "Always", "algorithm": "ECDSA", "size": 256}, "usages": ["digital signature", "server auth"]}),
             obj("v1", "Service", spec={"selector": labels.copy(), "ports": [{"name": "https", "port": 443, "targetPort": 8443}]}),
             obj("policy/v1", "PodDisruptionBudget", spec={"minAvailable": 1, "selector": {"matchLabels": labels.copy()}})]
    container = {"name": "webhook", "image": image, "imagePullPolicy": "IfNotPresent", "ports": [{"name": "https", "containerPort": 8443}],
                 "securityContext": {"runAsNonRoot": True, "runAsUser": 10001, "runAsGroup": 10001, "readOnlyRootFilesystem": True, "allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}},
                 "resources": {"requests": {"cpu": "100m", "memory": "128Mi"}, "limits": {"cpu": "1", "memory": "512Mi"}},
                 "volumeMounts": [{"name": "config", "mountPath": "/etc/modeldev-webhook", "readOnly": True}, {"name": "tls", "mountPath": "/tls", "readOnly": True}],
                 "readinessProbe": {"httpGet": {"scheme": "HTTPS", "path": "/readyz", "port": 8443}, "timeoutSeconds": 3, "periodSeconds": 5},
                 "livenessProbe": {"httpGet": {"scheme": "HTTPS", "path": "/livez", "port": 8443}, "timeoutSeconds": 2, "periodSeconds": 10}}
    rows.append(obj("apps/v1", "Deployment", spec={"replicas": 2, "revisionHistoryLimit": 3, "strategy": {"type": "RollingUpdate", "rollingUpdate": {"maxUnavailable": 0, "maxSurge": 1}}, "selector": {"matchLabels": labels.copy()}, "template": {"metadata": {"labels": labels.copy(), "annotations": {"ani.modeldev/config-sha256": checksum}}, "spec": {"serviceAccountName": NAME, "automountServiceAccountToken": True, "terminationGracePeriodSeconds": 15, "securityContext": {"fsGroup": 10001, "seccompProfile": {"type": "RuntimeDefault"}}, "topologySpreadConstraints": [{"maxSkew": 1, "topologyKey": "kubernetes.io/hostname", "whenUnsatisfiable": "DoNotSchedule", "labelSelector": {"matchLabels": labels.copy()}}], "containers": [container], "volumes": [{"name": "config", "configMap": {"name": config_name, "defaultMode": 292, "items": [{"key": "config.json", "path": "config.json"}] + [{"key": name, "path": "materials/"+name} for name in files]}}, {"name": "tls", "secret": {"secretName": NAME+"-tls", "defaultMode": 288}}]}}}))
    rows.append(obj("networking.k8s.io/v1", "NetworkPolicy", spec={"podSelector": {"matchLabels": labels.copy()}, "policyTypes": ["Ingress", "Egress"], "ingress": [{"from": [{"ipBlock": {"cidr": value}} for value in cidrs], "ports": [{"protocol": "TCP", "port": 8443}]}], "egress": [{"to": [{"ipBlock": {"cidr": value}} for value in cidrs], "ports": [{"protocol": "TCP", "port": api_server_port}]}, {"to": [{"ipBlock": {"cidr": str(service)}}], "ports": [{"protocol": "TCP", "port": kubernetes_service_port}]}, {"to": [{"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "kube-system"}}}], "ports": [{"protocol": "UDP", "port": 53}, {"protocol": "TCP", "port": 53}]}]}))
    webhooks = []
    for binding in config["bindings"]:
        webhooks.append({"name": binding["namespace"]+".modeldev.ani.io", "admissionReviewVersions": ["v1"], "sideEffects": "None", "failurePolicy": "Fail", "matchPolicy": "Exact", "reinvocationPolicy": "Never", "timeoutSeconds": 8, "namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": binding["namespace"], binding["owner_label"]["key"]: binding["owner_label"]["value"]}}, "objectSelector": {"matchLabels": {"pipelines.kubeflow.org/v2_component": "true"}}, "clientConfig": {"service": {"name": NAME, "namespace": namespace, "path": "/mutate", "port": 443}}, "rules": [{"operations": ["CREATE"], "apiGroups": [""], "apiVersions": ["v1"], "resources": ["pods"], "scope": "Namespaced"}]})
    registration = obj("admissionregistration.k8s.io/v1", "MutatingWebhookConfiguration", ns=None, webhooks=webhooks)
    for binding, hook in zip(config["bindings"], webhooks):
        if binding.get("training"):
            hook.pop("objectSelector")
    registration["metadata"]["annotations"] = {"cert-manager.io/inject-ca-from": namespace+"/"+NAME+"-serving"}
    return {"apiVersion": "v1", "kind": "List", "items": rows}, registration


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--webhook-binary", type=Path, required=True)
    parser.add_argument("--webhook-image", required=True)
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--issuer-name", required=True)
    parser.add_argument("--issuer-kind", choices=["Issuer", "ClusterIssuer"], default="Issuer")
    parser.add_argument("--api-server-cidr", action="append", required=True)
    parser.add_argument("--kubernetes-service-cidr", required=True)
    parser.add_argument("--api-server-port", required=True, type=int)
    parser.add_argument("--kubernetes-service-port", required=True, type=int)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    subprocess.run([str(args.webhook_binary), "--check-config", "--config", str(args.config)], check=True, capture_output=True, timeout=15)
    support, registration = render(json.loads(args.config.read_bytes()), args.namespace, args.webhook_image, args.issuer_name, args.issuer_kind, args.api_server_cidr, args.kubernetes_service_cidr, args.api_server_port, args.kubernetes_service_port)
    args.output.mkdir(mode=0o700, parents=True, exist_ok=False)
    for name, value in (("service.json", support), ("registration.json", registration)):
        (args.output/name).write_text(json.dumps(value, indent=2)+"\n")
    print(json.dumps({"rendered": True, "registered": False, "deployed": False, "cert_manager": "NOT_VERIFIED", "output": str(args.output)}))


if __name__ == "__main__":
    main()

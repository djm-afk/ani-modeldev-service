#!/usr/bin/env python3
"""Fedora-only, task-scoped CORE_FLOW foundation and manifest application.

No shared controller or existing application is modified. Private rendered
Secrets remain under --root/.private; reports contain references and UIDs only.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess

OWNER = "finetuning-core-flow-20261014"
SYSTEM = "ani-ft-core-system-20261014"
TENANT = "ani-ft-core-20261014"
CLUSTER_UID = "5277649d-e28d-4a0a-ae76-8354365c63bc"
POSTGRES = "172.16.101.10:5000/library/postgres@sha256:7bade6d532592ca8ce7ee32def7399dad2607c4ea5583839fc4352a095a11ea6"


def encoded(value):
    return json.dumps(value, separators=(",", ":"), ensure_ascii=False).encode()


def save(path, value):
    path.write_bytes(encoded(value) + b"\n")
    path.chmod(0o600)


class Cluster:
    def __init__(self, kubectl, kubeconfig):
        self.base = [kubectl, "--kubeconfig", kubeconfig, "--request-timeout=30s"]
        self.receipts = []

    def run(self, args, body=None):
        r = subprocess.run(self.base + args, input=body, capture_output=True, timeout=45)
        self.receipts.append({"args": args, "exit_code": r.returncode})
        if r.returncode:
            raise RuntimeError("kubectl failed; private response suppressed: " + " ".join(args[:4]))
        return r.stdout

    def get(self, kind, name, namespace=None, optional=False):
        args = ["get", kind, name] + (["-n", namespace] if namespace else [])
        if optional:
            args += ["--ignore-not-found"]
        raw = self.run(args + ["-o", "json"])
        return json.loads(raw) if raw.strip() else None

    def guard(self):
        if self.get("namespace", "kube-system")["metadata"]["uid"] != CLUSTER_UID:
            raise RuntimeError("authorized cluster UID mismatch")
        nodes = json.loads(self.run(["get", "nodes", "-o", "json"]))["items"]
        actual = {a["address"] for n in nodes for a in n["status"]["addresses"] if a["type"] == "InternalIP"}
        if actual != {"172.16.101.10", "172.16.101.11", "172.16.101.12"}:
            raise RuntimeError("authorized node identity mismatch")

    def apply(self, obj):
        meta = obj["metadata"]
        ns = meta.get("namespace")
        if ns not in {None, SYSTEM, TENANT} or meta.get("labels", {}).get("ani.io/managed-by") != OWNER:
            raise RuntimeError("manifest outside task ownership")
        if ns is None and not (obj["kind"] == "Namespace" and meta["name"] in {SYSTEM, TENANT} or obj["kind"] in {"ClusterRole", "ClusterRoleBinding"} and meta["name"] == OWNER):
            raise RuntimeError("cluster-scoped manifest outside finite task RBAC")
        original = self.get(obj["kind"], meta["name"], ns, optional=True)
        if original and original["metadata"].get("labels", {}).get("ani.io/managed-by") != OWNER:
            raise RuntimeError("existing object has another owner")
        self.run(["apply", "--dry-run=server", "-f", "-", "-o", "name"], encoded(obj))
        self.run(["apply", "-f", "-", "-o", "name"], encoded(obj))


def obj(kind, name, namespace=None, **fields):
    api = "v1" if kind in {"Namespace", "Secret", "Service", "ServiceAccount", "ConfigMap", "PersistentVolumeClaim", "ResourceQuota", "LimitRange"} else "rbac.authorization.k8s.io/v1"
    if kind == "Deployment":
        api = "apps/v1"
    if kind == "NetworkPolicy":
        api = "networking.k8s.io/v1"
    meta = {"name": name, "labels": {"ani.io/managed-by": OWNER}}
    if namespace:
        meta["namespace"] = namespace
    return {"apiVersion": api, "kind": kind, "metadata": meta, **fields}


def foundation(root):
    private = root / ".private"
    private.mkdir(mode=0o700, exist_ok=True)
    statefile = private / "foundation-state.json"
    if statefile.exists():
        state = json.loads(statefile.read_bytes())
    else:
        state = {"owner": OWNER, "database": "finetuning_core_flow", "runtime_role": "finetuning_runtime", "owner_password": secrets.token_hex(24), "runtime_password": secrets.token_hex(24)}
        save(statefile, state)
    if state["owner"] != OWNER:
        raise RuntimeError("foundation private state has another owner")
    rows = [obj("Namespace", SYSTEM), obj("Namespace", TENANT)]
    rows[1]["metadata"]["labels"]["ani.io/kubeflow-tenant"] = "true"
    rows += [obj("ServiceAccount", "modeldev-control", SYSTEM, automountServiceAccountToken=True)]
    for name in ["modeldev-step", "trainer-workload", "verifier"]:
        rows.append(obj("ServiceAccount", name, TENANT, automountServiceAccountToken=name == "modeldev-step"))
    rows.append(obj("ConfigMap", "ani-workspace-policy", TENANT, data={"storageClass": "ani-cephfs", "maxSize": "20Gi"}))
    rows.append(obj("ResourceQuota", "core-flow-budget", TENANT, spec={"hard": {"requests.cpu": "8", "limits.cpu": "16", "requests.memory": "32Gi", "limits.memory": "64Gi", "pods": "40", "persistentvolumeclaims": "8", "requests.storage": "160Gi"}}))
    # Native Argo init/wait containers omit these fields. Namespace defaults
    # satisfy this task's quota while preserving explicit workload resources.
    rows.append(obj("LimitRange", "core-flow-container-defaults", TENANT, spec={"limits": [{"type": "Container", "defaultRequest": {"cpu": "100m", "memory": "64Mi"}, "default": {"cpu": "500m", "memory": "512Mi"}}]}))
    cluster_rules = [{"apiGroups": [""], "resources": ["namespaces"], "resourceNames": [TENANT], "verbs": ["get"]}, {"apiGroups": ["authentication.k8s.io"], "resources": ["tokenreviews"], "verbs": ["create"]}]
    rows.append(obj("ClusterRole", OWNER, rules=cluster_rules))
    rows.append(obj("ClusterRoleBinding", OWNER, roleRef={"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": OWNER}, subjects=[{"kind": "ServiceAccount", "name": "modeldev-control", "namespace": SYSTEM}]))
    rules = [{"apiGroups": [""], "resources": ["pods", "pods/log", "persistentvolumeclaims", "serviceaccounts"], "verbs": ["get", "list", "watch"]}, {"apiGroups": [""], "resources": ["pods"], "verbs": ["patch"]}, {"apiGroups": [""], "resources": ["persistentvolumeclaims"], "verbs": ["create"]}, {"apiGroups": ["trainer.kubeflow.org"], "resources": ["trainjobs"], "verbs": ["get", "list", "watch", "create", "patch", "update"]}, {"apiGroups": ["trainer.kubeflow.org"], "resources": ["trainingruntimes"], "verbs": ["get"]}, {"apiGroups": ["jobset.x-k8s.io"], "resources": ["jobsets"], "verbs": ["get", "list", "watch"]}, {"apiGroups": ["batch"], "resources": ["jobs"], "verbs": ["get", "list", "watch"]}, {"apiGroups": ["argoproj.io"], "resources": ["workflows"], "verbs": ["get", "list", "watch", "patch", "update"]}, {"apiGroups": ["pipelines.kubeflow.org"], "resources": ["pipelines", "pipelines/versions", "experiments", "runs", "runs/terminate"], "verbs": ["get", "list", "create", "delete", "update"]}]
    rows.append(obj("Role", "modeldev-control", TENANT, rules=rules))
    rows.append(obj("RoleBinding", "modeldev-control", TENANT, roleRef={"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "modeldev-control"}, subjects=[{"kind": "ServiceAccount", "name": "modeldev-control", "namespace": SYSTEM}]))
    # Restrict the shared runtime lookup to the one already verified profile.
    # Runtime permission is folded into the existing finite role name above.
    rows[-4]["rules"].append({"apiGroups": ["trainer.kubeflow.org"], "resources": ["clustertrainingruntimes"], "resourceNames": ["ani-modeldev-cpu-uid10001-exit-v1-1eb08af8"], "verbs": ["get"]})
    rows[-2]["rules"].append({"apiGroups": ["pipelines.kubeflow.org"], "resources": ["runs"], "verbs": ["terminate"]})
    dsn = "postgres://finetuning_runtime:" + state["runtime_password"] + "@postgresql." + SYSTEM + ".svc:5432/finetuning_core_flow?sslmode=disable"
    secret = {"owner-password": state["owner_password"], "runtime-password": state["runtime_password"], "database.dsn": dsn}
    rows.append(obj("Secret", "core-flow-database", SYSTEM, type="Opaque", data={k: base64.b64encode(v.encode()).decode() for k, v in secret.items()}))
    rows.append(obj("PersistentVolumeClaim", "core-flow-postgresql", SYSTEM, spec={"storageClassName": "ani-block", "accessModes": ["ReadWriteOnce"], "resources": {"requests": {"storage": "4Gi"}}}))
    security = {"runAsNonRoot": True, "runAsUser": 999, "runAsGroup": 999, "fsGroup": 999, "seccompProfile": {"type": "RuntimeDefault"}}
    container = {"name": "postgresql", "image": POSTGRES, "imagePullPolicy": "IfNotPresent", "env": [{"name": "POSTGRES_DB", "value": state["database"]}, {"name": "POSTGRES_USER", "value": "finetuning_owner"}, {"name": "POSTGRES_PASSWORD", "valueFrom": {"secretKeyRef": {"name": "core-flow-database", "key": "owner-password"}}}, {"name": "PGDATA", "value": "/var/lib/postgresql/data/pgdata"}], "ports": [{"containerPort": 5432}], "resources": {"requests": {"cpu": "100m", "memory": "256Mi"}, "limits": {"cpu": "1", "memory": "1Gi"}}, "securityContext": {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}}, "volumeMounts": [{"name": "data", "mountPath": "/var/lib/postgresql/data"}], "readinessProbe": {"exec": {"command": ["pg_isready", "-U", "finetuning_owner", "-d", state["database"]]}, "initialDelaySeconds": 3, "periodSeconds": 3}}
    rows.append(obj("Deployment", "core-flow-postgresql", SYSTEM, spec={"replicas": 1, "strategy": {"type": "Recreate"}, "selector": {"matchLabels": {"app": "core-flow-postgresql"}}, "template": {"metadata": {"labels": {"app": "core-flow-postgresql", "ani.io/managed-by": OWNER}}, "spec": {"automountServiceAccountToken": False, "securityContext": security, "containers": [container], "volumes": [{"name": "data", "persistentVolumeClaim": {"claimName": "core-flow-postgresql"}}]}}}))
    rows.append(obj("Service", "postgresql", SYSTEM, spec={"selector": {"app": "core-flow-postgresql"}, "ports": [{"name": "postgres", "port": 5432, "targetPort": 5432}]}))
    rows.append(obj("Service", "modeldev", SYSTEM, spec={"selector": {"app": "core-flow-driver"}, "ports": [{"name": "command", "port": 9000, "targetPort": 9000}, {"name": "step", "port": 9001, "targetPort": 9001}, {"name": "admin", "port": 9002, "targetPort": 9002}]}))
    for ns in [SYSTEM, TENANT]:
        rows.append(obj("NetworkPolicy", "core-flow-default", ns, spec={"podSelector": {}, "policyTypes": ["Ingress", "Egress"], "ingress": [{"from": [{"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": SYSTEM}}}, {"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": TENANT}}}, {"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "kubeflow"}}}]}], "egress": [{"to": [{"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": n}}} for n in [SYSTEM, TENANT, "kubeflow", "ani-platform", "kubeflow-system"]]}, {"to": [{"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "kube-system"}}}], "ports": [{"protocol": "UDP", "port": 53}, {"protocol": "TCP", "port": 53}]}, {"to": [{"ipBlock": {"cidr": ip + "/32"}} for ip in ["172.16.101.10", "172.16.101.11", "172.16.101.12", "10.96.0.1"]], "ports": [{"port": 6443, "protocol": "TCP"}, {"port": 443, "protocol": "TCP"}]}]}))
    return rows


def database_proof(cluster, database, expected_materials):
    # A fresh psql invocation must observe the committed database. The exec
    # process exit alone cannot prove that stdin ever reached PostgreSQL.
    query = """
    WITH runtime AS (SELECT * FROM pg_roles WHERE rolname='finetuning_runtime')
    SELECT json_build_object(
      'database', current_database(), 'owner_role', current_user,
      'verified_at', clock_timestamp(),
      'schema_materials', (SELECT COALESCE(json_agg(json_build_object('name', name, 'sha256', sha256) ORDER BY name), '[]'::json) FROM public.core_flow_schema_materials),
      'runtime_role', (SELECT json_build_object('name', rolname, 'can_login', rolcanlogin, 'superuser', rolsuper, 'bypass_rls', rolbypassrls, 'create_database', rolcreatedb, 'create_role', rolcreaterole, 'replication', rolreplication, 'inherit', rolinherit) FROM runtime),
      'role_memberships', (SELECT COALESCE(json_agg(parent.rolname ORDER BY parent.rolname), '[]'::json) FROM pg_auth_members membership JOIN pg_roles parent ON parent.oid=membership.roleid WHERE membership.member=(SELECT oid FROM runtime)),
      'database_connect', has_database_privilege('finetuning_runtime', current_database(), 'CONNECT'),
      'database_create', has_database_privilege('finetuning_runtime', current_database(), 'CREATE'),
      'schema_usage', has_schema_privilege('finetuning_runtime', 'public', 'USAGE'),
      'schema_create', has_schema_privilege('finetuning_runtime', 'public', 'CREATE'),
      'table_privileges', (SELECT COALESCE(json_agg(json_build_object(
        'name', relation.relname, 'owner', relation.relowner=(SELECT oid FROM runtime),
        'select', has_table_privilege('finetuning_runtime', relation.oid, 'SELECT'),
        'insert', has_table_privilege('finetuning_runtime', relation.oid, 'INSERT'),
        'update', has_table_privilege('finetuning_runtime', relation.oid, 'UPDATE'),
        'delete', has_table_privilege('finetuning_runtime', relation.oid, 'DELETE'),
        'truncate', has_table_privilege('finetuning_runtime', relation.oid, 'TRUNCATE'),
        'references', has_table_privilege('finetuning_runtime', relation.oid, 'REFERENCES'),
        'trigger', has_table_privilege('finetuning_runtime', relation.oid, 'TRIGGER')) ORDER BY relation.relname), '[]'::json)
        FROM pg_class relation JOIN pg_namespace ns ON ns.oid=relation.relnamespace WHERE ns.nspname='public' AND relation.relkind IN ('r','p')),
      'sequence_privileges', (SELECT COALESCE(json_agg(json_build_object(
        'name', relation.relname, 'owner', relation.relowner=(SELECT oid FROM runtime),
        'usage', has_sequence_privilege('finetuning_runtime', relation.oid, 'USAGE'),
        'select', has_sequence_privilege('finetuning_runtime', relation.oid, 'SELECT'),
        'update', has_sequence_privilege('finetuning_runtime', relation.oid, 'UPDATE')) ORDER BY relation.relname), '[]'::json)
        FROM pg_class relation JOIN pg_namespace ns ON ns.oid=relation.relnamespace WHERE ns.nspname='public' AND relation.relkind='S'));
    """
    raw = cluster.run(["-n", SYSTEM, "exec", "deployment/core-flow-postgresql", "--", "psql", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-U", "finetuning_owner", "-d", database, "-c", query])
    try:
        proof = json.loads(raw)
    except (ValueError, TypeError):
        raise RuntimeError("task database readback absent or malformed") from None
    if not isinstance(proof, dict):
        raise RuntimeError("task database readback absent or malformed")
    role = proof.get("runtime_role")
    if proof.get("database") != database or proof.get("owner_role") != "finetuning_owner" or proof.get("schema_materials") != expected_materials or not isinstance(role, dict) or role.get("name") != "finetuning_runtime" or role.get("can_login") is not True or any(role.get(flag) is not False for flag in ("superuser", "bypass_rls", "create_database", "create_role", "replication", "inherit")) or proof.get("role_memberships") != [] or proof.get("database_connect") is not True or proof.get("schema_usage") is not True or proof.get("database_create") is not False or proof.get("schema_create") is not False:
        raise RuntimeError("task database migration or finite runtime role readback mismatch")
    tables = proof.get("table_privileges", [])
    if len(tables) < 2 or sum(row.get("name") == "core_flow_schema_materials" for row in tables) != 1:
        raise RuntimeError("task database table privilege inventory absent")
    for row in tables:
        allowed = row["name"] != "core_flow_schema_materials"
        if any(row.get(flag) is not allowed for flag in ("select", "insert", "update", "delete")) or any(row.get(flag) is not False for flag in ("owner", "truncate", "references", "trigger")):
            raise RuntimeError("task database table runtime privileges differ from finite grants")
    for row in proof.get("sequence_privileges", []):
        if row.get("usage") is not True or row.get("select") is not True or row.get("update") is not False or row.get("owner") is not False:
            raise RuntimeError("task database sequence runtime privileges differ from finite grants")
    return proof


def migrate(cluster, root, source):
    state = json.loads((root / ".private/foundation-state.json").read_bytes())
    if state["owner"] != OWNER or state["database"] != "finetuning_core_flow":
        raise RuntimeError("task database identity mismatch")
    sql = ["BEGIN;", "DO $$ BEGIN IF current_database() <> 'finetuning_core_flow' OR current_user <> 'finetuning_owner' THEN RAISE EXCEPTION 'wrong task database'; END IF; END $$;", "DO $$ BEGIN IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='finetuning_runtime' AND (rolsuper OR rolbypassrls OR rolcreatedb OR rolcreaterole)) THEN RAISE EXCEPTION 'unsafe runtime role'; END IF; IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='finetuning_runtime') THEN CREATE ROLE finetuning_runtime LOGIN PASSWORD '" + state["runtime_password"] + "' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; END IF; END $$;", "REVOKE CREATE ON SCHEMA public FROM PUBLIC;", "CREATE TABLE IF NOT EXISTS core_flow_schema_materials(name text PRIMARY KEY, sha256 text NOT NULL);"]
    files = []
    for path in sorted((source / "migrations").glob("*.up.sql")):
        raw = path.read_bytes()
        sha = hashlib.sha256(raw).hexdigest()
        files.append({"name": path.name, "sha256": sha})
        # psql's condition avoids applying a previous migration twice while
        # rejecting changed bytes under a previously installed filename.
        sql += ["DO $$ BEGIN IF EXISTS(SELECT 1 FROM core_flow_schema_materials WHERE name='" + path.name + "' AND sha256<>'" + sha + "') THEN RAISE EXCEPTION 'migration material drift'; END IF; END $$;", "SELECT NOT EXISTS(SELECT 1 FROM core_flow_schema_materials WHERE name='" + path.name + "') AS apply_migration \\gset", "\\if :apply_migration", raw.decode(), "INSERT INTO core_flow_schema_materials VALUES ('" + path.name + "','" + sha + "');", "\\endif"]
    if not files:
        raise RuntimeError("task migration source absent")
    sql += ["GRANT USAGE ON SCHEMA public TO finetuning_runtime;", "GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO finetuning_runtime;", "GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO finetuning_runtime;", "REVOKE ALL ON core_flow_schema_materials FROM finetuning_runtime;", "ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT,INSERT,UPDATE,DELETE ON TABLES TO finetuning_runtime;", "COMMIT;"]
    script = "\n".join(sql).encode() + b"\n"
    private = root / ".private/migrate.sql"
    private.write_bytes(script)
    cluster.run(["-n", SYSTEM, "exec", "-i", "deployment/core-flow-postgresql", "--", "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", "finetuning_owner", "-d", state["database"]], script)
    proof = database_proof(cluster, state["database"], files)
    save(root / "database-receipt.json", {"owner": OWNER, "database": proof["database"], "namespace": SYSTEM, "runtime_role": proof["runtime_role"]["name"], "schema_materials": proof["schema_materials"], "database_verification": proof, "database_verification_sha256": hashlib.sha256(encoded(proof)).hexdigest(), "business_rows_inserted": 0, "commands": cluster.receipts})
    print(json.dumps({"phase": "TASK_DATABASE_MIGRATED", "receipt": str(root / "database-receipt.json"), "business_rows_inserted": 0}))


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("command", choices=["foundation", "apply", "migrate"])
    p.add_argument("--root", type=Path, required=True)
    p.add_argument("--kubectl", required=True)
    p.add_argument("--kubeconfig", required=True)
    p.add_argument("--manifest", type=Path)
    p.add_argument("--source", type=Path)
    args = p.parse_args()
    if not Path("/etc/fedora-release").exists():
        raise SystemExit("execute only on Fedora")
    os.umask(0o077)
    args.root.mkdir(mode=0o700, parents=True, exist_ok=True)
    cluster = Cluster(args.kubectl, args.kubeconfig)
    cluster.guard()
    if args.command == "migrate":
        for name in [SYSTEM, TENANT]:
            if cluster.get("namespace", name)["metadata"]["labels"].get("ani.io/managed-by") != OWNER:
                raise RuntimeError("task namespace owner mismatch")
        migrate(cluster, args.root, args.source)
        return
    rows = foundation(args.root) if args.command == "foundation" else json.loads(args.manifest.read_bytes())["items"]
    private = args.root / ".private"
    private.mkdir(mode=0o700, exist_ok=True)
    rendered = private / (args.command + "-manifest.json")
    save(rendered, {"apiVersion": "v1", "kind": "List", "items": rows})
    for row in rows:
        cluster.apply(row)
    ns = {n: cluster.get("namespace", n)["metadata"]["uid"] for n in [SYSTEM, TENANT]}
    result = {"owner": OWNER, "cluster_uid": CLUSTER_UID, "namespace_uids": ns, "manifest_reference": str(rendered), "manifest_sha256": hashlib.sha256(rendered.read_bytes()).hexdigest(), "commands": cluster.receipts, "business_acceptance": "NOT_RUN"}
    save(args.root / (args.command + "-receipt.json"), result)
    print(json.dumps({"phase": "TASK_FOUNDATION_APPLIED", "namespace_uids": ns, "receipt": str(args.root / (args.command + "-receipt.json"))}))


if __name__ == "__main__":
    main()

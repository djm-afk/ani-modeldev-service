#!/usr/bin/env python3
"""Guarded maintenance-only Workbench submit/query through the designated SSH entry.

No credentials or raw logs are emitted. A reserved request ID is never submitted
twice by this helper, including after a lost SSH/HTTP response. Query it instead.
The existing Workbench enforces its own deployment UID/spec/RV checks later;
these preflight checks cannot atomically guard its future deployment operation.
"""
import argparse
import base64
import datetime
import getpass
import hashlib
from http.cookiejar import CookieJar
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import urllib.error
import urllib.request

SSH = "kubercloud@100.64.128.12"
BASE = "http://192.168.25.40:9998"
BRANCH = "codex/modeldev-create-maintenance"
FLAG = "ANI_MODELDEV_CREATE_PAUSED"
TARGET = {"ssh_user": "kubercloud", "ssh_host": "100.64.128.12",
          "context": "kubernetes-admin@ani-kcn-full",
          "apiserver": "https://lb.kubesphere.local:6443",
          "cluster_id": "f39bf6e2-f3ed-4191-ada3-caeebf86c220",
          "kube_system_uid": "3eeb5379-8748-4bf8-8f6d-d7a48ce3bc24"}
PREFIX = "10.10.1.66:30003/ani-gpu-platform/ani-governance"
STAGES = {"fetch", "verify", "build", "push", "deploy", "ready"}


class Refusal(Exception):
    pass


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def verified_required_check(commit):
    """Reuse the actual Fedora full gate only for its exact committed inputs."""
    proof = Path(__file__).resolve().parent.parent / "gov-maintenance-codec-pin" / "delivery.json"
    raw = proof.read_bytes()
    value = json.loads(raw)
    check = value["complete_make_check"]
    expected_files = {"go.mod", "go.sum", "app/admin/service/internal/data/modeldev_maintenance_original_codec_test.go"}
    if (value.get("schema") != "ani.gov.maintenance.original-v3-pin.delivery.v1"
            or value.get("commit") != commit or value.get("branch") != BRANCH
            or value.get("push", {}).get("exit_code") != 0
            or check.get("exit_code") != 0 or check.get("includes_audit") is not True
            or set(value.get("source_hashes", {})) != expected_files):
        raise Refusal("complete required-check receipt does not match selected commit")
    log = proof.parent / "make-check.log"
    if hashlib.sha256(log.read_bytes()).hexdigest() != check["log_sha256"]:
        raise Refusal("required-check raw log SHA mismatch")
    repo = "/home/chabking/workspace/.worktrees/governance-modeldev-create-maintenance"
    for path, digest in value["source_hashes"].items():
        blob = subprocess.check_output(["git", "-C", repo, "show", commit + ":" + path], timeout=10)
        if hashlib.sha256(blob).hexdigest() != digest:
            raise Refusal("committed input differs from tested input")
    return {"commit": commit, "exit_code": 0, "includes_audit": True,
            "delivery_sha256": hashlib.sha256(raw).hexdigest(),
            "log_sha256": check["log_sha256"], "execution_host": "ssh fedora"}


def scalar_fields(value, keys):
    return {k: value[k] for k in keys if k in value and
            (value[k] is None or type(value[k]) in (str, int, float, bool))}


def public_job(job):
    """Positive field allowlist; never forward arbitrary error text or payloads."""
    out = scalar_fields(job, ("id", "request_id", "project", "branch", "source_commit",
                        "run_verify", "status", "created_at", "started_at", "finished_at"))
    out["error_present"] = bool(job.get("error"))
    out["steps"] = [scalar_fields(s, ("id", "status", "started_at", "finished_at", "elapsed"))
                    for s in job.get("steps", []) if s.get("id") in STAGES]
    for key in ("image", "previous_image", "image_tag"):
        if job.get(key) is not None:
            if not re.fullmatch(re.escape(PREFIX) + r"(?::[A-Za-z0-9_.-]+|@sha256:[0-9a-f]{64})", job[key]):
                raise Refusal("unexpected job image reference")
            out[key] = job[key]
    out["recipe_sha256"] = {k: v for k, v in job.get("recipe_sha256", {}).items()
                            if k in ("Dockerfile", ".dockerignore", "build-image.py")
                            and isinstance(v, str) and re.fullmatch(r"[0-9a-f]{64}", v)}
    out["build"] = scalar_fields(job.get("build", {}), (
        "source_revision", "source_snapshot", "source_dirty", "version", "target", "platform",
        "image", "local_image_id", "local_config_digest", "local_descriptor_digest",
        "registry_digest", "pushed", "builder_image", "runtime_image"))
    intent = job.get("deployment_intent")
    if isinstance(intent, dict):
        out["deployment_intent"] = scalar_fields(intent, ("uid", "image", "time"))
    readiness = job.get("readiness")
    if isinstance(readiness, dict):
        out["readiness"] = scalar_fields(readiness, ("replicas", "observed_generation"))
        out["readiness"]["pods"] = []
        for pod in readiness.get("pods", []):
            item = scalar_fields(pod, ("name", "uid", "node", "phase", "terminating"))
            item["containers"] = [scalar_fields(c, ("name", "ready", "restarts", "image"))
                                  for c in pod.get("containers", [])]
            out["readiness"]["pods"].append(item)
    if isinstance(job.get("rollback"), dict):
        out["rollback"] = scalar_fields(job["rollback"], ("status", "image"))
    return out


def public_log_progress(raw):
    """Only package status lines and fixed command markers; never raw test output."""
    lines = raw.decode("utf-8", "replace").splitlines()
    pattern = re.compile(r"(?:ok|\?|FAIL)\s+go-wind-admin/[A-Za-z0-9_./-]+(?:\s+(?:[0-9.]+s|\(cached\)|\[no test files\]))?")
    markers = {"$ make check", "FAIL"}
    safe = [{"line": n, "text": line} for n, line in enumerate(lines, 1)
            if pattern.fullmatch(line.strip()) or line.strip() in markers]
    return {"safe_progress_lines": safe[-24:], "total_lines": len(lines),
            "timeout_marker_present": any("步骤超时，已停止本次执行" in line for line in lines),
            "test_failure_marker_present": any(line.startswith("--- FAIL:") for line in lines)}


def kget(*args, optional=False):
    result = subprocess.run(["kubectl", "--context", TARGET["context"], "--request-timeout=8s", *args, "-o", "json"],
                            capture_output=True, timeout=15)
    if result.returncode:
        if optional and b"(NotFound)" in result.stderr:
            return None
        raise Refusal("required Kubernetes read failed")
    return json.loads(result.stdout)


def identity():
    import yaml  # Same authoritative network.cluster_id extraction as verify_target.py.
    if getpass.getuser() != TARGET["ssh_user"]:
        raise Refusal("SSH user mismatch")
    cfg = kget("config", "view", "--minify")
    result = subprocess.run(["kubectl", "config", "current-context"],
                            capture_output=True, timeout=10, check=True)
    private = kget("-n", "ani-system", "get", "secret", "ani-resource-private")
    data = base64.b64decode(private["data"]["config.yaml"], validate=True)
    observed = dict(TARGET, context=result.stdout.decode().strip(),
                    apiserver=cfg["clusters"][0]["cluster"]["server"],
                    kube_system_uid=kget("get", "namespace", "kube-system")["metadata"]["uid"],
                    cluster_id=yaml.safe_load(data)["network"]["cluster_id"])
    if observed != TARGET:
        raise Refusal("complete cluster identity mismatch")
    return observed


def pause_env(container):
    """Kubelet precedence: ordered envFrom, then ordered explicit env entries."""
    effective, source = None, "unset"

    def read_key(kind, ref, key):
        name = ref.get("name")
        if not isinstance(name, str) or not name:
            raise Refusal("invalid pause reference")
        obj = kget("-n", "ani-system", "get", kind, name, optional=ref.get("optional") is True)
        if obj is None or key not in obj.get("data", {}):
            if ref.get("optional") is True:
                return None
            raise Refusal("required pause reference/key missing")
        value = obj["data"][key]
        return base64.b64decode(value, validate=True).decode("utf-8") if kind == "secret" else value

    for item in container.get("envFrom", []):
        prefix = item.get("prefix", "")
        if not FLAG.startswith(prefix):
            continue
        refs = [(kind, item[field]) for kind, field in
                (("configmap", "configMapRef"), ("secret", "secretRef")) if field in item]
        if len(refs) != 1:
            raise Refusal("invalid envFrom source")
        kind, ref = refs[0]
        # An existing envFrom source without this exact key contributes nothing.
        obj = kget("-n", "ani-system", "get", kind, ref["name"], optional=ref.get("optional") is True)
        key = FLAG[len(prefix):]
        if obj is not None and key in obj.get("data", {}):
            value = obj["data"][key]
            effective = base64.b64decode(value, validate=True).decode("utf-8") if kind == "secret" else value
            source = "envFrom:" + kind
    for item in container.get("env", []):
        if item.get("name") != FLAG:
            continue
        if "valueFrom" not in item:
            effective, source = item.get("value", ""), "env:literal"
            continue
        value_from = item["valueFrom"]
        refs = [(kind, value_from[field]) for kind, field in
                (("configmap", "configMapKeyRef"), ("secret", "secretKeyRef")) if field in value_from]
        if len(refs) != 1 or len(value_from) != 1:
            raise Refusal("unsupported pause valueFrom")
        kind, ref = refs[0]
        value = read_key(kind, ref, ref["key"])
        if value is not None:
            effective, source = value, "env:" + kind
    if effective not in (None, "", "true"):
        raise Refusal("pause flag is false or invalid")
    return {"source": source, "effective": "unset" if effective is None else effective}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def remote(args):
    if getpass.getuser() != TARGET["ssh_user"] or not os.environ.get("SSH_CONNECTION"):
        return {"status": "REJECTED_NO_POST", "reason": "designated SSH session required"}
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(), urllib.request.HTTPCookieProcessor(CookieJar()))

    def http(path, body=None, csrf=None, raw=False):
        headers = {"Accept": "application/json"}
        if body is not None:
            headers.update({"Content-Type": "application/json", "Origin": BASE, "X-CSRF-Token": csrf})
        request = urllib.request.Request(BASE + path, data=None if body is None else json.dumps(body).encode(),
                                         headers=headers, method="GET" if body is None else "POST")
        with opener.open(request, timeout=40) as response:
            data = response.read((4 << 20) + 1)
            if len(data) > 4 << 20:
                raise Refusal("bounded HTTP response exceeded")
            return (response.status, data) if raw else json.loads(data)

    posted = False
    selection = {"project": "governance", "branch": BRANCH, "commit": args.get("commit"),
                 "environment": "ani-kcn-full", "harbor": "intranet", "run_verify": not bool(args.get("verified_check")),
                 "request_id": args.get("request_id")}
    try:
        checked = {}
        if args["command"] == "submit":
            checked["identity"] = identity()
            if args.get("verified_check"):
                if args["verified_check"].get("commit") != selection["commit"]:
                    raise Refusal("required-check reuse commit mismatch")
                checked["required_check_reused"] = args["verified_check"]
            deployment = kget("-n", "ani-system", "get", "deployment", "ani-governance")
            if deployment["metadata"]["uid"] != args["expected_deployment_uid"]:
                raise Refusal("Gov Deployment UID mismatch")
            containers = [c for c in deployment["spec"]["template"]["spec"]["containers"] if c["name"] == "governance"]
            if len(containers) != 1:
                raise Refusal("Gov container mismatch")
            checked["pause_env"] = pause_env(containers[0])
            checked["deployment_uid"] = deployment["metadata"]["uid"]
        session = http("/api/me")
        if not isinstance(session.get("csrf"), str) or not session["csrf"]:
            raise Refusal("Workbench session unavailable")
        if args.get("expected_actor") and session.get("username") != args["expected_actor"]:
            raise Refusal("Workbench recovery actor mismatch")
        jobs = http("/api/jobs")
        if args["command"] == "query":
            matches = [j for j in jobs if j.get("id") == args.get("job_id")] if args.get("job_id") else [j for j in jobs if j.get("request_id") == args["request_id"]]
            if len(matches) != 1:
                return {"status": "NOT_FOUND_OR_AMBIGUOUS_NOT_PROOF_OF_NO_SUBMISSION", "request_id": args.get("request_id")}
            if not re.fullmatch(r"[0-9a-f]{32}", matches[0].get("id", "")):
                raise Refusal("invalid returned job ID")
            job = http("/api/jobs/" + matches[0]["id"])
            if job.get("project") != "governance" or job.get("branch") != BRANCH:
                raise Refusal("query is not the Gov maintenance job")
            _, log = http("/api/jobs/" + job["id"] + "/log", raw=True)
            return {"status": "QUERY", "job": public_job(job),
                    "log": {"bytes": len(log), "sha256": hashlib.sha256(log).hexdigest(), "contents_omitted": True,
                            **(public_log_progress(log) if args.get("diagnostic") else {})}}
        if any(j.get("request_id") == selection["request_id"] for j in jobs):
            raise Refusal("request ID already exists; query instead")
        if any(j.get("status") not in ("succeeded", "failed", "interrupted") for j in jobs):
            raise Refusal("Workbench has an active or unknown-state job")
        catalog = http("/api/catalog")
        gov = next(p for p in catalog["projects"] if p.get("id") == "governance")
        expected_env = {"id": "ani-kcn-full", "context": TARGET["context"], "namespace": "ani-system", "deployment": "ani-governance", "container": "governance"}
        expected_harbor = {"id": "intranet", "registry": "10.10.1.66:30003", "repository": "ani-gpu-platform/ani-governance"}
        if (any(gov["environment"].get(k) != v for k, v in expected_env.items()) or
            any(gov["harbor"].get(k) != v for k, v in expected_harbor.items()) or
            gov.get("kind") != "governance" or
            gov.get("repository") != "https://github.com/zhangzhe-ctrl/ani-governance.git" or
            gov.get("verify_commands") != "make check" or gov.get("verify_ready") is not True):
            raise Refusal("Gov catalog target or quality contract mismatch")
        branches = http("/api/branches?project=governance")
        if not any(b.get("name") == BRANCH and b.get("sha") == selection["commit"] for b in branches):
            raise Refusal("maintenance branch HEAD does not equal requested SHA")
        fresh = kget("-n", "ani-system", "get", "deployment", "ani-governance")
        if fresh["metadata"]["uid"] != deployment["metadata"]["uid"] or fresh["spec"] != deployment["spec"]:
            raise Refusal("Gov Deployment changed during preflight")
        checked["pause_env"] = pause_env(next(c for c in fresh["spec"]["template"]["spec"]["containers"] if c["name"] == "governance"))
        checked["target"] = expected_env
        posted = True  # Every failure from here is uncertain; there is no retry path.
        job = http("/api/jobs", body=selection, csrf=session["csrf"])
        if (job.get("project") != "governance" or job.get("source_commit") != selection["commit"] or
            job.get("branch") != BRANCH or job.get("request_id") != selection["request_id"] or
            job.get("run_verify") is not selection["run_verify"] or not re.fullmatch(r"[0-9a-f]{32}", job.get("id", "")) or
            job.get("selection") != {k: v for k, v in selection.items() if k != "request_id"}):
            raise Refusal("unexpected POST response")
        return {"status": "SUBMITTED", "selection": selection, "preflight": checked, "job": public_job(job)}
    except Exception as error:
        return {"status": "POST_OUTCOME_UNKNOWN" if posted else "REJECTED_NO_POST" if args["command"] == "submit" else "QUERY_FAILED",
                "selection": selection if args["command"] == "submit" else None,
                "reason": str(error) if isinstance(error, Refusal) else type(error).__name__,
                "http_status": error.code if isinstance(error, urllib.error.HTTPError) else None,
                "instruction": "Do not resubmit; query the same request_id." if posted else "No POST was attempted."}


def main():
    if len(sys.argv) == 3 and sys.argv[1] == "--remote":
        print(json.dumps(remote(json.loads(sys.argv[2])), ensure_ascii=False), flush=True)
        return
    parser = argparse.ArgumentParser(description=__doc__)
    subs = parser.add_subparsers(dest="command", required=True)
    submit = subs.add_parser("submit")
    submit.add_argument("--commit", required=True)
    submit.add_argument("--request-id", required=True)
    submit.add_argument("--expected-deployment-uid", required=True)
    submit.add_argument("--reuse-verified-check", action="store_true", help="reuse exact-commit Fedora make check including audit")
    query = subs.add_parser("query")
    query.add_argument("--diagnostic", action="store_true", help="allowlisted package progress only")
    select = query.add_mutually_exclusive_group(required=True)
    select.add_argument("--job-id")
    select.add_argument("--request-id")
    args = vars(parser.parse_args())
    for field, pattern in (("commit", r"[0-9a-f]{40}"), ("request_id", r"[A-Za-z0-9-]{16,64}"),
                           ("job_id", r"[0-9a-f]{32}"), ("expected_deployment_uid", r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}")):
        if args.get(field) is not None and not re.fullmatch(pattern, args[field]):
            parser.error("invalid " + field)
    if args.pop("reuse_verified_check", False):
        args["verified_check"] = verified_required_check(args["commit"])
    receipt = None
    reserved = {"status": "POST_OUTCOME_UNKNOWN", "recorded_at_utc": now(), "selection": args,
                "instruction": "Reserved before SSH. Never resubmit this request ID; query it if interrupted."}
    if args["command"] == "submit":
        receipt = Path(__file__).resolve().with_name("workbench-maintenance-" + args["request_id"] + ".json")
        try:
            with receipt.open("x") as output:
                os.chmod(receipt, 0o600)
                json.dump(reserved, output, indent=2); output.write("\n"); output.flush(); os.fsync(output.fileno())
        except FileExistsError:
            parser.error("request ID already reserved locally; query instead")
    command = "python3 - --remote " + shlex.quote(json.dumps(args))
    try:
        ssh_args = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10"]
        if os.environ.get("MODELDEV_TENANTENV_SSH_CONTROL_PATH"):
            ssh_args += ["-S", os.environ["MODELDEV_TENANTENV_SSH_CONTROL_PATH"]]
        result = subprocess.run(ssh_args + [SSH, command],
                                input=Path(__file__).read_text(), capture_output=True, text=True, timeout=240)
        if result.returncode:
            raise Refusal("SSH process failed; stderr omitted")
        value = json.loads(result.stdout)
    except Exception as error:
        value = dict(reserved, reason=type(error).__name__) if receipt else {"status": "QUERY_FAILED", "reason": type(error).__name__}
    if receipt:
        temporary = receipt.with_suffix(".next")
        with temporary.open("w") as output:
            os.chmod(temporary, 0o600)
            json.dump(value, output, ensure_ascii=False, indent=2); output.write("\n"); output.flush(); os.fsync(output.fileno())
        temporary.replace(receipt)
    print(json.dumps(value, ensure_ascii=False, indent=2))
    if value.get("status") not in ("SUBMITTED", "QUERY"):
        sys.exit(2)


if __name__ == "__main__":
    main()

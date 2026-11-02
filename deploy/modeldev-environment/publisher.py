#!/usr/bin/env python3
"""Publish only the four fixed ani-system definitions; never create a Run.

The byte hash remains that of the original compiled YAML. KFP GetVersion
returns Struct, using api_util.go's pipeline_spec/platform_spec wrapper.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import ssl
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

NAMESPACE = "ani-system"
PROFILES = {"modeldev-mlp-cpu", "modeldev-mlp-gpu", "modeldev-sft-lora-cpu", "modeldev-sft-lora-gpu"}
TARGET = {"ssh_host": "100.64.128.12", "ssh_user": "kubercloud", "context": "kubernetes-admin@ani-kcn-full", "apiserver": "https://lb.kubesphere.local:6443", "cluster_id": "f39bf6e2-f3ed-4191-ada3-caeebf86c220", "kube_system_uid": "3eeb5379-8748-4bf8-8f6d-d7a48ce3bc24"}


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False).encode()


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def unique(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON field")
        result[key] = value
    return result


def read_json(path):
    raw = Path(path).read_bytes()
    if not raw or len(raw) > 1 << 20:
        raise ValueError("bounded JSON input required")
    return json.loads(raw, object_pairs_hook=unique)


def uuid_id(value):
    try:
        return isinstance(value, str) and str(uuid.UUID(value)) == value and int(uuid.UUID(value)) != 0
    except (ValueError, AttributeError):
        return False


def ir_struct(raw):
    import yaml
    class Loader(yaml.SafeLoader):
        pass
    def mapping(loader, node, deep=False):
        return unique(loader.construct_pairs(node, deep=deep))
    Loader.add_constructor(yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, mapping)
    docs = list(yaml.load_all(raw, Loader=Loader))
    if len(docs) != 2 or not isinstance(docs[0], dict) or not isinstance(docs[1], dict) or set(docs[1]) != {"platforms"} or set(docs[1]["platforms"]) != {"kubernetes"}:
        raise ValueError("exact KFP 2.16 pipeline and Kubernetes platform documents required")
    parameters = docs[0].get("root", {}).get("inputDefinitions", {}).get("parameters", {})
    if set(parameters) != {"execution_id", "spec_hash", "environment_binding_id", "namespace_uid"} or any(p != {"parameterType": "STRING"} for p in parameters.values()):
        raise ValueError("exact four required string root parameters required")
    return {"pipeline_spec": docs[0], "platform_spec": docs[1]}


class PublicationError(Exception):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        raise PublicationError("KFP redirect refused")


def verify_identity_receipt(path):
    identity=read_json(path)
    if any(identity.get(key)!=value for key,value in TARGET.items()) or type(identity.get("verified_at_unix")) not in (int,float) or not 0<=time.time()-identity["verified_at_unix"]<=60:
        raise ValueError("fresh complete designated cluster identity receipt required before writes")
    return identity


class HTTP:
    def __init__(self, endpoint, ca_file, token_file, identity_receipt=None):
        parsed = urllib.parse.urlsplit(endpoint)
        if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in ("", "/"):
            raise ValueError("explicit HTTPS KFP endpoint required")
        self.endpoint, self.token_file = endpoint.rstrip("/"), Path(token_file)
        self.identity_receipt=identity_receipt
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(), urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=ca_file)))

    def request(self, method, path, body=None, content_type="application/json"):
        if method != "GET":
            if self.identity_receipt is None:
                raise PublicationError("fresh designated cluster identity required for mutation")
            verify_identity_receipt(self.identity_receipt)
        token = self.token_file.read_bytes()
        if not token or len(token) > 16384 or not re.fullmatch(rb"[A-Za-z0-9._~+/-]+=*", token):
            raise PublicationError("projected publisher token unavailable")
        request = urllib.request.Request(self.endpoint + path, data=body, method=method, headers={"Authorization": "Bearer " + token.decode(), "Accept": "application/json", "Content-Type": content_type})
        try:
            with self.opener.open(request, timeout=5) as response:
                raw = response.read((1 << 20) + 1)
                if len(raw) > 1 << 20:
                    raise PublicationError("bounded KFP response exceeded")
                return json.loads(raw, object_pairs_hook=unique)
        except urllib.error.HTTPError as error:
            raise PublicationError("KFP request rejected with status " + str(error.code)) from None
        except (OSError, ValueError):
            raise PublicationError("KFP request outcome unavailable") from None


class Publisher:
    def __init__(self, transport, journal_file):
        self.transport, self.journal_file = transport, Path(journal_file)
        self.journal = read_json(self.journal_file) if self.journal_file.exists() else {}

    def checkpoint(self):
        temporary = self.journal_file.with_suffix(".next")
        with temporary.open("wb") as output:
            output.write(canonical(self.journal))
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, self.journal_file)
        directory = os.open(self.journal_file.parent, os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)

    def pages(self, path, field, query):
        rows, tokens, token = [], set(), ""
        for _ in range(10):
            parameters = {**query, "page_size": 100}
            if token:
                parameters["page_token"] = token
            page = self.transport.request("GET", path + "?" + urllib.parse.urlencode(parameters))
            items = page.get(field, [])
            if not isinstance(items, list) or len(items) > 100:
                raise PublicationError("invalid KFP page")
            rows.extend(items)
            token = page.get("next_page_token", "")
            if not token:
                return rows
            if not isinstance(token, str) or len(token) > 4096 or token in tokens:
                raise PublicationError("ambiguous KFP page cursor")
            tokens.add(token)
        raise PublicationError("KFP paging incomplete")

    def publish(self, profile):
        name = profile["id"]
        if name not in PROFILES:
            raise ValueError("only the four fixed profiles may be published")
        raw = Path(profile["ir_file"]).read_bytes()
        digest = sha(raw)
        if digest != profile["ir_sha256"]:
            raise PublicationError("original IR bytes changed")
        expected = ir_struct(raw)
        version_name = "ir-" + digest[:16]
        parent_description = canonical({"schema": "ani.modeldev.managed-pipeline.v1", "owner": "ani-modeldev-service", "namespace": NAMESPACE, "profile_id": name}).decode()
        version_description = canonical({"schema": "ani.modeldev.managed-pipeline-version.v1", "owner": "ani-modeldev-service", "profile_id": name, "ir_sha256": digest}).decode()
        intent = self.journal.setdefault(name, {"parent_description": parent_description, "version_description": version_description, "ir_sha256": digest})
        if intent["parent_description"] != parent_description or intent["version_description"] != version_description or intent["ir_sha256"] != digest:
            raise PublicationError("publication journal belongs to different immutable IR")
        parents = [p for p in self.pages("/apis/v2beta1/pipelines", "pipelines", {"namespace": NAMESPACE}) if p.get("name", p.get("display_name")) == name and p.get("namespace") == NAMESPACE]
        if len(parents) > 1:
            raise PublicationError("ambiguous fixed Pipeline")
        if not parents:
            if intent.get("parent_sent"):
                raise PublicationError("unknown Pipeline create outcome; observation only")
            intent["parent_sent"] = True
            self.checkpoint()
            self.transport.request("POST", "/apis/v2beta1/pipelines", canonical({"name": name, "display_name": name, "namespace": NAMESPACE, "description": parent_description}))
            parents = [p for p in self.pages("/apis/v2beta1/pipelines", "pipelines", {"namespace": NAMESPACE}) if p.get("name", p.get("display_name")) == name and p.get("namespace") == NAMESPACE]
            if len(parents) != 1:
                raise PublicationError("new Pipeline not uniquely readable")
        parent = parents[0]
        parent_id = parent.get("pipeline_id")
        if not uuid_id(parent_id) or intent.get("pipeline_id", parent_id) != parent_id:
            raise PublicationError("original Pipeline identity changed")
        parent = self.transport.request("GET", "/apis/v2beta1/pipelines/" + parent_id)
        if parent.get("namespace") != NAMESPACE or parent.get("name", parent.get("display_name")) != name or parent.get("description") != parent_description or parent.get("error"):
            raise PublicationError("foreign fixed Pipeline conflict")
        intent["pipeline_id"] = parent_id
        self.checkpoint()
        path = "/apis/v2beta1/pipelines/" + parent_id + "/versions"
        versions = [v for v in self.pages(path, "pipeline_versions", {}) if v.get("name", v.get("display_name")) == version_name]
        if len(versions) > 1:
            raise PublicationError("ambiguous fixed PipelineVersion")
        if not versions:
            if intent.get("version_sent"):
                raise PublicationError("unknown PipelineVersion create outcome; observation only")
            intent["version_sent"] = True
            self.checkpoint()
            boundary = "modeldev-" + digest
            body = ("--" + boundary + '\r\nContent-Disposition: form-data; name="uploadfile"; filename="pipeline.yaml"\r\nContent-Type: application/yaml\r\n\r\n').encode() + raw + ("\r\n--" + boundary + "--\r\n").encode()
            query = urllib.parse.urlencode({"pipelineid": parent_id, "name": version_name, "display_name": version_name, "description": version_description})
            self.transport.request("POST", "/apis/v2beta1/pipelines/upload_version?" + query, body, "multipart/form-data; boundary=" + boundary)
            versions = [v for v in self.pages(path, "pipeline_versions", {}) if v.get("name", v.get("display_name")) == version_name]
            if len(versions) != 1:
                raise PublicationError("new PipelineVersion not uniquely readable")
        version_id = versions[0].get("pipeline_version_id")
        if not uuid_id(version_id) or intent.get("version_id", version_id) != version_id:
            raise PublicationError("original PipelineVersion identity changed")
        version = self.transport.request("GET", path + "/" + version_id)
        if version.get("pipeline_id") != parent_id or version.get("name", version.get("display_name")) != version_name or version.get("description") != version_description or version.get("error") or version.get("pipeline_spec") != expected:
            raise PublicationError("PipelineVersion original byte digest or structure conflict")
        intent["version_id"], intent["confirmed"] = version_id, True
        self.checkpoint()
        return {**profile, "pipeline_id": parent_id, "version_id": version_id, "namespace": NAMESPACE, "version_name": version_name, "phase": "CONFIRMED"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--endpoint", required=True)
    parser.add_argument("--ca-file", required=True)
    parser.add_argument("--token-file", required=True)
    parser.add_argument("--identity-receipt", type=Path, required=True)
    parser.add_argument("--journal", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    verify_identity_receipt(args.identity_receipt)
    manifest = read_json(args.manifest)
    if {p["id"] for p in manifest["profiles"]} != PROFILES or len(manifest["profiles"]) != 4:
        raise ValueError("exactly four profiles required")
    import fcntl
    with args.journal.with_suffix(".lock").open("w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        publisher = Publisher(HTTP(args.endpoint, args.ca_file, args.token_file,args.identity_receipt), args.journal)
        published = {**manifest, "profiles": [publisher.publish(p) for p in sorted(manifest["profiles"], key=lambda p: p["id"])]}
        with args.output.open("xb") as output:
            output.write(canonical(published))


if __name__ == "__main__":
    main()

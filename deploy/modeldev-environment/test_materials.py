"""Only external KFP boundaries are doubled; use pinned compiler tests separately."""
import copy
import json
from pathlib import Path
import tempfile
import unittest
import urllib.parse
from publisher import Publisher, PublicationError, PROFILES, canonical, ir_struct, sha
from bundle import PROFILE_CONTRACT, seal, prepare
from render import render

PARENT = "11111111-1111-4111-8111-111111111111"
VERSION = "22222222-2222-4222-8222-222222222222"
IMAGE = "registry.example.test/platform/service@sha256:" + "a" * 64
IR = b'''pipelineInfo: {name: fixed-fixture}
root:
  inputDefinitions:
    parameters:
      execution_id: {parameterType: STRING}
      spec_hash: {parameterType: STRING}
      environment_binding_id: {parameterType: STRING}
      namespace_uid: {parameterType: STRING}
---
platforms: {kubernetes: {deploymentSpec: {executors: {}}}}
'''


class KFP:
    def __init__(self, raw):
        self.parent, self.version, self.raw = None, None, raw
        self.parent_posts = self.version_posts = 0
        self.lose = None

    def request(self, method, path, body=None, content_type=None):
        url = urllib.parse.urlsplit(path)
        query = urllib.parse.parse_qs(url.query)
        if method == "GET" and url.path == "/apis/v2beta1/pipelines":
            return {"pipelines": [self.parent] if self.parent else []}
        if method == "GET" and url.path == "/apis/v2beta1/pipelines/" + PARENT:
            return copy.deepcopy(self.parent)
        if method == "POST" and url.path == "/apis/v2beta1/pipelines":
            self.parent_posts += 1
            if self.lose != "parent-not-created":
                self.parent = {**json.loads(body), "pipeline_id": PARENT}
            if self.lose in {"parent", "parent-not-created"}:
                raise PublicationError("lost external parent response")
            return self.parent
        if method == "GET" and url.path.endswith("/versions"):
            return {"pipeline_versions": [self.version] if self.version else []}
        if method == "POST" and url.path.endswith("/upload_version"):
            self.version_posts += 1
            self.version = {"pipeline_id": PARENT, "pipeline_version_id": VERSION, "name": query["name"][0], "description": query["description"][0], "pipeline_spec": ir_struct(self.raw)}
            if self.lose == "version":
                raise PublicationError("lost external version response")
            return self.version
        if method == "GET" and url.path.endswith("/versions/" + VERSION):
            return copy.deepcopy(self.version)
        raise AssertionError((method, path))


class Publication(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.addCleanup(self.temporary.cleanup)
        self.ir = self.root / "pipeline.yaml"
        self.ir.write_bytes(IR)
        self.profile = {"id": "modeldev-mlp-cpu", "ir_file": str(self.ir), "ir_sha256": sha(IR)}

    def test_exact_readback_and_idempotent_reuse(self):
        boundary = KFP(IR)
        publisher = Publisher(boundary, self.root / "journal.json")
        result = publisher.publish(self.profile)
        self.assertEqual(result["namespace"], "ani-system")
        self.assertEqual(result["version_name"], "ir-" + sha(IR)[:16])
        self.assertEqual(publisher.publish(self.profile), result)
        self.assertEqual((boundary.parent_posts, boundary.version_posts), (1, 1))
        boundary.version["pipeline_spec"]["pipeline_spec"]["root"]["extra"] = True
        with self.assertRaises(PublicationError):
            publisher.publish(self.profile)
        self.assertEqual(boundary.version_posts, 1)

    def test_lost_parent_and_version_resolve_original_without_repost(self):
        for lose in ("parent", "version"):
            with self.subTest(lose=lose):
                boundary = KFP(IR)
                boundary.lose = lose
                journal = self.root / (lose + ".json")
                with self.assertRaises(PublicationError):
                    Publisher(boundary, journal).publish(self.profile)
                boundary.lose = None
                result = Publisher(boundary, journal).publish(self.profile)
                self.assertEqual(result["version_id"], VERSION)
                self.assertEqual((boundary.parent_posts, boundary.version_posts), (1, 1))

    def test_unknown_zero_result_never_reposts_and_foreign_owner_conflicts(self):
        boundary = KFP(IR)
        boundary.lose = "parent-not-created"
        journal = self.root / "unknown.json"
        with self.assertRaises(PublicationError):
            Publisher(boundary, journal).publish(self.profile)
        boundary.lose = None
        with self.assertRaises(PublicationError):
            Publisher(boundary, journal).publish(self.profile)
        self.assertEqual(boundary.parent_posts, 1)
        foreign = KFP(IR)
        foreign.parent = {"pipeline_id": PARENT, "name": self.profile["id"], "namespace": "ani-system", "description": "foreign-owner"}
        with self.assertRaises(PublicationError):
            Publisher(foreign, self.root / "foreign.json").publish(self.profile)
        self.assertEqual((foreign.parent_posts, foreign.version_posts), (0, 0))

    def test_bad_roots_duplicate_yaml_and_same_short_name_wrong_digest_reject(self):
        for raw in (IR.replace(b"      namespace_uid: {parameterType: STRING}\n", b""), IR.replace(b"root:\n", b"root: {}\nroot:\n")):
            with self.assertRaises(ValueError):
                ir_struct(raw)
        boundary = KFP(IR)
        publisher = Publisher(boundary, self.root / "digest.json")
        publisher.publish(self.profile)
        boundary.version["description"] = boundary.version["description"].replace(sha(IR), "b" * 64)
        with self.assertRaises(PublicationError):
            publisher.publish(self.profile)
        self.assertEqual(boundary.version_posts, 1)

    def test_prepare_compiles_the_exact_four_fixed_profiles(self):
        import importlib.util
        fixture_file = Path(__file__).resolve().parents[2] / "pipelines/resource_fixture.py"
        spec = importlib.util.spec_from_file_location("material_resource_fixture", fixture_file)
        resources = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(resources)
        config = {"source_commit": "a" * 40, "source_tree": "b" * 40, "registry_authority": "registry.example.test", "platform_project": "platform", "anonymous_images": [], "support_images": {"driver": IMAGE, "launcher": IMAGE, "material_copy": IMAGE}, "profiles": []}
        for name in sorted(PROFILES):
            kind = "GENERAL_TRAINING" if PROFILE_CONTRACT[name][0] == "builtin.mlp" else "FINETUNING"
            compile_config = {"kind": kind, "component_image": IMAGE, "owner_config_map": "modeldev-step-owner", "storage_class": "workspace-fixture", "workspace_size": "2Gi", "workspace_access_mode": "ReadWriteOnce", "admission_profile": name, "execution_timeout_max_seconds": 7200, "component_resources": resources.component_resources(kind)}
            path = self.root / (name + "-compile.json")
            path.write_bytes(canonical(compile_config))
            config["profiles"].append({"id": name, "compile_config": str(path)})
        result = prepare(config, self.root / "compiled", Path(__file__).resolve().parents[2] / "pipelines/cpu_p01.py")
        self.assertEqual({p["id"] for p in result["profiles"]}, PROFILES)
        self.assertEqual(len({p["ir_sha256"] for p in result["profiles"]}), 4)
        for profile in result["profiles"]:
            self.assertEqual(sha(Path(profile["ir_file"]).read_bytes()), profile["ir_sha256"])
        config["profiles"][0]["id"] = "arbitrary-pipeline"
        with self.assertRaises(ValueError):
            prepare(config, self.root / "forbidden", Path("not-reached"))

    def test_immutable_bundle_shared_by_service_webhook_and_split_audiences(self):
        published = {"source_commit": "a" * 40, "source_tree": "b" * 40, "registry_authority": "registry.example.test", "platform_project": "platform", "anonymous_images": [], "support_images": {"driver": IMAGE, "launcher": IMAGE, "material_copy": IMAGE}, "profiles": []}
        materials = {}
        for name in sorted(PROFILES):
            published["profiles"].append({**self.profile, "id": name, "pipeline_id": PARENT, "version_id": VERSION, "namespace": "ani-system", "phase": "CONFIRMED"})
            material = {"implementation": {"implementation_id": PROFILE_CONTRACT[name][0], "pipeline_id": PARENT, "pipeline_version_id": VERSION, "pipeline_ir_sha256": sha(IR)}}
            path = self.root / (name + ".json")
            path.write_bytes(canonical(material))
            materials[name] = str(path)
        destination = self.root / "sealed"
        manifest = seal(published, materials, destination)
        digest = sha((destination / "manifest.json").read_bytes())
        platform = {"material_bundle_digest": digest, "profiles": [{k: p[k] for k in ("id", "pipeline_id", "version_id", "ir_sha256", "material_sha256")} for p in manifest["profiles"]], "ir_sources": [{"profile_id": p["id"], "file": p["ir_file"], "sha256": p["ir_sha256"]} for p in manifest["profiles"]], "kfp_token_file": "/var/run/modeldev-kfp/token", "artifact_access_key_file": "/var/run/modeldev-kfp-artifact-credentials/access_key", "artifact_secret_key_file": "/var/run/modeldev-kfp-artifact-credentials/secret_key"}
        objects = render(destination, platform, IMAGE, IMAGE, "platform-kfp-artifact-source", "modeldev", "webhook")
        self.assertTrue(objects["materials.configmap.json"]["immutable"])
        service = objects["service.patch.json"]["spec"]["template"]
        webhook = objects["webhook-materials.patch.json"]["spec"]["template"]
        self.assertIs(service["spec"]["enableServiceLinks"], False)
        self.assertNotIn("enableServiceLinks", webhook["spec"])
        self.assertEqual(service["metadata"]["annotations"], webhook["metadata"]["annotations"])
        self.assertEqual(service["metadata"]["annotations"], {"app.kubernetes.io/material-bundle-sha256": digest})
        for template in (service, webhook):
            self.assertFalse(any(key.startswith(("ani.modeldev/", "modeldev.ani.io/")) for key in template["metadata"]["annotations"]))
        self.assertNotIn("subPath", json.dumps(objects))
        audiences = {source["serviceAccountToken"]["audience"] for volume in service["spec"]["volumes"] if "projected" in volume for source in volume["projected"]["sources"] if "serviceAccountToken" in source}
        self.assertEqual(audiences, {"pipelines.kubeflow.org", "https://kubernetes.default.svc.cluster.local"})
        self.assertNotIn("projected", json.dumps(webhook))
        self.assertEqual(objects["install-order.json"]["live_status"], "NOT_RUN")
        for name in ("service.patch.json","webhook-materials.patch.json"):
            initializer=objects[name]["spec"]["template"]["spec"]["initContainers"][0]
            self.assertEqual(initializer["command"],["/usr/local/bin/ani-modeldev-webhook","--copy-materials-from","/source","--copy-materials-to","/materials","--copy-materials-manifest-sha256",digest])
        platform_init=service["spec"]["initContainers"][1]
        self.assertEqual(platform_init["command"],["/usr/local/bin/ani-modeldev-webhook","--copy-platform-from","/source/platform.json","--copy-platform-to","/platform/platform.json","--copy-platform-sha256",sha(canonical(platform))])
        self.assertEqual([v for v in service["spec"]["volumes"] if v["name"]=="modeldev-platform-config"],[{"name":"modeldev-platform-config","emptyDir":{}}])
        platform["access_key"] = "forbidden-inline-value"
        with self.assertRaises(ValueError):
            render(destination, platform, IMAGE, IMAGE, "platform-kfp-artifact-source", "modeldev", "webhook")


if __name__ == "__main__":
    unittest.main()

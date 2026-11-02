"""Deployment shape only; actual CEL enforcement is a separate API-server gate."""
import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
import render


class ManifestTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        root = Path(directory.name)
        material = root / "builtin.mlp.cpu.json"
        material.write_bytes(b'{"fixture":"validated-by-Go-before-render"}')
        manifest = root / "manifest.json"
        manifest.write_bytes(b'{"fixture":"same-shared-bundle"}')
        self.config = {"schema_version":"ani.modeldev.webhook.v2","driver":{"image":"frozen"},"material_bundle_file":str(manifest),"material_bundle_digest":hashlib.sha256(manifest.read_bytes()).hexdigest(),"profiles":[{"id":"modeldev-mlp-cpu","material_file":str(material),"material_sha256":hashlib.sha256(material.read_bytes()).hexdigest()}]}

    def call(self, config=None):
        return render.render(config or self.config,"ani-system","registry.example.test/platform/webhook@sha256:"+"a"*64,"operator-issuer","Issuer",["172.16.101.10/32"],"10.96.0.1/32",6443,443,platform_principals=["system:serviceaccount:kube-system:verified-platform"])

    def test_single_global_or_match_and_no_creator_skip(self):
        support, registration = self.call()
        self.assertEqual(len(registration["webhooks"]),1)
        hook=registration["webhooks"][0]
        self.assertNotIn("namespaceSelector",hook)
        self.assertNotIn("objectSelector",hook)
        expression=hook["matchConditions"][0]["expression"]
        for text in ["modeldev-step","trainer-workload","verifier","ani.modeldev/","modeldev.ani.io/","ani-kfp-workspace-","modeldev-kfp-artifacts","projected","initContainers","ephemeralContainers","envFrom"]:
            self.assertIn(text,expression)
        self.assertNotIn("request.userInfo",expression)
        self.assertEqual(hook["rules"][0]["operations"],["CREATE"])
        self.assertEqual(hook["failurePolicy"],"Fail")
        self.assertFalse(any(row["kind"]=="MutatingWebhookConfiguration" for row in support["items"]))

    def test_shared_bundle_init_regular_files_and_no_tenant_roles(self):
        support,_=self.call()
        self.assertFalse(any(row["kind"] in {"Role","RoleBinding","ClusterRole","ClusterRoleBinding","Secret"} for row in support["items"]))
        deployment=next(r for r in support["items"] if r["kind"]=="Deployment")
        pod=deployment["spec"]["template"]["spec"]
        self.assertEqual(deployment["spec"]["replicas"],2)
        annotations=deployment["spec"]["template"]["metadata"]["annotations"]
        config_map=next(r for r in support["items"] if r["kind"]=="ConfigMap")
        expected=hashlib.sha256(json.dumps(config_map["data"],sort_keys=True,separators=(",",":")).encode()).hexdigest()
        self.assertEqual(annotations,{"app.kubernetes.io/config-sha256":expected})
        self.assertFalse(any(key.startswith(("ani.modeldev/","modeldev.ani.io/")) for key in annotations))
        self.assertIn("--copy-materials-manifest-sha256",pod["initContainers"][0]["command"])
        self.assertEqual(pod["initContainers"][0]["image"],pod["containers"][0]["image"])
        source=next(v for v in pod["volumes"] if v["name"]=="material-source")
        self.assertEqual(source["configMap"]["name"],"modeldev-materials-"+self.config["material_bundle_digest"][:12])
        config_map=next(r for r in support["items"] if r["kind"]=="ConfigMap")
        self.assertEqual(set(config_map["data"]),{"config.json"})
        config=json.loads(config_map["data"]["config.json"])
        self.assertEqual(config["material_bundle_file"],"/etc/modeldev-materials/manifest.json")
        self.assertTrue(config_map["immutable"])

    def test_changed_material_or_missing_driver_rejected(self):
        for mutate in [lambda c:c.pop("driver"),lambda c:c["profiles"][0].update(material_sha256="0"*64),lambda c:c.update(material_bundle_digest="0"*64)]:
            config=copy.deepcopy(self.config);mutate(config)
            with self.assertRaises(ValueError):
                self.call(config)


if __name__=="__main__":
    unittest.main()

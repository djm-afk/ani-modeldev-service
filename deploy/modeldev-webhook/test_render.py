"""Offline deployment-boundary tests; no Kubernetes or Pod policy substitute."""
import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
import render


class ManifestTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        # render() accepts only material already validated by the real Go CLI.
        # This minimal byte fixture tests packaging/selection, not Release codec.
        path = Path(self.directory.name) / "release.json"
        path.write_bytes(b'{"fixture":"deployment-shape-only"}')
        self.config = {"schema_version": "ani.modeldev.webhook.v1", "bindings": [{"namespace": "test-tenant", "namespace_uid": "test-uid", "owner_label": {"key": "ani.io/managed-by", "value": "test-owner"}, "driver": {"image": "frozen"}}], "profiles": [{"id": "test-profile", "material_file": str(path), "material_sha256": hashlib.sha256(path.read_bytes()).hexdigest()}]}

    def call(self, config=None):
        return render.render(config or self.config, "test-system", "example.invalid/webhook@sha256:"+"a"*64, "operator-issuer", "Issuer", ["172.16.101.10/32"], "10.96.0.1/32", 16443, 1443)

    def test_registration_separate_and_missing_profile_still_selected(self):
        support, registration = self.call()
        self.assertFalse(any(row["kind"] == "MutatingWebhookConfiguration" for row in support["items"]))
        hook = registration["webhooks"][0]
        self.assertEqual(hook["objectSelector"], {"matchLabels": {"pipelines.kubeflow.org/v2_component": "true"}})
        self.assertEqual(hook["namespaceSelector"]["matchLabels"], {"kubernetes.io/metadata.name": "test-tenant", "ani.io/managed-by": "test-owner"})
        self.assertEqual(hook["rules"][0]["operations"], ["CREATE"])
        self.assertEqual(hook["failurePolicy"], "Fail")
        self.assertEqual(registration["metadata"]["annotations"]["cert-manager.io/inject-ca-from"], "test-system/ani-modeldev-webhook-serving")

    def test_least_privilege_renewal_and_two_replicas(self):
        support, _ = self.call()
        rows = {row["kind"]: row for row in support["items"]}
        self.assertEqual(rows["ClusterRole"]["rules"], [{"apiGroups": [""], "resources": ["namespaces"], "resourceNames": ["test-tenant"], "verbs": ["get"]}])
        self.assertEqual(rows["Role"]["rules"][0]["verbs"], ["get"])
        self.assertEqual(rows["Certificate"]["spec"]["privateKey"]["rotationPolicy"], "Always")
        self.assertEqual(rows["Deployment"]["spec"]["replicas"], 2)
        self.assertEqual(rows["Deployment"]["spec"]["strategy"]["rollingUpdate"]["maxUnavailable"], 0)
        egress = rows["NetworkPolicy"]["spec"]["egress"]
        self.assertEqual(egress[0]["ports"], [{"protocol": "TCP", "port": 16443}])
        self.assertEqual(egress[1]["ports"], [{"protocol": "TCP", "port": 1443}])
        cm = rows["ConfigMap"]
        self.assertTrue(cm["immutable"])
        config = json.loads(cm["data"]["config.json"])
        self.assertEqual(config["profiles"][0]["material_file"], "/etc/modeldev-webhook/materials/test-profile.json")
        for row in support["items"]:
            self.assertNotEqual(row["kind"], "Secret", "renderer must not generate or copy private keys")

    def test_missing_driver_or_changed_release_rejected(self):
        for change in (lambda c: c["bindings"][0].pop("driver"), lambda c: c["profiles"][0].update(material_sha256="0"*64)):
            value = copy.deepcopy(self.config); change(value)
            with self.assertRaises(ValueError):
                self.call(value)
        for api_port, service_port in ((0, 1443), (16443, 65536)):
            with self.assertRaises(ValueError):
                render.render(self.config, "test-system", "example.invalid/webhook@sha256:"+"a"*64, "operator-issuer", "Issuer", ["172.16.101.10/32"], "10.96.0.1/32", api_port, service_port)

    def test_training_pods_cannot_escape_by_omitting_kfp_label(self):
        self.config["bindings"][0]["training"] = {"service_account": "trainer-worker", "creator": "system:serviceaccount:kube-system:job-controller"}
        support, registration = self.call()
        self.assertNotIn("objectSelector", registration["webhooks"][0])
        role = next(row for row in support["items"] if row["kind"] == "Role")
        self.assertEqual({resource for rule in role["rules"] for resource in rule["resources"]}, {"workflows", "jobs", "jobsets", "trainjobs", "trainingruntimes"})
        self.assertTrue(all(rule["verbs"] == ["get"] for rule in role["rules"]))


if __name__ == "__main__":
    unittest.main()

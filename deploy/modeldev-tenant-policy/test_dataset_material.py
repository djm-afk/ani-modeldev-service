"""Dataset material renderer contract checks."""
import importlib.util
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location(
    "dataset_material", Path(__file__).with_name("dataset-material.py"))
dataset_material = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dataset_material)

TENANT = "cddce187-2949-410c-90ef-b2d5cf41427e"
NAMESPACE_UID = "0d1d381a-4e10-49fb-b305-20d433a6f023"
CA = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"


class DatasetMaterial(unittest.TestCase):
    def test_material_is_indexed_by_namespace_not_registry(self):
        document = dataset_material.render(TENANT, NAMESPACE_UID, CA)
        self.assertEqual(document["kind"], "List")
        account, ca = document["items"]
        self.assertEqual(account["kind"], "ServiceAccount")
        self.assertEqual(account["metadata"]["name"], "modeldev-dataset-importer")
        self.assertEqual(account["metadata"]["namespace"], "tenant-" + TENANT)
        labels = account["metadata"]["labels"]
        self.assertEqual(labels["ani.io/managed-by"], "ani-modeldev-service")
        self.assertEqual(labels["modeldev.ani.io/purpose"], "dataset-import")
        self.assertEqual(labels["modeldev.ani.io/tenant-id"], TENANT)
        self.assertNotIn("modeldev.ani.io/environment-id", labels)
        self.assertEqual(account["metadata"]["annotations"],
                         {"modeldev.ani.io/namespace-uid": NAMESPACE_UID})
        self.assertFalse(account["automountServiceAccountToken"])
        self.assertEqual(account["imagePullSecrets"], [])

        self.assertEqual(ca["kind"], "ConfigMap")
        self.assertEqual(ca["metadata"]["name"], "modeldev-dataset-import-ca")
        self.assertEqual(ca["metadata"]["labels"], labels)
        self.assertTrue(ca["immutable"])
        self.assertEqual(ca["data"], {"s3-ca.pem": CA})

    def test_invalid_identity_is_refused(self):
        for tenant, uid in ((TENANT.upper(), NAMESPACE_UID),
                            ("00000000-0000-0000-0000-000000000000", NAMESPACE_UID),
                            (TENANT, ""), (TENANT, "not-a-uuid")):
            with self.assertRaises(ValueError):
                dataset_material.render(tenant, uid, CA)

    def test_missing_ca_is_refused(self):
        for ca in ("", "   ", None):
            with self.assertRaises(ValueError):
                dataset_material.render(TENANT, NAMESPACE_UID, ca)


if __name__ == "__main__":
    unittest.main()

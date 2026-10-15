#!/usr/bin/env python3
"""Task control identity can remove steps without deleting retained artifacts."""
from pathlib import Path
import tempfile
import unittest

from deploy import SYSTEM, TENANT, foundation


class FoundationPermissions(unittest.TestCase):
    def test_step_can_read_only_its_frozen_namespace(self):
        with tempfile.TemporaryDirectory(prefix="foundation-step-namespace-") as temporary:
            objects = foundation(Path(temporary))
        name = "finetuning-core-flow-20261014-step-namespace"
        role = next((value for value in objects if value["kind"] == "ClusterRole" and value["metadata"]["name"] == name), None)
        binding = next((value for value in objects if value["kind"] == "ClusterRoleBinding" and value["metadata"]["name"] == name), None)
        self.assertIsNotNone(role)
        self.assertIsNotNone(binding)
        self.assertEqual(role["rules"], [{"apiGroups": [""], "resources": ["namespaces"], "resourceNames": [TENANT], "verbs": ["get"]}])
        self.assertEqual(binding["roleRef"], {"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": name})
        self.assertEqual(binding["subjects"], [{"kind": "ServiceAccount", "name": "modeldev-step", "namespace": TENANT}])

    def test_control_role_authorizes_compute_cleanup_only(self):
        with tempfile.TemporaryDirectory(prefix="foundation-rbac-") as temporary:
            objects = foundation(Path(temporary))
        role = next(value for value in objects if value["kind"] == "Role" and value["metadata"]["name"] == "modeldev-control")
        binding = next(value for value in objects if value["kind"] == "RoleBinding" and value["metadata"]["name"] == "modeldev-control")
        self.assertEqual(role["metadata"]["namespace"], TENANT)
        self.assertEqual(binding["metadata"]["namespace"], TENANT)
        self.assertEqual(binding["subjects"], [{"kind": "ServiceAccount", "name": "modeldev-control", "namespace": SYSTEM}])
        self.assertEqual(binding["roleRef"], {"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": role["metadata"]["name"]})

        def granted(verb, group, resource):
            return any(
                (verb in rule["verbs"] or "*" in rule["verbs"])
                and (group in rule["apiGroups"] or "*" in rule["apiGroups"])
                and (resource in rule["resources"] or "*" in rule["resources"])
                for rule in role["rules"]
            )

        self.assertTrue(granted("patch", "", "pods"))
        for group, resource in [
            ("", "pods"),
            ("batch", "jobs"), ("jobset.x-k8s.io", "jobsets"),
            ("trainer.kubeflow.org", "trainjobs"), ("argoproj.io", "workflows"),
        ]:
            with self.subTest(group=group, resource=resource):
                self.assertTrue(granted("delete", group, resource))
                self.assertFalse(granted("deletecollection", group, resource))
        for group, resource in [
            ("", "persistentvolumeclaims"), ("", "secrets"),
        ]:
            with self.subTest(group=group, resource=resource):
                self.assertFalse(granted("delete", group, resource))


if __name__ == "__main__":
    unittest.main()

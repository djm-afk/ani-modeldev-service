"""Fedora behavioral tests for the task-only Pod mutation boundary."""
import base64
import copy
import json
import unittest
import admission

IMAGE = "example.invalid/component@sha256:" + "a" * 64
RUN = "5dc1e8a2-b890-4f41-bba0-c3f9b1370177"
WORKFLOW = "general-cpu-test"
UID = "2d9c812d-c6ec-4fa4-8e6b-e14b5a0a0727"


def identities(_):
    return ({"metadata": {"uid": admission.NAMESPACE_UID, "labels": {"ani.io/managed-by": admission.OWNER}}}, {"metadata": {"name": WORKFLOW, "namespace": admission.NAMESPACE, "uid": UID, "labels": {"pipeline/runid": RUN}}})


def body(role):
    expected = admission.ROLE_MOUNTS[role]
    mounts = [{"name": "workspace", "mountPath": path, **({"subPath": value[0]} if value[0] else {})} for path, value in expected.items()]
    spec = {"serviceAccountName": "modeldev-step", "containers": [{"name": "main", "image": IMAGE, "volumeMounts": mounts, "resources": {"requests": {"cpu": "2"}, "limits": {"cpu": "4"}}}, {"name": "wait", "image": IMAGE}], "initContainers": [{"name": "init", "image": IMAGE}], "volumes": [{"name": "workspace", "persistentVolumeClaim": {"claimName": "ani-kfp-workspace-test"}}] if expected else []}
    return {"apiVersion": "admission.k8s.io/v1", "kind": "AdmissionReview", "request": {"uid": "549b5d38-739c-4bb7-8eb6-e262b945b1ed", "namespace": admission.NAMESPACE, "operation": "CREATE", "resource": {"group": "", "version": "v1", "resource": "pods"}, "userInfo": {"username": admission.CONTROLLER}, "object": {"apiVersion": "v1", "kind": "Pod", "metadata": {"namespace": admission.NAMESPACE, "labels": {"pipelines.kubeflow.org/v2_component": "true", "ani.modeldev/step-role": role, "workflows.argoproj.io/workflow": WORKFLOW, "pipeline/runid": RUN}, "ownerReferences": [{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "name": WORKFLOW, "uid": UID, "controller": True}]}, "spec": spec}}}


class AdmissionTests(unittest.TestCase):
    def call(self, value, reader=identities):
        return admission.review(value, {"images": [IMAGE], "component_image": IMAGE}, reader)["response"]

    def test_finite_roles_keep_cpu_and_apply_readonly_contract(self):
        for role, wanted in admission.ROLE_MOUNTS.items():
            with self.subTest(role=role):
                response = self.call(body(role))
                self.assertTrue(response["allowed"])
                patches = json.loads(base64.b64decode(response["patch"]))
                spec = patches[0]["value"]
                self.assertEqual(spec["securityContext"]["fsGroup"], 10001)
                self.assertEqual(patches[1]["value"], [admission.FINALIZER])
                main = spec["containers"][0]
                self.assertEqual(main["resources"]["requests"], {"cpu": "2", "ephemeral-storage": "10Gi"})
                self.assertEqual(main["resources"]["limits"], {"cpu": "4", "ephemeral-storage": "12Gi"})
                self.assertTrue(all(c["securityContext"]["readOnlyRootFilesystem"] for c in [*spec["containers"], *spec["initContainers"]]))
                self.assertTrue(all(c["securityContext"]["runAsUser"] == 10001 and c["securityContext"]["runAsGroup"] == 10001 for c in [*spec["containers"], *spec["initContainers"]]))
                self.assertEqual({m["mountPath"]: m["readOnly"] for m in main["volumeMounts"]}, {p: x[1] for p, x in wanted.items()})

    def test_wrong_namespace_uid_workflow_owner_role_image_mount_refused(self):
        bad_reader = lambda _: ({"metadata": {"uid": UID, "labels": {"ani.io/managed-by": admission.OWNER}}}, identities("")[1])
        self.assertFalse(self.call(body("export"), bad_reader)["allowed"])
        for mutate in (
            lambda p: p["metadata"]["ownerReferences"][0].update(uid=RUN),
            lambda p: p["metadata"]["labels"].update({"ani.modeldev/step-role": "shell"}),
            lambda p: p["spec"]["containers"][0].update(image="mutable:tag"),
            lambda p: p["spec"]["containers"][0]["volumeMounts"][0].update(subPath="publication"),
            lambda p: p["spec"].update(ephemeralContainers=[{}]),
        ):
            value = body("export")
            mutate(value["request"]["object"])
            self.assertFalse(self.call(value)["allowed"])

    def test_other_namespace_or_update_unchanged_and_replay_finalizer_unique(self):
        for field, value in (("namespace", "ani-system"), ("operation", "UPDATE")):
            request = body("collect")
            request["request"][field] = value
            response = self.call(request, lambda _: self.fail("out-of-scope read"))
            self.assertTrue(response["allowed"])
            self.assertNotIn("patch", response)
        request = body("collect")
        request["request"]["object"]["metadata"]["finalizers"] = [admission.FINALIZER]
        response = self.call(request)
        patch = json.loads(base64.b64decode(response["patch"]))
        self.assertEqual(patch[1]["value"], [admission.FINALIZER])


if __name__ == "__main__":
    unittest.main()

"""Finite template contract checks; no claim of real API-server enforcement."""
import copy
import unittest
import render


def config():
    p={name:"system:serviceaccount:platform:"+name.replace("_","-") for name in ["control","webhook","kfp_api","argo","trainer","jobset","job_controller","persistence_agent","dataset_control"]}
    p.update(control="system:serviceaccount:ani-system:modeldev-control",webhook="system:serviceaccount:ani-system:ani-modeldev-webhook",maintenance="verified-maintenance")
    return {"principals":p,"node_principals":["system:node:verified-node"],"kfp_ca_key":"ca.crt","network_metadata_writer":"system:serviceaccount:kcn-system:kcn-controller","network_status_writer":"system:serviceaccount:kube-system:ani-multus","gc_finalizer_writer":{"username":"system:serviceaccount:kube-system:generic-garbage-collector","uid":"33333333-3333-4333-8333-333333333333"},"scheduler_writer":{"username":"system:serviceaccount:volcano-system:ani-volcano-scheduler","uid":"44444444-4444-4444-8444-444444444444"},"device_plugin_writer":{"username":"system:serviceaccount:kube-system:volcano-device-plugin","uid":"55555555-5555-4555-8555-555555555555"}}


class NativeTemplates(unittest.TestCase):
    def test_exact_policies_old_object_and_global_token_secret_guard(self):
        document=render.render(config())
        policies={o["metadata"]["name"]:o for o in document["items"] if o["kind"]=="ValidatingAdmissionPolicy"}
        self.assertEqual(set(policies),set(render.NAMES))
        for policy in policies.values():
            self.assertEqual(policy["spec"]["failurePolicy"],"Fail")
            # Empty selectors are load-bearing: omitting them makes the API
            # server evaluate namespaceObject as null, so `protected` errors and
            # failurePolicy=Fail denies every covered request cluster-wide.
            self.assertEqual(policy["spec"]["matchConstraints"]["namespaceSelector"],{})
            self.assertEqual(policy["spec"]["matchConstraints"]["objectSelector"],{})
        configuration=str(policies[render.NAMES[1]])
        for word in ["kubernetes.io/service-account-token","oldObject","modeldev-execution-environment","modeldev-kfp-artifacts"]:
            self.assertIn(word,configuration)
        workload=str(policies[render.NAMES[2]])
        for word in ["spec.templates","spec.arguments","activeDeadlineSeconds","== 0","pods/ephemeralcontainers","modeldev.ani.io/","system:node:verified-node"]:
            self.assertIn(word,workload)
        self.assertNotIn("spec.shutdown",workload)
        self.assertNotIn("modeldev.ani.io/namespace-uid' in namespaceObject",workload)

    def test_no_runtime_privilege_expansion(self):
        rows=render.render(config())["items"]
        roles={o["metadata"]["name"]:o for o in rows if o["kind"]=="ClusterRole"}
        control=roles["modeldev-environment-control-v1"]["rules"]
        self.assertEqual(next(r for r in control if r["resources"]==["namespaces"])["verbs"],["get"])
        bind=next(r for r in control if "bind" in r["verbs"])
        self.assertEqual(set(bind["resourceNames"]),{n+"-v1" for n in render.RB})
        for role in roles.values():
            for rule in role["rules"]:
                self.assertNotIn("*",rule["verbs"])
                self.assertNotIn("*",rule["resources"])
                self.assertFalse(set(rule["verbs"]) & {"escalate","impersonate"})
                self.assertNotIn("serviceaccounts/token",rule["resources"])
                self.assertNotIn("subjectaccessreviews",rule["resources"])
                if "secrets" in rule["resources"]:
                    self.assertFalse(set(rule["verbs"]) & {"list","watch"})
        artifact=next(r for r in roles["modeldev-step-v1"]["rules"] if r["resources"]==["secrets"])
        self.assertEqual(artifact["resourceNames"],["modeldev-kfp-artifacts"])

    def test_no_wildcard_or_missing_identity_default(self):
        for mutate in [lambda c:c["principals"].update(argo=None),lambda c:c.update(node_principals=[]),lambda c:c["principals"].update(maintenance="system:*")]:
            value=copy.deepcopy(config());mutate(value)
            with self.assertRaises(ValueError):
                render.render(value)


    def test_importer_identity_is_separate_and_pinned(self):
        rows = render.render(config())["items"]
        roles = {o["metadata"]["name"]: o for o in rows if o["kind"] == "ClusterRole"}
        # The importer account itself has no API permissions.
        self.assertNotIn("modeldev-dataset-importer-v1", roles)
        # Its webhook reader is distinct from the training one and only reads
        # a Job, the importer account and Pods.
        webhook = roles["modeldev-dataset-webhook-read-v1"]["rules"]
        self.assertEqual({r["resources"][0] for r in webhook}, {"jobs", "serviceaccounts", "pods"})
        self.assertEqual(next(r for r in webhook if r["resources"] == ["serviceaccounts"])["resourceNames"], [render.IMPORTER])
        # Control keeps only the verbs the close/cleanup path needs.
        control = roles["modeldev-dataset-control-v1"]["rules"]
        self.assertEqual(next(r for r in control if r["resources"] == ["serviceaccounts"])["resourceNames"], [render.IMPORTER])
        self.assertEqual(next(r for r in control if r["resources"] == ["namespaces"])["verbs"], ["get"])
        for rule in control:
            self.assertNotIn("*", rule["verbs"])
            self.assertFalse(set(rule["verbs"]) & {"escalate", "impersonate"})

    def test_importer_service_account_shape_is_fixed(self):
        document = render.render(config())
        bootstrap = next(o for o in document["items"] if o["kind"] == "ValidatingAdmissionPolicy" and o["metadata"]["name"] == render.NAMES[0])
        expression = str(bootstrap)
        # automount off and no image pull secret, exactly like trainer-workload.
        self.assertIn("modeldev-dataset-importer", expression)
        self.assertIn("automountServiceAccountToken", expression)
        # Both rolebinding rows for the dataset branch exist in RBAC.
        rows = document["items"]
        names = {o["metadata"]["name"] for o in rows if o["kind"] == "ClusterRole"}
        self.assertIn("modeldev-dataset-control-v1", names)
        self.assertIn("modeldev-dataset-webhook-read-v1", names)

    def test_dataset_identity_does_not_require_a_training_registry(self):
        document = render.render(config())
        bootstrap = next(o for o in document["items"] if o["kind"] == "ValidatingAdmissionPolicy" and o["metadata"]["name"] == render.NAMES[0])
        variables = {v["name"]: v["expression"] for v in bootstrap["spec"]["variables"]}
        identity = variables["datasetIdentity"]
        # Only the importer account and the frozen attempt Secret may use it.
        self.assertIn("modeldev-dataset-importer", identity)
        self.assertIn("modeldev-dataset-import-ca", identity)
        self.assertIn("target-snapshot-sha256", identity)
        self.assertIn("namespace-uid", identity)
        self.assertNotIn("environment-id", identity)
        # Training objects keep the registry identity.
        self.assertIn("environment-id", variables["owner"])
        checks = [v["expression"] for v in bootstrap["spec"]["validations"]]
        self.assertIn("variables.owner || variables.datasetIdentity", checks[0])
        # The attempt Secret rides the dataset identity, not the training owner.
        self.assertTrue(any("variables.datasetIdentity" in c and "credentials.json" in c for c in checks))

    def test_importer_purpose_is_required_on_pods(self):
        document = render.render(config())
        workload = next(o for o in document["items"] if o["kind"] == "ValidatingAdmissionPolicy" and o["metadata"]["name"] == render.NAMES[2])
        text = str(workload)
        # Purpose label and the importer SA must appear together in the pod gate.
        self.assertIn("dataset-import", text)
        self.assertIn("modeldev-dataset-importer", text)
        # Creation is limited to the dataset control plane, not to a trainer.
        self.assertIn("system:serviceaccount:platform:dataset-control", text)

    def test_every_import_pods_read_is_null_guarded(self):
        """The importer gate must never read metadata of an absent object.

        On CREATE oldObject is absent and on DELETE object is absent. An
        unguarded `has(oldObject.metadata.labels)` makes the `protected`
        variable error out, and failurePolicy=Fail then denies every workload
        request cluster-wide regardless of labels. This actually happened.
        """
        document = render.render(config())
        workload = next(o for o in document["items"] if o["kind"] == "ValidatingAdmissionPolicy" and o["metadata"]["name"] == render.NAMES[2])
        protected = next(v["expression"] for v in workload["spec"]["variables"] if v["name"] == "protected")
        # Only the Dataset purpose clauses introduce their own metadata reads in
        # this variable; the shared marker() helper already guards itself.
        for obj in ("object", "oldObject"):
            needle = obj + '.metadata.labels["modeldev.ani.io/purpose"]'
            index = protected.find(needle)
            self.assertGreaterEqual(index, 0, f"dataset clause for {obj} missing")
            # The clause must open with the null guard for the same object.
            guard = "(" + obj + " != null && "
            clause = protected[protected.rfind(guard, 0, index):index]
            self.assertTrue(
                clause.startswith(guard),
                f"unguarded metadata read on {obj}: ...{protected[max(0, index-120):index]}",
            )

    def test_dataset_principal_is_required(self):
        value = copy.deepcopy(config())
        del value["principals"]["dataset_control"]
        with self.assertRaises(ValueError):
            render.render(value)


    def test_dataset_control_cannot_update_training_workloads(self):
        document = render.render(config())
        workload = next(o for o in document["items"] if o["kind"] == "ValidatingAdmissionPolicy" and o["metadata"]["name"] == render.NAMES[2])
        dataset_control = "system:serviceaccount:platform:dataset-control"
        # The generic trusted list feeds the UPDATE allow-list; the dataset
        # control plane must not be in it, or it could rewrite training specs.
        update_condition = next(e["expression"] for e in workload["spec"]["validations"] if "variables.sameIdentity" in e["expression"])
        self.assertNotIn('"' + dataset_control + '"', update_condition)
        create_condition = next(e["expression"] for e in workload["spec"]["validations"] if "only exact original controllers" in e["message"])
        self.assertIn("dataset-import", create_condition)
        # It appears only alongside the importer purpose label.
        self.assertIn(dataset_control, str(workload["spec"]["matchConditions"]))
        self.assertIn("dataset-import", str(workload["spec"]["matchConditions"]))

    def test_importer_shape_constrains_jobs_only(self):
        document = render.render(config())
        workload = next(o for o in document["items"] if o["kind"] == "ValidatingAdmissionPolicy" and o["metadata"]["name"] == render.NAMES[2])
        create_condition = next(e["expression"] for e in workload["spec"]["validations"] if "only exact original controllers" in e["message"])
        # The importer-only shape must not block TrainJob/TrainingRuntime creation.
        self.assertIn("request.resource.resource != 'jobs'", create_condition)

    def test_control_keeps_exit_evidence_release_when_sharing_dataset_identity(self):
        value = copy.deepcopy(config())
        value["principals"]["dataset_control"] = value["principals"]["control"]
        document = render.render(value)
        workload = next(o for o in document["items"] if o["kind"] == "ValidatingAdmissionPolicy" and o["metadata"]["name"] == render.NAMES[2])
        update_condition = next(e["expression"] for e in workload["spec"]["validations"] if "variables.sameIdentity" in e["expression"])
        # Prompt 01/02 reuse one identity for training control and dataset
        # control; the control principal must still release retained Pods.
        self.assertIn('"' + value["principals"]["control"] + '"', update_condition)

    def test_native_rbac_needs_no_global_authorization_inventory(self):
        rows=render.render(config())["items"]
        self.assertFalse(any(o["metadata"]["name"].startswith("modeldev-rbac-inventory-global") for o in rows))
        roles={o["metadata"]["name"]:o for o in rows if o["kind"]=="ClusterRole"}
        runtime=roles["modeldev-runtime-v1"]["rules"]
        self.assertFalse(any(set(r["resources"]) & {"roles","rolebindings","clusterroles","clusterrolebindings"} for r in runtime))
        control=roles["modeldev-environment-control-v1"]["rules"]
        bootstrap=next(r for r in control if r["resources"]==["clusterroles"] and "get" in r["verbs"])
        self.assertEqual(set(bootstrap["resourceNames"]),{n+"-v1" for n in render.RB})
        for role in roles.values():
            for rule in role["rules"]:
                self.assertNotIn("clusterrolebindings",rule["resources"])
                self.assertFalse("roles" in rule["resources"])
                self.assertFalse("rolebindings" in rule["resources"] and "list" in rule["verbs"])


if __name__=="__main__":
    unittest.main()

"""Finite template contract checks; no claim of real API-server enforcement."""
import copy
import unittest
import render


def config():
    p={name:"system:serviceaccount:platform:"+name.replace("_","-") for name in ["control","webhook","kfp_api","argo","trainer","jobset","job_controller","persistence_agent"]}
    p.update(control="system:serviceaccount:ani-system:modeldev-control",webhook="system:serviceaccount:ani-system:ani-modeldev-webhook",maintenance="verified-maintenance")
    return {"principals":p,"node_principals":["system:node:verified-node"],"kfp_ca_key":"ca.crt","network_metadata_writer":"system:serviceaccount:kcn-system:kcn-controller","network_status_writer":"system:serviceaccount:kube-system:ani-multus","gc_finalizer_writer":{"username":"system:serviceaccount:kube-system:generic-garbage-collector","uid":"33333333-3333-4333-8333-333333333333"},"scheduler_writer":{"username":"system:serviceaccount:volcano-system:ani-volcano-scheduler","uid":"44444444-4444-4444-8444-444444444444"},"device_plugin_writer":{"username":"system:serviceaccount:kube-system:volcano-device-plugin","uid":"55555555-5555-4555-8555-555555555555"}}


class NativeTemplates(unittest.TestCase):
    def test_exact_policies_old_object_and_global_token_secret_guard(self):
        document=render.render(config())
        policies={o["metadata"]["name"]:o for o in document["items"] if o["kind"]=="ValidatingAdmissionPolicy"}
        self.assertEqual(set(policies),set(render.NAMES))
        for policy in policies.values():
            self.assertEqual(policy["spec"]["failurePolicy"],"Fail")
            self.assertNotIn("namespaceSelector",policy["spec"]["matchConstraints"])
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

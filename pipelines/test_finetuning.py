"""Compile the same assembly and inspect its actual fine tuning DAG and limits."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from google.protobuf import json_format
from kfp.pipeline_spec import pipeline_spec_pb2
import yaml


class FinetuningAssembly(unittest.TestCase):
    def test_actual_ir_owns_export_verify_and_confirmation_before_close(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            configuration = {
                "component_image": "registry.example.test/finetuning-step@sha256:" + "b" * 64,
                "owner_config_map": "finetuning-step-owner-fixture",
                "storage_class": "finetuning-workspace-fixture",
                "workspace_size": "20Gi", "workspace_access_mode": "ReadWriteMany",
                "kind": "FINETUNING",
                "admission_profile": "finetuning-v1",
            }
            config, output = root / "config.json", root / "pipeline.yaml"
            config.write_text(json.dumps(configuration), encoding="utf-8")
            result = subprocess.run([sys.executable, str(Path(__file__).with_name("cpu_p01.py")),
                                     "--config", str(config), "--output", str(output)],
                                    capture_output=True, text=True, check=False)
            self.assertEqual(result.returncode, 0, result.stderr)
            ir, platform = yaml.safe_load_all(output.read_text(encoding="utf-8"))
            json_format.ParseDict(ir, pipeline_spec_pb2.PipelineSpec())
            # Observed KFP 2.16 / Argo v2 Pod naming. KCN uses the complete
            # name in a VNic annotation suffix, whose Kubernetes limit is 63.
            component_template = "metadata-0-2-retry-system-container-impl"
            native_pod_name = ir["pipelineInfo"]["name"] + "-abcde-" + component_template + "-4294967295"
            self.assertLessEqual(len(native_pod_name.encode("ascii")), 63)
            root_tasks = ir["root"]["dag"]["tasks"]
            body_name, = set(root_tasks) - {"finalize-close"}
            body = ir["components"][root_tasks[body_name]["componentRef"]["name"]]
            tasks = body["dag"]["tasks"]
            chain = ["workspace-name", "createpvc", "prepare", "train-wait", "export",
                     "verify", "collect", "publish", "register", "close"]
            self.assertEqual(set(tasks), set(chain))
            for before, after in zip(chain, chain[1:]):
                self.assertIn(before, tasks[after]["dependentTasks"])
            self.assertEqual(tasks["register"]["inputs"]["parameters"]["candidate"]["taskOutputParameter"],
                             {"producerTask": "publish", "outputParameterKey": "candidate"})
            finalizer = root_tasks["finalize-close"]
            self.assertEqual(finalizer["triggerPolicy"]["strategy"], "ALL_UPSTREAM_TASKS_COMPLETED")
            self.assertNotIn("triggerPolicy", tasks["close"])
            limits = {"prepare": 900, "train-wait": 3600, "export": 1800, "verify": 900,
                      "collect": 600, "publish": 900, "register": 600, "close": 600}
            for role, task in tasks.items():
                self.assertFalse(task.get("cachingOptions", {}).get("enableCache", False))
                self.assertEqual(int(task.get("retryPolicy", {}).get("maxRetryCount", 0)), 0)
                if role == "createpvc":
                    continue
                executor = ir["components"][task["componentRef"]["name"]]["executorLabel"]
                container = ir["deploymentSpec"]["executors"][executor]["container"]
                k8s = platform["platforms"]["kubernetes"]["deploymentSpec"]["executors"][executor]
                self.assertEqual(container["image"], configuration["component_image"])
                if role in limits:
                    self.assertEqual(int(k8s["activeDeadlineSeconds"]), limits[role])
                if role in ("prepare", "export", "verify"):
                    self.assertEqual(container["resources"]["resourceCpuRequest"], "2")
                    self.assertEqual(container["resources"]["resourceMemoryRequest"], "6Gi")
                self.assertEqual(k8s["podMetadata"]["labels"]["ani.modeldev/step-role"], role)
                self.assertEqual(k8s["podMetadata"]["labels"]["ani.modeldev/admission-profile"], "finetuning-v1")
                self.assertEqual(k8s["emptyDirMounts"][0]["sizeLimit"], "12Gi")
                self.assertFalse(k8s["emptyDirMounts"][0].get("medium"))
                if role in ("export", "verify"):
                    mounts = k8s["pvcMount"]
                    self.assertTrue(all(m["subPath"] and m["mountPath"] == "/workspace/" + m["subPath"] for m in mounts))
            self.assertNotIn("deletepvc", output.read_text(encoding="utf-8"))
            final_executor = ir["components"][finalizer["componentRef"]["name"]]["executorLabel"]
            final_labels = platform["platforms"]["kubernetes"]["deploymentSpec"]["executors"][final_executor]["podMetadata"]["labels"]
            self.assertEqual(final_labels["ani.modeldev/step-role"], "finalize-close")
            self.assertEqual(final_labels["ani.modeldev/admission-profile"], "finetuning-v1")


if __name__ == "__main__":
    unittest.main()

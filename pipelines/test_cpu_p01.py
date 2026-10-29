"""Verify the compiled public IR contract, not a hand-built DAG substitute."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from google.protobuf import json_format
from kfp.pipeline_spec import pipeline_spec_pb2
import yaml
from resource_fixture import component_resources


class PipelineAssembly(unittest.TestCase):
    def test_finetuning_verify_mounts_the_original_adapter_and_training_report(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            configuration = {
                "kind": "FINETUNING",
                "component_image": "registry.example.test/modeldev-fine-step@sha256:" + "b" * 64,
                "owner_config_map": "fine-step-owner-fixture",
                "storage_class": "fine-workspace-fixture",
                "workspace_size": "16Gi",
                "workspace_access_mode": "ReadWriteOnce",
                "admission_profile": "fine-v21-fixture",
                "execution_timeout_max_seconds": 14400,
                "component_resources": component_resources("FINETUNING"),
            }
            config, output = root / "compile.json", root / "pipeline.yaml"
            config.write_text(json.dumps(configuration), encoding="utf-8")
            result = subprocess.run(
                [sys.executable, str(Path(__file__).with_name("cpu_p01.py")),
                 "--config", str(config), "--output", str(output)],
                capture_output=True, text=True, check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            ir, platform = list(yaml.safe_load_all(output.read_text(encoding="utf-8")))
            json_format.ParseDict(ir, pipeline_spec_pb2.PipelineSpec())
            body_task, = [task for name, task in ir["root"]["dag"]["tasks"].items()
                          if name != "finalize-close"]
            tasks = ir["components"][body_task["componentRef"]["name"]]["dag"]["tasks"]
            self.assertIn("export", tasks["verify"]["dependentTasks"])
            for role, paths in {
                "export": {"inputs", "training", "reports/train", "export", "reports/export", "work/export"},
                "verify": {"inputs", "training", "reports/train", "export", "reports/export", "reports/verify", "work/verify"},
            }.items():
                with self.subTest(role=role):
                    executor = ir["components"][tasks[role]["componentRef"]["name"]]["executorLabel"]
                    k8s = platform["platforms"]["kubernetes"]["deploymentSpec"]["executors"][executor]
                    mounts = k8s["pvcMount"]
                    self.assertEqual({mount["mountPath"]: mount["subPath"] for mount in mounts},
                                     {"/workspace/" + path: path for path in paths})
                    self.assertEqual(len(mounts), len(paths))
                    self.assertEqual(len({mount["taskOutputParameter"]["producerTask"] for mount in mounts}), 1)
                    self.assertEqual(int(k8s["activeDeadlineSeconds"]), 14400)

    def test_compiled_pipeline_preserves_execution_chain_and_failure_close(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            configuration = {
                "component_image": "registry.example.test/modeldev-step@sha256:" + "a" * 64,
                "owner_config_map": "cpu-step-owner-fixture",
                "storage_class": "cpu-workspace-fixture",
                "workspace_size": "2Gi",
                "workspace_access_mode": "ReadWriteOnce",
                "admission_profile": "cpu-v1",
                "execution_timeout_max_seconds": 7200,
                "component_resources": component_resources("GENERAL_TRAINING"),
            }
            config = root / "compile.json"
            config.write_text(json.dumps(configuration), encoding="utf-8")
            output = root / "pipeline.yaml"
            result = subprocess.run(
                [sys.executable, str(Path(__file__).with_name("cpu_p01.py")),
                 "--config", str(config), "--output", str(output)],
                capture_output=True, text=True, check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            documents = list(yaml.safe_load_all(output.read_text(encoding="utf-8")))
            ir, platform = documents
            json_format.ParseDict(ir, pipeline_spec_pb2.PipelineSpec())
            # Actual KFP 2.16 executor suffix from the failed cluster Run.
            # KCN uses the generated Pod name in an annotation name (63 bytes).
            generated_name = (ir["pipelineInfo"]["name"] +
                              "-abcde-metadata-0-2-retry-system-container-impl-4294967295")
            self.assertLessEqual(len(generated_name.encode("ascii")), 63)
            inputs = ir["root"]["inputDefinitions"]["parameters"]
            self.assertEqual(set(inputs), {"execution_id", "spec_hash"})
            self.assertTrue(all(p["parameterType"] == "STRING" and "defaultValue" not in p for p in inputs.values()))
            root_tasks = ir["root"]["dag"]["tasks"]
            self.assertEqual(len(root_tasks), 2)
            self.assertIn("finalize-close", root_tasks)
            body_name, = set(root_tasks) - {"finalize-close"}
            finalizer = root_tasks["finalize-close"]
            self.assertEqual(finalizer["dependentTasks"], [body_name])
            self.assertEqual(finalizer["triggerPolicy"]["strategy"], "ALL_UPSTREAM_TASKS_COMPLETED")
            self.assertEqual(set(finalizer["inputs"]["parameters"]), {"execution_id", "spec_hash", "run_id"})
            self.assertEqual(finalizer["taskInfo"]["name"], "close-finalizer")
            body = ir["components"][root_tasks[body_name]["componentRef"]["name"]]
            tasks = body["dag"]["tasks"]
            self.assertEqual(set(tasks), {"workspace-name", "createpvc", "prepare", "train-wait", "collect", "publish", "close"})
            for before, after in [("workspace-name", "createpvc"), ("createpvc", "prepare"), ("prepare", "train-wait"), ("train-wait", "collect"), ("collect", "publish"), ("publish", "close")]:
                self.assertIn(before, tasks[after]["dependentTasks"])
            self.assertNotIn("triggerPolicy", tasks["close"])
            self.assertEqual(tasks["close"]["inputs"]["parameters"]["candidate"]["taskOutputParameter"],
                             {"producerTask": "publish", "outputParameterKey": "candidate"})
            close_component = ir["components"][tasks["close"]["componentRef"]["name"]]
            self.assertEqual(close_component["inputDefinitions"]["parameters"]["candidate"]["defaultValue"], "")
            for name, task in {**tasks, "finalize-close": finalizer}.items():
                self.assertFalse(task.get("cachingOptions", {}).get("enableCache", False))
                self.assertEqual(int(task.get("retryPolicy", {}).get("maxRetryCount", 0)), 0)
                if name == "createpvc":
                    continue  # Official KFP resource primitive, not a runnable image.
                executor = ir["components"][task["componentRef"]["name"]]["executorLabel"]
                container = ir["deploymentSpec"]["executors"][executor]["container"]
                self.assertEqual(container["image"], configuration["component_image"])
                self.assertEqual(container["command"], ["/ani-modeldev-step", "close" if name == "finalize-close" else name])
                if name == "finalize-close":
                    self.assertIn("--close-only", container["args"])
                    self.assertNotIn("--publication-ref", container["args"])
                    self.assertNotIn("--publication-ref-file", container["args"])
                if name == "publish":
                    self.assertIn("--publication-ref-file",container["args"])
                if name == "close":
                    self.assertIn("--publication-ref",container["args"])
                self.assertNotIn("--candidate-json",container["args"])
                self.assertNotIn("--candidate-file",container["args"])
                if name != "workspace-name":
                    self.assertEqual(task["inputs"]["parameters"]["run_id"],
                                     {"runtimeValue": {"constant": "{{$.pipeline_job_uuid}}"}})
                    self.assertEqual(container["args"][container["args"].index("--run-id") + 1],
                                     "{{$.inputs.parameters['run_id']}}")
                    self.assertNotIn("{{$.pipeline_job_uuid}}", container["args"])
                    self.assertNotIn("--task-id", container["args"])
                    self.assertNotIn("{{$.pipeline_task_uuid}}", container["args"])
                self.assertNotIn("sh", container["command"])
                k8s = platform["platforms"]["kubernetes"]["deploymentSpec"]["executors"][executor]
                self.assertEqual(int(k8s["activeDeadlineSeconds"]), configuration["execution_timeout_max_seconds"])
                self.assertEqual(k8s["podMetadata"]["labels"]["ani.modeldev/admission-profile"], "cpu-v1")
                self.assertEqual(k8s["podMetadata"]["labels"]["ani.modeldev/step-role"], name)
                self.assertEqual(bool(k8s.get("pvcMount")), name in {"prepare", "collect", "publish", "close"})
                self.assertFalse(k8s.get("secretAsVolume"))
                self.assertEqual({item["name"] for item in k8s["fieldPathAsEnv"]}, {"ANI_POD_NAME", "ANI_POD_UID", "ANI_POD_NAMESPACE"})
            self.assertNotIn("deletepvc", output.read_text(encoding="utf-8"))

    def test_missing_or_invalid_admission_profile_is_rejected_before_ir_creation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            configuration = {
                "component_image": "registry.example.test/modeldev-step@sha256:" + "a" * 64,
                "owner_config_map": "cpu-step-owner-fixture", "storage_class": "cpu-workspace-fixture",
                "workspace_size": "2Gi", "workspace_access_mode": "ReadWriteOnce",
                "execution_timeout_max_seconds": 7200,
                "component_resources": component_resources("GENERAL_TRAINING"),
            }
            for index, profile in enumerate((None, "", "UPPER", "../another", "a" * 64)):
                with self.subTest(profile=profile):
                    current = dict(configuration)
                    if profile is not None:
                        current["admission_profile"] = profile
                    config, output = root / "compile.json", root / ("rejected-%d.yaml" % index)
                    config.write_text(json.dumps(current), encoding="utf-8")
                    result = subprocess.run([sys.executable, str(Path(__file__).with_name("cpu_p01.py")),
                                             "--config", str(config), "--output", str(output)],
                                            capture_output=True, text=True, check=False)
                    self.assertNotEqual(result.returncode, 0, result.stdout)
                    self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()

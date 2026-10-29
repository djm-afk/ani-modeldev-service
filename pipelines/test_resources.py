"""Actual pinned KFP IR projection of explicit resources and PVC role scopes."""

import hashlib
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


class ResourceProjection(unittest.TestCase):
    def test_explicit_resources_work_mounts_and_readers_compile_without_hidden_defaults(self):
        for kind in ("GENERAL_TRAINING", "FINETUNING"):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                config = {"component_image": "example.test/step@sha256:" + "a" * 64,
                          "owner_config_map": "owner", "storage_class": "workspace",
                          "workspace_size": "64Gi", "workspace_access_mode": "ReadWriteOnce",
                          "admission_profile": "current-v3", "execution_timeout_max_seconds": 7200,
                          "kind": kind, "component_resources": component_resources(kind)}
                source, output = root / "config.json", root / "pipeline.yaml"
                source.write_text(json.dumps(config))
                result = subprocess.run([sys.executable, str(Path(__file__).with_name("cpu_p01.py")),
                                         "--config", str(source), "--output", str(output)], capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                ir, platform = list(yaml.safe_load_all(output.read_text()))
                json_format.ParseDict(ir, pipeline_spec_pb2.PipelineSpec())
                finalizer = ir["root"]["dag"]["tasks"]["finalize-close"]
                body, = [v for k, v in ir["root"]["dag"]["tasks"].items() if k != "finalize-close"]
                tasks = {**ir["components"][body["componentRef"]["name"]]["dag"]["tasks"], "finalize-close": finalizer}
                for role, task in tasks.items():
                    if role == "createpvc":
                        continue
                    executor = ir["components"][task["componentRef"]["name"]]["executorLabel"]
                    container = ir["deploymentSpec"]["executors"][executor]["container"]
                    k8s = platform["platforms"]["kubernetes"]["deploymentSpec"]["executors"][executor]
                    expected = config["component_resources"][role]
                    self.assertEqual(container["resources"]["resourceCpuRequest"], expected["cpu_request"])
                    self.assertEqual(container["resources"]["resourceMemoryLimit"], expected["memory_limit"])
                    digest = hashlib.sha256(json.dumps(expected, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
                    self.assertEqual(k8s["podMetadata"]["annotations"]["ani.modeldev/component-resources-sha256"], digest)
                    self.assertEqual(k8s["emptyDirMounts"][0]["sizeLimit"], expected["ephemeral_storage_limit"])
                    mounts = {m["mountPath"]: m.get("subPath", "") for m in k8s.get("pvcMount", [])}
                    if role == "prepare":
                        self.assertEqual(mounts, {"/workspace": "", "/workspace/work/prepare/tmp": "work/prepare/tmp"})
                    elif role in ("close", "register"):
                        self.assertEqual(mounts, {"/workspace": ""})
                    elif role == "finalize-close":
                        self.assertEqual(mounts, {})
                    if role in ("export", "verify", "collect", "publish"):
                        self.assertEqual(mounts["/workspace/work/" + role], "work/" + role)
                    if role in ("prepare", "export", "verify", "collect", "publish"):
                        env = {e["name"]: e["value"] for e in container["env"]}
                        self.assertEqual(env["TMPDIR"], "/workspace/work/" + role + "/tmp")
                        self.assertEqual(env["HF_DATASETS_CACHE"], "/workspace/work/" + role + "/hf/datasets")


if __name__ == "__main__":
    unittest.main()

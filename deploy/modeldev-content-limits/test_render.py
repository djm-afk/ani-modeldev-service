"""Exercise the offline deployment CLI; no cluster or business substitute."""
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest


RENDERER = Path(__file__).with_name("render.py")


class ContentLimitDeploymentTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.modeldev = {
            "server": {"grpc": {"addr": ":9443", "timeout": "10s"}},
            "command": {"database_url_file": "/private/database-url"},
            "runtime": {
                "limits": {"max_content_bytes": "${MODELDEV_MAX_CONTENT_BYTES}"},
                "api_timeout": "30s",
                "object_storage": {"credentials_file": "/private/storage-identity"},
            },
        }
        self.provider = {
            "apiVersion": "apps/v1", "kind": "Deployment",
            "metadata": {"namespace": "ani-system", "name": "actual-model-provider"},
            "spec": {"replicas": 2, "template": {"spec": {"containers": [
                {"name": "model", "image": "registry.invalid/model@sha256:" + "a" * 64,
                 "env": [
                     {"name": "ANI_MINIO_SECRET_KEY", "valueFrom": {"secretKeyRef": {"name": "storage", "key": "secret"}}},
                     {"name": "ANI_MODEL_SIGNED_URL_TTL_SECONDS", "value": "300"},
                     {"name": "MODELDEV_MAX_CONTENT_BYTES", "value": "123"},
                 ]},
                {"name": "sidecar", "env": [{"name": "SIDECAR_SETTING", "value": "unchanged"}]},
            ]}}},
        }
        self.config_path = self.root / "modeldev.json"
        self.provider_path = self.root / "model-provider.json"
        self.config_path.write_text(json.dumps(self.modeldev))
        self.provider_path.write_text(json.dumps(self.provider))

    def invoke(self, output, limit):
        environment = os.environ.copy()
        environment.pop("MODELDEV_MAX_CONTENT_BYTES", None)
        environment["MAX_CONTENT_BYTES"] = "4294967296"
        if limit is not None:
            environment["MODELDEV_MAX_CONTENT_BYTES"] = limit
        return subprocess.run([
            sys.executable, str(RENDERER),
            "--modeldev-config", str(self.config_path),
            "--model-provider-deployment", str(self.provider_path),
            "--model-provider-container", "model",
            "--output", str(output),
        ], env=environment, capture_output=True, text=True)

    def test_one_deployment_value_projects_four_and_sixteen_gib_to_both_services(self):
        original_config = self.config_path.read_bytes()
        original_provider = self.provider_path.read_bytes()
        for limit in ("4294967296", "17179869184"):
            with self.subTest(max_content_bytes=limit):
                output = self.root / ("rendered-" + limit)
                result = self.invoke(output, limit)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(sorted(p.name for p in output.iterdir()), ["managed-runtime.yaml", "model-provider.json"])
                runtime_path, provider_path = output / "managed-runtime.yaml", output / "model-provider.json"
                modeldev = json.loads(runtime_path.read_text())
                provider = json.loads(provider_path.read_text())
                self.assertEqual(modeldev["runtime"]["limits"]["max_content_bytes"], limit)
                expected_modeldev = json.loads(original_config)
                expected_modeldev["runtime"]["limits"]["max_content_bytes"] = limit
                self.assertEqual(modeldev, expected_modeldev)
                expected_provider = json.loads(original_provider)
                expected_provider["spec"]["template"]["spec"]["containers"][0]["env"][2] = {"name": "MODELDEV_MAX_CONTENT_BYTES", "value": limit}
                self.assertEqual(provider, expected_provider)
                self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o700)
                for path in (runtime_path, provider_path):
                    self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
                self.assertEqual(self.config_path.read_bytes(), original_config)
                self.assertEqual(self.provider_path.read_bytes(), original_provider)

    def test_required_positive_int64_never_falls_back_or_writes_failed_output(self):
        marker = "PRIVATE_CONFIG_MUST_NOT_BE_LOGGED"
        self.modeldev["command"]["database_url_file"] = marker
        self.modeldev["runtime"]["limits"]["max_content_bytes"] = "17179869184"
        self.config_path.write_text(json.dumps(self.modeldev))
        before = sorted(path.name for path in self.root.iterdir())
        for limit in (None, "", "0", "-1", "9223372036854775808"):
            with self.subTest(invalid_limit=limit):
                result = self.invoke(self.root / "invalid-output", limit)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, "")
                self.assertNotIn(marker, result.stderr)
                self.assertEqual(sorted(path.name for path in self.root.iterdir()), before)

    def test_second_projection_failure_and_existing_bundle_cannot_publish_partial_update(self):
        original = self.provider_path.read_bytes()
        self.provider["spec"]["template"]["spec"]["containers"][0]["name"] = "different-container"
        self.provider_path.write_text(json.dumps(self.provider))
        before = sorted(path.name for path in self.root.iterdir())
        result = self.invoke(self.root / "invalid-provider", "4294967296")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(sorted(path.name for path in self.root.iterdir()), before)

        self.provider_path.write_bytes(original)
        output = self.root / "existing-bundle"
        result = self.invoke(output, "4294967296")
        self.assertEqual(result.returncode, 0, result.stderr)
        files = {path.name: path.read_bytes() for path in output.iterdir()}
        result = self.invoke(output, "17179869184")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual({path.name: path.read_bytes() for path in output.iterdir()}, files)
        self.assertEqual(sorted(path.name for path in self.root.iterdir()), sorted(before + ["existing-bundle"]))


if __name__ == "__main__":
    unittest.main()

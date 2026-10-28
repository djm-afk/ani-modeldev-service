"""Real native export/verify consume legal dynamic inventories above 64 KiB.

The supplied workspace is produced by actual SFT training in the same image.
Only ancillary artifact fixture bytes are added; production inventory/hash and
report writers build their identities. No model, writer, loader or SHA check is
replaced. Platform service acceptance remains separate.
"""

import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location(
    "finetune_entry", Path(__file__).parents[1] / "finetuning/finetune_entry.py"
)
ENTRY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ENTRY)


@unittest.skipUnless(os.environ.get("ANI_FINETUNE_TEST_WORKSPACE"), "requires actual SFT outputs from the same pinned image")
class DynamicManifestConsumerTests(unittest.TestCase):
    def test_real_export_and_verify_accept_legal_large_manifests(self):
        environment = dict(os.environ)
        cached_temporary_root = tempfile.tempdir

        def restore_environment():
            os.environ.clear()
            os.environ.update(environment)
            tempfile.tempdir = cached_temporary_root

        self.addCleanup(restore_environment)
        prepared = Path(os.environ["ANI_FINETUNE_TEST_WORKSPACE"])
        with tempfile.TemporaryDirectory(dir=os.environ["TMPDIR"]) as temporary:
            workspace = Path(temporary) / "workspace"
            shutil.copytree(prepared, workspace)
            # Discard only outputs in this test's new private copy. The actual
            # completed training facts and original frozen inputs are retained.
            for relative in ("export", "reports/export", "reports/verify"):
                shutil.rmtree(workspace / relative)
                (workspace / relative).mkdir()
            adapter = workspace / "training/adapter"
            original_files = ENTRY.inventory(adapter)
            auxiliary = adapter / "provenance"
            auxiliary.mkdir()
            for index in range(600):
                ENTRY.write_json(auxiliary / f"member-{index:04d}.json", {"member": index, "scope": "real ancillary fixture bytes"})
            files = ENTRY.inventory(adapter)
            indexed = {item["path"]: item for item in files}
            self.assertTrue(all(indexed[item["path"]] == item for item in original_files))
            for name, field in (("summary.json", "adapter_files"), ("adapter_manifest.json", "files")):
                path = workspace / "reports/train" / name
                report = json.loads(ENTRY.read_regular(path, ENTRY.MAX_MANIFEST_BYTES))
                report[field] = files
                ENTRY.write_json(path, report)
                self.assertGreater(path.stat().st_size, 65536)
                self.assertLess(path.stat().st_size, ENTRY.MAX_MANIFEST_BYTES)

            def frozen(name):
                return hashlib.sha256((workspace / "inputs" / name).read_bytes()).hexdigest()

            arguments = argparse.Namespace(
                workspace=workspace,
                configuration=workspace / "inputs/finetune_configuration.json",
                dataset_manifest=workspace / "inputs/dataset-manifest.json",
                expected_dataset_manifest_sha256=frozen("dataset-manifest.json"),
                expected_base_manifest_sha256=frozen("base_manifest.json"),
                expected_template_plan_sha256=frozen("template-plan.json"),
            )
            def stage(role):
                command = [sys.executable, "-I", ENTRY.__file__, role,
                    "--workspace", str(arguments.workspace),
                    "--configuration", str(arguments.configuration),
                    "--dataset-manifest", str(arguments.dataset_manifest),
                    "--expected-dataset-manifest-sha256", arguments.expected_dataset_manifest_sha256,
                    "--expected-base-manifest-sha256", arguments.expected_base_manifest_sha256,
                    "--expected-template-plan-sha256", arguments.expected_template_plan_sha256]
                completed = subprocess.run(command, capture_output=True, text=True, timeout=120)
                self.assertEqual(completed.returncode, 0, completed.stderr[-6000:])

            stage("export")
            merged_manifest = workspace / "reports/export/merged_manifest.json"
            self.assertGreater(merged_manifest.stat().st_size, 65536)
            self.assertLess(merged_manifest.stat().st_size, ENTRY.MAX_MANIFEST_BYTES)
            stage("verify")
            verification = json.loads((workspace / "reports/verify/verify.json").read_bytes())
            self.assertEqual(verification["status"], "PASSED")
            self.assertTrue(verification["finite_logits"])
            self.assertEqual(verification["merged_manifest_sha256"], ENTRY.file_digest(merged_manifest)["sha256"])
            self.assertEqual(ENTRY.inventory(adapter), files)
            print(json.dumps({"state": "ACTUAL_LARGE_MANIFEST_CONSUMERS_PASS",
                "summary_bytes": (workspace / "reports/train/summary.json").stat().st_size,
                "adapter_manifest_bytes": (workspace / "reports/train/adapter_manifest.json").stat().st_size,
                "merged_manifest_bytes": merged_manifest.stat().st_size,
                "ancillary_files": 600, "original_artifact_identities_unchanged": True}))


if __name__ == "__main__":
    unittest.main()

"""Run in the pinned native image; exercise its real Triton cache without a GPU."""

import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parents[1] / "finetuning"))
from template_runtime import configure_work_directory


class TritonWorkDirectoryTests(unittest.TestCase):
    def test_native_cache_uses_role_workspace_despite_unwritable_home(self):
        from triton.runtime.cache import FileCacheManager

        with tempfile.TemporaryDirectory() as directory, patch.dict(
            os.environ,
            {"HOME": "/nonexistent", "TRITON_CACHE_DIR": "/nonexistent/inherited-triton"},
        ):
            workspace = Path(directory)
            for role in ("train", "export", "verify"):
                with self.subTest(role=role):
                    work = configure_work_directory(workspace, role)
                    cache = FileCacheManager("managed-role-cache-constructor")
                    cached = Path(cache.put(b"native cache proof", "proof.bin"))
                    self.assertTrue(cached.is_relative_to(work / "triton"))
                    self.assertEqual(Path(cache.get_file("proof.bin")).read_bytes(), b"native cache proof")
                    self.assertEqual(os.environ["TRITON_CACHE_DIR"], str(work / "triton"))
                    self.assertEqual(os.environ["HOME"], "/nonexistent")
                    self.assertEqual(os.environ["HF_HUB_OFFLINE"], "1")
                    self.assertEqual(os.environ["TRANSFORMERS_OFFLINE"], "1")

    def test_linked_triton_cache_is_rejected_before_native_cache_write(self):
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory)
            outside = workspace / "outside"
            outside.mkdir()
            work = workspace / "work/train"
            work.mkdir(parents=True)
            (work / "triton").symlink_to(outside, target_is_directory=True)
            with self.assertRaisesRegex(ValueError, "linked work directory"):
                configure_work_directory(workspace, "train")
            self.assertEqual(list(outside.iterdir()), [])


if __name__ == "__main__":
    unittest.main()

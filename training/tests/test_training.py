"""CPU03 behavior tests: run only on Fedora or in an authorized cluster Job."""

import csv
import hashlib
import json
import math
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest

from dataset_fixture import manifest_arguments

TRAINING_ROOT = Path(__file__).resolve().parents[1]


class FixedCSVTrainingTest(unittest.TestCase):
    def run_python(self, *arguments, timeout=120, environment_overrides=None):
        environment = os.environ.copy()
        environment.update({"CUDA_VISIBLE_DEVICES": "", "WORLD_SIZE": "1"})
        environment.update(environment_overrides or {})
        return subprocess.run(
            [sys.executable, "-I", *map(str, arguments)],
            text=True,
            capture_output=True,
            timeout=timeout,
            check=False,
            env=environment,
        )

    def test_fixed_csv_produces_48_step_checkpoint_that_a_new_process_can_reload(self):
        dependency = self.run_python(
            "-c",
            "import torch; print(torch.__version__); "
            "assert torch.version.cuda is None, 'CPU wheel required'",
        )
        self.assertEqual(
            dependency.returncode,
            0,
            "ENVIRONMENT_NOT_READY (not a behavior RED): a working CPU PyTorch "
            f"environment is required\n{dependency.stdout}\n{dependency.stderr}",
        )
        with tempfile.TemporaryDirectory(prefix="cpu03-training-") as temporary:
            root = Path(temporary)
            data = root / "inputs/data.csv"
            data.parent.mkdir()
            output = root / "execution-output"
            fixture = self.run_python(TRAINING_ROOT / "make_data.py", "--output", data)
            self.assertEqual(fixture.returncode, 0, fixture.stderr)
            with data.open(newline="", encoding="utf-8") as stream:
                records = list(csv.reader(stream))
            self.assertEqual(records[0], [f"x{i}" for i in range(16)] + ["label"])
            self.assertEqual(len(records) - 1, 1024)
            input_bytes = data.read_bytes()
            input_sha256 = hashlib.sha256(input_bytes).hexdigest()

            trained = self.run_python(
                TRAINING_ROOT / "train_mlp.py",
                *manifest_arguments(data),
                "--output",
                output,
            )
            self.assertEqual(
                trained.returncode,
                0,
                "valid fixed CSV must train and produce a checkpoint; "
                f"exit={trained.returncode}\n{trained.stdout}\n{trained.stderr}",
            )
            self.assertEqual(
                data.read_bytes(), input_bytes, "selected input must remain unchanged"
            )
            expected_files = {
                "model.pt",
                "model_config.json",
                "metrics.jsonl",
                "summary.json",
            }
            self.assertEqual(
                {path.name for path in output.iterdir()},
                expected_files | {"result.json"},
            )
            summary = json.loads((output / "summary.json").read_text(encoding="utf-8"))
            self.assertEqual(summary["samples"], 1024)
            self.assertEqual(summary["epochs"], 3)
            self.assertEqual(summary["batch_size"], 64)
            self.assertEqual(summary["steps"], 48)
            self.assertEqual(summary["world_size"], 1)
            self.assertEqual(summary["device"], "cpu")
            self.assertEqual(summary["input_sha256"], input_sha256)
            self.assertEqual(summary["input_bytes"], str(len(input_bytes)))
            self.assertTrue(math.isfinite(summary["initial_loss"]))
            self.assertTrue(math.isfinite(summary["final_loss"]))
            self.assertLess(summary["final_loss"], summary["initial_loss"])

            metrics = [
                json.loads(line)
                for line in (output / "metrics.jsonl")
                .read_text(encoding="utf-8")
                .splitlines()
            ]
            self.assertEqual([entry["step"] for entry in metrics], list(range(1, 49)))
            for entry in metrics:
                self.assertEqual(entry["name"], "train.loss")
                self.assertEqual(entry["rank"], 0)
                self.assertTrue(math.isfinite(entry["value"]))
                self.assertGreaterEqual(entry["value"], 0)

            candidate = json.loads((output / "result.json").read_text(encoding="utf-8"))
            self.assertEqual(candidate["schema"], "ani.output-candidate.v1")
            self.assertEqual(candidate["input_sha256"], input_sha256)
            self.assertFalse(candidate["deployable"])
            self.assertEqual(len(candidate["files"]), 4)
            self.assertEqual(
                {entry["path"] for entry in candidate["files"]}, expected_files
            )
            for entry in candidate["files"]:
                actual = (output / entry["path"]).read_bytes()
                self.assertGreater(len(actual), 0)
                self.assertEqual(entry["bytes"], str(len(actual)))
                self.assertEqual(entry["sha256"], hashlib.sha256(actual).hexdigest())

            reloaded = self.run_python(
                Path(__file__).with_name("reload_checkpoint.py"), output
            )
            self.assertEqual(reloaded.returncode, 0, reloaded.stderr)
            self.assertEqual(
                json.loads(reloaded.stdout), {"shape": [4, 2], "device": "cpu"}
            )

    def test_invalid_input_is_rejected_before_training_and_preserves_paths(self):
        dependency = self.run_python(
            "-c",
            "import torch; assert torch.version.cuda is None, 'CPU wheel required'",
        )
        self.assertEqual(
            dependency.returncode,
            0,
            f"ENVIRONMENT_NOT_READY (not a behavior RED)\n{dependency.stderr}",
        )
        cases = (
            "wrong_sha256",
            "wrong_size",
            "negative_size",
            "empty_data",
            "missing_feature",
            "extra_feature",
            "duplicate_feature",
            "invalid_label",
            "fractional_label",
            "nan_feature",
            "inf_feature",
            "negative_inf_feature",
            "world_size_two",
            "world_size_zero",
            "world_size_invalid",
            "symlink_data",
            "symlink_output",
            "nonempty_output",
        )
        with tempfile.TemporaryDirectory(prefix="cpu03-rejected-input-") as temporary:
            root = Path(temporary)
            fixture_path = root / "fixture.csv"
            generated = self.run_python(
                TRAINING_ROOT / "make_data.py", "--output", fixture_path
            )
            self.assertEqual(generated.returncode, 0, generated.stderr)
            with fixture_path.open(newline="", encoding="utf-8") as stream:
                fixed_rows = list(csv.reader(stream))
            for case in cases:
                with self.subTest(case=case):
                    case_root = root / case
                    case_root.mkdir()
                    data = case_root / "inputs/data.csv"
                    data.parent.mkdir()
                    output = case_root / "execution-output"
                    rows = [row.copy() for row in fixed_rows]
                    if case == "empty_data":
                        rows = rows[:1]
                    elif case == "missing_feature":
                        for row in rows:
                            row.pop(15)
                    elif case == "extra_feature":
                        rows[0].insert(16, "x16")
                        for row in rows[1:]:
                            row.insert(16, "0.0")
                    elif case == "duplicate_feature":
                        rows[0][15] = "x14"
                    elif case == "invalid_label":
                        rows[1][-1] = "2"
                    elif case == "fractional_label":
                        rows[1][-1] = "0.5"
                    elif case in ("nan_feature", "inf_feature", "negative_inf_feature"):
                        rows[1][0] = {
                            "nan_feature": "NaN",
                            "inf_feature": "Inf",
                            "negative_inf_feature": "-Inf",
                        }[case]
                    with data.open("x", newline="", encoding="utf-8") as stream:
                        csv.writer(stream).writerows(rows)
                    selected_bytes = data.read_bytes()
                    expected_sha256 = hashlib.sha256(selected_bytes).hexdigest()
                    expected_size = len(selected_bytes)
                    if case == "wrong_sha256":
                        expected_sha256 = "0" * 64
                    elif case == "wrong_size":
                        expected_size += 1
                    elif case == "negative_size":
                        expected_size = -1
                    if case == "symlink_data":
                        target = case_root / "protected-input.csv"
                        data.rename(target)
                        data.symlink_to(target)

                    protected_output = None
                    if case == "symlink_output":
                        protected_output = case_root / "protected-output"
                        protected_output.mkdir()
                        output.symlink_to(protected_output, target_is_directory=True)
                    elif case == "nonempty_output":
                        output.mkdir()
                        protected_output = output
                        (output / "sentinel.txt").write_bytes(
                            b"existing execution bytes\n"
                        )
                    world_size = {
                        "world_size_two": "2",
                        "world_size_zero": "0",
                        "world_size_invalid": "not-an-integer",
                    }.get(case, "1")
                    rejected = self.run_python(
                        TRAINING_ROOT / "train_mlp.py",
                        *manifest_arguments(data, expected_sha256=expected_sha256,
                                            expected_bytes=expected_size),
                        "--output",
                        output,
                        environment_overrides={"WORLD_SIZE": world_size},
                    )
                    self.assertNotEqual(
                        rejected.returncode,
                        0,
                        f"{case} must be rejected before computation\n"
                        f"{rejected.stdout}\n{rejected.stderr}",
                    )
                    events = []
                    for line in rejected.stdout.splitlines():
                        try:
                            events.append(json.loads(line))
                        except json.JSONDecodeError:
                            continue
                    self.assertFalse(
                        any(
                            isinstance(event, dict)
                            and event.get("name") == "train.loss"
                            for event in events
                        ),
                        "rejection must not emit a completed optimization step",
                    )
                    self.assertEqual(
                        data.read_bytes(),
                        selected_bytes,
                        "input bytes must be preserved",
                    )
                    self.assertEqual(data.is_symlink(), case == "symlink_data")
                    if protected_output is None:
                        self.assertFalse(
                            output.exists(),
                            "rejection must not create an output directory",
                        )
                        self.assertFalse(output.is_symlink())
                    else:
                        self.assertEqual(
                            {path.name for path in protected_output.iterdir()},
                            {"sentinel.txt"} if case == "nonempty_output" else set(),
                            "existing output must not acquire training artifacts",
                        )
                        if case == "nonempty_output":
                            self.assertEqual(
                                (protected_output / "sentinel.txt").read_bytes(),
                                b"existing execution bytes\n",
                            )
                        self.assertEqual(output.is_symlink(), case == "symlink_output")

    def test_learning_rate_changes_training_and_accepts_an_existing_empty_output(self):
        dependency = self.run_python(
            "-c",
            "import torch; assert torch.version.cuda is None, 'CPU wheel required'",
        )
        self.assertEqual(
            dependency.returncode,
            0,
            f"ENVIRONMENT_NOT_READY (not a behavior RED)\n{dependency.stderr}",
        )
        with tempfile.TemporaryDirectory(prefix="cpu03-learning-rate-") as temporary:
            root = Path(temporary)
            data = root / "inputs/data.csv"
            data.parent.mkdir()
            generated = self.run_python(
                TRAINING_ROOT / "make_data.py", "--output", data
            )
            self.assertEqual(generated.returncode, 0, generated.stderr)
            input_bytes = data.read_bytes()
            common = manifest_arguments(data)
            baseline_output = root / "default-rate-output"
            baseline = self.run_python(
                TRAINING_ROOT / "train_mlp.py",
                *common,
                "--output",
                baseline_output,
            )
            self.assertEqual(baseline.returncode, 0, baseline.stderr)

            changed_output = root / "selected-rate-output"
            changed_output.mkdir()
            changed = self.run_python(
                TRAINING_ROOT / "train_mlp.py",
                *common,
                "--output",
                changed_output,
                "--learning-rate",
                "0.02",
            )
            self.assertEqual(
                changed.returncode,
                0,
                "a valid explicit learning rate and an existing empty output must train\n"
                f"{changed.stdout}\n{changed.stderr}",
            )
            self.assertEqual(data.read_bytes(), input_bytes)
            for output, effective_rate in (
                (baseline_output, "0.01"),
                (changed_output, "0.02"),
            ):
                summary = json.loads(
                    (output / "summary.json").read_text(encoding="utf-8")
                )
                self.assertEqual(summary["learning_rate"], effective_rate)
                self.assertEqual(summary["epochs"], 3)
                self.assertEqual(summary["batch_size"], 64)
                self.assertEqual(summary["steps"], 48)
                self.assertTrue(math.isfinite(summary["final_loss"]))
                metrics = [
                    json.loads(line)
                    for line in (output / "metrics.jsonl")
                    .read_text(encoding="utf-8")
                    .splitlines()
                ]
                self.assertEqual(
                    [entry["step"] for entry in metrics], list(range(1, 49))
                )
                reloaded = self.run_python(
                    Path(__file__).with_name("reload_checkpoint.py"), output
                )
                self.assertEqual(reloaded.returncode, 0, reloaded.stderr)
            comparison = self.run_python(
                Path(__file__).with_name("compare_checkpoints.py"),
                baseline_output,
                changed_output,
            )
            self.assertEqual(comparison.returncode, 0, comparison.stderr)
            self.assertEqual(
                json.loads(comparison.stdout), {"different_parameters": True}
            )

    def test_nondefault_parameters_partial_batches_and_non_decreasing_loss(self):
        with tempfile.TemporaryDirectory(prefix="mlp-open-parameters-") as temporary:
            root = Path(temporary)
            data = root / "inputs/data.csv"
            data.parent.mkdir()
            with data.open("w", newline="") as stream:
                writer = csv.writer(stream)
                writer.writerow([f"x{i}" for i in range(16)] + ["label"])
                writer.writerows([[0] * 16 + [i % 2] for i in range(17)])
            contents = data.read_bytes()
            outputs = []
            for seed in (7, 4294967295):
                output = root / f"output-{seed}"
                result = self.run_python(
                    TRAINING_ROOT / "train_mlp.py",
                    *manifest_arguments(data),
                    "--output",
                    output,
                    "--epochs",
                    "2",
                    "--batch-size",
                    "6",
                    "--learning-rate",
                    "1000",
                    "--seed",
                    str(seed),
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                summary = json.loads((output / "summary.json").read_text())
                self.assertEqual(
                    (
                        summary["samples"],
                        summary["epochs"],
                        summary["batch_size"],
                        summary["steps"],
                    ),
                    (17, 2, 6, 6),
                )
                self.assertEqual(summary["seed"], seed)
                self.assertEqual(summary["learning_rate"], "1000")
                self.assertEqual(summary["status"], "SUCCEEDED")
                self.assertTrue(math.isfinite(summary["final_loss"]))
                self.assertGreater(summary["final_loss"], summary["initial_loss"])
                reloaded = self.run_python(
                    Path(__file__).with_name("reload_checkpoint.py"), output
                )
                self.assertEqual(reloaded.returncode, 0, reloaded.stderr)
                outputs.append(output)
            comparison = self.run_python(
                Path(__file__).with_name("compare_checkpoints.py"), *outputs
            )
            self.assertEqual(comparison.returncode, 0, comparison.stderr)

    def test_invalid_learning_rate_is_rejected_before_training(self):
        dependency = self.run_python(
            "-c",
            "import torch; assert torch.version.cuda is None, 'CPU wheel required'",
        )
        self.assertEqual(
            dependency.returncode,
            0,
            f"ENVIRONMENT_NOT_READY (not a behavior RED)\n{dependency.stderr}",
        )
        invalid_rates = (
            "",
            "0",
            "-0.01",
            "999999999999999999999999999999999",
            "NaN",
            "Inf",
            "-Inf",
            "1e-2",
            "+0.02",
            "00.02",
            " 0.02",
            "0." + "0" * 31 + "1",
        )
        with tempfile.TemporaryDirectory(prefix="cpu03-rejected-rate-") as temporary:
            root = Path(temporary)
            data = root / "inputs/data.csv"
            data.parent.mkdir()
            generated = self.run_python(
                TRAINING_ROOT / "make_data.py", "--output", data
            )
            self.assertEqual(generated.returncode, 0, generated.stderr)
            input_bytes = data.read_bytes()
            for number, rate in enumerate(invalid_rates):
                with self.subTest(learning_rate=rate):
                    output = root / f"rejected-{number}"
                    rejected = self.run_python(
                        TRAINING_ROOT / "train_mlp.py",
                        *manifest_arguments(data),
                        "--output",
                        output,
                        "--learning-rate",
                        rate,
                    )
                    self.assertNotEqual(rejected.returncode, 0, rejected.stdout)
                    self.assertNotIn('"train.loss"', rejected.stdout)
                    self.assertFalse(
                        output.exists(), "invalid learning rate must not create output"
                    )
                    self.assertEqual(data.read_bytes(), input_bytes)

    def test_managed_failure_recipe_optimizes_five_steps_then_fails_without_success_artifacts(
        self,
    ):
        dependency = self.run_python(
            "-c",
            "import torch; assert torch.version.cuda is None, 'CPU wheel required'",
        )
        self.assertEqual(
            dependency.returncode,
            0,
            f"ENVIRONMENT_NOT_READY (not a behavior RED)\n{dependency.stderr}",
        )
        with tempfile.TemporaryDirectory(prefix="cpu03-fail-recipe-") as temporary:
            root = Path(temporary)
            data = root / "inputs/data.csv"
            data.parent.mkdir()
            output = root / "execution-output"
            generated = self.run_python(
                TRAINING_ROOT / "make_data.py", "--output", data
            )
            self.assertEqual(generated.returncode, 0, generated.stderr)
            selected_bytes = data.read_bytes()
            failed = self.run_python(
                TRAINING_ROOT / "train_mlp.py",
                *manifest_arguments(data),
                "--output",
                output,
                "--recipe",
                "fail",
            )
            self.assertNotEqual(failed.returncode, 0)
            events = [json.loads(line) for line in failed.stdout.splitlines()]
            metrics = [event for event in events if event.get("name") == "train.loss"]
            self.assertEqual(
                [event["step"] for event in metrics],
                [1, 2, 3, 4, 5],
                f"managed failure must follow real optimization\n{failed.stderr}",
            )
            self.assertTrue(all(math.isfinite(event["value"]) for event in metrics))
            self.assertIn("CPU03_RECIPE_FAILURE", failed.stderr)
            self.assertEqual(data.read_bytes(), selected_bytes)
            self.assertEqual(
                {path.name for path in output.iterdir()},
                {"metrics.jsonl", "summary.json"},
                "failed training must retain diagnostics without a successful checkpoint/candidate",
            )
            persisted = [
                json.loads(line)
                for line in (output / "metrics.jsonl")
                .read_text(encoding="utf-8")
                .splitlines()
            ]
            self.assertEqual(persisted, metrics)

    def test_managed_slow_recipe_remains_stoppable_after_owner_window_without_success_artifacts(
        self,
    ):
        dependency = self.run_python(
            "-c",
            "import torch; assert torch.version.cuda is None, 'CPU wheel required'",
        )
        self.assertEqual(
            dependency.returncode,
            0,
            f"ENVIRONMENT_NOT_READY (not a behavior RED)\n{dependency.stderr}",
        )
        with tempfile.TemporaryDirectory(prefix="cpu03-stop-recipe-") as temporary:
            root = Path(temporary)
            data = root / "inputs/data.csv"
            data.parent.mkdir()
            output = root / "execution-output"
            stdout_path, stderr_path = root / "stdout.log", root / "stderr.log"
            generated = self.run_python(
                TRAINING_ROOT / "make_data.py", "--output", data
            )
            self.assertEqual(generated.returncode, 0, generated.stderr)
            selected_bytes = data.read_bytes()
            command = [
                sys.executable,
                "-I",
                str(TRAINING_ROOT / "train_mlp.py"),
                *manifest_arguments(data),
                "--output",
                str(output),
                "--recipe",
                "slow-stop",
            ]
            environment = os.environ.copy()
            environment.update({"CUDA_VISIBLE_DEVICES": "", "WORLD_SIZE": "1"})
            events, observed_metrics = [], []
            with (
                stdout_path.open("w", encoding="utf-8") as stdout,
                stderr_path.open("w", encoding="utf-8") as stderr,
            ):
                process = subprocess.Popen(
                    command, stdout=stdout, stderr=stderr, env=environment
                )
                try:
                    observe_deadline = time.monotonic() + 45
                    with stdout_path.open(encoding="utf-8") as reader:
                        while (
                            len(observed_metrics) < 3
                            and time.monotonic() < observe_deadline
                        ):
                            line = reader.readline()
                            if line:
                                event = json.loads(line)
                                events.append(event)
                                if event.get("name") == "train.loss":
                                    observed_metrics.append(event)
                            elif process.poll() is not None:
                                break
                            else:
                                time.sleep(0.02)
                        self.assertGreaterEqual(
                            len(observed_metrics),
                            3,
                            "slow-stop must perform real optimization before the stop request\n"
                            + stderr_path.read_text(encoding="utf-8"),
                        )
                        # Real close-owner/API reconciliation can take more than
                        # ten seconds after an observed step. Keep training real
                        # and observe its progress throughout that stop window.
                        hold_started = time.monotonic()
                        hold_deadline = hold_started + 12.5
                        while time.monotonic() < hold_deadline:
                            line = reader.readline()
                            if line:
                                event = json.loads(line)
                                events.append(event)
                                if event.get("name") == "train.loss":
                                    observed_metrics.append(event)
                            else:
                                time.sleep(0.02)
                    held_seconds = time.monotonic() - hold_started
                    self.assertGreaterEqual(held_seconds, 12)
                    self.assertIsNone(
                        process.poll(),
                        "stop must target a still-running training process after the owner window; "
                        f"held_seconds={held_seconds:.3f}, exit={process.returncode}, "
                        f"real_metrics={len(observed_metrics)}\n"
                        + stderr_path.read_text(encoding="utf-8"),
                    )
                    self.assertGreaterEqual(
                        len(observed_metrics),
                        9,
                        "slow-stop must continue real optimization during the owner window",
                    )
                    self.assertLess(len(observed_metrics), 48)
                    descriptors = [
                        event
                        for event in events
                        if event.get("schema") == "ani.cpu03.recipe.v1"
                    ]
                    self.assertEqual(len(descriptors), 1)
                    self.assertEqual(descriptors[0]["recipe"], "slow-stop")
                    self.assertEqual(descriptors[0]["max_steps"], 48)
                    self.assertEqual(descriptors[0]["step_delay_seconds"], 2)
                    self.assertEqual(descriptors[0]["deadline_seconds"], 180)
                    self.assertTrue(
                        all(math.isfinite(event["value"]) for event in observed_metrics)
                    )
                    process.terminate()
                    exit_code = process.wait(timeout=10)
                    self.assertEqual(exit_code, 128 + signal.SIGTERM)
                    print(
                        f"slow-stop owner-window behavior: held_seconds={held_seconds:.3f}, "
                        f"real_metrics={len(observed_metrics)}, exit={exit_code}",
                        flush=True,
                    )
                finally:
                    if process.poll() is None:
                        process.kill()
                        process.wait(timeout=5)
            self.assertEqual(data.read_bytes(), selected_bytes)
            self.assertEqual(
                {path.name for path in output.iterdir()},
                {"metrics.jsonl", "summary.json"},
                "stopped training must not report a successful checkpoint or candidate",
            )
            persisted = [
                json.loads(line)
                for line in (output / "metrics.jsonl")
                .read_text(encoding="utf-8")
                .splitlines()
            ]
            self.assertGreaterEqual(len(persisted), 3)
            self.assertLess(len(persisted), 48)
            self.assertEqual(
                [event["step"] for event in persisted],
                list(range(1, len(persisted) + 1)),
            )
            emitted_metrics = [
                event
                for event in map(
                    json.loads, stdout_path.read_text(encoding="utf-8").splitlines()
                )
                if event.get("name") == "train.loss"
            ]
            self.assertEqual(persisted, emitted_metrics)


if __name__ == "__main__":
    unittest.main()

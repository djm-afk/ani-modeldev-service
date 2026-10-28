"""Fedora-only smoke of the packaged image, including its actual PID 1 entrypoint.

All containers are task-owned, offline, resource-limited and time-bounded.
This is CPU03 image evidence, not BFF/Trainer/S3/L4 verification.
"""

import hashlib
import json
import math
from pathlib import Path
import re
import subprocess
import sys
import time

from dataset_fixture import manifest_arguments


def require(condition, message):
    if not condition:
        raise ValueError(message)


def main():
    require(len(sys.argv) == 3, "usage: image_smoke.py IMAGE_ID NEW_RUN_DIRECTORY")
    image_id = sys.argv[1]
    require(re.fullmatch(r"sha256:[0-9a-f]{64}", image_id), "use the immutable local image ID")
    run_dir = Path(sys.argv[2])
    require(run_dir.is_absolute() and not run_dir.exists(), "run directory must be new and absolute")
    run_dir.mkdir(mode=0o700)
    source_root = Path(__file__).resolve().parents[2]
    records = []

    def command(arguments, label, *, check=True, timeout=90):
        records.append({"label": label, "argv": arguments})
        (run_dir / "commands.json").write_text(json.dumps(records, indent=2) + "\n")
        try:
            completed = subprocess.run(arguments, text=True, capture_output=True, timeout=timeout)
        except subprocess.TimeoutExpired as error:
            (run_dir / f"{label}.stdout").write_bytes(error.stdout or b"")
            (run_dir / f"{label}.stderr").write_bytes(error.stderr or b"")
            (run_dir / f"{label}.exit").write_text("TIMEOUT\n")
            raise
        (run_dir / f"{label}.stdout").write_text(completed.stdout)
        (run_dir / f"{label}.stderr").write_text(completed.stderr)
        (run_dir / f"{label}.exit").write_text(f"{completed.returncode}\n")
        if check and completed.returncode:
            raise RuntimeError(f"{label} failed with exit {completed.returncode}; see evidence")
        return completed

    source_sha = command(["git", "-C", str(source_root), "rev-parse", "HEAD"], "source").stdout.strip()
    command(["git", "-C", str(source_root), "diff", "--exit-code", "HEAD", "--", "training/"], "source-clean")
    image = json.loads(command(["podman", "image", "inspect", image_id], "image").stdout)[0]
    require(image["Config"]["User"] == "10001:10001", "image must declare the fixed nonroot UID/GID")
    require(image["Config"]["Labels"]["org.opencontainers.image.revision"] == source_sha, "image revision differs from smoke source")
    require(image["Config"]["Entrypoint"] == ["/opt/venv/bin/python", "-I", "/opt/cpu03/train_mlp.py"], "unexpected image entrypoint")
    common = [
        "podman", "run", "--timeout", "90", "--pull", "never", "--network", "none",
        "--http-proxy=false", "--cpus", "2", "--memory", "2g", "--memory-swap", "2g",
        "--pids-limit", "256", "--cap-drop", "all", "--security-opt", "no-new-privileges",
        "--read-only", "--tmpfs", "/tmp:rw,size=128m", "--userns", "keep-id:uid=10001,gid=10001",
        "--passwd=false", "--env", "USER=", "--env", "LOGNAME=", "--env", "LNAME=", "--env", "USERNAME=",
    ]
    command(common + ["--rm", "--entrypoint", "/opt/venv/bin/python", image_id, "-I", "-c",
        "import getpass,grp,json,os,platform,pwd,sys,sysconfig,torch; "
        "assert os.getuid()==10001 and os.getgid()==10001 and os.getpid()==1; "
        "assert not any(os.environ.get(name) for name in ('USER','LOGNAME','LNAME','USERNAME')); "
        "user=pwd.getpwuid(10001); group=grp.getgrgid(10001); "
        "assert user.pw_name=='modeldev' and user.pw_uid==10001 and user.pw_gid==10001; "
        "assert group.gr_name=='modeldev' and group.gr_gid==10001; "
        "assert getpass.getuser()=='modeldev'; "
        "assert platform.python_version()=='3.14.8'; "
        "assert sys.prefix=='/opt/venv' and sys.base_prefix=='/usr'; "
        "assert sysconfig.get_config_var('SOABI')=='cpython-314-x86_64-linux-gnu'; "
        "assert not sysconfig.get_config_var('Py_GIL_DISABLED'); "
        "assert torch.__version__=='2.10.0+cpu'; assert torch.version.cuda is None; "
        "print(json.dumps({'uid':os.getuid(),'gid':os.getgid(),'user':user.pw_name,'group':group.gr_name,"
        "'python':platform.python_version(),'torch':torch.__version__,'cuda':torch.version.cuda}))"
    ], "runtime")
    package_probe = r"""import json
from pathlib import Path
packages = {}
for stanza in Path('/lib/apk/db/installed').read_text().split('\n\n'):
    fields = dict(line.split(':', 1) for line in stanza.splitlines() if ':' in line)
    if 'P' in fields:
        assert fields['P'] not in packages
        packages[fields['P']] = fields['V']
print(json.dumps(packages, sort_keys=True))
"""
    system_packages = json.loads(command(common + ["--rm", "--entrypoint", "/usr/bin/python", image_id,
        "-I", "-c", package_probe], "system-packages").stdout)
    package_lock = json.loads((source_root / "training/runtime-packages.lock.json").read_text())
    require(system_packages == package_lock["packages"], "complete runtime APK package set differs from lock")
    command(common + ["--rm", "--entrypoint", "/opt/venv/bin/python", image_id,
        "-I", "-m", "pip", "check"], "dependencies")
    command(common + ["--rm", "--entrypoint", "/opt/venv/bin/python", image_id, "-I", "-c",
        "import setuptools,filelock,fsspec,jinja2,markupsafe,mpmath,networkx,sympy,typing_extensions; "
        "assert str(markupsafe.escape('<cpu03>'))=='&lt;cpu03&gt;'; "
        "assert jinja2.Template('{{ value }}').render(value='cpu03')=='cpu03'; "
        "print('All locked dependency modules imported; MarkupSafe and Jinja2 executed.')"
    ], "dependency-imports")

    selected = run_dir / "selected"
    selected.mkdir()
    data_script = source_root / "training" / "make_data.py"
    command(common + ["--rm", "--entrypoint", "/opt/venv/bin/python",
        "-v", f"{data_script}:/tool/make_data.py:ro,Z", "-v", f"{selected}:/selected:rw,Z",
        image_id, "-I", "/tool/make_data.py", "--output", "/selected/input.csv"], "fixture")
    inputs = selected / "inputs"
    inputs.mkdir()
    data = inputs / "data.csv"
    (selected / "input.csv").replace(data)
    manifest_sha = manifest_arguments(data)[-1]
    selected_bytes = data.read_bytes()
    data_hash = hashlib.sha256(selected_bytes).hexdigest()
    (run_dir / "input-identity.json").write_text(json.dumps({"bytes": len(selected_bytes), "sha256": data_hash}) + "\n")
    input_arguments = ["--dataset-manifest", "/selected/inputs/dataset-manifest.json",
        "--expected-dataset-manifest-sha256", manifest_sha, "--work-root", "/work/train",
        "--output", "/output", "--learning-rate", "0.01"]


    def workload(recipe, output, *, detached=False):
        output.mkdir()
        run_options = ["--detach"] if detached else ["--rm"]
        return common + run_options + ["-v", f"{selected}:/selected:ro,Z", "-v", f"{output}:/output:rw,Z",
            "--tmpfs", "/work:rw,size=256m,mode=1777", image_id] + input_arguments + ["--recipe", recipe]

    def metrics(output):
        entries = [json.loads(line) for line in (output / "metrics.jsonl").read_text().splitlines()]
        require(all(entry["name"] == "train.loss" and math.isfinite(entry["value"]) for entry in entries), "invalid optimization metrics")
        require([entry["step"] for entry in entries] == list(range(1, len(entries) + 1)), "noncontiguous optimization steps")
        return entries

    success = run_dir / "success"
    command(workload("success", success), "success")
    require(len(metrics(success)) == 48, "success must optimize 48 times")
    expected = {"model.pt", "model_config.json", "metrics.jsonl", "summary.json", "result.json"}
    require({item.name for item in success.iterdir()} == expected, "unexpected success artifacts")
    summary = json.loads((success / "summary.json").read_text())
    require(summary["steps"] == 48 and summary["samples"] == 1024 and summary["device"] == "cpu", "wrong recipe summary")
    require(summary["input_sha256"] == data_hash and int(summary["input_bytes"]) == len(selected_bytes), "wrong selected input")
    candidate = json.loads((success / "result.json").read_text())
    require(candidate["schema"] == "ani.output-candidate.v1" and candidate["deployable"] is False, "wrong candidate classification")
    require({item["path"] for item in candidate["files"]} == expected - {"result.json"}, "wrong candidate files")
    require(len(candidate["files"]) == 4, "duplicate candidate files")
    for item in candidate["files"]:
        contents = (success / item["path"]).read_bytes()
        require(len(contents) == int(item["bytes"]) and hashlib.sha256(contents).hexdigest() == item["sha256"], "candidate bytes do not match")
    reload_script = source_root / "training" / "tests" / "reload_checkpoint.py"
    command(common + ["--rm", "--entrypoint", "/opt/venv/bin/python",
        "-v", f"{reload_script}:/tool/reload_checkpoint.py:ro,Z", "-v", f"{success}:/output:ro,Z",
        image_id, "-I", "/tool/reload_checkpoint.py", "/output"], "reload")

    failure = run_dir / "failure"
    failed = command(workload("fail", failure), "failure", check=False)
    require(failed.returncode != 0 and "CPU03_RECIPE_FAILURE" in failed.stderr, "failure recipe must fail explicitly")
    require(len(metrics(failure)) == 5, "failure recipe must optimize five times")
    require({item.name for item in failure.iterdir()} == {"metrics.jsonl"}, "failure has successful artifacts")

    stopped = run_dir / "stopped"
    container_id = command(workload("slow-stop", stopped, detached=True), "stop-start").stdout.strip()
    require(re.fullmatch(r"[0-9a-f]{64}", container_id), "invalid task-owned container ID")
    try:
        deadline = time.monotonic() + 45
        observed = []
        while time.monotonic() < deadline:
            log = command(["podman", "logs", container_id], "stop-observe", check=False)
            observed = [json.loads(line) for line in log.stdout.splitlines() if line.strip()]
            if len([event for event in observed if event.get("name") == "train.loss"]) >= 3:
                break
            time.sleep(0.1)
        actual_steps = [event for event in observed if event.get("name") == "train.loss"]
        require(len(actual_steps) >= 3, "no observed real optimization before stop")
        running = command(["podman", "inspect", "--format", "{{.State.Running}}", container_id], "stop-running").stdout.strip()
        require(running == "true", "stop must target live training")
        command(["podman", "kill", "--signal", "TERM", container_id], "stop-signal")
        waited = command(["podman", "wait", container_id], "stop-wait", timeout=10)
        require(waited.stdout.strip() == "143", "expected SIGTERM exit 143")
        command(["podman", "logs", container_id], "stop-final-logs")
        require(3 <= len(metrics(stopped)) < 48, "stop did not interrupt actual training")
        require({item.name for item in stopped.iterdir()} == {"metrics.jsonl"}, "stop has successful artifacts")
    finally:
        command(["podman", "rm", "--force", container_id], "stop-owned-cleanup", check=False)

    require(data.read_bytes() == selected_bytes, "selected input changed")
    (run_dir / "result.json").write_text(json.dumps({"status": "PASS", "source_sha": source_sha, "image_id": image_id,
        "scope": "CPU03 offline image smoke; no target cluster, BFF, KFP, Trainer or S3 verification"}, indent=2) + "\n")
    print(f"CPU03 image smoke passed: {image_id}")


if __name__ == "__main__":
    main()

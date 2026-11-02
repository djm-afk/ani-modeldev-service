#!/usr/bin/env python3
"""Fedora-only fixed-file build; no archive, pull, push, or cluster operations."""
import datetime
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import time

work = Path(sys.argv[1]).resolve()
snapshot = work / 'inputs-d807'
manifest = json.loads((work / 'build-inputs.json').read_text())
store = '/home/chabking/workspace/modeldev-open-limits-20261028-01a182e4/runtime-image-storage'
runroot = '/run/user/1000/modeldev-open-limits-image-storage'
tmp = '/home/chabking/workspace/modeldev-open-limits-20261028-01a182e4/runtime-image-tmp'
podman = ['podman', '--root', store, '--runroot', runroot, '--tmpdir', tmp, '--storage-driver', 'overlay']
go = '/home/chabking/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.10.linux-amd64/bin/go'
source = work / 'source'
result = {'source_commit': manifest['source_commit'], 'source_git_tree': manifest['source_git_tree'],
          'input_files_canonical_sha256': manifest['input_files_canonical_sha256'],
          'work_directory': str(work), 'storage_root': store, 'runroot': runroot, 'tmpdir': tmp,
          'started_at_utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
          'commands': [], 'images': {}, 'push': 'NOT_RUN', 'rollout': 'NOT_RUN',
          'training_cpu': 'BLOCKED_CACHED_OFFICIAL_BASE_MISSING',
          'training_gpu': 'BLOCKED_CACHED_OFFICIAL_BASE_MISSING'}


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def save():
    (work / 'result.json').write_text(json.dumps(result, indent=2) + '\n')


def run(name, command, cwd=None, env=None, timeout=600, expected=0):
    started = time.monotonic()
    with (work / (name + '.log')).open('w') as log:
        completed = subprocess.run(command, cwd=cwd, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=timeout)
    entry = {'name': name, 'command': command, 'cwd': str(cwd) if cwd else None,
             'exit': completed.returncode, 'expected_exit': expected,
             'seconds': round(time.monotonic() - started, 3), 'log': str(work / (name + '.log'))}
    result['commands'].append(entry)
    save()
    print(json.dumps(entry), flush=True)
    if completed.returncode != expected:
        raise RuntimeError(name + ' failed: see retained remote log')


try:
    assert manifest['source_commit'] == 'd807cbdbd5dd6664d61480acedc752e19cfca48a'
    mismatches = []
    for relative, expected in manifest['input_files'].items():
        path = snapshot / relative
        if not path.is_file() or path.is_symlink() or digest(path) != expected:
            mismatches.append(relative)
    result['source_mismatches'] = mismatches
    save()
    if mismatches:
        raise RuntimeError('fixed source mismatch; no compilation attempted')
    for relative, expected in manifest['input_files'].items():
        target = source / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        if not target.exists():
            target.write_bytes((snapshot / relative).read_bytes())
        assert digest(target) == expected
    result['copied_verified_files'] = len(manifest['input_files'])
    binaries = work / 'bin'
    binaries.mkdir(exist_ok=True)
    environment = dict(os.environ, GOWORK='off', GOTOOLCHAIN='local', GOPROXY='off', GOSUMDB='off',
                       CGO_ENABLED='0', GOOS='linux', GOARCH='amd64', GOMAXPROCS='2', GOFLAGS='-p=2',
                       GOMEMLIMIT='2GiB', TMPDIR=tmp)
    result['go_binary'] = {'path': go, 'sha256': digest(Path(go))}
    run('go-version', [go, 'version'], cwd=source, env=environment, timeout=30)
    result['go_version'] = (work / 'go-version.log').read_text().strip()
    run('build-binaries', [go, 'build', '-mod=readonly', '-buildvcs=false', '-trimpath',
                          '-o', str(binaries) + '/', './cmd/ani-modeldev-step', './cmd/ani-modeldev-webhook'],
        cwd=source, env=environment, timeout=600)
    result['binaries'] = {p.name: {'bytes': p.stat().st_size, 'sha256': digest(p)} for p in binaries.iterdir()}
    for kind, dockerfile, binary in [
        ('step', 'pipelines/step.Dockerfile', 'ani-modeldev-step'),
        ('webhook', 'deploy/modeldev-webhook/Dockerfile', 'ani-modeldev-webhook')]:
        context = work / (kind + '-context')
        context.mkdir()
        shutil.copy2(binaries / binary, context / binary)
        shutil.copy2(source / dockerfile, context / 'Dockerfile')
        tag = 'localhost/ani-modeldev-' + kind + ':tenantenv-d807cbdbd5dd-' + work.name.rsplit('-', 1)[-1]
        exists = subprocess.run(podman + ['image', 'exists', tag], capture_output=True)
        if exists.returncode != 1:
            raise RuntimeError('new image tag is not provably absent')
        command = podman + ['build', '--pull=never', '--network=none', '--http-proxy=false', '--format=oci',
                           '--platform=linux/amd64', '--cpu-period=100000', '--cpu-quota=200000',
                           '--memory=2g', '--memory-swap=2g', '--cap-drop=all',
                           '--security-opt=no-new-privileges', '--jobs=1',
                           '--build-arg', 'SOURCE_REVISION=' + manifest['source_commit'],
                           '--build-arg', 'SOURCE_TREE=' + manifest['source_git_tree'],
                           '--label', 'io.ani.build-inputs-sha256=' + manifest['input_files_canonical_sha256'],
                           '--iidfile', str(work / (kind + '.iid')), '--tag', tag, str(context)]
        run('build-' + kind, command, env=environment, timeout=180)
        inspected = subprocess.run(podman + ['image', 'inspect', tag], capture_output=True, text=True, check=True, timeout=25)
        image = json.loads(inspected.stdout)[0]
        (work / (kind + '-inspect.json')).write_text(inspected.stdout)
        config = image['Config']
        assert config['Labels']['org.opencontainers.image.revision'] == manifest['source_commit']
        assert config['Labels']['io.ani.source-tree'] == manifest['source_git_tree']
        assert config['User'] == '10001:10001'
        result['images'][kind] = {k: image.get(k) for k in ['Id', 'Digest', 'Os', 'Architecture', 'Size', 'RepoTags']}
        result['images'][kind].update(tag=tag, labels=config['Labels'], binary_sha256=digest(binaries / binary),
                                     dockerfile_sha256=digest(source / dockerfile), user=config['User'])
        run('missing-config-' + kind, podman + ['run', '--rm', '--network=none', '--read-only', '--cap-drop=all',
            '--security-opt=no-new-privileges', '--cpus=1', '--memory=512m', '--pids-limit=64',
            '--tmpfs=/tmp:rw,nosuid,nodev,size=64m', tag], env=environment, timeout=30, expected=1)
    result['status'] = 'STEP_WEBHOOK_LOCAL_PASS_TRAINING_BLOCKED_BASE_MISSING'
except Exception as error:
    result['status'] = 'BLOCKED'
    result['reason'] = type(error).__name__ + ': ' + str(error)
    print(json.dumps({'status': result['status'], 'reason': result['reason']}), flush=True)
    raise
finally:
    result['finished_at_utc'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
    save()

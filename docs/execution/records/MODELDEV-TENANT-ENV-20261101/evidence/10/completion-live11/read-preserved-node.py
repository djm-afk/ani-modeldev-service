import base64
import datetime
import getpass
import json
import os
import socket
import subprocess
import sys

import yaml

CTX = 'kubernetes-admin@ani-kcn-full'


def k(*args):
    p = subprocess.run(['kubectl', '--context', CTX, '--request-timeout=10s', *args, '-o', 'json'], capture_output=True, timeout=20)
    if p.returncode:
        raise RuntimeError('KUBERNETES_READ_FAILED:' + args[-1])
    return json.loads(p.stdout)


def require(ok, message):
    if not ok:
        raise RuntimeError(message)


def observe(o):
    m, s = o['metadata'], o.get('status', {})
    ready = next((c['status'] for c in s.get('conditions', []) if c['type'] == 'Ready'), s.get('ready'))
    return {'kind': o['kind'], 'namespace': m.get('namespace'), 'name': m['name'], 'uid': m['uid'], 'deleting': bool(m.get('deletionTimestamp')), 'phase': s.get('phase'), 'ready': str(ready).lower() if ready is not None else None, 'volume_name': o.get('spec', {}).get('volumeName')}


def run(baseline):
    require(getpass.getuser() == 'kubercloud' and socket.gethostname() == 'node1' and os.environ.get('SSH_CONNECTION'), 'SOLE_ENTRY_REQUIRED')
    c = k('config', 'view', '--minify')
    require(c['current-context'] == CTX and c['clusters'][0]['cluster']['server'] == 'https://lb.kubesphere.local:6443', 'CONTEXT_API_MISMATCH')
    require(k('get', 'ns', 'kube-system')['metadata']['uid'] == '3eeb5379-8748-4bf8-8f6d-d7a48ce3bc24', 'CLUSTER_UID_MISMATCH')
    secret = k('-n', 'ani-system', 'get', 'secret', 'ani-resource-private')
    require(yaml.safe_load(base64.b64decode(secret['data']['config.yaml']))['network']['cluster_id'] == 'f39bf6e2-f3ed-4191-ada3-caeebf86c220', 'CLUSTER_ID_MISMATCH')
    current = {}
    for resource in ['pvc', 'vm.kubevirt.io', 'vmi.kubevirt.io', 'pods']:
        for o in k('get', resource, '-A')['items']:
            row = observe(o)
            current[(row['kind'], row['namespace'], row['name'])] = row
    result = {'schema': 'ani.modeldev.final-preserved-assets.v1', 'observed_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'read_only': True, 'identity': baseline['identity'], 'identity_match': True, 'baseline_observed_at': baseline['observed_at'], 'assets': [], 'namespace_samples': [], 'unrelated_pods': [], 'mutations': 0}
    for before in baseline['assets']:
        after = current.get((before['kind'], before['namespace'], before['name']))
        same_uid = bool(after and after['uid'] == before['uid'])
        before_ready = str(before.get('ready')).lower() if before.get('ready') is not None else None
        same_health = bool(after and before.get('phase') == after['phase'] and before_ready == after['ready'] and before.get('deleting', False) == after['deleting'])
        if before['kind'] == 'PersistentVolumeClaim':
            same_health = bool(after and after['phase'] == before['phase'] and after['volume_name'] == before['volume_name'] and not after['deleting'])
        result['assets'].append({'before': {x: before.get(x) for x in ['kind', 'namespace', 'name', 'uid', 'phase', 'ready', 'volume_name']}, 'after': after, 'same_uid': same_uid, 'same_health': same_health})
    for before in baseline['namespace_samples']:
        ns = k('get', 'ns', before['observed']['name'])
        result['namespace_samples'].append({'name': ns['metadata']['name'], 'uid': ns['metadata']['uid'], 'same_uid': ns['metadata']['uid'] == before['expected_namespace_uid'], 'phase': ns['status']['phase'], 'not_deleting': not ns['metadata'].get('deletionTimestamp'), 'same_network_labels': all(ns['metadata'].get('labels', {}).get(key) == value for key, value in before['network_labels'].items())})
    for before in baseline['unrelated_A_B_workload_pods']:
        after = current.get(('Pod', before['namespace'], before['name']))
        result['unrelated_pods'].append({'before': before, 'after': after, 'same_uid': bool(after and after['uid'] == before['uid']), 'same_health': bool(after and after['phase'] == before['phase'] and after['ready'] == str(before['ready']).lower() and not after['deleting'])})
    result['all_original_assets_unchanged'] = all(x['same_uid'] and x['same_health'] for x in result['assets'])
    result['all_original_namespaces_unchanged'] = all(x['same_uid'] and x['same_network_labels'] and x['not_deleting'] and x['phase'] == 'Active' for x in result['namespace_samples'])
    result['all_original_unrelated_pods_unchanged'] = all(x['same_uid'] and x['same_health'] for x in result['unrelated_pods'])
    print(json.dumps(result))

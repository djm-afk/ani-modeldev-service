#!/usr/bin/env python3
"""Exact original credential projection -> one fixed Fedora helper via stdin."""
import hashlib
import json
from pathlib import Path
import subprocess
import sys

BASE = Path(__file__).resolve().parent
HELPER = BASE.parents[1] / "09" / "workbench-maintenance-job.py"
REMOTE_FETCH = r'''
i=identity()
d=kget('-n','ani-system','get','deployment','ani-resource-service')
assert d['metadata']['uid']=='9b17ed7e-6521-40e8-8bf5-70a44237e501'
spec=d['spec']['template']['spec']
v=next(v for v in spec['volumes'] if v['name']=='input')
projection=next(x['secret'] for x in v['projected']['sources'] if x.get('secret',{}).get('name')=='ani-resource-private')
items={x['path']:x['key'] for x in projection['items']}
assert items['harbor.password']=='harbor.password' and items['harbor-ca.crt']=='harbor-ca.crt'
init=next(c for c in spec['initContainers'] if c['name']=='private-regular-files')
assert any(m['name']=='input' and m['mountPath']=='/input' and m.get('readOnly') for m in init['volumeMounts'])
assert any(m['name']=='private' and m['mountPath']=='/private' for m in init['volumeMounts'])
s=kget('-n','ani-system','get','secret','ani-resource-private')
assert s['metadata']['uid']=='54303da4-f97a-451d-b186-4d1a35d0c466'
import yaml
im=yaml.safe_load(base64.b64decode(s['data']['config.yaml']))['image']
assert im['harbor_url']=='https://10.10.1.66:30003' and im['platform_project']=='ani-resource-factor10-20261022'
assert im['harbor_password_file']=='/private/harbor.password' and im['harbor_ca_file']=='/private/harbor-ca.crt'
password=base64.b64decode(s['data'][items['harbor.password']],validate=True).decode().strip()
ca=base64.b64decode(s['data'][items['harbor-ca.crt']],validate=True)
assert hashlib.sha256(ca).hexdigest()=='65f1a1d14d21fed31b89f25b143e78b16605b82d32c56ef6bf8167fe78bab871'
print(json.dumps({'identity_match':True,'authority':'10.10.1.66:30003','project':im['platform_project'],'username':im['harbor_username'],'password':password,'ca_pem_b64':base64.b64encode(ca).decode(),'public_source':{'identity':i,'deployment_uid':d['metadata']['uid'],'deployment_resource_version':d['metadata']['resourceVersion'],'config_secret_uid':s['metadata']['uid'],'password_source':'ani-system/ani-resource-private:harbor.password','ca_source':'ani-system/ani-resource-private:harbor-ca.crt','ca_sha256':hashlib.sha256(ca).hexdigest(),'init_container':'private-regular-files','projected_mount':'input:/input readonly -> private:/private','credentials_emitted':False}}))
'''


def main():
    remote_script, output_name = sys.argv[1:]
    assert remote_script.startswith("/home/chabking/tenantenv-20261101/images-d807-cymq2rk8/") and remote_script.endswith(".py")
    assert "/" not in output_name and output_name.endswith(".json")
    source = "__name__='original_registry_source'\n" + HELPER.read_text() + REMOTE_FETCH
    read = subprocess.run(["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=12", "kubercloud@100.64.128.12", "python3", "-"], input=source.encode(), capture_output=True, timeout=90)
    if read.returncode:
        result = {"stage": "CREDENTIAL_SOURCE", "result": "READ_FAILED_NO_FEDORA_COMMAND", "ssh_exit": read.returncode, "stderr_sha256": hashlib.sha256(read.stderr).hexdigest()}
    else:
        credentials = json.loads(read.stdout)
        executed = subprocess.run(["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=12", "fedora", "python3", remote_script], input=json.dumps(credentials).encode(), capture_output=True, timeout=900)
        try:
            result = json.loads(executed.stdout)
            result["fedora_ssh_exit"] = executed.returncode
        except Exception:
            result = {"result": "REMOTE_COMMAND_RESULT_UNKNOWN", "ssh_exit": executed.returncode, "stdout_sha256": hashlib.sha256(executed.stdout).hexdigest(), "stderr_sha256": hashlib.sha256(executed.stderr).hexdigest()}
    target = BASE / output_name
    assert not target.exists(), "preserve prior attempt receipt"
    target.write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result))


if __name__ == "__main__":
    main()

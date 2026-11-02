#!/usr/bin/env python3
import hashlib,importlib.util,json,subprocess
from pathlib import Path
b=Path(__file__).resolve().parent
helper=(b.parents[1]/'09/workbench-maintenance-job.py').read_text()
spec=importlib.util.spec_from_file_location('credential_source',b/'credential-source-and-run.py');mod=importlib.util.module_from_spec(spec);spec.loader.exec_module(mod)
runner="__name__='node_small_library'\n"+helper+"\nREGISTRY_READ="+repr(mod.REMOTE_FETCH)+"\n__name__='__main__'\n"+(b/'node-small-build.py').read_text()
remote="__name__='start_small_library'\n"+helper+"\nfrom pathlib import Path\nimport hashlib,json,subprocess\nchecked=identity()\nroot=Path('/home/kubercloud/tenantenv-20261101/images-d807-build-20261101')\nassert json.loads((root/'source-identity.json').read_text())['source_commit']=='d807cbdbd5dd6664d61480acedc752e19cfca48a'\nassert not (root/'small-receipt.json').exists(),'preserve previous execution; query existing receipt instead'\nrunner="+repr(runner)+"\np=root/'context/node-small-build-run.py';p.write_text(runner);p.chmod(0o700)\nargs=['sudo','-n','systemd-run','--unit=ani-modeldev-d807-small-runner.service','--collect','--uid=kubercloud','--property=StandardOutput=append:'+str(root/'small-runner.log'),'--property=StandardError=append:'+str(root/'small-runner.log'),'/usr/bin/python3',str(p)]\nr=subprocess.run(args,check=True,capture_output=True)\nprint(json.dumps({'identity':checked,'runner_sha256':hashlib.sha256(p.read_bytes()).hexdigest(),'source_commit':'d807cbdbd5dd6664d61480acedc752e19cfca48a','source_binary':'Original verified Fedora build','command':args,'exit':r.returncode,'resource_limits':'USER_REMOVED'}))\n"
r=subprocess.run(['ssh','-S','/home/chabking/workspace/.evidence/modeldev-node-control-20261101-v2/node.sock','-o','BatchMode=yes','-o','ProxyCommand=false','kubercloud@100.64.128.12','python3','-'],input=remote.encode(),capture_output=True,timeout=60)
assert r.returncode==0,(r.returncode,r.stderr.decode())
(b/'node-small-build-start.json').write_bytes(r.stdout);print(r.stdout.decode())

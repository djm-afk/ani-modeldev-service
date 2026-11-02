#!/usr/bin/env python3
from pathlib import Path
import subprocess,json
BASE=Path(__file__).resolve().parent
REMOTE=r'''
checked=identity()
import json,subprocess,time
cr={x['metadata']['name']:x for x in kget('get','clusterroles')['items']}
nr={x['metadata']['name']:x for x in kget('-n','ani-system','get','roles')['items']}
bindings=[('RoleBinding',x) for x in kget('-n','ani-system','get','rolebindings')['items']]+[('ClusterRoleBinding',x) for x in kget('get','clusterrolebindings')['items']]
def grants(rule,verb,name):
 return any(g in ('pipelines.kubeflow.org','*') for g in rule.get('apiGroups',[])) and any(r in ('pipelines','*') for r in rule.get('resources',[])) and any(v in (verb,'*') for v in rule.get('verbs',[])) and (not rule.get('resourceNames') or bool(name) and name in rule['resourceNames'])
candidates={};matched=[];group_only=[]
for kind,b in bindings:
 ref=b['roleRef'];role=(cr if ref['kind']=='ClusterRole' else nr).get(ref['name']);rules=role.get('rules',[]) if role else []
 if not (any(grants(r,'create','') for r in rules) or any(grants(r,'list','') for r in rules)):continue
 record={'kind':kind,'name':b['metadata']['name'],'namespace':b['metadata'].get('namespace'),'uid':b['metadata']['uid'],'role_ref':ref,'matching_rules':[r for r in rules if grants(r,'create','') or grants(r,'list','')],'subjects':[]}
 for subject in b.get('subjects',[]):
  if subject['kind']=='ServiceAccount':principal='system:serviceaccount:'+subject.get('namespace',b['metadata'].get('namespace',''))+':'+subject['name']
  elif subject['kind']=='User':principal=subject['name']
  else:group_only.append({'binding':b['metadata']['name'],'group':subject['name'],'usable_by_KFP_groups_omitted_SAR':False});continue
  record['subjects'].append({'principal':principal,'subject_kind':subject['kind']});candidates.setdefault(principal,[]).append({'binding_kind':kind,'binding':b['metadata']['name'],'role':ref['name']})
 matched.append(record)
assert len(candidates)<=24,'Bounded static publisher candidate list exceeded; do not probe arbitrary identities'
reviews={}
for principal,sources in candidates.items():
 allowed=[]
 for verb,name in [('create',''),('list','')]+[('get',p) for p in ('modeldev-mlp-cpu','modeldev-mlp-gpu','modeldev-sft-lora-cpu','modeldev-sft-lora-gpu')]:
  a={'group':'pipelines.kubeflow.org','version':'v1beta1','resource':'pipelines','namespace':'ani-system','verb':verb,'name':name}
  body={'apiVersion':'authorization.k8s.io/v1','kind':'SubjectAccessReview','spec':{'user':principal,'resourceAttributes':a}}
  p=subprocess.run(['kubectl','--context','kubernetes-admin@ani-kcn-full','create','-f','-','-o','json'],input=json.dumps(body).encode(),capture_output=True,timeout=10);assert p.returncode==0
  status=json.loads(p.stdout)['status'];allowed.append({'attributes':a,'status':{k:status[k] for k in ('allowed','denied','evaluationError') if k in status}})
 reviews[principal]={'binding_sources':sources,'reviews':allowed,'all_required_allowed':all(x['status'].get('allowed') is True for x in allowed),'source_proven_as_managed_publisher':False}
print(json.dumps({'schema':'ani.modeldev.existing-publisher-identities.v1','identity':checked,'observed_at_unix':time.time(),'matching_existing_bindings':matched,'group_only_bindings_not_reused':group_only,'candidate_reviews':reviews,'no_principal_selected_by_power_alone':True,'groups_supplied':False,'token_request_issued':False,'impersonation_used':False,'rbac_modified':False,'publication_issued':False}))
'''
source="__name__='publisher_identity_library'\n"+(BASE.parents[1]/'09/workbench-maintenance-job.py').read_text()+REMOTE
r=subprocess.run(['ssh','-S','/home/chabking/workspace/.evidence/modeldev-node-control-20261101-v2/node.sock','-o','BatchMode=yes','-o','ProxyCommand=false','kubercloud@100.64.128.12','python3','-'],input=source.encode(),capture_output=True,timeout=180)
assert r.returncode==0,(r.returncode,r.stderr.decode());out=json.loads(r.stdout);(BASE/'existing-publisher-identities.json').write_text(json.dumps(out,indent=2)+'\n');print(json.dumps({'binding_count':len(out['matching_existing_bindings']),'group_only':out['group_only_bindings_not_reused'],'candidates':{k:{'all_required_allowed':v['all_required_allowed'],'binding_sources':v['binding_sources']} for k,v in out['candidate_reviews'].items()},'token_request_issued':False,'rbac_modified':False}))

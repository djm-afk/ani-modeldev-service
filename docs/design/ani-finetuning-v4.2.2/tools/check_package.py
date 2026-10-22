#!/usr/bin/env python3
"""Static DOCUMENT_ONLY checks; never executes uploaded/project business code."""
from __future__ import annotations
import argparse, hashlib, json, re, sys
from pathlib import Path
from urllib.parse import urlparse,unquote
from bs4 import BeautifulSoup
from markdown_it import MarkdownIt
import render_package as R

def main()->None:
 ap=argparse.ArgumentParser();ap.add_argument('--root',type=Path,default=Path(__file__).resolve().parents[1]);a=ap.parse_args();root=a.root.resolve()
 s=(root/'spec.md').read_text();old=(root/'reference/spec-v4.2.1.md').read_text();sha=hashlib.sha256(s.encode()).hexdigest()
 by={x['id']:x for x in R.parse_sections(s)};ob={x['id']:x for x in R.parse_sections(old)}
 checks=[]
 def ck(name,passed,detail=''):
  checks.append({'check':name,'status':'PASS' if passed else 'FAIL','detail':detail})
 ck('current_source_version',s.startswith('# ANI CPU 受管微调方案 v4.2.2'))
 ck('complete_26_chapters',list(by)==[f'A{i:02}' for i in range(1,13)]+[f'D{i:02}' for i in range(1,15)]+['sources'])
 ck('direct_baseline_preserved',hashlib.sha256((root/'reference/spec-v4.2.1.md').read_bytes()).hexdigest()=='363016708aef1c22454cbd320effead34b952aadf2bad08ea3a3582dc9e4c8b1')
 lock=json.loads((root/'review/source-lock.json').read_text())
 for item in lock['preserved_sources']:
  p=root/item['path'];ck('source_bytes:'+item['path'],p.is_file() and hashlib.sha256(p.read_bytes()).hexdigest()==item['sha256'])
 for ref in ['A02.1','D01.2','D02.7','D10.2','D12.5','D13.1','D06']:
  ck('preserved_boundary:'+ref,R.excerpt(ref,by)==R.excerpt(ref,ob))
 budget=lambda x:[r for r in R.read_tables(R.excerpt('D10',x).split('### 10.1')[0],r'^\| ') if len(r)==4 and re.fullmatch(r'\d+s',r[3])]
 ck('resource_budget_rows_unchanged',budget(by)==budget(ob))
 total=sum(int(r[3][:-1]) for r in budget(by));ck('normal_budget_sum',total==10800,str(total))
 ft=R.read_tables(R.excerpt('D11.2',by),r'^\| FT\d{2} \|');sub=R.read_tables(by['D11']['full'],r'^\| FT\d{2}\.\d+ \|');pa=R.read_tables(R.excerpt('D11.2.2',by),r'^\| PA\d{2} \|');cm=R.read_tables(R.excerpt('D11.3',by),r'^\| C\d{2} \|');mr=R.read_tables(R.excerpt('D08.1',by),r'^\| MR\d{2} ')
 ck('scope_counts',[len(ft),len(sub),len(pa),len(cm),len(mr)]==[32,15,10,34,8])
 ck('original_ft_ids', [r[0] for r in ft]==[f'FT{i:02}' for i in range(1,33)])
 op=R.read_tables(R.excerpt('D11.2.2',ob),r'^\| PA\d{2} \|');oc=R.read_tables(R.excerpt('D11.3',ob),r'^\| C\d{2} \|')
 ck('parameter_test_rows_preserved',pa==op)
 ck('v04_mapping_preserved',cm==oc)
 osub=R.read_tables(ob['D11']['full'],r'^\| FT\d{2}\.\d+ \|');sd={r[0]:r for r in sub}
 ck('old_12_refinements_preserved',all(sd.get(r[0])==r for r in osub) and len(osub)==12)
 ck('only_3_new_registration_refinements',set(sd)-{r[0] for r in osub}=={'FT25.1','FT25.2','FT25.3'})
 om=R.read_tables(R.excerpt('D08.1',ob),r'^\| MR\d{2} ');ck('model_provider_requirements_preserved',all(r==x for r,x in zip(mr,om) if not r[0].startswith('MR04')))
 ck('mainflow_and_bff_separated','M3-A' in by['A12']['body'] and 'M3-B' in by['A12']['body'] and '只在准备M3-B时' in R.excerpt('D12.7',by))
 ck('test_boundary_no_production_bypass',all(t in R.excerpt('D12.6',by) for t in ['不借用Gov证书','不直接插成功行','不维护新的“当前Release”','正式ModelDev应用用例']))
 ck('publication_confirm_retained_when_model_absent','register前半段' in R.excerpt('D11.6',by) and '不能将该执行写成A10' in R.excerpt('D11.6',by))
 ck('registration_dispatch_contract_complete',all(t in R.excerpt('D09.3',by) for t in ['每个具体','同一执行记录行锁','事务外','Stop先于','CLOSED后用户明确恢复','不是把本域Stop与远端Model事务']))
 ck('scope_is_not_business_state','不是新的业务状态、API字段或运行模式' in R.excerpt('D11.6',by))
 # Records are plans/templates only. Structural review is not a business PASS.
 bad=[]
 for p in (root/'records').glob('*.json'):
  data=json.loads(p.read_text())
  def walk(v,path=''):
   if isinstance(v,dict):
    for k,x in v.items():
     if (k=='status' or k.endswith('_status')) and x not in (None,'NOT_RUN','DEFERRED'):bad.append((p.name,path+k,x))
     walk(x,path+k+'.')
   elif isinstance(v,list):
    for i,x in enumerate(v):walk(x,path+str(i)+'.')
  walk(data)
  if data.get('spec_sha256')!=sha:bad.append((p.name,'spec_sha256','mismatch'))
 ck('all_new_business_records_not_run',not bad,str(bad))
 plan=json.loads((root/'records/acceptance-plan.json').read_text())
 ck('bff_deferred_in_time_not_scope',plan['bff_scheduling']['blocks_core_flow'] is False and plan['bff_scheduling']['retained_in_final_acceptance'] is True)
 ck('gpu_nonblocking',plan['gpu_preparation']['blocks_cpu_acceptance'] is False)
 vs=json.loads((root/'records/parameter-default-combination-vectors.json').read_text())['cases']
 expected=['ACCEPT','ACCEPT','REJECT','REJECT','ACCEPT'];ck('combination_vectors',len(vs)==5 and [v['expected'] for v in vs]==expected)
 # Verify exact markdown excerpts and their hashes independently of rendered headings.
 slices=0;fail=[]
 for p in list(root.glob('*.md'))+list((root/'tasks').glob('*.md'))+list((root/'guides').glob('*.md')):
  txt=p.read_text()
  for m in re.finditer(r'<!-- source-section: ([^;]+); source-sha256: ([0-9a-f]{64}) -->\n',txt):
   ref=m.group(1);e=R.excerpt(ref,by);slices+=1
   if m.group(2)!=hashlib.sha256(e.encode()).hexdigest() or txt[m.end():m.end()+len(e)]!=e:fail.append((str(p.relative_to(root)),ref))
 ck('all_markdown_source_slices',not fail,{'slices':slices,'failures':fail})
 pages=list(root.glob('*.html'));soups={p.name:BeautifulSoup(p.read_text(),'html.parser') for p in pages};links=[];idsfail=[];bodyfail=[];bodycount=0
 md=MarkdownIt('commonmark',{'html':True}).enable('table')
 def normalized(text):return re.sub(r'\[S(\d+)\]',r'S\1',re.sub(r'\s+','',text).replace('**',''))
 for name,soup in soups.items():
  ids=[x['id'] for x in soup.find_all(id=True)]
  if len(ids)!=len(set(ids)):idsfail.append(name)
  for a in soup.select('[href],[src]'):
   target=a.get('href',a.get('src',''));u=urlparse(target)
   if u.scheme or not u.path and not u.fragment:continue
   p=(root/unquote(u.path)) if u.path else (root/name)
   if not p.exists():links.append((name,target,'missing file'));continue
   if u.fragment and p.suffix=='.html':
    ts=soups.get(p.name) or BeautifulSoup(p.read_text(),'html.parser')
    if not ts.find(id=unquote(u.fragment)):links.append((name,target,'missing anchor'))
  for b in soup.select('.source-body[data-source-ref]'):
   ref=b['data-source-ref']
   if ref in by:src=by[ref]['body']
   else:src=R.excerpt(ref,by).split('\n',1)[1].strip()
   bodycount+=1
   actual=BeautifulSoup(str(b),'html.parser')
   for c in actual.select('.codehead'):c.decompose()
   target=BeautifulSoup(md.render(src),'html.parser')
   if b.get('data-source-sha256')!=hashlib.sha256(src.encode()).hexdigest() or normalized(actual.get_text())!=normalized(target.get_text()):bodyfail.append((name,ref))
 ck('html_unique_anchors',not idsfail,idsfail)
 ck('html_local_links_and_anchors',not links,links)
 ck('html_visible_source_text',not bodyfail,{'source_bodies':bodycount,'failures':bodyfail})
 ck('html_current_source_hash',all(x.select_one('meta[name="ani-spec-sha256"]')['content']==sha for x in soups.values()))
 md_bad=[]
 for p in list(root.glob('*.md'))+list((root/'tasks').glob('*.md'))+list((root/'guides').glob('*.md')):
  for href in re.findall(r'\]\(([^)]+)\)',p.read_text()):
   u=urlparse(href)
   if u.scheme or not u.path:continue
   if not (p.parent/unquote(u.path)).exists():md_bad.append((str(p.relative_to(root)),href))
 ck('markdown_local_links',not md_bad,md_bad)
 import difflib
 expect=''.join(difflib.unified_diff(old.splitlines(True),s.splitlines(True),fromfile='reference/spec-v4.2.1.md',tofile='spec.md'))
 ck('complete_diff_matches_source',(root/'review/spec-v4.2.1-to-v4.2.2.diff').read_text()==expect)
 report={'scope':'DOCUMENT_ONLY','document_version':R.VERSION,'spec_sha256':sha,'status':'PASS' if all(c['status']=='PASS' for c in checks) else 'FAIL','checks':checks,'counts':{'checks':len(checks),'html_files':len(pages),'source_slices':slices,'visible_bodies':bodycount},'not_validated':['business implementation','Fedora workload','BFF/Model RPC','KFP/Trainer deployment','concurrency correctness','Inference','GPU']}
 (root/'review/document-checks.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
 print(json.dumps({'status':report['status'],'counts':report['counts'],'failures':[c for c in checks if c['status']=='FAIL']},ensure_ascii=False))
 if report['status']!='PASS':sys.exit(1)
if __name__=='__main__':main()

#!/usr/bin/env python3
"""Rebuild document-only views. No business-code generation or remote calls.

Edit only spec.md. Project-side regeneration belongs on the approved Fedora
machine. A conversation-side document build is not a Fedora/LIVE business test.
Actual records must be stored separately; edited templates/plans are protected.
"""
from __future__ import annotations
import argparse, difflib, hashlib, html, json, re
from pathlib import Path
from typing import Any
from bs4 import BeautifulSoup, NavigableString
from markdown_it import MarkdownIt

VERSION='v4.2.2'
A_FILE=f'ANI-CPU受管微调-{VERSION}-业务与流程方案.html'
D_FILE=f'ANI-CPU受管微调-{VERSION}-开发与Model对接说明.html'
G_FILE=f'ANI-CPU受管微调-{VERSION}-主流程开发与分段验收手册.html'
R_FILE=f'ANI-CPU受管微调-{VERSION}-变更与自复核.html'
MD=MarkdownIt('commonmark',{'html':True,'typographer':False}).enable('table')
WORK_REFS={
 'W0':['A12','A07.3','A07.4','D05.3','D06.1','D06.4','D07.2','D10','D13'],
 'W1':['A12','A04','A06','A07','D01.1','D01.2','D02','D05','D06.2.1','D09.5','D12.4'],
 'W2':['A12','A05','D03','D04','D12.6'],
 'W3':['A12','A08','A11','D05.3','D06','D09.3','D10','D11.6','D12.6'],
 'W4':['A12','D07','D08','D09','D11.2.3','D11.6'],
 'W5':['A04','A06','A09','D08','D09.3','D11.6'],
 'W6':['A12','A07','A10','D11','D12','D13'],
}
WORK_FT={
 'W0':['FT04','FT11','FT13','FT14','FT27'],
 'W1':['FT05','FT06','FT07','FT08','FT09','FT30'],
 'W2':['FT01','FT02','FT03','FT04','FT10'],
 'W3':['FT11','FT12','FT15','FT22','FT23','FT24','FT26','FT30'],
 'W4':['FT16','FT17','FT18','FT20','FT21','FT25','FT28'],
 'W5':['FT05','FT07','FT08','FT10','FT18','FT19','FT20','FT21','FT25','FT28','FT29'],
 'W6':['FT01–FT32；原12项细化与本轮FT25.1–3'],
}
WORK_PA={'W0':['PA04','PA08','PA09'],'W1':['PA01','PA02','PA03','PA05','PA06','PA07','PA10'],'W2':[], 'W3':['PA04','PA05','PA06','PA08','PA09'],'W4':['PA09'],'W5':[],'W6':['PA01–PA10']}
PARAMETER_REFS=['A12','A07','D01.1','D02.4','D02.5','D02.6','D05','D06.4','D09.5','D11.2.2','D11.6','D12.7']
GPU_REFS=['A02.1','D01.2','D02.7','D10.2','D11.5','D12.5','D13.1']
GUIDE_REFS=['A03','A12','D11.6','D12.1','D12.2','D12.3','D12.4','D12.6','D12.7']


def sha_bytes(b:bytes)->str:return hashlib.sha256(b).hexdigest()
def sha_text(t:str)->str:return sha_bytes(t.encode())
def parse_sections(spec:str)->list[dict[str,str]]:
 heads=list(re.finditer(r'^## (.+)$',spec,re.M));out=[]
 for i,m in enumerate(heads):
  end=heads[i+1].start() if i+1<len(heads) else len(spec);title=m.group(1)
  key=title.split(' · ',1)[0] if re.match(r'^[AD]\d{2} · ',title) else 'sources'
  out.append({'id':key,'title':title,'full':spec[m.start():end].rstrip()+'\n','body':spec[m.end():end].strip()})
 return out

def excerpt(ref:str,by:dict)->str:
 ch=ref[:3];s=by[ch]['full']
 if '.' not in ref:return s
 target=str(int(ch[1:]))+ref[3:]
 hs=list(re.finditer(r'^(#{3,6}) (\d+(?:\.\d+)*)(?:\s|$)',s,re.M))
 m=next((x for x in hs if x.group(2)==target),None)
 if not m:raise ValueError('Missing section '+ref)
 end=len(s)
 for x in hs:
  if x.start()>m.start() and len(x.group(1))<=len(m.group(1)):
   end=x.start();break
 return s[m.start():end].rstrip()+'\n'

def read_tables(text:str,pattern:str)->list[list[str]]:
 return [[c.strip() for c in line.strip().strip('|').split('|')] for line in text.splitlines() if re.match(pattern,line)]

def decorate_md(text:str,key:str)->str:
 chunks=re.split(r'(```[\s\S]*?```)',text)
 for i,c in enumerate(chunks):
  if not c.startswith('```'):chunks[i]=re.sub(r'\[S([0-9]+)\]',r'[S\1](#source-s\1)',c)
 soup=BeautifulSoup(MD.render(''.join(chunks)),'html.parser')
 # Embedded markdown views sit below the page's single document title.
 for heading in soup.find_all('h1'):heading.name='h2'
 for node in list(soup.find_all(string=True)):
  if node.parent.name in ('code','pre','script','style') or node.find_parent('pre'):continue
  t=str(node)
  if '**' not in t:continue
  bits=re.split(r'(\*\*[^\n]+?\*\*)',t)
  if len(bits)==1:continue
  for bit in bits:
   if bit.startswith('**') and bit.endswith('**'):
    tag=soup.new_tag('strong');tag.string=bit[2:-2];node.insert_before(tag)
   else:node.insert_before(NavigableString(bit))
  node.extract()
 for table in list(soup.find_all('table')):
  wrap=soup.new_tag('div',attrs={'class':'tablewrap','role':'region','tabindex':'0','aria-label':'可横向滚动的表格'});table.wrap(wrap)
 for pre in list(soup.find_all('pre')):
  code=pre.find('code');lang='TEXT'
  if code:lang=next((c[9:].upper() for c in code.get('class',[]) if c.startswith('language-')),'TEXT')
  wrap=soup.new_tag('div',attrs={'class':'codewrap'});pre.wrap(wrap)
  head=soup.new_tag('div',attrs={'class':'codehead'});label=soup.new_tag('span');label.string=lang;head.append(label)
  button=soup.new_tag('button',attrs={'class':'copy','type':'button','aria-label':'复制示例'});button.string='复制';head.append(button);wrap.insert(0,head)
 for h in soup.find_all(['h3','h4','h5','h6']):
  t=h.get_text();sm=re.match(r'S([0-9]+)\s*·',t);num=re.match(r'(\d+(?:\.\d+)*)\s',t)
  if sm:h['id']='source-s'+sm.group(1)
  elif num:h['id']=key+'-'+num.group(1).replace('.','-')
 for a in soup.find_all('a',href=True):
  if a['href'].startswith('#source-'):a['class']=['srcmark']
  elif a['href'].startswith(('http://','https://')):a['rel']='noreferrer noopener'
 return str(soup)

def html_shell(root:Path,title:str,body:str,sh:str)->str:
 css=(root/'tools/document.css').read_text();js=(root/'tools/document.js').read_text()
 menu='<button class="menuBtn" id="menu" aria-label="展开目录">目录</button>' if 'class="sidebar"' in body else ''
 top=f'<header class="top"><div class="brand"><a href="index.html">ANI / MODEL DEVELOPMENT</a></div><div class="toplinks"><a href="{G_FILE}">当前开工</a><a href="{A_FILE}">业务方案</a><a href="{D_FILE}">开发说明</a><a class="shortHide" href="{R_FILE}">变更复核</a>{menu}<button id="fontMinus" aria-label="减小正文字号">A−</button><button id="fontPlus" aria-label="增大正文字号">A+</button><button id="print">打印</button></div></header>'
 return '<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">'+f'<meta name="ani-spec-sha256" content="{sh}"><meta name="ani-document-scope" content="DOCUMENT_ONLY"><title>{html.escape(title)}</title><style>{css}</style></head><body>{top}{body}<script>{js}</script></body></html>'

def book(root:Path,sections:list[dict[str,str]],sources:dict,title:str,lead:str,sh:str)->str:
 nav=''.join(f'<a class="navlink" data-chapter="{x["id"]}" href="#{x["id"]}"><span class="num">{x["id"]}</span><span>{html.escape(x["title"].split(" · ",1)[-1])}</span></a>' for x in sections)
 parts=[]
 for i,x in enumerate(sections):
  prev=f'#{sections[i-1]["id"]}' if i else 'index.html';nxt=f'#{sections[i+1]["id"]}' if i+1<len(sections) else '#sources'
  parts.append(f'<section class="chapter" data-chapter="{x["id"]}" id="{x["id"]}"><h2><small>{x["id"]} / {VERSION}</small>{html.escape(x["title"].split(" · ",1)[-1])}</h2><div class="source-body" data-source-ref="{x.get("source_ref",x["id"])}" data-source-sha256="{sha_text(x["body"])}">{decorate_md(x["body"],x["id"])}</div><div class="endlinks"><a href="{prev}">← 上一节</a><a href="{nxt}">下一节 →</a></div></section>')
 source_html=f'<section class="chapter" id="sources"><h2>来源与证据口径</h2><div class="source-body" data-source-ref="sources" data-source-sha256="{sha_text(sources["body"])}">{decorate_md(sources["body"],"sources")}</div></section>'
 body=f'''<div class="layout"><aside class="sidebar"><h2>阅读目录 / {len(sections)}节</h2><input id="search" class="search" placeholder="筛选本册章节…" aria-label="按关键词筛选章节"><div class="count" id="count"></div>{nav}<a class="navlink" href="#sources"><span class="num">SRC</span><span>来源与边界</span></a><div class="sidebottom">微调{VERSION} ≠ 平台v0.4<br>2026-10-14 · DESIGN_UPDATED<br><a href="index.html">完整包入口</a><a href="spec.md">唯一权威正文</a><a href="reference/spec-v4.2.1.md">v4.2.1原件</a></div></aside><main class="main"><div class="hero"><p class="eyebrow">CORE FLOW FIRST / GOVERNED INTEGRATION LATER</p><h1>CPU 受管微调 {VERSION}<br>{title}</h1><p>{lead}</p><div class="chips"><span class="chip">先执行与产物主链</span><span class="chip">BFF／前端后接</span><span class="chip">Model分期</span><span class="chip">GPU独立准备</span></div></div><div class="notice"><strong>当前不等真实BFF：</strong>合同先定，M3-A验证真实ModelDev／KFP／Trainer／S3和停止关闭；M3-B补真实BFF／前端。后置事项仍属最终验收，不用夹具冒充上游授权或Model READY。登记具体写派发与Stop的共同仲裁见D09.3。</div><div class="document-meta">正文 SHA256：{sh}</div><div class="empty" id="empty">没有匹配章节。<button id="clear">清除筛选</button></div>{''.join(parts)}{source_html}<div class="footer">DOCUMENT_ONLY · 文档检查不代表任何负载、接口、并发或目标环境通过。</div></main></div>'''
 return html_shell(root,f'ANI {VERSION} · {title}',body,sh)

def write_json(path:Path,obj:Any,protected:bool=False)->None:
 if path.exists() and protected:
  old=json.loads(path.read_text())
  def normalized(v:Any)->Any:
   if isinstance(v,dict):return {k:normalized(x) for k,x in v.items() if k not in ('spec_sha256','document_version')}
   if isinstance(v,list):return [normalized(x) for x in v]
   return v
  if normalized(old)!=normalized(obj):
   raise RuntimeError(f'Protect existing record/plan: {path}; preserve edits and compare before regeneration.')
 path.parent.mkdir(parents=True,exist_ok=True)
 path.write_text(json.dumps(obj,ensure_ascii=False,indent=2)+'\n')

def slice_view(title:str,refs:list[str],by:dict,sh:str,prefix:str='')->str:
 text=f'# {title}｜{VERSION}同源阅读视图\n\n权威正文：{prefix}spec.md；SHA256：`{sh}`。本文件只切片原文，不独立修改；先改spec再导出。\n\n'
 for ref in refs:
  e=excerpt(ref,by);text+=f'<!-- source-section: {ref}; source-sha256: {sha_text(e)} -->\n'+e+'\n'
 text+='\n'+by['sources']['full']
 return text

def main()->None:
 ap=argparse.ArgumentParser();ap.add_argument('--root',type=Path,default=Path(__file__).resolve().parents[1]);args=ap.parse_args();root=args.root.resolve()
 for d in ['records','tasks','guides','review']:(root/d).mkdir(exist_ok=True)
 spec=(root/'spec.md').read_text();sh=sha_text(spec);sections=parse_sections(spec);by={x['id']:x for x in sections}
 assert list(by)==[f'A{i:02}' for i in range(1,13)]+[f'D{i:02}' for i in range(1,15)]+['sources']
 (root/A_FILE).write_text(book(root,[x for x in sections if x['id'].startswith('A')],by['sources'],'业务与流程方案','保留最终BFF业务架构，先把执行与产物做实；阶段完成、单次执行成功与整版验收各自判断。',sh))
 (root/D_FILE).write_text(book(root,[x for x in sections if x['id'].startswith('D')],by['sources'],'开发与Model对接说明','复用正式合同与现有应用用例；新增登记派发仲裁，任务与验收按当前主链、后续入口分别推进。',sh))
 for name,title,refs in [
  ('model-integration-requirements.md','Model需求与具体登记派发边界',['A04','A06','A09','D08','D09.3','D11.6']),
  ('parameter-and-frontend-contract.md','参数合同先定，真实BFF／前端后接',PARAMETER_REFS),
  ('gpu-extension-boundary.md','GPU后续接入边界（独立准备，不阻塞CPU）',GPU_REFS),
  ('guides/core-flow-first.md','主流程开发与分段验收手册',GUIDE_REFS),
 ]:
  text=slice_view(title,refs,by,sh,'../' if '/' in name else '')
  if '/' in name:
   # Path references in source excerpts stay text, not executable commands.
   text=text.replace('权威正文：../spec.md','权威正文：`../spec.md`')
  (root/name).write_text(text)
 guide=[]
 for i,ref in enumerate(GUIDE_REFS,1):
  e=excerpt(ref,by);head,body=e.split('\n',1)
  guide.append({'id':f'G{i:02}','source_ref':ref,'title':f'G{i:02} · '+re.sub(r'^#+\s*','',head),'body':body.strip()})
 (root/G_FILE).write_text(book(root,guide,by['sources'],'主流程开发与分段验收手册','从这里开工：测试来件只替代上游边界，后端执行和产物是真的；真实BFF与前端放到M3-B，不阻塞当前主链。',sh))
 ft=read_tables(excerpt('D11.2',by),r'^\| FT\d{2} \|')
 sub=read_tables(by['D11']['full'],r'^\| FT\d{2}\.\d+ \|')
 pa=read_tables(excerpt('D11.2.2',by),r'^\| PA\d{2} \|')
 cm=read_tables(excerpt('D11.3',by),r'^\| C\d{2} \|')
 mr=read_tables(excerpt('D08.1',by),r'^\| MR\d{2} ')
 works=read_tables(excerpt('D12.3',by),r'^\| W\d ')
 stages=read_tables(excerpt('D11.6',by),r'^\| (?:WORKLOAD|CORE_FLOW|MODEL_INTEGRATION|BFF_INTEGRATION|SCHEME_FINAL|GPU_PRECHECK)')
 assert [len(ft),len(sub),len(pa),len(cm),len(mr),len(works),len(stages)]==[32,15,10,34,8,7,6]
 for row in works:
  wid,title=row[0].split(' ',1)
  t=f'# {wid} · {title}｜{VERSION}派生任务卡\n\n权威：`../spec.md`，SHA256：`{sh}`。先读A12、D11.6、D12.6；本卡引用与原文一致，不独立改规范。\n\n'
  t+=f'**当前先交付：**{row[1]}。\n\n**后续及不阻塞边界：**{row[2]}。\n\n'
  t+='真实BFF/前端在M3-B接入，不是当前主链前置。合同/参数解析现在固定；Model只阻塞其真实用途；GPU是独立准备。测试来件不是Gov授权，不放宽正式鉴权、不改库造状态、不用fake Model返回READY。\n\n'
  t+='本机只阅读/编码/Git，既有`fedora`负责生成/编译/测试/镜像/Pipeline与负载；.10～.12目标K8s运行集成。实际工作路径先核实，不猜IP，不借其他agent环境，不因包存在视为部署授权。\n\n'
  t+='**对应FT：**'+'、'.join(WORK_FT[wid])+'。\n\n**对应PA：**'+('、'.join(WORK_PA[wid]) or '无独立PA新增责任')+'。同一用例分本域/上游断言与环境留证；主链部分PASS不等于整项PASS。\n\n'
  for ref in WORK_REFS[wid]:
   e=excerpt(ref,by);t+=f'<!-- source-section: {ref}; source-sha256: {sha_text(e)} -->\n'+e+'\n'
  t+='## 记录与交接\n\n复制`../records/work-progress.template.json`和相应用例模板另存真实结果，不覆盖计划或已填进度。记录明确源码commit、环境、入口层级、替代边界、未测断言及下一步。先读AGENTS与工作区状态；只推本人任务分支，不自动合并、强推、stash/reset/clean；不修改ENV共享控制器。\n'
  (root/'tasks'/f'{wid}.md').write_text(t)
 index=f'# W0–W6任务入口｜{VERSION}\n\n权威`../spec.md`，SHA256：`{sh}`。**当前先完成微调执行与产物主链，真实BFF/前端后接。**W1先交正式合同；W0/W2/W3/W4不等W1-P4或W6-P2。\n\n| 工作包 | 当前先交付 | 后续及不阻塞边界 |\n|---|---|---|\n'
 for r in works:
  wid=r[0].split()[0];index+=f'| [{r[0]}]({wid}.md) | {r[1]} | {r[2]} |\n'
 for ref in ['A12','D12.4','D11.6']:
  e=excerpt(ref,by);index+=f'\n<!-- source-section: {ref}; source-sha256: {sha_text(e)} -->\n'+e+'\n'
 index+='\n先读[主流程手册](../guides/core-flow-first.md)确定测试入口与记录；后续真实BFF操作只在M3-B前落实。[GPU边界](../gpu-extension-boundary.md)独立，不新增W7。\n'
 (root/'tasks/index.md').write_text(index)
 stageplan=[{'scope':r[0].split('：',1)[0],'stage':r[0],'requirements':r[1],'coverage_limit':r[2],'status':'NOT_RUN'} for r in stages]
 plan={'scope':'BUSINESS_PLAN_NOT_EXECUTED','document_version':VERSION,'spec_sha256':sh,'status':'NOT_RUN','note':'主FT32和PA10/MR8/C34原范围保留；12项原细化加FT25.1–3。BFF/前端后置不是DEFERRED；部分断言通过不得标整项PASS。',
 'cases':[{'id':r[0],'name':r[1],'required_evidence':r[2],'status':'NOT_RUN'} for r in ft],
 'subcases':[{'id':r[0],'parent':r[0].split('.')[0],'arrangement':r[1],'required_evidence':r[2],'status':'NOT_RUN','roles':['export','verify'] if r[0].startswith(('FT23.','FT24.')) else []} for r in sub],
 'parameter_cases':[{'id':r[0],'name':r[1],'required_evidence':r[2],'related_and_owners':r[3],'status':'NOT_RUN'} for r in pa],
 'v04_mapping':[{'id':r[0],'original_scope':r[1],'coverage':r[2]} for r in cm],
 'stage_plan':stageplan,'bff_scheduling':{'phase':'M3-B','blocks_core_flow':False,'retained_in_final_acceptance':True,'status':'NOT_RUN'},
 'gpu_preparation':{'scope':'GPU_PRECHECK','blocks_cpu_acceptance':False,'status':'NOT_RUN','formal_delivery':'DEFERRED'}}
 write_json(root/'records/acceptance-plan.json',plan,True)
 write_json(root/'records/model-readiness-plan.json',{'scope':'MODEL_REQUIREMENTS_NOT_EXECUTED','spec_sha256':sh,'status':'NOT_RUN','note':'按MR真实用途分别交付；不是CORE_FLOW全部开发前置。上游来件替代不等于Gov授权通过。','items':[{'id':r[0].split()[0],'capability':r[0],'provider':r[1],'consumer':r[2],'evidence':r[3],'status':'NOT_RUN'} for r in mr]},True)
 base={'template_only':True,'document_version':VERSION,'spec_sha256':sh,'status':'NOT_RUN'}
 templates={
 'work-progress.template.json':{**base,'authoritative_spec_path':None,'work_package':None,'current_phase':None,'scope':None,'repositories':[],'fedora':{'ssh_alias':'fedora','approved_source_path':None,'artifact_path':None},'cluster':{'expected_node_internal_ips':['172.16.101.10','172.16.101.11','172.16.101.12'],'context':None,'identity':None,'handoff_ref':None},'test_entry_layer':None,'replaced_boundaries':[],'evidence':[],'untested_assertions':[],'remaining_dependencies':[],'next_action':None},
 'acceptance-results.template.json':{**base,'note':'scope是证据范围，不是新业务状态；后置BFF断言NOT_RUN。每条result附case_id/assertion/scope/environment/evidence。','results':[],'single_execution_results':[],'core_flow_stage':{'status':'NOT_RUN','evidence':[],'model_scope':None,'bff_scope':'NOT_RUN','unverified_assertions':[]},'scheme_final_acceptance':{'status':'NOT_RUN','bff':None,'frontend':None,'model':None,'ft29_c23':None,'regression':None,'handover':None}},
 'stage-progress.template.json':{**base,'note':'阶段达标不得把未验证的父用例标PASS。','stages':stageplan},
 'core-flow-run.template.json':{**base,'scope':'CORE_FLOW','entry':{'source_path':None,'method_or_command':None,'application_layer':None,'source_commit':None,'image_digest':None},'isolation':{'database_reference':None,'namespace_uid':None,'approval_reference':None},'upstream_replacement':{'description':None,'not_gov_authorization':True,'not_tested_assertions':[]},'candidate':{'id':None,'digest':None,'snapshot_hash':None,'contract_version':None,'not_current_gov_binding':True},'inputs':{'ready_ref':None,'base_model_source':None,'base_model_read_boundary':None},'real_dependencies':[],'replaced_dependencies':[],'execution_id':None,'resource_refs':[],'publication_evidence':[],'detached_retrieval_evidence':[],'registration':{'status':'NOT_RUN','dependency_error':None,'receipt':None},'close_evidence':[],'not_claimed':['BFF_AUTHORIZATION','MODEL_READY_WITHOUT_REAL_INTEGRATION','FT29_WITHOUT_INFERENCE']},
 'registration-dispatch-race.template.json':{**base,'case_id':None,'scope':None,'mode':'NORMAL_OR_AUTHORIZED_RECOVERY_TO_BE_RECORDED','execution_id':None,'operation_id':None,'registration_key':None,'write_operation':None,'request_fingerprint':None,'original_target':None,'original_publication_ref':None,'database_serialization_boundary':None,'transaction_order':{'dispatch_commit':None,'stop_commit':None,'observed_generation':None},'original_deadlines':None,'external_call_count':None,'late_receipts':[],'readback_evidence':[],'replay_evidence':[],'model_boundary_is_real':None,'evidence':[]},
 'bff-candidate-validation.template.json':{**base,'scope':'BFF_INTEGRATION','phase':'M3-B','blocks_core_flow':False,'actual_bff_endpoint_reference':None,'approved_isolation':None,'test_identity_references':[],'original_binding':None,'candidate_binding':None,'binding_owner':None,'real_contract_versions':[],'parameter_cases':[],'rollback_precondition':None,'rollback_method':None,'rollback_result':None,'evidence':[]},
 'start-stop-race.template.json':{**base,'case_id':None,'scope':None,'role':None,'execution_id':None,'spec_hash':None,'run_id':None,'task_id':None,'pod_uid':None,'environment':None,'transaction_order':{'start_admission_commit':None,'stop_commit':None,'observed_close_generation':None},'deadlines':{'original_execution_deadline':None,'start_admitted_at':None,'step_deadline':None},'process_evidence':{'spawn_count':None,'spawn_and_exit_records':[],'in_flight_resolved':None},'evidence':[]},
 'parameter-validation.template.json':{**base,'case_id':None,'scope':None,'environment':None,'release_id':None,'release_digest':None,'binding_generation':None,'parameter_contract_version':None,'execution_id':None,'spec_hash':None,'intent_presence_and_values':None,'resolved_configuration':None,'resolved_configuration_sha256':None,'effective_configuration':None,'effective_configuration_sha256':None,'actual_optimizer_steps':None,'metrics_sample_count':None,'preset_response_evidence':None,'historical_detail_evidence':None,'actual_frontend_evidence':None,'evidence':[],'untested_assertions':[]},
 }
 for n,obj in templates.items():write_json(root/'records'/n,obj,True)
 candidate={'scope':'DESIGN_TEST_VECTOR_NOT_APPROVED_RELEASE','document_version':VERSION,'spec_sha256':sh,'source_section':'A07.4 / D02.6.1','status':'NOT_RUN','do_not_import_as_release':True,'baseline':{'learning_rate':'0.0001','max_steps':20},'nondefault_candidate':{'learning_rate':'0.0002','max_steps':10},'formal_activation':'NOT_APPROVED','note':'当前先测正式解析与实际负载；BFF/前端M3-B补，不删除最终参数验收。B不是已批准值。'}
 write_json(root/'records/parameter-validation-plan.json',candidate,True)
 vectors=[]
 for r in read_tables(excerpt('D02.6.1',by),r'^\| `'):
  inp=json.loads(r[0].strip('`'));lr,steps=[x.strip() for x in r[1].split('/')]
  vectors.append({'input':inp,'resolved':{'learning_rate':lr,'max_steps':int(steps)},'expected':'REJECT' if '拒绝' in r[2] else 'ACCEPT','note':r[2],'status':'NOT_RUN'})
 write_json(root/'records/parameter-default-combination-vectors.json',{'scope':'CONTRACT_TEST_VECTORS_NOT_A_RELEASE','spec_sha256':sh,'source_section':'D02.6.1','status':'NOT_RUN','precondition':'测试规则默认A且仅允许完整A/B；真实批准需各自验收。','cases':vectors},True)
 change=excerpt('D14.6',by)
 (root/'CHANGELOG.md').write_text('# v4.2.2 变更记录\n\n'+change+'\n完整差异：`review/spec-v4.2.1-to-v4.2.2.diff`。底稿原件：`reference/spec-v4.2.1.md`。\n')
 original=(root/'reference/spec-v4.2.1.md').read_text()
 (root/'review/spec-v4.2.1-to-v4.2.2.diff').write_text(''.join(difflib.unified_diff(original.splitlines(True),spec.splitlines(True),fromfile='reference/spec-v4.2.1.md',tofile='spec.md')))
 actions=[r for r in read_tables(change,r'^\| ') if len(r)==4 and r[0] not in ('讨论项','---')]
 write_json(root/'review/revision-actions.json',{'scope':'DOCUMENT_ONLY','document_version':VERSION,'source_section':'D14.6','actions':[{'topic':r[0],'decision':r[1],'locations':r[2],'limit':r[3]} for r in actions]})
 cards=[
  (G_FILE,'01 · 当前从这里开工','主流程开发手册：测试入口、当前阶段完成条件、Fedora与集群、后续BFF候选操作。'),
  (A_FILE,'02 · 业务与流程','最终架构不变。先执行与产物，后真实用户入口；模型目标、参数与完成层级明确。'),
  (D_FILE,'03 · 开发与Model接缝','R1逐写派发仲裁、既有启动/停止、单一参数合同、W0–W6与分段用例。'),
  (R_FILE,'04 · 变更与文档复核','从v4.2.1到v4.2.2的完整差异、来源锁定、静态/浏览器检查及未验证边界。')]
 chtml=''.join(f'<article class="intro-card"><h2>{t}</h2><p>{d}</p><a href="{u}">打开阅读 →</a></article>' for u,t,d in cards)
 whtml=''.join(f'<div class="task-row"><strong><a href="tasks/{r[0].split()[0]}.md">{html.escape(r[0])}</a></strong><div>{html.escape(r[1])}</div><div class="document-meta">后续与不阻塞边界：{html.escape(r[2])}</div></div>' for r in works)
 hub=f'''<main class="hub-layout"><div class="hero"><p class="eyebrow">EXECUTION FIRST / SAME FINAL CONTRACT</p><h1>CPU 受管微调<br>{VERSION} 完整计划包</h1><p>先打通真实执行与产物主链，再接真实BFF与前端；接口合同现在固定，最终验收不缩水。</p><div class="chips"><span class="chip">32 FT + 15细化 + 10 PA</span><span class="chip">7个原工作包</span><span class="chip">Model按能力后接</span><span class="chip">Fedora保留</span></div></div><div class="notice"><strong>当前阶段不等BFF：</strong>M3-A使用隔离应用层测试驱动与真实ModelDev/KFP/Trainer/S3，完成训练、导出、校验、发布确认与停止关闭。真实BFF/前端及候选绑定在M3-B补齐；主链通过不冒充Gov授权、Model READY或整版通过。</div><div class="intro-grid">{chtml}</div><section class="chapter"><h2>当前 → 后续 → 最终</h2>{decorate_md(excerpt('A12',by),'hub-stage')}<p><a href="{D_FILE}#D11-11-6">完整阶段用例映射</a> · <a href="{D_FILE}#D12-12-6">如何用隔离测试驱动主链</a></p></section><section class="chapter"><h2>这轮三项审核怎么落实</h2><p><strong>R1：</strong>具体CreateModel、版本登记及必要存储写入逐次与Stop/deadline仲裁；原意图不批准所有后继；迟到结果如实保存。</p><p><strong>R2：</strong>候选通过真实BFF验收的操作说明只在M3-B前落实，当前不等待。合同先定、上游测试边界清楚、不新增生产旁路。</p><p><strong>R3：</strong>五个完整组合／默认黄金向量纳入原PA03/06/07，不自动补出未批准的组合；B仍为待测候选。</p><p><a href="{D_FILE}#D09-9-3">登记派发与停止</a> · <a href="{D_FILE}#D02-2-6-1">参数组合向量</a></p></section><section class="chapter"><h2>原七个工作包</h2>{whtml}<p><a href="tasks/index.md">任务索引</a> · <a href="guides/core-flow-first.md">主流程手册Markdown</a> · <a href="records/acceptance-plan.json">验收计划</a></p></section><section class="chapter"><h2>单一正文与分工</h2><p>先读<code>spec.md</code> A12/D11.6/D12.6，再读对应W任务。沿用已登记的<code>.scratch/finetuning-cpu/spec.md</code>或唯一ANI-doc位置；不是要求两处双写，不强制把scratch加入Git。</p><p>本机编辑/Git；<strong>Fedora生成、编译、测试、镜像/Pipeline及负载</strong>；172.16.101.10～12的K8s执行真实部署和联合验收。所有真实路径、身份和资源取现有批准记录，不猜。</p><p><a href="model-integration-requirements.md">Model需求</a> · <a href="parameter-and-frontend-contract.md">参数与后续前端合同</a> · <a href="gpu-extension-boundary.md">GPU独立准备</a></p><p><a href="README.md">采用与维护</a> · <a href="spec.md">完整正文</a> · <a href="manifest.json">摘要清单</a></p><p class="document-meta">spec SHA256：{sh}</p></section><div class="footer">DOCUMENT_ONLY · 未操作业务仓库、Fedora、Model、BFF、集群或GPU。新业务验收初始NOT_RUN。</div></main>'''
 # Hub also includes the source references used by embedded A12.
 hub=hub.replace('</main>',f'<section class="chapter" id="sources"><h2>来源与证据</h2>{decorate_md(by["sources"]["body"],"sources")}</section></main>')
 (root/'index.html').write_text(html_shell(root,f'ANI CPU受管微调 {VERSION} · 主流程优先计划包',hub,sh))
 review=(root/'review/self-review.md').read_text()
 rbody=f'<main class="hub-layout"><div class="hero"><p class="eyebrow">DOCUMENT REVIEW / NOT BUSINESS EVIDENCE</p><h1>{VERSION}<br>变更与自复核</h1><p>正文、派生视图、阶段用例与记录同步；检查结果只适用于文档。</p></div><section class="chapter">{decorate_md(change,"revision")}</section><section class="chapter">{decorate_md(review,"review")}</section><section class="chapter"><h2>可核查文件</h2><p><a href="review/spec-v4.2.1-to-v4.2.2.diff">完整diff</a> · <a href="reference/spec-v4.2.1.md">原件</a> · <a href="review/document-checks.json">静态检查</a> · <a href="review/browser-checks.json">浏览器检查</a> · <a href="review/source-lock.json">来源锁</a></p></section><section class="chapter" id="sources"><h2>来源与证据</h2>{decorate_md(by["sources"]["body"],"sources")}</section></main>'
 (root/R_FILE).write_text(html_shell(root,f'ANI {VERSION} · 变更与自复核',rbody,sh))
 (root/'README.md').write_text(f'''# ANI CPU受管微调 {VERSION} 完整计划包

平台基线仍v0.4；直接底稿v4.2.1。采用两轮讨论：R1具体登记派发与停止共同仲裁，R2真实BFF验收后置，R3组合/默认黄金向量。当前先完成真实执行与产物主链；最终BFF、前端、Model、独立Inference及原参数范围不删。GPU独立准备不阻塞CPU。

## 阅读顺序

1. 打开`index.html`，当前实施优先阅读`{G_FILE}`。
2. 权威`spec.md`先看A12、D11.6、D12.6：当前做什么、到哪里验收、不经过BFF怎样隔离测试。
3. 再看`tasks/index.md`和对应W任务。W1先定合同/解析，W3/W6先主链；W1-P4/W6-P2为后续BFF工作。
4. 登记实现前读D09.3；参数组合读D02.6.1；真实BFF开始前再读D12.7。

## 文件

| 文件 | 用途 |
|---|---|
| {A_FILE} | A01–A12业务、范围、对象、流程与阶段 |
| {D_FILE} | D01–D14开发与Model接缝 |
| {G_FILE}、guides/core-flow-first.md | 从权威正文切片的当前开工手册 |
| model-integration-requirements.md | MR需求及原登记派发仲裁 |
| parameter-and-frontend-contract.md | 合同先行、前端后接的同源视图 |
| gpu-extension-boundary.md | 原GPU独立准备，不新增当前前置 |
| tasks/W0.md–W6.md | 原七工作包的当前与后续交付 |
| records/acceptance-plan.json | 32FT、15细化（原12+新3）、10PA、34原映射，分阶段记录 |
| records/*.template.json | 另存实际进度/主链/竞态/BFF候选/参数与结果；不填假证据 |
| reference/spec-v4.2.1.md | 1375行未改动底稿；更早reference保留 |
| CHANGELOG.md、review/spec-v4.2.1-to-v4.2.2.diff | 本轮完整修订与差异 |
| review/*-checks.json | 仅文档静态/浏览器检查，不是业务验收 |

## 采用位置与进度保护

保持包目录，不只复制一个HTML。先保存旧v4.2.1及已填任务进度，再审阅本版。实际权威路径沿用`<实际ani-modeldev-service工作树>/.scratch/finetuning-cpu/spec.md`；若已迁ANI-doc，以已登记唯一位置为准。不要双写主方案，不以本包强制`git add -f` scratch。

本包权威正文SHA256：`{sh}`。采用记录包含实际绝对路径、版本/SHA与仓库commit；本包没有给出假Fedora路径、BFF地址或集群API端点。旧CPU-P01/ENV职责不变，不把历史所有goal重新拼接执行。本包不是新的全平台总控。

## 机器职责

本机阅读、编码、文档、Git与受控转运；既有SSH别名`fedora`上的批准目录负责生成/编译/模块测试/镜像与Pipeline/真实负载；.10～.12所属K8s执行真实ModelDev/KFP/Trainer和集成。不要在节点宿主机改代码、构建或裸跑服务。

## 记录口径

WORKLOAD、CORE_FLOW、MODEL_INTEGRATION、BFF_INTEGRATION、SCHEME_FINAL、GPU_PRECHECK是测试记录scope，不是运行API字段/授权能力。当前不等BFF，后置不等于DEFERRED；Model没登记不能标A10单次成功；同一FT/PA拆断言记录，局部PASS不覆盖未验证的上游权限。

原验收计划全部NOT_RUN。旧的实际结果单独保留并评估可否复用，不自动迁移为新PASS。当前只做文档，不代表计划中任何命令已存在或已获远程写入授权。

## 文档重建

只编辑spec.md。项目侧按原约定在Fedora使用`tools/requirements-document-tools.txt`中的文档依赖，运行`tools/render_package.py`，随后`tools/check_package.py`、`tools/check_browser.py`，最终`tools/seal_package.py`。本包工具只处理文档，不编译/部署业务。浏览器检查需要已有Playwright和可用Chromium，缺少则报告NOT_RUN。工具通过set_content检查本地HTML呈现/页内操作，不宣称跨页导航通过；链接目标另做静态检查。本次环境的导航限制和未测项见review/browser-checks.json，不将环境失败改成PASS。

计划/模板有修改时生成器会拒绝覆盖，先另存实际结果再比较更新；不要靠删除已填记录使检查通过。更新self-review后重新派生HTML并重复检查。manifest不包含自身，SHA256SUMS覆盖payload与manifest；整包外部SHA另列。
''')
 print(json.dumps({'version':VERSION,'spec_sha256':sh,'chapters':26,'work_packages':7,'ft':len(ft),'subcases':len(sub),'pa':len(pa),'mr':len(mr),'vectors':len(vectors)},ensure_ascii=False))

if __name__=='__main__':main()

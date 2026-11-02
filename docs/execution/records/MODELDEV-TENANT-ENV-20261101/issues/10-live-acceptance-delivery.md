# 10 指定集群 A/B 验收、旧代码清理与交付

Status: ready-for-agent
Resolution: resolved
Blocked by: None

## Goal

完成指定环境全部必要合同与A/B真实矩阵，核对清理和共存边界，形成可追溯的最终实施交付。
依据：[spec.md](../spec.md) §13.5、§15、§17；只有全部必要证据满足才将相关任务resolved，未测不等于通过。

## Scope

- 三仓本次相关代码/配置/测试/正式文档与09新部署；只在既定测试集群使用合法A/B身份。
- ModelDev CONTEXT.md、正式环境/API/部署合同、ADR0001过时静态描述；ADR0002原release权威保留。
- 包内 acceptance-results.tsv、deployment-inputs.json、implementation-report.md、证据目录及各issue Result。
- 09的清理manifest、原charge/release ACK、保留共存资源、四定义/IR/bundle和最终部署imageID记录。
- 按仓库要求在批准远端运行定向集成与make verify；本地仅轻量编辑，不扩其他集群/账户/Inference。

## Implementation

1. 复核01–09实际Result及所有NS/EN/API/SNP/ST/DP/CL/IM/RS/AD组件行，缺证据补定向验证而非沿用设计声明。
2. LIVE01/02：重核五项身份、完成部署输入与旧manifest，保存策略状态/无CEL warning/正反server dry-run和最小RBAC证据。
3. LIVE03：同一进程Ensure A再B、A并发重放及Get；记录服务Pod UID/启动时间不变、各一READY与Experiment。
4. LIVE04：真实投影control token读取A/B Experiment及ani-system父Pipeline/Version；正确SAR与错误audience/未授权目标否定例。
5. LIVE05：保持同一进程观察实际token轮换，完成其他测试后再次LIVE04；只记录非敏感代际/期限，调整合法TTL必须恢复。
6. LIVE06：A小型GENERAL_TRAINING CPU成功，B小型FINETUNING GPU_SHARED成功，完整记录原UID链、I/O/logs/publish/Model及B GPU释放ACK。
7. LIVE07/08：B另一次小CPU运行中合法Stop且A查询可用；A访问B详情/日志/Stop/artifact授权及至少一次反向访问均拒绝无副作用。
8. LIVE09：实际受管镜像pull、STS跨scope否定、普通tenant原groups对Secret/挂载/reserved SA/exec/TokenRequest的边界验证。
9. LIVE10：短维护窗口受控webhook不可达，普通VM/Instance代表Pod与MD Pod server dry-run；立即恢复并记录探针，不创建多余VM。
10. LIVE11：复核原清单/旧表/Gov commands/旧配置引用为零及全额退额；共存VM/Sandbox/无关PVC UID和健康保持。
11. 删除旧静态binding/facts/STS数组/webhook上限/按tenant全局策略脚本/旧allowlist/v3 fixture等残留，更新文档并完成必需仓库gate。
12. 填每行PASS/FAIL/BLOCKED/NOT_RUN和证据路径、实际值；记录最终commit/API pin/image映射/材料摘要/保留资产，再更新真实完成任务Result。

## Acceptance

- NS01、NS02、EN01–EN04、API01–API02、SNP01、ST01–ST02全部有有效证据，不以live成功替代关键负向合同。
- DP01–DP03、CL01–CL03、IM01、RS01、AD01–AD02全部通过，GPU_WHOLE仅列实际完成的定向回归。
- LIVE01–LIVE05证明目标正确、策略/权限、同进程A/B开通与实际token轮换；无敏感token或Secret值输出。
- LIVE06–LIVE09证明CPU训练/GPU_SHARED微调、Stop、跨租户拒绝、镜像/存储/Secret边界及原release链。
- LIVE10–LIVE11证明webhook故障隔离、即时恢复、旧资源清零/账目及共Namespace其他owner健康。
- 最终三仓提交、正式API pin、生成物、部署映射、全部输入、任务Result和实施报告一致；所有必要行满足后才声明完成。

## Constraints

- 没有合法GPU_SHARED preset/容量或B基模则具体BLOCKED，不能换CPU宣称全链通过；不声称未实测GPU_WHOLE。
- 不删除真实Namespace/SA做UID负例，不实际创建额外VM；UID重建用fake API/事务fixture。
- 不手造token、靠重启冒充轮换或长时间循环sleep；不以直接伪造tenant header替代实际Gov授权。
- 不把旧报告UNKNOWN升级成通过，不把NOT_RUN/BLOCKED记PASS，不用单次旧A CPU代替新A/B矩阵。
- 不保留无用旧兼容入口，不扩共享owner策略/ACC/Model协议；必要外部阻塞写精确对象/调用/原因和剩余项。

## Result

### 本轮最终回填（2026-11-02 Asia/Shanghai）

- 当前结果：**PASS / RESOLVED**。原必需验收逐项PASS，新增REG01最终版本A4普通训练/B5 GPU微调完整流程PASS，日志/下载/Model READY/自动回收/原退额均有实际证据；见 acceptance-results.tsv、evidence/10/completion-multitenant-regression/result.json。
- 组件、外部double与真实现场范围分开；按用户要求本轮未重跑全量make verify，未实测GPU_WHOLE。具体证据及边界见[完整报告](../implementation-report.md)与[逐项验收](../acceptance-results.tsv)。

### 历史固定源码与此前checkpoint（保留原事实，当前状态以上文为准）

- Outcome: IN_PROGRESS。当前恢复原66真实A/B主线；A3 CPU成功与LIVE07运行中Stop已实际完成，B2失败已正常全退；B3真实训练/export成功但verify Begin409且已正常全退，B4八步骤/Model READY/原退额完成，下载SHA待回，不宣称Goal完成。
- Target/runtime: kubercloud@100.64.128.12 / kubernetes-admin@ani-kcn-full / clusterf39bf6e2…；主服务1dfd6941e5780eca0d55dc4e5fbbd2f58e0710ab、imagefe944d52…、Podb98cfbef-fd6d-4a8f-99fe-d47b6ed2d6f9/15:53:50Z/0restart，配置保持。新10及其训练前停止要求仅作历史checkpoint。见 [当前实际部署](../evidence/10/main-verify-time-1dfd6941-19240-release/rollout-actual.json)。
- Completed actual acceptance: 新A/B显式Ensure/Get READY；LIVE04/05同进程真实token访问/自然轮换及指定403/401；A3实际CPU loss下降、五步骤/模型发布/下载SHA一致并原owner清理APPLIED；LIVE07实际Running后正常Stop、writer退出、原Compute APPLIED；LIVE08已7项双向详情/日志/Stop及B→A artifact拒绝/0副作用，A→B成功artifact负例仍待B成功产物。证据见[验收表](../acceptance-results.tsv)、[A3](../evidence/10/resumed-mainflow/a3-original-owner-compute-cleanup-final.json)、[Stop/跨租户](../evidence/10/resumed-mainflow/live08-normal-jwt-negative-and-b-cpustop-1793539024651916679.json)。
- B2: 原设备Allocate拒绝后终态失败/Python未启动事实保留；2ac使严格never-created证明正常读回，原ownerCLOSED/Step及Compute APPLIED，原event9c2cac54… ACK APPLIED、原charge24570全退/account44version4 occupied0=Σ0，无人工改账。见 [原最终收据](../evidence/10/main-admission-abort-19240-release/b2-original-close-release-1793540950210389961.json)。
- User authorization/native: 用户明确以原平台SA/nativeRBAC信任方式覆盖§11.1–§11.3的平台逐字段限制；native源码4d6013/VAP10/MWC3已实际安装，其他租户身份/owner/spec/Secret边界保留，0新增RBAC/身份。见 [最终native](../evidence/10/trusted-platform-native-final/actual.json)。当前5b99仍pin7，追加5.4/pin维护按用户主线优先未执行；不得冒称§7冻结材料已更新。
- Remaining: B4微调/19产物发布/Model READY及原24570全退已完成，实际下载SHA与Model正式GET/跨租户artifact否定收据待回；LIVE09/10剩余NOT_RUN不前置；LIVE08剩余成功artifact否定及最终旧入口/保护项/工具总账、pins收敛尚未全部完成。用户本轮禁止新tests/verify/扫描，历史组件PASS不移植到新源码；不新调查、备份或另写总结。
- Delivery: 继续唯一[八节实施报告](../implementation-report.md)及[原验收表](../acceptance-results.tsv)回填；根docs正式promote等待主线结果。原任务正文与历史Comment保留。
- B3/current repair: 原execution a3b48752…真实train loss7.0513/export exit0；verify Begin409，Python verify未运行。原owner自动7compute全gone/原event ACK APPLIED/24570全退/account44version6 occupied0=Σ0，见[B3收据](../evidence/10/resumed-mainflow/b3-original-close-and-release-final-1793547050096007731.json)（SHA256 2307f1a605c62d7e90dd0749c7023581c827dd4358da0d57cb5c60e18e152af4）。1dfd6941仅修runtimeproof/finetuning_steps.go中exportReportedFinishedAt纳秒与Pod main.finishedAt秒精度比较，见[实际Ready](../evidence/10/main-verify-time-1dfd6941-19240-release/rollout-actual.json)；原Pod native秒值未保留，不能宣称已完全证实唯一根因。B4已在修复后通过verify及全部8步骤；这不补造原B3 Pod已缺失的秒值。
- B4 actual: execution59f92b91-5e86-4dd1-90ec-cb1037756758已SUCCEEDED/PUBLISHED/CLOSED，8steps SUCCEEDED/19产物PUBLISHED/loss7.05126428604126，Model454cef60-ca01-4dae-a4e6-b8881026c688/version6afb1f32-a0d4-4c92-8d82-7799e450f5ee登记READY。见[正常读回](../evidence/10/resumed-mainflow/readback-B-1793548972071555495.json)、[原生观察](../evidence/10/b4-real-finetuning/current.json)。原charge9249ff3e…24570全退/account44version8 occupied0=Σ0、原event ACK APPLIED/7compute gone，见[最终释放](../evidence/10/resumed-mainflow/b4-original-close-and-release-final-1793549008812487147.json)（SHA256 f3dbe26c3623f4a308e82833c98ef97e890ea9fa3d50beb57a2cfe73cedb8d4a）。下载SHA/Model正式GET及A→B artifact否定收据待Root/snapshot，LIVE06未提前PASS；原pins漂移/追加5.4未执行及剩余现场用例不被本次成功覆盖。

## Comments

以下为按时间保留的历史记录；当前状态以本文件 Result 为准。

- 初始任务，尚无实施或验收完成声明；后续按时间追加事实与证据。
- 达到本项验收后再更新 Resolution 和 Result；后续现场证据不得提前标记通过。

第二轮补齐EN02真实25秒预算续跑与Gov真实JWT/Redis/Casbin/PG→HTTP→mTLS peer正向/撤权组件集成，均exit0；19项组件行PASS不代替AD/native或11项LIVE。Gov原default/race exit0、make check exit2、audit恢复后续一次exit1已取得，失联/未知状态已解除。新发现官方v1原UID归档，但第三原live/denial/writer资格仍不满足，两原训练日志仍缺；详新的09归档评估，不再泛称所有Workflow归档不存在。

当前续跑状态：用户已批准报告§7限定一次性退休修正（两日志不可恢复如实记载、原UID archive明确来源）；原先待裁定问题已答复，不再要求补不存在的归档。现场仍须原exit/writer/PVC/fence/CAS/cleanup/原事件ACK，未通过。旧API维护45ed49完整make check/audit PASS且原Workbench job eda85e66a4f24c3895b7be87a5211500已提交；目前指定SSH连接关闭与本地SOCKS GET连接关闭，最终job/503未确认，无A/B真实PASS。当前证据见evidence/09/retirement-amendment-approval-20261101.json、entry-connectivity-20261101.json、workbench-local-socks-connectivity-20261101.json。

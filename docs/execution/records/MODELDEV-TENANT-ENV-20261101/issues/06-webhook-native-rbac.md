# 06 动态 webhook、native 策略与精确权限

Status: ready-for-agent
Resolution: resolved
Blocked by: None

## Goal

使共享webhook按live投影验证ModelDev资源，并让共Namespace普通工作负载免受该远端webhook影响。
依据：[spec.md](../spec.md) §6.3、§10–11；使用03投影与04四参数/v4合同，保持原退出证据及合法镜像继承。

## Scope

- 仅 ani-modeldev-service；internal/server/modeldevwebhook/{config,kubernetes,policy,training,handler}.go。
- cmd/ani-modeldev-webhook/main.go与相关fixed catalogue/profile接线。
- deploy/modeldev-webhook/render.py、deploy/modeldev-tenant-policy/render.py及固定bootstrap/native RBAC模板。
- 原owner chain、stepidentity/trainer镜像合同、Pod UPDATE/ephemeral guard必要共享校验。
- 包内精确KFP RBAC、artifact、projection合同作为输入；实际principals由01/08证据提供，不猜测。

## Implementation

1. 删除启动bindings数组及旧数量上限，保留单个全局ani-modeldev-webhook；每次匹配精确GET live Namespace和固定投影。
2. 严格验证projection schema/规范字节/digest、tenant/cluster/原UID和受管材料；未知binding不再outside_scope。
3. GET原SA并比较UID与exact refs，解析实际owner chain和原对象UID；不读业务DB/Gov，不引入缓存。
4. 所有KFP Pod含root/backend driver，从原Workflow固定runtime-config路径严格解析四参数和pipeline_root，重复/未知字段拒绝。
5. 按固定profile/image/command/support images、资源向量、workspace、creator与退出证据验证；verifier身份命中明确拒绝新Pod。
6. matchConditions以reserved SA、保留标记/finalizer、reserved PVC、artifact Secret引用任一项OR匹配，所有可选字段先has。
7. 非法creator不能放入匹配条件而被跳过；错误Namespace同样进入验证；普通VM/Instance/default Pod不进入远端。
8. 固定三个native VAP/Binding：bootstrap写约束、受管配置保护、工作负载guard；失败关闭且Deny。
9. bootstrap策略约束所有Namespace中的control写，再验证标准name/labels、固定对象/key/roleRef/subject；先策略后create/bind授权。
10. UPDATE使用oldObject防删标记绕过；冻结Workflow模板/参数/entrypoint/SA，精确允许KFP API principal将activeDeadlineSeconds设0，以及spec不变的Argo全对象UPDATE与必要status/metadata/finalizer路径。
11. 保护Pod模板、ephemeralcontainers和artifact Secret/SA旁路；所有Namespace禁止三个reserved SA的legacy service-account-token Secret CREATE/UPDATE；普通tenant实际groups宽权限冲突必须明确BLOCKED，不自动重写其他owner授权。
12. 生成最小ClusterRole/RoleBinding：Namespace仅GET，固定resourceNames读/patch和有限bind；按contracts/authorization-inventory.md增加明确的只读RBAC授权边审查权限；镜像合法SA继承按exact refs保留。

## Acceptance

- AD01：任一保留身份/引用命中；去标签不能逃逸；unknown projection deny；普通Pod远端请求计数为零。
- AD02：四参数覆盖driver，迟到旧UID拒绝；Workflow模板、Pod UPDATE/ephemeral绕过失败；正常terminate/status通过。
- EN04、IM01：坏/未知投影拒绝；合法imagePullSecrets继承通过，额外/前缀伪装及SA UID变化拒绝。
- NS02：live Namespace异常拒绝，组件action中无Namespace create/patch；不因共享NS部署干扰无关owner。
- LIVE02、LIVE10：本项提供server dry-run正反对象和恢复步骤；实际安装/故障验证在09/10，不能用渲染成功声称现场通过。
- LIVE04、LIVE09：精确SAR矩阵和Secret/SA借用否定例可执行；真实principal、token和普通tenant权限由后续live验证。

## Constraints

- 不增加tenant级webhook item、全租户启动map、正/负缓存、DB访问或回退outside_scope。
- 不改共享ani-kfp-trainjob、不添加ani.io/kubeflow-tenant标签迁就它，不全局放宽已有策略。
- 不使用通配RBAC、任意Secret GET、impersonate、serviceaccounts/token或control自建Role/ClusterRole能力。
- 不将所有Pod送远端后handler no-op称为隔离；不冻结整个对象而阻断合法关闭。
- 只生成/验证新模板；旧环境尚运行时不得替换全局策略、撤销旧权限或切换webhook，统一交09。

## Result

### 本轮最终回填（2026-11-02 Asia/Shanghai）

- 当前结果：**PASS / RESOLVED**。最终native四项真实拒绝、AD01/AD02原组件层级及LIVE10真实失败关闭/恢复通过；固定平台trust按用户批准修正，见 evidence/10/completion-admission/ad01-ad02-coverage.json、result.json。
- 组件、外部double与真实现场范围分开；按用户要求本轮未重跑全量make verify，未实测GPU_WHOLE。具体证据及边界见[完整报告](../implementation-report.md)与[逐项验收](../acceptance-results.tsv)。

### 历史固定源码与此前checkpoint（保留原事实，当前状态以上文为准）

- Outcome: 最终平台trust native安装已实际完成；AD01/AD02未完成的租户现场子项仍BLOCKED/NOT_RUN，Resolution保持open，不以CEL通过冒称业务完成。
- Current: 原66/ani-kcn-full。native源码4d6013c5已安装到原VAP UID72edb341…generation10/observed10/typeChecking{}及原MWC UIDc452d29d…generation3；2次UID/RV/fullspec守卫CAS、0新增RBAC/身份。见 [最终实际](../evidence/10/trusted-platform-native-final/actual.json)、[固定用户名](../evidence/10/trusted-platform-native-final/trusted-platform-principals.json)。
- User override: 附件原Implementation/Acceptance正文保留。用户明确覆盖§11.1–§11.3平台逐字段限制：KCN、Multus、Volcano scheduler/device-plugin、原精确nodes、GC、Trainer、JobSet整体跳过workload VAP/命中的CREATE Pod MWC，无逐key/UID锁/兼容分支；persistence仅UPDATE status或sameSpec，未放开CREATE/spec改变。Argo/JobController必要CREATE注入保留；租户身份/owner/spec/Secret保护保留，Inference/其他策略未改。
- Frozen inputs: §7租户冻结规则保留。当前5b99配置仍pin7，最终VAP10/MWC3形成明确bootstrap drift；用户主线优先，追加5.4/pin收敛未执行，不静默改原ProvisionSpec/READY记录。
- Actual main flow: A3 CPU训练/发布/下载/原owner APPLIED完成，LIVE07运行中Stop与同期A GET200完成；B2正常CLOSED/APPLIED/原event ACK APPLIED/24570全退。B3真实train loss7.0513/export成功，但verify Begin409而非Python verify失败；原owner自动7compute全gone/原24570全退。1dfd6941已Ready，B4已实际8步骤成功/19产物PUBLISHED/Model READY/正常CLOSED及原24570全退；下载SHA收据待回，LIVE06未提前PASS。见 [A3](../evidence/10/resumed-mainflow/a3-original-owner-compute-cleanup-final.json)、[LIVE07](../evidence/10/resumed-mainflow/live08-normal-jwt-negative-and-b-cpustop-1793539024651916679.json)、[B2](../evidence/10/main-admission-abort-19240-release/b2-original-close-release-1793540950210389961.json)。
- Evidence boundaries: 历史d423完整门禁及原native dry-run/handler组件PASS仍仅对应历史源码；本轮2ac/4d6013/1dfd6941按用户要求未跑新tests/verify/矩阵。LIVE04/05实际投影访问/自然轮换已PASS，LIVE09/10剩余NOT_RUN不作为B4前置；未宣称整个Goal完成。
- B3/current repair: 原execution a3b48752…真实train loss7.0513/export exit0；verify Begin409，Python verify未运行。原owner自动7compute全gone/原event ACK APPLIED/24570全退/account44version6 occupied0=Σ0，见[B3收据](../evidence/10/resumed-mainflow/b3-original-close-and-release-final-1793547050096007731.json)（SHA256 2307f1a605c62d7e90dd0749c7023581c827dd4358da0d57cb5c60e18e152af4）。1dfd6941仅修runtimeproof/finetuning_steps.go中exportReportedFinishedAt纳秒与Pod main.finishedAt秒精度比较，见[实际Ready](../evidence/10/main-verify-time-1dfd6941-19240-release/rollout-actual.json)；原Pod native秒值未保留，不能宣称已完全证实唯一根因。B4已在修复后通过verify及全部8步骤；这不补造原B3 Pod已缺失的秒值。
- B4 actual: execution59f92b91-5e86-4dd1-90ec-cb1037756758已SUCCEEDED/PUBLISHED/CLOSED，8steps SUCCEEDED/19产物PUBLISHED/loss7.05126428604126，Model454cef60-ca01-4dae-a4e6-b8881026c688/version6afb1f32-a0d4-4c92-8d82-7799e450f5ee登记READY。见[正常读回](../evidence/10/resumed-mainflow/readback-B-1793548972071555495.json)、[原生观察](../evidence/10/b4-real-finetuning/current.json)。原charge9249ff3e…24570全退/account44version8 occupied0=Σ0、原event ACK APPLIED/7compute gone，见[最终释放](../evidence/10/resumed-mainflow/b4-original-close-and-release-final-1793549008812487147.json)（SHA256 f3dbe26c3623f4a308e82833c98ef97e890ea9fa3d50beb57a2cfe73cedb8d4a）。下载SHA/Model正式GET及A→B artifact否定收据待Root/snapshot，LIVE06未提前PASS；原pins漂移/追加5.4未执行及剩余现场用例不被本次成功覆盖。

## Comments

- 初始任务，尚无实施或验收完成声明；后续按时间追加事实与证据。
- 达到本项验收后再更新 Resolution 和 Result；后续现场证据不得提前标记通过。

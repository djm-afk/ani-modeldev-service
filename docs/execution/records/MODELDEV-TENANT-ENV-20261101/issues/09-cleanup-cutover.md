# 09 原环境关闭清理、Res 回填与一次切换

Status: ready-for-agent
Resolution: resolved
Blocked by: None

## Goal

按原关闭/释放协议排空并精准清理旧ModelDev，再完成共享Namespace UID维护和仅v4新系统切换。
依据：[spec.md](../spec.md) §1、§13、§16；本项是唯一旧环境清理/运行切换任务，保留共Namespace其他owner资产。

## Scope

- 三仓本次已完成提交、旧ModelDev/Gov原聚合和指定测试集群；写前核对五项集群身份。
- ModelDev原Stop/close/cleanup/GPU release和一次性manifest维护工具；Gov精确Create维护入口与publisher生命周期。
- 02的Res nullable UID migration/精确回填命令及新Res配置；既有Network/VM/Instance记录保留。
- 08准备的四定义/镜像/bundle、06固定bootstrap/native policy、主服务与webhook部署；旧配置入口的移除。
- 只操作精确manifest原ID/UID/对象版本；记录清理摘要、FK/行数、quota恒等式与部署映射。

## Implementation

1. 核对指定入口/context/API/cluster_id/kube-system UID；不一致停止写。生成非敏感旧资源清单和摘要，维护工具先只输出计划。
2. 精确停止Gov ModelDev新Create与Create publisher新claim，等待在途lease；原Stop/close/cleanup/release继续，逐原operation对账。
3. 固定原tenant/operation/execution/spec/dispatch/Run/Workflow/namespace及子UID、存储引用、charge/outbox/ACK，区分独占与外部引用资产。
4. 原尚未发送/未知/已发送分别走原fence与Stop/close；先停止原创建者，持久step退出，按原UID/RV处理自有finalizer。
5. 先保存日志，再按原TrainJob→JobSet→Job→Pod→PodGroup→合格临时PVC→Workflow→TrainingRuntime顺序cleanup，保留writer检查和3次上限。
6. 符合原资格后seal原GPU scope+outbox并取得Gov匹配ACK；scope-without-outbox仅按ADR0002一次性journal原事件结清，不SQL清零。
7. 核验每charge全额释放和账户恒等式、原ACK落盘；有未决创建/写者/账目即保留对应证明并BLOCKED，不继续破坏其依据。
8. 停止旧MD worker和Gov旧项重放，确认无active lease/迟到Create；再删manifest内独占旧PVC/配置/SARB/KFP/S3版本，保留共享及外部资产。
9. 按spec九表FK顺序删除精确旧聚合；Gov在实际FK受控事务删全释放原项，保留账户/租户/角色/套餐/政策与其他owner。
10. Res小窗口停新Network身份接纳并等在途→新增UID列→精确回填验零NULL→上线拒NULL/不自动建NS新Res→恢复写，禁止旧Res再写NULL。
11. 核验旧可恢复执行/可投递command/绑定配置为零；按08工具发布四定义并回读ID/digest，先native策略验证再授create/bind及部署新服务。
12. 记录commit→image digest→imageID；装同一bundle并显式Ensure A再B；未满足前置保持BLOCKED，交10继续完整LIVE验收。

## Acceptance

- CL01、CL02、CL03：原关闭、writer/UID保护及GPU事件/ACK完整；不以Get404或物理GPU空闲代替原资格。
- RS01：回填前后精确计数、既有UID不覆盖、零NULL；运行入口拒NULL，旧Res不会回填后继续写空。
- LIVE01、LIVE02：目标身份和部署输入有证据，native模板无编译/type-check warning，实际正反dry-run满足固定合同。
- LIVE03：同一新MD/webhook部署可先A后B显式开通；完整并发/不重启证据由10补齐，未达READY如实阻塞。
- LIVE11：manifest旧资源、九表旧行和Gov可投递项为零；原charge全退，A/B原VM/Sandbox/无关PVC UID与健康保持。
- 切换记录含精确manifest摘要/删除数量/保留资产/发布IDs/最终镜像映射；LIVE04–LIVE10不在本项伪记PASS。

## Constraints

- 不删标准或旧Namespace本身，不DeleteCollection/整桶/全库TRUNCATE CASCADE，不以清空其他owner资源达到计数目标。
- 不跳过原release ACK，不SQL改occupied_units，不加ACC ACK前置，不重发不确定Run或更换原operation。
- 不将现有ApplyExecutionCleanup成功当PVC/S3/DB已全清；KFP DeleteRun2xx/Get404不证明Workflow删除。
- 未排空旧执行前不撤销旧运行所需权限、替换破坏性全局策略或上线仅v4读取其旧数据。
- 不保留legacy worker/reader/双写/旧binding导入；事实冲突留证，不暗自放宽身份或授权。

## Result

### 本轮最终回填（2026-11-02 Asia/Shanghai）

- 当前结果：**PASS / RESOLVED**。原manifest自有资源/九表/Gov原项已清，原charge全退；最终共存UID和健康、旧入口搜索通过，临时额外工具已清，见 evidence/10/completion-live11/result.json、evidence/10/completion-live09/cleanup.json。
- 组件、外部double与真实现场范围分开；按用户要求本轮未重跑全量make verify，未实测GPU_WHOLE。具体证据及边界见[完整报告](../implementation-report.md)与[逐项验收](../acceptance-results.tsv)。

### 历史固定源码与此前checkpoint（保留原事实，当前状态以上文为准）

- Outcome: 原66旧历史清理、Res窗口和本轮失败attempt释放已实际完成；最终bootstrap pins追加收敛未执行，Resolution保持open。原新10 checkpoint仅为历史，当前入口为kubercloud@100.64.128.12/ani-kcn-full/cluster f39bf6e2…。
- Original cleanup/cutover: 原限定外部与双库历史精确删除、原占额0；Res官方0025/两UID回填/NULL0、原15writer全gone、67d正式切换/release及窗口工具清理均有原始收据。见 [原历史](../evidence/09/legacy-data-cleanup/native-exact-discard-actual-result.json)、[Res完成](../evidence/09/res-recovery-v3-final-window-actual.json)、[工具清理](../evidence/09/res-window-finished-tools-cleanup.json)。
- Attempts: A1/A2按用户无备份直接清理授权精确手动退役，非正常CLOSED PASS；B1/B2/B3/B4均由原owner真实关闭/清理APPLIED/原释放event ACK，分别原24570全退，账户恒等式0。A3原7compute释放后正常owner APPLIED；日志/产物/合同保留PVC保留。见 [A1](../evidence/10/a1-exact-manual-retirement/exact-database-delete-actual.json)、[A2](../evidence/10/resumed-mainflow/a2-manual-cleanup/exact-database-delete-actual.json)、[B1](../evidence/10/b-gpu-original-release/original-b-normal-release-final-1793534000214437359.json)、[B2](../evidence/10/main-admission-abort-19240-release/b2-original-close-release-1793540950210389961.json)、[A3](../evidence/10/resumed-mainflow/a3-original-owner-compute-cleanup-final.json)。不SQL伪造CLOSED/APPLIED/ACK或账户清零。
- Runtime/materials: 主服务1dfd6941/fe944d52…，Podb98cfbef…start15:53:50Z/单Ready/0restart/原配置不变；最终bundle4a655已启用、新A/B显式Ensure/Get READY。native4d6013/VAP10/MWC3已按用户平台trust授权安装，0RBAC/新身份。见 [当前部署](../evidence/10/main-verify-time-1dfd6941-19240-release/rollout-actual.json)、[新A/B](../evidence/10/final-material-maintenance/new-A-B-Ensure-actual.json)、[最终native](../evidence/10/trusted-platform-native-final/actual.json)。
- Remaining: 此前5.4重登记已完成；native10/MWC3后的追加5.4因用户主线优先未执行、0该维护写，当前5b99仍pin7明确漂移。§7租户冻结保留、平台准入例外依用户新授权记录；不静默改canonical或冒称pins已收敛。B3训练/export成功但verify Begin409，原owner自动7compute全gone/原24570全退；B4实际微调/发布/Model登记/正常退额完成，下载SHA待Root；保留标准Namespace/输入/其他owner/Inference。
- B3/current repair: 原execution a3b48752…真实train loss7.0513/export exit0；verify Begin409，Python verify未运行。原owner自动7compute全gone/原event ACK APPLIED/24570全退/account44version6 occupied0=Σ0，见[B3收据](../evidence/10/resumed-mainflow/b3-original-close-and-release-final-1793547050096007731.json)（SHA256 2307f1a605c62d7e90dd0749c7023581c827dd4358da0d57cb5c60e18e152af4）。1dfd6941仅修runtimeproof/finetuning_steps.go中exportReportedFinishedAt纳秒与Pod main.finishedAt秒精度比较，见[实际Ready](../evidence/10/main-verify-time-1dfd6941-19240-release/rollout-actual.json)；原Pod native秒值未保留，不能宣称已完全证实唯一根因。B4已在修复后通过verify及全部8步骤；这不补造原B3 Pod已缺失的秒值。
- B4 actual: execution59f92b91-5e86-4dd1-90ec-cb1037756758已SUCCEEDED/PUBLISHED/CLOSED，8steps SUCCEEDED/19产物PUBLISHED/loss7.05126428604126，Model454cef60-ca01-4dae-a4e6-b8881026c688/version6afb1f32-a0d4-4c92-8d82-7799e450f5ee登记READY。见[正常读回](../evidence/10/resumed-mainflow/readback-B-1793548972071555495.json)、[原生观察](../evidence/10/b4-real-finetuning/current.json)。原charge9249ff3e…24570全退/account44version8 occupied0=Σ0、原event ACK APPLIED/7compute gone，见[最终释放](../evidence/10/resumed-mainflow/b4-original-close-and-release-final-1793549008812487147.json)（SHA256 f3dbe26c3623f4a308e82833c98ef97e890ea9fa3d50beb57a2cfe73cedb8d4a）。下载SHA/Model正式GET及A→B artifact否定收据待Root/snapshot，LIVE06未提前PASS；原pins漂移/追加5.4未执行及剩余现场用例不被本次成功覆盖。

## Comments

以下为按时间保留的历史记录；当前状态以本文件 Result 为准。

- 初始任务，尚无实施或验收完成声明；后续按时间追加事实与证据。
- 达到本项验收后再更新 Resolution 和 Result；后续现场证据不得提前标记通过。

第二轮当前状态复核：2026-10-31T20:23:19Z至20:23:22Z，完整identity一致；三原execution仍OPEN且close reason/原Pod与containerID、115bytes日志hash均未变；三个原Workflow仍NotFound。无新可推进原关闭证据。证据evidence/09/open-current-recheck-20261031T202319Z.json；未重跑全量inventory/OpenSearch，未进行持久写。

第二轮新原证据：官方v1 GetRun detail精确返回3原Run的6份manifest，原WorkflowUID/name/Namespace匹配，35/35/25节点终态，但无直接PodUID或原日志archive URI。第三条原CloseAuthority已存在、4原Pod退出已核；原live UID/RV读取、image_contract Error资格及完整writer/PVC/模板证明仍不满足，单纯authority重放no-op。详evidence/09/kfp-v1-original-run-detail-20261031T204206Z.json及third-archive-close-assessment.md。未做CAS/重建/清理；与此前‘未建立归档来源’不同，当前具体断点是原关闭不支持该archive证据及额外资格缺口。

用户最新明确答复：无其他原日志/Workflow归档，并批准报告§7限定一次性退休修正，见[evidence/09/retirement-amendment-approval-20261101.json](../evidence/09/retirement-amendment-approval-20261101.json)。上述日志/归档不满足原协议的内容保留为历史发现；现由独立旧base维护工具如实记录两条不可恢复日志与原UID archive，未解除其他writer/exit/fence/CAS/cleanup/原释放资格。工具定向验证已运行，现场只读plan和apply仍NOT_RUN。

当前现场具体阻塞是指定SSH入口连接关闭/单文件传输超时，及本地SOCKS对指定更新入口仍RemoteDisconnected；原job只查询、不重复POST。证据见[SSH](../evidence/09/entry-connectivity-20261101.json)、[SOCKS](../evidence/09/workbench-local-socks-connectivity-20261101.json)。原GPU事件独立维护决策见[原事件评估](../evidence/09/original-gpu-event-assessment-20261101.json)，尚无实际新ACK。部署后[维护门禁探针](../evidence/09/gov-maintenance-live-acceptance.py)只准备未执行，不作为PASS。

原更新任务恢复查询确定verify FAIL（UTC22:04:07），未进build/push/deploy，精确失败为timeutil日期fixture受host DST时区影响；见evidence/09/workbench-maintenance-verify-failure-20261101.json。修复仍保留原run_verify=true和原失败记录，不重复原job。

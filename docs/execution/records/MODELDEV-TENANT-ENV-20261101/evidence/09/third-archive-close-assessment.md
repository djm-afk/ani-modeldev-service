# 第三条原执行的归档关闭评估

结论：已有真实原 Workflow 归档和四个原 Pod 退出元数据，但**不能通过现有入口直接关闭**。单独恢复 CloseAuthority 不会推进；现有资格仍有两个明确不满足条件。没有执行状态写入、CAS、对象重建、S3读取或退款操作。

本次实读回执：`third-archive-close-assessment-20261031T204646Z.json`，UTC 2026-10-31 20:46:46–20:46:49，SSH exit0，完整集群身份与原 Namespace UID匹配。仅第三条原 Run detail和四个已知原 Workflow Pod读取；没有读取另两条训练日志。

## 已有原事实

- 原 execution `eca4d47b-5911-4f0f-8b45-89c1be775925`，Run `5f04cda7-4c66-4f16-a552-053ebd4e5b6f`，Workflow `md-kpwkw` / UID `d2acb3df-3979-4d60-aed5-16db0ce440fb`。当前PG：close_generation=1，owner_revision=6，DEADLINE，closed_at=null，review=KFP_CREATE_UNRESOLVED；唯一原 confirmed Run，normal run_authorities=0。
- 当前facts已有完全相同的原 CloseAuthority；Workspace=null、Training不存在、CloseEvidence不存在。原 attempt/plan/spec/operation等关联完整保存在回执。
- 原 v1 workflow_manifest SHA256 `c88268fdbb84fade76d2c488e5211fd697f6f852feb6c0a75b21ef67842142fb`，110484 bytes，与前次读回一致。metadata原UID/name/Namespace匹配，历史RV=18632368，删除时间空，completed标签=true，唯一Completed=True条件，原phase=Error，finishedAt=2026-10-30T09:21:45Z。25节点均终态，没有compressed/offload节点。
- 四个已知原 Pod当前仍为原UID，单一controller精确指向原 Workflow UID，SA=modeldev-step，node-id关联原归档节点；phase=Succeeded，init/main/wait报告的容器均已终止，exitCode=0。此精确四对象读取不是完整当前 Namespace writer/PVC扫描。
- 两个Error executor节点 `md-kpwkw-2678142235`（workspace-name）及 `md-kpwkw-1084837950`（finalize-close）无过程/outputs/exit字段，progress=0/1、taskResultSynced=true、retried=true、retry-max-count=0；原Retry父节点均唯一child、同boundary及同起止时间。二者message SHA256均为 `7940bb854efac90ea6594030a8690356f0cf8ce068b27f61adda4d2116fa5cb3`。
- 在本地对旧正式webhook的有限固定denial原因生成规范错误文案并计算SHA，二者精确匹配 `image_contract` 拒绝。两个Retry父message哈希也精确匹配相应原task名称包裹此拒绝文案。这是有限分类，不保存原manifest/错误正文/环境变量/秘密。
- 原 live Workflow当前仍为NotFound；归档有内容不能改变该实读结果。

## 旧21bf4bd关闭协议的具体限制

以下源码均固定 `21bf4bd6540d462a61c91b629f26250899583223`：

1. `internal/data/runtimeproof/closing_run.go:122–124` 的 VerifyClosingRun最后强制GET原live Workflow并核对原UID，404返回RuntimeNotReady；此前仍要求原frozen dispatch/plan摘要、独立KFP GetManagedRun及当前Pod controller关联。
2. `internal/data/lifecycle/owner_recovery.go:62–108` 的 RecordClosingRun在原identity锁内核对完整candidate、原dispatch和confirmed Runs；91–92行在已有唯一confirmed Run+CloseAuthority时直接no-op，105–106行明确恢复owner不能消除缺失writer证据。不会插入正常run authority或清review。
3. `internal/data/runtimeproof/owner_close.go:62–69,240–242` 的 VerifyOwnerWritersAbsent要求两次原live Workflow读取并验证同RV。历史归档RV不能伪装成这两次live观察。
4. 同文件369–395行的 serverDeniedAdmissionNode只接受指定服务端超时，或executor的固定 `work_environment` policy拒绝；**不接受image_contract**。因此两Error executor不能依现有规则被排除为未创建的Pod，当前也没有它们的实际原Pod退出证据。25个终态节点和4个成功Pod不能覆盖此缺口。
5. `internal/data/lifecycle/owner_recovery.go:164–175,180–207`的原fence和owner变更始终持同一identity行锁，复用已有generation，只有实际变更才推进revision；`internal/data/lifecycle/repository.go:588–637`的 ConfirmRuntimeClosed仍要求原generation、合法close证据、原唯一Run及创建收敛，633–636行才保存close证据/清review/设置closed_at。StepExitCleanup、ComputeCleanup和释放资格仍须按原顺序继续，不能从本次元数据直接推导CLOSED或可退款。

现成service/step close-only入口没有archive读取或只读dry-run模式；step还要求当前原workload身份。现有测试中的core-flow close-only并非只读维护入口，会实际投递Stop/推进关闭。故本轮没有可安全直接运行的归档关闭命令；repository dry-run为NOT_RUN，而不是PASS。

## 最小可实施下一步及必须先核定的证明

下一步可实施的是**固定旧base的独立一次性、单execution只读dry-run工具**，不是在新v4 runtime增加v3兼容。它首先输出计划和全部拒绝条件，不作数据库写；任何后续apply应按spec§13.1只接受固定清单摘要、完整原身份及精确对象集合。

工具必须保留原生产Freeze/GetManagedRun/owner图、fence与repository约束，补齐尚未做的当前业务证明：原admission/plan摘要和Run全部绑定校验；原预期PVC精确读取与createpvc未物化核验；按原校验范围完整扫描当前owner/PVC写者；两Error节点的原模板及retry=0完整证明；以真实原Pod UID和容器退出构造并持久化原StepExitCleanup证据。不能用本次四个已知Pod读取宣称完整writer证明通过。

在工具能具备apply前，root必须核定两项有证据的维护范围最小修正，否则继续BLOCKED：

- 是否允许以同一原认证KFP Run保存的终态原Workflow归档（原UID+名称+Namespace+完整节点+完成标志+固定SHA），结合当前原Workflow不存在及完整原写者实读，作为**明确标记来源的归档证明**；不能把它包装成live GET返回或APIObjectPresent=true。当前旧协议强制live读取，这不是现有许可。
- 是否允许仅在此原执行的一次性维护判定中，把精确原webhook `image_contract` 服务端拒绝与原零重试、无进程字段、完整父子关系合并证明为创建前拒绝。现有业务只接受另一固定原因；不能把一般Error、仅名字匹配、404或任意admission消息放宽为退出。

受影响条款：spec§9保留原owner/UID/exit/fence和日志语义；§13.3.2–5要求原关闭、创建收敛及先持久退出证据；§13.3.8要求未解决writer/cleanup继续报告阻塞；§13.1要求维护工具先只输出清单/计划、apply接受固定摘要。此评估没有批准上述替代，也没有改变原退款、日志、训练或Inference资格。

本轮已结束外部调查，不实现新维护工具/兼容或资格扩展。以上下一步仅是需要root先核定合同的有界可能修正，当前仍BLOCKED。另两条 `TRAINING_LOGS_UNAVAILABLE` 的原日志缺失是独立阻断，不因第三条原归档取回而解除。

# 05 多租户派发、关闭与已 seal 释放投递

Status: ready-for-agent
Resolution: resolved
Blocked by: None

## Goal

扩大原dispatch/close发现范围，使全部已接入租户被公平处理，同时保留唯一发送和原释放权威。
依据：[spec.md](../spec.md) §8–9、§13；本项只改现有循环和相关持久投影，不另建调度平台。

## Scope

- 仅 ani-modeldev-service；internal/biz/dispatch_worker.go、pipeline_submission.go。
- internal/data/submission/pending.go、queries.sql、repository.go及execution投影migration/admission写入。
- internal/biz/close_worker.go、gpu_release.go；internal/data/lifecycle/pending_close.go、queries.sql、gpu_release.go。
- 原Submit/Reserve、identity fence、lifecycle/cleanup与GPUReleaseReconciler必要接线及定向回归。
- 原runtime冻结身份读取/日志/Stop/cleanup调用点；不改Gov占额协议或ACC调度算法。

## Implementation

1. 新增executions.cluster_id/environment_binding_id/deadline_at非空投影与固定复合索引，由写快照同一事务填写。
2. canonical snapshot仍为权威，逐项比较投影；冲突隔离本执行，不自动修复或转用当前配置。
3. 实现ListPendingDispatchTenants/ListPendingAdmissions，外内层复用spec的identity、deadline、无dispatch eligibility谓词。
4. 实现两级keyset：tenant外游标，(accepted_at,execution_id)内游标与数据库sweep_before；各类失败也推进，末尾才重置。
5. 保留两条loop；固定1秒tick、16tenant×4候选≤64、全局并发≤4、同tenant同时1项、无timer backlog。
6. SQL≤3秒、单dispatch/close≤30秒、已seal outbox≤10秒；loop goroutine统一更新有界游标。
7. 每候选解码比较→当前READY原关联→live Namespace/SA UID→冻结DispatchOwner→原Submit/Reserve。
8. 首次Reserve事务按环境行→execution identity锁顺序重验READY/deadline/close fence；已有dispatch先返回原幂等事实。
9. 有任何SUBMITTING/NOT_SENT/UNCERTAIN/CONFIRMED行都不再发现发送；保留SendPermit、commit未知禁发送和有限写回路径。
10. 关闭发现按contracts/close-discovery.md采用专用安全JSON helper、同一coarse predicate和逐行严格Reasons；NONE/坏行也返回examined tuple并推进；保留原所有期限/cleanup/GPU分支，不JOIN当前READY。
11. 已seal outbox仅ACK和DeliveredAt都空时独立投递；都存在为完成，仅一项存在隔离为冲突；Namespace缺失不能挡合法投递；未seal仍需原资格，scope无outbox报冲突。
12. 真实PG验证坏UTF8/JSON/timestamp/数组形状不会炸掉分页，隔离坏行/单tenant依赖错误；DB故障按1/2/4/8/16/30秒退避恢复，不调用永久shutdown；保留writer检查及原cleanup最多3次。

## Acceptance

- DP01：33个逻辑环境越过旧32/64静态上限；坏首行、非READY和持续新增都不能饿死旧候选。
- DP02：真实事务多worker竞争只有一次Reserve/POST；commit未知、not-sent、uncertain、重启不重发。
- DP03：close或非READY先获锁拒绝首次Reserve；已提交permit最多一次在途send，Owner来自快照。
- CL01：关闭候选包含原全部分支，环境不可用仍发现；Namespace消失时原sealed事件仍得到Gov ACK。
- CL02：NoDispatch/runtimeNeverMaterialized和原资格回归；scope-without-outbox仍冲突，未新增ACC ACK前置条件。
- CL03：未贴MD标签的同PVC writer阻止误删；同Namespace无关VM/PVC和同名新UID对象保持。

## Constraints

- 不加tenant worker/队列、执行lease或新发送状态机，不将发现结果当发送许可。
- 不重试已有Run发送，不按超时/Get404/物理GPU空闲退款，不改四字段release事件。
- 不要求原执行的读取/日志/Stop/close等当前环境READY，也不据新binding接管旧对象。
- 不删除或绕过原exit evidence、finalizer、日志留存、creation fence与cleanup retry上限。
- 不在本项运行旧数据清理或部署新worker；现场排空与零可恢复旧项核验归09。

## Result

### 本轮最终回填（2026-11-02 Asia/Shanghai）

- 当前结果：**PASS / RESOLVED**。原创建/关闭fence、自动计算清理及原GPU事件/ACK保留，B5原24570全退/账户恒等式成立；见 evidence/10/completion-multitenant-regression/b5-original-close-and-release-actual.json。
- 组件、外部double与真实现场范围分开；按用户要求本轮未重跑全量make verify，未实测GPU_WHOLE。具体证据及边界见[完整报告](../implementation-report.md)与[逐项验收](../acceptance-results.tsv)。

### 历史固定源码与此前checkpoint（保留原事实，当前状态以上文为准）

- Outcome: PASS（源码与真实PG/必要外部边界集成）；指定集群清理/切换/真实A-B验收仍由09/10报告，未记LIVE通过。
- Base / final commits: base `21bf4bd6540d462a61c91b629f26250899583223`；最终源码提交 `d807cbdbd5dd6664d61480acedc752e19cfca48a`，已SSH推送；最终完整make verify exit0见evidence/modeldev-final-make-verify.log。
- Changed files / generated artifacts: 原dispatch/close两个worker、共享WorkerCoordinator、submission/execution/lifecycle投影与sqlc、0003 migration、安全JSON helper、runtime实例接线、原UID关闭路径；无新队列/执行lease/发送状态机。
- Validation commands / exit codes / required gates: Fedora `go test -count=1 ./internal/data/submittest` exit0；`./internal/data/lifecycle`整包exit0；`./internal/data/submission`整包exit0；33环境/坏行跨页/已seal独立投递、1/2/4秒DB故障恢复均通过；coordinator race测试exit0；完整源码make verify收敛由统一gate记录报告，首次失败及修复日志保留。
- Acceptance IDs / actual values / evidence paths: DP01：33个真实Ensure READY登记，32转BLOCKED、目标前4坏行，第5原候选在持续新增中实际进入唯一Reserve/HTTP；DP02：原COMMIT失败零POST、并发唯一permit/POST、unknown/not-sent/restart不重发；DP03：环境先持锁BLOCKED拒绝首次Reserve、已提交dispatch在环境锁/BLOCKED时仍只重放无permit；CL01/02：真实PG7行（6损坏+1有效）跨页、坏UTF8/JSON/数组/投影/运行时间隔离，seal后无环境登记仍原事件得Gov stub ACK且ACK/DeliveredAt同事务；CL03：原UID、未贴标签PVC writer、替换对象和3次cleanup上限原组回归通过；补测提交e406744验证真实PG/cleaner7原target APPLIED后，同Namespace独立VM/launcher/另一PVC原UID/内容/状态保持，外部K8s/KFP为替身，evidence/10-cl03-coexistence.log exit0。证据 `evidence/05-*`、`evidence/worker-coordinator-*`、`evidence/04/modeldev-cpu-mainflow-final-shared-budget.log`、`evidence/04/modeldev-compute-parent-budget-final.log`及统一make verify日志。
- Deployment or cleanup effects / preserved assets: 无线上清理/部署；仅任务独占PG随机schema/外部边界fixture。原Gov释放四字段和账目协议未改。
- Remaining blockers / affected dependencies: 09原3执行缺原日志/Workflow证据；新环境现场开通、原生策略安装后行为和A/B均未执行。
- Contract conflicts / evidence / minimal correction: 完整tag主链确认原5/10限流器在第154次HTTP时返回would-exceed-deadline，原计算父GET误归IDENTITY_CONFLICT。保留spec8.1的30秒，runtime三K8s client共享一个有限20/40限流器，并把未证实父不存在的API错误保留为BOUNDARY_UNCERTAIN，未增加删除证据/次数/权限；新组和原compute/absence/retry/UID/writer组exit0。修复后真实CPU48步→发布→自然CLOSED→7计算targets APPLIED→重连日志/独立checkpoint forward通过。原两loop各4槽也按全局4/同tenant1冻结要求共享一有限协调器；无队列/lease。

## Comments

- 初始任务，尚无实施或验收完成声明；后续按时间追加事实与证据。
- 达到本项验收后再更新 Resolution 和 Result；后续现场证据不得提前标记通过。

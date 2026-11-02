# 03 环境登记、Ensure/Get 与有限开通

Status: ready-for-agent
Resolution: resolved
Blocked by: None

## Goal

实现 ModelDev 持久环境登记和显式有限开通，使同一受信tenant+cluster总是恢复同一环境。
依据：[spec.md](../spec.md) §3–5、§6.1、§10；采用01固定API和02动态镜像合同，不引入后台开通器。

## Scope

- 仅 ani-modeldev-service；internal/biz/tenant_environment.go 与必要领域port。
- internal/data/tenantenvironment/：migration、queries、repository、专用连接锁、typed receipts、kubernetes.go、kfp.go。
- internal/service/tenant_environment.go、internal/server/governance_command.go、gRPC注册和精确verified methods。
- cmd/ani-modeldev-service/{runtime,runtime_operations,model_connection,app}.go、internal/conf/v1/{conf.proto,validate.go}。
- 只实现spec固定SA/RB/CM/专用artifact Secret及Res镜像引用适配；平台bootstrap模板由06/08负责。

## Implementation

1. 建立唯一表 modeldev_tenant_execution_environments；environment_id就是BindingID，unique(tenant_id,cluster_id)，四state与有限phase固定。
2. 短事务INSERT ON CONFLICT+GET先登记，丢弃竞争失败方随机ID；外部操作前按固定算法取得专用连接advisory lock并重新读行。
3. 推进预算25秒、单上游5秒；不跨外部调用持SQL事务；解锁用独立≤2秒context，状态不可信则丢弃连接。
4. 仅信Governance认证方法与当前tenant范围；Ensure仅operation_id，Get空请求；严格拒绝protobuf未知字段和非法枚举。
5. 按NAMESPACE→RESOURCES→EXPERIMENT→VERIFY→PUBLISH→COMPLETE推进；先读标准Namespace并固定原UID。
6. 固定对象先持久意图再GET，仅NotFound可Create；任何响应后读回精确owner/spec/原UID，未知结果不换名。
7. 调用Res获取单受管authority镜像引用；按固定SA规则配置refs；在首次写入/轮换专用artifact两键Secret前完成contracts/authorization-inventory.md的只读授权边审查；支持原UID值轮换。
8. Experiment固定md-env-<environment_id>与规范描述；持久原请求、处理完整分页，未知POST仅在唯一性已证实条件下有限同请求重试。
9. 平台control token每个有界请求重新读投影文件；未有binding时使用私有平台身份路径，不伪造空Run绑定。
10. 核对原对象、Experiment、ani-system四定义、精确bootstrap材料；不授Ensure任意SAR/impersonate或创建测试Run。
11. 先固定binding规范字节，创建immutable投影并读回，再CAS READY；不含投影自身UID/RV、Secret值/摘要或可变状态。
12. Get只返回持久观察且无行404；Busy返回真实已登记state；预算/失败保留receipts供显式Ensure恢复，不隐式重新绑定。

## Acceptance

- EN01：真实PostgreSQL并发不同operation仅一行/同一environment_id；锁忙不改写READY/BLOCKED/NEEDS_REVIEW。
- EN02：各Create响应丢失及进程中断后精确恢复原对象；foreign-owner/换UID拒绝；多次有界调用能继续receipts。
- EN03：完整分页零/一/多匹配、未知POST及未证实唯一性分支均符合spec，每次Ensure最多一次Experiment POST。
- EN04：seal→投影→readback→CAS顺序、摘要无自引用/未知字段拒绝；Get无登记404且不插入。
- NS02、API01：错误Namespace零子资源写；精确trusted方法/unknown fields/预算生效；token逐请求重读有受控测试；普通主体危险授权边、审查不完整均阻塞Secret写入。
- API02：Ensure/Get不创建Execution、acceptance或charge；真实A/B开通和artifact I/O留09/10，不伪称组件证据为LIVE通过。

## Constraints

- 不实现环境worker、lease/heartbeat、新队列、Delete/Rebind/UpdateProfile接口或自动补偿删除。
- 不CREATE/PATCH/Adopt Namespace，不替换已固定Namespace/SA UID、Experiment、scope或provision_spec。
- 不复制业务STS/管理员存储凭据作KFP artifact身份，不在DB/投影/日志/回包保存秘密或其摘要。
- 不上传Pipeline/Version、不创建租户Profile、不发用户token，不将平台固定cluster开放为请求字段。
- 不触碰旧环境在线部署或旧v3数据；迁移和切换由09按原关闭/释放顺序执行。

## Result

### 本轮最终回填（2026-11-02 Asia/Shanghai）

- 当前结果：**PASS / RESOLVED**。最终A/B显式Get404→Ensure/Get READY，同一实例；并发锁忙返回持久view已核实，无重复环境/Experiment，见 evidence/10/completion-live03/persisted-view-resolution.json。
- 组件、外部double与真实现场范围分开；按用户要求本轮未重跑全量make verify，未实测GPU_WHOLE。具体证据及边界见[完整报告](../implementation-report.md)与[逐项验收](../acceptance-results.tsv)。

### 历史固定源码与此前checkpoint（保留原事实，当前状态以上文为准）

- Outcome: 组件及当前native增量完整必需门禁 PASS；正式发布源码d423ff9b31b1097ae972e346353e36dc7f0289d8与被测1756文件一致。真实A/B开通仍BLOCKED/NOT_RUN；原d807 gate仅保留对应历史快照。
- 用户修正：原 Implementation/Acceptance 正文保留为附件原要求；其中全局授权图 scanner、128/162 引用预算及42 trusted 输入已由用户明确废止，当前统一使用既有 configured ServiceAccount + Kubernetes native RBAC/admission，不另造授权系统。原方向及保留约束见 [user-direction.json](../evidence/09/native-rbac-convergence/user-direction.json)。标准 Namespace/原UID、owner、bootstrap pins、幂等与真实API拒绝继续失败关闭。
- 已有实现/历史验证：显式 Ensure/Get、PG唯一登记和专用连接锁、typed receipts、25秒总预算/5秒上游、seal→投影→readback→CAS READY 已实现。原 d807cbdbd5dd6664d61480acedc752e19cfca48a 与后续预算测试6b0b952478081034baee8ab27fd8216b26742700的组件/进程恢复结果分别见 evidence/03-environment-component-tests.log、evidence/03-environment-process-recovery.log、evidence/03-environment-budget-recovery-record.json、evidence/03-environment-budget-recovery.log；预算测试已实际执行exit0，不再是NOT_RUN。
- 当前 native 增量定向验证：Fedora真实PG生产Ensure到READY及原API拒绝保持BLOCKED/不创建Experiment实际PASS；原SAUID、无全局inventory请求、退役配置拒绝、API认证/授权/admission拒绝后不继续副作用均通过。证据 evidence/09/native-sa-rbac-source/targeted-final-output.log，具体 TestEnvironmentPostgresVerticalEnsureSealPublishAndBusyRead、TestEnvironmentPostgresNativeDenialKeepsBlockedWithoutExperiment、TestEnvironmentNativeAPIDenialStopsFurtherRequests。旧“危险/不完整scanner审查”PASS仅作已废止合同的历史，不是当前实现证明。
- 最新完整门禁：首次默认并行 make verify exit2，失败包为 internal/data/input、internal/data/submission、internal/data/submittest；同源未改代码/超时的三包 -p=1 顺序执行exit0。两者分别见 evidence/09/native-sa-rbac-source/modeldev-native-rbac-fullverify-20261101T045658Z.result.json 与同前缀 .three-failed-packages-isolation.json。完整串包门禁已于05:18:06Z实际exit0（GOFLAGS=-p=1），含固定生成/稳定性、全部默认真实PG测试、vet/build/mod verify/diff check；发布d423ff9b31b1097ae972e346353e36dc7f0289d8的1756文件与被测源码逐路径一致、mismatch=[]。证据 evidence/09/native-sa-rbac-source/result.json、modeldev-native-rbac-fullverify-20261101T045658Z.sequential.result.json、modeldev-native-rbac-fullverify-20261101T045658Z.sequential.make-verify.log 及 evidence/09/final-switch/native-rbac-release-source-equivalence.json。首轮并行FAIL与三包顺序PASS保留为历史，不再是当前门禁终态。
- 现场已完成：原54聚合全部关闭、旧worker停止，限定18Pod/46PVC+PV/73KFP/295内部S3版本释放，Gov146+MD334行按固定PK/rowSHA的两个受控事务删除，原账户occupied=全owner剩余占额=0，保护项hash未变。用户明确丢弃历史后不补旧codec/显示状态或重放已退款事件，见 evidence/09/legacy-data-cleanup/history-discard-user-correction.json、native-exact-discard-actual-result.json。同目录external各阶段实际receipt保留；不再列“三执行OPEN”。
- 平台材料/native/webhook现场：四个ani-system定义/原Pipeline+Version和canonical材料已实发封存，临时publisher已清理，见 evidence/08/platform-materials-final/{actual-publication-window.json,seal-receipt.json,publisher-temporary-input-cleanup.json}。用户native方向的4项RBAC精确收窄已执行、其余17对象UID/公开内容未变；当前19对象模板和更新11个pre-MWC生产Go摘要见 evidence/09/native-rbac-convergence/{actual-result.json,bootstrap.json,actual-eleven-bootstrap-refs.json}。全局webhook public alias同digest、2Ready/TLS及实际MWC读回GREEN见 evidence/09/final-switch/webhook-final/global-webhook-final-actual.json。
- Res窗口实际：官方0025迁移、精确A/B两UID回填、NULL0、原owner/ACL保持已完成；仅ModelDev caller退役3字段及 network.namespace_prefix 已精确移除，Inference保持。见 evidence/09/res-window-through-old-writer-stop.json、res-source-recovery-readonly-gate.json、res-recovery-v2-retired-prefix-config-correction.json。新配置SHA d3fd79712ed28807d6d132f47dc652bb2a140c108ad489c636e57214eb10e429；Res最终切换与窗口释放已于05:17:37Z实际完成：正常Workbench Job34eed91756034329aa34031034f1070a成功，source67dceef03f01215c294a55f11464e3e3a52cb783，新digest67b0751b46c9c8f1d888b0910b12498ea3ab9f7ffd7fea9311d9dcb3d1ecfad2；最终Pod23f92d36-30ca-4047-a6aa-c35ff035eb88 Ready/restarts0、无调度门控。schema25/两UID/NULL0，原15后端(pid,start)全gone/原IP连接0，官方release Applied=true/临时constraint0。证据 evidence/09/res-recovery-v3-final-window-actual.json。 Resource窗口工具已完成精确清理，node私有根和本地transit不存在、Fedora3bin不存在，仅保留公开plans/journals/build日志；原Secret/共享工具链/cache/已交付镜像/soleCM保持，见 evidence/09/res-window-finished-tools-cleanup.json。
- Remaining: ModelDev/Gov最终运行源切换、A/B真实Ensure/Get、实际artifact I/O、token轮换与业务隔离验收未完成；Res最终运行源切换/释放及native完整gate/正式源码交付已实际完成。基础设施安装/组件READY均不替代LIVE通过。
- 合同事实修正保留：KFP原Struct不保留编译原bytes，仍先验证bundle四个原IR文件SHA，再与GetVersion完整结构等值比较，固定 ir-前16版本名不改hash语义；原证据见 evidence/08/platform-materials-final/pinned-kfp-source.json。
- Evidence provenance: 本Result只同步本次实际evidence；没有新增测试、现场写或PASS推断。

## Comments

- 初始任务，尚无实施或验收完成声明；后续按时间追加事实与证据。
- 达到本项验收后再更新 Resolution 和 Result；后续现场证据不得提前标记通过。

第二轮补证：Fedora恢复后，原预算恢复草稿在已核对的最终源码mirror唯一一次定向执行exit0，25.555s；真实固定25秒耗尽后持久7receipt（6confirmed/1pending）、锁释放，下次显式Ensure同environment/Namespace/对象UID到sealed READY，Experiment POST=1。仅test提交6b0b952478081034baee8ab27fd8216b26742700，生产/API未改。证据evidence/03-environment-budget-recovery-record.json和03-environment-budget-recovery.log。原NOT_RUN为上一轮历史，当前已解除。

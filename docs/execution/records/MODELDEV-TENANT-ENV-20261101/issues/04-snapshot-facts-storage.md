# 04 v4 快照、动态事实、存储与四参数 IR

Status: ready-for-agent
Resolution: resolved
Blocked by: None

## Goal

使新受理从持久环境和平台材料形成严格v4快照，运行材料与存储scope全部绑定原租户和执行。
依据：[spec.md](../spec.md) §6–7、§9；公共字段按01冻结，03提供环境查询，本项不给业务入口隐式开通。

## Scope

- 仅 ani-modeldev-service；contract/cpup01/snapshot.go、snapshot_parts.go、canonical/hash/codec/plan/conformance及相关source proto。
- internal/data/admissionfacts/reader.go；internal/biz/admission_resolution.go、managed_materials.go；internal/service/finetuning_readiness.go。
- internal/data/storagecredentials/issuer.go、finetuningmodel/live.go；cmd/ani-modeldev-service/model_connection.go与runtime copier。
- pipelines/cpu_p01.py及固定编译入口；internal/data/kfp/client.go、managed_run.go、runtimeproof/root_admission_close.go。
- internal/biz/runtime_plan.go、managed_runtime.go、internal/data/trainer/pod_contract.go、stepidentity/verifier.go及既有imageruntime client。

## Implementation

1. 只保留 ani.modeldev.execution-spec.v4 reader；删除v3 codec/fixture，字段号及canonical顺序遵从01合同。
2. 增加DispatchOwner、三个固定SA UID标量和排序去重的ManagedImageAccess.PullBindings，保持Environment可比较。
3. canonical前严格检查tenant非零规范UUID和NamespaceName精确tenant-UUID；删除隐式lowercase接受路径。
4. 动态AdmissionFactsReader从可信tenant+配置cluster读READY环境并核验live原Namespace/SA身份，业务调用不Ensure。
5. GPU ComputeSelection cluster必须与配置一致；CPU由ModelDev固定cluster，Gov不补造cluster；错误在AcceptFrozen前返回。
6. 实现输入、execution发布、基模、独立KFP artifacts的固定scope；ApprovedPrefix无尾斜杠，子对象比较用prefix加斜杠。
7. STS去掉启动Bindings数组，使用原InputImport/Execution身份与用途精确对象/VersionID；保持900秒、原期限及关闭后禁止新签发。
8. Readiness注入只读AdmissionMaterialReader，按固定implementation/device读平台材料，不调用依赖环境facts的ManagedMaterials.Implementation。
9. 基模scope由可信tenant与平台bucket纯算法生成；Readiness和Prepare复用，Prepare再冻结；copier目标bucket来自平台配置。
10. 四份IR root只允许execution_id、spec_hash、environment_binding_id、namespace_uid；RuntimeConfig.pipeline_root及SA来自快照。
11. generic training继续单一非空TrainingImage.PullSecretRef；fine显式使用managed refs；trainer SA保持空refs，step/verifier精确refs。
12. 形成同一不可变catalogue的编译输入/摘要，供06准入与08发布；ListImplementations/TimingFor仍按既有动态envfacts前置条件。

## Acceptance

- SNP01：DispatchOwner、三个SA UID和managed refs各自变化均改变spec_hash；v3/未知字段/坏投影拒绝。
- NS01、NS02：v4拒绝非canonical身份，不靠lowercase修复；原Namespace/SA UID变化不能冻结新受理。
- ST01：输入/基模精确对象与VersionID、原execution发布scope、关闭后禁签、A不得读写B；保留原用途与期限。
- ST02：合法tenant尚无环境仍可source-only Readiness；B基模prefix正确、copier使用平台bucket。
- IM01：general exact singleton与fine managed refs均进入RuntimePlan，合法继承保留，额外或前缀伪装ref拒绝。
- API02、AD02：环境失败在占额前；四root参数及严格runtime-config解析有合同证据，真实运行由10完成。

## Constraints

- 不保留v3双reader/默认tenant/backfill，不导入旧binding，不为缺环境生成虚假facts或自动Ensure。
- 不扩大STS为List/Delete/跨tenant范围，不将共享artifact AK描述为tenant级S3 IAM隔离。
- 不添加generic public-image空PullSecret分支，不恢复ani-registry-pull前缀放行或任意SA继承。
- 不开放用户IR、任意Pipeline、namespace/cluster/experiment覆盖；不创建Kubeflow Profile CR。
- 本项只产出新合同/材料，旧在线定义、部署与数据库切换必须等待09。

## Result

### 本轮最终回填（2026-11-02 Asia/Shanghai）

- 当前结果：**PASS / RESOLVED**。动态环境从持久登记进入原业务snapshot，当前A4 CPU/B5 GPU完整执行和原owner关闭成功；见 evidence/10/completion-multitenant-regression/result.json。
- 组件、外部double与真实现场范围分开；按用户要求本轮未重跑全量make verify，未实测GPU_WHOLE。具体证据及边界见[完整报告](../implementation-report.md)与[逐项验收](../acceptance-results.tsv)。

### 历史固定源码与此前checkpoint（保留原事实，当前状态以上文为准）

- Outcome: PASS（04源码及必要关联行为证据；此处不代表09切换或10真实A/B验收PASS）。
- Base / final commits: base 21bf4bd6540d462a61c91b629f26250899583223；最终源码提交 `d807cbdbd5dd6664d61480acedc752e19cfca48a` 已SSH推送；最终完整make verify exit0见evidence/modeldev-final-make-verify.log。
- Changed files / generated artifacts: cpup01 单一 v4 Snapshot/codec、DispatchOwner/ManagedImageAccess/三 SA UID、严格 tenant/Namespace/storage scopes；删除 snapshot-v3 fixture，独立 Python 生成 snapshot-v4.json/vectors；AdmissionFactsReader 从 READY 登记及同一四材料目录只读合成并 live 验证原身份；Prepare/Finalize 冻结和重新核对原环境/Owner；STS 删除 tenant Bindings 数组，仅平台物理 store+原执行scope；source-only Readiness 使用 PlatformImplementation；基模 scope 按可信 tenant 派生；四 root IR/KFP/关闭 root config 严格解析；general/fine 显式精确 refs，删除 training 名字前缀放行。
- Validation commands / exit codes / required gates: 仅在 ssh fedora:/home/chabking/tenantenv-20261101/modeldev-snapshot 执行；GOTOOLCHAIN=auto Go 1.26.10。组件目标 go test（contract/cpup01/...、admissionfacts、kfp、storagecredentials、finetuningmodel、stepidentity）exit 0；真实 Ensure+PGRepo→READY→动态 Reader→真实 Prepare→Finalize 和同一环境支持MLP/fine两组固定路径、source-only Readiness 定向测试 exit 0；固定 KFP/kfp-kubernetes/kfp-pipeline-spec 2.16.0 venv 下 pipelines 5 编译行为测试 exit 0。make verify 由主 agent 在最终集成后统一执行，当前未宣称通过。
- Acceptance IDs / actual values / evidence paths: SNP01 v4/未知字段拒绝和 Owner/三SA UID/managed refs 改变 hash、ST01 原对象/VersionID/原execution发布prefix/关闭禁签、ST02 未开环境 readiness 与 B tenant 基模 scope、IM01 精确 refs、AD02 四 root 编译及 KFP Create 前原 Namespace 替换/错属/Terminating 零 token/零 POST，已有组件证据：evidence/04/component-final.log、readiness-final.log、pipelines.log。完整跨组件/真实验收仍由 06/09/10回填；不将这些日志视为 LIVE PASS。
- Deployment or cleanup effects / preserved assets: 本项未部署、未调用集群写、未清旧数据；保持 09 规定顺序。
- Remaining blockers / affected dependencies: 04无未解代码阻塞。03已提供真实Ensure和完整12对象sealed binding；05 sqlc/worker接线后，真实PG trainer 422原拒绝闭环、runtimeproof三处原关闭/清理fixture、cmd实际mTLS/自动worker链及tagged close-only行为均已必要回归PASS。主 agent负责最终整仓gate；09/10现场切换、原占额释放与真实A/B验收仍NOT_RUN，不由本项结果替代。
- Contract conflicts / evidence / minimal correction: 发现共享binding只有一组InputFilePath/OutputDirectoryPath，而既有业务有限合同要求MLP=/inputs/data.csv,/outputs、fine=/workspace/inputs/data/finetune_data.jsonl,/workspace/training；若直接沿binding路径，单共享环境只能Prepare其中一种。受影响spec6.5“按tenant+implementation由登记+平台材料+明确公共配置合成facts”。最小修正：dynamic reader依两个固定implementation提供既有固定路径，保持一个READY环境，不扩API、不改binding身份、不按kind开环境。`evidence/04/modeldev-facts-real-ensure.log`显示同真实Ensure+PG READY环境读取两种paths以及Prepare/Finalize均PASS。Kratos v3.0.0原config Scan的protojson DiscardUnknown:true（reader.go169/value.go174）会静默丢旧配置字段；loader现在保留Kratos解析合并后Scan为RawMessage，再strict protojson解当前Bootstrap，拒绝binding_file/binding_sha256/facts_files/base approved_prefix及camelCase旧key，安全有限错误，必要实际file-loader测试PASS（`modeldev-strict-config-scan.log`），生产进程加载配置并SIGTERM退出复测PASS（`modeldev-strict-process.log`）。旧 trainer 测试原本默认 ani-registry-pull 继承和旧 SA 名，已改为 spec7.5固定 SA + 显式精确 refs；未保留兼容入口。首次 Fedora 系统 Go1.26.7 低于模块1.26.10，使用远端官方自动 toolchain 已解除；没有本地构建或 Fedora 打包传输。

- 关联真实业务证据：`evidence/04/modeldev-cmd-dynamic-environment.log`（cmd/conf 定向 exit0：真实Ensure→READY→Accept→自动worker→KFP确认；Prepare/Finalize mTLS；坏材料启动不fallback和池释放）；`modeldev-runtimeproof-ensure.log`（真实PG三fixture及finalizer/launcher/原对象清理/日志保留exit0）；`modeldev-cmd-close-dynamic.log`（tagged CloseOnly原无dispatch、uncertain不重发、identity/source drift三项exit0）；`modeldev-facts-real-ensure.log`（真实Ensure支撑的动态facts全部4项exit0）。lifecycle RuntimeV21/shared publication expiry fixture也已真实Ensure并冻结同owner，必要回归exit0（`modeldev-lifecycle-v21-ensure.log`）。跨测试schema共享advisory锁合法busy响应由测试helper在25秒内显式Ensure重入处理；未改生产锁或跳过READY。所有替身仅K8s/KFP/Res等外边界；未用SQL插入READY。共享helper位于internal/testsupport/tenantenvironment，Admission wrapper返回按实际binding重绑后原owner/Runtime/hash，原执行Record/Reserve/close使用真实restricted PG。
- 集成 gate 暴露的旧共享 fixture 已迁移 v4：component/workspace/step 三整包远端必要回归 exit0（15.462s / 4.425s / 32.511s，`evidence/04/modeldev-v4-shared-fixtures.log`）；RuntimePlan general/fine 都必须带 frozen exact managed refs，定向 exit0（`modeldev-runtime-plan-v4.log`）。cpu_mainflow buildtag 旧静态事实/STS Bindings/webhook Bindings 已删除，真实 Ensure+restricted PG fixture 编译 exit0（`modeldev-cpu-mainflow-compile-after.log`）；execution authority 与关闭禁签 STS 存量测试 PASS 17.87s（`modeldev-cpu-mainflow-required.log`）。同文件首轮真实 CPU 路径在旧 harness 缺 Resource pull Secret port 时未启动训练，明确不是训练验收；补 port 后实际 CPU MLP 48 optimizer steps、唯一 TrainJob、实际文件/manifest/tar 上传、远端 bytes 校验后 PUBLISHED 均已执行（`modeldev-cpu-mainflow-required-after-pull.log`），旧边界现已补齐原 CreatePVC 请求/driver/PVC创建时间和原prepare-controller输入，坏输入按真实权限/PG/原UID关闭 PASS（`modeldev-cpu-mainflow-rejected-final.log`，52.59s）。完整成功主链原QPS5/Burst10在关闭清理第154个HTTP调用遇到 `client rate limiter Wait returned an error: rate: Wait(n=1) would exceed context deadline`，被旧父链读取分类成IDENTITY_CONFLICT（`modeldev-cpu-mainflow-parent-get-error.log`）；spec8.1固定单执行调和≤30s，未扩大预算。与主agent拟议生产接线一致，tag所有dynamic/core clients改为共享同一个有限20/40 token-bucket pointer后，完整成功主链 PASS 44.65s、坏输入 PASS 13.00s，同run exit0（`modeldev-cpu-mainflow-final-shared-budget.log`）：Ensure→实际CPU48步→PUBLISHED→原自然CLOSED/7个计算target APPLIED→7个component/driver Pod清理→重连mTLS原训练日志→独立下载checkpoint weights-only重载，forward shape=[4,2]/cpu；该次完整close实际245 HTTP调用。PASS日志旧文字“6 original steps”为旧输出文案，源码assert为7并已过，随后仅将文案改为7。主agent已将生产runtime_clients的workload/environment/core三client落盘为同一个有限20/40 limiter pointer，30s/并发4上限不变。父链GET依赖故障仅返回ErrCleanupUncertain，仍存在parent/身份不等仍Conflict；caller保留原错误，不以限流失败伪造身份冲突。生产接线及最小错误分类的必要关联回归已PASS：cmd runtime dispatch/mTLS、坏材料、STS轮换/拒临时凭据、streaming预算组exit0 4.437s（`modeldev-runtime-clients-budget-final.log`）；真实rate limiter would-exceed故障仅GET且Uncertain非Conflict，加原compute cleanup/absence/retry/UID/writer完整相关组exit0 108.464s（`modeldev-compute-parent-budget-final.log`）。首轮因独占远端副本尚未同步主agent已修的pending_close CompletedAt nil guard而panic，保留`modeldev-compute-parent-budget-stale-dependency.log`；同步现有修复后同组通过，不代表当前源码仍有该故障。未重复CPU训练。此为 Fedora 真实业务进程+外边界 doubles，09/10现场A/B仍须单独验收。

## Comments

- 初始任务，尚无实施或验收完成声明；后续按时间追加事实与证据。
- 达到本项验收后再更新 Resolution 和 Result；后续现场证据不得提前标记通过。

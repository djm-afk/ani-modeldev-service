# CPU 受管微调：后续工作交接

交接基准：2026-10-15，`codex/finetuning-cpu-m3a`，HEAD `848d50bd5878454ea0d69096dcaa2778365460b9`。
本文交接 M3-A 之后的工作。上述 SHA 是原验收基线；后续合入 main 的代码验证与集群已运行材料分别记录，不将 Git 发布视为新增集成或部署验收。

## 1. 从已经完成的主链继续

用户最终应得到的流程是：

```text
用户经 BFF 选择数据、基模版本、目标与参数
→ Gov 鉴权、冻结 Release 并持久投递 → ModelDev 持久受理
→ KFP prepare → 唯一 TrainJob 真训练 → export / verify / collect
→ 发布并确认原产物 → Model 自动登记并核实 READY → 关闭计算
```

训练后的独立 Inference 验收另行执行，不给每次训练追加推理部署步骤。
当前 **CORE_FLOW / M3-A 已完成**：上述 ModelDev/数据库/KFP/Trainer/S3 执行与产物子链真实运行，Model 登记边界明确报依赖不可用后关闭。
A/B、卷外取回、运行中 Stop、应用重建、新进程恢复、deadline、退出留证及自动回收已有原始实际。
这没有证明真实 Gov/BFF/页面、Model MR、FT29，或 FT22 正常成功、A10 六项单次成功及 SCHEME_FINAL。

接手先看 [已交付证据索引](../.scratch/finetuning-cpu/records/M3-A-handoff-20261015.md) 与 [完整 Goal](../.scratch/finetuning-cpu/goal.md)。
唯一规范是 [spec.md](../.scratch/finetuning-cpu/spec.md)，SHA256 `60fabe6a1a40b9dc316f407e6b40cea2a1f8381749ac44fe2f14e35b5d03853e`。
后续改合同应修改该唯一规范并按既定流程同步派生材料，不另立可独立修改的第二方案。
`.scratch` 及其中 actual、原 ZIP/reference 有意未入 Git；Fedora 镜像、台账、私有配置也不会随 checkout 自动取得。
换 worktree/执行机前先受控转运所需材料并核 SHA，凭据只走既有安全交接，不写进本文或提交库。

## 2. 精确基线与接手前读回

| 项目 | 本次已交付基线 |
| --- | --- |
| 最终代码/远端已核 HEAD | `848d50bd5878454ea0d69096dcaa2778365460b9`，tree `945ce411018ec95ed5a864e498d90105a57d27b9` |
| 实际业务 driver 源码 | `1ca8f031dab70cfd702edb9c277ca6de8c0897db`，tree `97750627bd600f96b352a7d228004fa9d6d2438b` |
| driver / 正式 webhook 镜像 | 分别为 `sha256:9be4c9ab0daed5beccbb0ad2988f94c60c7b7a2fd138f86274ef96c7b0c3c0a4` / `sha256:1e36436e41f15aff9bda4b73737e9f0d24077cdb27ae04097f0d2c565d51b840` |
| 实际 component | 源码 `f7b852e6242608bcade9a2b1676971390bdc848f`，镜像 `sha256:39cde89462cbc215cbb480f662a2f2f1870abbbe6768b955325b06dc60db762a` |
| PipelineVersion / IR SHA | `f07dcf97-14d4-4cfb-a807-cdc0913c8a54` / `3d1d3a85de4474718d864428a28f0b53fac9ea0bbe0582b8c0fce24ad57c3f8d` |
| 隔离候选 Release / digest | `e3ce1f45-0d98-5e28-94d9-5dbb9da7add4` / `1191b7d5e2013b2ebc6a197c88b1fc56de6d6304f82e57b80305d227367c106f`；未开启公共 FINETUNING/current |
| 正式 profile | `ft-v1-s15-f7b852e`；实际 K8s `v1.35.8`、Argo 镜像及二进制 `v3.7.3` |

最终 HEAD、driver、component 是三个明确来源，不应把已运行镜像统称为“848 版本”。
代码门禁见 [最终 webhook 门禁](../.scratch/finetuning-cpu/records/WEBHOOK-transient-unavailable-20261015/verification-receipt.json)；真实业务证据见上述交付索引。
后续部署若换源码、镜像、IR 或 Runtime，应重新冻结其实际绑定，不能拿旧镜像/旧门禁冒充新材料。

所有生成、构建、数据库验证、测试和负载仍在 `ssh fedora` 执行，本地只做阅读、编码、文档、Git 等轻任务。
Fedora 任务根为 `/home/chabking/workspace/finetuning-cpu-m3a-20261014`；业务材料根为其 `env/deployment-direct-s15-1ca8f03-r2`。
唯一获准集群 API 是 `https://172.16.101.10:6443`，kube-system UID `5277649d-e28d-4a0a-ae76-8354365c63bc`，节点属于 `.10～.12`。
TENANT 为 `ani-ft-core-20261014`，UID `8f79e7cc-769b-4b2d-87d3-a69c53c946f3`；SYSTEM 为 `ani-ft-core-system-20261014`，UID `f81b058f-91fb-4599-b96e-b700c605cc92`。
这些是交付时事实，接手部署前读回，不假定环境永久不变。

[最终环境 actual](../.scratch/finetuning-cpu/records/FINAL-environment-actual-20261015.json) 的 09:50:59Z 快照没有当前训练/业务 driver writer。
19 个原 workspace PVC 全同 UID Bound 保留，quota 为 19 PVC/380Gi，已用满；这不是可以自动删除历史 PVC 或继续扩额的授权。
正式 webhook 为 generation8/2Ready，Role 已恢复，原 MWC scope 保持；共享 RustFS 为 generation4/Ready1。

## 3. 下一次先交付一条真实自动登记链

当前首要断点是 ModelDev 的真实 Model 接入，而不是重做训练流水线。
[RegisterModel](../internal/biz/finetuning_registration.go) 当前只保存原登记内容并返回 `MODEL_DEPENDENCY_UNAVAILABLE`；
[持久化实现](../internal/data/lifecycle/finetuning_registration.go) 保存的也不是 Model receipt 或外部写许可。
[基模读取端口](../internal/biz/finetuning_admission.go) 已留出只读边界，生产接入仍须真实提供方。
不要因为字段/端口已存在，就把真实登记、逐写仲裁或恢复写成已实现。

下一条最小纵向切片建议先选**已授权的现有目标模型**，完成一个真实版本登记，再补新建模型分支：

1. Model owner 给出当前正式 API 模块版本、完整 RPC 名、业务 mTLS 入口、服务身份与租户/资源授权合同。
   先评估已有能力，缺少原业务键 pending/结果查证或固定对象表达时，由提供方补最窄正式接口；不复制 Proto、不使用 sibling replace。
2. 接入真实基模/目标读取，固定 Model UUID、基模精确版本、文件 version/size/SHA；保留现有 prepare 的内容核验。
   测试身份/委托须来自既有调用合同和 owner 批准范围，不借 Gov 证书；仍被替代的上游部分单独说明。
3. 选定一条存储交接：Model 能接受原 publication 固定对象则直接引用；确需复制才采用固定的受控复制绑定。
   不在失败时换 bucket/目标或动态切模式，不能把固定 version 降为 latest。
4. 先补 D09.3 的具体写派发与 Stop/deadline 仲裁，再允许第一个真实外部写。
   沿现有 biz 端口、data 适配与持久记录增量实现；不增加通用许可平台或另一个登记工作流。
5. 用新 case 经正式 JSON READY/受理/KFP/训练/发布代码走到真实 `CreateModelVersion`，读回核实原字段及 READY，再完成正常关闭。
   原业务键重放只返回同一 ModelVersion；从 Model 读取同一 tar，核原 publication 的 version/size/SHA。
6. 补新建容器分支与本切片直接相关的失败窗口；保持发布、关闭、旧 CPU 行为及同键冻结约束。
   先交付这一条可用链，不以页面、全部跨框架矩阵或独立 Inference 全部完成作为开工前置。

本步只通过的 Model/身份断言逐项记实际；真实 Gov 用户链与 BFF 未接通时，不把替代来件升级为它们已通过。
当 A10 单次六项事实及 FT22 正常成功确实成立时，才新增相应成功声明；原 M3-A 的依赖错误结果不改写。

## 4. 按依赖顺序交付后续阶段

| 优先级/责任 | 具体交付 | 验收出口与依赖 |
| --- | --- | --- |
| P0 Model owner + ModelDev | 当前绑定所需 MR01–08；真实读/登记/查证、固定对象、删除保护；D09.3/FT25 | 先完成上节最小真实登记链，再补相关竞态；页面不阻塞；各 MR 按真实覆盖范围记录 |
| P1 Gov + ModelDev | 精确 Release 查询、真实服务/用户身份、持久受理投递与重放、停止/查询 | 复用现有 Proto；至少一组非默认参数从真实 Gov 来件贯穿原快照和执行；不冒用 Gov 身份 |
| P2 BFF/页面负责人 | 真实表单、预设/历史详情、提交、查询、停止与登记恢复入口 | 依赖 P1 合同；PA05 等真实页面断言与后端事实一致，错误有可理解结果 |
| P3 Model + Inference owner | FT29/C23 从本次登记新包独立部署/调用现有 Inference/KServe | 依赖真实 ModelVersion；不挂训练 PVC；结果关联原 publication/tar SHA，不用旧基模或 fake 响应 |
| P4 各 owner | 补齐本版适用 FT/PA/MR/CPU 回归、非原作者交接及正式启用材料 | 达到 D11.4 的 SCHEME_FINAL 后，由 Gov/环境 owner 按批准流程启用；不因 M3-A 完成自动切公共指针 |

P0 的 API/身份确认和 P1 合同核对可并行；前后端可据固定合同开发，不必串行等待所有实现。
顺序控制的是实际纵向验收与启用，不缩减最终 MR/FT/PA 范围。
每轮只扩大到改动相关或有失败关联的验证；代码提交前仍须在 Fedora 完成仓库要求的 `make verify`。

### P0：Model 与 FT25 需要落到哪一步

合同入口为 [Model 对接视图](../.scratch/finetuning-cpu/model-integration-requirements.md) 的 D08、唯一 spec 的 D09.2/D09.3。
MR01/02/03 解决可信读取与目标；MR04/05 解决登记/原结果查证；MR06/07 固定成品与存储交接；MR08 验证实际删除路径保护。
这些是所选绑定需要的真实能力，不是要求 ModelDev 作者接管整个 Model 仓库。
提供方可能已有部分能力；接手应核其当前正式版本和真实部署，不沿用旧文档的“尚缺 API”快照作当前结论。

稳定业务键保持 `md-container:<execution_id>`、`md-<execution_id>` 和 `exec-<execution_id>`；new/existing 分支不互换。
每项 `CreateModel`、`CreateModelVersion` 及选定复制模式的真实写都单独与执行行的 Stop/deadline 仲裁，事务外才做 RPC/I/O。
先获准的请求可能迟到成功；记录原 receipt 不自动授权下一写，404 不单独证明原写未发生。

| 必须补的真实场景 | 必须观察的结果 |
| --- | --- |
| FT25.1：Stop/到期先提交 | 后进入仲裁的具体外部写次数为 0；原意图不放行 |
| FT25.2：容器已成功、版本派发前 Stop | 容器 receipt 保留，新的版本写为 0；不删容器、不换目标/key |
| FT25.3：版本派发先获准、随后 Stop 并丢响应 | 只查证/保存原在途事实，无双版本/重训；正常后继写或重发不再获准 |

CLOSED 后的登记恢复必须经当前授权、独立有限预算，继续原内容/key；不重开计算，也不续原计算 deadline。
先核同键异内容冲突、pending/READY 查证、丢响应和部分提交，再补与实现直接关联的异常；不能把 lease 当跨服务绝对一次证明。

### P1/P2：精确 Release、真实身份与页面

接口入口是 [modeldev.proto](../api/ani/modeldev/v1/modeldev.proto) 的 `ListPresetsRequest`/`PresetReleaseSelection` 与原查询/命令服务；
查询实现见 [finetuning_materials.go](../internal/service/finetuning_materials.go)、[materials.go](../internal/service/materials.go)、[query.go](../internal/service/query.go)，规则来源为 [参数与前端合同](../.scratch/finetuning-cpu/parameter-and-frontend-contract.md)。
Gov 保持唯一生效指针，按精确 `preset_id/release_id/release_digest` 与 `binding_generation` 读取规则并核绑定；不回退历史第一份 Release。
新受理冻结默认值，历史详情读原 Snapshot；原键重放不因当前 Release 改变而换参数、目标或 deadline，但仍检查当前访问权限。
`finetuning` 必须是对象，`{}` 合法；当前 A=0.0001/20、B=0.0002/10 只接受批准的完整组合。
只提交 B 的一个字段不得顺带改另一个；缺失/null、未知项和固定设置覆盖保持提前拒绝。

内部调用沿用 Gov 的 `docs/contracts/service-to-service-calls.md`（来源见 spec S2），接手确认完整 RPC、精确服务身份、tenant/资源范围和超时。
分别核用户权限/租户映射、Gov→ModelDev 持久投递和受理后 ModelDev→Model 后台身份；普通 body actor/tenant 不是可信授权。
BFF 真实提交至少覆盖一组非默认值、同键重放、历史详情、停止及登记恢复；不能只验证页面显示或直接调用后台 fixture。

### P3/P4：独立使用与整版启用

FT29/C23 使用**本次真实登记的 ModelVersion**，由 Model 提供固定包，经既有 Inference/KServe 真实调用。
关联版本、publication、tar SHA、Inference 请求和实际响应；不挂训练 PVC，不向推理工作负载发 ModelDev 控制身份。
M3-A 独立 CPU forward/generate 只证明产物可消费，不能替代这条 Model→Inference 链。
最终按唯一 spec D11.4 核全部适用 FT/PA/MR 与相关 CPU 回归及交接；A10 单次任务与方案最终验收分别报告。
公共 FINETUNING 依赖/Release/current 的启用由其 owner 管理；候选隔离验收通过不直接开放普通用户。

## 5. 历史未解决事实单独处置

| 原执行/证据 | 保留事实 | 后续独立处置边界 |
| --- | --- | --- |
| a16 `a16a03ee-873f-4bcb-8b72-59a0d73aad6a`；[close-recovery actual](../.scratch/finetuning-cpu/records/CORE_FLOW-stop-374ac79-20261015/close-recovery/actual-summary-terminal.json) | USER_STOP CLOSED/Step完成；Compute `NEEDS_REVIEW / attempts=3 / BLOCKED`，原5终态对象/PVC保留 | 先核原 UID/持久退出/后继依赖，再走受控清理；不重置尝试预算、删 finalizer 或 SQL 改 APPLIED |
| b7 `b7b52879-46da-441c-85f4-4b93c4d1c046`；[terminal observation](../.scratch/finetuning-cpu/records/b7-stop-observation-20261015/terminal-actual.json) | `TRAINJOB_CREATE_UNRESOLVED / NEEDS_REVIEW`，原 POST 结果 UNKNOWN，ClosedAt null；未观察到 active writer | 先核原请求/Run/UID及审计事实；不因后续422或当前无writer推断原POST失败，不重POST或假CLOSED |
| R2 `bc66c25a-98f2-4d9c-8f92-5a6738a3313c`；[failure actual](../.scratch/finetuning-cpu/records/CORE_FLOW-restart-1ca8f03-r2-20261015/failure-actual.json) | 原wait访问API VIP报no-route，网络根因仍UNKNOWN；后置16TLS200、后来R3通过不证明已定位/修复原原因 | 有新复现才围绕原节点/身份/网络做窄诊断；不追溯改判、不顺手改共享CNI/网络策略 |

[Deadline r1](../.scratch/finetuning-cpu/records/CORE_FLOW-deadline-1ca8f03-r1-20261015/failure-actual.json) 是训练前到期失败，不能替代已单列的运行中 Deadline r2。
上述历史处置不重开本次已完成 Goal，也不抹去 M3-A 通过；若后续切片依赖某原对象，先解决其身份/结果不明再操作。
其他原失败、ZIP/reference、私有原始日志与 actual 均保留，不用新报告覆盖。

## 6. 下一位接手者的首次交付

先对照唯一 spec A04/D08/D09.3/D11.4/D12.7、当前源码和 Model 已公布合同，固定一张本次实际调用表：RPC、模块版本、身份、tenant来源、资源、key、预算、查证语义。
然后交付第3节那条最小真实登记链及相关测试证据，写清尚被替代的上游；不要先重跑全部历史用例或铺开通用框架。
新用例由官方 renderer 生成新 execution/op/新 case；未知 start 先核原单次台账和持久事实，不重复 create、借旧 case 改键或续期。
短 deadline 沿 [原交接 atomic 机制](../.scratch/finetuning-cpu/records/M3-A-handoff-20261015.md) 同进程 render→guarded start，原已完成入口不得重跑。

仅部署本任务隔离资源及已经确认的有限身份/profile，不把历史授权扩大到未来共享变更。
共享 Model/Gov/Inference 部署、API发布/数据库迁移由对应 owner 协调；共享控制器升级、替换旧hook、扩大MWC/VAP准入、改共享CNI/RustFS须另行明确授权。
涉及PVC/产物/历史对象清理，先列原UID、保留集和可恢复证据，再取得对应清理授权；19PVC用满不构成删除理由。
每次回报区分 CODE/真实PG/目标集群/真实用户链/独立Inference，附精确源码与材料和实际结果。
只有本次真正实施的那一层通过才升级状态；当前本文只完成后续交接说明。

# ModelDev 动态租户环境实施报告

报告时间：2026-11-02（Asia/Shanghai）；原始收据为UTC。执行者：Codex。

**原Goal已完成；01–10任务已回填，必需验收及新增多租户完整业务回归全部PASS。** 组件测试和真实现场证据分别列明，未执行范围见第6–7节。

## 1. 实际交付

业务链已实际到达：原标准Namespace → 显式Ensure/Get READY → 正常业务Create → KFP/Trainer真实执行 → 导出/verify/产物发布 → Model登记READY → 正常关闭 → 原GPU事件ACK及占额全退。A3普通CPU、B4 GPU_SHARED微调、B独立CPU运行中Stop已有实际结果；本轮最终配置下又完成A4普通训练/B5 GPU微调的完整多租户回归。

| 任务 | 本轮实际状态 | 证据/边界 |
|---|---|---|
| 01 基线与合同 | 固定API不变，当前源码/用户平台信任修正已记录 | [固定pin](evidence/09/final-switch/fixed-sources-api-pins.json)；新tests/verify未运行 |
| 02 Res NS/镜像 | 标准NS复用，两新受管镜像tuple实际消费 | [tuple登记](evidence/10/finetuning-ir-correction-20261101/image-metadata/readback-cleanup-actual.json) |
| 03 环境注册 | 最终A/B显式READY、并发持久视图合同已核实 | [LIVE03](evidence/10/completion-live03/persisted-view-resolution.json) |
| 04 v4/facts/STS | 实际I/O、下载SHA、自然token轮换及业务STS跨scope拒绝通过 | [当前STS](evidence/10/completion-live09/actual.json) |
| 05 派发/关闭 | B5正常原owner自动完成，原占额全退 | [B5关闭/原ACK](evidence/10/completion-multitenant-regression/b5-original-close-and-release-actual.json) |
| 06 native/webhook | 最终native正反例、AD01/AD02及真实故障恢复通过 | [实际收据](evidence/10/completion-admission/result.json)、[组件/现场边界](evidence/10/completion-admission/ad01-ad02-coverage.json) |
| 07 Gov API | Ensure/Create/Get/合法Stop/跨租户拒绝及原ACK实际使用 | LIVE06/07/08 |
| 08 材料 | 四新Version/最终bundle已发布、seal、消费，publisher撤权 | [唯一发布与撤权](evidence/10/finetuning-ir-correction-20261101/publisher/final-step67/actual-publication-window.json) |
| 09 清理切换 | 原历史清理、原退额、最终pins及共存对账完成 | [LIVE11](evidence/10/completion-live11/result.json) |
| 10 真实验收 | 原必需验收全部PASS，新增当前多租户完整回归PASS | [验收表](acceptance-results.tsv)、[REG01](evidence/10/completion-multitenant-regression/result.json) |

## 2. 源码与部署映射

| 组件 | 固定源码 | 实际镜像/证据 |
|---|---|---|
| ModelDev main | `1dfd6941e5780eca0d55dc4e5fbbd2f58e0710ab` | `10.10.1.66:30003/ani-gpu-platform/ani-modeldev-service@sha256:fe944d52cabd90e728ab1c8c5b3f29e08e51edd2e8a1f897e7b68a42311eb998`；[构建](evidence/10/main-verify-time-1dfd6941-19240-release/publication.json) |
| native/MWC renderer | `4d6013c5b13cf150268ebbf63544ad16e1672d13` | VAP10/observed10/typeChecking{}；MWC原spec恢复一致、现generation5；[安装](evidence/10/trusted-platform-native-final/actual.json) |
| Gov | `244780ffe6b9a7deb1c3f6f162e474e76a73a566` | `sha256:493f472161824989be406f3cdeebd365e10a4e7d3baa469ac06090dbecb68540`；[原部署](evidence/09/final-switch/governance-final-workbench-actual.json) |
| Resource | `67dceef03f01215c294a55f11464e3e3a52cb783` | [原66正式切换](evidence/09/res-recovery-v3-final-window-actual.json)；本轮未改其源码 |
| step/CPU/GPU运行镜像 | `67b86699cb15e7ecdba5cc30f22c3be45b367726` | [三镜像完整ref与实际发布](evidence/10/step-67b86699-19240-release/publication.json) |

当前单MD Pod `6f29950c-625e-4b2f-98a0-bc7309016d7e`，startedAt `16:30:58Z`，0restart；最终A/B Ensure共用该进程。最终维护只换平台/runtime配置，main镜像未变、webhook Deployment未重滚。API仍ModelDev `d807cbdbd5dd`/Resource `71b3ad7a2a88`。[最终切换](evidence/10/final-policy-maintenance/final-config-switch-actual.json)

必要修复：原关闭authority恢复循环（9ca）、严格never-created原生证明（2ac）、平台身份信任（4d）、export报告时间对齐Kubernetes秒精度（1df）。没有ACC决定退额的依赖；Gov原ledger/event/ACK不改。按用户本轮指令只做真实必要编译/运行，新增tests/make verify为NOT_RUN；历史固定源码测试范围见各Result，未挪用为当前源码PASS。

## 3. 固定合同落地

一个共享实例、显式Ensure/Get；复用`tenant-<canonical tenant UUID>`标准Namespace，核验原UID及network归属，缺失报错，不新建/认领/补标签。无新Res Namespace RPC。Reserve、关闭fence和原占额释放保留；只接受v4及固定四root参数，冻结镜像/参数/资源规格。未知POST不盲重放，无运行时兼容，Inference未改。

仅`kubectl create ns`不足：标准名、平台网络归属和Resource核验仍是开通前提；随后必须显式Ensure READY。最终平台pins原漂移已通过原5.4维护消除，原冻结spec未原位修改。

按用户明确授权，原KCN、Multus、GC、Volcano scheduler/device plugin、精确node、Trainer、JobSet身份整体绕过ModelDev workload与Pod webhook；persistence仅UPDATE status或sameSpec metadata放行。没有新增身份/RBAC、逐注解或UID锁。Argo/JobController创建Pod仍保留现有运行配置及退出证据finalizer注入，日志留存/释放由MDowner负责。[信任范围及实际策略](evidence/10/trusted-platform-native-final/actual.json)

## 4. 部署输入与四个定义

唯一集群入口`kubercloud@100.64.128.12`；context `kubernetes-admin@ani-kcn-full`；API `https://lb.kubesphere.local:6443`；cluster UUID `f39bf6e2-f3ed-4191-ada3-caeebf86c220`；kube-system UID `3eeb5379-8748-4bf8-8f6d-d7a48ce3bc24`。写前完整身份核对见[最终维护](evidence/10/final-policy-maintenance/window-start-actual.json)。历史新10不是当前目标。

[deployment-inputs.json](deployment-inputs.json)已记录当前实际映射：platform SHA `2d75921aa497aca3b603c5f8483e3bfb4825eaee678b404766c45756dee7c4e5`、runtime SHA `5c13854685b2adf50bcb6926de79b12fcb311738b84330c0788560f849e51919`，12个实际bootstrap refs已封存，VAP10/MWC最终spec与平台pins一致；LIVE10恢复后MWC generation5，digest剔除generation。最终bundle仍`4a655afda6f4d1b6b798f30359c7ccfd0ce4271836b3bbba7f187b02c3f011e4`。[最终结果](evidence/10/final-policy-maintenance/result.json)

| Profile | 原 parent Pipeline ID | 新 Version ID | IR SHA256 | canonical material SHA256 |
|---|---|---|---|---|
| modeldev-mlp-cpu | `dc2f677d-5cd6-439b-87ae-04d7e73ae42e` | `3df2416f-642a-4c4f-a78c-75c228f9b844` | `35577aa27932f75c76e2829fb099f7c77924c85f1ce67f3cfde3d2684c246fc7` | `a98d99efbde37e9e9d7a7baa377c641961509f9d3f8742f37ff956f25ae9c504` |
| modeldev-mlp-gpu | `9616ccd5-b32b-4ba4-adcc-f00a0b8f1a39` | `f7c84a7b-33a8-4c64-b3b0-cc5b41d0d25e` | `fe24eea47f177daaa64c6599056b0b046805414375c04e21c3947e919caeab49` | `bcb9d87ff106391b1d82f8197b5ebb8a9cb1a572fe4422c1aa0b555fc025fbe3` |
| modeldev-sft-lora-cpu | `4d042c5b-fc25-4819-9a28-80ca7903c863` | `7f3e6ae3-66e5-4f1d-aaec-9b64d0165a64` | `abf50d2acf4611497391824d53a20dec69880ff20c81d429bb808bfe074eb5c5` | `236211c4bad796471b1fc6ffb35fe328d1abc9be93d5929514158b3b0862e84e` |
| modeldev-sft-lora-gpu | `a3cf8be5-0244-4a6d-9010-c5afda0d61b1` | `aa58f5c6-c80f-4ba0-b526-6e455ecec9c5` | `e7bf8f33984cf5274a90e2b0d3dc0c7ef408a9fb5e347c01bca3b5b38b197c31` | `3a8b8fa0ceaebe1b0bed327d156f70b52fafbd1d625eef49087b0998bd98e17f` |


三运行镜像仍为step `439aa9c7541d…`、CPU `ec20caf624f6…`、GPU `245a32006ddd…`，完整digest及registry GET200见[发布](evidence/10/step-67b86699-19240-release/publication.json)。CPU/GPU Res tuple为`img_3af6f494c02c493dbbdff11099290d72:1`/`img_4e77202d4e3c49daac15d995366f2e8b:1`；原大base层复用，不在Fedora打包传输。四canonical材料与IR随[sealed目录](evidence/10/finetuning-ir-correction-20261101/final/materials/sealed/)交付，publisher临时身份已撤权。

## 5. 旧数据和资源清理

原旧逻辑资源/数据按原UID范围清理；不备份，不改Inference/共存资产。[原历史清理](evidence/09/legacy-data-cleanup/native-exact-discard-actual-result.json)。A1/A2失败attempt手动精确退役，不伪造CLOSED；B1/B2/B3原失败执行均保留事实并原协议全额退额。B4正常owner自动关闭及清理：7/7compute不存在、原event `bf4eae12-8601-4d27-a900-dd764248282d` ACK APPLIED，原charge24570全退、account44/version8/occupied0=remaining0。[最终原释放](evidence/10/resumed-mainflow/b4-original-close-and-release-final-1793549008812487147.json)。PVC按合同保留，不把compute回收写成PVC已删。

最终5.4维护：正常删除5个已完成KFP Run；原28自有接入对象、两旧Experiment metadata及两旧环境登记精确撤销，再使用最终pins新开通A/B。业务Execution、模型、输入、S3、下载、标准NS和共存资产保留；Gov短暂停入已恢复UNSET/defaultfalse。[撤旧](evidence/10/final-policy-maintenance/revoke-actual.json)、[恢复](evidence/10/final-policy-maintenance/gov-unpause-actual.json)。

清单内额外工具及私有凭据暂存已清，本轮临时STS client也已清。19240服务binary/OCI供复用、共享Docker/ctr/Go/cache/KFPvenv和业务downloads保留；不声称扫描全主机。[原工具回执](evidence/10/task-final-tools-cleanup-20261101/actual-cleanup.json)、[本轮STS工具](evidence/10/completion-live09/cleanup.json)、[观察工具](evidence/10/completion-multitenant-regression/b5-observer-complete.json)、[总账](evidence/task-added-tools-cleanup-ledger.json)。最终维护没有新增工具/身份/TokenRequest或凭据文件。

## 6. 验收结果

- **LIVE01 PASS：** 当前完整身份、源码/镜像及最终配置映射已填，见部署输入和最终维护结果。
- **LIVE02/AD01/AD02 PASS：** 四个真实native错误对象均明确被原VAP拒绝422，正常UPDATE和真实CPU/GPU创建成功。[实际native](evidence/10/completion-admission/native-evaluated.json)。AD01/AD02按原组件层级验收，已有组件测试与当前生产Go零diff、普通租户核心AST等价；当前实际注入、原Stop、LIVE10构成现场证据。未把组件外部double写成全字段现场矩阵。[逐项映射](evidence/10/completion-admission/ad01-ad02-coverage.json)。
- **LIVE03 PASS：** 最终A/B显式开通READY，同一MD进程。并发200READY/202PROVISIONING返回同env/binding/Experiment，最终GetREADY；锁忙源码仅读取持久行，锁持有者合法持久化PROVISIONING后重验，并非伪造状态，无需修代码。[并发持久view核实](evidence/10/completion-live03/persisted-view-resolution.json)。
- **LIVE04/05 PASS（固定原进程/最终4a655材料时段）：** 十个KFP GET在自然token轮换前后均200，两指定audience/授权拒绝成立，0TokenRequest/TTL改变。[轮换前](evidence/10/projected-token-live/final-bundle-rotation/first-actual.json)、[拒绝](evidence/10/projected-token-live/final-bundle-rotation/two-fixed-denials-actual.json)、[轮换后](evidence/10/projected-token-live/final-bundle-rotation/second-after-natural-rotation-actual.json)。未将旧Pod身份倒填为新进程。
- **LIVE06 PASS：** A3 CPU loss下降、模型下载SHA一致。[A执行](evidence/10/resumed-mainflow/readback-A-1793535042015777219.json)、[A下载](evidence/10/resumed-mainflow/download-A-1793535060348452124.json)。B4执行`59f92b91-5e86-4dd1-90ec-cb1037756758`真实GPU_SHARED训练loss7.05126428604126，八步骤全成功、SUCCEEDED/PUBLISHED/CLOSED、19产物PUBLISHED。[B执行](evidence/10/resumed-mainflow/readback-B-1793548972071555495.json)、[原Pod观察](evidence/10/b4-real-finetuning/original-pod-main-observations.jsonl)。Model父`454cef60-ca01-4dae-a4e6-b8881026c688`及版本`6afb1f32-a0d4-4c92-8d82-7799e450f5ee`正式GET200 READY且父versions包含该版本（`id`为UUID、`model_id`为业务标识）。[正式Model](evidence/10/b4-real-finetuning/normal-parent-model-1793549545722774863.json)。B正常下载adapter200/1274720B/SHA `c99f214957264a07c15ffd40f5ec1b96f1813dbb3d14a5320db249be9ae2f987`，与发布metadata一致。[下载](evidence/10/resumed-mainflow/download-B-1793549202671540591.json)。正常自动原退额见第5节。按用户追加要求，最终配置后又完成A4/B5完整多租户回归，见下文REG01。
- **LIVE07 PASS：** B真实Running trainer后合法Stop，A同期GET200；writer exit143/回收/log可用，CLOSED、Compute APPLIED/6原对象不存在。[Running](evidence/10/resumed-mainflow/readback-B-1793538509431302017.json)、[Stop](evidence/10/resumed-mainflow/stop-b_cpu_stop-1793538561609716217.json)、[同期A](evidence/10/resumed-mainflow/get-A-1793538559034465325.json)、[owner结果](evidence/10/resumed-mainflow/live08-normal-jwt-negative-and-b-cpustop-1793539024651916679.json)。
- **LIVE08 PASS：** 双向详情/日志/Stop与artifact八项真实404，前后Stop意图不变，合法B下载200。[前七项](evidence/10/resumed-mainflow/live08-normal-jwt-negative-and-b-cpustop-1793539024651916679.json)、[A→B成功artifact拒绝](evidence/10/b4-real-finetuning/normal-model-and-foreign-download-1793549330561127174.json)。
- **LIVE09 PASS：** A4实际prepare取得正式业务STS，自己固定输入version读取206，B4已发布adapter精确version拒绝403 AccessDenied；凭据只在内存，临时工具已清。[STS实际](evidence/10/completion-live09/actual.json)、[清理](evidence/10/completion-live09/cleanup.json)。普通tenant原workload Secret/挂载/借SA/exec/TokenRequest实际RBAC拒绝另有原收据。
- **LIVE10 PASS：** 当前真实MD代表dryrun201→受控不可达500→原spec恢复后201/readyz200；故障中普通VM代表201且无MD调用。0持久Pod/新身份/RBAC，原MWC已恢复。[故障及恢复](evidence/10/completion-admission/window-actual.json)。
- **LIVE11 PASS：** 原manifest资源、九表与Gov原PK清理及原占额释放完成，旧运行入口已移除；最终41个共存资产、原NS与无关Pod的UID/基线健康保留。[完整对账](evidence/10/completion-live11/result.json)、[最终共存及同进程](evidence/10/completion-multitenant-regression/final-coexistence-and-process.json)。
- **REG01 PASS（用户追加）：** 最终platform2d759/runtime5c138、同一MD Pod下，正常A4普通训练`50dc7220-8001-463e-83f3-fe4d70f9af66`与正常B5 GPU_SHARED微调`636f6bee-093a-4ea4-b768-029bb5031ca3`均SUCCEEDED/PUBLISHED/CLOSED。A日志loss下降，B八步骤成功/loss7.05126428604126；A4504B与B1274720B产物正常下载且SHA匹配。B模型父`1f7fb625-bb25-4075-9ea8-0b7e79a09c11`及版本`f07553cf-41f5-4d02-98d6-2f743398b245`正式READY；两个owner各自动7/7compute回收。B原24570全退、原event`0bf7bceb-738a-45bf-bb0e-158106b4b8f4` ACK APPLIED、account44/version10/occupied0=remaining0；没有手动Stop/reconcile或SQL变更。正常A访问新B执行404。[新增完整回归及全部证据索引](evidence/10/completion-multitenant-regression/result.json)。
- **必需验收无BLOCKED/NOT_RUN。** 按用户要求未重跑全量make verify；GPU_WHOLE及全字段现场组合矩阵未执行，未冒充已覆盖范围。原表各组件/现场证据层级保留在[验收表](acceptance-results.tsv)。

## 7. 风险和边界

- 原10项任务及必需验收已完成，新增当前多租户回归通过。新租户仍需标准Namespace及显式Ensure READY，普通手建NS不会自动认领。组件/真实现场证据分层见第6节。
- 已授权平台信任是用户对附件spec§11.1–11.3/冻结策略范围的明确修正，原RBAC/身份不变；不静默扩大租户权限。必要Pod创建注入仍在，重构该注入不在本轮。
- 原B3 verify Begin409已由现存middleware日志确认；源码存在纳秒/秒直接比较缺陷，1df已修。原B3 native完成秒值未保留，不能把B4成功倒填为已证实B3唯一根因。[原RPC](evidence/10/resumed-mainflow/b3-original-main-rpc-opensearch-actual.json)、[原报告时间](evidence/10/resumed-mainflow/b3-original-finetuning-timing-actual.json)。
- GPU_WHOLE未实测，不以CPU或GPU_SHARED替代。原历史A1/A2手动清理不冒充正常CLOSED，原B失败与成功结果均保留。未修改Inference。

## 8. 交付文件

[部署输入](deployment-inputs.json)、[逐项验收](acceptance-results.tsv)、[issues01–10 Result](issues/)、固定[spec](spec.md)/[contracts](contracts/)、完整公开证据及SHA256清单随本目录交付。主服务/step binary和OCI保留在19240，供复用；私有工具与凭据暂存不进入交付。

本报告按八节模板交付；新增当前多租户回归见[REG01](evidence/10/completion-multitenant-regression/result.json)；最终环境开通证据见[维护结果](evidence/10/final-policy-maintenance/result.json)，B4训练证据见[当前汇总](evidence/10/b4-real-finetuning/current.json)。

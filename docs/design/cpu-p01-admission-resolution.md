# 不可变受理候选解析

当前实现使用 `biz.AdmissionResolver.Prepare` 和 `Finalize`，不再使用历史
Release＋facts 文件入口。租户开通、平台配置与证据边界见
[动态租户执行环境](../modeldev-tenant-environments.md)，只读环境来源见
[受管解析事实](cpu-p01-managed-admission-facts.md)。

## 输入和事实来源

Prepare 接收 Governance 已授权的 IdentityRef、Intent、ComputeOrigin 和
AcceptedAt。tenant、actor、operation、execution 来自可信调用者，不能由用户
指定 Namespace、Pipeline、Experiment、材料文件或存储 scope。
Implementation 和 ComputePreset 分开选择，不建立实现/计算组合审批。

| 事实 | 读取来源 |
| --- | --- |
| 固定实现、Pipeline/Version/原IR hash、程序/参数/输出合同与组件资源 | 同一不可变 bundle 的规范 ImplementationMaterial |
| InputVersion、版本化对象、大小/hash与核验证明 | 同 tenant 的 PostgreSQL READY 记录 |
| 原 Namespace/SA UID、Experiment、binding ID/digest、scope/workspace/timing、dispatch owner与managed refs | 该 tenant＋固定 cluster 的持久 READY sealed binding，逐请求 live 身份复核 |
| 计算来源、预设和原授权范围 | Governance 提供的正式 ComputeOrigin；GPU计划另由正式 ACC事实固定 |
| 固定受理时间及期限 | 原 AcceptedAt和平台有限 timing；重试不重新计时 |

Prepare 只读事实并返回完整 PreparedCandidate。它不写 acceptance/execution，
不创建 Run/TrainJob/PVC，不产生 Reserve 或占额。它校验输入/基模属于原 tenant
批准的 connection/bucket/完整路径前缀，固定所选材料、原身份、参数、资源与
`artifacts/<tenant>/<execution>` PipelineRoot。普通训练镜像经现有 Resource
ImageRuntime 解析，冻结 managed authority 与精确 pull refs。

Finalize 重读 READY 环境与 live 原身份，逐项比较候选的 Environment、DispatchOwner、
Workspace、PublicationScope、ManagedImageAccess；微调另核对 ModelScope。
它使用已核验的正式 GPU事实编译 RuntimePlan，然后生成规范
`ani.modeldev.execution-spec.v4` 字节及 SHA256。环境、owner或managed refs变化时
拒绝原候选，不隐式改写或重绑。Dispatch 在外部请求前再次复核当前 READY和原身份。

## 配置入口

`command.admission_resolution.catalogue_directory` 是固定四份材料的普通文件目录。
`runtime.tenant_environment_file` 和 `tenant_environment_sha256` 固定一个公共平台
spec，不含 tenant 或完整binding/facts数组。启动组装登记 Repository、Platform和
只读 facts Reader；受理请求不调用 Ensure。普通文件与 ConfigMap投影的安全复制
方式见[部署合同](../modeldev-tenant-environments.md#authoritative-inputs)。

旧 `facts_files`、`binding_file`、`binding_sha256`、`controlled_model_facts`
属于被移除的入口；strictconfig拒绝它们。省略runtime仍是未就绪运行壳；显式配置
runtime必须同时提供command、catalogue、固定平台spec和真实依赖，不以空块开启。

## 验证边界

共享合同校验规范字节、关联和纯形状；它本身不能证明集群资源、授权或存储存在。
生产 facts Reader额外校验READY记录和live原Namespace/SA，环境Ensure负责固定对象、
完整授权边审查与seal发布。Governance继续拥有当前授权、用户幂等和正式受理事务。
已受理原键重放也必须先完成当前授权，旧actor仅用于审计。

真实PG/生产用例＋外部边界替身测试和真实A/B业务验收分别记证据。规范候选、HTTP200、
配置可加载、渲染成功或监听就绪均不能代替真实开通、调度、训练、artifact隔离、关闭
和原占额释放验收。旧CPU-P01 Release/facts文件模块记录仅属于其历史revision，
不证明当前动态环境实现或指定测试集群已上线。

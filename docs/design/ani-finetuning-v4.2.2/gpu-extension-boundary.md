# GPU后续接入边界（独立准备，不阻塞CPU）｜v4.2.2同源阅读视图

权威正文：spec.md；SHA256：`5323612099bbfe54f2e13b3aea4a5c3c4f9a4cf143a64f7cc526b530f9925ec9`。本文件只切片原文，不独立修改；先改spec再导出。

<!-- source-section: A02.1; source-sha256: f2666316d7dab8a7ff2c5b251981ceed291459a19f43beea2905841c7b929647 -->
### 2.1 GPU后续接入：本轮记录接缝，不并入正式功能

当前业务仍为CPU微调。**GPU正式资源选择、Gov占额、ACC的ModelDev owner接入、GPU训练和退额，作为后续独立批次交付，不进入本版产品接口或完成条件。**通用训练与微调将共用执行资源接入，GPU不作为学习率等算法参数塞入`finetuning`。[S10]

W1/W3在现有工作中核对并记录资源解析、快照、Trainer渲染和计算关闭的接入位置；若结构已经足够，**无需为“留接缝”修改生产代码**。不提前发布GPU Proto/数据库字段，不把`CPUResources`一律改名，不给CPU执行补零值GPU或假charge，不建设资源插件框架。

Gov/ACC合同差异核对、Fedora渲染检查及具备条件时的隔离GPU验证可以独立并行。**它们不构成W0–W6开工或本版验收的前置，也不能替代任何FT/PA/MR证据。**缺GPU、缺设备插件或未完成owner注册，只影响相应GPU准备项；当前CPU路径仍严格拒绝GPU。范围和分工见D10.2、D11.5、D12.5。

<!-- source-section: D01.2; source-sha256: 44bd2f890949c349a9f993fe3cb9b8307cbaa8a511457c57e89d9db6d97f5b78 -->
### 1.2 ModelDev公共接缝核对，不要求提前实现GPU

本轮只在已涉及的代码处记录后续资源接入点：W1核对意图/Release/快照及资源摘要查询，W3核对实际训练容器渲染和持久关闭事实，W6维持CPU拒绝GPU及原快照回归。相关位置仍以本仓实际`contract/cpup01`、受理解析、Trainer adapter和CloseWorker为准，不为文档示意搬目录或新增平行实现。[S10]

当前CPU合同只允许CPU是正确限制；这项限制不能被描述成ModelDev永久不支持其他执行资源。但**核对接缝不等于本轮必须重构所有资源类型**：现有结构足够就只记录；仅当发现同时影响本版CPU正确性或本版正修改逻辑的具体耦合时，才在原任务内做局部修正和回归。纯GPU专属改动留待下一批或隔离试验分支，不改变当前生产请求/快照/启用规则。

不新增CPU占额记录，不在CPU关闭路径空调GPU退款接口；不以“未来要支持”为由接受未实现GPU字段。仓库分工和验证范围以D10.2为准。

<!-- source-section: D02.7; source-sha256: a1ec71056034854590cea766e86b408055530cc3f2e8d05f9d7b0fcf77265f3f -->
### 2.7 后续执行资源入口与当前请求的分界

本版请求不增加GPU输入；资源/Namespace/Runtime等受管字段继续按2.1拒绝。后续GPU选择属于**与`finetuning`或`general_parameters`平级的执行资源合同**，由Gov/ACC现有规范承接，不由算法参数通道透传调度字段。后续具体字段、Proto编号、版本识别和附件关联在GPU批次固定；本段不是可用API示例或当前发布要求。

创建页当前仅展示真实CPU资源摘要，不能因本补充而显示可提交的假GPU规格。后续资源目录使用ACC并核对租户授权、ModelDev程序/Release与环境支持；历史详情使用原执行冻结的安全资源摘要，不读取最新规格重建旧任务。现在仅记录这些来源和接缝，不要求前端或Model服务增加GPU逻辑。

<!-- source-section: D10.2; source-sha256: 473b51200009d7ed3a2ee0976220b97b829df02a20ca121386f0f92d0a1bb9f7 -->
### 10.2 GPU后续接入准备：差异表与隔离验证，非本版交付门槛

#### 10.2.1 现在准备什么，下一批实现什么

**现在的最小产物是“调用/字段对应及差异表”和“适配验证记录”。**现有Gov/ACC合同能够表达的内容直接引用，不另写通用GPU授权或配额规范；不能表达的内容列为明确提供方需求。核对并记录实际仓库/合同commit，不把上一轮源码观察当成当前owner已经获准接入。服务间调用继续遵守A04的Gov规范。[S10]

后续正式闭环的职责方向是：前端经BFF选择获准资源；Gov沿既有机制解析计划并在权威受理中占额一次；ModelDev持久接受原执行/计划，作为唯一训练owner渲染和观察；计算关闭后按GPU批次固定的原账关联可靠通知Gov；ACC负责资源计划和使用/实际绑定观察。**本轮只核对接缝，不实现这条GPU业务链，不增加第二次扣额、额外注册前置或新的退额审批。**释放范围/时机、RPC与重试字段在GPU批次按既有合同定稿，不能从本CPU文档推导即时退额许可。

下一批首个目标建议为单节点、单进程、一个GPU设备，**整卡和固定共享规格同时纳入**；仅TRAIN使用GPU，train-wait及export/verify等先沿用已验证CPU路径。资源政策沿用整卡按数量、共享按申请显存MiB；不将共享实例自动算作整卡，也不把多卡/QLoRA/多机捆绑进首批。此处是后续目标边界，不是本版支持声明或硬件就绪证明。

#### 10.2.2 各仓库的当前动作和后续动作

| 项目 | 当前可做的核对/隔离准备 | 下一批正式实现；本轮不提前启用 |
|---|---|---|
| ani-governance | 核对GPU受理、投递、原占额关联和释放合同，列ModelDev业务binding/身份/字段差异 | ModelDev GPU准入、一次占额、可靠投递及原账退额接收 |
| ani-accelerator-service | 核对规格/计划能否被Trainer消费，列owner登记、使用关联、观察与验证材料缺口 | 仅补真实需要的ModelDev owner及协议适配；足够的已有能力不改 |
| ani-modeldev-service | W1/W3定位资源解析/快照/渲染/关闭接点；按需要参加隔离渲染验证 | 正式GPU请求与附件接收、固定计划执行、占额关联和可靠关闭通知 |
| ani-installer / ENV | 读取实际驱动、Volcano/HAMi、Trainer/JobSet及Runtime条件，管理已批准的隔离验证清单 | 按单一环境owner落实必要部署变更，不重复安装另一套调度体系 |
| 现有前端 | 记录资源区位置、目录和历史摘要来源；本版仍仅真实CPU能力 | ACC受控目录、资源选择与原执行资源详情 |
| Model / Inference | 本GPU准备不增加改造任务 | 继续各自模型/推理职责，不承担训练资源合同 |

当前不要求四个仓库同时修改生产代码。Gov/ACC先核对并输出需求即可；ModelDev/Installer如需试验代码，使用受控测试路径或独立分支，写明与正式adapter的关系。共享配置仍由ENV统一维护，Git分支不隔离集群；不冒用Inference身份，不越权接管共享组件。

#### 10.2.3 接缝差异表至少回答的问题

| 问题 | 要记录的结论；不猜填实际值 |
|---|---|
| 选择与解析 | 集群/池/规格/版本等选择由谁授权、谁解析；哪些为用户选择，哪些为受管固定事实 |
| 受理与附件 | 原operation/execution、计划摘要、占额关联怎样沿用既有合同传递；不能先CPU受理可执行，再补另一条GPU创建命令 |
| 快照与兼容 | 新资源合同怎样识别，原CPU字节/含义怎样保持；现有CPU类型是否可直接保留，而非无理由全局替换 |
| 真实训练对象 | GPU resources、调度及必要metadata落在哪个TrainJob/Runtime/PodTemplate字段和实际训练容器；等待组件不能误占GPU |
| 结束通知 | 原持久计算关闭事实在哪里；下一批原账释放关联、可靠通知及未知结果怎样接入；本轮不补假charge、不调用退款 |
| 查询与目录 | 目录来自ACC，支持范围由业务/环境筛选；详情读原计划安全摘要，不暴露密钥或原始内部配置 |

以上是业务用途和对应关系，不是新Proto/枚举/数据库schema。表中未能确定的项注明实际缺口和负责方，不能通过实现一个总返回成功的GPU adapter来补齐。

#### 10.2.4 三层验证，分别报告，不能互相抵扣

| 层次 | 方法与能证明的事实 | 明确不能证明 |
|---|---|---|
| Fedora渲染/模块检查 | 尽量调用ModelDev真实渲染/映射函数，或明确标注的候选适配；用锁定计划fixture检查TrainJob/Runtime/目标容器字段 | 未经真实ACC解析的fixture不证明ACC接入，离线YAML不证明控制器/设备可用 |
| 目标控制器验证 | 在已批准隔离范围观察Trainer→JobSet/Job→实际Pod，逐项比对资源、调度、metadata和挂载；保留ID/UID及版本 | Pod创建或Pending只证明对象/字段传递，不证明GPU计算及额度闭环 |
| 实际设备验证 | 具备授权GPU硬件和环境时执行小型真实计算，整卡/共享分别记录设备实际使用与对应限制观察 | 不代替BFF权限、占额、幂等、停止退额及微调FT验收，也不证明未测规格可用 |

手写TrainJob可用于定位环境，但**手写清单成功不等于ModelDev正式渲染已经正确**；报告必须说明是否经过同一渲染代码以及任何候选差异。现有正式CPU渲染拒绝GPU时不为探针放宽它，候选验证保持隔离并明确未启用。未具备硬件时只完成可做的层次，设备部分NOT_RUN/BLOCKED，不把CPU或假设备测试算GPU通过；CPU主线继续。

验证代码/渲染/编译在既定Fedora完成，控制器与设备行为在有相应能力的目标K8s验证；不猜Fedora具备GPU，不在本机或节点宿主机编译/裸跑负载。测试仅使用获准Namespace、资源和有限身份，不借用生产owner证书，不建立绕过BFF的常驻业务入口；日志脱敏，清理只针对明确归属且可安全清理的资源。本文不自动授权安装驱动、升级共享控制器或执行设备占用。

#### 10.2.5 两份小产物，不新增GPU管理平台

差异表记录核对commit、既有合同字段/方法、当前足够项、待实现项、负责仓库与CPU兼容结论；验证记录标注`scope=GPU_PRECHECK`、执行层次、代码/材料版本、真实对象与证据、未测项及下一步。沿用现有文档和测试记录位置，不要求新建表、状态服务、GP用例体系或独立执行引擎。

本轮接缝说明随W1/W3记录即可；隔离验证按条件独立排期。**GPU前置NOT_RUN/BLOCKED不阻塞本版CPU验收；前置通过也不能声称GPU正式业务已交付。**发现同时影响本版CPU的确切缺陷，作为原任务缺陷修复并回归；仅GPU专属缺口交下一批，不借此扩大v4.2范围。

<!-- source-section: D11.5; source-sha256: 158d0da916856adbc00599116cd92ea783ce3f8486c4304fdee245082ea61f48 -->
### 11.5 GPU前置证据独立，不增加本版验收前置

FT01–FT32、原12项细化、PA01–PA10、MR01–MR08及C01–C34映射继续保留，D11.2.3的3项登记细化是本次CPU正确性修订而非GPU验收；C33的CPU拒绝GPU继续必测。D10.2只定义可独立开展的接缝核对和准备，不将GPU渲染、硬件、owner注册或占退额纳入这些用例的通过条件。

已开展的GPU检查使用独立`scope=GPU_PRECHECK`记录，逐层保存结果；尚未开展或条件不具备，如实NOT_RUN/BLOCKED，不影响已满足D11.4的CPU交付判断。反之，GPU检查通过不抵扣任何FT/PA/MR，也不证明BFF→Gov→ACC→ModelDev业务已贯通。没有为GPU准备另加一组必须通过的GP用例，不把此准备视为Model真实接入的替代。

若GPU调查暴露了同时影响当前CPU正确性、隔离或关闭的真实缺陷，仍须按原受影响用例修复回归；不得利用“不阻塞”忽略本版本身的问题。纯GPU支持缺口则进入下一批，不更改本版成功/关闭判定和硬件前置。

<!-- source-section: D12.5; source-sha256: 05ffdaaca5b23127221302e3a1847064436b548cb604f9882ef991aa45f154c4 -->
### 12.5 W1/W3的接缝记录与独立GPU准备线

| 责任 | 本轮增加的有界说明/核对 | 不属于该包新增开发或验收 |
|---|---|---|
| W1 合同与受理 | 在既有合同工作中记录算法参数/执行资源分界、原快照兼容、Gov/ACC既有附件与查询接点；参见D01.2/D02.7 | 不提前发布GPU字段/枚举/表，不开放资源表单，不重做资源或身份系统 |
| W3 执行与关闭 | 记录实际训练容器渲染点、CPU限制所属合同、计算关闭持久事实；可协助D10.2独立试验 | 不改CPU配方用GPU，不在CPU路径增加GPU退款RPC，不因预留强制重构全部Trainer/CloseWorker |
| W6 故障与交接 | 保持C33和FT/PA回归，交接GPU差异/试验记录的引用及未测项 | 不给现有用例追加GPU必须通过条件，不等待GPU环境才完成本版 |
| GPU准备负责人＋ENV | 按D10.2协调Gov/ACC差异、ModelDev测试映射和Installer隔离环境；主线具备对应能力时即可并行 | 不是新增W7，不默认要求当前微调作者实现四仓生产接入；无硬件则停止设备层，其他工作继续 |

这是一条**独立准备线**，不是M0–M4的新前置。主开发线继续按12.3/12.4完成CPU微调和当前参数链；接缝核对发现现有结构已经足够，可以只交记录，不以代码行数衡量完成。生产GPU正式能力待下一批确定范围和验收后再合入启用；局部试验不得修改其他agent的工作树或共享资源。

最小移交为D10.2.3差异表及已有条件下的分层验证记录。文档由单一`spec.md`派生的`gpu-extension-boundary.md`供跨仓阅读，不另外维护一份可独立修改的GPU规范。测试结果只作为证据另存，不反向把文档计划标成运行PASS。

<!-- source-section: D13.1; source-sha256: 4b5d5be35e31efeafec649450efae846e8a76998a790562a58a721fd75f7abea -->
### 13.1 仅影响GPU准备线的待核实项

Gov/ACC实际合同版本、ModelDev owner/API差异、Trainer到Pod的具体字段、GPU硬件/驱动/插件条件和隔离测试授权，只是D10.2相应GPU准备动作的前置，**不追加到本版CPU启用与最终验收的必需事实表**。未知时记录负责方、已尝试动作和恢复条件；不能猜字段/硬件、借用他人凭据，或把全部CPU工作标为BLOCKED。


## 来源与证据口径

本稿直接基于用户上传的**完整v4.2.1包**，按两轮审核/讨论改写登记派发仲裁、主链优先顺序和组合测试，见S11/S12。未请求重新调研技术选型或最新代码，因此本次不借外部资料补写未核实实现。历史S0–S10保留其版本来源；本次新增是设计修订，不是当前分支/硬件的审计事实。无业务源码修改、依赖发布、Fedora训练、K8s/Model/Inference/GPU验收；文档检查不写成业务PASS。

### S0 · 用户附件《ANI CPU 受管微调方案 v3》

沿用原v3附件；内容包括单TrainJob、KFP export/verify、原受理/发布/关闭复用、Model需求、资源预算及原验收映射。原文副本位于 `reference/spec-v3.md`。其中指标适配职责是v4.1指标意见的原依据，不与本次421-R2候选BFF排期混同；已被v4收窄的CSV等范围不恢复。

### S1 · 模型研发平台 v0.4

《精简业务与架构方案》《精简开发实施说明》《管理与操作指导手册》是平台范围背景；v4形成时重点核对开发册D16的C01–C34，本次保留其原含义及已明确的覆盖边界。v0.4不是所有内容均已实现的报告。v0.2/v0.4已含target_model_id/new_model_name两个目标分支。

### S2 · Governance统一调用合同（沿用v4固定引用）

仓库 `zhangzhe-ctrl/ani-governance`，固定commit `34388cf90d0834da76c9e5701ade5d92a248144f`，`docs/contracts/service-to-service-calls.md`，重点§1–3与§5–6。本规范涵盖三种调用方向，并明确规范入仓不代表存量实现全部符合。本次沿用v4已经固定的合同引用，未重新拉取当前分支，不宣布实现已全部对齐。

固定来源：<https://github.com/zhangzhe-ctrl/ani-governance/blob/34388cf90d0834da76c9e5701ade5d92a248144f/docs/contracts/service-to-service-calls.md>

### S3 · ModelDev已有机制（沿用前轮固定源码核查）

仓库 `zhangzhe-ctrl/ani-modeldev-service`，固定commit `5b7fd05d9a74a8d0c65577e71b9a5c2db5dc8163`。主要位置：`internal/component/io.go`、`runner.go`、`internal/biz/execution_state.go`、`execution_closer.go`、`managed_runtime.go`、`internal/data/runtimeproof/proof.go`、`tasks.go`、`owner_close.go`、`internal/data/workspace/pipeline_io.go`、`pipelines/cpu_p01.py`、`README.md`。

固定来源：<https://github.com/zhangzhe-ctrl/ani-modeldev-service/tree/5b7fd05d9a74a8d0c65577e71b9a5c2db5dc8163>

本轮没有重审所有文件或当前默认分支，也没有复跑其测试。涉及正常关闭、Task核验、状态、Fedora职责的事实沿用前轮已读取源码，不作为新增微调已通过的证明。

### S4 · Model接口与实现（沿用前轮固定源码核查）

仓库 `zhangzhe-ctrl/ani-model-service`，固定commit `6473f1ec1386067e68dbd1294c10c660da7d1e7b`；`api/model/v1/model.proto`、`internal/service/model.go`、相关存储与身份实现。已存在方法不等于已满足本次可信接收、pending查证、固定引用和finalization要求；MR项以提供方交接及真实联调为准。

固定来源：<https://github.com/zhangzhe-ctrl/ani-model-service/tree/6473f1ec1386067e68dbd1294c10c660da7d1e7b>

### S5 · Model身份和名称唯一性（沿用前轮固定源码核查）

同S4：`migrations/000001_model_control_plane.sql`、`000006_external_model_id.sql`、`internal/data/postgres/model_store.go`、`queries/model.sql`及service错误映射。源码定义tenant内name唯一和外部model_id映射；本轮未连接实际库检查迁移状态。

### S6 · 本轮及紧邻对话的已确认决定

采用Gov统一S2S合同；Model需求先行、留接缝后对接；新模型/已有目标两分支；Alpaca JSON单格式；复用状态、deadline和正常关闭；不将审核澄清扩大为新系统。用户已有Fedora编译机，本机只编码/Git，172.16.101.10～12是目标集群。未提供或尚未核实的本任务具体环境值按D13处理。

### S7 · v4.1历史直接输入：v4与三项评审

v4原件在`reference/spec-v4.md`，v4.1的三项原评审在`reference/review-input-v4.1.md`。启动/停止仲裁、指标职责、单次执行与方案完成层级已在v4.1并入，本版继续保留D14.2对应正文，不因参数开放重写它们。

### S8 · v4.2的历史底稿：用户上传v4.1完整计划包

原包名：`ANI-CPU受管微调-v4.1-完整计划与复核包(1).zip`。其完整spec SHA256为`4c8430fc90ed92671ad06120113a909030ee76d70f807074790c5633f0b0de77`，本包按原字节保留`reference/spec-v4.1.md`。旧W0–W6是其派生视图，本版从新spec重新派生，不复制旧视图里已过时规则。

### S9 · v4.2参数审核依据与历史代码引用边界

`reference/parameter-review-v4.2.md`保留当时《ANI 微调v4.1：面向下一阶段参数开放的评审要点》原文；它是v4.2的历史依据，不是本次重新审核。P-01–P-05及W6六类验收已在D14.3映射；A07.4、D02.5、D05.3、D11.2.2为落实意见而新增的明确设计选择，不宣称来自原包逐字条款。

上一轮参数代码核对固定于ModelDev `c5fd93c60f4aa6af229ef39d3cc503396f6d0ffb`，涉及`contract/cpup01/intent.go`、`release.go`、`snapshot.go`，`internal/biz/admission_resolution.go`、`internal/service/materials.go`及`api/ani/modeldev/v1/modeldev.proto`。本轮沿用该审核事实，没有重新拉取/执行最新代码；它与S3较早的执行机制基线分开记录。固定来源：<https://github.com/zhangzhe-ctrl/ani-modeldev-service/tree/c5fd93c60f4aa6af229ef39d3cc503396f6d0ffb>。

本轮仅文档生成与一致性/呈现检查；无业务源码、Git、Model发布、Fedora训练、K8s部署或数据库迁移。文档PASS标DOCUMENT_ONLY，不写入FT、PA或MR运行结果。源材料未提供的实际端口/密钥/运行版本/安全范围不猜填。

### S10 · v4.2.1所用底稿与当时确认的GPU分期边界

底稿为已交付的`ANI-CPU受管微调-v4.2-参数扩展完整计划包.zip`，原spec按字节保留为`reference/spec-v4.2.md`，SHA256：`22e3c2af2424e4a2e7ef158171922979500229ffba2cf35558e701244a4af16b`。本轮用户先确认“GPU正式功能紧接CPU主干，合同与关键适配提前核对”，随后明确要求把“ModelDev公共接入位置/测试、v4.2小段边界说明”加入方案。

本次落实的原意是：Gov/ACC沿用既有规范，只核对ModelDev具体接入；ModelDev/Installer按条件做隔离渲染与设备前置验证；不强迫多仓当前启用GPU，不给CPU加假charge，不放松CPU限制，GPU未就绪不阻塞CPU微调验收。生成文档不是对远程写入、共享环境变更或设备占用的新授权。

Gov/ACC具体合同文档、接口字段、owner与证书配置由GPU准备负责人读取其正式仓库并记录确切commit；本轮不据旧讨论宣告当前实现已支持或尚不支持某RPC。`gpu-extension-boundary.md`只切片本规范，不成为新的GPU协议权威。


### S11 · 直接底稿v4.2.1与本次审核意见

原上传包：`ANI-CPU受管微调-v4.2.1-GPU后续接入边界完整计划包(1).zip`，SHA256 `5850581e31c29eaaaa8369751ca550302c06757dc3fea8608844701147713e25`。权威spec为1375行、SHA256 `363016708aef1c22454cbd320effead34b952aadf2bad08ea3a3582dc9e4c8b1`，按字节保留`reference/spec-v4.2.1.md`。审核意见原件保留`reference/review-v4.2.1.md`：R1具体登记派发仲裁；R2后续候选经BFF验证的操作说明；R3组合/默认黄金向量。本次未重审GitHub最新实现，不能把这些方案缺口说成已复现生产故障。

### S12 · 用户随后确认的执行顺序与本轮修订

用户明确“现在先完成微调主流程，真实BFF验收放到后面”，并要求据两轮讨论优化整包。落实为A12/D11.6/D12.6–7：合同先定；隔离应用层测试替代上游来件，复用实际后端主链；真实BFF/前端和候选绑定后置但最终仍验收；Model按原分期接入，不伪造READY；GPU独立准备继续不阻塞CPU。`review/review-input.md`记录这组输入摘要及原审核引用，不虚构对话原件或新增执行授权。

本次仅在对话工作目录编辑/生成/复核文档，没有连接用户本机、Fedora或K8s。最终代码/API编号、实际测试驱动方法、环境/凭据和有效参数批准仍需按任务验证并记录。本包全部业务计划初始NOT_RUN；历史已填记录应另存保留，不被新模板覆盖。

# ANI CPU 受管微调方案 v4：单格式、单训练作业、继承公共机制

版本：微调方案 **v4**；平台基线：**v0.4**；日期：2026-10-13。状态：`DESIGN_UPDATED`。本轮仅修订文档，新增代码、Model接入、负载与目标环境验收均不因本文存在而通过。

本文件为完整可维护正文。业务/架构HTML、开发HTML和Model需求稿均由本文件派生，禁止多份独立编辑。采用本稿前保留v3历史副本；权威仓库路径及执行环境见D12。本版已将最新确认的改动写入正文，不要求读者另找补丁覆盖旧段落。

阅读分册：A01–A12为业务、职责与流程；D01–D14为开发落点、接口、资源和验收。


## A01 · 术语：先分清对象、动作和成功事实

本方案编号是 **微调方案 v4**，所沿用的平台范围基线是 **模型研发平台 v0.4**。两者不是同一版本轴。本文修订附件《ANI CPU 受管微调方案 v3》，不将历史平台文档全部升级或重写。[S0][S1]

| 术语 | 本文中的唯一含义 | 不能混同为 |
|---|---|---|
| 通用训练 / CPU-P01 | 已有单节点 CPU MLP 训练主干及其受理、发布、查询、停止机制 | 所有框架、所有拓扑均已支持 |
| 本次微调 | 固定基模上的 CPU、单节点单进程 LoRA SFT | 预训练、QLoRA、多机训练或效果评测 |
| Alpaca | 本次采用的指令样本结构；首期只接受 instruction/input/output 三字段子集 | JSON/CSV 这种文件编码格式 |
| JSON | UTF-8 文件编码与结构载体；本期为一个数组 | ShareGPT 或 Alpaca 的业务语义 |
| 数据 READY | 固定文件已授权、已验证，内容与登记事实一致，可供受理选择 | 已经训练、质量评分通过或效果良好 |
| 基模版本 | 开始训练时读取的确切 ModelVersion 和固定权重 | 本次结果要存入的目标模型 |
| 目标模型 | 自有 Model 中组织多个输出版本的模型容器 | 基模、某一个模型版本、Kubernetes 容器 |
| 模型 UUID / 版本 UUID | 分别标识模型容器与其中一个具体版本 | 一个对象被要求重复生成几套身份 |
| 外部 model_id | Model 已有的对外业务标识，可能不是 UUID；仅在 adapter 边界解释 | 一律等同于内部模型 UUID |
| execution_id | 一次业务执行；关联原受理、输入、实际流程与结果 | ModelVersion ID、KFP Run ID |
| 预设 / Release | 预设是业务入口；Release 是平台锁定并验证的实现与配置组合 | 用户自行选择的功能套餐版本 |
| PipelineVersion / Run | 前者是 KFP 流程实现版本；后者是该流程的一次执行 | Trainer 作业 |
| KFP Task / 组件 Pod | 流程中某一步的执行及其容器；本次 export、verify 在此运行 | 独立微服务或额外 TrainJob |
| Trainer / TrainJob | 独立训练控制器及其一次作业资源；本次只有训练阶段使用一个 TrainJob | KFP 的所有步骤或第二套业务编排 |
| Adapter / merged | LoRA 增量权重 / 合并后的完整 HF 模型文件集 | 两者都能直接按同一种推理接口使用 |
| verify | 在独立进程加载合并模型并做固定短推理的可用性检查 | 模型效果、准确率或业务质量评测 |
| publication / PUBLISHED | ModelDev 确认真实文件已经持久发布到受管对象存储的事实 | Model 已登记 READY |
| registration / READY | 本次产物按原目标登记为确切 ModelVersion，字段核对通过 | 计算已经关闭、原 KFP Run 被改写为成功 |
| close / CLOSED | 停止创建并确认本次实际计算已结束 | 删除 PVC、删除模型、登记成功 |
| 合同测试 / 真实联调 | 测试边界结构和错误语义 / 实际双方服务、身份与持久化链路验证 | 替身通过即真实依赖通过 |

**“声明了一个事实”“取得了真实证明”“对外展示了一个标签”是三件事。**数据、训练、发布、登记和关闭分别判断；UI 或 API 的整体标签只解释这些事实，不成为第二份权威状态。

## A02 · 范围与明确决定：本次交付什么、不交付什么

| 项目 | v4 的决定 |
|---|---|
| 业务入口 | 沿用现有 BFF/Governance；不增加第二个用户入口 |
| 新服务数量 | 不新增常驻服务；在已有 ani-modeldev-service 及相关领域服务内增量实现 |
| 训练方式 | CPU、单节点单进程、LoRA SFT；一个 Trainer TrainJob |
| 基模 | 本租户已批准且受保护的 Qwen2.5-0.5B-Instruct 固定版本；不是 Qwen2-0.5B，也不是任意最新版本 |
| 文件输入 | **仅受管 S3 中的 Alpaca JSON 数组**，原文件 ≤32 MiB；不交付浏览器/本机上传 |
| 数据结构 | 三个必填字符串 instruction/input/output；instruction/output 非空，input 可为空字符串；不接受其他字段 |
| 样本规模 | 首个启用预设要求64条；这是预设条件，不是所有 Alpaca 导入的全局限制 |
| 可调项 | 接口仅保留 learning_rate、max_steps；实际可用值进一步受已测试 Release 限制 |
| 数据暂缓 | 指令型 CSV、JSONL、ShareGPT、自定义字段映射、history/system/tools 扩展 |
| 原通用训练 CSV | 保持 CPU-P01 的数值特征 CSV 原语义；不因微调增加而改变 |
| 模型交付 | 合并 HF 未压缩 tar + 独立 Adapter 工件 + 必要运行报告；目标为自有 Model 中的模型版本 |
| Model 开发顺序 | 先提出需求并留调用接缝，提供方正式就绪后再接真实 adapter；不能以此宣布完整微调已交付 |
| 状态和关闭 | 复用 CPU-P01 公共机制；只增加微调阶段、文件和登记事实 |
| 推理验收 | 复用现有 Inference/KServe 路径独立使用本次新包；不在训练流水线自动上线服务 |
| 页面与管理 | 复用已有查询/操作入口；本包不承诺新建完整页面、自助环境开通或发布管理产品 |
| 不在本次 | GPU/QLoRA、分布式、任意脚本、清洗、效果评测、Hub、自动调参、Direct 双后端、多个 TrainJob、通用 grant/登记/阶段调度系统 |

这些是对 v3 的明确收敛：CSV 从“本版交付”改为“暂缓”；Model 从“所有开发同步完成的前置”改为“对应真实调用与最终验收的前置”。模型交付目标、一个 TrainJob、已有正常关闭语义不变。[S0]

**不把“Alpaca 三字段子集”说成 Alpaca 的全部能力。**本期拒绝尚未支持的内容，不能接受后再静默丢弃。暂缓能力没有隐藏开关，不为了将来支持先建立通用插件框架。

## A03 · 完整主链与两个验收边界

```text
管理员把 JSON 放到已批准 S3 范围
  → 受控导入：固定对象、校验内容、登记 READY

用户经现有 BFF 提交微调
  → 当前用户/租户/权限校验
  → 原键重放检查；新请求解析并冻结 Release、输入、基模、目标
  → Gov 持久受理与可靠投递
  → ModelDev 持久 inbox，返回受理 ACK
  → 提交并绑定权威 KFP Run
      → workspace-name / CreatePVC
      → prepare：原 JSON、固定基模、安全解包、准备清单
      → train-wait：由 ModelDev 创建并观察唯一 TrainJob
      → export：独立 KFP Pod 合并本次 Adapter
      → verify：独立 KFP Pod 加载 merged 并短推理
      → collect：检查文件全集及打包选择
      → publish：上传候选文件和主模型 tar
      → register：先独立确认 publication，再调用 Model 登记
      → close：提交 NATURAL_TERMINAL，取得有效持久收据后返回
  → KFP 自然完成
  → 独立 owner/CloseWorker 确认计算结束，记录 CLOSED

另行执行验收：经授权从对象存储取新包，无训练 PVC，使用现有 Inference 实际调用
```

**正常业务链不能由管理员逐次手工串接。**管理员负责预设、镜像、基模、数据导入和例外处置；每次正常提交后的计算、发布、登记与关闭由程序自动执行。

| 边界 | 能证明什么 | 不能宣布什么 |
|---|---|---|
| Model 未正式就绪时的开发/分段验证 | 真实训练程序可运行，ModelDev 模块、发布与关闭可开发和验证；Model 接口结构可测试 | 已有可信 Model 登记；已完成用户完整微调闭环 |
| 完整业务交付 | 真实训练/导出/verify，固定远端 publication，Model 原版本 READY，最终 CLOSED，并独立使用新包 | 未做的 CSV/上传/多框架/GPU 同时已支持 |

**Model 未接入时不增加一个永久的“只训练不登记”公共微调模式。**可做内部负载或分段测试；公共 FINETUNING 的完整能力保持未启用。已受理后 Model 临时故障则走原登记失败/恢复路径，不抹掉已发布结果。见 A09。

## A04 · 职责与统一服务间调用：不另造协议

**Gov 已固定的 `docs/contracts/service-to-service-calls.md` 是内部业务 RPC 的通用规范。**本文只填写微调的具体调用关系与领域内容，不重新定义身份、委托、传输、重试或授权平台。[S2]

| 责任方 | 负责 | 不负责 |
|---|---|---|
| Governance/BFF | 当前用户认证；租户映射；模块/action/数据范围；原意图重放；Release 绑定；持久受理与投递 | 搬运权重、推进 KFP DAG、直接写 ModelDev/Model 数据库 |
| ModelDev | 本域校验与持久事实；权威 Run；唯一 TrainJob owner；准备/产物/Model adapter；执行观察与关闭 | 重新实现 IAM、另建工作流引擎或登记中心 |
| KFP | 正常步骤依赖、Task 执行、原始 Run/Task 与产物关系 | 决定用户权限或作为 Model 数据库写入者 |
| Trainer | 单 TrainJob 的实际训练工作负载 | 承担本次 export/verify，或替代 KFP 的完整流程 |
| Model | 基模/目标业务权威；固定模型引用；新建/版本登记；重放查证、finalization、删除保护 | 从普通 body actor/tenant 直接推断可信用户授权 |
| Installer/环境负责人 | 既定版本、Runtime、CPU池、存储、证书、网络和运行身份 | 代替每次业务创建 TrainJob 或伪造环境/业务通过 |

### 4.1 本次仅登记这些关系

| 调用 | 目的 | 信任来源 |
|---|---|---|
| Gov → ModelDev | 预检、持久命令、当前查询/恢复 | Gov 已认证主体及公布的调用合同 |
| ModelDev → Model：受理前 | 只读解析固定基模、目标与名称冲突 | 已验证 Gov 委托及其范围；不要求尚未形成的 execution spec |
| ModelDev → Model：受理后 | 读取已批准版本；按原产物/原目标登记；查证原操作 | 可信受理的持久事实；以 ModelDev 服务身份调用 |
| 用户恢复 → Gov → ModelDev → Model | 原登记的明确恢复 | 当前权限 + 原持久登记事实；不能更换目标/内容 |

每条实际关系补齐“调用方/接收方精确服务身份、完整 RPC、允许用途、租户来源、资源范围、幂等键、超时”。复用接收方公布的固定 API 模块；不复制生成代码、不跨仓导入 internal、不用本地 replace 冒充正式发布。参考代码不是本期新建共享运行时的理由。[S2]

### 4.2 两类既有执行协议不要混淆

ModelDev → KFP 的受保护可信身份头，以及 KFP 受管步骤 → ModelDev 的既有 workload/TokenReview 证明，是已有执行接入边界；不是让训练 Pod 冒充 Gov。其既有 listener/验证实现仅按新增步骤精确适配，不以本次微调名义重做全平台证书或入口迁移。

ModelDev → Model 等内部业务 RPC 必须按 Gov 规范接入已有业务 mTLS listener。KFP 组件不获取 Model 管理证书，训练程序不获取控制面凭据。缺依赖时明确未启用或失败，不换普通 listener、不回退明文、不跳过 TLS。

## A05 · 输入与基模：一种结构、固定内容、不同批准条件

### 5.1 本版唯一微调输入

文件是 **UTF-8、无 BOM 的一个 JSON 数组**。数组非空；每个元素必须是对象，且恰好有 instruction、input、output 三个键，值均为字符串。instruction/output 长度至少为1，input 可以为 `""`。不裁剪、改写或过滤字符串；本版不额外做空白文本质量判定。重复键、未知键、null、非字符串、无效 UTF-8、JSONL、CSV、消息列表均拒绝。

```json
[
  {"instruction":"概括下面的内容。","input":"上午整理数据，下午完成训练测试。","output":"今天完成了数据整理和训练测试。"},
  {"instruction":"写一句欢迎语。","input":"","output":"欢迎加入团队。"}
]
```

上例只说明结构，不是64条验收数据。首个微调 Release 对 READY 数据额外要求 `row_count=64`；导入器负责文件格式、固定性与统计，不把64条写成所有 Alpaca 数据的永久限制。

**本版单格式物化明确选择“保留原字节”，不是多格式转换。**JSON 经固化和验证后，prepare 按同一对象版本再次核验并原样写到 `inputs/data/finetune_data.json`。不重排键、不重编码转义、不另建 normalization 版本生命周期。`dataset_info.json` 是平台按锁定模板生成的独立配置，单独记录其摘要。D03 给出开发规则。

这是 v4 为减少单格式实现歧义作出的具体实现澄清：删除 v3 的 CSV→JSON 转换及源/派生双数据模型；原文件及工作区文件 SHA 必须相同。将来加格式转换时再发布明确的新 schema/Release，不影响现有记录。

### 5.2 基模与输出模型各自固定

基模必须为本租户已批准、未加密、受保护的 Qwen2.5-0.5B-Instruct 精确版本，并有完整存储引用、归档 SHA、大小、format 和预期文件集。批准名称不替代批准版本。

`target_model_id` 只决定结果进入哪个模型容器，**不会把该模型的最近版本自动变成基模**。当前只允许固定批准基模；使用一次微调的输出再继续训练，是未来另行批准的输入范围。

导入 READY、Model READY 和 ModelDev PUBLISHED 是不同领域事实；均不能靠管理员直接改数据库产生。

## A06 · 模型目标、对象身份与同名规则

### 6.1 两个目标分支，与最初平台方案一致

| 请求 | 唯一含义 |
|---|---|
| `new_model_name` | **create-only**：本次新建模型容器，再创建一个输出版本 |
| `target_model_id` | 给已存在、已授权可写的模型容器增加本次输出版本 |

两字段必须且只能出现一个。不按名称在登记时自动切换分支，不新建模型身份映射中心。[S0][S1]

本次新增微调请求中的 `target_model_id` 明确使用 **Model 返回的内部模型 UUID（Model.id）**；Model 旧 API 允许的外部别名不因此改变。适配器读回 Model.id 后校验固定目标，调用 Model 时也使用该 UUID。若已有消费者已经发布了不同的该字段合同，先登记冲突再对齐，不静默重解释。

### 6.2 同一操作重放与另一轮训练不同

```text
执行 A：new_model_name = cpu-ft-demo
  → 以 md-container:A 创建模型 M
  → 在 M 下以 md-A 登记版本 exec-A

A 的网络重试
  → 原 key / 原内容，找回同一个 M 和 exec-A

执行 B：target_model_id = M
  → 不调用 CreateModel
  → 在 M 下以 md-B 登记版本 exec-B

执行 C：new_model_name = cpu-ft-demo（名称已存在）
  → 名称冲突；调用者应新建请求，明确选择 target_model_id 或另一个名称
```

基线 Model 迁移已有 `UNIQUE(tenant_id,name)`，外部标识也有相应唯一约束；是否已在目标库应用，需要部署交接确认。本设计沿用该名称语义，不允许同租户同名堆积。[S5]

新受理时尽早检查名称冲突；**预检不占名**。最终创建仍由 Model 唯一约束判定。受理后出现并发同名竞争时，登记报冲突并保留产物、关闭计算；不自动换目标/名称/key。原执行重放必须先按原键查回，不能被自己的已建名称误拒绝。

若必须给已失败执行改投另一目标，本版没有该操作；管理员不能通过改库或重试参数暗中实现。普通用户改目标要新建业务请求，并承认那不是“原登记恢复”。

### 6.3 不增加多余 UUID

模型 UUID、版本 UUID、execution_id 分别是模型容器、具体版本、来源执行。外部 model_id 可能是普通字符串；version 标签 `exec-<execution_id>` 和登记键也不是额外模型对象。ModelDev 只保存必要关联与收据；不再给同一个 Model 容器生成另一套本域身份。

## A07 · 平台版本管理、参数范围与旧 CPU 兼容

用户选择预设和参数，平台解析当前**正式启用且适配**的 Release；不是自动使用上游最新组件，也不是版本号最大即启用。原受理固定之后，不因配置切换、重新投递或服务重启改变基模、数据、镜像、Runtime、目标或参数。

新请求先完成当前身份与动作检查，再查原键。原键命中按旧事实重放；只有新请求才解析当前绑定。新增暂停不妨碍合法原键查询/重放和可信关闭；新用户动作仍按当前权限处理。

| 参数 | 请求级硬边界 | 首个启用范围 |
|---|---|---|
| learning_rate | 无指数、有限精度的十进制字符串；数值 (0,1]，默认0.0001；不经二进制浮点求规范摘要 | 由实测 Release 明确；仅完成固定配方验证时只开放0.0001 |
| max_steps | JSON 整数，硬边界1–200，默认20；拒绝小数/截断/字符串冒充整数 | 仅完成20步验证时只开放20；允许1–200之前需完成边界与预算测试 |
| 样本数 | 来自 READY 的实际 row_count，不信任用户自行声明 | 当前预设要求64，预处理不得静默丢样本 |
| 其他训练设置 | 不作为用户自由字段 | 由锁定配方、镜像和已验证参数向量决定 |

“API 硬边界”不代表该范围已安全启用。Release 未验证的值明确拒绝。学习率具体十进制词法与存在性编码要在原合同维护点固定向量；沿用现有规范化规则，不在各服务分别四舍五入或解析。

旧 CPU kind、规范字节、摘要、枚举编号、错误和拒绝集合必须保持；微调字段在旧 CPU 编码中真正省略，不写入零值。共享机制的必要缺陷修复单列原因及相关回归，不以微调为由进行无关重构。

## A08 · 每一步完成什么：训练、Task 和 Run 分层判断

| 步骤 | 输入/动作 | 成功事实与后继条件 | 失败时 |
|---|---|---|---|
| prepare | 固定 JSON、固定基模；安全物化；生成准备清单 | 原字节/基模完整，原 PVC UID、spec 与清单一致 | 不启动训练；保存诊断并进入关闭 |
| train-wait | 请求 ModelDev 唯一 Trainer adapter；等实际训练 | 原 TrainJob 成功，真实优化步数/Adapter，训练写者已退出 | 不导出；不得以创建成功代替训练成功 |
| export | 只读取固定基模及本次 Adapter；同步运行导出 | export Task/Pod 成功退出，merged 完整且可关联本次 Adapter | 不 verify/登记；留下失败阶段 |
| verify | 独立进程读 merged；greedy 固定短输入，最多16 token | verify Task/Pod 成功，实际加载/生成和摘要报告完整 | 不交付 READY 模型；不是效果不佳判定 |
| collect | 读取原执行所有规定输出 | 精确文件全集、摘要和 in_tar 规则通过 | 不继续上传；保留唯一输出 |
| publish | 捕获文件到磁盘 spool，上传候选 | 返回原对象 candidate；**尚不能自证 PUBLISHED** | 保留输出，关闭计算 |
| register：前半段 | 核验前序 publish Task/Pod 与固定远端对象 | 事务持久 publication 后，才允许调用 Model | 未确认不得调用 Model |
| register：后半段 | 原目标、原 key、原 tar 调 Model 并查证 | 原 ModelVersion READY 且字段匹配，持久登记结果 | 保留 PUBLISHED，不重训；有界查证/恢复 |
| close（正常） | 请求 NATURAL_TERMINAL | 有效持久 CLOSING 收据后返回，让 Run 自然完成 | 独立 owner 继续对账，不假 CLOSED |
| owner/CloseWorker | 独立观察整个执行及计算写者 | Run/Workflow 终结与实际无写者，CLOSED | UNKNOWN/NEEDS_REVIEW，保留停止限制 |

**前序成功检查只针对前序 Task/原 TrainJob，绝不要求整个 Run 已结束。**否则后继任务会等待自己尚未完成的 Run。最终 CLOSED 才检查整个 Run/Workflow。普通通用训练已有“close 核验前序 publish”范例，本次按执行类型扩展到 register，不新建通用阶段引擎。[S3]

KFP 决定正常下一步。查询、配置读取、结果上报的重放不能重新拉起训练/export/verify；正常计算禁自动重试。需要重训时新建 execution；原结果不明先查原资源。

## A09 · Model 未就绪时如何开发，何时才可启用

**Model 不是所有微调编码的开工前置，但是真实基模调用、真实模型登记及完整交付的前置。**保留开发接缝，不降级业务成功标准。

| 阶段 | 可以做 | 必须标明 |
|---|---|---|
| 接口需求先行 | ModelDev 领域端口、输入输出约定、原登记记录、显式错误、测试替身；Model 提供方评估已有 API | 仅结构/行为合同测试；不是 Model 权限/持久化通过 |
| Model 未接收方就绪 | 在 Fedora 验证固定真实负载；在具备条件的隔离环境验证 ModelDev 子链、存储和关闭 | 基模素材需来自已批准材料；替身边界和绕开的依赖逐项记录 |
| 读取能力先就绪 | 真实接入基模/目标只读解析，验证身份和固定内容；登记继续独立开发 | 读取通过不等于写登记通过 |
| Model 完整就绪 | 发布正式 API；ModelDev 固定消费版本；验证真实读写、重放与失败窗口 | 对应代码/服务/数据库不替身，独立合同验收 |
| 微调正式启用 | 相关能力与目标环境全部满足，配置启用对应 Release | 一次提交自动完成完整交付；不靠管理员每次补登记 |

公共 FINETUNING 新受理只在**本 Release 必需依赖**就绪时启用。Model 可选装配缺失不影响原 CPU 的已支持功能，但微调返回明确不可用；不能接受后明知无 Model 仍承诺自动登记。

已受理后临时失联不改变原快照。到预算后保存登记失败/UNKNOWN并关闭计算；在原产物仍存在且已 CLOSED 时，用户可经当前授权触发原登记恢复。它不是新的交付模式，也不是按名字重新发现目标。

具体对接需求集中在 D08，并派生为单独的 Model 需求交接稿；**派生稿不是另一份可以独立修改的合同**。不先实现所有备选 API；优先复用已公布方法，只有不能表达必要查证时才由 Model 补一个窄接口。

## A10 · 对外结果：复用事实投影，不创建第二个状态引擎

当前通用训练已有 compute/delivery/close/resource 分离机制。本次沿用其枚举含义，增加微调 export/verify 和 registration 必要事实；`current_step` 反映实际步骤，不负责调度。[S3]

**下表是展示判定规则，不是新增的数据库权威状态。**代码枚举和 RPC wire 通过现有合同映射，不因本表中文标签重新生成一套全局状态机。

| 已验证事实 | 应当展示 | 不得展示 |
|---|---|---|
| 训练已成功，export 正在运行 | 处理中：导出模型；训练已完成 | 完整微调成功 |
| 训练成功，verify 失败，关闭完成 | 失败：模型加载校验失败；计算已关闭 | 登记成功、已评测通过 |
| publication 已确认，登记失败，CLOSED | 已发布，登记失败/待原登记恢复 | 把 PUBLISHED 清空，或把训练改成失败 |
| Model READY，但 close=CLOSING | 模型已登记，计算关闭中 | 全部完成 |
| Model READY，但 close=NEEDS_REVIEW | 模型已登记，计算关闭待核实 | 全部完成、可以清理工作区 |
| 原登记恢复成功，CLOSED | 交付完成；显示恢复记录 | 改写原 KFP Run 的历史失败结果 |
| 用户停止，Model 写入在途后返回成功 | 保留“用户停止”与“已确认模型版本”两项事实 | 假装 Model 从未执行或自动删掉版本 |
| Model 未配置且无新受理 | 功能未启用/依赖未接入 | 一个虚构的已成功任务 |

完整交付成立条件：**真实训练结果 + export 成功 + verify 成功 + 持久 publication + 原 ModelVersion READY且一致 + CLOSED**。raw KFP/Task 状态和历史错误保留；READY不能覆盖停止意图，停止意图也不能抹掉已发生的 Model 写入。

在存在停止意图、明确步骤失败或关闭待核实时，展示必须优先显露这一事实；不能仅凭 compute=SUCCEEDED 显示绿色整体成功。原 CPU 没有 Model 登记，不要求给其添加 registration 才能判定原交付。

## A11 · 正常关闭、异常停止与产物保留

### 11.1 正常路径：继承现有实现

```text
register 成功
  → close 请求 NATURAL_TERMINAL
  → owner 持久化关闭意图并返回有效收据
  → close 组件返回，不等待自己退出
  → KFP/Argo 自然结束
  → 独立 CloseWorker 核验全部计算结束
  → CLOSED
```

正常 close **不主动终止一个正在正常收尾的 Run**。退出 finalizer 是幂等兜底：已有正常/用户停止原因不能被无条件改写成 STEP_FAILED；未建立正常关闭事实且前序失败时，按原失败路径请求关闭。[S3]

### 11.2 用户停止、deadline、真实失败

先持久保存停止/关闭标志，禁止新的业务计算及正常登记副作用；随后停止 KFP 与唯一 TrainJob，核对在途创建、原 UID、控制器及全部实际写者。新增 export/verify 的子进程必须同步结束或保持待核实。

“关闭标志生效”“发送停止请求”“实际无计算写者”不等价。NotFound、请求超时或 KFP Failed 不能独自证明 CLOSED。继续复用现有确定性命名、UID历史、结果不明对账和关闭条件；发现共性缺陷时单列修复及反例，不删安全检查来通过测试。

关闭 worker 使用独立的有限清理时限，可以在原计算 deadline 之后继续**观察/停止**，不能继续训练、导出或正常登记。无法证明时 NEEDS_REVIEW，不无限循环重启程序。对已在途 Model 调用只保存/查证事实；副作用重发遵守原登记恢复规则。

### 11.3 清理不等于关闭

CLOSED 不自动删 PVC。唯一未发布输出、未知写者、未确认创建、未解决关闭均阻止相关清理。已登记模型、READY输入与批准基模的保护由各自权威服务落实到删除入口，不只是隐藏按钮。

首期固定基模应在批准给 Release 使用之前建立 Model 侧保护；删除与保护检查在其本域一致实现，不增加跨服务 TCC。已有 READY/登记事实对应保护要保持一致。安全解除合同未实现时受保护对象先禁止删除。

## A12 · 开工顺序、人工职责和交付声明

| 阶段 | 交付目标 | 外部依赖未就绪时 |
|---|---|---|
| M0 负载锁定 | Fedora 真正20步训练、合并、独立加载；确定全部文件/精度/资源 | 使用获准固定素材；只证明负载，不假造业务授权通过 |
| M1-A ModelDev增量 | JSON READY、受理合同、唯一 Trainer、export/verify、发布/关闭及查询；Model端口/请求/结果测试 | Model边界可用明确替身做合同测试，标 NOT_INTEGRATED；不据此正式启用 |
| M1-B Model正式对接 | 提供方正式 API、Gov统一调用关系、真实读取/登记/查证和持久化 | 只阻塞该项与最终交付，不阻塞 M0/M1-A |
| M2 相关故障与兼容 | 原键、同名竞争、停止/deadline、子进程、重启、登记丢响应/部分提交；旧 CPU 回归 | 对替身覆盖和真实覆盖分别标注 |
| M3 目标环境联合验收 | .10～.12 的 KFP/Trainer/工作区/S3/Model，真实新包独立使用 | 不用 Fedora 或历史 Run 替代目标证据 |
| M4 独立交接 | 非原作者按实际入口完成批准、启用、用户执行、原登记恢复和受控清理 | 只声明本版范围完成，不能声称全部平台v0.4用例通过 |

**构建环境明确保留 Fedora。**本机只编辑、阅读、Git提交推送和受控文件转运；Fedora 执行生成、编译、测试、Pipeline编译、镜像构建和模块/负载验证；172.16.101.10～12 所属 K8s 集群承担真实部署和联合验收。Fedora 不是这三个训练节点之一，不能将所有工作挪到集群。实际别名/路径按已批准记录，见 D12。

管理员管理预设/基模/镜像、输入代导入、启用/回退及失败例外；程序自动管理每次 Run、TrainJob、各步骤、发布与登记。仍无可用操作入口的内容标“待实现”，不能写一条虚构命令让人改数据库代劳。

本文是开发评审基线。**文档版本 v4 不代表新增代码、负载、Model接入或 LIVE 测试通过。**所有新验收初始为 NOT_RUN；可复用代码不等于复用测试结论。

## D01 · 实现分类与改动落点：复用不等于重写

下列“已有”是附件引用的 **ModelDev `5b7fd05…` 源码评审基线**，不是对当前分支或实际部署的全量确认。实施前读取仓库 AGENTS/README、任务进度和实际 commit；发现后来已完成的微调能力，先复用，不按本稿重复建设。[S0][S3]

| 类别 | 本次处理 | 已知相关位置/边界 |
|---|---|---|
| 继承并回归 | Gov持久受理、重放、Release冻结与可靠投递 | 既有BFF、ModelDev client、admission/inbox |
| 继承并回归 | 单TrainJob创建、UID、观察、停止、未知响应恢复 | `internal/data/trainer/`、`internal/biz/managed_runtime.go` |
| 继承并回归 | 正常/异常关闭、owner独立核验 | `internal/component/io.go`、`internal/biz/execution_closer.go` |
| 继承并回归 | 计算/发布/关闭独立事实与投影 | `internal/biz/execution_state.go` |
| 微调增量 | Alpaca JSON验证/READY、固定基模物化 | `managed_input/input_importer/managed_materials`、`internal/data/input/`、`objectstore/`、`workspace/` |
| 微调增量 | 固定LoRA入口、export、verify，步骤角色及结果 | `internal/component/runner.go`、受管step入口、`runtimeproof/tasks.go`、`proof.go`、`owner_close.go` |
| 微调增量 | 多级安全文件、HF全集、tar选择、大文件预算 | `workspace/collector`、`pipeline_io.go`、对象验证器 |
| 微调增量 | register调用原publication确认，保留CPU由close确认 | `ManagedRuntime.Publish`、`VerifyPublication` 等真实角色白名单 |
| Model提供方 | 可信接收、唯一目标、固定对象、可靠登记/查证/保护 | Model公开API与本域存储实现；需求见D08，不在本次强行同步开发 |
| 文档澄清 | 正常close/前序Task/整体结果/同名语义/验收映射 | 不拆成新状态服务、阶段引擎或ID系统 |

路径是已有评审落点，不是要求新增同名目录。统一意图/快照合同沿用 `contract/cpup01`；biz 不导入 Kratos/protobuf/存储驱动；data、service/server、composition root按实际分层处理。采用生成工程现有 `internal/`、`cmd/` 布局，不为历史示意搬仓。

**既有明确行为：**通用Pipeline没有 Model register；`close`在确认publication后请求正常关闭；`VerifyPublication`核验前序publish Task/Pod，而不是整个Run终态；ModelDev已有独立状态投影。微调必须在这些位置做有限适配，不写一份平行实现。[S3]

## D02 · 请求、受理和接缝字段

### 2.1 公网请求

保留 `POST /admin/v1/modeldev/executions`，在现有合同上增加 FINETUNING 分支。下面是业务示意，不是已发布API或可直接启用的fixture。

```json
{
  "name": "cpu-ft-smoke",
  "kind": "FINETUNING",
  "preset_id": "<已启用微调预设UUID>",
  "dataset_version_id": "<本租户READY Alpaca JSON UUID>",
  "base_model_version_id": "<已批准固定基模版本UUID>",
  "finetuning": {"learning_rate": "0.0001", "max_steps": 20},
  "new_model_name": "cpu-ft-demo",
  "idempotency_key": "<这次创建的业务键>"
}
```

| 字段 | 合同 |
|---|---|
| name | 1–80字符，沿用现有请求校验的字符计数与规范；不另改旧CPU算法 |
| kind/preset | FINETUNING必须匹配微调kind预设；不能借切换Release kind改变旧请求含义 |
| dataset_version_id | 确切本租户READY JSON记录；不得输入S3 URL/key来绕过导入 |
| base_model_version_id | 确切获准版本；不得选择latest、其他模型家族或加密版本 |
| finetuning | 与general_parameters互斥；仅learning_rate/max_steps，见A07 |
| target_model_id / new_model_name | 必须二选一；target为Model.id内部UUID；new name遵守Model现有名称规则及create-only语义 |
| idempotency_key | 沿用原同租户/主体/动作语义；重放优先于解析最新Release |

拒绝null、重复键、未知字段、类型截断，以及用户提供tenant/actor、镜像、命令、资源、namespace、storage/URL、证书、Runtime等可信运行配置。`source_execution_id`继续现有未开放边界。已有公共字段的历史错误字符串不改；新增微调错误在同一映射位置定义，不吞掉领域原因。

### 2.2 受理次序

1. Gov取得真实Principal、映射业务租户，执行当前模块/action校验；不存在“跳过权限的原键重放”。
2. 验证请求结构，按既有合同保存原意图的值及字段存在性，查原幂等记录。同键同意图返回原记录，异意图冲突。
3. 仅新请求解析当前Release、READY数据和固定基模；校验目标写权或新建权，尽早检查名称冲突，核实必需依赖可用。
4. 外部只读解析不置于Gov数据库事务；事务内核对当前绑定generation并原子保存operation/execution、规范快照和待投递命令。
5. ModelDev按现有Gov调用合同校验原tenant/operation/intent/spec，原子持久inbox后ACK。ACK不是允许任意KFP请求创建TrainJob的凭据。

受理前委托可以只有可信tenant/intent与受限选择器，不要求尚不存在的spec_hash。受理后读固定事实和原目标。字段编码优先使用现有公布的结构化合同/附件；缺少的最小字段与提供方共同发布，不发明通用grant生命周期。[S2]

### 2.3 仅两个必要的拟新增业务能力

| 能力 | 实施方式 |
|---|---|
| `ImportFinetuningDataset` | 窄管理RPC；format明确为JSON；不让用户任意选择schema，详见D03。若尚未发布则按现有API流程生成/发布 |
| 原登记恢复 | 拟采用 `/admin/v1/modeldev/executions/{id}:register`；具体已存在等价入口优先复用；最终选择和wire在API交付时固定，不能同时开放两套别名 |

这里“拟”表示尚未作为提供方正式API发布，不等于开发时可随意改业务语义。恢复参数不包含新目标、名称、版本或对象路径；只能指定原执行及本次恢复请求的幂等/追踪字段。D08/D09定义真正执行的原内容。

## D03 · Alpaca JSON 导入与物化的确定实现

### 3.1 保留原导入机制，删除多格式负担

旧 `ImportCSV` 继续数值MLP语义。新 `ImportFinetuningDataset` 管理请求只接受原固定object、来源scope、format=JSON和既有管理请求身份；schema由服务固定为 `ani.finetune.alpaca.v1`。不接受CSV/JSONL/ShareGPT、自定义列名或用户给出的转换程序。

按现有状态保存 VALIDATING/READY/REJECTED 及有界错误。首次验证前持久原请求、来源对象和格式；瞬时不可读保留原对象等待受控重试，不当作内容REJECTED，也不换key重新导入。内容非法才REJECTED。

### 3.2 固定与验证

先固定S3具体版本/已批准不可覆写对象，再读取实际长度与SHA；不得先哈希一个可变key，再复制它的“当前最新”。来源不具备固定性时复用既有安全导入固化能力；没有可用的固化能力则明确拒绝，不在本次另造存储系统。

用严格解析验证A05结构，保留字符串原值和行序，计算row_count。源≤32MiB；实际长度超限、摘要不匹配或选错版本都拒绝READY。管理员提交的size/SHA仅是待核对声明。READY、幂等收据及本域保护事实一致持久化，同ID换字节/格式/scope冲突。

**64条在微调Release受理时检查。**合法的其他行数可以导入为READY，但不能交给只批准64条的当前预设；页面/接口返回明确“该预设要求64条”。若运行前数据预处理发现实际有效样本数变化或样本被静默过滤，当前固定配方失败，而不是仍报64条成功。正常多次遍历数据不等于扩大源样本数。

### 3.3 原样物化，不再维护源/派生格式转换系统

prepare再次按同一固定引用读取，独立核对长度/SHA后原样写入：

```text
inputs/data/finetune_data.json   = 已READY的原JSON字节
inputs/data/dataset_info.json   = 由锁定模板生成的配置字节
```

`data.json` 的本地SHA与原对象SHA必须相同；保持空input、Unicode、转义和空格，不能静默整理。`dataset_info.json`以固定数据集名 `ani_cpu_smoke` 映射 instruction/input/output，其模板摘要和实际文件摘要纳入prepared-manifest。

v4不新增v3草稿中的CSV normalization_version、源/派生双数据摘要或64MiB派生文件上限；单一原文件≤32MiB足够。公共schema版本、验证实现与已保存对象摘要仍保留。若现有工作树已产生相关微调记录/字段，实施前报差异并按已有合同处理，不擅自删记录。

这不要求用户按字节做规范序列化：不同空格排列可以是两份合法文件，各自有准确摘要。请求规范化用于execution spec，不与数据文件字节规范化混为一谈。

## D04 · 基模与Model读取接缝

### 4.1 最小读取合同

复用 `GetModelVersion` / `GetModelDownloadURL`，方法全名、API模块版本与字段在Model提供方交接中固定。受理前解析固定版本并校验READY、未加密、批准/保护、租户与使用权；冻结以下领域事实：

| 事实 | 用途 |
|---|---|
| Model内部模型UUID、确切版本UUID | 定位同一资产，不依赖显示名 |
| provider/reference及固定版本选择器或不可覆写承诺 | 后续读取同一对象 |
| 归档size、SHA256、模型format | 实际取回时核验 |
| 基模批准/保护事实及预期HF清单摘要 | 确认当前预设允许使用且生命周期安全 |

不能依赖下载响应里未被实际赋值的默认布尔值判断是否加密；使用版本接口的可靠事实。当前范围拒绝加密、跨租户与未批准基模。Model读取未接入时该真实链路标阻塞，不能把普通body tenant转成Principal。

### 4.2 临近下载时取得短期能力

ModelDev使用自己的服务身份和原受理事实申请本次版本的短期只读能力。KFP组件可以通过既有受管步骤接口取得限定原对象的短期能力，或使用已核对的物化流；**不扩一个通用下载代理API**。具体使用哪种既有通路在对接时固定，禁止组件请求任意Model ID/URL。

URL失效可在原预算内重签同一版本；不重新选择最新版本，不把有效URL写入snapshot、日志或长久manifest。服务间RPC走Gov合同；临时GET数据流不是第二个业务入口。

### 4.3 安全物化

下载和哈希流式处理，先写本次staging，再安全解包并核对实际HF文件全集，最后原子发布 `inputs/model`。起测归档≤2GiB、展开≤4GiB，确切文件数/路径/大小边界由M0锁定。不得对大权重用 `io.ReadAll`。

拒绝绝对路径、穿越、反斜杠、重复成员、软硬链接、设备/FIFO、未批准扩展头和超限展开。批准基模实际归档必须在M0验证满足该合同；不靠禁用解包校验适配异常包。

prepared-manifest包含原JSON、dataset_info、基模归档/文件清单摘要，原PVC UID与spec_hash。半成品目录不作为准备成功；重复准备必须定位原字节，不从网络重新挑选基模。

## D05 · Release与规范快照的最小增量

| 合同位置 | 增量字段/事实 | 限制 |
|---|---|---|
| Intent | FINETUNING、finetuning参数、基模版本、目标分支 | 原字段存在性与旧CPU摘要规则不变 |
| InputRef | format=JSON、schema、row_count、原固定对象 | 不加多格式normalization数组 |
| Program/Release | 训练Program、ExportProgram、VerifyProgram；版本/digest、配方、固定argv规则 | 只一个TrainingPlan；不加Trainer phase列表 |
| Workspace | export子路径、必要报告布局 | 复用原inputs/training/reports/publication；CPU原根语义不变 |
| OutputContract | MERGED_HF_MODEL、HF/Adapter/报告角色、in_tar、文件和字节上限 | 真实文件全集来自M0，不猜固定“七文件” |
| Snapshot | 基模与目标、Gov委托事实、模型存储交接绑定、组件配置/预算 | 受理后不变；实际UID/输出SHA/registration receipt属于运行事实 |

Release同时固定KFP PipelineVersion/IR摘要、Runtime内容与target、组件和训练镜像digest、所有程序/配方摘要、CPU池、工作区、资源与deadline。不能只改一个镜像tag就称复现同一Release。

原CPU无微调子结构时必须**真正省略**新增字段，不写空数组/空对象/零值改变规范JSON。枚举追加不改旧编号；codec/conformance双向覆盖。不要为通过新测试而更新旧CPU黄金摘要或放宽旧错误/拒绝集。

公共 `contract/cpup01` 是现有唯一维护位置，不复制到Gov手工修改；提供方先正式发布，Gov/ModelDev再固定版本。尚未公布的Model API不能用本地replace充当正式互操作证据。

M0需产出完整可解析的Release候选与数据/manifest测试向量；`<digest>`等文档占位值不得进入启用材料。原旧环境的镜像/Run只作来源，不补本轮PASS。

## D06 · 唯一TrainJob、KFP计算步骤与证明范围

### 6.1 训练入口

唯一TrainJob运行受管 `finetune_entry`，调用锁定版本的LLaMA-Factory。固定SFT/LoRA、target=all、rank8/alpha16、cutoff128、batch1/accumulation1、seed/data_seed42、CPU；关闭混合精度训练、flash attention、外部上报和额外中间checkpoint。必须保存最终Adapter。

M0以**实际锁定版本**验证所有argv/配置覆盖规则，不能把MLP的 `--recipe` 直接传给裸CLI。只启动一个训练进程；谁负责进程拉起由受管entry与Runtime共同锁定，不让CLI和Runtime重复启动分布式进程。

训练实际加载精度、Adapter保存精度、export精度、verify精度分别锁定。关闭fp16/bf16不自动决定导出dtype；本稿不猜填未经测试的版本或峰值。summary记录真实optimizer_steps、实际有效源样本统计及输入/基模/配方/Adapter摘要，不填期望值。[S0]

### 6.2 export / verify不是额外训练作业

两个组件同步运行锁定程序，不能调用EnsureTraining，也不新建TrainJob。通过已有受管step入口验证实际workload/权威Run/task/Pod UID、原spec和停止/deadline。配置读取只授权本次固定步骤，不是一张永久或可转交的凭据。

只新增必要的role、原task/Pod UID、启动/结果、清单引用事实，以便识别重复执行与关闭；保存在现有执行机制可承载的位置。**不新增阶段调度器、创建许可平台或多Job生命周期。**

禁KFP计算cache/自动retry，restartPolicy=Never。entry不会因为某次查询重放再次运行CLI。取消/SIGTERM传给进程组并等待实际退出；超时不得留后台权重写者。结果不明查询原Pod，不以重启计算生成“确定结果”。

### 6.3 具体证明对象

| 动作 | 必查对象 | 不可复用的错误证明 |
|---|---|---|
| 开始export | 原TrainingHandle/Observation、实际训练结束/无写者、Adapter与原输入关联；train-wait对应Task完成 | 整个KFP Run已完成 |
| 开始verify | 原export Task及Pod零退出，merged-manifest/文件字节属于本次 | export自报一条成功日志 |
| 确认publication | 原publish Task/Pod零退出，候选与实物大小/SHA/tar完全一致 | publish尚在运行时自报“已发布” |
| 最终CLOSED | 原Run/Workflow、唯一TrainJob控制器历史和全部相关写者，包括export/verify | 某个前序Task成功或当前Pod NotFound |

`runtimeproof/tasks.go`等白名单要按执行类型包含新角色；`ManagedRuntime.Publish`/`VerifyPublication`对微调只允许register确认，CPU仍只允许原close。不要改成任何已认证Pod都能确认任意publication。

M0除短推理外，应验证导出确实读取本次Adapter：记录基模/Adapter摘要和真实argv；用固定输入比较“基模+Adapter”与merged的一致性可作为负载验证。它不是模型效果评测，不要求每次跑大型评测集。

## D07 · 工作区、文件全集、tar与发布

### 7.1 路径与挂载

| 路径 | 内容 | 正常写入者 |
|---|---|---|
| inputs/data | 原样finetune_data.json、dataset_info.json | prepare |
| inputs/model | 核验后的固定基模 | prepare |
| training/adapter | Adapter权重、配置 | 唯一TrainJob |
| export/merged | 完整HF模型 | export Pod |
| reports/train | 指标、summary、Adapter关联清单 | TrainJob/受管采集实现 |
| reports/export | merged清单与诊断 | export Pod |
| reports/verify | verify报告 | verify Pod |
| publication | inventory、candidate等非权威暂存 | collect/publish |

prepare建立本次后续所需的空子目录并写入输入，不写训练/导出权重。TrainJob只读inputs，写training及reports/train；export只读其前序输入，写export与reports/export；verify只读merged，写reports/verify；collect/publish只读产物，写publication与受限spool；register/close只读必要报告，不持Model管理证书。

不存在“外部TrainJob自动继承KFP卷挂载”的假设；两处分别配置。所有写子路径明确分离，允许合同中故意共享的只读父路径；不要把父目录和子目录的合法只读关系误判成写入重叠。目录创建与K8s subPath存在性在目标CSI中验证。

UID/GID10001、只读根文件系统、受管缓存/临时写目录与CPU池配置按已验证Runtime/组件材料应用。多租户启用范围依据实际证据，不因设计有两个租户字段就声称任意隔离均通过。

### 7.2 文件全集与主tar

微调RequiredFiles相对workspace根；CPU保持原training根和叶文件合同。M0固定当前镜像、基模的真实export全集：config/tokenizer、完整safetensors或实际shards/index；另外收集Adapter、真实metrics/summary、verify及必要清单。缺文件、未经批准的额外文件、摘要冲突均拒绝；更新全集走新Release。

主模型使用**未压缩USTAR tar**，只纳入 `in_tar=true` 的 `export/merged` 文件；成员名剥离该前缀，在包根直接出现HF文件，保留必要内层路径。不把export/merged目录套进tar根，不放Adapter、平台output-manifest或诊断报告。

Adapter权重/配置/关联清单作为独立工件保存。file SHA、manifest SHA、tar SHA分别表示各自字节；Model的checksum为整个tar SHA，format为模型文件格式（本例safetensors），不是把format写成tar。

不强制重写已稳定的CPU打包规范。微调tar禁止路径碰撞、穿越、link、设备/FIFO及非批准PAX扩展；标准tar尾部零填充允许，非零附加数据拒绝。重放读取原已固定tar，不重新打包并期待随机元数据恰好相同。

### 7.3 发布与确认

大文件流式哈希/捕获到磁盘spool，捕获后再上传原执行前缀；检查真实大小、路径和读期间变更。逐级fd/O_NOFOLLOW、受控文件类型和多链接检查沿用并扩展现有安全I/O，不改成简单拼路径。

publish返回候选，不能自证自己的Pod已经结束。register前半段复用独立核验，验证原publish身份/终态以及远端每个固定对象的实长/SHA、完整manifest和主tar，再事务持久publication。失败不调用Model，不能只上传一个manifest就标PUBLISHED。

直接对象引用或必要复制以受理前的绑定确定，详见D08；没有经验证的绑定不得正式启用该Release。不要同时开发两套动态fallback模式。

## D08 · Model需求交接：先留接缝，提供方就绪后接入

本节是 **ModelDev向Model提出的具体领域需求**，不是新的服务间调用规范。传输/身份/委托/超时/重放统一引用Gov合同。Model提供方先确认现有API是否足够；不足的窄能力由提供方正式发布，消费方再固定依赖。不得以本节示意方法名复制一套Model Proto。[S2]

### 8.1 本次职责与就绪条件

| 编号 | Model需要提供/保证 | ModelDev需要完成 | 正式联调证据 |
|---|---|---|---|
| MR01 可信接收 | 在已有业务mTLS入口按服务+完整RPC+用途建立可信上下文；检查tenant与资源；不信任body自报Principal | 重建metadata；只从已验证Gov委托/持久受理构造请求；不冒用Gov证书 | 无证书、错服务、越权方法、冲突tenant、伪造actor拒绝；合法原范围通过 |
| MR02 基模读取 | 返回已批准精确版本及真实size/SHA/format/加密/固定引用；短期下载始终同版本 | 冻结读取事实、临近使用才签发能力、流式核验 | 固定版本读回；源同key新版本不替换基模；禁止任意版本读取 |
| MR03 目标与名称 | Model.id与外部标识明确；目标归属/可写检查；租户内名称唯一，新建重放回原模型 | create-only与existing分支固定；预检仅提示/拒绝，不承担最终唯一性 | 同名新执行冲突；原键重放同模型；显式existing新增版本 |
| MR04 版本登记 | 原key+原业务内容幂等；完整登记内容一致性；可靠finalization | 第一次写入前持久原登记意图，后续只使用原目标/原publication | 一次结果；同key异内容拒绝；丢响应/部分提交恢复 |
| MR05 原结果查证 | 限定原tenant/业务键返回未创建、pending/部分完成、READY或冲突及原字段；READY-only旧接口含义不变 | 先查证再按合同有限重放；不拿404单独当无副作用 | 响应丢失能找回；不会创建第二版本；未完成状态不可当READY |
| MR06 固定对象与下载 | 固定版本选择器或受管不可覆写key贯穿持久引用、校验、下载和重放 | 不将versioned object降成latest；tar SHA/size与原发布一致 | 同key后来内容改变，仍下载原内容；长度/SHA不符拒绝 |
| MR07 存储交接 | 明确当前作用域可否接受原publication引用；需上传时票据不另建业务版本 | 固定一条经验证绑定；仅必要时复制，不动态切模式 | 实際从Model读取同一tar；配置缺失明确未启用 |
| MR08 删除保护 | 批准基模/登记成品的保护在实际删除路径生效；保护建立时机明确 | 在批准、引用和记录时使用权威事实；不靠隐藏按钮 | 删除与批准/登记竞争时不会删掉受保护资产 |

MR项全部完成是完整微调交付的条件；**不是要求ModelDev开发人员同时实现两个仓库**。Model暂未交付时，这些接口可以先用边界替身进行消费者合同测试，但不能把替身的权限/事务结果算成MR已通过。

### 8.2 实际API优先复用

现有方法族包括 `GetModel`、`GetModelVersion`、`GetModelDownloadURL`、`CreateModel`、`CreateModelVersion`。复用这些正式API及固定模块版本；名称精确占用检查和pending查证如现有接口无法可靠表达，再由Model补一个限定能力，不新建通用查询系统。`GetUploadURL`只有当前选定交接确实需要复制时才启用。

每一项调用登记填入**最终完整RPC全名**、已签发的服务SAN、结构化上下文或既有附件字段；不能把上述用途名称当成已经发布的RPC。原actor用于审计，不独自赋权。普通查询用当前授权；受理后后台完成限于原任务批准内容，不重放长寿命用户JWT。

### 8.3 稳定键、内容与事务

| 操作/记录 | 本次规则 |
|---|---|
| 新建容器操作 | `md-container:<execution_id>`；只有new分支使用；持久保存实际Model.id收据 |
| 输出版本标签 | `exec-<execution_id>`；不是查最大版本再加一 |
| 版本登记操作 | `md-<execution_id>`；同tenant下稳定；请求追踪ID允许变化，业务key不变 |
| ModelDev原登记内容 | 原受理/目标分支、规范Model UUID（或尚待创建的原name）、基模/verify/publication关联、原tar引用/版本/size/SHA/format、加密与engine配置、交接模式 |
| Model侧内容fingerprint | 以其实际接收合同为准，覆盖影响最终模型版本的全部持久字段：tenant、目标、版本、对象固定引用、size/SHA/format、加密和engine等；不能只比key |

ModelDev的来源追溯字段不要求在Model当前Proto中无理由复制；Model回读字段覆盖自身登记内容，ModelDev另外核对本域publication/来源。未传给Model的字段不能声称由Model独立验证。

在外部首次写入前，ModelDev持久原登记意图；new分支先请求容器、记录/找回原Model UUID，随后形成完整版本请求。容器响应不明按原key查证，不因缺UUID改走按名自动复用。

Model先在事务外核验**不可变**对象，随后在本域事务中锁定原登记/版本，核对相同内容，插入或复用唯一Artifact，置READY并持久必须的审计事实；不把对象流和跨服务RPC包在长数据库事务中。已有历史“Artifact完成但READY未写”状态必须可恢复。同key异内容拒绝；READY重放返回原持久字段，不用新请求拼旧结果。[S4]

ModelDev查到原READY并核对确切target/version/object/size/SHA等后才写registration成功；HTTP/gRPC成功或durable ACK不等同于Model READY。是否采用已有查询或新窄方法，由Model在API交接时固定，并保留旧查询语义。

### 8.4 存储只启用当前需要的一条路径

优先核对**直接引用原publication固定对象**是否在Model的租户作用域、权限与引用格式内真实可用。兼容则直接引用，不为了格式统一重复复制。

确实不兼容时，当前环境选择**固定的受控复制绑定**：由ModelDev服务侧从原已发布对象读取，复制到Model批准范围，核对实长/SHA，并固化最终引用。upload票据只管字节传输，不能顺便生成另一个模型版本。

这两项是受管部署选择，不是每次失败时尝试的双后端。首个环境只交付它实际选择的模式；未实现模式保持不支持，不宣称已通过两套验收。选定后冻结配置，不在执行中切换模式/目标bucket。

固定VersionID或不可覆写key需被真实下载链消费；不能只在ModelDev记录版本而Model读取latest。既有上传URL未到期不能再覆盖成品；需要staging/final或固定版本的条件由提供方落到实际接口。

### 8.5 依赖不可用的结果

Model尚未配置/正式接入：原CPU可继续；FINETUNING保持未启用，模型读取与登记真实验收为BLOCKED，不新建假版本。已经受理后的短暂故障：有限查证/重放，预算耗尽保存失败或UNKNOWN并关闭计算；后续走D09的原登记恢复。

## D09 · 运行事实、恢复与查询投影

### 9.1 本期最小事实，不规定必须新增多少表

| 事实 | 持久位置原则 |
|---|---|
| 入站受理、原Intent/Snapshot | 复用现有Governance受理和ModelDev inbox/execution；事务/唯一约束不改 |
| JSON READY | 复用input记录的类型分支、固定对象、row_count、schema和验证结果 |
| export/verify | 最小role、原task/Pod UID、开始/结果、文件清单引用；不能用它推进DAG |
| publication | 扩原输出合同和核验；CPU由close确认，微调由register确认 |
| registration | 本域原登记意图、容器/版本收据、请求fingerprint、状态和最后有界错误 |
| close | 原停止generation、在途资源、控制器/实际写者证明；纳入新增组件 |

逻辑记录可以由既有表加可选字段或有限新增表承载，由仓库当前模型决定；不要求每一项单建服务/状态机。对外状态由同一事实投影产生，保留旧CPU语义。

### 9.2 原登记恢复

仅当真实训练和export/verify成功、publication已确认、原登记事实存在、执行CLOSED且无关闭待核实时，才能接受恢复。Gov核对当前操作者对原执行及原目标的权限，持久一条恢复命令；ModelDev使用其原记录执行，不信任用户body替换业务内容。

恢复不启动KFP、TrainJob、export或verify，不挂原训练PVC，不改变旧计算deadline。需要原绑定的存储交接时只从已发布对象读取，用原upload/registration key完成；它是有界交付I/O，不是重新训练。

原正常登记和恢复共享同一记录，用有限租约/CAS防止并发混乱。若Model写入已在途，旧处理者可能仍返回结果，因此仍必须依赖Model原key幂等并核对内容，不能把lease当作跨服务绝对一次执行证明。

同名/内容冲突停止自动重试；依赖暂不可用允许按原内容有限重试。所有变化只在允许的登记状态发生，不能为了“恢复”重开计算。没有原publication或原登记意图时，不将该接口泛化成手工发布任意文件的入口。

### 9.3 停止/deadline与登记竞态

停止前已在途的外部写入不能被撤销为“未发生”；按原key查证并保存receipt。停止之后不得产生新的正常登记写入；原receipt记录与只读查证仍允许。CLOSED可记录迟到的真实版本事实，不据此再次启动步骤。

用户在CLOSED后显式恢复，是新的当前授权动作，但操作内容仍是原登记；它有自己的正RPC预算，不延长旧计算许可。状态结果按A10显示，原KFP历史不改写。

### 9.4 错误与查询

原CPU公开错误、未启用行为不变；新增微调错误在同一服务错误映射定义。查询、下载、日志、恢复重新检查当前权限及数据范围，不从当初创建者身份自动授予现在的访问权。

当前已支持scope以实际CPU主干为准；若当前仅支持tenant-all，更细SELF/组织scope仍明确拒绝，不降级放大为tenant-all。该拒绝可以证明安全边界，不能标为已支持原v0.4 C02完整产品能力。

## D10 · 起测资源与时限：共用机制、微调预算

数值沿用v3作为**待实测起点**，不是容量保证或已经测试的生产规格。execution_timeout_seconds=10800，受理固定deadline_at；正常预算合计10800秒。[S0]

| 环节 | CPU request/limit | 内存 request/limit | 预算 |
|---|---|---|---:|
| 排队/编排余量 | — | — | 900s |
| prepare | 2/4 | 6Gi/8Gi | 900s |
| TRAIN与train-wait | 训练2/4；等待0.5/1 | 训练6Gi/8Gi；等待512Mi/1Gi | 3600s |
| export | 2/4 | 6Gi/8Gi | 1800s |
| verify | 2/4 | 6Gi/8Gi | 900s |
| collect | 1/2 | 1Gi/2Gi | 600s |
| publish | 1/2 | 1Gi/2Gi | 900s |
| register含确认/已选交接 | 组件0.5/1；服务I/O另预算 | 组件512Mi/1Gi；服务流式内存 | 600s |
| 正常close余量 | 0.5/1 | 512Mi/1Gi | 600s |

每个步骤的deadline是 `min(该步骤第一次实际开始时刻+预算, execution.deadline_at)`。TRAIN负载与train-wait共享一次TRAIN预算，不能分别获得3600秒；查询重放、重签和重启不能重置开始时刻。

复用已有deadline/取消机制，将预算传到KFP timeout、Runner context、子进程、RPC/I/O；不能只改Snapshot还留下原统一1800秒限制。旧CPU默认值和执行行为保持不变。逐RPC是调用deadline，整个登记多次RPC共同消耗register预算，不每次重新领取600秒。

CloseWorker在原deadline后使用有限的观察/清理预算，不延长计算或正常登记。600秒是本次目标，不是“超过后自动CLOSED”的条件。无法证明时保持NEEDS_REVIEW/待核实并给出诊断。

| 文件/存储预算 | 起测值及含义 |
|---|---|
| JSON原文件 | ≤32MiB；本地准备副本与原字节相同 |
| 基模归档/展开 | ≤2GiB / ≤4GiB；具体成员上限M0固定 |
| collected files | ≤4GiB累计，包含哪些文件由RequiredFiles确定 |
| 主模型tar | ≤3GiB；Model checksum为tar摘要 |
| 全部发布字节 | ≤8GiB；merged文件和其tar的双份传输分别计入 |
| 工作区 | 20GiB起测；实际需要合计基模、Adapter、merged、清单/缓存等 |
| publish spool | ≤8GiB磁盘；临时磁盘request10GiB/limit12GiB；不使用内存emptyDir存整模 |

spool/TMPDIR必须实际挂到足够的受管磁盘，不继续落入原小型16MiB临时目录；capture/打包用量共同受限。export/verify缓存及服务侧复制分别计入实际容量/配额，明确不同生命周期，不把“写了limit”当作CSI/磁盘已验证。

M0验证固定20步；扩大max_steps/learning_rate可用范围之前验证对应程序、预算与失败行为。超预算需测试并发布新Release，不给正在运行的任务续期。

## D11 · 验收：本版新增用例与原v0.4逐项映射

### 11.1 证据级别

| 记录 | 含义 |
|---|---|
| CODE_READY | 相关代码、合同、测试和fixture已交付；不是测试已经执行 |
| FEDORA_PASS | 在指定Fedora完成相应生成/编译/模块或真实负载测试；记录真实外部替身边界 |
| MODEL_INTEGRATION_PASS | 真实Gov规范调用、Model接收/数据库及业务链通过；不是fake Model |
| LIVE_PASS | 指定K8s集群上对应真实依赖、资源和行为通过 |
| NOT_RUN / BLOCKED / FAIL / DEFERRED | 未执行/当前前置缺失/执行失败/明确不在本版；不能互相替换 |

同一用例可以有Fedora和LIVE两份结果，不能只留一个PASS隐藏环境。历史CPU证据用于回归选择与对比，不补微调通过。没有按本轮依赖范围实际执行的测试保持NOT_RUN。

### 11.2 本版微调用例

| ID | 场景 | 必须观察 |
|---|---|---|
| FT01 | 合法Alpaca JSON导入 | 真实授权、固定对象、实际size/SHA、严格解析，事务READY |
| FT02 | 错格式/超限 | CSV/JSONL/ShareGPT、重复/未知键、null/类型错、坏UTF8、超32MiB拒绝 |
| FT03 | 固定数据 | 原key出现新内容不改变READY；prepare本地JSON与原固定字节一致 |
| FT04 | 64条预设 | 其他合法行数可READY，但当前预设拒绝；预处理不丢样本 |
| FT05 | 当前权限与租户 | 无权基模/目标、跨租户详情/日志/下载/恢复拒绝；窄scope不扩大 |
| FT06 | 原创建重放 | 原key同内容回原ID，异内容冲突；切Release/暂停不更换原快照 |
| FT07 | 新建/已有目标 | new分支只建一个容器；existing不CreateModel，增加原容器的新版本 |
| FT08 | 同名与并发 | 新执行同名冲突，最多一个新建成功；不按名偷换目标，原重放不误拒绝 |
| FT09 | Model未接入 | 微调新受理未启用，CPU既有能力不被迫失效；分段测试不标完整交付 |
| FT10 | 基模真实固定读取 | 读取确切版本/归档，拒绝加密/错家族/越权；流式实长SHA与安全解包 |
| FT11 | 单TrainJob真LoRA | 一个权威TrainJob UID，实际优化步数与非空Adapter；无GPU/重复训练 |
| FT12 | 未知创建结果 | 只找原资源，迟到响应与多Run不导致第二份计算 |
| FT13 | export正确来源 | 基模+本次Adapter导出真实merged；关联摘要/argv完整，不只加载一个基模充数 |
| FT14 | verify独立加载 | 单独进程、锁定精度、greedy短推理与实际报告，失败不得登记READY |
| FT15 | 前序Task证明 | 不等整Run终态；错误前序Task/Pod UID、未退出的publisher不能放行 |
| FT16 | 文件与tar | 缺/多文件、link/穿越、错前缀/非零尾随拒绝；HF根布局、Adapter独立正确 |
| FT17 | 持久发布 | 原publisher退出，远端每个固定对象及主tar字节核验后才PUBLISHED |
| FT18 | 当前存储交接 | 实际选定模式正确；Model固定读取原tar；未启用模式不写PASS |
| FT19 | Model可信链 | 真服务身份/完整RPC/委托范围/tenant检查；拒绝假Principal与越权方法 |
| FT20 | 登记一致性 | 原容器/版本，全部业务字段一致；READY重放返回持久真值 |
| FT21 | 登记故障窗口 | 容器响应丢失、Artifact写后中断、READY响应丢失；原key恢复不造新版本 |
| FT22 | 正常close | NATURAL_TERMINAL收据后返回，Run自然成功终结；无自等待/无主动杀正常Run |
| FT23 | 用户停止 | 排队/训练/export/verify各阶段停止，实际子进程无残留，无新计算 |
| FT24 | deadline/重启 | 原时限不续期；owner重启继续观察/关闭，不重训；未知不假CLOSED |
| FT25 | 迟到登记/恢复 | 停止后在途receipt如实保留；CLOSED后当前授权恢复原内容，不挂训练卷 |
| FT26 | 状态显示 | compute成功不盖verify/登记失败；Model READY+CLOSING不显示全部完成 |
| FT27 | 大文件/容量 | 实際spool目录、磁盘/内存界限和流式I/O；超限安全失败，旧CPU默认不变 |
| FT28 | 保护与清理 | 基模/READY/成品实际删除受限；唯一未发布/未知写者阻断清理，UID plan/apply一致 |
| FT29 | 独立新模型使用 | 无训练PVC，经授权从Model取新tar，以现有Inference/KServe真正调用 |
| FT30 | 原CPU回归 | 旧字节/摘要/错误/拒绝集与相关训练/发布/关闭保持，必要旧48步链通过 |
| FT31 | 自动业务与交接 | 正常提交无需人串步骤；非原作者按实际入口完成启用/执行/恢复 |
| FT32 | 来源与范围 | 记录真实commit/digest、环境/资源/对象事实，暂缓项不标PASS，凭据不入证据 |

以上32项是计划，不是本次执行结果。MR提供方未就绪时，FT07/08/18–21/25/29中依赖真实Model的部分为BLOCKED或NOT_RUN；结构替身通过只记录相应消费者测试。FT11/13/14可先做Fedora真实负载，但不冒充LIVE。

### 11.3 与平台v0.4的C01–C34对照

这里保留原用例含义，不重命名它们来制造“全覆盖”。原表依据v0.4 D16。[S1]

| 原ID | 原范围 | 本版处理 |
|---|---|---|
| C01 | 两租户权限 | 必须验证本次微调及受影响既有查询/恢复 |
| C02 | SELF/组织与动作 | 动作和当前已支持scope验证；未实现SELF/组织仍拒绝并标DEFERRED，**不声明C02全覆盖** |
| C03 | 身份头/旁路 | 复用现有接入，新增角色/调用关系需负向回归 |
| C04 | 训练运行身份 | 包含新增export/verify；不得带Model管理凭据 |
| C05 | 新受理暂停 | 保留并回归新微调kind |
| C06 | 小JSON/CSV用户上传 | **本版DEFERRED**；受管JSON导入不能替代上传 |
| C07 | 超限/错格式/迟到PUT | 导入的内容/固定性验证；上传会话专属部分DEFERRED，不声明全部覆盖 |
| C08 | 受管S3代导入 | 本次Alpaca JSON必测；旧MLP导入相关回归 |
| C09 | 创建重放 | 必测，含新目标字段 |
| C10 | Release与在途 | 必测，旧快照不改 |
| C11 | 两服务目录一致 | 必测，未启用/不匹配明确拒绝 |
| C12 | KFP提交不明 | 继承并对受影响路径回归 |
| C13 | 公共Trainer | 两类任务复用同一个单作业机制 |
| C14 | 工作区 | 新组件与TrainJob挂载、互不覆盖必测 |
| C15 | 第二个登记镜像 | 是通用训练既有范围，不因微调自动宣布完成；受影响时回归，否则关联其独立证据 |
| C16 | CPU MLP | 相关回归；原48步主链不被微调破坏 |
| C17 | CPU LoRA | 本版核心新增 |
| C18 | 导出损坏/缺文件 | 本版核心新增 |
| C19 | 真实发布 | 微调tar/manifest/远端字节必测 |
| C20 | 新模型版本 | Model正式对接后必测；此前BLOCKED/NOT_RUN |
| C21 | 登记丢响应/重试 | Model正式对接后必测 |
| C22 | Model一致性 | Model提供方真实联调必测 |
| C23 | 新HF包推理 | 实际Inference/KServe链路必测，无法完成不能宣布完整微调交付 |
| C24 | 停运行/排队 | 含export/verify的实际停止 |
| C25 | 停止/迟到创建 | 继承，加入迟到组件/登记事实 |
| C26 | 重启 | 原记录观察/关闭和原登记恢复 |
| C27 | 失败日志 | 新阶段有界错误/日志；缺口如实显示 |
| C28 | 发布失败保留 | 必测，不误删唯一输出 |
| C29 | 受保护资产删除 | Model/输入实际入口，提供方待就绪则对应阻塞 |
| C30 | 受控清理 | 有限既有plan/apply扩新目录，不扩通用GC |
| C31 | 手册/启用回退 | 按本次微调实际入口复演 |
| C32 | 不逐次人工串流程 | 完整Model接入后正常自动链通过 |
| C33 | CPU禁GPU | 请求/渲染/运行三者均验证 |
| C34 | 历史证据隔离 | 全程遵守，不将历史或替身替换新验收 |

**本版完成声明是“本版CPU受管微调增量与相关CPU回归完成”，不是“平台v0.4的C01–C34全部已实现”。**CSV、ShareGPT、上传等暂缓能力仍不启用；Model对接是分期前置，不是永久删除的目标。

## D12 · 环境、文档位置与并行工作包

### 12.1 三类机器，不再混为一处

| 环境 | 允许职责 | 不能作为哪种证据 |
|---|---|---|
| 本机/本地源码工作树 | 阅读、编码/文档编辑、Git提交推送、发起受控远程操作与转运制品 | 不本机编译、代码生成、单测、容器构建或训练 |
| **Fedora编译/验证机** | 既有SSH配置的Fedora；生成/格式化、编译、测试、Pipeline编译、镜像构建、临时数据库模块验证、真实LoRA负载验证 | Fedora边界替身闭环不等于目标K8s业务通过 |
| 172.16.101.10～12所属K8s | 实际部署、KFP/Trainer/存储/权限/停止与独立取模联合验收 | 不替代Fedora编译机，不在节点宿主机编辑/构建或裸跑服务 |

沿用已有 `fedora` SSH入口及当前批准的工作目录。具体地址/源码路径/测试数据库/镜像缓存引用来自本任务现有受控记录，不能直接把其他installer任务的目录当成modeldev目录。若缺少本任务值，核实已有SSH/任务记录后登记，不猜值、不移到集群规避。

K8s三节点本机可直连；从现有授权context核对API端点、集群标识和节点InternalIP，不猜`.10`必是控制节点或API固定6443，不用旧192.168.*环境。远程构建必须检出已推送的明确commit，生成差异回本机审阅后提交。保留已有临时数据库/容器清理流程，只清理本次资源。

### 12.2 方案从哪里读取

沿用附件v3给出的单一权威相对路径：

```text
<实际 ani-modeldev-service 工作树>/.scratch/finetuning-cpu/spec.md
```

本交付包的 `spec.md` 是完整v4正文；HTML和 `model-integration-requirements.md` 由同一正文派生，不能各自修改。采用v4时先保留旧v3，再由用户/获准agent将v4放到上述已确认权威位置。当前下载生成**没有修改用户仓库**。

如实际任务已改用ANI-doc中的另一个权威位置，先核对并在任务记录登记唯一位置，再更新所有引用；不同时维护`.scratch`与ANI-doc两份可编辑“主方案”。旧CPU-P01/ENV任务包保留其原路径与职责，本版不是新的总控goal，不应把全套历史goal拼接执行。

开工记录应包含实际方案绝对路径、文件SHA、各仓branch/commit、Fedora工作路径及集群交接来源。不得把`<实际工作树>`占位符当作已确认目录，也不能因缺一份环境交接就停止全部编码。

### 12.3 并行工作包，不按流水线逐人排队

| 工作包 | 可以先开发 | 真实联调前置 |
|---|---|---|
| W0 负载与材料 | 固定JSON、受管entry、export/verify、镜像与文件全集 | Fedora获准构建/素材与负载验证 |
| W1 合同与受理 | 复用CPU意图/快照/错误，新增kind/基模/目标；消费者合同 | 正式API版本与实际Gov↔ModelDev |
| W2 输入与准备 | JSON validator、READY、固定副本、基模物化端口 | 原S3、工作区及真实Model读取能力 |
| W3 执行与关闭 | 单Trainer程序挂载、KFP新增步骤、身份/Task证明、取消与状态投影 | 环境Runtime/KFP/CSI与真实新负载 |
| W4 产物与ModelDev接缝 | HF收集、tar、publication、原登记记录与可替换client端口 | S3、已选交接模式与Model API |
| W5 Model提供方 | 按MR01–08需求评估/改进自身接口，独立发布 | 不强制由ModelDev人员承担；完成后双方联调 |
| W6 故障与交接 | 编写FT用例、范围映射、操作记录 | 各真实模块逐段可用即验证，不等最后一起大联调 |

Model或目标环境未就绪时继续W0–W4及可行的Fedora检查；把依赖它的真实用例逐项BLOCKED。不能因“留接口”就实现一个总返回成功的假Model生产adapter。API参数及配置字段由合同方先固定，真实Runtime/版本ID后填确切值。

工作树先读AGENTS和他人改动；任务分支/独立worktree，禁止自动stash/reset/clean、强推和自动合并主分支。ENV agent管理installer共享环境，不顺手改其工作树或升级控制器；分支不隔离共享集群。

## D13 · 实施前必须锁定的事实与停止条件

**“消除歧义”不等于编造尚未实测的值。**下面的未知项有明确负责人、用途和未就绪行为，不能由agent自行选择另一条架构。

| 待锁定事实 | 负责人/阶段 | 缺少时 |
|---|---|---|
| 相关工作树/正式API/模板实际commit | 各仓负责人，开工 | 可读代码和准备合同；不假定默认分支与评审基线一致 |
| Fedora实际登录与本任务源码/制品路径 | 当前执行负责人，W0前 | 只阻塞相应远程动作；不本机运行或把集群当构建机 |
| LLaMA-Factory/Python/PyTorch/transformers/peft版本、镜像digest、离线材料 | W0/M0 | 不发布可启用Release，不猜标签 |
| CPU训练/保存/导出/verify实际dtype与CLI向量 | W0/M0 | 不声称关闭bf16即可保证输出精度 |
| 64条真实样例、Tokenization有效样本、20步输出全集/摘要 | W0/M0 | 不预填实际优化步数、文件名全集或推理结果 |
| Model方法/模块版本、精确SAN、字段映射/查证能力 | W5/M1-B | Model接入未启用；允许显式消费者替身测试 |
| 直接引用是否可用、否则唯一已选复制绑定 | W4/W5/M1-B | 不临时切模式，不泛化实现两个storage后端 |
| KFP/Trainer/Runtime/CPU池、StorageClass、受管身份与入口 | ENV交接+W3 | 相关LIVE阻塞，业务编码/Fedora检查可继续 |
| Inference取新包和实际CPU/其他既有推理Runtime支持 | 现有Inference负责人，M3前 | FT29/C23不通过就不能称完整交付；不另造推理后端 |
| 公开目标API既有语义是否与本稿冲突 | 合同owner，合并前 | 明确登记差异，不暗改历史客户或改主键 |

Model/Gov共同合同足够规定调用规则；本表只补实际接入材料与业务方法，不升级为新信任体系。共同关闭机制发现确切缺陷时，以已复现反例修正和回归，未证实的担心不无边界扩任务。

所有占位值必须在启用时已被真实受管值替换并验证；缺少的必需值明确失败。代码、合同/替身验证、Model真实联调、目标集群通过分别记账，不“默认通过”。

## D14 · v3到v4变更与冲突消除清单

| v3位置/旧说法 | v4唯一结论 | 性质 |
|---|---|---|
| §1/§13“close关闭/停止KFP”未区分正常异常 | 正常NATURAL_TERMINAL收据后返回，Run自然终结；异常才主动停止；owner独立完成CLOSED | 继承现有实现，修正歧义 |
| §8“KFP终态”笼统描述 | 前序成功检查是Task/原TrainJob；最终关闭才是Run/Workflow | 证明范围澄清 |
| §2/§5 JSON与指令型CSV | 仅Alpaca JSON三字段子集；CSV/ShareGPT/自定义暂缓；旧MLP CSV不变 | 用户确认的范围缩减 |
| §5 normalization源/派生双模型 | 单格式采用原样物化，原JSON与本地SHA一致；只生成独立dataset_info | 本版明确的简化实现选择 |
| §5共同校验64条，§3又称Release限制 | 导入验证结构并统计；64条由当前Release受理检查 | 条件归属澄清 |
| §11另列大段通用S2S规则 | Gov合同唯一通用规范，本稿仅具体调用关系和Model业务需求 | 不新增授权体系 |
| §11 Model为所有业务开发的同步前置 | ModelDev先端口/记录/测试，Model正式后真实接入；公共微调完整能力仍待依赖齐备 | 用户确认的分期开发 |
| §4 new_model_name跨执行语义空缺 | create-only；重复操作原键；另一轮追加必须target_model_id；并发由Model唯一约束裁决 | 必要业务规则 |
| target/base容易混淆 | target只决定输出容器，base决定读取权重；输出V1不自动成为下次基模 | 保持最初平台设计 |
| 对外状态表被误解为新设计缺口 | 状态和完整成功条件原已存在；只新增微调事实与统一展示解释 | 修正审核分类 |
| 资源预算被误解为微调专用机制 | 复用原deadline/取消；传递新步骤预算，旧CPU默认不变 | 有界适配 |
| M1要求真实Model同步全部完成 | 分M1-A消费者/主体开发与M1-B真实Model联调，替身只证合同 | 前置拆分，不降低最终标准 |
| §16“完整C01–C34不缩减” | 按D11明确上传、scope等暂缓/部分覆盖；不将导入代替上传 | 修正实际范围冲突 |
| 环境执行可能被误读成全放K8s | 本机编辑/Git，Fedora构建/测试/负载验证，K8s真实部署/验收 | 恢复用户明确约束 |
| 两种storage交接都被当作首期必建 | 环境固定选一条实际必要模式；直接兼容不复制，无运行时fallback | 减少不必要实现 |

**开发者不得从历史v3单句恢复已删能力。**本版明确决定优先于v3对应段落；没有变化的公共执行实现继续复用。Gov通用调用规则不因本稿另有示例被覆盖。实际代码差异先记录到任务，再由合同owner决策，不从“留扩展口”推导出新增平台授权。

本文不重新估算总人天，也不为Model待就绪承诺完成日期；后续任务只针对新增/受影响点估算，不能把公共机制再计为从零开发。

## 来源与证据口径

本稿是对给定材料与用户后续决定的修订，不是一次重新部署、测试或全仓审计。**“继承”描述源码评审基线；“本版决定”描述设计；“待锁定”描述必须经实现/实测确认的值。**

### S0 · 用户附件《ANI CPU 受管微调方案 v3》

本轮完整输入，383行；内容包括单TrainJob、KFP export/verify、原受理/发布/关闭复用、Model需求、资源预算及原验收映射。原文副本位于 `reference/spec-v3.md`。v4变更详见D14；不覆盖附件原件。

### S1 · 模型研发平台 v0.4

《精简业务与架构方案》《精简开发实施说明》《管理与操作指导手册》是平台范围背景；本轮重点重新读取开发册D16的C01–C34，保持各用例原含义。v0.4不是所有内容均已实现的报告。v0.2/v0.4已含target_model_id/new_model_name两个目标分支。

### S2 · Governance统一调用合同（本轮读取）

仓库 `zhangzhe-ctrl/ani-governance`，固定commit `34388cf90d0834da76c9e5701ade5d92a248144f`，`docs/contracts/service-to-service-calls.md`，重点§1–3与§5–6。本规范涵盖三种调用方向，并明确规范入仓不代表存量实现全部符合。

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

本轮产出仅为文档及其呈现/一致性检查；无业务源码写入、Git推送、Model依赖发布、Fedora训练、K8s部署、数据库迁移或业务验收。

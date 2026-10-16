# ANI CPU 受管微调方案 v4.2.2：主流程优先、登记仲裁与分段验收

版本：微调方案 **v4.2.2**；平台基线：**v0.4**；日期：2026-10-14。状态：`DESIGN_UPDATED`。直接底稿为用户上传的完整 **v4.2.1**。本轮并入两轮讨论：①登记中具体写操作的派发与停止/deadline共同仲裁；②先完成ModelDev微调执行与产物主链，真实BFF和前端联调后置；③完整参数组合与省略默认值的黄金向量。[S11][S12] **最终产品范围不缩减：参数链、真实BFF/前端、Model、独立Inference及相关回归仍须最终验收。GPU仍是独立准备线，不阻塞当前CPU主链或本版最终验收。**

本文件为唯一可维护正文。业务/开发HTML、主流程开发手册、Model需求、参数/前端视图、GPU边界、W0–W6及验收计划均由本文件派生；不得各自编辑。采用前保留当前v4.2.1及已填进度；权威路径和Fedora入口见D12。本文已直接替换冲突的前置关系，不需要叠加聊天补丁。`review/self-review.md`只记录文档检查，不是运行结果。原历史材料不构成本轮执行授权。

**当前首要目标：微调执行与产物主链通过。**先固定正式接口和参数合同，允许隔离测试装配代替尚未接通的上游来件，但下游ModelDev持久化、KFP/Trainer、训练/export/verify、真实发布确认及停止关闭必须按被验收层级真实执行。BFF/前端未就绪不阻塞该目标；被替代的身份授权和接入部分不标PASS。不存在新增生产测试入口、假Gov身份或默认成功的Model adapter。

阅读分册：A01–A12为业务、职责与流程；D01–D14为开发落点、接口、资源和验收。


## A01 · 术语：先分清对象、动作和成功事实

本方案编号是 **微调方案 v4.2.2**，平台范围基线仍是 **模型研发平台 v0.4**。直接底稿是v4.2.1，保留既定关闭/指标/完成层级、参数链和GPU后续边界；本轮调整开发与验收顺序，补齐登记派发仲裁，不发布任何运行能力。文档版本、程序参数合同版本、Release ID和用户执行参数仍是不同对象。[S0][S1][S7][S8][S11][S12]

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
| 启动已受理 / 实际已启动 | 前者是原步骤首次启动通过仲裁并已持久保存；后者是原Pod实际派生子进程的观察事实 | 数据库提交已经原子地启动了进程 |
| 单次执行成功完成 | 本次训练、导出、验证、发布、登记和关闭满足A10，且按停止/恢复规则展示 | 单个Run通过就代表本方案全部验收通过 |
| 本方案最终验收完成 | D11规定的本版用例、相关回归、Model真实接入、独立Inference调用和交接完成 | 每次任务必须自动部署推理服务 |
| 合同测试 / 真实联调 | 测试边界结构和错误语义 / 实际双方服务、身份与持久化链路验证 | 替身通过即真实依赖通过 |
| 公共请求合同 / 程序参数合同 | 前者定义结构与规范编码；后者定义具体程序支持字段、语义、硬边界及执行映射 | 用首个样例数值永久约束所有训练程序 |
| Release参数规则 | 当前批准的默认值、允许值/范围和固定设置；随不可变Release冻结 | 最新镜像中随时变化的默认值 |
| 显式参数 / 解析后参数 | 用户实际提供的字段及存在性 / 按本次Release补默认值后的完整结果 | 两份可以各自编辑的权威配置 |
| 有效配置展示 | 创建时看当前可提交规则；执行详情看原快照；运行结果看实际事实 | 用当前预设重建旧任务的配置 |
| 固定配方验证 / 参数链验收 | 前者只证基准点；后者证明批准的非默认值确实进入程序 | 基准点通过即批准所有字段或连续范围 |
| 主链阶段通过 | 按D11.6完成真实执行与产物、参数实际消费及必要停止/关闭检查的阶段成果 | 真实BFF授权已通过、Model必已READY或整版已验收 |
| 隔离测试驱动 / 测试来件 | 在隔离测试装配中替代上游来件，复用正式应用用例与持久化；边界显式记录 | 正式Gov授权事实、生产直连入口或管理员改库 |
| 登记意图 / 具体派发在途 | 前者固定交付目标；后者是某一原业务写操作已通过派发仲裁的记录 | 一份意图自动批准后续所有写操作 |

**“声明了一个事实”“取得了真实证明”“对外展示了一个标签”是三件事。**数据、训练、发布、登记和关闭分别判断；UI 或 API 的整体标签只解释这些事实，不成为第二份权威状态。

## A02 · 范围与明确决定：本次交付什么、不交付什么

| 项目 | v4.2.2 继承的最终范围与当前执行顺序 |
|---|---|
| 业务入口 | 最终仍经现有BFF/Governance；真实BFF/前端联调后置，不作为当前执行主链的前置；不增加生产旁路 |
| 新服务数量 | 不新增常驻服务；在已有 ani-modeldev-service 及相关领域服务内增量实现 |
| 训练方式 | CPU、单节点单进程、LoRA SFT；一个 Trainer TrainJob |
| 基模 | 本租户已批准且受保护的 Qwen2.5-0.5B-Instruct 固定版本；不是 Qwen2-0.5B，也不是任意最新版本 |
| 文件输入 | **仅受管 S3 中的 Alpaca JSON 数组**，原文件 ≤32 MiB；不交付浏览器/本机上传 |
| 数据结构 | 三个必填字符串 instruction/input/output；instruction/output 非空，input 可为空字符串；不接受其他字段 |
| 样本规模 | 首个启用预设要求64条；这是预设条件，不是所有 Alpaca 导入的全局限制 |
| 当前请求可输入项 | 当前微调参数合同只接受learning_rate、max_steps；规则归当前合同/Release，不是FINETUNING永久字段上限 |
| 继承的参数交付 | 先完成合同、解析、快照及实际程序消费，至少一组非默认真实验证；当前规则查询/真实BFF/前端贯通随后完成，仍属最终范围 |
| 更多参数阶段 | batch、梯度累积、cutoff、LoRA rank/alpha等列为下一批候选，必须另行批准字段/组合/预算后启用；本包不提前接受或静默忽略这些字段 |
| 数据暂缓 | 指令型 CSV、JSONL、ShareGPT、自定义字段映射、history/system/tools 扩展 |
| 原通用训练 CSV | 保持 CPU-P01 的数值特征 CSV 原语义；不因微调增加而改变 |
| 模型交付 | 合并 HF 未压缩 tar + 独立 Adapter 工件 + 必要运行报告；目标为自有 Model 中的模型版本 |
| Model 开发顺序 | 先提出需求并留调用接缝，提供方正式就绪后再接真实 adapter；不能以此宣布完整微调已交付 |
| 状态和关闭 | 复用 CPU-P01 公共机制；只增加微调阶段、文件和登记事实 |
| 推理验收 | 复用现有 Inference/KServe 路径独立使用本次新包；不在训练流水线自动上线服务 |
| 页面与管理 | 规则/查询合同先定，真实BFF和现有表单/详情在后续联调阶段完成；不作为主链验收前置，不新建工作台/表单引擎/发布管理产品 |
| 不在本次 | GPU/QLoRA、分布式、任意脚本、清洗、效果评测、Hub、自动调参、Direct 双后端、多个 TrainJob、通用 grant/登记/阶段调度系统 |

继续保留对v3的收敛：CSV暂缓、Model仅阻塞其真实用途与最终验收，一个TrainJob及正常关闭语义不变。[S0] 参数与展示是v4.2已经明确的范围增量。[S9] v4.2.2仅把真实BFF/前端排到主链之后：**后置不是DEFERRED，不从本版删除，也不把接口合同推迟到最后再设计。**[S12]

**不把“Alpaca 三字段子集”说成 Alpaca 的全部能力。**本期拒绝尚未支持的内容，不能接受后再静默丢弃。暂缓能力没有隐藏开关，不为了将来支持先建立通用插件框架。

### 2.1 GPU后续接入：本轮记录接缝，不并入正式功能

当前业务仍为CPU微调。**GPU正式资源选择、Gov占额、ACC的ModelDev owner接入、GPU训练和退额，作为后续独立批次交付，不进入本版产品接口或完成条件。**通用训练与微调将共用执行资源接入，GPU不作为学习率等算法参数塞入`finetuning`。[S10]

W1/W3在现有工作中核对并记录资源解析、快照、Trainer渲染和计算关闭的接入位置；若结构已经足够，**无需为“留接缝”修改生产代码**。不提前发布GPU Proto/数据库字段，不把`CPUResources`一律改名，不给CPU执行补零值GPU或假charge，不建设资源插件框架。

Gov/ACC合同差异核对、Fedora渲染检查及具备条件时的隔离GPU验证可以独立并行。**它们不构成W0–W6开工或本版验收的前置，也不能替代任何FT/PA/MR证据。**缺GPU、缺设备插件或未完成owner注册，只影响相应GPU准备项；当前CPU路径仍严格拒绝GPU。范围和分工见D10.2、D11.5、D12.5。

## A03 · 最终业务链与当前先做的执行主链

### 3.1 最终用户业务链（架构不变）

下图是最终集成目标，不是要求所有步骤的外部依赖在开工前同时就绪。

```text
管理员把 JSON 放到已批准 S3 范围
  → 受控导入：固定对象、校验内容、登记 READY

用户经现有 BFF 提交微调
  → 当前用户/租户/权限校验
  → 原键重放检查；新请求解析并冻结 Release、输入、基模、目标
  → 按本次合同/Release解析默认值与显式值，冻结实际配置
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

本方案另行执行验收：经授权从 Model 取得本次新包，无训练 PVC，使用现有 Inference 实际调用
```

**正常业务链不能由管理员逐次手工串接。**管理员负责预设、镜像、基模、数据导入和例外处置；每次正常提交后的计算、发布、登记与关闭由程序自动执行。

| 边界 | 能证明什么 | 不能宣布什么 |
|---|---|---|
| Model 未正式就绪时的开发/分段验证 | 真实负载、模块、发布与关闭及Model消费者合同可逐项验证 | 已有可信Model登记；已完成用户完整微调闭环 |
| 当前执行与产物主链 | 在隔离装配中复用正式ModelDev持久化、真实KFP/Trainer及原Pipeline；实际训练/export/verify、发布确认、产物取回和关闭通过 | 上游真实BFF/前端、未接入的Model或整版验收已通过 |
| 单次执行成功完成 | 真实训练/导出/verify，固定远端publication，原ModelVersion READY且一致，最终CLOSED；按A10处理停止与恢复历史 | 本方案已全部验收，或用户已自动获得一个新推理服务 |
| 本方案最终验收完成 | 本版应交付用例、相关回归与交接完成；FT29/C23从训练卷外独立使用本次新包真实调用 | 未做的CSV/上传/多框架/GPU同时已支持 |

**Model未接入时不增加永久的“只训练不登记”公共微调模式。**公共FINETUNING完整能力保持未启用。内部主链验证可以在发布确认后观察真实的依赖未接入错误和计算关闭，记录“执行与产物主链通过、Model登记未接入”；不能把该执行改为A10六项齐备的业务成功。已受理后的Model临时故障继续走原失败/恢复路径。

### 3.2 当前先验什么（不是第二条生产流程）

```text
隔离测试驱动：正式codec/程序合同 + 固定候选执行来件
  → ModelDev正式应用用例和真实数据库持久记录
  → 原KFP Pipeline → 唯一Trainer TrainJob → 真实LoRA
  → export → verify → collect → publish
  → register前半段：真实确认publication
  → Model可用则真实登记；未接入则明确不可用，不返回假READY
  → 复用正常或失败关闭路径，确认实际计算关闭
  → 不挂训练卷，从持久对象取回并校验真实产物
```

上游测试来件不代表Gov已经认证/授权，也不冒充Gov持久账本或生效指针。只有明确声明的上游/外部边界可以替身；原工作负载身份、权威Run绑定、真实写者和对象核验不能用假成功代替。具体测试装配、数据/基模来源和证据分层见D12.6，当前阶段完成清单见D11.6。真实BFF/前端及FT29独立Inference留在随后阶段；原合同应现在固定。

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

## A07 · 参数规则、可调范围与旧合同兼容

本节完整规则现在定义。当前优先实现合同/默认解析/快照与entry实际消费；实际BFF取规则、页面提交及端到端权限验证后置到M3-B，不阻塞M1-A/M3-A。A07.4非默认真实负载仍属主链必要参数验证，不能随页面一起全部后推。

### 7.1 本次到底开放多少

**本轮正式实现的可输入训练字段仍是learning_rate与max_steps；本轮必须把承载/展示/执行做成完整参数链。**固定基准配方可先通过，但仅一个参数点通过不能宣称本版参数能力已完成。至少一个经验证的非默认配置需要完成D11.2.2对应检查；批次/序列长度/LoRA结构等新可输入字段不因此自动启用。

首个基准点为learning_rate=0.0001、max_steps=20，其余配置见D06。规则阶段分开：只有基准点验证时，候选Release允许集只能是该点；经过非默认验证后，发布新的、包含实际批准值的Release。不原地改旧Release，不把单点证据变成整个范围的保证。

### 7.2 四层规则，一份事实来源

| 层次 | 权威职责 | 明确不做 |
|---|---|---|
| 公共解码/意图规范化 | kind与结构、类型、字段存在性、数字词法、重复键/未知字段、大小限制及稳定编码 | 在所有kind共享函数里写epochs=3、batch=64或微调max_steps=20 |
| 受管程序参数合同 | 支持字段、语义/单位、程序硬边界、类型转换、执行映射与必要组合语义 | 任意透传框架参数、运行期猜参数名称 |
| 不可变Release | 在程序支持能力内批准默认值、允许枚举或范围、固定配置、预算与验证引用 | 修改已受理任务；为每个用户输入值生成新Release |
| 执行快照 | 本次合同/Release及完整解析参数、受管固定配置；供执行与历史查询 | 执行时重读最新默认值；建立第二份可编辑参数权威 |

公共维护点仍是现有`contract/cpup01`及对应程序适配，不新建参数服务、DSL、通用插件系统。typed `finetuning`保留，不要求换成任意map。细化字段见D05.1。[S9]

### 7.3 当前参数合同与启用规则

| 参数 | 当前程序合同的硬边界（不是已批准运行范围） | Release负责 | 实际框架映射 |
|---|---|---|---|
| learning_rate | DECIMAL；沿既有有限精度无指数十进制字符串，值(0,1]；不经二进制浮点计算意图摘要 | 默认0.0001；批准枚举或区间必须位于硬边界内 | learning_rate |
| max_steps | INTEGER；JSON整数1–200，拒绝小数截断和字符串冒充；规范值按合同转十进制字符串 | 默认20；初始仅20，非默认及边界通过后逐批扩大 | max_steps，表示优化器更新步数上限 |
| row_count | READY中的实际统计，不来自用户训练参数 | 当前Release要求64，预处理不得静默丢样本 | 数据实际消费与有效样本统计 |
| rank/alpha/cutoff/batch/accumulation等 | 当前仅为程序受管固定设置，不是公开请求字段 | 固定值和镜像/映射绑定；以只读摘要展示 | D06及D05.3锁定的受管映射 |

请求硬边界属于当前程序合同，不属于所有未来程序的通用上限。Release的枚举与范围二选一；默认值必须属于实际允许集合；不得只验证字段存在就放过值。固定集合只有一个取值时，前端可显示只读，但当前可输入字段同值显式提交仍按合同接受；从未开放的受管字段即使填相同值也拒绝。

### 7.4 基准与非默认值的验收决定

为避免继续只验固定样例，**本版新增设计测试向量**：A为0.0001/20，B优先为0.0002/10。B只是待实测候选，不是已批准值；同一批准基模/数据/固定配方下验证。A/B均通过相应层级后，候选Release可以明确批准这两个完整组合；不得仅把两个字段各列两个值就默认另外两种组合也通过。实际范围批准和组合限制一起固化。

B失败应保留失败结果，修复或由W0/W1明确修订候选，再按新摘要复测；不能隐藏失败、直接把允许范围写成(0,1]/[1,200]。至少有一组非默认合法配置完成参数链验收前，只能声明固定配方进度。本版不要求穷举连续数值所有组合，但不能没有边界/代表性依据。

### 7.5 兼容的是旧合同，不是永久固定样例

原CPU-P01请求/快照codec、规范字节/摘要、枚举编号和错误语义保持；微调子结构在旧CPU编码中真正省略。当前微调开发不放宽旧CPU v1拒绝集合。FT30只约束旧合同及其相关行为，不代表未来GENERAL_TRAINING永远拒绝epochs=5或batch=32。

本轮的入口分流明确为：GENERAL_TRAINING使用已公布CPU codec；FINETUNING使用新增且固定的微调codec。不能先经过CPU三参数校验再进入微调Release。现有微调版本如已在工作树正式发布，应先核实并沿用，不擅自重解释。

当前两个字段的取值/默认变化，若仍在同一请求与程序合同中，可用新Release表达。新增字段、单位、派生关系或规范化语义时必须交付相应API/codec/程序合同；**新codec应能由请求自身的明确版本或独立已公布方法确定，不能靠可能已经切换的当前Release猜旧请求编码**。未来具体wire变更属于那一批任务，不在本版预先开放隐藏字段。

### 7.6 用户不选择技术版本，切版行为仍须明确

用户仍选择预设；Gov是当前启用指针唯一权威。创建页得到一组一致的当前Release规则，受理新请求使用受理时当前有效绑定；原键命中不重新解析。两次操作间发生切版时：新值仍合法则按新绑定解析并返回实际release/有效配置；不合法则明确拒绝、刷新规则，禁止静默裁剪或重试成别的值。这里保留既有“受理时解析当前版本”语义，不新增用户任意选择历史Release的入口。

UI展示的默认值尚不是执行承诺。用户实际修改的字段应显式提交；未修改字段不在原键重试时偷偷补入。已受理详情只看原快照；安全配置摘要与精确查询组合见D02.4和D09.5。

## A08 · 每一步完成什么：训练、Task 和 Run 分层判断

| 步骤 | 输入/动作 | 成功事实与后继条件 | 失败时 |
|---|---|---|---|
| prepare | 固定 JSON、固定基模；安全物化；生成准备清单 | 原字节/基模完整，原 PVC UID、spec 与清单一致 | 不启动训练；保存诊断并进入关闭 |
| train-wait | 请求 ModelDev 唯一 Trainer adapter；等实际训练 | 原 TrainJob 成功，真实优化步数/Adapter，训练写者已退出 | 不导出；不得以创建成功代替训练成功 |
| export | 按D06.2仲裁首次启动；只读取固定基模及本次Adapter，同步运行导出 | export Task/Pod 成功退出，merged 完整且可关联本次 Adapter | 不 verify/登记；留下失败阶段 |
| verify | 按D06.2仲裁首次启动；独立进程读merged，greedy固定短输入，最多16 token | verify Task/Pod 成功，实际加载/生成和摘要报告完整 | 不交付 READY 模型；不是效果不佳判定 |
| collect | 读取原执行所有规定输出 | 精确文件全集、摘要和 in_tar 规则通过 | 不继续上传；保留唯一输出 |
| publish | 捕获文件到磁盘 spool，上传候选 | 返回原对象 candidate；**尚不能自证 PUBLISHED** | 保留输出，关闭计算 |
| register：前半段 | 核验前序 publish Task/Pod 与固定远端对象 | 事务持久 publication 后，才允许调用 Model | 未确认不得调用 Model |
| register：后半段 | 原目标、原 key、原 tar 调 Model 并查证 | 原 ModelVersion READY 且字段匹配，持久登记结果 | 保留 PUBLISHED，不重训；有界查证/恢复 |
| close（正常） | 请求 NATURAL_TERMINAL | 有效持久 CLOSING 收据后返回，让 Run 自然完成 | 独立 owner 继续对账，不假 CLOSED |
| owner/CloseWorker | 独立观察整个执行及计算写者 | Run/Workflow 终结与实际无写者，CLOSED | UNKNOWN/NEEDS_REVIEW，保留停止限制 |

**前序成功检查只针对前序 Task/原 TrainJob，绝不要求整个 Run 已结束。**否则后继任务会等待自己尚未完成的 Run。最终 CLOSED 才检查整个 Run/Workflow。普通通用训练已有“close 核验前序 publish”范例，本次按执行类型扩展到 register，不新建通用阶段引擎。[S3]

KFP 决定正常下一步。查询、配置读取、结果上报的重放不能重新拉起训练/export/verify；正常计算禁自动重试。需要重训时新建 execution；原结果不明先查原资源。

## A09 · Model 未就绪时如何开发，何时才可启用

**Model和真实BFF都不是所有微调编码及执行主链验证的全局前置。**Model仅阻塞其真实基模调用/模型登记和相应单次执行成功事实；真实BFF/前端仅阻塞其接入用例。两者仍是本方案最终验收及相应正式启用的必要条件。先主链、后BFF，不降低最终成功标准。

| 阶段 | 可以做 | 必须标明 |
|---|---|---|
| 接口需求先行 | ModelDev 领域端口、输入输出约定、原登记记录、显式错误、测试替身；Model 提供方评估已有 API | 仅结构/行为合同测试；不是 Model 权限/持久化通过 |
| Model 接收方未就绪 | Fedora验证获准固定负载；隔离环境验证ModelDev执行/发布确认/关闭 | 按D12.6逐项标明测试素材、替代调用及未验证权限；不写Model READY |
| 真实BFF/前端未就绪 | 正式输入合同先固定；使用隔离测试装配验证真实执行与产物；可以并行编码BFF但不等待其完成 | 不伪造Gov身份，不放宽正式listener；PA05及真实页面相关断言NOT_RUN |
| 读取能力先就绪 | 真实接入基模/目标只读解析，验证身份和固定内容；登记继续独立开发 | 读取通过不等于写登记通过 |
| Model 完整就绪 | 发布正式 API；ModelDev 固定消费版本；验证真实读写、重放与失败窗口 | 对应代码/服务/数据库不替身，独立合同验收 |
| 微调正式启用 | 相应Release必需依赖、目标验证及本方案最终验收要求满足后，按原批准流程启用 | 一次提交自动完成本次微调与模型登记，不靠管理员每次补登记；不自动部署推理服务 |

公共FINETUNING仍只在本Release必需依赖及既定正式验收满足后启用。此限制不禁止D12.6的隔离候选测试；候选测试不更改Gov全局当前指针，不给普通用户开放未验收能力。Model可选装配缺失不影响原CPU；正式路径不能明知无Model还受理并承诺自动登记。

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
| 原登记恢复成功，CLOSED | 本次产物登记恢复完成；保留停止/失败及恢复记录 | 改写原KFP Run历史，或把曾被停止的任务改写成无异常正常成功 |
| 用户停止，Model 写入在途后返回成功 | 保留“用户停止”与“已确认模型版本”两项事实 | 假装 Model 从未执行或自动删掉版本 |
| Model 未配置且无新受理 | 功能未启用/依赖未接入 | 一个虚构的已成功任务 |

### 10.1 单次执行成功完成条件

**真实训练结果 + export成功 + verify成功 + 持久publication + 原ModelVersion READY且一致 + CLOSED**。这六项是本次微调及模型登记的成功事实条件；判定还必须遵守本节的停止、失败和登记恢复规则，不能用结果集合覆盖历史异常。raw KFP/Task状态与原错误保留，READY不能覆盖用户停止，停止也不能抹掉已发生的Model写入。

该条件用于本次任务的查询与展示，不要求每次自动部署Inference/KServe。受控任务六项事实实际成立时，可记录执行结果，但BFF未接入不得声称Gov授权通过。仅有PUBLISHED+CLOSED而Model未登记时，只能按D11.6报告主链阶段成果，不满足本节单次成功条件。测试范围和最终验收另记，不新增业务状态枚举，也不据阶段结果放宽正式启用条件。

### 10.2 关闭、执行成功与方案验收各自独立

失败/被停止的任务也可能CLOSED；CLOSED只说明实际计算关闭，不单独等于成功。原登记恢复后可以显示本次产物登记已完成，同时保留原Run失败/停止和恢复事实，不改写历史。

**阶段边界见D11.6，最终验收条件见D11.4。**阶段顺序为负载与合同、真实执行/产物主链、真实BFF/前端及Model对应接入、整版独立使用与交接。主链阶段不需要全部FT/PA先通过；最终仍包括真实BFF、前端、Model、FT29/原C23及相关回归。FT29不添加为每个Run的后继步骤。

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

先持久保存停止/关闭标志，禁止其后新受理的业务计算及新获准的正常登记派发，再停止KFP与唯一TrainJob并核对在途请求、原UID和全部写者。export/verify首次启动按D06.2仲裁；登记中每个具体外部写操作按D09.3仲裁，整份登记意图不是后续写操作的通行证。启动/派发先受理的请求即使稍后才真正执行也须保留并处理；不能只检查当前运行进程，不能用Stop提交宣称所有远端副作用已撤销。

“关闭标志生效”“发送停止请求”“实际无计算写者”不等价。NotFound、请求超时或 KFP Failed 不能独自证明 CLOSED。继续复用现有确定性命名、UID历史、结果不明对账和关闭条件；发现共性缺陷时单列修复及反例，不删安全检查来通过测试。

关闭 worker 使用独立的有限清理时限，可以在原计算 deadline 之后继续**观察/停止**，不能继续训练、导出或正常登记。无法证明时 NEEDS_REVIEW，不无限循环重启程序。对已在途 Model 调用只保存/查证事实；副作用重发遵守原登记恢复规则。

### 11.3 清理不等于关闭

CLOSED 不自动删 PVC。唯一未发布输出、未知写者、未确认创建、未解决关闭均阻止相关清理。已登记模型、READY输入与批准基模的保护由各自权威服务落实到删除入口，不只是隐藏按钮。

首期固定基模应在批准给 Release 使用之前建立 Model 侧保护；删除与保护检查在其本域一致实现，不增加跨服务 TCC。已有 READY/登记事实对应保护要保持一致。安全解除合同未实现时受保护对象先禁止删除。

## A12 · 主流程优先的开工顺序与阶段声明

**当前先交付“微调执行与产物主链通过”。真实BFF、前端、候选经BFF进入验收的操作说明后置，不是W0/W2/W3/W4开工或M3-A通过条件。**W1现在必须交付正式结构、参数语义/解析和快照合同，不能最后再考虑上游兼容。后置工作可并行编码，但不能反过来阻塞主链。[S12]

| 阶段 | 交付目标 | 当前不等待什么／可声明什么 |
|---|---|---|
| M0 负载与合同候选 | Fedora先跑基准，再跑一组非默认候选；锁entry/指标/配置/文件/预算；W0/W1协作 | 不等完整BFF、Model或前端；只证真实负载与相应合同 |
| M1-A ModelDev增量 | 正式应用用例、真实数据库、输入/参数解析/原快照、KFP/Trainer适配、export/verify、发布确认、查询投影、关闭；测试装配 | 不等BFF listener/页面验收；Model边界可显式替身但不标MR通过 |
| M1-B Model对接（独立依赖线） | 提供方正式API；Gov规范调用、固定对象、具体登记派发仲裁及真实读取/登记/查证 | 按能力逐项接入；不阻塞不依赖Model的M0/M1-A/M3-A片段；没有真登记不声称单次成功 |
| M2 当前正确性与兼容 | 主链幂等、启动/停止/deadline/重启、实际指标、参数拒绝/组合、产物保留；R1在登记实现前落地；相关CPU回归 | 与开发同步，不等BFF最后一轮才测；上游权限与真实Model的未测断言单列 |
| M3-A 当前目标集群主链 | .10～.12真实ModelDev、数据库、KFP/Trainer、S3和实际进程；基准及非默认真实执行、发布确认、脱卷取回、停止关闭 | 按D11.6报告CORE_FLOW阶段证据；不要求BFF/前端或FT29先通过；Model缺失按D12.6明确报依赖错误并保留发布结果 |
| M3-B 后续真实BFF/前端 | 已有BFF认证授权/当前绑定/持久投递/查询/停止、两参数实际透传、已有页面；此时落实候选绑定和回退说明 | 复用主链，不重写训练；补PA05及其他含BFF/页面的断言，不拿夹具代替 |
| M3综合／M4最终 | 所有本版适用FT/PA/MR与相关回归、FT29/C23真实Inference、非原作者交接及正式启用材料 | 按D11.4报告SCHEME_FINAL；不删除后置项，不把单次/阶段通过当整版通过 |

这些名称表示研发阶段和证据范围，不是增加业务状态机或新的产品发布系统。M0/M1/M2与M3-A可按具体能力迭代合流；不要求先写完所有模块才联调。Model就绪可提前接入，不要求人为等到BFF阶段。

**三类机器保持：**本机只阅读/编码/文档/Git及受控转运；既有`fedora`编译机负责生成、编译、模块测试、镜像/Pipeline构建和真实负载；172.16.101.10～12所属K8s运行实际部署、KFP/Trainer及集成驱动/独立取回作业。具体入口/路径按D12核实，不猜Fedora IP或把构建搬到训练节点宿主机。

管理员准备批准材料、配置和测试范围；测试驱动只触发一次业务应用用例，不逐Task手工建Job或搬权重。最终正常用户链仍一次提交后由程序自动运行。

文档版本v4.2.2不是新增代码、Fedora负载、主链、BFF、Model、GPU或方案验收通过。计划初始NOT_RUN；当前仅更新文档与派生检查。

## D01 · 实现分类与改动落点：复用不等于重写

下列执行公共机制沿用 **ModelDev `5b7fd05…`** 的前轮评审；参数相关补充来自上一轮核对的 **`c5fd93c…`**，见S9。两者不是同一轮全仓审核，也不是本次重新读取的最新分支。实施前核实AGENTS/README、现有进度和实际commit，已完成能力先复用。[S0][S3][S9]

| 类别 | 本次处理 | 已知相关位置/边界 |
|---|---|---|
| 继承并回归 | Gov持久受理、重放、Release冻结与可靠投递 | 既有BFF、ModelDev client、admission/inbox |
| 继承并回归 | 单TrainJob创建、UID、观察、停止、未知响应恢复 | `internal/data/trainer/`、`internal/biz/managed_runtime.go` |
| 继承并回归 | 正常/异常关闭、owner独立核验 | `internal/component/io.go`、`internal/biz/execution_closer.go` |
| 继承并回归 | 计算/发布/关闭独立事实与投影 | `internal/biz/execution_state.go` |
| 参数链增量 | 规则单一来源、真实类型投影、当前Release与原执行展示、entry实际消费 | `contract/cpup01/{intent,release,snapshot}.go`、`internal/biz/admission_resolution.go`、`internal/service/materials.go`、公开Proto/相关Gov装配 |
| 微调增量 | Alpaca JSON验证/READY、固定基模物化 | `managed_input/input_importer/managed_materials`、`internal/data/input/`、`objectstore/`、`workspace/` |
| 微调增量 | finetune_entry指标适配、export/verify首次启动与停止共同仲裁，原步骤绑定及结果 | `internal/component/runner.go`、受管step入口、`runtimeproof/tasks.go`、`proof.go`、`owner_close.go` |
| 微调增量 | 多级安全文件、HF全集、tar选择、大文件预算 | `workspace/collector`、`pipeline_io.go`、对象验证器 |
| 微调增量 | register复用publication确认；每个具体登记写派发与Stop/deadline仲裁，迟到结果与原恢复 | `ManagedRuntime.Publish`、`VerifyPublication`、本域登记记录/原执行锁；不新建许可平台 |
| Model提供方 | 可信接收、唯一目标、固定对象、可靠登记/查证/保护 | Model公开API与本域存储实现；需求见D08，不在本次强行同步开发 |
| 文档澄清 | 正常close/前序Task/整体结果/同名语义/验收映射 | 不拆成新状态服务、阶段引擎或ID系统 |

路径是已有评审落点，不是要求新增同名目录。统一意图/快照合同沿用 `contract/cpup01`；biz 不导入 Kratos/protobuf/存储驱动；data、service/server、composition root按实际分层处理。采用生成工程现有 `internal/`、`cmd/` 布局，不为历史示意搬仓。

**既有明确行为：**通用Pipeline没有 Model register；`close`在确认publication后请求正常关闭；`VerifyPublication`核验前序publish Task/Pod，而不是整个Run终态；ModelDev已有独立状态投影。微调必须在这些位置做有限适配，不写一份平行实现。[S3]

### 1.1 参数扩展必须检查的现有落点

上一轮参数源码核对发现：intent公共校验接受最多三个CPU参数，并把epochs/batch固定；Release校验固定MLP命令/模板/参数版本；受理解析限定MLP合同；查询中的`parameterView`将epochs整数返回3、其他整数返回64。**这些规则对旧CPU合同成立，但不能机械套给FINETUNING。**[S9]

W1应建立明确kind/合同分流、复用真实类型解析和规则投影；旧CPU合法请求的结果保持。新增max_steps=20不能误显示64，更不能由前端写死20掩盖投影错误。对非法内部参数返回显式失败，不吞错补默认值。W1同时检查Snapshot验证是否仍重用CPU三参数校验，以及Gov所消费合同版本是否一致。

以上是本次复核要点，不表示要求重写`contract/cpup01`、搬迁仓库或修改整个通用训练产品；不通过local replace或复制生成代码绕过跨仓正式依赖。

### 1.2 ModelDev公共接缝核对，不要求提前实现GPU

本轮只在已涉及的代码处记录后续资源接入点：W1核对意图/Release/快照及资源摘要查询，W3核对实际训练容器渲染和持久关闭事实，W6维持CPU拒绝GPU及原快照回归。相关位置仍以本仓实际`contract/cpup01`、受理解析、Trainer adapter和CloseWorker为准，不为文档示意搬目录或新增平行实现。[S10]

当前CPU合同只允许CPU是正确限制；这项限制不能被描述成ModelDev永久不支持其他执行资源。但**核对接缝不等于本轮必须重构所有资源类型**：现有结构足够就只记录；仅当发现同时影响本版CPU正确性或本版正修改逻辑的具体耦合时，才在原任务内做局部修正和回归。纯GPU专属改动留待下一批或隔离试验分支，不改变当前生产请求/快照/启用规则。

不新增CPU占额记录，不在CPU关闭路径空调GPU退款接口；不以“未来要支持”为由接受未实现GPU字段。仓库分工和验证范围以D10.2为准。

## D02 · 请求、受理和接缝字段

### 2.1 公网请求

**正式合同现在固定，真实BFF接线与验收在M3-B完成。**当前测试驱动复用这些codec/解析及正式ModelDev用例，不另写宽松JSON接口；D02.2的Gov真实身份/当前指针/投递事实在被替代时必须标未验证。

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
| finetuning | FINETUNING必须提供对象且与general_parameters互斥；允许{}使用两项默认，不接受缺失/null；对象内仅learning_rate/max_steps且各自可省略。结构/词法先验，数值及组合在新受理解析时校验，见A07/D05 |
| target_model_id / new_model_name | 必须二选一；target为Model.id内部UUID；new name遵守Model现有名称规则及create-only语义 |
| idempotency_key | 沿用原同租户/主体/动作语义；重放优先于解析最新Release |

拒绝null、重复键、未知字段、类型截断，以及用户提供tenant/actor、镜像、命令、资源、namespace、storage/URL、证书、Runtime等可信运行配置。`source_execution_id`继续现有未开放边界。已有公共字段的历史错误字符串不改；新增微调错误在同一映射位置定义，不吞掉领域原因。

### 2.2 受理次序

1. Gov取得真实Principal、映射业务租户，执行当前模块/action校验；不存在“跳过权限的原键重放”。
2. 先按kind和已公布请求版本选定固定codec，检查结构/词法并规范原意图（保留字段存在性），查原幂等记录。同键同意图返回原记录，异意图冲突；不得为原键重放重读当前参数允许集。
3. 仅新请求解析当前Release、READY数据和固定基模；按D05合并/校验全部参数和组合；校验目标写权或新建权、名称冲突与必需依赖。未知参数、越界、错误组合在投递和计算前拒绝。
4. 外部只读解析不置于Gov数据库事务；事务内核对当前绑定generation并原子保存operation/execution、规范快照和待投递命令。
5. ModelDev按现有Gov调用合同校验原tenant/operation/intent/spec，原子持久inbox后ACK。ACK不是允许任意KFP请求创建TrainJob的凭据。

受理前委托可以只有可信tenant/intent与受限选择器，不要求尚不存在的spec_hash。受理后读固定事实和原目标。字段编码优先使用现有公布的结构化合同/附件；缺少的最小字段与提供方共同发布，不发明通用grant生命周期。[S2]

### 2.3 两个写操作能力；参数查询复用原查询接口

| 能力 | 实施方式 |
|---|---|
| `ImportFinetuningDataset` | 窄管理RPC；format明确为JSON；不让用户任意选择schema，详见D03。若尚未发布则按现有API流程生成/发布 |
| 原登记恢复 | 拟采用 `/admin/v1/modeldev/executions/{id}:register`；具体已存在等价入口优先复用；最终选择和wire在API交付时固定，不能同时开放两套别名 |

这里“拟”表示尚未作为提供方正式API发布，不等于开发时可随意改业务语义。恢复参数不包含新目标、名称、版本或对象路径；只能指定原执行及本次恢复请求的幂等/追踪字段。D08/D09定义真正执行的原内容。

### 2.4 预设与执行查询的安全字段合同

本节结构及安全投影现在定义和模块验证；真实当前绑定查询、BFF装配与页面交互后置到M3-B，不阻塞M3-A。没有真实响应时可提供显式回放样例，但不标实际BFF/页面通过。

本节字段是新增设计语义，**不是已发布Proto或默认已实现方法**。W1以现有`ParameterRule/PresetView/ExecutionView`与BFF DTO增量映射，确定最终字段名和wire编号；不另开一套参数服务/查询入口。字段存在性必须明确，不能由零值猜“无上限”。

| 对象 | 必需公开信息 | 来源/约束 |
|---|---|---|
| 当前预设视图 | preset_id、kind、active_release_id、release_digest、binding_generation、parameter_contract_version | Gov读取当前绑定，再取得该确切不可变Release；不能按任意首个历史Release拼规则 |
| 参数规则 | name、INTEGER/DECIMAL类型、required、default、允许枚举或上下界及开闭性、是否可编辑/不可编辑原因 | 直接投影Program/Release规则；单值集合可显示固定；不由前端补第二份范围 |
| 允许参数组合 | 当前候选使用完整组合列表时返回allowed_parameter_sets，均采用同一类型/值表示；必须与真实受理组合检查一致 | 不是任意表达式；单字段合法不代表跨字段组合合法，前端不能只消费独立上下界 |
| 受管配置摘要 | 当前已实际锁定的基模/镜像版本标签、CPU/单进程、SFT/LoRA、batch/accumulation/cutoff/rank/alpha及精度等白名单设置 | 明确标“平台固定”；不暴露Secret、bucket/key、短期URL、任意命令、Pod路径或原始全快照 |
| 执行详情 | 原release_id/digest、parameter_contract_version、用户显式字段存在性、resolved_parameters、安全固定配置及原spec_hash | 原执行Snapshot/Intent投影；无运行结果时不假造effective运行事实 |
| 实际运行摘要 | entry实际消费配置的规范摘要、真实optimizer_steps、实际样本/部分结果 | 原运行summary/观察；与请求上限及默认值分开 |

数值投影必须使用字段真实Value并按目标wire范围检查，不再按名字返回3/64。学习率以规范十进制字符串交互，UI的格式化展示不能改变原键重试请求的值/存在性。

### 2.5 当前生效指针与规则的一致读取

Gov保持唯一生效指针。BFF读到`release_id + digest + generation`后，通过既有目录读取能力取得该确切Release并生成PresetView；ModelDev不另造默认指针。现有ListPresets按多个Release聚合时，只返回与绑定一致的一组规则；其他镜像候选如有不同规则，不能共享一份任意规则。未能取得匹配数据返回明确不可用，不回退到历史第一个。

无需把跨服务网络调用放数据库事务中。并发切换时用已确定的revision保持一次响应自洽；可做有界重新读取，无法自洽就报可重试错误，不能混合A版本ID和B版本规则。新创建仍按A07.6“受理时当前绑定”处理，事务generation冲突沿既有重解/拒绝规则，不让同一operation落入两个快照。

查询对新增字段实施原租户/当前scope校验、白名单和有限大小限制。创建成功响应或随后的详情必须可以查询实际解析版本/参数，以便前端确认与提交前预览是否变化。

### 2.6 字段省略与重放

保持既有原始意图存在性规则：省略某字段、显式提供默认值是两个可区分意图；补默认值只能发生在新受理生成快照时，不能先改原意图再求hash。前端未修改只读展示时不得在原键重试中自动加入字段。允许省略的两个参数按Release补齐；required=true的字段缺失应拒绝。FINETUNING的finetuning对象必须存在，{}表示无显式训练参数而使用两个默认，null/缺失对象/未知键拒绝；这些约定只针对本次新分支，旧CPU存在性不变。W1固定精确黄金向量，跨服务一致。

本版不把参数规则摘要、Binding generation当作用户可写的授权令牌。未来需要“按预览版本提交”的产品行为再定义显式条件请求；当前不暗中新增这一语义。

#### 2.6.1 完整组合与省略默认值的黄金向量

下表是解析/拒绝合同测试，前提为一份**仅在对应验证后才可批准**的候选规则：默认A=0.0001/20，允许完整组合只有A与B=0.0002/10。不是本稿批准B，不因为表存在就启用该候选。单字段枚举不能隐含四个组合。

| finetuning对象 | 补默认后的值 | 结果与意图规则 |
|---|---|---|
| `{}` | 0.0001 / 20 | 接受A；原意图中两个字段均省略 |
| `{"learning_rate":"0.0001","max_steps":20}` | 0.0001 / 20 | 接受A；与{}的字段存在性不同，不用同一幂等键当相同请求 |
| `{"learning_rate":"0.0002"}` | 0.0002 / 20 | 组合未批准，拒绝；不能自动把max_steps改成10 |
| `{"max_steps":10}` | 0.0001 / 10 | 组合未批准，拒绝；不能自动把学习率改成0.0002 |
| `{"learning_rate":"0.0002","max_steps":10}` | 0.0002 / 10 | 接受B；值进入原快照和真实entry，仍须相应实测 |

将这些向量加入现有PA03/PA06/PA07，不新增PA11或DSL。当前先测共享解析与持久化；M3-B再验证真实BFF及前端按完整组合展示/提交。后端不得为匹配某组合偷偷修改另一个参数，前端不得在原键重试中补入新字段；不同来源阶段分别留证据。

### 2.7 后续执行资源入口与当前请求的分界

本版请求不增加GPU输入；资源/Namespace/Runtime等受管字段继续按2.1拒绝。后续GPU选择属于**与`finetuning`或`general_parameters`平级的执行资源合同**，由Gov/ACC现有规范承接，不由算法参数通道透传调度字段。后续具体字段、Proto编号、版本识别和附件关联在GPU批次固定；本段不是可用API示例或当前发布要求。

创建页当前仅展示真实CPU资源摘要，不能因本补充而显示可提交的假GPU规格。后续资源目录使用ACC并核对租户授权、ModelDev程序/Release与环境支持；历史详情使用原执行冻结的安全资源摘要，不读取最新规格重建旧任务。现在仅记录这些来源和接缝，不要求前端或Model服务增加GPU逻辑。

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

`inputs/data/finetune_data.json` 的本地SHA与原对象SHA必须相同；保持空input、Unicode、转义和空格，不能静默整理。`dataset_info.json`以固定数据集名 `ani_cpu_smoke` 映射 instruction/input/output，其模板摘要和实际文件摘要纳入prepared-manifest。

v4.2继续不新增v3草稿中的CSV normalization_version、源/派生双数据摘要或64MiB派生文件上限；单一原文件≤32MiB足够。公共schema版本、验证实现与已保存对象摘要仍保留。若现有工作树已产生相关微调记录/字段，实施前报差异并按已有合同处理，不擅自删记录。

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

不能依赖下载响应里未被实际赋值的默认布尔值判断是否加密；使用版本接口的可靠事实。当前范围拒绝加密、跨租户与未批准基模。Model读取未接入时该真实链路标阻塞，不能把普通body tenant转成Principal。当前M0/M3-A只可使用已批准的固定测试素材与显式读取边界，来源及替代调用按D12.6登记；素材不能冒充正式Model READY/使用权验收。

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
| Program/Release | 训练Program、ExportProgram、VerifyProgram；参数合同、允许规则/默认/固定配置、映射与镜像/配方摘要 | 只一个TrainingPlan；不加Trainer phase列表或规则服务 |
| Workspace | export子路径、必要报告布局 | 复用原inputs/training/reports/publication；CPU原根语义不变 |
| OutputContract | MERGED_HF_MODEL、HF/Adapter/报告角色、in_tar、文件和字节上限 | 真实文件全集来自M0，不猜固定“七文件” |
| Snapshot | 原参数合同与Release规则引用、完整resolved_parameters/安全固定配置，基模/目标、Gov事实、存储绑定、组件预算 | 受理后不变；运行配置回读、UID、输出SHA及registration receipt属于运行事实 |

Release同时固定KFP PipelineVersion/IR摘要、Runtime内容与target、组件和训练镜像digest、所有程序/配方摘要、CPU池、工作区、资源与deadline。不能只改一个镜像tag就称复现同一Release。

原CPU v1无微调子结构时必须**真正省略**新增字段，不写零值改变其规范JSON。枚举追加不改旧编号，codec/conformance双向覆盖；旧CPU黄金摘要和拒绝向量原样回归。此要求只约束旧合同；批准新的参数能力必须用明确的新合同/Release路径，不修改旧向量凑兼容，也不把旧拒绝集合当成未来所有程序的永久限制。

公共 `contract/cpup01` 是现有唯一维护位置，不复制到Gov手工修改；提供方先正式发布，Gov/ModelDev再固定版本。尚未公布的Model API不能用本地replace充当正式互操作证据。

W0/W1共同产出完整可解析Release候选及参数/数据/manifest向量。早期M0可以先锁训练程序/配置片段，KFP PipelineVersion真实生成后再组成完整候选，避免“未编Pipeline先要求完整Release”的循环依赖；缺真实ID时明确不是可启用材料。`<digest>`等占位值不得用于受理或启用，旧环境证据不补本轮PASS。

### 5.1 参数定义与Release规则的最小结构

沿用现有`ParameterContractVersion`、`DefaultParameters`、`ResolvedParameters/ResolvedArgs`和公开`ParameterRule`承载，不新建规则数据库。当前程序合同的受支持字段/类型/硬边界与执行映射由同一受管实现维护；Release在该能力内保存批准规则。名称可映射现有等价字段，但下列信息不能遗漏或多头维护。

| 合同内容 | 语义与验证 |
|---|---|
| parameter_contract_version | 标识具体程序参数语义，当前微调可采用`ani.finetune.lora.parameters.v1`；若已正式发布等价ID则沿用并登记，不重命名覆盖 |
| defaults | 两个当前可输入字段的完整默认；与固定受管配置分开；必须通过程序和Release规则 |
| rules | 仅程序支持的请求字段；type一致；枚举或区间二选一，区间开闭性显式；规则自身须可校验 |
| fixed_configuration | 锁定配方中影响运行的受管值及配方/实现摘要；不是可由用户覆盖的参数map |
| combination_policy | 与合同版本绑定的有限受管检查：本轮可使用已批准完整组合列表；不执行用户表达式或泛化DSL |
| execution_mapping | 每个可输入值的确切框架字段/CLI位置/单位与类型；固定字段来源；受管映射实现与镜像摘要锁定 |

Release解析时检查：同名重复、无实现参数、默认越界、相互冲突的枚举/范围、错误类型、空允许集、固定字段与可输入字段碰撞均拒绝。程序硬边界不因Release声明更大范围而消失。公开query由该已核验规则投影，不独立手写第二套范围。

对当前两字段，A07的基准/非默认测试可固定完整允许组合。组合表只是小型发布配置，不是参数搜索/自动调优系统；不要求未来所有连续空间都枚举。

### 5.2 新受理解析与规范化顺序

```text
按固定kind/request codec解码，校验结构/词法并保留显式字段
  → 规范原Intent，查原键；命中则按原事实重放
  → 仅新请求读取本次Gov绑定和确切Release
  → 检查程序合同、权限、数据/基模与环境能力
  → 克隆该Release defaults，覆盖合法显式字段
  → 检查字段硬边界、Release允许集及组合
  → 加入不可覆盖的受管固定配置
  → 按受管映射解析实际命令/配置值
  → 生成规范Snapshot与spec_hash，核对generation后持久受理
```

公共解析不能先调用CPU v1三参数校验再让FINETUNING解释剩余字段。程序/Release语义校验不是幂等键查回的前提，不能因当前允许范围收缩而拒绝原已接受执行的合法原键重放；当前用户访问授权仍需检查。

取值规范化沿用已公布的整数/精确十进制规则。W1固定省略/显式默认、参数顺序和等值合法数字的黄金向量；对语义不同的新执行参数，resolved配置和其内容摘要必须不同，不能被过滤或模板漏传。真实spec_hash还含其他执行事实，不能把“不同execution hash”单独当作参数生效证据。

### 5.3 快照里的批准值与运行中的实际值

原Intent保存用户显式值；Snapshot保存完整解析值、固定设置、参数合同版本、Release digest与程序映射引用。执行不得调用当前目录补值。使用原子快照承载，不新增另一张可独立编辑的前端配置表。

`ResolvedArgs`可继续用于明确启动向量；若当前框架更适合配置文件，entry在既有受管临时目录生成固定格式文件，路径由W0锁定，W1/W3只实现该已选映射，不同时维护两条后备路线。它必须由同一冻结参数和映射产生，不能以任意用户YAML/环境变量覆盖；不增加无用途的configs业务域。模板文件与最终生效配置不同，摘要各按所指内容记录。

参数内容摘要统一使用一个明确对象：`{parameter_contract_version, parameters, managed_settings}`，parameters按名称排序并用固定typed value，managed_settings仅包含W0/W1锁定的执行相关安全白名单；不含URL/密钥、临时路径、时间、UID、执行名称或日志计数。此对象从Snapshot解析得到的摘要称`resolved_configuration_sha256`；从框架实际解析结果得到的同形对象/摘要称`effective_configuration`与`effective_configuration_sha256`。两者同类型同规范字节才可相等比较，不能与整个spec_hash直接比较。

entry在原summary中扩展保存上述参数合同、resolved摘要、实际effective对象/摘要，W0/W1与现有summary codec一致落实；不改ani.metric.v1，不新建报告服务。锁定框架若对字段作了合法的显式派生，映射合同须预先将同一派生纳入resolved投影；无法预先解释的差异在训练前拒绝，不事后修改Snapshot。

summary是运行证据，不改写Snapshot。entry无法取得实际解析结果或发现值与原批准配置冲突，应在开始训练前失败；不会读取新默认值“修正”原任务。失败/停止后的步数仍从事实生成。

### 5.4 新能力演进而不重解释旧执行

当前CPU v1与微调codec分别解码，提供/消费方固定版本。已经发布且同语义的字段可以在新Release改变默认/范围；新字段、新派生规则或编码语义必须发布清楚可识别的新合同，保留旧请求的解码与重放路径。是否需要新增wire版本字段由后续确实新增字段的批次落实，本版不预放一个无使用者的通用版本选择器。

公共字段变更只按现有API流程追加或版本化，不复制Proto、不将显式参数变成自由JSON。一个新Release可复用同一PipelineVersion，只要参数输入位置、程序和文件结构没有变；改变Pipeline实现或输出结构时才生成对应新版本。参数取值不同产生新执行，而不是为每个学习率部署新Pipeline。

## D06 · 唯一TrainJob、KFP计算步骤与证明范围

### 6.1 训练入口

唯一TrainJob运行受管`finetune_entry`，调用锁定版本LLaMA-Factory。**本版基准Release的受管固定配置**为SFT/LoRA、target=all、rank8/alpha16、cutoff128、batch1/accumulation1、seed/data_seed42、CPU；关闭混合精度训练、flash attention、外部上报和额外中间checkpoint，保存最终Adapter。这些值归该程序/Release配方，不写成所有未来微调的公共常量；当前API仍不接受用户修改它们。learning_rate/max_steps消费原快照解析值。

M0以**实际锁定版本**验证所有argv/配置覆盖规则，不能把MLP的 `--recipe` 直接传给裸CLI。只启动一个训练进程；谁负责进程拉起由受管entry与Runtime共同锁定，不让CLI和Runtime重复启动分布式进程。

训练实际加载精度、Adapter保存精度、export精度、verify精度分别锁定。关闭fp16/bf16不自动决定导出dtype；本稿不猜填未经测试的版本或峰值。summary记录真实optimizer_steps、实际有效源样本统计及输入/基模/配方/Adapter摘要，不填期望值。[S0]

**指标和summary的唯一生成责任属于TrainJob内的受管 `finetune_entry`，不是collect/publish。**entry从锁定版本LLaMA-Factory的真实日志或callback采集训练事件，生成符合既有 `ani.metric.v1` 合同的 `reports/train/metrics.jsonl`，并将实际采集的结果写入 `reports/train/summary.json`。M0在两种来源中选定并验证本配方实际使用的一种，固定来源位置/事件、字段和类型映射、步数含义、采样规则、异常处理及实现摘要；不是要求首版同时维护两套采集器。[S0][S7]

`optimizer_steps`来自实际优化器执行事实，不能填请求中的max_steps，也不能直接用metrics.jsonl行数代替。采样点数量与优化步数可以不同，但必须符合锁定采样规则。有效源样本统计、输入/基模/配方/Adapter摘要使用实际事实；失败或取消留下的部分结果如实标明，不补齐期望值。

指标记录的字段/类型/必填项复用仓库已公布的 `ani.metric.v1`，本稿不另造字段集。该schema及现有解析器尚未核实时由W0/W1在M0核实并固定，不依据一个示意JSON生成正式合同。训练成功交付必须具备合同所需指标及summary；collect只验证格式、身份/摘要与必需文件并收集，publish只发布，不重新解析不明来源日志、伪造缺失指标或把训练标为成功。新增的指标适配与原CPU指标合同分别回归，见FT11.1–FT11.3。

### 6.2 export / verify不是额外训练作业

两个组件同步运行锁定程序，不能调用EnsureTraining，也不新建TrainJob。通过已有受管step入口验证实际workload、权威Run、原Task ID/Pod UID、原spec及D06.3的前序事实。**读取配置或通过一次身份检查，不等于已经受理本次启动，更不是永久或可转交的启动凭据。**

#### 6.2.1 首次启动与停止的共同仲裁

ModelDev在现有执行的**同一数据库事务串行化边界**下受理export/verify首次启动和写入停止/关闭标志；使用同一执行记录的行锁或等价的条件原子更新。只在组件内加进程锁、分别读stop再写启动记录，或在两个不相互排斥的事务中操作，都不满足合同。多实例ModelDev同样受这一边界约束。

身份与前序外部证据按现有可信验证取得；短事务内重新核对原执行/权威绑定、停止/关闭标志、原deadline、本步骤是否已受理，并持久保存首次启动事实。不得在事务中等待Kubernetes/KFP RPC、派生子进程或等待程序结束。只有首次成功提交产生本次启动分支，重复/查询响应不再产生启动分支。

| 必须固定的事实 | 约束 |
|---|---|
| tenant + execution + role | 每个执行的export或verify最多一次首次启动受理；复用现有记录，不增加重试attempt树 |
| spec、权威Run、原Task ID、原Pod UID | 与可信步骤身份一致；另一Pod/Task即使role相同也不能接管或重领启动 |
| 启动受理时间、已观察的关闭generation、步骤有效deadline | 使用本域权威时钟在首次事务中固定；export/verify预算从启动受理时间计，见D10，不从稍后实际spawn重新起算 |
| 实际启动/退出、结果与清单引用 | 后续运行事实；不能用“受理已提交”推定进程已运行、已退出或成功 |

字段名映射到既有等价字段即可，以上语义必须保持。配置读取、结果上报、启动受理和停止不得改变已冻结Release/输入/命令或spec。

| 竞争顺序 | 明确结果 |
|---|---|
| 停止/关闭先提交 | 拒绝首次启动，不派生export/verify子进程 |
| 首次启动事务检查时deadline已到 | 拒绝首次启动；Pod已存在不能绕过时限 |
| 首次启动先提交，停止随后提交 | 原启动已属于在途计算；停止流程必须跟踪该绑定，包括尚未spawn或启动结果不明的原Pod |
| 首次启动响应丢失、超时或相同请求重放 | 只查原记录/原Pod，不再次返回可执行启动分支，不换Pod、不重跑CLI；单次“查不到”也不能排除仍在途的原请求 |
| 原入口退出或owner重启 | 从原记录恢复观察/停止，不重新受理或续期；能证明原入口终止且不会再派生时，按实际事实关闭，否则待核实 |

**原子仲裁的是启动受理与停止，不是数据库提交与操作系统spawn。**启动已先受理时，停止提交后仍可能短暂观察到原在途启动，必须立即取消/停止并等到真实退出；不得宣称“stop一提交就保证所有进程已消失”。相反，停止在首次启动受理前提交时，实际启动次数必须为零。

#### 6.2.2 原入口、丢响应与关闭

原受管入口只消费首次成功返回的本次启动分支，最多派生一次原命令；查询、响应重放和Pod/服务重启不能再次派生。响应不明确时保持只读查证，不从“已有启动记录”推导可再运行。若因保守处理未启动，记录失败/取消/待核实的真实结果；本版不自动恢复该计算步骤。

入口在等待与准备派生期间处理取消和剩余deadline；尚未派生时终止该原入口的启动路径，已经派生时将取消/SIGTERM传给进程组并等待退出。owner先持久关闭，再使用原KFP/Pod身份停止/观察，覆盖所有启动已受理、在途或结果不明的记录；不能因当前没有子进程、缺少started_at或单次Pod NotFound而跳过。已经关闭的执行不能被迟到启动响应重新打开；必要的迟到结果仅作为事实记录。

禁KFP计算cache/自动retry，restartPolicy=Never；不后台遗留权重写者，不以重启计算生成“确定结果”。是否可CLOSED仍由A11的实际控制器/Pod/写者证据决定，不靠启动记录自身推断。

只扩现有role、绑定、启动/结果和清单事实，**不新增阶段调度器、创建许可平台、分布式事务或多Job生命周期**。本项是必须落到代码并测试的微调增量，不只是注释；FT23.1–FT23.5、FT24.1–FT24.2给出可重复的竞争测试。

### 6.3 具体证明对象

| 动作 | 必查对象 | 不可复用的错误证明 |
|---|---|---|
| 开始export | 原TrainingHandle/Observation、实际训练结束/无写者、Adapter与原输入关联；train-wait对应Task完成 | 整个KFP Run已完成 |
| 开始verify | 原export Task及Pod零退出，merged-manifest/文件字节属于本次 | export自报一条成功日志 |
| 确认publication | 原publish Task/Pod零退出，候选与实物大小/SHA/tar完全一致 | publish尚在运行时自报“已发布” |
| 最终CLOSED | 原Run/Workflow、唯一TrainJob控制器历史和全部相关写者，包括export/verify | 某个前序Task成功或当前Pod NotFound |

`runtimeproof/tasks.go`等白名单要按执行类型包含新角色；`ManagedRuntime.Publish`/`VerifyPublication`对微调只允许register确认，CPU仍只允许原close。不要改成任何已认证Pod都能确认任意publication。

M0除短推理外，应验证导出确实读取本次Adapter：记录基模/Adapter摘要和真实argv；用固定输入比较“基模+Adapter”与merged的一致性可作为负载验证。它不是模型效果评测，不要求每次跑大型评测集。

### 6.4 参数覆盖与实际框架消费的单一入口

W0/W1把“参数→实际框架字段”固定成合同向量。当前两字段直接映射learning_rate/max_steps；受管固定设置也从本次快照/锁定配方取得。entry按D05.3生成最终argv或配置，再核验框架实际解析结果，不能以“命令里出现了值”替代“程序消费了值”。

优先级只有一条：已冻结的受管固定配置不可覆盖；可输入字段使用已批准的resolved值。镜像内配方仅提供该Release锁定模板，不得二次覆盖合法用户值；冲突失败而非后写者胜。业务不允许的路径/命令/环境变量不进入参数通道；测试失败/慢速配方仍只由受管验收配置触发。

本版只采用max_steps控制训练长度，不对用户开放epochs。entry固定该控制方式，不同时把另一个训练长度字段作为可调项隐式参与。未来批准epochs时需在新合同中明确模式互斥/优先级，并验证锁定框架行为；不能沿用旧框架默认优先级却不给用户说明。

基准A与非默认B在同一受管程序上分别验证：请求/快照值、最终解析值、真实optimizer事件、summary和产物对应同一次执行。正常达到上限的结果与提前失败/停止的部分结果分别检查；指标行数不等于步数。首例非默认配置仍使用相同固定基模/JSON语义，不以参数测试为由恢复数据格式或新模型家族。

### 6.5 不新增计算与指标管理体系

参数解析在受理和入口各执行其限定校验，不触发额外TrainJob、导出Task或新的阶段记录。D06.2的启动/停止事务关系原样保留；参数数量增多也不能放宽role/Pod绑定或让重放重新spawn。effective配置摘要复用summary与日志/报告通道，不新建指标常驻服务，也不由collect补造缺失配置证据。

## D07 · 工作区、文件全集、tar与发布

### 7.1 路径与挂载

| 路径 | 内容 | 正常写入者 |
|---|---|---|
| inputs/data | 原样finetune_data.json、dataset_info.json | prepare |
| inputs/model | 核验后的固定基模 | prepare |
| training/adapter | Adapter权重、配置 | 唯一TrainJob |
| export/merged | 完整HF模型 | export Pod |
| reports/train | metrics.jsonl、summary.json、Adapter关联清单 | 唯一TrainJob内的finetune_entry；collect/publish只读校验与发布 |
| reports/export | merged清单与诊断 | export Pod |
| reports/verify | verify报告 | verify Pod |
| publication | inventory、candidate等非权威暂存 | collect/publish |

prepare建立本次后续所需的空子目录并写入输入，不写训练/导出权重。TrainJob只读inputs，写training及reports/train；export只读其前序输入，写export与reports/export；verify只读merged，写reports/verify；collect/publish只读产物，写publication与受限spool；register/close只读必要报告，不持Model管理证书。

不存在“外部TrainJob自动继承KFP卷挂载”的假设；两处分别配置。所有写子路径明确分离，允许合同中故意共享的只读父路径；不要把父目录和子目录的合法只读关系误判成写入重叠。目录创建与K8s subPath存在性在目标CSI中验证。

UID/GID10001、只读根文件系统、受管缓存/临时写目录与CPU池配置按已验证Runtime/组件材料应用。多租户启用范围依据实际证据，不因设计有两个租户字段就声称任意隔离均通过。

### 7.2 文件全集与主tar

微调RequiredFiles相对workspace根；CPU保持原training根和叶文件合同。M0固定当前镜像、基模与批准组合的真实文件结构：config/tokenizer、完整safetensors或实际shards/index；另外收集Adapter、entry实际metrics/summary、verify和清单。summary新增安全参数证据不新建参数存储域。缺文件、未经批准的额外文件、同次读取摘要冲突均拒绝；结构或上限变化要更新相应合同/Release。

**固定的是获准路径/角色/结构及上限，不是首个样例的权重SHA或准确文件长度。**不同参数产生不同权重摘要是正常运行事实；收集器不得要求等于M0基准SHA。非默认测试要核对实际文件结构仍满足合同，或者先修订并验证对应新文件合同；不能直接禁用多文件/上限检查。

主模型使用**未压缩USTAR tar**，只纳入 `in_tar=true` 的 `export/merged` 文件；成员名剥离该前缀，在包根直接出现HF文件，保留必要内层路径。不把export/merged目录套进tar根，不放Adapter、平台output-manifest或诊断报告。

Adapter权重/配置/关联清单作为独立工件保存。file SHA、manifest SHA、tar SHA分别表示各自字节；Model的checksum为整个tar SHA，format为模型文件格式（本例safetensors），不是把format写成tar。

不强制重写已稳定的CPU打包规范。微调tar禁止路径碰撞、穿越、link、设备/FIFO及非批准PAX扩展；标准tar尾部零填充允许，非零附加数据拒绝。重放读取原已固定tar，不重新打包并期待随机元数据恰好相同。

### 7.3 发布与确认

大文件流式哈希/捕获到磁盘spool，捕获后再上传原执行前缀；检查真实大小、路径和读期间变更。逐级fd/O_NOFOLLOW、受控文件类型和多链接检查沿用并扩展现有安全I/O，不改成简单拼路径。

publish返回候选，不能自证自己的Pod已经结束。register前半段复用独立核验，验证原publish身份/终态以及远端每个固定对象的实长/SHA、完整manifest和主tar，再事务持久publication。失败不调用Model，不能只上传一个manifest就标PUBLISHED。

**M3-A不等待BFF或Model登记，但不能省略上述发布确认。**保持同一Pipeline/组件顺序与实际proof；Model边界未接入的测试装配应在确认后返回明确依赖不可用，记录PUBLISHED与真实关闭，不能直接跳到正常成功close或写假registration。这个预期失败可满足对应主链测试断言，不满足A10单次业务成功；后续替换为真实Model adapter无需重写发布/关闭路径。独立S3取回校验不挂训练PVC，不等于FT29的Model→Inference验收。

直接对象引用或必要复制以受理前的绑定确定，详见D08；没有经验证的绑定不得正式启用该Release。不要同时开发两套动态fallback模式。

## D08 · Model需求交接：先留接缝，提供方就绪后接入

本节是 **ModelDev向Model提出的具体领域需求**，不是新的服务间调用规范。传输/身份/委托/超时/重放统一引用Gov合同。Model提供方先确认现有API是否足够；不足的窄能力由提供方正式发布，消费方再固定依赖。不得以本节示意方法名复制一套Model Proto。[S2]

### 8.1 本次职责与就绪条件

| 编号 | Model需要提供/保证 | ModelDev需要完成 | 正式联调证据 |
|---|---|---|---|
| MR01 可信接收 | 在已有业务mTLS入口按服务+完整RPC+用途建立可信上下文；检查tenant与资源；不信任body自报Principal | 重建metadata；只从已验证Gov委托/持久受理构造请求；不冒用Gov证书 | 无证书、错服务、越权方法、冲突tenant、伪造actor拒绝；合法原范围通过 |
| MR02 基模读取 | 返回已批准精确版本及真实size/SHA/format/加密/固定引用；短期下载始终同版本 | 冻结读取事实、临近使用才签发能力、流式核验 | 固定版本读回；源同key新版本不替换基模；禁止任意版本读取 |
| MR03 目标与名称 | Model.id与外部标识明确；目标归属/可写检查；租户内名称唯一，新建重放回原模型 | create-only与existing分支固定；预检仅提示/拒绝，不承担最终唯一性 | 同名新执行冲突；原键重放同模型；显式existing新增版本 |
| MR04 版本登记 | 原key+原业务内容幂等；完整登记内容一致性；可靠finalization | 持久原登记意图；具体写派发按D09.3与停止仲裁，不以整份意图代替在途事实 | 一次结果；同key异内容拒绝；丢响应/部分提交及停止竞争 |
| MR05 原结果查证 | 限定原tenant/业务键返回未创建、pending/部分完成、READY或冲突及原字段；READY-only旧接口含义不变 | 先查证再按合同有限重放；不拿404单独当无副作用 | 响应丢失能找回；不会创建第二版本；未完成状态不可当READY |
| MR06 固定对象与下载 | 固定版本选择器或受管不可覆写key贯穿持久引用、校验、下载和重放 | 不将versioned object降成latest；tar SHA/size与原发布一致 | 同key后来内容改变，仍下载原内容；长度/SHA不符拒绝 |
| MR07 存储交接 | 明确当前作用域可否接受原publication引用；需上传时票据不另建业务版本 | 固定一条经验证绑定；仅必要时复制，不动态切模式 | 实際从Model读取同一tar；配置缺失明确未启用 |
| MR08 删除保护 | 批准基模/登记成品的保护在实际删除路径生效；保护建立时机明确 | 在批准、引用和记录时使用权威事实；不靠隐藏按钮 | 删除与批准/登记竞争时不会删掉受保护资产 |

本版所需MR能力在当前选定绑定上真实完成，是正式启用及本方案最终验收的必要条件；**不是要求ModelDev开发人员同时实现两个仓库**。Model暂未交付时，这些接口可以先用边界替身进行消费者合同测试，但不能把替身的权限/事务结果算成MR已通过。

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

在外部首次写入前，ModelDev持久原登记意图；new分支先请求容器、记录/找回原Model UUID，再形成版本请求。**意图存在不等于这些调用都已在途；每个具体写操作取得派发资格按D09.3与Stop/deadline共同仲裁。**容器响应不明按原key查证，不按名复用；其迟到成功也不自动授权下一次版本派发。

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
| export/verify | 原role/Task ID/Pod UID绑定，启动受理时间/步骤deadline、实际启动/退出及清单；首次启动与stop共用D06.2的执行级原子仲裁，不能用记录推进DAG |
| publication | 扩原输出合同和核验；CPU由close确认，微调由register确认 |
| registration | 原意图/内容、具体写操作的派发在途事实、收据、fingerprint、状态和有界错误；复用现有记录，不为每个RPC新建平台 |
| close | 原停止generation、在途资源、控制器/实际写者证明；纳入新增组件 |

逻辑记录可以由既有表加可选字段或有限新增表承载，由仓库当前模型决定；不要求每一项单建服务/状态机。对外状态由同一事实投影产生，保留旧CPU语义。

### 9.2 原登记恢复

仅当真实训练和export/verify成功、publication已确认、原登记事实存在、执行CLOSED且无关闭待核实时，才能接受恢复。Gov核对当前操作者对原执行及原目标的权限，持久一条恢复命令；ModelDev使用其原记录执行，不信任用户body替换业务内容。

恢复不启动KFP、TrainJob、export或verify，不挂原训练PVC，不改变旧计算deadline。需要原绑定的存储交接时只从已发布对象读取，用原upload/registration key完成；它是有界交付I/O，不是重新训练。

原正常登记和恢复共享同一记录，用有限租约/CAS防止并发混乱。若Model写入已在途，旧处理者可能仍返回结果，因此仍必须依赖Model原key幂等并核对内容，不能把lease当作跨服务绝对一次执行证明。

同名/内容冲突停止自动重试；依赖暂不可用允许按原内容有限重试。所有变化只在允许的登记状态发生，不能为了“恢复”重开计算。没有原publication或原登记意图时，不将该接口泛化成手工发布任意文件的入口。

### 9.3 登记具体副作用派发与停止共同仲裁

本节是v4.2.1审核R1的必要补齐。[S11] 复用本域执行/登记记录；不增加通用授权服务、许可平台或跨服务事务。**登记意图固定的是原目标和内容，不是后续所有副作用的永久派发资格。登记worker之间的lease/CAS也不能代替它与Stop共同仲裁。**

#### 9.3.1 同一执行级串行化边界

正常登记每个具体会产生外部副作用的操作，在取得本次派发资格时，必须与Stop/关闭/deadline共用同一执行记录行锁或等价数据库原子条件。事务内重新核对原执行/spec、登记阶段、业务键/内容、原目标、停止/关闭状态及剩余register/执行预算，记录该具体操作已获准在途；事务外才执行RPC或对象I/O。多实例同样受约束，禁止事务内等待网络或文件传输。

记录至少能区分“原登记意图已存”“哪项具体操作已获准派发”“其结果未明/已确认”。沿现有字段增量承载，不要求新建一套表。new分支的CreateModel、CreateModelVersion分别仲裁；若当前选定存储交接会真实写目标对象，该有限写操作也明确原key、范围及在途归属。纯只读查证和字段转换不新增派发许可记录。

每次**新的实际重发**也要检查当时Stop/deadline和原内容；不能凭停止前取得过一次许可，在停止后任意重发。正常登记只对提供方合同允许的瞬时错误有限重放，依赖原key幂等；未知结果优先查证。关闭发生后只允许原结果记录和限定只读查证，不再产生新的正常写派发。

#### 9.3.2 谁先提交，结果是什么

| 提交顺序/情况 | 必须行为 |
|---|---|
| Stop先于某具体派发提交 | 不产生该新的正常外部写调用；已有意图不豁免 |
| 仲裁时执行或register预算已到 | 拒绝派发；重放/重签/重启不重置原预算 |
| 该具体派发先获准，Stop随后提交 | 保留为原在途请求，响应可迟到；取消可取消的本地等待/I/O，原key查证并保存真实receipt |
| CreateModel已完成或迟到成功，版本派发尚未获准 | 停止已生效则不得再发CreateModelVersion；容器收据保留，不按同名自动追加或自动删除 |
| 版本写响应丢失/worker重启 | 找原操作、原内容/原目标；不换key、不用一次NotFound推定没有写入，不重训 |
| 已CLOSED后用户明确恢复原登记 | 走D09.2新的当前授权恢复命令与有限预算；仍用原内容/key，不重开计算，不被误写成永久禁恢复 |

**仲裁的是本域“是否获准派发”，不是把本域Stop与远端Model事务变成物理原子操作。**先获准的原RPC可能在Stop之后才真正到达或写成READY；必须如实记录，不能声称Stop撤回了远端写入。迟到响应本身不产生后继写操作的许可。

#### 9.3.3 在途、重放与关闭的边界

原normal handler与恢复worker共用登记记录的有限lease/CAS；仍须依赖Model的原key/内容幂等与查证，不能把lease当绝对一次副作用证明。首次派发资格丢响应、进程重启或传输结果不明时，按原记录处理；查询记录不自动重发，明确重发也必须通过相应原内容和当前时限/停止检查。

计算CLOSED不以Model READY为前置，也不因远端结果未知而编造成功。原进程/权重写者必须按既有关闭条件结束；远端Model业务结果可在关闭后继续限定查证并作为迟到事实保存。受控复制若仍有本执行服务侧实际写入者，必须取消/收齐或保持未解决，不能借“只是登记”遗漏。清理不得删除未知写入引用或唯一未发布产物。

W4与现有关闭负责人在允许真实登记写入前实现本节，并把同步竞态加入FT21/FT25；W0/W1及不调用Model的主链开发不等待该提供方上线。已CLOSED恢复不修改旧计算deadline，不扩大目标或数据，原KFP历史不改写。

### 9.4 错误与查询

原CPU公开错误、未启用行为不变；新增微调错误在同一服务错误映射定义。查询、下载、日志、恢复重新检查当前权限及数据范围，不从当初创建者身份自动授予现在的访问权。

当前已支持scope以实际CPU主干为准；若当前仅支持tenant-all，更细SELF/组织scope仍明确拒绝，不降级放大为tenant-all。该拒绝可以证明安全边界，不能标为已支持原v0.4 C02完整产品能力。

### 9.5 有效配置展示与现有前端衔接

共享投影/数据合同现在实现，**实际BFF/前端联调在M3-B完成，不作为M3-A前置**。先用真实查询用例和严格回放样例验证本域数据，但不能据此将PA05/PA07/PA10的前端部分标通过。

创建页继续从预设读取信息，按D02.4显示可编辑值、固定项及原因。不开新的前端配置权威；固定项可见不等于能写入请求。无已批准规则时显示未就绪，不把模板示例当成可运行范围。

详情页读取原执行的Intent/Snapshot，只返回安全白名单；预设更新后原默认值和参数仍不变。实际指标/summary单独展示“已完成步数”等运行事实；尚未启动的执行不显示伪造effective值。查询权限沿原scope/动作边界，不为查看参数放开完整内部快照。

创建期间切版采用A07.6唯一规则；收到受理后以返回/详情中的真实release与resolved配置为准。前端可以提示“按受理时版本执行”，不得自动将用户选择改为服务器默认，或使用原幂等键发送一份修改后的请求。范围拒绝需要刷新后由用户形成新意图。

回归至少覆盖max_steps=20/10等整数正确投影、当前Release匹配、同预设历史Release共存、旧任务在默认值改变后展示不变、只读固定项不进请求、敏感字段未泄露。完整验收见PA01–PA10。

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

每个步骤沿用现有固定预算与原execution.deadline_at。**export/verify新增的有效deadline在D06.2首次启动受理事务中固定为 `min(start_admitted_at + 该步骤预算, execution.deadline_at)`，不等待操作系统spawn才起算。**实际started_at仅作观察事实；延迟收到响应、子进程稍后拉起或重放不能续期。其余步骤保持既有首次开始与时限语义；TRAIN负载与train-wait共享一次TRAIN预算，不能分别获得3600秒。查询重放、重签和重启不重置任何已固定的起算点。

复用已有deadline/取消机制，将预算传到KFP timeout、Runner context、子进程、RPC/I/O；不能只改Snapshot还留下原统一1800秒限制。旧CPU默认值和执行行为保持不变。逐RPC是调用deadline，整个登记多次RPC共同消耗register预算，不每次重新领取600秒。D09.3的每次具体派发及合同允许的重发都使用该原剩余预算；当前后置BFF不放宽任何执行/停止时限。

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

M0先验证20步基准，再验证A07.4非默认候选及对应预算。只有基准点时不批准更大集合；PA用例记录每个批准组合的时间/内存/磁盘/失败证据。扩大范围前验证，不给运行中任务续期。普通参数开放不自动增加动态资源估算或用户自由改资源能力。

### 10.1 下一批参数的开通清单（暂缓字段，不是隐藏开关）

| 候选项 | 批准时需要写明 | 本版状态 |
|---|---|---|
| batch_size / gradient_accumulation_steps | 真实框架字段、每设备与累计含义、允许组合、实际优化步统计 | 不作为本版请求字段；固定1/1继续生效 |
| cutoff_len | 截断语义、数据预处理可见影响及内存/时间预算 | 固定128；候选范围尚未批准 |
| lora_rank / lora_alpha | 分别独立可调，或明确派生规则；规则与派生结果冻结 | 固定8/16；不默认alpha=2×任意rank |
| epochs | 与max_steps的训练长度模式、互斥/优先级、稳定请求codec | 本版拒绝；后续合同明确后才开放 |
| scheduler/warmup/weight_decay等 | 具体程序是否消费、类型/边界/组合与资源影响 | 后续产品决定，不自动透传 |

下一批批准流程仍是W0/W1/W3/W6的同一小批迭代：支持字段与映射→候选规则→代表性/边界/反例→固定Release→真实查询和执行一致。无需穷举所有连续数值，但高负载组合必须有依据；若只允许完整组合列表，逐项限定，不把独立上下界误当任意组合都能运行。

镜像/程序、预算和输出合同未变时，可复用PipelineVersion；实际输出结构、程序或组件参数签名变更，发布相应新版本。不得因一组小配方成功承诺所有资源规格/基模/设备支持。

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
| FT11 | 单TrainJob真LoRA与指标适配 | 一个权威TrainJob UID；真实优化步数/Adapter，无GPU/重复训练；finetune_entry生成ani.metric.v1与真实summary；细化见FT11.1–FT11.3 |
| FT12 | 未知创建结果 | 只找原资源，迟到响应与多Run不导致第二份计算 |
| FT13 | export正确来源 | 基模+本次Adapter导出真实merged；关联摘要/argv完整，不只加载一个基模充数 |
| FT14 | verify独立加载 | 单独进程、锁定精度、greedy短推理与实际报告，失败不得登记READY |
| FT15 | 前序Task证明 | 不等整Run终态；错误前序Task/Pod UID、未退出的publisher不能放行 |
| FT16 | 文件与tar | 缺/多文件、link/穿越、错前缀/非零尾随拒绝；HF根布局、Adapter独立正确 |
| FT17 | 持久发布 | 原publisher退出，远端每个固定对象及主tar字节核验后才PUBLISHED |
| FT18 | 当前存储交接 | 实际选定模式正确；Model固定读取原tar；未启用模式不写PASS |
| FT19 | Model可信链 | 真服务身份/完整RPC/委托范围/tenant检查；拒绝假Principal与越权方法 |
| FT20 | 登记一致性 | 原容器/版本，全部业务字段一致；READY重放返回持久真值 |
| FT21 | 登记故障窗口 | 容器响应丢失、Artifact写后中断、READY响应丢失；原key恢复不造新版本；与FT25.1–3联合验证逐写派发边界 |
| FT22 | 正常close | NATURAL_TERMINAL收据后返回，Run自然成功终结；无自等待/无主动杀正常Run |
| FT23 | 用户停止与首次启动竞态 | 各阶段实际停止；stop先提交不启动，先受理启动纳入在途关闭；不新受理/重复派生，见FT23.1–FT23.5 |
| FT24 | deadline/重启 | 首次启动原子检查deadline；owner重启按原启动事实继续观察/关闭，不重跑/续期；见FT24.1–FT24.2 |
| FT25 | 登记派发/停止/恢复 | 具体派发与Stop/deadline共同仲裁；先派发的迟到receipt如实保留，未获准的下一写不执行；CLOSED后当前授权恢复，见FT25.1–3 |
| FT26 | 状态与完成层级 | compute成功不盖verify/登记失败；Model READY+CLOSING不全成；单次执行与方案验收分离，见FT26.1 |
| FT27 | 大文件/容量 | 实際spool目录、磁盘/内存界限和流式I/O；超限安全失败，旧CPU默认不变 |
| FT28 | 保护与清理 | 基模/READY/成品实际删除受限；唯一未发布/未知写者阻断清理，UID plan/apply一致 |
| FT29 | 方案验收：独立新模型使用 | 无训练PVC，经Model取本次新tar并通过现有Inference/KServe真实调用；不是每个任务的后继步骤，见FT29.1 |
| FT30 | 原CPU合同回归 | 旧CPU v1字节/摘要/错误/拒绝集与相关训练/发布/关闭保持，必要旧48步链通过；不将旧拒绝集作为未来新合同永久限制 |
| FT31 | 自动业务与交接 | 正常提交无需人串步骤；非原作者按实际入口完成启用/执行/恢复 |
| FT32 | 来源与范围 | 记录真实commit/digest、环境/资源/对象事实，暂缓项不标PASS，凭据不入证据 |

以上32项是计划，不是本次执行结果。MR提供方未就绪时，FT07/08/18–21/25/29中依赖真实Model的部分为BLOCKED或NOT_RUN；结构替身通过只记录相应消费者测试。FT11/13/14可先做Fedora真实负载，但不冒充LIVE。


### 11.2.1 继承v4.1的12项细化子用例

保留FT01–FT32编号，以下12项是已有FT11/23/24/26/29下的实施与验收细化，不代替父项的其他检查。初始全部NOT_RUN。export与verify共用FT23/24的同一组测试合同，但两种role都必须实际覆盖，不能只测其中之一。

| 子用例 | 触发/安排 | 必须证据与结果 |
|---|---|---|
| FT11.1 | 锁定的20步真实训练，entry按M0选定日志/callback生成文件 | 现有ani.metric.v1解析器校验通过；记录来源/映射/采样版本、真实文件与SHA；采样数量不被当成优化步数 |
| FT11.2 | 正常及受控提前失败/停止，比较请求上限与实际执行 | summary如实记录实际步数/样本统计及部分结果；不得填满请求max_steps；保留原错误/停止事实 |
| FT11.3 | 缺指标文件、破坏schema或人为改写summary必需事实 | 现有校验/collect拒绝当成完整训练产物，不补造记录；失败证据不冒充成功交付 |
| FT23.1 | 同一执行的stop事务提交后，再进入export/verify首次启动事务 | 启动被拒绝，实际子进程次数为0；证明两路径使用同一数据库串行化边界 |
| FT23.2 | 启动受理已提交，在入口spawn前暂停；提交stop后释放同步点 | 原启动作为在途计算保留；入口取消或迟到原进程被停止；不能早记CLOSED；无第二次启动 |
| FT23.3 | 并发首次请求、相同请求重放及另Task/Pod UID请求 | 每个execution+role最多一次首次受理；不匹配绑定拒绝；重复响应不触发新子进程 |
| FT23.4 | 首次启动事务已提交，丢失返回；随后查询/重放 | 只返回/查证原事实，不补发启动能力、不更名重跑；记录无法确认时的待核实/关闭路径 |
| FT23.5 | export/verify子进程实际运行后提交stop，向原进程组传播取消 | 收齐原入口/子进程退出与原Pod/控制器证据；无后台权重写者，不以单次NotFound代替证明 |
| FT24.1 | 首次启动事务检查时已到deadline，另测已受理但延迟收到响应 | 到期首次请求不启动；已受理请求仍用原start_admitted_at/deadline，不因响应延迟重置预算 |
| FT24.2 | 启动已受理/已运行时重启owner，或原入口退出未回结果 | 恢复原绑定与时限，只观察/停止；不重建计算；有真实退出证明才关闭，未知保持待核实 |
| FT26.1 | 六项执行事实成立但FT29尚未执行，另测停止/登记恢复历史 | 记录单次执行结果与单独方案验收状态；不自动创建推理服务，不抹掉历史，不据局部通过正式启用 |
| FT29.1 | 相应Release/Runtime验收使用本次真实微调登记版本独立部署/调用 | ModelVersion、publication/tar SHA和实际Inference请求结果可关联；无训练PVC；使用旧基模或仅fake响应不通过 |

并发测试使用测试环境中的可控事务/进程同步点，明确stop与启动的提交顺序，不能用随机sleep碰运气。至少覆盖数据库多连接/多owner竞争；同步点只存在测试夹具，不增加生产“暂停/发放启动”管理API。Fedora模块/负载结果与目标集群结果分别记录，原子逻辑自测不代替真实Pod/进程停止证据。

### 11.2.2 v4.2新增参数链验收（与原FT编号并存）

本版新增PA01–PA10，不重编号FT01–FT32，也不删除v4.1的12项细化。PA是本次明确采纳的参数/展示验收，不代表开放所有候选字段。各项按FEDORA_PASS、实际API/前端联调、LIVE等适用层级分别记录；Model未就绪仅阻塞真实依赖部分。M3-A先完成执行侧参数检查，真实BFF与前端相关断言按D11.6留到M3-B；后置项NOT_RUN，不是DEFERRED，也不能将部分验证升级为整项PASS。

| ID | 场景 | 必须观察 | 关联/责任 |
|---|---|---|---|
| PA01 | 参数合同与Release规则自洽 | 两字段类型/硬边界/默认/允许集/组合合法；错类型、重复项、无实现字段、默认越界、规则冲突明确拒绝 | FT06/30；W1 |
| PA02 | 预设数值与生效版本投影 | max_steps=20/10正确而非64；十进制值准确；Gov绑定ID/digest/generation与一份规则一致；历史Release共存和切换无混合投影 | FT06/26；W1/W6 |
| PA03 | 原意图、省略与重放 | 省略/显式默认按固定向量编码；当前规则收缩/默认改变后原键合法重放仍返回原执行；不同显式意图冲突 | FT06/30；W1 |
| PA04 | 基准与非默认真实负载 | A与经批准B分别经entry实际解析、真实LoRA/导出/verify；至少一个非默认配置，实际消费值、步数、产物有对应证据，不只改argv | FT11/13/14；W0/W3 |
| PA05 | BFF到原快照到程序贯通 | 非默认合法值经现有BFF受理，冻结相同值并在真实目标执行；Model接入前只分段记录，不用fake冒充正式BFF整链 | FT06/11；W1/W3/W6 |
| PA06 | 拒绝和固定字段保护 | 未开放字段、越界、非法组合、固定设置覆盖在计算前拒绝；镜像模板与冻结值冲突不静默覆盖或裁剪 | FT02/06/12；W1/W3 |
| PA07 | 配置详情与历史不漂移 | 创建用当前规则，原任务详情读原快照；切版后旧参数/固定摘要不变；运行步数来自实际summary；前端只读展示不改变原请求 | FT06/26；W1/W6 |
| PA08 | 指标、参数和部分结果 | summary有效配置与快照可核对，真实optimizer_steps不同于日志条数/请求值；B配置提前失败/停止不填满上限 | FT11.1–3/23/24；W0/W3 |
| PA09 | 组合预算与文件合同 | 批准B及必要边界的时间/内存/spool/输出限制受测；不同权重SHA不被误拒，缺/多文件和真实超限仍拒绝；停止/deadline不改旧语义 | FT16/27/30；W0/W3/W4/W6 |
| PA10 | 能力发布和最终声明 | 只有基准证据时不能标参数开放完成；提供/消费方合同固定、原参数规则与新批准Release并存、相关回归与实际前端接口复演完成 | FT31/32；W1/W6 |

测试A/B使用同一批准数据/基模和明确不同execution；判断参数生效需比对参数内容与实际消费，不能只看两个spec_hash不同。组合表改变后对受影响用例复测，失败值不自动进入允许集。PA01/03模块测试不要求Model就绪；PA05对应的完整LIVE仍受原MR/环境前置约束。

W1当前先交付正式BFF响应合同、安全投影与显式回放样例，不要求已有真实BFF响应才能完成主链模块。M3-B补真实响应和页面证据；页面代码未在授权范围内时如实保留未执行，不拿手写表格当联调。原A/B与省略默认向量直接复用D02.6.1，当前共享解析与后续BFF/前端分别记录，不新增参数用例体系。

### 11.2.3 登记派发仲裁细化（新增3项，挂在原FT25下）

下列细化补齐D09.3，不新增主FT编号；原12项细化完整保留，本版合计15项细化。用真实数据库多连接和可控同步点验证本域事务，Model边界替身可观测调用次数但不算MR通过；提供方就绪后补相应真实链证据。

| 子用例 | 触发/安排 | 必须证据与结果 |
|---|---|---|
| FT25.1 | Stop先提交，再让具体CreateModel或CreateModelVersion进入派发仲裁；另测到期分支 | 该外部写调用次数为0；原登记意图不放行；所选复制模式如有目标写同样检查，不把只读查证误禁 |
| FT25.2 | CreateModel成功并保存收据，暂停版本派发；Stop提交后继续 | 原模型容器收据保留，CreateModelVersion新调用数为0；不自动换目标/key、不删除已建容器；D09.2显式恢复仍可按原内容另行验 |
| FT25.3 | 版本派发先获准，随后Stop并丢响应；重启/迟到响应与正常重发竞争 | 只查证/保存原在途事实；正常后继写和新的正常重发不再获准；不双版本、不重训、不把远端完成改成未发生；CLOSED后当前授权原恢复不冒充正常后台续跑 |

对当前选定复制绑定，附记具体写操作的在途归属和取消/残留证据；无需为未选模式补一套实现。记录原execution/operation、原业务key、请求内容摘要、派发提交与Stop顺序、原预算、外部调用次数和原receipt。只读对账/字段转换没有副作用，不为它们建立许可记录。R1可在Fedora先验证，真实Model断言仍待M1-B/M3综合，不阻塞无关主链工作。

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
| C23 | 新HF包推理 | 作为本方案最终验收必测；未通过不能宣布方案验收完成；不加入每次微调任务的成功判据 |
| C24 | 停运行/排队 | 含export/verify首次启动与停止共同仲裁，以及实际在途/运行进程停止 |
| C25 | 停止/迟到创建 | 继承，加入先受理后stop的迟到组件、启动响应丢失及原登记事实 |
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


### 11.4 本方案最终验收条件（不是逐任务后继步骤）

本版最终验收要求：FT01–FT32、继承的12项细化和D11.2.3新增3项按适用真实层级通过；PA01–PA10包括至少一组非默认真实BFF→原快照→执行贯通、真实当前预设/历史详情及已有前端联调；相关CPU回归完成。Model按MR真实接入，FT29/C23使用本次登记新包完成独立Inference，M4交接。**这是整版终点，不是当前主链前置。**M3-A先按D11.6核验执行/产物子链，M3-B后补上游；不拿CODE_READY或替身抵扣真实断言。

只完成基准点，可以声明“固定配方分段/负载通过”，不能声明v4.2参数链及本方案最终验收完成；反过来PA通过也不抵扣MR和原微调闭环。范围只扩到本版明确的两个可输入字段、规则承载与展示，新字段候选仍DEFERRED。

**A10判定一项任务是否成功完成，D11.4判定本方案及相应锁定组合是否最终验收完成。**每次正常微调自动执行到模型登记和计算关闭，不必自动上线推理；单次执行已成功但FT29尚未执行时，只能声明该执行或子链通过，方案最终验收仍为NOT_RUN/BLOCKED。FT29失败时方案验收为FAIL，不能倒改已经成立的单次任务事实。

本条保持A09的正式启用约束：候选负载/受控验证可以逐步进行，必需依赖和正式验收未满足前，不把候选公开宣称为正式可用。原v0.4中明确DEFERRED或部分覆盖的事项继续按D11.3表述，不为了“全通过”改原用例含义，也不扩大本期范围。

### 11.5 GPU前置证据独立，不增加本版验收前置

FT01–FT32、原12项细化、PA01–PA10、MR01–MR08及C01–C34映射继续保留，D11.2.3的3项登记细化是本次CPU正确性修订而非GPU验收；C33的CPU拒绝GPU继续必测。D10.2只定义可独立开展的接缝核对和准备，不将GPU渲染、硬件、owner注册或占退额纳入这些用例的通过条件。

已开展的GPU检查使用独立`scope=GPU_PRECHECK`记录，逐层保存结果；尚未开展或条件不具备，如实NOT_RUN/BLOCKED，不影响已满足D11.4的CPU交付判断。反之，GPU检查通过不抵扣任何FT/PA/MR，也不证明BFF→Gov→ACC→ModelDev业务已贯通。没有为GPU准备另加一组必须通过的GP用例，不把此准备视为Model真实接入的替代。

若GPU调查暴露了同时影响当前CPU正确性、隔离或关闭的真实缺陷，仍须按原受影响用例修复回归；不得利用“不阻塞”忽略本版本身的问题。纯GPU支持缺口则进入下一批，不更改本版成功/关闭判定和硬件前置。

### 11.6 当前主链、后续入口与最终验收的执行映射

以下scope只写在测试/进度记录中，**不是新的业务状态、API字段或运行模式**。FT/PA/MR保持原编号与完整断言；一项含多段时分记录coverage/未测断言，不以本域测试PASS覆盖整项。按排期后做填NOT_RUN；只有实际前置缺失才BLOCKED；只有本版真正不交付的上传/GPU等才DEFERRED。

| 证据范围 | 当前或后续的完成条件 | 关联用例与尚不能宣称 |
|---|---|---|
| WORKLOAD：M0 | Fedora上基准和非默认真实entry/LoRA/export/verify、指标/summary、参数消费与资源材料 | FT11/13/14和PA04/08/09的负载部分；不证KFP/Trainer、Gov授权或Model |
| CORE_FLOW：M3-A（当前阶段） | 真实ModelDev应用/数据库、KFP/Trainer、S3；READY/原快照/唯一训练、export/verify、发布确认、脱卷取回；失败/幂等/停止/deadline/重启及相关CPU回归；先基准再非默认 | FT01–04的本域部分、FT11–17、FT22–24、FT26–28/30/32的适用执行部分，以及PA01/03/04/06/08/09执行侧；未验证的Gov/Model/页面子断言单列 |
| MODEL_INTEGRATION：M1-B及综合 | MR01–08实际使用的正式API/身份/数据库/固定对象与登记查证；登记派发竞态对应真实验证 | FT07/08/10/18–21/25/28中Model断言；不要求先完成页面，也不自动证明Gov用户链 |
| BFF_INTEGRATION：M3-B（后续阶段） | 真实BFF用户认证、当前绑定/默认值、目标使用/写权、幂等冻结与投递、查询/停止/恢复，实际前端字段行为；候选绑定按D12.7 | PA05及PA02/03/06/07/10的上游/页面断言；FT05/06/09/25/26/31对应入口事实；不得拿测试来件或静态页面抵扣 |
| SCHEME_FINAL：M3综合/M4 | D11.4全部适用断言与回归、真Model、真实BFF/前端、FT29/C23新模型独立Inference、交接 | 保持整版原承诺，不要求每次任务自动部署推理 |
| GPU_PRECHECK：独立准备 | 仅D10.2约定的合同/字段与有条件渲染/设备观察 | 不阻塞上述任何CPU阶段，不代替FT/PA/MR |

**M3-A主链阶段最小收尾：**至少一份实际发布的模型tar/Adapter/指标/summary可脱卷取回并核对；真实训练/export/verify前序事实完整；实际停止不遗留计算；原请求重放、重启与deadline不造成重复计算；基准与至少一个非默认配置的实际消费可关联。第一条基准成功可先作为检查点，不要求首次试跑一次满足所有阶段用例，但完成主链阶段前需补相应正确性和非默认检查。

Model未就绪时，保留register前半段的真实publication确认；后半段明确报依赖未接入，再以原失败/关闭路径收尾。测试可以验证“PUBLISHED + 依赖错误 + CLOSED”这一预期结果而报告CORE_FLOW片段/阶段达标，**不能将该执行写成A10六项齐备的成功或将MR/FT29标PASS**。基模真实读取未接入时使用的批准素材及替代边界另记，FT10真实Model部分保持未测。尚未实际跑KFP/Trainer时只能报WORKLOAD/模块通过，不能报CORE_FLOW完整阶段通过。

FT22正常收尾的本域收据/自然关闭分支可先做模块验证；Model未就绪导致当前完整Pipeline以依赖错误收尾时，不将该失败路径充当FT22真实正常成功链，完整正常链随Model接入补测。

真实BFF未就绪不妨碍现有正式合同的解析与本域投影测试；但Gov绑定/权限/可靠投递必须由后续真实链证明。后置事项在`records/acceptance-plan.json`及实际结果中保留未测，不能因阶段达标而删掉。上述映射用于排期，不变更用例原语义，不能把所有FT标签统一标成CORE_FLOW后整项通过。

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

本交付包`spec.md`为完整v4.2.2正文；两册HTML、主流程开发手册、参数/前端视图、GPU边界、Model需求、W0–W6及验收计划全部同源。采用前保留v4.2.1与已有实际进度，再将新稿放入已确认唯一权威位置。若`.scratch`按当前AGENTS属于不入Git目录，遵守仓库规范，不为“提交方案”强行git add -f；正式文档或受管记录按现有约定保存。历史原件放reference，不并行编辑两份规范。当前生成**没有修改用户仓库**。

如实际任务已改用ANI-doc中的另一个权威位置，先核对并在任务记录登记唯一位置，再更新所有引用；不同时维护`.scratch`与ANI-doc两份可编辑“主方案”。旧CPU-P01/ENV任务包保留其原路径与职责，本版不是新的总控goal，不应把全套历史goal拼接执行。

开工记录应包含实际方案绝对路径、文件SHA、各仓branch/commit、Fedora工作路径及集群交接来源。不得把`<实际工作树>`占位符当作已确认目录，也不能因缺一份环境交接就停止全部编码。

### 12.3 七个工作包：主链先做，入口后接

| 工作包 | 当前先交付 | 后续真实联调与不阻塞边界 |
|---|---|---|
| W0 负载与材料 | Alpaca JSON、A/B候选entry实际消费、指标/summary、export/verify、文件和预算 | Fedora材料核实后即可做；不等BFF/Model/页面 |
| W1 合同与受理 | 正式kind/codec、规则/默认/组合、意图与快照、共享解析和安全投影；为主链提供确定来件合同 | 实际Gov绑定/受理投递与BFF/前端放M3-B；W1这部分未完成不阻塞W0/W2/W3/W4 |
| W2 输入与准备 | 真实JSON验证/持久READY、固定字节与物化、固定基模读取端口 | S3/卷相关条件满足即联调；Model读取未就绪按D12.6限定素材，不冒充MR通过 |
| W3 执行与关闭 | 正式ModelDev持久执行、唯一Trainer、KFP export/verify、参数消费、启动停止原子仲裁、deadline/重启/实际关闭 | 目标组件可用即M3-A；不等真实BFF/前端；与W4共用执行停止边界 |
| W4 产物与ModelDev登记接缝 | HF真实收集/tar/publication确认、原登记记录/接口，D09.3逐副作用派发仲裁及故障测试 | S3主链先通过；Model未就绪不阻塞发布确认/关闭，真实登记等待提供方；不能假成功 |
| W5 Model提供方 | MR01–08需求和正式接口评估/交付，固定引用/可靠登记/查证/保护 | 独立提供方线；不是ModelDev作者必须同步改两仓，不成为主链全局前置 |
| W6 分段验证与交接 | 当前CORE_FLOW用例、竞态/参数组合向量、主链测试驱动边界与证据；保留FT/PA/MR原表 | M3-B再做真实BFF/前端及候选绑定；最终补FT29与交接；GPU独立不阻塞 |

已完成公共能力先复用。测试驱动由W3/W6在ModelDev现有测试装配中落地，不新增W7、不新增服务。人员与工期另行确定。环境缺项只阻塞相关LIVE动作，继续合同、编码和Fedora测试；不能把“合同先定”扩大成必须等所有上游/页面一起完成。

### 12.4 包内小任务与会合点

| 子任务 | owner | 当前阶段／完成物 |
|---|---|---|
| W0-P1 | W0＋W1 | M0：基准/非默认候选、真实框架映射、最终配置覆盖与指标；不等完整BFF |
| W1-P1 | W1 | M1-A：两字段程序合同、Release规则自校验、kind分流、旧CPU黄金回归 |
| W1-P2 | W1 | M1-A：共享默认合并/组合与存在性、原快照/摘要；真实Gov事务/投递接线在M3-B补证 |
| W1-P3 | W1 | M1-A：修正真实类型投影、预设/详情DTO与安全回放样例；不声称已对接页面 |
| W1-P4 | W1＋现有前端 | M3-B：真实BFF当前绑定、权限、受理与投递、查询/停止/恢复、页面提交与历史展示 |
| W3-P1 | W3＋W0 | M1-A/M3-A：entry消费原快照、summary回读、A/B真实训练与新增步骤 |
| W3-P2 | W3＋W6 | M3-A：D12.6隔离应用层测试驱动、真实持久化/KFP/Trainer/发布确认/停止；不新增生产API |
| W4-P1 | W4 | M1-A/M3-A：动态权重SHA与固定文件结构、tar/publication，Model未接入后的明确失败和计算关闭 |
| W4-P2 | W4＋关闭负责人 | 真实登记前：D09.3逐写派发与停止/时限仲裁、原收据/恢复、FT25.1–3 |
| W6-P1 | W6 | 当前：CORE_FLOW适用断言、PA共享解析/实际消费和R3向量；后续：BFF/页面剩余断言及整体汇总 |
| W6-P2 | W6＋BFF/ENV负责人 | M3-B前：D12.7候选隔离绑定/身份/回退记录；不是M0/M3-A前置 |

这些是原包内有限拆分，不增加产品范围或新的平台。W0/W1先形成候选合同可并行；完整候选等真实PipelineVersion/镜像生成后拼齐，不填假ID。真实环境先接通ModelDev→KFP/Trainer的主链，再用相同合同接回BFF；不因后置而改旧快照或引入第二默认指针。前端可并行开发，不得要求主链等它验收。

本机仅编辑/Git，Fedora构建/验证，目标K8s运行。先读AGENTS和工作区状态，使用本人分支或独立worktree；不stash/reset/clean/强推/自动合并，不改ENV agent的installer工作树，不无授权升级共享控制器。

### 12.5 W1/W3的接缝记录与独立GPU准备线

| 责任 | 本轮增加的有界说明/核对 | 不属于该包新增开发或验收 |
|---|---|---|
| W1 合同与受理 | 在既有合同工作中记录算法参数/执行资源分界、原快照兼容、Gov/ACC既有附件与查询接点；参见D01.2/D02.7 | 不提前发布GPU字段/枚举/表，不开放资源表单，不重做资源或身份系统 |
| W3 执行与关闭 | 记录实际训练容器渲染点、CPU限制所属合同、计算关闭持久事实；可协助D10.2独立试验 | 不改CPU配方用GPU，不在CPU路径增加GPU退款RPC，不因预留强制重构全部Trainer/CloseWorker |
| W6 故障与交接 | 保持C33和FT/PA回归，交接GPU差异/试验记录的引用及未测项 | 不给现有用例追加GPU必须通过条件，不等待GPU环境才完成本版 |
| GPU准备负责人＋ENV | 按D10.2协调Gov/ACC差异、ModelDev测试映射和Installer隔离环境；主线具备对应能力时即可并行 | 不是新增W7，不默认要求当前微调作者实现四仓生产接入；无硬件则停止设备层，其他工作继续 |

这是一条**独立准备线**，不是M0–M4的新前置。主开发线继续按12.3/12.4完成CPU微调和当前参数链；接缝核对发现现有结构已经足够，可以只交记录，不以代码行数衡量完成。生产GPU正式能力待下一批确定范围和验收后再合入启用；局部试验不得修改其他agent的工作树或共享资源。

最小移交为D10.2.3差异表及已有条件下的分层验证记录。文档由单一`spec.md`派生的`gpu-extension-boundary.md`供跨仓阅读，不另外维护一份可独立修改的GPU规范。测试结果只作为证据另存，不反向把文档计划标成运行PASS。

### 12.6 当前主链的隔离测试装配与启动指引

本节不是已存在命令清单，也不是生产旁路。**W3/W6先核实现有集成测试入口；不足时只补一次性测试驱动，在同仓测试装配中调用正式ModelDev应用用例。**不得新建常驻服务或公网“测试创建”API，不增加生产allow-direct-access开关。具体测试路径/命令/镜像在代码实现后回填记录，不凭本文猜已有命令。

| 动作 | 当前可执行做法／约束 |
|---|---|
| 1. 定位材料 | 记录本包spec的实际绝对路径/SHA、任务仓分支commit、Fedora本任务路径、目标集群交接；只缺BFF资料不停止全部任务 |
| 2. 固定来件合同 | 复用正式codec、程序/Release解析与快照验证；测试材料显式选择候选ID/摘要/参数和批准输入，不另写宽松JSON或浮动默认值 |
| 3. 装配上游替代边界 | 隔离测试装配提供限定测试上下文/来件，进入真实应用层受理和数据库事务；记录入口在哪层。测试operation/identity不是Gov真实授权/持久受理证据，不能混入正式库或正式租户 |
| 4. 运行真实下游 | KFP/Trainer/实际步骤Pod与训练程序、真实数据库/S3必须是本次被验证层级的实际依赖；既有workload认证、权威Run/Task/UID和写者证明不关闭 |
| 5. 查、停、恢复观察 | 测试驱动调用同一查询/停止/对账用例，经过真实持久停止与关闭；不直接删Pod代替业务Stop，不手工改库造状态。BFF动作授权仍未验证 |
| 6. 发布及Model边界 | 保留register前半段真实publication核验。Model未接入时仅其明确边界可返回不可用，记录真实错误/关闭，不默认READY；真实Model可用则按已登记调用接入 |
| 7. 独立取回与交接 | 以批准的只读测试身份从原持久对象取回校验，不挂训练卷；它证明存储独立性，不证明经BFF下载授权或经Model/Inference使用。后续对应断言再测 |

代码生成、测试驱动编译、镜像/Pipeline编译在既有Fedora完成，固定已推送commit；需要真实KFP/Trainer的驱动/被测程序部署到获准隔离K8s范围。Fedora临时数据库或边界替身模块测试可以先做，但不计目标集群主链完整通过。测试资源名称/数据库/namespace隔离、凭据引用与清理范围依现有授权，不让测试驱动访问正式资源。

上游RPC若只接受Gov证书，**保持这个正式拒绝规则**；从应用层测试装配替换上游，不借用Gov证书、不给普通调用方白名单、不把body tenant/actor直接作为正式Principal。测试装配没有经过的认证/委托/可靠投递断言全部NOT_RUN。若现有代码无法隔离装配，先补同仓测试接缝，不能用改库或关认证省事。

Model读取未接入时，素材必须有获准来源、固定字节/摘要与测试归属。可以用明确的只读Model边界fixture/获准素材适配驱动实际准备，不把虚构ModelVersion写入正式Model，也不声称MR02/FT10整项通过。数据READY则由真实本域导入用例产生，不直接插成功行。

**候选配置不是第二个生效指针。**主链测试输入显式固定一个候选，并通过相同校验；ModelDev不维护新的“当前Release”。Gov正式指针不修改。若现有Pipeline完整材料尚缺，可先做负载/局部adapter测试，补齐真实ID后才做相应主链；不能拿占位符换取受理。

实施后回填：测试源码/命令、源commit和镜像、入口应用层方法、替代边界、实际/未测依赖、隔离数据库和Namespace UID、候选摘要、Run/TrainJob/Pod/对象事实、停止/取回证据。现有`work-progress`/`acceptance-results`可附加这些信息，包内模板仅帮助记录，不要求新增业务表或另一套运维产品。

### 12.7 后续真实BFF联调的候选绑定（不作为当前主链前置）

**只在准备M3-B时落实本节。**W0/W2/W3/W4和M3-A不等待候选经真实BFF调用的操作说明、页面或上游接线。最终用户入口仍是BFF，这里是阶段安排而非永久删除。

| M3-B前需要记录 | 采用规则 |
|---|---|
| 候选隔离范围 | 选择现有系统实际支持的隔离测试部署或受管绑定；不假定已有逐租户Release开关，不建设新的候选发布平台 |
| 真实入口和身份 | 已有BFF及真实认证/权限/租户映射，明确获准测试用户；不增加绕过授权的测试RPC |
| 固定版本与变更所有权 | 候选release/digest/generation、当前指针的原值、具体负责仓库/配置/操作人；指针仍由Gov负责 |
| 参数/查询/停止路径 | PA05及其他含BFF/页面的断言走真实来件和投递；对照原快照与entry实际配置，不仅检查UI显示 |
| 回退与清理 | 记录实际回退方式；恢复前核对当前绑定仍为本次值，避免覆盖其他agent新变更；不停止无关历史执行、不删已发布模型 |

候选在隔离测试环境通过真实BFF验证，不等于向普通用户正式启用；M3-B及最终验收前后记录清楚。已经通过的M3-A记录继续保留，新增上游引发的真实错误仍需修正并回归，不通过宣称“主链已过”跳过整体链。Model若仍未正式接入，只能记相应BFF部分，PA05全链/MR/FT29继续未测。

## D13 · 实施前必须锁定的事实与停止条件

**“消除歧义”不等于编造尚未实测的值。**下面的未知项有明确负责人、用途和未就绪行为，不能由agent自行选择另一条架构。

| 待锁定事实 | 负责人/阶段 | 缺少时 |
|---|---|---|
| 相关工作树/正式API/模板实际commit | 各仓负责人，开工 | 可读代码和准备合同；不假定默认分支与评审基线一致 |
| Fedora实际登录与本任务源码/制品路径 | 当前执行负责人，W0前 | 只阻塞相应远程动作；不本机运行或把集群当构建机 |
| LLaMA-Factory/Python/PyTorch/transformers/peft版本、镜像digest、离线材料 | W0/M0 | 不发布可启用Release，不猜标签 |
| CPU训练/保存/导出/verify实际dtype与CLI向量 | W0/M0 | 不声称关闭bf16即可保证输出精度 |
| 64条真实样例、有效样本、基准20步与非默认候选的输出结构/真实SHA | W0/M0 | 不用基准权重SHA约束其他合法输出，不预填真实步数和推理结果 |
| ani.metric.v1实际schema/解析器版本、真实日志/callback来源、字段/采样/summary映射 | W0/W1，M0 | 不猜字段，不用请求上限或JSONL行数伪造步数；指标适配验收未通过不发布该配方 |
| export/verify与stop共用的数据库锁/原子条件、首次受理与重放语义、启动时间/步骤deadline | W1/W3，首次代码联调前 | 配置读取不代替启动仲裁；未实现时不启用计算角色，不以Mock证明并发安全 |
| Model方法/模块版本、精确SAN、字段映射/查证能力 | W5/M1-B | Model接入未启用；允许显式消费者替身测试 |
| 直接引用是否可用、否则唯一已选复制绑定 | W4/W5/M1-B | 不临时切模式，不泛化实现两个storage后端 |
| KFP/Trainer/Runtime/CPU池、StorageClass、受管身份与入口 | ENV交接+W3 | 相关LIVE阻塞，业务编码/Fedora检查可继续 |
| Inference取新包和实际CPU/其他既有推理Runtime支持 | 现有Inference负责人，M3综合/FT29前（非M3-A前置） | FT29/C23未通过不能称本方案最终验收完成；不要求每次任务自动部署，不另造推理后端 |
| 当前参数codec/合同ID、Release规则与执行映射的真实字段 | W1，合同定稿前 | 保留typed请求；不套CPU三参数校验，不接受未实现字段 |
| 非默认测试组合与批准依据、参数/固定配置安全投影 | W0/W1/W6，参数验收前 | 仅基准PASS不得启用扩大集合；不把候选值当已验证事实 |
| 正式来件codec/测试应用入口、隔离库/命名空间和上游替代范围 | W1/W3/W6，M3-A前 | 先核实既有测试装配；缺项只停相应测试，不改生产鉴权、不改库造状态 |
| 具体登记派发与Stop/deadline的同一锁/原子条件、在途记录和重发边界 | W4/关闭负责人，首次真实写入前 | 先实现和测试R1；Model依赖不阻塞其他主链，不能声明尚未接入的登记正确性通过 |
| Gov当前指针查询、候选BFF隔离绑定/回退、实际前端仓库落点 | W1/W6/前端，M3-B前 | BFF/页面断言NOT_RUN；不是W0/W2/W3/W4或M3-A的前置 |
| 公开目标API既有语义是否与本稿冲突 | 合同owner，合并前 | 明确登记差异，不暗改历史客户或改主键 |

Model/Gov共同合同足够规定调用规则；本表只补实际接入材料与业务方法，不升级为新信任体系。共同关闭机制发现确切缺陷时，以已复现反例修正和回归，未证实的担心不无边界扩任务。

所有占位值必须在启用时已被真实受管值替换并验证；缺少的必需值明确失败。代码、合同/替身验证、Model真实联调、目标集群通过分别记账，不“默认通过”。

### 13.1 仅影响GPU准备线的待核实项

Gov/ACC实际合同版本、ModelDev owner/API差异、Trainer到Pod的具体字段、GPU硬件/驱动/插件条件和隔离测试授权，只是D10.2相应GPU准备动作的前置，**不追加到本版CPU启用与最终验收的必需事实表**。未知时记录负责方、已尝试动作和恢复条件；不能猜字段/硬件、借用他人凭据，或把全部CPU工作标为BLOCKED。

## D14 · 版本变更、参数评审采纳与冲突消除清单

### 14.1 v3到v4的既定收敛（v4.2继续保留）

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


### 14.2 v4到v4.1：继承的三项评审修订

| 评审项 | v4缺口 | v4.1修订与联动 | 性质/范围 |
|---|---|---|---|
| R1/P1：启动与停止共同仲裁 | D06仅写检查stop/deadline和保存启动事实，未规定原子关系 | D06.2固定同一执行级数据库串行化边界、首次受理、原Run/Task/Pod绑定、丢响应只查证；A11/D09/D10及W3/FT23/24同步 | 现有步骤/关闭中的必要代码增量，不新建许可平台 |
| R2/P2：指标职责 | D06/D07只要求真实metrics/summary，漏写谁生成平台schema | finetune_entry从M0锁定真实日志/callback生成ani.metric.v1 metrics.jsonl与实际summary；collect/publish只校验和发布；W0/M0/FT11同步 | 恢复v3已存在的训练适配职责，不重建指标系统 |
| R3/P2：完成标准层级 | A10与C23均使用“完整交付”，容易误读成每任务必部署推理 | A10单次执行成功条件；D11.4本方案最终验收条件，FT29/C23独立Inference验收；A03/A09/D13和Model派生稿一致 | 术语与判定层级澄清，不新增每任务自动部署 |

自复核同时修正两处编辑不一致：D03将孤立的 `data.json` 改成实际合同路径 `inputs/data/finetune_data.json`；A09“Model未接收方就绪”改成“Model接收方未就绪”。这些不改变输入字节、对象模型或开发范围。

P1的预算联动是明确修订：export/verify从首次启动受理事务时间固定有效deadline，实际spawn时间只作观察，不另起一份预算；其他步骤和旧CPU默认值不变。原请求、模型目标、服务规范、单TrainJob、Alpaca JSON、Model分期接入、Fedora职责和C01–C34覆盖边界均不改变。

当前文档自复核与机器检查见`review/self-review.md`、`review/document-checks.json`，只证明文档一致性与呈现；v4.1的原运行状态不转换成本轮结果。所有FT/PA/MR及负载/LIVE记录仍为NOT_RUN。

### 14.3 v4.1到v4.2：参数评审的完整落地

以下新增条款是按用户本轮请求采纳上一轮审核后的设计决定，不冒称原v4.1已有实现，也不是新实测结论。[S9]

| 审核项 | 本版明确修订 | 联动位置/任务 | 不扩大什么 |
|---|---|---|---|
| P-01 规则归属 | 公共格式、程序语义、Release批准、原执行快照四层；只一个维护点 | A07、D02、D05；W1 | 不新增参数服务/DSL/任意map |
| P-02 兼容对象 | 旧CPU v1保留；当前kind分流；未来新codec必须明确识别，不用当前Release猜旧编码 | A07.5、D02.2、D05.4、FT30；W1/W6 | 本轮不开放可变epochs/batch的通用CPU产品 |
| P-03 展示 | 创建读当前Release、详情读原快照、指标读事实；修正3/64型投影 | D01.1、D02.4/5、D09.5；W1 | 不重做前端工作台、不公开敏感全快照 |
| P-04 真实消费 | 冻结参数决定最终配置，模板不能覆盖；summary保留安全解析结果 | D05.3、D06.4；W0/W3 | 不开放用户任意YAML、不新增参数权威表 |
| P-05 组合/预算/文件 | 批准完整组合或受管规则；动态权重SHA不是固定结构约束 | A07.4、D07、D10.1；W0/W4 | 不自动批准全部组合、不建资源估算器 |
| W6验收增量 | PA01–10，至少一组非默认贯通、真实查询/前端、历史不漂移 | D11.2.2、D11.4、D12.4；W6 | 原32+12项与MR不删除、Model替身不算真实通过 |

### 14.4 本版为消除执行歧义作出的具体选择

| 选择 | 明确结果 |
|---|---|
| 当前开放字段 | 仍learning_rate/max_steps，其他候选先不接受；交付能力包括可变值完整链，不再只做空扩展口 |
| 非默认起测 | 基准0.0001/20，B优先0.0002/10；仅为待验证设计向量，失败保留，启用前需实际批准 |
| 当前请求存在性 | finetuning对象必需，{}使用两项默认，null/缺失拒绝；两个子字段可省略。新FINETUNING分支的明确选择，不重解释CPU |
| 预览到受理切版 | 保留“新受理使用当前绑定”，返回实际配置；不增加用户选择旧Release/预览令牌 |
| 运行配置证据 | 扩原summary安全字段，不新增常驻服务或独立可编辑配置表 |
| 参数工作交付边界 | 先落承载/展示/非默认两字段，下一批新字段另行批准；不无限延后，也不一次复刻参考页全部参数 |
| 文档采用 | 完整v4.2取代v4.1正文；所有派生视图重建，原件与已填记录保留，禁止只改一个任务切片 |

新增参数/前端字段的最终Proto编号和已存在等价方法，由W1基于真实提供方API固定；这不是允许任意变更表中语义。程序/环境尚未核实的值按D13停止相关启用，不用猜测消除歧义。

### 14.5 v4.2到v4.2.1：仅补GPU后续接入边界

本次依据完整v4.2和用户随后确认的GPU分期讨论修订。[S10] 原参数链与CPU微调产品范围不变，不代表GPU已经开工或通过。

| 讨论点 | 并入正文的明确规则 | 联动位置 | 不扩大什么 |
|---|---|---|---|
| 现在准备，下一批正式接入 | 先核对既有Gov/ACC合同、记录接缝；GPU正式资源/占额/执行/退额另批 | A02.1、D10.2 | 不使GPU成为本版完成条件 |
| 涉及哪些项目 | 分开Gov/ACC核对、ModelDev映射、Installer/ENV隔离验证；Model/Inference不加任务 | D10.2.2 | 不要求四仓同时改生产代码 |
| ModelDev现在怎样留接缝 | W1/W3记录实际解析/快照/渲染/关闭位置；足够则无需代码改动 | D01.2、D02.7、D12.5 | 不强制改名CPUResources、加GPU Proto/表或假charge |
| 关键验证证明什么 | Fedora渲染、真实控制器Pod字段、实际设备三层分开；手写清单不替代正式adapter证明 | D10.2.4 | 不让Mock/CPU结果充当GPU能力；不授权旁路业务入口 |
| 原验收怎样保持 | FT/PA/MR/C表不变，GPU记录独立，NOT_RUN不阻塞CPU，当前CPU缺陷仍修复 | D11.5、D13.1 | 不新增GPU硬门槛，不放松CPU禁GPU |
| 资料与环境 | 单一spec派生两册、任务卡和GPU视图；保留Fedora与原路径分工，v4.2原文留存 | D12、S10 | 不改仓库/运行配置、不搬构建到K8s |

本次只补文档和对应派生视图，没有开启GPU生产接口、修改Gov/ACC allowlist、迁移数据库、建立GPU账本或执行硬件测试。实际GPU批次需核对当时正式合同与环境，不从本补充的用途描述猜测API字段或当前实现状态。

### 14.6 v4.2.1到v4.2.2：两轮审核与主流程优先

以下是本次明确采纳的修订，不称原v4.2.1已经完整写明，也不是新代码审计结论。[S11][S12]

| 讨论项 | 本版写入的唯一规则 | 同步位置/任务 | 不改变的边界 |
|---|---|---|---|
| 421-R1：登记与停止竞争 | 每个具体正常写派发与Stop/deadline共同仲裁；整份意图不预授权后继；在途迟到结果真实保留 | D08/D09.3、A11、W4-P2、FT25.1–3 | 不新增授权/许可平台或跨服务事务；原CLOSED恢复仍可按授权执行 |
| 421-R2：候选经BFF验收 | 改为M3-B启动前的操作交接；当前执行主链不等待BFF/前端/候选上游绑定 | A03/A09/A12、D11.6、D12.6/12.7、W1/W6 | 最终BFF仍唯一用户入口，不新增生产测试API或第二指针 |
| 421-R3：组合与默认 | 明确A/B完整组合及省略默认的5个黄金向量，不自动修改另一个字段 | D02.6.1、PA03/06/07、W1/W6 | B仍待实测；不新增PA11、DSL或更多可输入字段 |
| 用户：先完成微调主流程 | 当前M3-A真实ModelDev/KFP/Trainer/S3/发布确认/停止关闭；BFF和前端在M3-B接回 | A12、D11.6、D12.3/4/6、主流程派生手册 | 合同现在固定；后置项NOT_RUN而非删掉/DEFERRED |
| Model后接仍不造假 | 发布确认在Model写前必须真实完成；Model未接入明确错误+计算关闭，不能伪造READY | A03/A09/A10、D04/D07/D12.6 | 主链阶段结果不等于单次六项成功，MR/FT29仍需真实接入 |
| 完成证据分层 | WORKLOAD/CORE_FLOW/BFF_INTEGRATION等仅为记录scope；本域/上游断言分记录 | D11.6及records模板，W6 | 不加业务状态机；原32 FT+12细化+10 PA+8 MR+34映射保留，新增3个登记细化 |

历史D14.1–14.5保留其版本时点含义；遇到开发次序冲突，以本节及现行A12/D11.6/D12为准。不是让agent在多套矛盾主方案之间自行选择。GPU独立准备、单TrainJob、Alpaca JSON、正式Model目标、正常close、指标、预算数值、Fedora职责均保留。

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

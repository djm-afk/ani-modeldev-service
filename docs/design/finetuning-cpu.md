# 模型微调（受管微调 / LoRA SFT）能力设计

> **⚠ 历史设计稿：被 v4.2.2 承接并取代（2026-10-15 更新）**：本文档是早期设计稿，
> 是同一演化链（v3 → v4 → v4.1 → v4.2 → v4.2.1 → v4.2.2）的直系前身。实施权威现为
> `docs/design/ani-finetuning-v4.2.2/`（权威正文 `spec.md`，v4.2.2）。后者**承接**了
> 本文的能力定位与复用/扩展边界（capability 驱动、基模走 Model RPC、未压缩 tar、
> verify 短推理等），但**取代**其具体决策——本文与 v4.2.2 冲突的条目不再有效，主要为：
> - 编排：三次 Trainer 阶段提交 → v4.2.2 D06 为**唯一 TrainJob**，export/verify 复用锁定
>   程序但**不是额外训练作业**（经既有受管 step 入口同步运行）；
> - `configs/` 子路径 → v4.2.2 D07 工作区为 inputs/data、inputs/model、training/adapter、
>   export/merged、reports/{train,export,verify}、publication，不新增 configs；
> - `export-wait`/`verify-wait` 受管等待步 → 对应 v4.2.2 A08/D06.2 的 export/verify 计算
>   步骤；
> - 两段式 register 简化描述 → v4.2.2 D08/D09.3 以稳定键、内容 fingerprint、**每个具体
>   外部写操作与 Stop/deadline 共同仲裁**为准；
> - 资源/时限 3600s、2CPU/6Gi 起测 → v4.2.2 D10 为 10800s 及对应预算；
> - `reports/` 扁平布局 → v4.2.2 D03/D06 为原样物化 + `reports/train` 等分工。
>
> 实现范围、阶段/验收条件与当前开工顺序以 v4.2.2 的 A12/D11.6/D12.6 为准。保留本文
> 仅作设计演进记录与复核底稿（技术复核见 `finetuning-cpu-review.md`）。

本设计定义在既有 CPU-P01 通用训练流程之上新增 **FINETUNING**（受管微调）能力的方式。
它不新增编排系统，不改变 CPU-P01 已验收的字节语义；微调与通用训练共用同一套
受理、快照、KFP 提交、受管步骤、Trainer、产物收集与关闭流程，仅通过 capability
（`ExecutionKind`）选择差异化语法。

**权威基线**：本设计对齐 v0.4《精简业务与架构方案》与《精简开发实施说明》
（`kubeflow文档/模型研发平台-v0.4/source/{architecture,development}.md`）与
`cpu-baseline/`（`finetune_cpu.yaml`、`export_cpu.yaml`、`dataset_info.json`、`finetune_data.json`）。
v0.4 为当前精简交付基线，冲突时以 v0.4 为准。关键对齐点：

- 基模是**平台自有 Model 仓库中的一个已批准固定版本**（首例 Qwen2.5-0.5B-Instruct），
  经 `base_model_version_id` 受理、由 Model RPC 取回并挂载（A09/D04/D10/D11）。
- 微调走**锁定的 LLaMA-Factory 配方**（`llamafactory-cli train / export` 与镜像内
  verify 脚本），训练、导出与校验是**不同阶段**（A06/README/D10）。
- 闭环包含 **train → export → verify → collect → publish → register → close**，
  产出 Model 仓库版本 READY（A09/D10/D11）。
- 交付物为**未压缩 tar**（根含 config.json/tokenizer/safetensors，不套外层目录），
  Adapter 独立归档（A10/D11）。

**与模型仓库的闭环（读基模 ↔ 写成品）**：微调是一次**双向**的仓库交互——基模从
`ani-model-service` 的模型仓库**读入**，合并了 adapter 的完整 HF 模型**写回**同一仓库：

```
        ani-model-service（模型仓库）
          ▲  ③ 登记：CreateModelVersion（合并后完整 HF tar → 新 ModelVersion）
          │
   ┌──────┴──────────────────────────────────────────────────────────┐
   │              ani-modeldev-service（受管微调执行链）                │
   │  prepare → train → export → verify → collect → publish → register → close
   │            (training/adapter)  (export/merged)                    │
   └──────┬──────────────────────────────────────────────────────────┘
          │  ① 选基模：base_model_version_id → GetModelVersion / DownloadURL
          ▼
        ani-model-service（模型仓库）
```

- **入**（§2.1）：基模是仓库中已批准固定版本，受理 `base_model_version_id`，
  由 Model RPC 取回挂载；**不从环境路径、不让用户自报路径**。
- **出**（§7.5）：register 步骤把**合并后完整 HF 模型**（未压缩 tar）登记为新
  ModelVersion；adapter 独立归档，进仓库的是合并主模型。
- 两端都经 `ani-model-service`，modeldev **不造第二仓库**（D01）。

权威顺序（实现层）：本仓 `contract/cpup01` 是意图/快照/Release 的唯一维护点；
`internal/biz` 拥有领域与端口；`internal/data` 拥有出站适配；`internal/service`
为传输适配；KFP 只推进流程，不按数据库阶段重演。

> 状态：本文档是**待实施 spec**。微调能力的代码改动已按评审决定撤回，仓库当前只保留
> 本文档；§4 的文件清单为**实施时应新增/修改**的范围，不是现状。§7 为**必做实施批次**
> （对齐 v0.4），非可选项。实现与文档不一致时以本节"权威基线"为准。
> 本版（2026-10-13）按《技术复核报告》收敛三项决策（dataset_info 落盘位置、verify
> 执行载体、阶段提交建模，见 §2.5/§4.4/§7.3），并新增 §4.6 资源与超时、§4.7 异常与
> 失败路径。

## 1. 现状梳理：通用训练流程的核心模块与可复用能力

现有通用训练流程（CPU-P01）是一条 **受理 → 冻结快照 → KFP Run → 受管步骤 →
Trainer → 收集 → 发布 → 关闭** 的纵向切片。逐层盘点如下。

### 1.1 核心模块与代码结构

| 层 | 位置 | 职责 |
|---|---|---|
| 契约 | `contract/cpup01/` | 意图、快照、Release、产物清单的纯函数校验与规范化 |
| 领域/端口 | `internal/biz/` | Admission、ExecutionRuntime、PipelineDispatch、ManagedRuntime、InputImporter、Publication、Close |
| 数据适配 | `internal/data/` | execution/lifecycle/submission/input/catalogue/objectstore/workspace/kfp/trainer/runtimeproof/storagecredentials |
| 传输适配 | `internal/service/` | 查询、受理、命令、受管步骤、材料、运维 |
| 单步二进制 | `cmd/ani-modeldev-step/` | prepare/train-wait/collect/publish/close 组件入口与 workspace-name 子命令 |
| 编排定义 | `pipelines/cpu_p01.py` | KFP 六步装配（能力无关） |
| 训练负载 | `training/train_mlp.py` | 实际训练程序（CPU 专用） |
| Proto | `api/ani/training/v1`、`api/ani/modeldev/v1` | 自有 wire 契约 |

### 1.2 可直接复用的公共能力

| 能力 | 复用位置 | 复用方式 |
|---|---|---|
| 受理与幂等 | `biz/execution.go`、`data/execution` | 原样复用；幂等键与租户校验与能力无关 |
| 冻结快照与 spec hash | `biz/execution_runtime.go` `FreezeTrainingPlan`、`cpup01.Snapshot` | 复用结构；校验按 capability 分支 |
| 参数解析与 argv 生成 | `biz/admission_resolution.go` | **复用骨架，需扩展**：参数源现为固定 5 键 map（仅 LEARNING_RATE 映射），须按 kind 泛化键集与 `Format` 判定；intent 的参数值/数量硬编码校验（epochs="3"、batch_size="64"、≤3 个）按 kind 分支（见 §4.2） |
| KFP 提交 | `biz/pipeline_dispatch.go`、`pipeline_submitter.go`、`data/kfp` | 原样复用；`PipelineVersionID` 由 Release 指定 |
| 后台投递/关闭 worker | `biz/dispatch_worker.go`、`close_worker.go` | 原样复用 |
| 受管步骤编排 | `biz/managed_runtime.go`、`biz/managed_steps.go` | **复用骨架，需扩展**：`Configuration` 的任务白名单当前硬编码为 `prepare/train-wait/collect/publish/close`，微调新增 `export-wait/verify-wait/register` 必须改它（见 §5/§7.3） |
| 组件运行器 | `internal/component/runner.go` | 复用运行器；新增 `register` 组件语义（`export-wait`/`verify-wait` 为 Trainer 阶段等待步，组件镜像不做推理，见 §7.3） |
| Trainer 适配 | `data/trainer/`（Kubeflow Trainer TrainJob） | 复用适配；train/export/verify 三阶段需按阶段序号多次提交（见 §7.3） |
| 工作区与收集 | `data/workspace/collector.go`、`pipeline_io.go` | 复用框架；**必须扩展**：`collectFile`/`capturePipelineFile` 支持多级相对路径（逐级安全打开，保持防穿越）、`PrepareInput` 按 kind 分支（CSV/JSON）；微调按 `in_tar` 区分入包子集、tar 成员名取基名、tar 根排除 manifest（见 §5/§7.4） |
| 产物发布与核验 | `data/workspace/pipeline_io.go`（UploadOutput）、`data/objectstore` | 复用核验框架；输入结构校验按 capability 分支 |
| 关闭与恢复 | `biz/execution_closer.go`、`data/lifecycle` | 原样复用 |
| 临时存储凭据 | `biz/storage_credentials.go`、`data/storagecredentials` | 原样复用 |
| KFP 六步 Pipeline | `pipelines/cpu_p01.py` | CPU pipeline **零改动**；微调复用同一骨架新增独立 pipeline（见 §5/§7.3） |

**结论：编排与执行侧复用骨架，非 100% 原样复用。** 受理、快照、KFP 提交、worker、
关闭、凭据为原样复用；**受管步骤（`managed_runtime.go` 任务白名单）、Trainer 三阶段
提交、工作区/收集（含 `collectFile`/`capturePipelineFile` 多级路径与 `PrepareInput`
按 kind 分支）必须扩展**。能力差异落在：契约语法、输入结构校验器、训练配方/镜像、
受管步骤扩展，以及两个 CPU 没有的新环节——**基模解析（Model RPC）**与**登记
（Model RPC，由 modeldev 侧执行）**。后者复用现有 Model 公开接口，不新增服务。

## 2. 差异化扩展点（扩展而非重复开发）

| 扩展点 | CPU-P01 | FINETUNING | 承载位置 |
|---|---|---|---|
| 执行种类 | `GENERAL_TRAINING` | `FINETUNING` | `contract/cpup01/capability.go` |
| 参数契约版本 | `ani.cpu.mlp.parameters.v1` | `ani.lora.sft.parameters.v1` | 同上 |
| 用户参数集（开放） | epochs/batch_size/learning_rate | learning_rate/max_steps | `finetuning.go` |
| 预设固定参数 | — | lora_rank/lora_alpha/cutoff_len/finetuning_type/lora_target（由预设与镜像固定，非用户参数） | `finetuning.go` |
| 输入格式 | CSV `ani.cpu.csv.v1` 1024×16 | Alpaca JSON `ani.finetune.alpaca.v1` 64 条 + `dataset_info.json` | `finetuning.go` |
| 输出契约 | `ani.cpu.output.v1` CHECKPOINT | `ani.finetune.output.v1` LORA_ADAPTER | `finetuning.go` |
| 输出角色 | model.pt/model_config.json/metrics/summary | adapter 权重与配置、merged HF tar、verify 报告、metrics、summary | `finetuning.go` |
| 程序命令 | `/opt/cpu03/train_mlp.py` | `llamafactory-cli train` / `llamafactory-cli export` / `verify_cpu.py` | `finetuning.go` |
| argv 模板 | 5 项 + 可选 recipe | 训练/导出/校验三段 argv（配置路径 + 受管挂载源） | `finetuning.go` |
| 输入结构校验 | `objectstore.VerifyCSV` | `objectstore.VerifyAlpacaJSON` | `dataset_verifier.go` |
| 训练负载 | `training/train_mlp.py` | 锁定 LLaMA-Factory 镜像 + `finetune_cpu.yaml`/`export_cpu.yaml`/`verify_cpu.py` | 新增 |

### 2.1 预训练权重（基模）加载逻辑

基模**不是**环境事实路径，而是**平台自有 Model 仓库中的一个已批准固定 ModelVersion**：

1. 受理时用户在意图中提交 `base_model_version_id`（首例固定为 Qwen2.5-0.5B-Instruct，
   后端核验，不能任意填其他模型）。
2. 受理/准备阶段由 Model RPC `GetModelVersion`（tenant + model_version_id）解析基模。
   该 RPC 内部强制 `Version.DeploymentInput()`：**要求 `status=="ready"` 且
   `ArtifactProvider/ArtifactReference/ArtifactSHA256` 齐全**，否则报 `ErrModelNotReady`；
   响应同时带 `checksum_sha256`/`size_bytes`/`storage_path` 与 `is_encrypted`/`encrypt_algo`/
   `encrypt_hint`。必要时实际读取前 `GetModelDownloadURL` 签发**短时效（5 分钟）**下载
   链接，链接**不进长期快照**（对齐 D11）；**每次实际读取前签发**，下载中断重试须
   **重新签发**、不得复用旧链接。
3. 基模目录物化到本次工作区的输入子路径（`{workspace}/inputs/model`，见 §2.5），
   训练配置以该平台路径为 `model_name_or_path`（配方的 `/ani/input/model` 是样例路径，
   需按 §2.5 映射，不可照抄）。
4. 冻结快照记录 `base_model_version_id` 与解析出的物化路径摘要；链接/凭据不进长期快照。

微调负载/配方只接受一个已存在的目录路径，不做任何下载。

> **基模 allowlist**：首例允许清单（Qwen2.5-0.5B-Instruct；注意勿与仓库记录中的
> Qwen2-0.5B 混用——`cpu-baseline/README` L27）作为受管常量固化在
> `contract/cpup01/finetuning.go`，受理与 prepare 双重核验，实现位置见 §4.2。
>
> **双输入通道互不复用**：基模走 Model RPC 通道（本节）；数据集走 `VerifyAlpacaJSON`
> 通道。基模**不经过** `InputImport`/`VerifyCSV`——其 1024×16 CSV 语义与 ≤32 MiB
> 直传上限（A03）仅适用数据集侧。

> **加密基模边界**：`GetModelVersion`/`GetModelDownloadURL` 都可能返回
> `is_encrypted=true` 与算法提示。**首版仅支持未加密基模**（首例 Qwen 未加密）；
> 加密模型需要 modeldev 侧先解密，留作后续 capability 版本，本设计不做。

> 与 v0.4 一致：A09「微调基模通过平台已有 Model 接口准备」、D04 `base_model_version_id`、
> D10 prepare「获取固定基模与 JSON」、D11「解析基模 → GetModelVersion」。

> **prepare 资源与物化方式**：基模约 1 GiB，现有 CPU 的 `prepare` 组件限额
> （100m→500m CPU、128Mi→512Mi）与 `io.ReadAll` 全量读入不适用，必须改为**流式拷贝**
> 并上调为微调预设的资源（A06 建议请求 2CPU/6Gi、上限 4CPU/8Gi，工作区 20Gi）。
> 该规格记入 Release 的 `resources`/`workspace`（见 §4.3）。

### 2.5 工作区布局与挂载路径映射

平台不使用 `cpu-baseline` 样例的 `/ani/input`、`/ani/output` 绝对路径，而是以
v0.4 D09 的工作区布局为准，由 `WorkspaceContract` 子路径契约决定；配方的样例路径
必须映射到该契约，不能照抄到 Release。

| v0.4 D09 目录 | 现有契约字段（`snapshot.go`） | 微调用途 | 配方样例路径 → 平台路径 |
|---|---|---|---|
| `inputs/` | `InputSubpath` | 数据集与基模 | `/ani/input/data` → `{ws}/inputs/data`；`/ani/input/model` → `{ws}/inputs/model` |
| `configs/` | （新增 `ConfigsSubpath`） | 对齐 D09 布局保留；**首版不承载运行时必读文件**（`dataset_info.json` 落 `inputs/data/`，配方固化镜像内，见下） | — |
| `training/` | `TrainingSubpath` | 训练唯一可写输出（adapter） | `/ani/output/adapter` → `{ws}/training/adapter` |
| `export/` | （新增 `ExportSubpath`） | 合并模型目录 | `/ani/output/merged` → `{ws}/export/merged` |
| `reports/` | `ReportsSubpath` | 校验、日志、指标 | — |
| `publication/` | `PublicationSubpath` | 最终归档暂存 | — |

- 现有 CPU 只定义了 `InputSubpath/TrainingSubpath/ReportsSubpath/PublicationSubpath`；
  微调新增 `configs/`、`export/` 两个子路径，为 `WorkspaceContract` 的**增量字段**。
- **新增子路径必须一并纳入 `invalidWorkspaceContract` 的（非重叠/非根/无 traversal/无
  反斜杠）校验与 `Snapshot.Canonical()` 的确定性处理**，否则微调可用非法布局通过契约
  （现有校验只覆盖原 4 个子路径）。
- **配方位置（与 §4.3 统一）**：训练/导出/校验配方 `finetune_cpu.yaml`/`export_cpu.yaml`
  **固化在镜像内**（`/config/...`），且配方中的路径**直接写平台绝对路径**（KFP 挂载点
  固定为 `/workspace`：`/workspace/inputs/model`、`/workspace/inputs/data`、
  `/workspace/training/adapter`、`/workspace/export/merged`）。`configs/` 不承载配方
  或"重写等价配置"，**不存在两种来源并存**；prepare 无需做任何路径重写。
- **`dataset_info.json` 落盘（§7.2 同步）**：由 prepare 物化到 `{ws}/inputs/data/`
  （与数据文件**同目录**）——LLaMA-Factory 在 `dataset_dir` 目录下查找
  `dataset_info.json`，放 `configs/` 会导致训练启动即找不到数据集。
- KFP 侧仍以 `/workspace` 挂载 PVC（同 `cpu_p01.py`），子目录由契约拼接。
- 锁定配方中的 `model_name_or_path`、`dataset_dir`、`output_dir`、`export_dir` 已直接
  写上述平台绝对路径，禁止沿用 `/ani/...`。

### 2.2 冻结层与学习率差异化调度

首版 LoRA 只注册 `finetuning_type=lora`；`lora_target=all` 等具体目标层由**镜像内
固定配方**（`finetune_cpu.yaml`）决定，不接受用户覆盖。**用户只可调 `learning_rate`
与 `max_steps`**，其余 LoRA/长度/批量参数由预设固定（对齐 D04）。`learning_rate` 走
与 CPU 相同的 DECIMAL 语法（首版微调放宽上界到 (0,1]），差分调度留给后续 capability
版本，不改变首版字节。`max_steps` 为正整数，**首版上界 200**（起测值，待压测校准；
与 `execution_timeout_seconds` 的预算关系见 §4.6）。意图 `name` 长度 1–80 字符（D04）。

**首版固定约束（A06）**：禁用 fp16/bf16 与 GPU 专有配置——配方固化
`fp16: false`/`bf16: false`/`flash_attn: disabled`，用户不可覆盖。

受管 recipe 与 CPU-P01 对齐：微调 Release 允许在其 `args_template` 末尾追加固定对
`--recipe success|fail`（字面量，不接受参数源），语义与 CPU 分支一致，使登记的失败
路径（`fail`）可按 Release 复现。该 recipe 是 Release 固定的受管内部项，永不来自
用户参数。

### 2.3 LoRA/QLoRA 等高效微调策略

本设计将 `finetuning_type` 注册为**固定字面量**（首版 `lora`）。QLoRA 作为
**未来扩展点**：新增一个 capability 版本（如 `ani.qlora.sft.parameters.v1`）与
对应命令，不触碰 `lora` 分支——这正是 capability 驱动的价值。GPU 与量化位宽的
资源配置通过现有 `CPUResources`/未来 GPU 资源字段承载，不进入本设计首版。

### 2.4 验证与评估指标适配

训练负载产出 `metrics.jsonl`（`ani.metric.v1`，逐 step `train.loss`）与
`summary.json`，由既有 collector 读取、`OutputManifestBytes` 绑定。

区分 v0.4 的 **verify** 与 **evaluate**（A01）：

- **verify（首版必经）**：`export` 之后独立加载合并模型，固定短输入完成一次推理
  （不超过 16 token、固定贪心解码、不检查必须回答某句话——出处
  `cpu-baseline/README` L35；D10 仅要求"固定短输入完成一次推理"），产出加载可用
  报告与文件摘要。verify 是**与 export 共用锁定镜像的独立 TrainJob 阶段**（受管
  `verify-wait` 等待，见 §7.3），不在 `ani-modeldev-step` 组件镜像内执行推理。
- **evaluate（首版不做）**：效果评测不在首版必经流程；模型效果未评测需单独显示，
  不用一次回复或 Loss 趋势证明效果（A09），故不引入无验收的口径。

## 3. 接口一致性（上层仅切换配置）

- **受理入口不变（路由与调用方式）**：`POST /admin/v1/modeldev/executions` 路由、鉴权与
  幂等机制不变；通用训练继续用 `general_parameters[]`（结构不变）。**微调按 v0.4 D04
  使用独立的 `finetuning` 对象**，并新增 `base_model_version_id` 与
  `target_model_id`/`new_model_name`——这是**增量字段**，不是"结构完全不变"。
- **`finetuning` 与 `general_parameters` 的关系**：二者**互斥**。通用训练用
  `general_parameters[]`（现有 intent 硬编码 `epochs/batch_size/learning_rate`）；
  微调用 `finetuning{learning_rate, max_steps}`。微调 Release 的
  `parameter_contract_version=ani.lora.sft.parameters.v1`，其 `user_parameters` 只声明
  `learning_rate/max_steps`。
- **数值类型换算**：D04 示例 `max_steps: 20` 是**数字**，而现有 `Parameter.Value` 契约是
  **十进制字符串**（`learning_rate` 规范十进制、`INTEGER` 为十进制整数字符串）。受理层
  需把 `finetuning` 的 JSON 值规范化为契约字符串（`20 → "20"`、`0.0001 → "0.0001"`），
  再进入参数解析/argv 生成；不做无类型 JSON 透传（D04 L112）。
- **KFP 提交不变**：仍是 `PipelineID` + `PipelineVersionID` + 两个 Run 参数
  （`execution_id`、`spec_hash`），由 Release 决定指向哪条 PipelineVersion。
- **步骤协议（RPC 形状）不变，但步骤集合扩展**：受管步骤的 RPC 形状与鉴权方式沿用，
  但 `managed_runtime.go` 的任务白名单需由 `prepare/train-wait/collect/publish/close`
  扩展为 `prepare/train-wait/export-wait/verify-wait/collect/publish/register/close`
  ——`export-wait`/`verify-wait` 是 Trainer 阶段等待步，组件镜像不做推理（见 §5/§7.3）。
- **查询视图不变**：`ExecutionView.kind` / `PresetView.kind` 由 Release 的
  capability 映射，字段与结构不变。
- **更替方式**：同一个 preset 切换到不同 capability 的 Release，即从通用训练
  变为微调，调用方无需改代码。

## 4. 完整实现方案

> 状态说明（以此为准）：本文档是**待实施的 spec**。微调能力的代码改动**已按评审决定
> 撤回**，仓库当前只保留本文档；§4.1/§4.2 的文件清单是**实施时应新增/修改**的范围，
> 不是现状。§4.3 起的配置模板与入口示例是**目标形态**。对齐 v0.4 的实施批次见 §7，
> 为**必做**（非可选）。§7.4 提到的未压缩 tar 打包基础设施属 **CPU-P01 既有实现**，
> 其余微调相关均待实施。

### 4.1 待新增的文件

| 文件 | 作用 | 状态 |
|---|---|---|
| `contract/cpup01/capability.go` | capability 常量、参数契约版本、ability 谓词 | 待新增 |
| `contract/cpup01/finetuning.go` | 微调参数/输入/输出/程序命令与 argv 模板语法 | 待新增 |
| `contract/cpup01/finetuning_test.go` | 微调快照/意图/Release 的 Go 测试 | 待新增 |
| `contract/cpup01/protobuf/finetuning_codec_test.go` | 微调能力的 wire 往返测试 | 待新增 |
| `contract/cpup01/conformance/finetuning-release-v1.json` | 微调 Release 一致性向量 | 待新增 |
| `contract/cpup01/conformance/finetuning_vector_test.go` | 向量解析测试 | 待新增 |
| `internal/data/objectstore/dataset_verifier.go` | Alpaca JSON 数据集结构校验 | 待新增 |
| `internal/biz/model_resolver.go` | Model RPC 解析基模（GetModelVersion/DownloadURL） | 待新增（消费方对接） |
| `internal/data/model/client.go` | Model 适配：gRPC 客户端（解析基模、`CreateModel`/`CreateModelVersion`），含 mTLS/身份配置 | 待新增（D11） |
| `internal/data/model/publisher.go` | **存储归属适配**：把 modeldev 发布产物映射/受控复制到 model-service storage 的租户作用域，产出 `storage_path` | 待新增（见 §7.5） |
| `internal/biz/register_model.go` | 登记编排：由 modeldev 侧调用 Model（`CreateModel` 或 `CreateModelVersion`）（KFP 仅触发） | 待新增（D06/D11） |
| `training/verify_cpu.py` | 锁定镜像内 verify 短推理脚本（贪心 ≤16 token，产 `reports/verify.json`） | 待新增 |
| `pipelines/finetune_cpu.py` | 微调 KFP pipeline（三阶段 TrainJob 等待后接 collect/publish/register） | 待新增（§7.3） |
| `training/finetune_cpu.yaml` | 锁定训练配方（路径按 §2.5 映射） | 待新增 |
| `training/export_cpu.yaml` | 锁定导出配方（路径按 §2.5 映射） | 待新增 |
| `training/dataset_info.json` | LLaMA-Factory 数据集映射（受管常量） | 待新增 |
| `training/make_finetune_data.py` | Alpaca JSON 夹具生成（64 条） | 待新增 |
| `training/Dockerfile.finetune` | 锁定 LLaMA-Factory 离线镜像 | 待新增 |

### 4.2 需要修改的文件

| 文件 | 修改 | 兼容性 |
|---|---|---|
| `go.mod` / `go.sum` | **跨仓依赖**：import `github.com/liangzai006/ani-model-service/api/model/v1`，固定版本 | 新增依赖 |
| `api/ani/training/v1/training.proto` | 新增 `EXECUTION_KIND_FINETUNING`、`OUTPUT_KIND_LORA_ADAPTER`、FileRole（adapter/merged/config/verify report）；**`RequiredOutput` 增 `in_tar`** | 纯新增枚举值/字段 |
| `api/ani/training/v1/training.pb.go` | 由 buf 重新生成（含 `RequiredOutput.in_tar`） | 生成物 |
| `api/ani/modeldev/v1/*.proto` | 意图新增 `finetuning`、`base_model_version_id`、`target_model_id`/`new_model_name` | 纯新增字段（可选） |
| `contract/cpup01/intent.go` | kind 校验改 `SupportedExecutionKind`；`finetuning` 参数分派与类型规范化；基模版本字段；**参数值/数量硬编码校验（epochs="3"、batch_size="64"、≤3 个、`len(ResolvedParameters)!=3`）按 kind 分支**；`name` 长度 1–80 校验 | CPU 分支不变 |
| `contract/cpup01/snapshot.go` | 输入/参数数/输出契约按 kind 分支；记录基模版本（**以 omitempty/独立子结构承载，保证 CPU 规范化字节零变化**）；`WorkspaceContract` 增 `configs/export` 子路径（并纳入重叠校验）；`RequiredOutput` 增 `in_tar` | CPU 分支不变 |
| `contract/cpup01/release.go` / `release_parts.go` | 程序、argv、输出契约按 kind 分派；`invalidOutputContract` 按 kind 分支；**微调枚举合并 HF 全文件集**；`ReleaseDocument` 新增 `ExportProgram`/`VerifyProgram`/`Registration` 可选字段（现有 `ParseRelease` 为 `DisallowUnknownFields`，不新增即解析失败；CPU 分支不填、Canonical 字节不变） | CPU 分支不变 |
| `contract/cpup01/output_manifest.go` | 微调分支按 kind 校验（不再用 `max_file_count=0` 哨兵） | CPU 分支不变 |
| `contract/cpup01/protobuf/codec.go` | kind/output_kind 通用映射；**`RequiredOutput.in_tar` 双向映射** | CPU 映射不变 |
| `internal/data/objectstore/output_verifier.go` | **按 kind 分支**：微调 `wanted` 不加 manifest、tar 成员名↔`RequiredFiles` 相对路径做映射（**取基名 `path.Base`**，见 §7.4） | CPU 分支不变 |
| `internal/biz/managed_runtime.go` | **任务白名单**由 `prepare/train-wait/collect/publish/close` 扩为 `prepare/train-wait/export-wait/verify-wait/collect/publish/register/close`（合并白名单 + `Configuration()` 按 kind 返回期望步骤集）；`Publish` 鉴权保持 `close` | CPU 分支不变 |
| `internal/biz/managed_steps.go` | 微调链新增 export-wait/verify-wait/register 步骤 | CPU 步骤不变 |
| `internal/biz/admission_resolution.go` | 参数契约校验、参数源解析（**values 键集按 kind 扩展：`MODEL_PATH`/`ADAPTER_PATH`/`EXPORT_DIR`/`CONFIG_PATH` 等**、`Format` 判定按 kind）、基模解析与挂载源、输入行数源 | CPU 行为不变 |
| `internal/biz/execution_runtime.go` | 冻结快照新增基模版本解析结果 | CPU 快照不变 |
| `internal/data/workspace/collector.go` | `collectFile` 支持**多级相对路径**（逐级 `Openat` 目录 fd + 每级 `O_NOFOLLOW`/`O_DIRECTORY`，保持防穿越语义——`O_NOFOLLOW` 仅护末级，现状中间目录组件不设防）；微调按 `in_tar` 收集（仍逐项收集 `RequiredFiles`，集合不变） | CPU 分支不变 |
| `internal/data/workspace/pipeline_io.go` | `PrepareInput` **按 kind 分支**（CSV 原样 / 微调落 `finetune_data.json`+`dataset_info.json` 至 `inputs/data`）；`capturePipelineFile` 放开多级路径（L283 现硬拒 `/`，放开时保持防穿越）；`bundlePipelineFiles` 按 kind 选入包子集、tar 成员名取基名、tar 根排除 manifest（见 §7.4）；`LogicalKey` 前缀按 kind（微调 `lora-sft:`） | CPU 分支不变 |
| `internal/data/trainer/` | 三阶段提交（train/export/verify）与阶段序号（§7.3 已收敛为方案一；现状 `create.go` 为单次提交，**TrainingPlan/EnsureTraining 阶段扩展属新增能力，B3 单列评审**） | CPU 不改路径 |
| `internal/service/materials.go` | preset 名称/kind、整数参数取值泛化 | CPU 输出不变 |
| `internal/service/query.go` | `ExecutionView.kind` 由 Release 映射；交付/登记状态 | CPU 输出不变 |

### 4.3 配置模板

微调 **Release** 承载两段程序（训练 + 导出）与登记目标。**配方固化在镜像内**
（`/config/...`），`configs/` 仅承载 prepare *派生* 的 `dataset_info.json` 与路径重写后的
等价配置（§2.5）。模板见 `contract/cpup01/conformance/finetuning-release-v1.json`：

```jsonc
{
  "kind": "FINETUNING",
  "delivery_mode": "SAVE_ARTIFACTS",
  "program": {
    "command": ["llamafactory-cli", "train", "/config/finetune_cpu.yaml"],
    "config_path": "/config/finetune_cpu.yaml",           // 镜像内锁定配方（配方内路径已写 /workspace 平台绝对路径，prepare 无需重写）
    "parameter_contract_version": "ani.lora.sft.parameters.v1",
    "user_parameters": ["learning_rate", "max_steps"],     // 仅这两项用户可填（D04）
    "default_parameters": [
      {"name": "learning_rate", "type": "DECIMAL", "value": "0.0001"},
      {"name": "max_steps", "type": "INTEGER", "value": "20"}
    ]
  },
  "export_program": {
    "command": ["llamafactory-cli", "export", "/config/export_cpu.yaml"],
    "config_path": "/config/export_cpu.yaml"
  },
  "verify_program": {
    "command": ["python", "/opt/lf/verify_cpu.py",
                "/workspace/export/merged", "/workspace/reports/verify.json"],
    "max_new_tokens": 16, "decoding": "greedy"          // cpu-baseline/README L35
  },
  "resources": {"nodes": 1, "processes_per_node": 1,               // Release 必填
    "request_millicpu": "2000", "limit_millicpu": "4000",          // 字符串（int64,string）
    "request_memory_bytes": "6442450944", "limit_memory_bytes": "8589934592"},
  "workspace": {"mode": "EXECUTION_PVC", "storage_class": "<sc>",  // Release 必填；合法枚举
    "capacity_bytes": "21474836480",
    "input_subpath": "inputs", "configs_subpath": "configs",       // configs 为新增
    "training_subpath": "training", "export_subpath": "export",    // export 为新增
    "reports_subpath": "reports", "publication_subpath": "publication"},
  "execution_timeout_seconds": 3600,                               // Release 必填
  "runtime": {"name": "<training-runtime>", "kind": "ClusterTrainingRuntime",
    "api_group": "trainer.kubeflow.org", "target_jobs": ["<train-job-label>", "<export-job-label>", "<verify-job-label>"]},  // 三阶段目标（同一锁定镜像）
  "output_contract": {
    "schema_version": "ani.finetune.output.v1",
    "output_kind": "LORA_ADAPTER",
    "delivery_mode": "SAVE_ARTIFACTS",
    "create_tar_bundle": true,                 // 未压缩 tar（§7.4）
    // required_files = 全部 collected 集合（存在性/角色/大小校验）；各条目的
    // `in_tar` 标记区分"入 tar 的合并 HF 文件"与"普通发布文件"（见 §7.4）。
    // collector 仍逐项收集 required_files（collector.go）。合并集须**确定性全枚举**（C）。
    "required_files": [
      // —— 入 tar 的合并 HF 文件（tar 成员名 = 相对路径取基名 path.Base，根不套层）——
      // max_size_bytes 均为起测上界，待实测校准
      {"role": "MERGED_CONFIG",            "relative_path": "export/merged/config.json",             "max_size_bytes": "1048576", "in_tar": true},
      {"role": "MERGED_GENERATION_CONFIG", "relative_path": "export/merged/generation_config.json",  "max_size_bytes": "1048576", "in_tar": true},
      {"role": "MERGED_WEIGHTS",           "relative_path": "export/merged/model.safetensors",       "max_size_bytes": "2147483648", "in_tar": true},
      {"role": "MERGED_TOKENIZER",         "relative_path": "export/merged/tokenizer.json",          "max_size_bytes": "20971520", "in_tar": true},
      {"role": "MERGED_TOKENIZER_CONFIG",  "relative_path": "export/merged/tokenizer_config.json",   "max_size_bytes": "1048576", "in_tar": true},
      {"role": "MERGED_VOCAB",             "relative_path": "export/merged/vocab.json",              "max_size_bytes": "4194304", "in_tar": true},
      {"role": "MERGED_MERGES",            "relative_path": "export/merged/merges.txt",              "max_size_bytes": "2097152", "in_tar": true},
      // —— 不进 tar、作为普通发布对象 ——
      {"role": "ADAPTER_WEIGHTS",  "relative_path": "training/adapter/adapter_model.safetensors", "max_size_bytes": "268435456", "in_tar": false},
      {"role": "ADAPTER_CONFIG",   "relative_path": "training/adapter/adapter_config.json",  "max_size_bytes": "1048576", "in_tar": false},
      {"role": "VERIFY_REPORT",    "relative_path": "reports/verify.json",                   "max_size_bytes": "1048576", "in_tar": false},
      {"role": "METRICS",          "relative_path": "reports/metrics.jsonl",                 "max_size_bytes": "10485760", "in_tar": false},
      {"role": "SUMMARY",          "relative_path": "reports/summary.json",                  "max_size_bytes": "1048576", "in_tar": false}
    ],
    "max_file_count": 12,       // = len(required_files)（精确计数，不用 0 哨兵）；须与真实枚举一致
    "max_total_bytes": "4294967296"  // 起测上界 4 GiB（含 tar 字节；合并模型独立上传+tar 双份+adapter/报告），待实测校准
  },
  "registration": {
    "reuse": "Model.CreateModelVersion",       // 复用现有 Model RPC（D11）
    "executed_by": "modeldev",                  // 由 modeldev 侧执行，KFP 仅触发（A11/D06）
    // new_model_name 分支需先 CreateModel；target_model_id 分支直接建版本
    "new_model_branch": "CreateModel",          // 名称受 ValidateModelName 约束（见 §7.5）
    "format": "safetensors",                    // 仅 safetensors|gguf|pytorch（Version.Validate）
    "is_encrypted": false,                      // 首版仅未加密（§2.1）；encrypt_algo/encrypt_hint 留空
    "engine_type": "",                          // 首版不声明推理引擎，由推理平台默认
    "startup_command": "", "startup_args": [],  // 首版不声明启动命令（RPC 字段存在，默认空）
    "version_rule": "由 execution 推导（如 exec-<execution_id>），非“查最大版本+1”",
    "provider": "upload",                       // model-service 固定 provider="upload"
    "storage_path": "<model-service storage 租户作用域内的对象引用>",  // 见 §7.5
    "registration_key_prefix": "md-"
  }
}
```

> `in_tar` 是微调分支为"入包集合"新增的标记（CPU 分支无此字段、语义不变）：
> 见 §7.4。它把"进模型 tar 的合并 HF 文件"与"一起发布但不进 tar 的 adapter/报告/metrics"
> 分开，避免 tar 根出现 `adapter/`、`reports/` 前缀，满足 A10/D11。

> **登记 RPC 字段对齐（§7.5 详述）**：`CreateModelVersion` 的
> `storage_path`/`checksum_sha256`/`size_bytes`/`version`/`format` 均为必填语义；
> `checksum_sha256` 是**未压缩 tar 整体**的摘要；`version` 与 `idempotency_key` 是两个
> 不同字段（前者是版本号，后者是幂等键，冲突返回 409 CONFLICT）。

锁定训练配方与导出配方直接采用 `cpu-baseline`（跨仓，位于
`kubeflow文档/模型研发平台-v0.4/cpu-baseline/` 下的 `finetune_cpu.yaml`、`export_cpu.yaml`）：
`stage: sft`、`finetuning_type: lora`、`lora_target: all`、`lora_rank: 8`、
`lora_alpha: 16`、`dataset: ani_cpu_smoke`、`cutoff_len: 128`、`max_steps: 20`、
`use_cpu: true`、`seed/data_seed: 42`。
**其中 `model_name_or_path`、`dataset_dir`、`output_dir`、`export_dir` 不照抄基线的
`/ani/...`，镜像内配方直接写平台绝对路径（`/workspace/...`，见 §2.5）**，
prepare 不做路径重写。

数据集映射采用 `cpu-baseline/dataset_info.json`（`ani_cpu_smoke` → `finetune_data.json`，
Alpaca `instruction/input/output`）。**约束**：`dataset_info.json` 的 `file_name` 必须与
`prepare` 物化后的数据文件名一致——平台输入版本的实际 S3 key/文件名未必叫
`finetune_data.json`，`prepare` 重命名/落盘时须保证与 `file_name` 对齐（建议固定落为
`finetune_data.json`），否则 LLaMA-Factory 找不到数据集。且 `dataset_info.json` 由
prepare 物化到 `{ws}/inputs/data/`（与数据文件**同目录**，满足 LLaMA-Factory 在
`dataset_dir` 下查找的语义；见 §2.5）。

### 4.4 入口调用示例

```jsonc
// POST /admin/v1/modeldev/executions
{
  "name": "cpu-ft-smoke",
  "kind": "FINETUNING",
  "preset_id": "<微调 preset UUID>",
  "dataset_version_id": "<已 READY 的 Alpaca JSON 输入 UUID>",
  "base_model_version_id": "<批准的基模版本 UUID>",   // 首例 Qwen2.5-0.5B-Instruct
  "finetuning": {"learning_rate": "0.0001", "max_steps": 20},
  "new_model_name": "cpu-ft-demo",
  "idempotency_key": "<本次新建 UUID>"
}
```

微调链步骤（基于 v0.4 D10 调整）：

```
prepare → train-wait → export-wait → verify-wait → collect → publish → register → close
```

> **两处与 D10 的偏差及理由**：
> ① `collect` 后置到 `verify-wait` 之后——verify 报告必须进入 `RequiredFiles` 发布集合
> （`len(files)==len(RequiredFiles)` 精确校验），而训练产物在 `train-wait` 成功时已固化
> 于工作区，不损失证据语义；
> ② `export`/`verify` 为受管等待步（`export-wait`/`verify-wait`）等待的 **Trainer 阶段**，
> 不在组件镜像内执行——三阶段共用同一锁定 LLaMA-Factory 镜像、各自独立 TrainJob 与
> 超时、不合成一个 shell（出处 `cpu-baseline/README` L34；D02 `export_verify` 合并组件
> 为"建议落点"，本设计以"同镜像三阶段"等价承载并注明偏差）。

程序输出为 Adapter 与合并完整 HF 目录，最终以未压缩 tar 交付并登记为 Model 版本。

### 4.5 验证测试用例

| 用例 | 位置 | 断言 |
|---|---|---|
| 微调快照合法并规范化 | `finetuning_test.go` | 通过、可 digest |
| 能力混用拒绝 | `finetuning_test.go` | CSV 输入/LoRA 输出/越界参数被拒 |
| 用户参数收敛 | `finetuning_test.go` | 仅 learning_rate/max_steps 被接受，lora_rank 等被拒 |
| 微调 Release 程序注册 | `finetuning_test.go` | CPU 命令在微调 kind 下被拒 |
| 基模版本必需 | `finetuning_test.go` | 缺 `base_model_version_id` 的意图/Release 被拒 |
| 受管 recipe 通道 | `finetuning_test.go` | `--recipe success\|fail` 通过，其它被拒 |
| 一致性向量 | `finetuning_vector_test.go` | 嵌入向量可解析、摘要 64 位 |
| wire 往返 | `finetuning_codec_test.go` | kind/output_kind/参数摘要/**`in_tar`**保持 |
| CPU-P01 不回归 | `snapshot_test.go`/`intent_test.go` | 原字节、原摘要、原拒绝集全绿 |
| 输入结构校验 | `dataset_verifier_test.go` | Alpaca JSON 合法/畸形/计数/摘要 |
| 工作区子路径校验 | `snapshot_test.go` | 新增 `configs/export` 参与重叠/规范化校验 |
| tar 入包子集 | `output_manifest_test.go` | `in_tar` 子集入包、根不套层、无 manifest；CPU 分支不变 |
| tar 校验侧映射 | `output_verifier_test.go` | 微调分支 `wanted` 不含 manifest、tar 成员名↔相对路径映射一致 |
| 基模解析（未就绪/加密） | `model_resolver_test.go` | 非 ready 拒绝；`is_encrypted=true` 明确不支持 |
| 登记分支与存储归属 | `register_model_test.go` | `new_model_name` 走 `CreateModel`；`storage_path` 映射/复制后校验；`version` 非"+1" |
| 三阶段/登记映射 | D16 C17/C18/C20 | 训练+合并+短推理、导出损坏拒绝、登记字段匹配 |
| 多级路径收集（负向） | `collector_test.go`/`pipeline_io_test.go` | `../`、绝对路径、中间目录 symlink 替换均拒绝；`export/merged/config.json` 可正常收集 |
| `PrepareInput` 分支 | `pipeline_io_test.go` | 微调落 `finetune_data.json`+`dataset_info.json` 于 `inputs/data`；CSV 分支字节不变 |
| 导出清单失配 | `collector_test.go` | 合并目录实际文件与 Release 枚举不一致（缺/多）时明确失败 |
| `target_model_id` 分支 | `register_model_test.go` | 直接 `CreateModelVersion` 建版本；同 model 同 version 重复语义明确 |
| `max_steps` 越界 | `finetuning_test.go` | >200（首版上界）拒绝 |

> 每条用例实施时补齐期望观测（日志关键字 / 输出对象 key 清单 / status），并映射到
> D16 C17/C18/C20/C21/C23；资源与超时按 §4.6 压测校准。

### 4.6 资源与超时调度（起测预算，待实测校准）

> 预算依据 v0.4 A06 与 `cpu-baseline/README`（2CPU/6Gi → 4CPU/8Gi、工作区 20Gi，
> "起测预算，不是已证实峰值"）。现有 `cpu_p01.py` 统一 100m/500m、128Mi/512Mi
> **不适用微调**；新 pipeline 文件按步注入以下限额。

| 步骤 | 类型 | CPU req/limit | 内存 req/limit | 步超时 | 说明 |
|---|---|---|---|---|---|
| prepare | 组件 | 2 / 4 | 6Gi / 8Gi | 900s | 1 GiB 基模流式下载 + 数据集物化（§2.1） |
| train-wait | 组件 | 0.5 / 1 | 512Mi / 1Gi | 3600s | 仅等待 TrainJob；训练本体限额由 Release `resources` 承载 |
| export-wait | 组件 | 0.5 / 1 | 512Mi / 1Gi | 1800s | 等待导出阶段（合并加载全模型 + 写 ~1–2 GiB） |
| verify-wait | 组件 | 0.5 / 1 | 512Mi / 1Gi | 900s | 等待校验阶段（加载 0.5B 模型短推理） |
| collect | 组件 | 1 / 2 | 2Gi / 4Gi | 900s | **须流式**读取大文件/tar（若现状非流式须先改造，§4.2） |
| publish | 组件 | 1 / 2 | 2Gi / 4Gi | 1800s | ~2 GiB tar 上传；tmp 卷 ≥2Gi（现状 16Mi 不够） |
| register / close | 组件 | 0.5 / 1 | 512Mi / 1Gi | 600s | register 由 modeldev 侧执行 RPC |

- 工作区 PVC ≥ 20 GiB（§4.3 模板已填 21474836480）。
- **调度**：训练/导出/校验 TrainJob 与各组件均调度到 CPU 池（`use_cpu: true`；
  nodeSelector/toleration 由 `<training-runtime>` 承载，禁入 GPU 池——A06 禁 GPU
  专有配置）。CPU 下 20 steps 冒烟预期耗时量级为分钟级，压测后校准。
- **总时限**：`execution_timeout_seconds=3600` 为起测值，分步超时之和不得超过总时限；
  实施后按 §4.5 用例压测校准，并遵循 D08"重试不延长 deadline_at"。

### 4.7 异常与失败路径

| 失败场景 | 阶段 | 执行状态 | 可重试？ | 幂等键 / 动作 |
|---|---|---|---|---|
| 基模非 ready / 加密 / 不在 allowlist | 受理 / prepare | 受理拒绝或 prepare 失败 | 修正后重开执行 | 无 |
| 下载 URL 过期 | prepare | 组件失败 | 自动（组件内重签） | 每次实际读取前签发（§2.1） |
| 训练失败（train-wait 非零） | train-wait | 失败 | 用户重开；workspace 保留供排查 | — |
| 导出损坏 / 缺文件 | export-wait → verify-wait | verify 失败，**不得登记 READY**（C18） | 用户重开 | — |
| verify 不通过 | verify-wait | 失败，不登记 READY | 用户重开 | — |
| collect / publish 失败 | collect / publish | "计算成功、交付失败" | 人工触发原步骤重试 | 同 publication 幂等键 |
| register 冲突 | register | 交付失败 | 人工重试原登记 | 同 `registration_key`（`md-<execution_id>`）；fingerprint 相同返回原记录、不同 409（23505→409，model.go L604-606）；**不重复训练**（A09/D12/C21） |
| 执行超时 | 任意 | 失败 | 先查证再重试 | **超时不证明没有执行**——登记前用原 key 查证（§7.5.1/规范 §5） |
| PVC 清理 | close | — | close 成功后按既有 close_worker 语义回收；失败路径保留工作区（§7.4"发布失败不删 PVC"） | — |

## 5. 不可复用的独立模块与最小侵入

| 模块 | 为何不可复用 | 最小侵入实现 |
|---|---|---|
| 训练配方与负载 | CPU 用自研 MLP；微调用锁定 LLaMA-Factory，算法与入口无共同代码 | 新增 `finetune_cpu.yaml`/`export_cpu.yaml` 与 LLaMA-Factory 镜像，与 `train_mlp.py` 并列，互不引用 |
| 镜像 | 依赖集不同（LLaMA-Factory + transformers/peft） | 新增 `Dockerfile.finetune`，复用同一离线 wheel 纪律；不加入 `build-image.sh` 的 10 轮断言 |
| 输入结构校验与物化 | CSV 与 Alpaca JSON 结构不同；`PrepareInput` 硬编码 `data.csv` | 新增 `VerifyAlpacaJSON`（含 ≤32 MiB 上限，A03），复用 `verifier.verify` 核心；`PrepareInput` 按 kind 分支；不改 `VerifyCSV` |
| 输出角色枚举 | 微调产物（adapter/合并 tar/verify 报告）与 CPU 不同 | 纯新增 proto 枚举值，不改既有编号 |
| 基模解析与登记 | 需调 Model RPC，且登记要求对象落在 model-service storage 的租户作用域；CPU 无此环节 | 新增 `internal/data/model/client.go` + `publisher.go`（映射/受控复制）；复用现有 Model 公开 RPC，不造第二仓库（D11/§7.5.1） |
| 受管步骤集合 | `managed_runtime.go` 任务白名单硬编码 5 个步骤，微调需 8 个 | 白名单按 kind 扩展（`export-wait/verify-wait/register` 增量，合并白名单 + `Configuration()` 返回期望步骤集）；export/verify 为 Trainer 阶段等待步而非组件执行（对齐 D02 同镜像语义，粒度偏差已注明 §4.4），CPU 分支判定与鉴权不变（§1.2/§7.3） |
| 合并产物入包/发布 | tar 成员只能来自 collected 集合；微调需区分"入 tar 的合并 HF 文件"与"普通发布文件"，且现读写层不支持多级路径 | `RequiredOutput` 增 `in_tar`；`collectFile`/`capturePipelineFile` 多级路径安全打开；`bundlePipelineFiles` 按 kind 选子集、成员名取基名、排除 manifest；CPU 分支字节不变（§7.4） |
| 微调 KFP Pipeline | 微调链含 export-wait/verify-wait/register 阶段，与 CPU 六步不同 | 新增独立 `pipelines/finetune_cpu.py`，`cpu_p01.py` 零改动（§7.3） |

**破坏性改动控制：**

1. 所有校验改动均为 `if kind == KindFinetuning { ... } else { 原逻辑 }`，
   CPU-P01 的判定顺序、错误字符串、规范字节、spec hash 完全不变。
2. proto 仅**新增**枚举值与可选字段，不重编号、不删字段。
3. 新增文件均为独立包/文件，不修改既有测试的期望值。
4. `pipelines/cpu_p01.py`、`train_mlp.py` 与其测试零改动。

## 6. 验证边界

- **本设计尚未实施，故没有对应的本仓验证结果。** 待实施后应按 §4.5 用例在
  `contract/...`、`internal/biz/...`、`internal/service/...` 与数据集校验测试上验证。
- **未验证（需在 Fedora/集群补齐）**：微调镜像离线构建与 wheel 锁、LLaMA-Factory
  真实训练/导出与 CPU 上短推理、GPU/QLoRA 扩展、Model 登记 RPC 联调、目标集群
  KFP/Trainer/BFF/S3 与 v0.4 D16 的 C01–C34 业务验收。这些不因本设计的代码完成而成立。
  另需补齐：§4.6 资源/超时预算压测校准、§4.7 异常路径演练（超时查证、登记冲突重试）。
- 一致性向量与 Release 模板均为**待新增的合成夹具**，不构成真实 ENV、镜像或已启用 Release。

## 7. v0.4 对齐实施批次（必做；代码待实施）

> 本节是**对齐 v0.4 必做基线**的实施清单，非可选。**代码尚未实施**，仅在本文档定义；
> 实施顺序见 §7.6。它不改动 CPU-P01 的字节/摘要/拒绝集。

### 7.1 v0.4 与首版草稿的差异（已在本版正文收敛）

v0.4 业务册 A06/A09/A10/A11 与 `cpu-baseline/` 要求微调走**锁定的 LLaMA-Factory 配方**、
基模来自 **Model 仓库**、闭环含**登记**。**本版正文（§2–§4）已按 v0.4 收敛**，差异
不再是"二选一"，而是落地程度：

| 维度 | v0.4 要求 | 本文档正文状态 |
|---|---|---|
| 基模来源 | Model 仓库固定版本（`base_model_version_id`） | §2.1 已对齐 |
| 训练/导出/校验 | LLaMA-Factory 阶段分离 | §4.3 已用三段 program 表达（train/export/verify 同一锁定镜像） |
| 数据 | Alpaca JSON + `dataset_info.json` | §4.3/§7.2 已对齐 |
| 交付 | 未压缩 tar（根不套层） | §4.3 `create_tar_bundle=true`；§7.4 |
| 独立校验 | verify 短推理 | §2.4 已列为必经步骤；§7.5 |
| 模型登记 | Model `CreateModelVersion` | §4.3 registration 段；§7.5 |

### 7.2 数据契约对齐（`dataset_info.json` + `finetune_data.json`）

LLaMA-Factory 需要一个 `dataset_info.json` 把数据集名映射到文件与列名，再加一个
数据文件：

```json
{ "ani_cpu_smoke": { "file_name": "finetune_data.json",
  "columns": { "prompt": "instruction", "query": "input", "response": "output" } } }
```

**对齐方式**：在 capability 内注册**输入容器格式** `ani.finetune.alpaca.v1`
（Alpaca JSON，`instruction/input/output`，非空数组，**首版固定 64 条**——与 CSV
1024×16 同级的契约常量；`RowCount` 对 JSON 数组 = 数组元素数），在
`validFinetuningInputContract` 中作为**首选形态**，并让 `prepare` 步骤在落盘时把
平台输入物化为 `{ws}/inputs/data/{finetune_data.json,dataset_info.json}`（**同目录**，
满足 `dataset_dir` 查找语义；工作区子路径见 §2.5；`file_name` 一致性约束见 §4.3）。

- 输入对象仍是一个不可变固定的 S3 对象（可以就是 `finetune_data.json`）；
  `dataset_info.json` 作为**受管常量**随镜像或 Release 固定，不由用户上传。
- 契约校验仍校验行数（`RowCount`）与 SHA256，不放松。

### 7.3 三阶段：训练、导出与校验分离

"训练与导出在 Pipeline 中是不同阶段，不自动在一个 shell 中连续跑完"（出处
`cpu-baseline/README` L34；D10 步骤表仅语义支持）。

**方案（已收敛，不改动共享编排）**：新增**独立的微调 KFP pipeline 文件**
`pipelines/finetune_cpu.py`，复用与 `cpu_p01.py` 相同的骨架：

```
workspace-name → CreatePVC → prepare
              → train-wait  （TrainJob 阶段1: llamafactory-cli train）
              → export-wait （TrainJob 阶段2: llamafactory-cli export）
              → verify-wait （TrainJob 阶段3: verify_cpu.py 短推理）
              → collect → publish → register → close
```

- 两条 pipeline 各自独立，`cpu_p01.py` **零改动**（CPU-P01 字节/摘要/回归不受影响）。
- 每个 Release 通过自己的 `pipeline_version_id` 指向对应 pipeline，无需在编排层
  增加能力分支。
- **阶段建模已收敛为方案一**：train/export/verify 为同一受管执行下的**三次 Trainer
  阶段提交（阶段序号）**，共用同一锁定 LLaMA-Factory 镜像、各自独立 TrainJob 与超时，
  不合成一个 shell（`cpu-baseline/README` L34）；三阶段唯一来源是 Release 的
  `program`/`export_program`/`verify_program`（§4.3）。现状 `data/trainer/create.go`
  为单次提交，**TrainingPlan/EnsureTraining 的阶段扩展属 biz/data 新增能力，B3 单列
  评审**。
- **组件镜像不做推理**：`export-wait`/`verify-wait` 仅等待对应阶段，短推理在锁定镜像的
  TrainJob 内执行——这是与"export/verify 作为受管组件"路线的关键差异（D02
  `export_verify` 合并组件为建议落点，本设计以同镜像三阶段等价承载）。
- **`collect` 后置**到 `verify-wait` 之后（与 D10 偏差，理由见 §4.4 注）。

### 7.4 合并 tar 发布与输出契约表达（已具备基础设施）

关键发现：**未压缩 tar 的打包与校验已实现**，只是微调未启用。

- 打包：[bundlePipelineFiles](../../internal/data/workspace/pipeline_io.go) 用
  `archive/tar` + `tar.FormatUSTAR` 写入**扁平、根不套层**的条目；由
  `OutputContract.CreateTarBundle` 触发。
- 校验：[VerifyOutputBundle](../../internal/data/objectstore/output_verifier.go)
  逐条目核对清单（拒绝 PAX/linkname/重复/尾随字节）。

**输出契约表达（补齐 §4.3）**：纠正前一版"tar 内独立成员清单/`max_file_count=0` 哨兵"的
写法——它与代码不符。事实链：

- [collector.go](../../internal/data/workspace/collector.go#L36-L53) 的 `Collect` **严格遍历 `contract.RequiredFiles` 逐项收集**，
  collected `files` 就是 RequiredFiles 的镜像；
- [output_manifest.go](../../contract/cpup01/output_manifest.go#L26-L47) 要求
  `len(files) == len(RequiredFiles)` 且逐条 path+role 精确匹配、size 上限匹配
  （`SizeBytes <= MaxSizeBytes`）；
- [pipeline_io.go](../../internal/data/workspace/pipeline_io.go#L167-L168) 的 tar 由 `files`（=collected）构建。

因此**不存在代码意义上的"独立 tar 成员清单"**：tar 成员只能来自 collected 集合。
微调分支的正确表达是**在 `RequiredFiles` 内区分"入包/不入包"**：

- `RequiredFiles` 仍是全部 collected 集合（`collector` 逐项收集、`OutputManifestBytes`
  仍按 `len(files)==len(RequiredFiles)` 精确校验），**不使用 `max_file_count=0` 哨兵**；
- 每条 `RequiredOutput` 增 `in_tar` 标记（§4.2）：`in_tar=true` 的是**合并 HF 模型文件**，
  `in_tar=false` 的是 adapter、verify 报告、metrics、summary（作为**普通发布对象**，
  单独上传、不进 tar）；
- `bundlePipelineFiles` 入参从"全部 files"改为**按 kind 选定的入包子集**
  （CPU 分支 = 全部 files，行为不变；微调分支 = `in_tar=true` 的子集）。

**tar 根成员与"根不套层"**：

- 合并 HF 文件在 `RequiredFiles` 中以 `export/merged/...` 定位（供 collector 读取），
  但**写入 tar 的成员名 = 该条目相对路径的基名（`path.Base`，如
  `export/merged/config.json` → `config.json`）**——注意不是 `TrimPrefix("merged/")`
  （那会得到 `export/config.json`）——使 tar 根直接是 `config.json`/`tokenizer.json`/
  `model.safetensors`，满足 A10/D11"根不套层"；
- **`output-manifest.json` 不进模型 tar**（现有 `bundlePipelineFiles` 会把它作为根成员
  写入；微调分支须排除，CPU 分支保持现状），避免被 Inference HF 物化器判为非法成员；
  清单仍作为独立发布凭证存在，`manifest_sha256` 与 `bundle_sha256` 各自独立、无递归；
- adapter 走**独立归档**，report/metrics 作为普通发布对象——均**不入**模型 tar。

**校验侧必须同步（否则打包与核对不自洽）**：`internal/data/objectstore/output_verifier.go`
的 `VerifyOutputBundle` 当前与上述三件事硬冲突，微调分支须改（**列入 §4.2**）：

- `output_verifier.go` L26 无条件把 `output-manifest.json` 加入 `wanted`，L49 要求
  `len(seen)==len(wanted)` → 微调分支 **不把 manifest 加进 `wanted`**，否则必失败；
- L38 用 `header.Name` **精确**查 `wanted` → tar 成员名（取基名后）与
  `RequiredFiles[].RelativePath`（`export/merged/...`）**不一致**，须在校验侧做**反向映射**：
  `tar 成员名 ↔ RequiredFiles 相对路径` 的规则为「`in_tar=true` 条目取基名
  （`path.Base`）」，或在 manifest 中显式承载该映射；**打包侧与校验侧必须用同一规则**；
- L20 拒绝 `RelativePath=="output-manifest.json"` 的 `files` 条目——与方案兼容，
  但实现时勿把 manifest 混入 `files`。

**合并文件集须确定性枚举（C）**：collector 与 `OutputManifestBytes` 均要求
`len(files)==len(RequiredFiles)`，故微调 Release **必须完整、确定地枚举合并目录的每个
文件**（Qwen 导出集固定：`config.json`、`generation_config.json`、`model.safetensors`、
`tokenizer.json`、`tokenizer_config.json`、`vocab.json`、`merges.txt` 等，逐一列出并给出
各自 `max_size_bytes`），`max_file_count` = 该枚举条数。§4.3 已给出**完整枚举与起测上界**（含各文件
`max_size_bytes` 与 4 GiB `max_total_bytes`，待实测校准）。**失配检测**：collect 时
合并目录实际文件集与枚举不一致（缺/多）必须以明确错误失败，不允许静默缺漏或忽略
多余文件（如 `special_tokens_map.json`、`added_tokens.json`——镜像升级可能引入；
若确需纳入，走 Release 版本更新而非静默容忍）。

**双重发布说明（D）**：`in_tar=true` 的合并文件会**既**作为独立对象上传
（key=`prefix+export/merged/...`）**又**进 tar——这与 CPU 现状一致（`UploadOutput` 对
`files` 各传一份 + bundle 一份），是既有设计，非重复错误。对微调即约 2× 合并模型体积
的发布流量（`max_total_bytes` 起测 4 GiB，§4.3），容量规划时计入。

> 该 tar 与 v0.4 A10 一致；D11 特别指出 Model/Inference 的 `checksum_sha256` 必须是
> **整个归档字节**的摘要。

### 7.5 独立加载校验与模型登记

- **独立校验（verify 阶段，首版必经）**：v0.4 要求"检查完整文件后加载模型，最多生成
  16 token，固定贪心解码，不检查必须回答某句话"（出处 `cpu-baseline/README` L35；
  A09/D10 要求短推理）。对齐方案：作为与 `export` 共用锁定镜像的**独立 TrainJob 阶段**
  （受管 `verify-wait` 等待，§7.3），独立加载合并模型并完成一次固定短推理，产出加载
  可用报告与文件摘要；verify 失败**不得登记 READY**（D16 C18）。
- **模型登记（register 步骤，首版必经）**：v0.4 **把 register 作为微调链的一个受管步骤
  放在 modeldev 内**（D02 `components/register`、D06 `RegisterPublishedModel`、
  D10 register 步、D11）。**执行侧归属**：真正调用 Model 的是 **modeldev 服务侧**
  （D06 内部 RPC `RegisterPublishedModel`），**KFP 的 `register` 步骤只是发起方**，
  不让训练程序/训练 Pod 拿到模型仓库管理凭据（A11）。登记**复用现有 Model RPC**，
  **不造第二仓库**（D01 复用 ani-model-service）。登记失败保留"计算成功、交付失败"的
  真实状态，人工触发原登记重试（同 `registration_key`，如 `md-<execution_id>`），
  不重复训练（A09/D12）。

#### 7.5.1 modeldev → model 调用链（新增依赖）

委托 `ani-model-service` 的 `ModelService`（module `github.com/liangzai006/ani-model-service`，
生成包 `api/model/v1`）。这是**一条新的服务间调用链**，现有接入表只有
Gov→Network/Image/ModelDev/Accelerator/Inference，**没有 ModelDev→Model**，须按
`ani-governance/docs/service-integration.md` 新增登记。

> 本文引用跨仓文件时用「**仓库名:仓内路径** + 行号/章节」纯文本，不写本机绝对路径
> （跨仓无法用相对路径链接）。

**用到的 RPC（逐条对齐真实契约）**：

| RPC | 用途 | 真实约束（源码：`ani-model-service`） |
|---|---|---|
| `GetModelVersion` | 解析基模 | `internal/service/model.go` L106 强制 `DeploymentInput()`：`status=="ready"` 且 provider/reference/sha256 齐全 |
| `GetModelDownloadURL` | 实际读取前签发 | `internal/service/model.go` L403 短时效 **5 分钟**；响应带 `is_encrypted/encrypt_algo/encrypt_hint` |
| `CreateModel` | `new_model_name` 路径先建容器 | `internal/service/model.go` L116-149；名称受 `ValidateModelName`（小写/数字/`-`/`.`，≤63）约束 |
| `CreateModelVersion` | 建版本并置 READY | `internal/service/model.go` L250-318 |

**（1）存储归属 —— 最大缺口**：`CreateModelVersion` 传 `storage_path` 时会在
**model-service 自己的 storage、且按 `p.TenantID` 作用域**校验对象
（`ani-model-service: internal/service/model.go` L282-295）：`ObjectExists` +
`VerifyChecksum`，通过后 `CreateArtifact` + `MarkReady`，artifact 的
`reference` 即该 `storage_path`，`provider` 固定 `"upload"`。而 modeldev 的发布落在
**自己的** publication scope/bucket/凭据。二者必须二选一并在 §4.1 落实：

- **同存储**：modeldev 发布位置与 model-service storage 是**同一受管位置且同一租户
  作用域** → `objectRef` 直接映射为 `storage_path`；
- **异存储**：需一次**受控复制**到 model-service storage 并核验（v0.4 D11 已提示）→
  需要复制适配器（`internal/data/model/publisher.go`）。

**（2）`new_model_name` 需 `CreateModel` 分支**（D04 二选一）：
`target_model_id` → 直接 `CreateModelVersion(model_id=target_model_id, ...)`；
`new_model_name` → 先 `CreateModel`（返回 model）再建版本。`resolveModel`
（`ani-model-service: internal/service/model.go` L354-375）先按 UUID 再按 external id 解析。

**（3）`version` 与幂等键**：`CreateModelVersionRequest.version` **必填非空**
（`ani-model-service: internal/biz/model/model.go` L64），且**不得**"查最大版本+1"（D11）；
规则由 execution 推导（如 `exec-<execution_id>`）。`idempotency_key` 与 `version` 是两个
字段，前者冲突返回 409 CONFLICT（映射点：`ani-model-service: internal/service/model.go`
L604-606，PgError 23505 → 409；重试须携带构成一致的 `RequestFingerprint`）。**重放语义**（`ani-model-service: internal/service/model.go`
L271-278）：`status=="ready"` 时直接返回、不再校验/插 artifact——重试须用
**同一 `storage_path` + 同 key**。

**（4）身份/授权/依赖合同（须符合 `ani-governance/docs/contracts/service-to-service-calls.md`）**：

modeldev→model 属 **svc→svc**（该规范 §1），是"明确登记的领域依赖"，须按该规范登记并实现，逐项对应：

- **调用登记**：登记「调用服务身份 `ani-modeldev`、接收服务身份 `ani-model`、完整 RPC
  方法（`/model.v1.ModelService/{GetModelVersion,GetModelDownloadURL,CreateModel,CreateModelVersion}`）、
  业务范围（读取已批准基模版本 / 登记微调产物版本）、租户来源、幂等键、超时」；
  接收方维护**允许清单**，未知服务/方法默认拒绝，**禁止 `*` 与方法前缀授权**（规范 §2）。
- **mTLS/身份**：TLS 1.3 + 双向证书，禁止明文 fallback 与 `InsecureSkipVerify`；
  客户端身份取**唯一、精确登记的 DNS SAN**（`ani-modeldev`，`DNSNames==1`、无通配符/URI/IP/email）；
  调用方用可信配置的**字面 `server_name`** 校验服务器证书；`address` 只决定连到哪里，
  **不得从地址推导权限**，用户不能指定地址/`server_name`/CA/owner；**不与其它服务共用私钥、
  不冒用 Gov 证书**（规范 §2）。
- **配置**：一组 `ANI_MODEL_ADDR/CA/CERT/KEY/SERVER_NAME/TIMEOUT`（沿用"已校验正值/字面 DNS"
  纪律）；CA/证书/私钥/地址/`server_name`/正超时**必须齐全**——**必须依赖则启动失败**，
  可选依赖则禁用该功能并返回不可用，**禁止伪造成成功或回退明文**；连接由 composition root
  统一装配与关闭，证书只在启动读取者续期后须重启重载（规范 §4）。
- **租户/操作人/后台事实**：model-service 用
  `identity.RequireTenant`（`ani-model-service: internal/identity/context.go` L45-65）校验，
  **租户不能由请求体自报**。读基模属**用户委托**（tenant 取自前一步已验证并保存的
  业务上下文，即 modeldev 受理时已认证的租户）；登记属**后台事实**（模型登记是执行链
  的完成事实，用**自己的持久事实**，不伪造用户 Principal）。A11 明确不得开
  `ANI_ALLOW_DIRECT_ACCESS` 凑通；出站**重建 metadata**，不透传公网 Authorization/AK-SK/租户/
  owner header；身份多位置须单值一致，冲突拒绝（规范 §3）。
- **超时/重试/幂等**：每次 RPC 有明确 deadline 并继承调用链取消（不用 `context.Background()`
  切断）；写入重试用**原 `idempotency_key` 与原内容**，仅对合同允许的瞬时失败有限重试，
  关闭未审查的自动 retry/hedging；同 ID 同内容返回原结果、同 ID 异内容拒绝；**超时不证明
  没有执行**（登记超时后须用原 key 查证，不得盲目重建）（规范 §5）。
- **跨仓依赖**：modeldev import model-service 的**已发布 API 模块**
  `github.com/liangzai006/ani-model-service/api/model/v1` 并**固定版本**；
  **不跨仓导入 `internal`、不复制 Proto/生成代码、不用本地 `replace` 冒充正式互操作**
  （规范 §1/§5）。
- **落地要求**：新增调用须"**先登记并打通真实最小链路：调用方业务 → mTLS → 接收方权限/
  持久化 → 可观察结果**"，仅允许链路外部依赖用明确替身；model-service 侧需把
  `ani-modeldev` 加入其 mTLS 允许清单与信任 CA 登记（跨仓改动，属本设计**外部依赖**）。

**（5）`format` 与 artifact**：`Version.Validate`/`Artifact.Validate` 只接受
`safetensors|gguf|pytorch`（`ani-model-service: internal/biz/model/model.go` L45/L67）；
登记的 artifact 参考的是**未压缩 tar 整体**，`checksum_sha256` 必须是整个归档字节摘要。

### 7.6 实施顺序（必做）

| 批次 | 内容 | 触及层 | 风险 |
|---|---|---|---|
| B0 | **产物读写地基**：`collectFile`/`capturePipelineFile` 多级相对路径（逐级安全打开，保持防穿越语义）+ `PrepareInput` 按 kind 分支（CSV/JSON）+ `LogicalKey` 前缀按 kind | data/workspace | 高（防穿越安全语义保持） |
| B1 | 基模来源改为 Model 仓库（`base_model_version_id` + GetModelVersion/DownloadURL → 物化到 `{ws}/inputs/model`，prepare 改流式+上调资源）；**新建 modeldev→model 调用链身份/mTLS/跨仓依赖**（§7.5.1） | contract/biz/data/go.mod | 中 |
| B2 | 工作区/挂载路径映射（§2.5，`WorkspaceContract` 增 configs/export）+ 数据契约 Alpaca JSON + `dataset_info.json`（落 `inputs/data/` 与数据同目录） | contract/data | 中 |
| B3 | LLaMA-Factory 三阶段 pipeline（train → export → verify → collect）+ 合并 tar 与入包子集（`in_tar`、proto/codec 同步、`CreateTarBundle=true`、tar 成员名取基名、tar 根无 manifest、`output_verifier.go` 按 kind 分支与成员名映射）；**TrainingPlan/EnsureTraining 阶段扩展单列评审** | contract/pipelines/biz/data | 高（Trainer 阶段扩展 + `RequiredOutput.in_tar` + 校验侧映射） |
| B4 | register 步骤（modeldev 侧调 Model：`CreateModel`/`CreateModelVersion`）+ **存储归属映射/受控复制** + `version` 规则 + 交付/登记失败语义（§7.5.1） | biz/data/service | 中高（存储归属） |
| B5 | 锁定配方/镜像/夹具（`finetune_cpu.yaml`/`export_cpu.yaml`/`dataset_info.json`/LLaMA-Factory 镜像，路径按 §2.5） | training | 中 |

**共同约束**：B0–B5 均为**能力增量**，不得改变 CPU-P01 的字节、摘要、错误字符串与
拒绝集；每个批次独立可回滚。资源/超时为 §4.6 起测预算，实施后压测校准。验收以
v0.4 D16 的 C17/C18/C20/C21/C23（微调、导出、登记、新 HF 包推理）为准。

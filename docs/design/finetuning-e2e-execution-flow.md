# 模型微调任务端到端执行流程（FINETUNING / LoRA SFT）

> 本文梳理「用户在前端发起创建请求 → 各后端服务协同处理 → 模型回写仓库」的完整链路。
> 权威来源：`docs/design/finetuning-cpu.md`（微调 spec）、`api/ani/modeldev/v1/*.proto`、
> `api/ani/training/v1/*.proto`、`ani-governance` 的 `admin` BFF 实现、`ani-model-service` 的
> `internal/service/model.go`，以及 v0.4《精简开发实施说明》（`kubeflow文档/模型研发平台-v0.4/source/development.md`，D01–D18）。
>
> 更新日期：2026-10-13。

---

## 0. 阅读前提：现状基线 vs 目标形态（务必先读）

**这条链路当前只有「通用训练（`GENERAL_TRAINING` / CPU-P01）」是全链路已实现并验收的。**
「模型微调（`FINETUNING` / LoRA SFT）」是一份**待实施 spec**——设计文档自述（`finetuning-cpu.md` L49–54）：
"微调能力的代码改动已按评审决定撤回，仓库当前只保留本文档"。

因此本文的统一约定：

| 标记 | 含义 |
|---|---|
| ✅ **现状** | 仓库中存在真实代码 / 已验收 |
| 🧩 **待实施** | 仅存在于设计文档，仓库中无对应代码（微调专属） |
| 🔁 **共享骨架** | 现状已实现的通用机制，微调复用同一套（仅按 `kind` 分支） |

### 0.1 已实现 / 待实施对照表

| 环节 | 现状（✅ 已实现） | 微调目标（🧩 待实施） | 证据 |
|---|---|---|---|
| ExecutionKind | 仅 `EXECUTION_KIND_GENERAL_TRAINING=1` | 新增 `EXECUTION_KIND_FINETUNING` | `api/ani/training/v1/training.proto` L10-13 |
| OutputKind | 仅 `OUTPUT_KIND_CHECKPOINT` + 4 文件 | `LORA_ADAPTER` + adapter/merged/verify 角色 + `in_tar` | `contract/cpup01/release_parts.go` L50-53 |
| 参数契约 | `ani.cpu.mlp.parameters.v1`（epochs/batch_size/learning_rate，≤3 个） | `ani.lora.sft.parameters.v1`（learning_rate/max_steps） | `internal/biz/admission_resolution.go` L143 |
| 基模来源 | 无（不涉及模型仓库读取） | `base_model_version_id` → Model RPC 取回 | 🧩 设计 §2.1 |
| 训练配方 | 自研 `training/train_mlp.py` | 锁定 LLaMA-Factory 镜像 + `finetune_cpu.yaml`/`export_cpu.yaml` | 🧩 设计 §4.1 |
| Trainer 阶段 | **单次** TrainJob（`TargetJobs` 必须 == 1） | **三阶段**（train/export/verify），同一镜像三次独立 TrainJob | `internal/data/trainer/create.go` L226 |
| 受管步骤白名单 | 5 步 `prepare/train-wait/collect/publish/close` | 8 步（+ `export-wait/verify-wait/register`） | `internal/biz/managed_runtime.go` L134-139 |
| KFP Pipeline | 仅 `pipelines/cpu_p01.py`（6 任务） | 新增独立 `pipelines/finetune_cpu.py` | `pipelines/cpu_p01.py` |
| 工作区子路径 | `inputs/training/reports/publication` 4 个 | 增 `configs/`、`export/` | `contract/cpup01/snapshot.go` L110-118 |
| collect/发布 | 单层叶名、tar 成员=相对路径、tar 根含 manifest | 多级安全路径、tar 成员取基名、tar 根排除 manifest | `internal/data/workspace/pipeline_io.go` L283/L353/L365 |
| register（模型登记） | **无**（modeldev 无 model 依赖） | modeldev 侧调 Model RPC 登记合并后 HF 模型 | 🧩 设计 §7.5 |
| modeldev → model 调用链 | **不存在**；且 model-service 授权策略未放行 modeldev | 新增登记为 `svc→svc` 领域依赖 | `ani-model-service/cmd/ani-model-service/rpc_policy.go` L93-108 |

### 0.2 实现前必须闭合的关键缺口（Gap）

这些不是"细节"，而是会让链路根本跑不通的硬缺口：

1. **前端与后端字段不匹配**：前端原型（`微调任务原型参考.html`）是"LlamaFactory 参数直填式"长表单（预设模板/微调技术/基座模型路径/轮次/学习率…），而后端 wire 契约是 `preset_id + parameters[]` 的**键值参数模型**。「基座模型路径」这类字段在后端契约中**不存在**（基模只能通过 `base_model_version_id` 指定，见设计 §2.1）。两者需要收敛。
2. **gover 的创建契约无微调字段**：`CreateExecutionRequest`（`ani-governance/api/protos/modeldev/service/v1/execution.proto` L10-34）**没有** `finetuning` / `base_model_version_id` / `target_model_id` / `new_model_name`，`kind` 只声明支持 `GENERAL_TRAINING`。微调需按设计 §4.2 纯新增字段。
3. **modeldev 无 model 依赖**：`ani-modeldev-service` 全仓检索 `ModelVersion|CreateModelVersion|ani-model-service|base_model` **零命中**；`go.mod` 无 `ani-model-service`。
4. **model-service 授权策略未放行 modeldev**：`ModelServicePolicy()`（`rpc_policy.go` L93-108）**只授权** `ani-inference.ani.internal` 调用 `GetModel`/`GetModelVersion`/`GetModelDownloadURL` 三个只读方法；deny-by-default 下 `CreateModel`/`CreateModelVersion`/`GetUploadURL` **无任何身份被授权**。需在 model-service 侧把 `ani-modeldev` 加入 mTLS 允许清单（跨仓改动）。
5. **model-service 无目录/多文件上传**：合并后的完整 HF 模型（多文件）回写仓库，现有只能 `GetUploadURL`（单文件，15 分钟）+ `CreateModelVersion`；无目录上传/分片能力（`docs/superpowers/plans/2026-09-24-training-integration.md` 列为未来扩展）。
6. **受管步骤回调身份**：modeldev 的每个受管步骤回调都重验 workload 身份与 KFP task 归属（`managed_runtime.go` L43-120），微调新增步骤必须一并纳入。

> **本文的写法**：以"目标形态"描述全链路（因为用户的诉求是微调流程），但在每个环节显式标注 ✅ 现状 / 🧩 待实施 / 🔁 共享骨架，并给出证据。**不要把本文的"目标形态"当成"已经能跑"**。

---

## 1. 系统总览

### 1.1 参与方与职责

| 参与方 | 服务身份（mTLS DNS SAN） | 职责 |
|---|---|---|
| 前端（Web） | — | 创建表单、参数提交、**轮询**状态、展示产物/日志 |
| **ani-governance**（BFF） | `ani-governance` | HTTP 解析 → 鉴权 → **持久受理** → 后台 worker gRPC 投递 → 视图聚合 |
| **ani-modeldev-service** | `ani-modeldev-service` | 受理冻结、KFP 提交、受管步骤编排、Trainer 三阶段、收集/发布、登记 |
| **ani-model-service**（模型仓库） | `ani-model` | 基模解析与下载签发、模型/版本登记 |
| Kubeflow Pipelines (KFP) | — | Pipeline 推进（只推进流程，不按数据库阶段重演） |
| Kubeflow Trainer | — | 执行 TrainJob（训练/导出/校验三阶段） |
| S3 / MinIO | — | 对象存储（输入、产物、tar bundle） |
| PostgreSQL | — | 各服务持久化（受理/运行时/发布/模型元数据） |

### 1.2 通信矩阵

| 链路 | 协议 | 身份校验 | 关键点 |
|---|---|---|---|
| 前端 → gover | HTTPS + REST JSON（`lower_snake_case`） | `Authorization: Bearer <access-token>` | 禁止直连 modeldev 的 `:9000`/`:9001` |
| gover → modeldev | **gRPC over mTLS**（TLS 1.3 + 双向证书） | 客户端 DNS=`ani-governance`，服务端字面 DNS=`ani-modeldev-service` | 出站**重建 metadata**，不透传公网头；地址来自 `ANI_MODELDEV_ADDR/CA/CERT/KEY/TIMEOUT` |
| modeldev → KFP | HTTPS（CreateRun） | 受管 SA | 参数仅 `execution_id`、`spec_hash`；无 Idempotency-Key |
| modeldev → Trainer | Kubernetes API（TrainJob CRD） | 受管 SA | 固定 Runtime、单创建方、确定性命名 |
| 受管步骤 → modeldev | gRPC（受管 Step 口） | TokenReview workload + KFP task 归属校验 | 不给训练程序凭据 |
| modeldev → model | **gRPC over mTLS** 🧩 | 客户端 DNS=`ani-modeldev`（待登记） | 属 `svc→svc` 领域依赖，须登记允许清单；**现状未打通** |
| modeldev → S3 | S3 API（临时 STS 凭据） | 单执行单步骤临时凭据 | prepare 只限冻结输入 key；publish 只限 `ApprovedPrefix/executionID/` |

### 1.3 端到端时序（高层）

```
前端                gover(BFF)              modeldev                 model-service        KFP/Trainer/S3
 │  POST /executions  │                       │                          │                    │
 │───────────────────>│  鉴权+受理+双摘要      │                          │                    │
 │  202 Accepted      │  落库 QUEUED           │                          │                    │
 │<───────────────────│                       │                          │                    │
 │  (轮询详情)         │  worker: AcceptExecution│                          │                    │
 │                    │──────────────────────>│  持久 inbox + ACK         │                    │
 │                    │                       │  冻结快照/解析参数         │                    │
 │                    │                       │  解析基模─────────────────>│ GetModelVersion    │
 │                    │                       │<──────────────────────────│ (ready 校验)        │
 │                    │                       │  提交 KFP Run ────────────┼───────────────────>│
 │                    │                       │  受管步骤回调             │                    │
 │                    │                       │  prepare (物化基模/数据) ──┼───────────────────>│ S3: 下载+落盘
 │                    │                       │  train-wait → TrainJob 阶段1: train ─────────>│
 │                    │                       │  export-wait → 阶段2: export ────────────────>│
 │                    │                       │  verify-wait → 阶段3: verify ───────────────>│
 │                    │                       │  collect (多级路径/in_tar) ──────────────────>│
 │                    │                       │  publish (双份上传+tar) ────────────────────>│ S3
 │                    │                       │  register ───────────────>│ CreateModel/Version │
 │                    │                       │  close (回收 PVC)          │                    │
 │  GET /executions/{id}│ 视图映射(四轴)       │                          │                    │
 │<───────────────────│<──────────────────────│  ExecutionView           │                    │
```

---

## 2. 阶段一：前端创建微调任务交互（F1–F6）

### 2.1 页面结构（现状原型）

前端创建页 `/camp/admin/ai/manage/finetuning/create`（Vue 3 + Element Plus），是
**单页长表单 + 卡片分区 + 可折叠高级区**，不是分步向导。自上而下：

`基础信息` → `算力规格` → `实例镜像` → `存储配置` → `微调基础配置` → `训练超参数`
→ 折叠`更多微调参数` → 折叠`高级模式（分布式）` → 折叠`高级选项`，底部固定提交栏（单一「创建」按钮）。

> ⚠️ **重要**：该原型是"参数直填式"，其字段（预设模板 `quick/standard/quality`、微调技术
> `lora/full/qlora`、基座模型**路径**、训练轮次、LoRA Rank/Alpha…）与后端 wire 契约
> **不一一对应**。后端契约由 `preset_id` 决定参数集，用户只可填少量开放参数（微调首版仅
> `learning_rate`/`max_steps`）。**两者需要产品侧收敛**，否则前端提交的字段后端无处安放。

### 2.2 关键字段与校验（原型，节选）

| 字段 | 控件 | 默认 | 校验/范围 |
|---|---|---|---|
| 任务名称 | input | 空 | 异步唯一性校验，重复报 `PROJECT_NAME_DUPLICATE_JOB` |
| 架构 / 类型 | radio | `amd64` / `private_gpu` | — |
| 算力规格 | table 单选 | — | 型号/显存/卡数/CPU/内存/磁盘（11 种规格） |
| 训练阶段 | select | `Supervised Finetuning (SFT)` | 映射 `stage: sft` |
| 基座模型路径 | select（可输入） | — | 必须以 `/` 开头、无 `..`、必须已挂载 |
| 数据集 | select | — | 选择已创建数据集 |
| 微调技术 | radio | `lora` | `lora`/`full`/`qlora` |
| 训练轮次 | NumberInput | 1 | min 1 / max 100 |
| 学习率 | input | `1e-4` | 自由文本（后端契约要求规范十进制字符串） |
| Batch Size | NumberInput | 2 | min 1 / max 64 |
| 最大序列长度 | NumberInput | 1024 | min 256 / max 32768 |
| 训练精度 | radio | `bf16` | `bf16`/`fp16` |
| LoRA Rank / Alpha | NumberInput | 8 / 16 | 1–256 / 1–512 |

> **原型内无任何请求构造代码**（SingleFile 快照已剥离 `<script>`）：`fetch`/`axios`/
> `idempotency_key`/`kind`/`preset_id` 等关键词在文件中**零命中**。**前端提交格式必须以后端
> proto/HTTP 合同为准**（见 §2.3、§10），不能依据原型 DOM 推断。

### 2.3 参数提交格式（权威：后端合同）

**现状（✅ 通用训练）** —— `POST /admin/v1/modeldev/executions`：

```jsonc
{
  "name": "cpu-mlp-demo",
  "kind": "GENERAL_TRAINING",
  "preset_id": "<preset-uuid>",
  "dataset_version_id": "<ready-input-version-uuid>",
  "idempotency_key": "cpu-demo-001",
  "general_parameters": [
    { "name": "learning_rate", "type": "DECIMAL", "value": "0.01" }
  ]
}
```

**微调目标形态（🧩 设计 §3 / v0.4 D04）**：

```jsonc
{
  "name": "cpu-ft-smoke",
  "kind": "FINETUNING",
  "preset_id": "<微调 preset UUID>",
  "dataset_version_id": "<READY 的 Alpaca JSON 输入 UUID>",
  "base_model_version_id": "<批准的基模版本 UUID>",   // 首例 Qwen2.5-0.5B-Instruct
  "finetuning": { "learning_rate": "0.0001", "max_steps": 20 },
  "new_model_name": "cpu-ft-demo",                   // 与 target_model_id 二选一
  "idempotency_key": "<本次新建 UUID>"
}
```

**字段约束**：

| 字段 | 必填 | 说明 |
|---|---|---|
| `name` | 是 | 微调 1–80 字符 |
| `kind` | 是 | 应取自 `GET /presets` 的 `preset.kind`，**勿硬编码** |
| `preset_id` | 是 | 决定 capability、参数契约版本、镜像、配方 |
| `dataset_version_id` | 是 | 必须为 `READY` 的输入版本（微调为 Alpaca JSON，64 条） |
| `base_model_version_id` | 微调必需 | 平台自有 Model 仓库中的**已批准固定版本**，后端核验 allowlist |
| `finetuning` | 微调必填 | `learning_rate`（DECIMAL 字符串，首版 (0,1]）、`max_steps`（正整数，首版上界 200） |
| `target_model_id` / `new_model_name` | **二选一** | 决定 register 走 `CreateModelVersion` 还是先 `CreateModel` |
| `idempotency_key` | 是 | 同租户/主体/动作唯一；重放不换键 |
| `general_parameters` | — | 与 `finetuning` **互斥** |

**数值类型换算（易错点）**：D04 示例 `max_steps: 20` 是数字，但 `Parameter.Value` 契约是
**十进制字符串**。受理层须把 `finetuning` 的 JSON 值规范化为契约字符串（`20 → "20"`、
`0.0001 → "0.0001"`），**不做无类型 JSON 透传**（设计 §3）。

**幂等规则**：
- 超时/断网：**必须用同一 `idempotency_key` + 完全相同的意图**重试 → 返回原执行（`replayed=true`）。
- 同一 key 改参数 → `409 IDEMPOTENCY_CONFLICT`。
- `general_parameters` 的**省略 ≠ 显式 `[]`**（两种不同意图）；`null` 被拒绝。

### 2.4 响应与 202 语义

创建成功返回 **`202 Accepted`**：

```jsonc
{
  "operation_id": "uuid", "execution_id": "uuid", "resolved_release_id": "uuid",
  "replayed": false,
  "compute_state": "ACCEPTED", "delivery_state": "PENDING",
  "close_state": "OPEN", "resource_state": "NOT_APPLICABLE"
}
```

> **`202` 只代表「已持久受理」，不代表开始计算或训练成功。** 前端不得据此显示"训练中"。
> 响应体中的四轴只是**受理时刻快照**（若受理前已有持久投递确认，可能已是中间态）——
> 一律以轮询 `GET .../executions/{id}` 为准。

### 2.5 前端状态反馈机制

- **无 SSE / WebSocket 推送**，只能**轮询** `GET /admin/v1/modeldev/executions/{execution_id}`。
- 轮询建议：3–5s 一次，进入 `TRAINING` 后放缓到 10–15s。
- 响应为**直接返回 `ExecutionView` 对象**（无外层包装），含**四轴独立状态**（见 §12）。
- 判终态：`compute_state ∈ {SUCCEEDED, FAILED}`。
- 日志接口 `GET .../logs` 有 1000 行 / 64 KiB 上限，**不要高频轮询**；训练 Pod 回收后返回 `503`，须容错。

### 2.6 阶段一：输入 / 输出 / 依赖 / 分支

| 项 | 内容 |
|---|---|
| **输入** | 用户在表单填写的名称/预设/数据集/基模/参数 |
| **输出** | `POST /executions` 请求；`202` 响应（`execution_id`）；持续轮询 |
| **核心动作** | 表单校验（含异步唯一性）→ 构造 payload → 提交 → 轮询 |
| **依赖组件** | 「模型研发」API 客户端；`GET /presets`、`GET /input-versions?state=READY` |
| **成功分支** | 拿到 `execution_id`，进入轮询 |
| **失败分支** | `400` 表单校验；`401` 登录失效；`403` 无权限/套餐无 MODEL/路由未登记/数据范围非 ALL；`409` 幂等冲突；`412`（`INPUT_NOT_READY`/`NO_COMPATIBLE_RELEASE`/`SUBMISSIONS_PAUSED`）；`503` 依赖不可用；`504` 超时（**用原幂等键重试**） |

---

## 3. 阶段二：gover 受理、鉴权与投递（G1–G7）

gover **不是透明反向代理**，而是 BFF：本地解析 + 鉴权 + **持久受理**后再 gRPC 投递。

### 3.1 G1 路由注册

路由契约声明在 proto（`api/protos/admin/service/v1/i_modeldev.proto`），但真实运行路由
由**手写 HTTP 适配器**注册（保留 JSON 数组"省略 vs 显式空数组"语义 + 严格参数白名单）：

- `app/admin/service/internal/server/modeldev_http.go`：`POST /admin/v1/modeldev/executions`（L29）
- `server/modeldev_query_http.go`：ListExecutions / GetExecution / logs / artifacts / download
- `server/modeldev_catalogue_http.go`：ListPresets / ListInputVersions / GetInputVersion
- `server/modeldev_stop_http.go`：`POST .../{id}:stop`

> 生成的 `RegisterModelDevServiceHTTPServer`（`api/gen/go/.../i_modeldev_http.pb.go` L47）
> **存在但从未被调用**。

### 3.2 G2 鉴权（三层 + 一层专属）

1. **认证中间件** `auth.Server`（`pkg/middleware/auth/auth.go`）→ `Principal`（租户/操作人）。
2. **租户闸门** `TenantAccessChecker`（`data/tenant_access_checker.go`，**fail-closed**）：
   租户状态、套餐模块白名单（需含 `MODEL`）、`(path, method)` 必须在 Api 表登记，否则拒绝。
3. **Casbin RBAC** `authz.Server`（resource=PathTemplate，action=HTTP method，domain=tenantId）；
   启动守卫强制 `authz.type == casbin`（`wiring_ent.go` L39-40）。
4. **ModelDev 专属授权** `ModelDevAuthorizationRepo`（`data/modeldev_authorization.go`，
   在 `RepeatableRead + ReadOnly` 事务内）：租户启用 → 用户属该租户 → 角色 `DataScope=ALL`
   （排除模板角色）→ 权限码 **`modeldev:manage_execution`** → API 记录的
   `Path=/admin/v1/modeldev/executions`、`Method=POST`、`Scope=Admin`、`BusinessModule=Model`。

> 服务层组合校验顺序（`service/modeldev_service.go` L60-123）：Principal 非空 → `SubjectUser`
> 且 TenantID≠0 → `AuthorizeCreate` → 租户映射 → Actor → 幂等键校验 → `cpup01.CanonicalIntent`。

### 3.3 G3 双摘要与持久受理

- **`intent_hash`**：对用户明确请求及**字段存在性**计算（省略 vs `[]` 不同）。
- **`execution_spec_hash`**：对**解析后**的固定版本、镜像、输入摘要、参数、资源与目录合同计算。
- 持久化到 `sys_modeldev_acceptances`（表列：`tenant_id/resource_tenant_id/actor/action/
  idempotency_key/operation_id/execution_id/intent_hash/intent_canonical/execution_spec_hash/
  snapshot_canonical/accepted_at/dispatch_state`）。
- 唯一键：`(resource_tenant_id, actor, action, idempotency_key)`；同键同 intent → `replayed=true`，
  同键异 intent → `ErrModelDevIdempotencyConflict`（→ 409）。
- 受理动作常量：`ModelDevCreateAction = "modeldev.execution.create"`。
- 本次受理置 `DispatchState = QUEUED`。

### 3.4 G4 202 响应投影

`service/modeldev_service.go` L187-218：

```go
switch accepted.DispatchState {
case "QUEUED", "DISPATCHING", "UNKNOWN":
    reply.ComputeState, reply.DeliveryState = "ACCEPTED", "PENDING"
    reply.CloseState, reply.ResourceState = "OPEN", "NOT_APPLICABLE"
case "ACKED":  // 已有持久 owner 回执 → 直接从回执投影四轴，不做额外查询
    reply.ComputeState = accepted.OwnerReceipt.ComputeState; ...
}
```

### 3.5 G5 后台投递（dispatch worker）

`app/admin/service/internal/service/modeldev_dispatch_worker.go`：

- 循环：先投递关闭命令，再投递创建命令，空闲 300ms。
- `ClaimDelivery`（**租约 15s**）→ gRPC `AcceptExecution(envelope)` → `Ack` / `Defer`。
- 投递队列状态机：`QUEUED → DISPATCHING → ACKED`（+ `UNKNOWN`）。
- 指数退避：`delay = min(30s, 1s << min(attempt-1, 5))`。
- **失败分类**：`INVALID_COMMAND`/`COMMAND_CONFLICT` 永久（阻断重试）；
  `OWNER_UNAVAILABLE`/`INVALID_ACK` 瞬时（重试）。
- **stop-before-create 门闩**：`stop_intent_generation=0` 或关闭已 `ACKED` 才允许创建投递。
- worker 仅在 mTLS 客户端已配置时装配（`wiring_ent.go` L391-393）。

> **gover 不参与 KFP/Kubernetes/S3 提交** —— 全仓生产代码零 KFP 引用。KFP 提交是
> modeldev（业务 owner）的职责。

### 3.6 G6 视图聚合（查询）

- `ExecutionView`：把 modeldev 的枚举**去前缀**后以扁平字符串透传（`EXECUTION_KIND_` / 
  `COMPUTE_STATE_` / `PIPELINE_STEP_`…）。
- `PresetView`：把 modeldev 目录 **+ gover 本地 `ModelDevReleaseBindingRepo`** 合并，
  因此 `active_release_id` / `new_submissions_enabled` / `binding_generation` 由 **gover** 填充。
- `InputVersionView` / `ArtifactView` 同理去前缀透传。

### 3.7 G7 错误映射

| 场景 | HTTP | reason |
|---|---|---|
| 取消 / 超时 | 499 / 504 | `MODELDEV_CREATE_CANCELED` / `MODELDEV_CREATE_TIMEOUT` |
| 授权拒绝 | 403 | `FORBIDDEN` |
| 幂等冲突 | 409 | `IDEMPOTENCY_CONFLICT` |
| 无绑定 / 暂停 | 412 | `NO_COMPATIBLE_RELEASE` / `SUBMISSIONS_PAUSED` |
| 输入未就绪 | 412 | `INPUT_NOT_READY` |
| 环境未就绪 / 上游不可用 | 503 | `ENVIRONMENT_NOT_READY` / `UPSTREAM_UNAVAILABLE` |
| 绑定代际变化 | 409 | `MODELDEV_BINDING_CHANGED` |

> 出站 gRPC 错误重建时**只放行固定 code/reason 对**，不复制下游 message/metadata/correlationID
> （下游私有文本不跨公网边界）。

### 3.8 阶段二：输入 / 输出 / 依赖 / 分支

| 项 | 内容 |
|---|---|
| **输入** | 前端 HTTP 请求（Bearer token + JSON body） |
| **输出** | `202` + 受理快照四轴；持久记录 `sys_modeldev_acceptances`（QUEUED）；后经 worker gRPC `AcceptExecution` 投递 |
| **核心动作** | 路由 → 三层鉴权 → 双摘要 → 幂等受理 → 202 → 后台投递 |
| **依赖组件** | PostgreSQL（acceptance/binding）、Casbin/权限表、modeldev gRPC 客户端（mTLS） |
| **成功分支** | 受理成功 → 投递 `ACKED` → 四轴从回执投影 |
| **失败分支** | 见 §3.7；投递失败按分类重试/阻断；**超时用原幂等键重试** |

---

## 4. 阶段三：modeldev 受理与冻结（M1–M5）

modeldev **不接收用户 HTTP**，只收 gover 的**已冻结命令** `AcceptExecution`。

### 4.1 M1 AcceptExecution（持久 inbox）

`internal/service/accept_execution.go` L18：

1. 校验调用方是**已认证 Governance 投递**（`governanceDeliveryKey`）。
2. 输入校验：nil / unknown proto 字段 / `AcceptedAt` 合法且**微秒精确非零**。
3. `DecodeIntent` / `DecodeSnapshot`；构造 `biz.Admission`；`CanonicalPayloads()`。
4. **租户/actor 必须等于认证投递域**（不能自报）。
5. 落库 `repository.Accept`（写入 `modeldev_executions` 收件箱）。
6. `operation_id` 唯一，**重复命令不重复创建**。

### 4.2 M2 ResolveAdmission（只读解析，🧩 微调扩展）

`internal/service/admission.go` L25 → `resolver.ResolveManaged`。

**现状（🔁 骨架）** `internal/biz/admission_resolution.go` `Resolve` L115-224：
- Release 兼容校验：**硬编码** `ParameterContractVersion != "ani.cpu.mlp.parameters.v1"` 拒绝（L143）。
- 输入 READY 校验；默认参数 + 显式参数合并；构造 `Snapshot`（**硬编码 `Format: "CSV"`** L199）。
- **argv 生成 `resolveAdmissionArguments` L265-292**：**固定 5 键 map**
  `INPUT_PATH/OUTPUT_PATH/INPUT_SHA256/INPUT_BYTES/LEARNING_RATE`。

**微调目标（🧩 设计 §3.4/§4.2）**：需按 `kind` 泛化：
- 参数契约版本 → `ani.lora.sft.parameters.v1`；
- argv 键集扩展 → `MODEL_PATH`/`ADAPTER_PATH`/`EXPORT_DIR`/`CONFIG_PATH` 等；
- `Format` 判定按 kind（CSV / Alpaca JSON）；
- 参数值/数量硬编码校验（epochs="3"、batch_size="64"、≤3 个）按 kind 分支。

### 4.3 M3 基模解析（Model RPC）🧩

设计 §2.1：受理/准备阶段由 **Model RPC `GetModelVersion`**（tenant + model_version_id）解析基模。
该 RPC 内部强制 `DeploymentInput()`（要求 `status=="ready"` 且 provider/reference/sha256 齐全）。
必要时 `GetModelDownloadURL` 签发**短时效（5 分钟）**下载链接——链接**不进长期快照**，每次实际读取前**重新签发**。

> **双输入通道互不复用**：基模走 Model RPC 通道；数据集走 `VerifyAlpacaJSON` 通道。
> 基模**不经过** `InputImport`/`VerifyCSV`。首版**仅支持未加密基模**。

### 4.4 M4 冻结快照

`internal/biz/execution_runtime.go` `FreezeTrainingPlan` L227-246：
- 校验 workspace 与快照一致；
- `Name = "md-"+lower(ExecutionID)`；
- `RequestSHA256 = sha256(json(plan))`。

微调需在快照中记录 `base_model_version_id` 与解析出的物化路径摘要（链接/凭据不入快照）。

### 4.5 M5 工作区布局（v0.4 D09）

| 目录 | 契约字段 | 微调用途 |
|---|---|---|
| `inputs/` | `InputSubpath` | 数据集 + 基模（`{ws}/inputs/model`） |
| `configs/` | **新增 `ConfigsSubpath`** 🧩 | 对齐 D09 布局 |
| `training/` | `TrainingSubpath` | 训练唯一可写输出（adapter） |
| `export/` | **新增 `ExportSubpath`** 🧩 | 合并模型目录 |
| `reports/` | `ReportsSubpath` | 校验、日志、指标 |
| `publication/` | `PublicationSubpath` | 最终归档暂存 |

> 现状 `WorkspaceContract` 仅 4 子路径（`snapshot.go` L110-118）；新增子路径必须一并纳入
> `invalidWorkspaceContract` 的重叠/根/traversal 校验与 `Canonical()` 的确定性处理。
> KFP 侧统一挂 `/workspace`；配方内的 `model_name_or_path`/`dataset_dir`/`output_dir`/
> `export_dir` 直接写平台绝对路径（**不照抄样例的 `/ani/...`**）。
> `dataset_info.json` 由 prepare 物化到 `{ws}/inputs/data/`（与数据文件**同目录**）。

### 4.6 阶段三：输入 / 输出 / 依赖 / 分支

| 项 | 内容 |
|---|---|
| **输入** | gover 的 `AcceptExecutionRequest`（intent + snapshot + identity + accepted_at） |
| **输出** | 持久执行记录（收件箱）；冻结快照/训练计划；解析后的参数与 argv |
| **核心动作** | 认证投递域 → 解码 → 幂等落库 → 解析 Release/输入/基模 → 冻结计划 |
| **依赖组件** | PostgreSQL、catalogue（Release 目录）、objectstore、**Model RPC（微调新增）** |
| **成功分支** | 落库成功 → 待 KFP 提交 |
| **失败分支** | 未认证投递 → 拒绝；租户/actor 不符 → 拒绝；Release 不兼容/输入非 READY/基模非 ready·加密·不在 allowlist → 受理拒绝或解析失败 |

---

## 5. 阶段四：KFP 提交与受管步骤编排（K1–K3）

### 5.1 K1 提交（唯一发送许可）

`internal/biz/pipeline_submitter.go` `Submit` L40-114：`Reserve` 拿许可（**唯一发送许可来源**）
→ **只此一次网络调用** `creator.CreateRun` → Confirm/NotSent/Uncertain 分别落库。

- `PipelineDispatchPlan.Freeze()`：`PipelineID`/`PipelineVersionID` 取自 snapshot.Release；
  `DisplayName = "md-"+executionID`。
- KFP 载荷参数**仅** `execution_id`、`spec_hash`（`data/kfp/client.go` L114-124）。
- 表 `modeldev_pipeline_dispatches` 状态：`SUBMITTING`/`SUBMISSION_NOT_SENT`/`SUBMISSION_UNCERTAIN`/`SUBMISSION_CONFIRMED`。
- **不自动重发**（未知一律 fail，保留围栏等待对账）；D06：**display_name 不是幂等键，找不到不是没创建**。

### 5.2 K2 微调 Pipeline（🧩 `pipelines/finetune_cpu.py`）

```
workspace-name → CreatePVC → prepare
              → train-wait  （TrainJob 阶段1: llamafactory-cli train）
              → export-wait （TrainJob 阶段2: llamafactory-cli export）
              → verify-wait （TrainJob 阶段3: verify_cpu.py 短推理）
              → collect → publish → register → close
```

- 现状 `cpu_p01.py` 为 6 任务（`workspace_name/CreatePVC/prepare/train-wait/collect/publish/close`），
  **微调新增独立 pipeline，`cpu_p01.py` 零改动**。
- 每步固定 `set_caching_options(False).set_retry(0)`；安全上下文 `run_as_user=10001`。
- **现状 `empty_dir("/tmp")` 仅 16Mi**，微调 publish（~2 GiB tar）不够，需上调。

### 5.3 K3 受管步骤白名单（🔁 骨架需扩展）

`internal/biz/managed_runtime.go` `Configuration` L134-139 **现状硬编码 5 步**：

```go
case "prepare", "train-wait", "collect", "publish", "close":
```

🧩 微调扩展为 8 步（+ `export-wait`/`verify-wait`/`register`），`Publish` 仍按 `close` 步鉴权。
每个回调都**重验 workload 身份与 KFP task 归属**（L43-120）。

### 5.4 组件二进制

`cmd/ani-modeldev-step/` 现状 5 子命令（`prepare/train-wait/collect/publish/close`）+ `workspace-name`；
🧩 微调新增 `export-wait`/`verify-wait`/`register` 组件语义（`export-wait`/`verify-wait` 为
Trainer 阶段**等待步**，组件镜像**不做推理**）。

### 5.5 阶段四：输入 / 输出 / 依赖 / 分支

| 项 | 内容 |
|---|---|
| **输入** | 冻结快照 / 训练计划 / Release（pipeline_version_id） |
| **输出** | KFP Run ID（Confirm）；受管步骤回调序列 |
| **核心动作** | Reserve → CreateRun → 落库提交事实；受管步骤逐回调鉴权 |
| **依赖组件** | KFP API、PostgreSQL、PVC |
| **成功分支** | `SUBMISSION_CONFIRMED`，进入 prepare |
| **失败分支** | `SUBMISSION_NOT_SENT`/`SUBMISSION_UNCERTAIN` → 保留围栏、**不自动重发**、人工/对账处理；步骤回调身份不符 → `ErrManagedStepUnauthorized` |

---

## 6. 阶段五：Trainer 三阶段（训练环境 / 执行 / 监控）

### 6.1 训练环境初始化

`internal/data/trainer/create.go`：
- `CreateTraining` L28-60：先 Get 复用，NotFound 才 Create；K8s Status 400/403 记为 `TrainingCreationRejection`。
- `trainingRequest` L122-178：`runtimeRef`（`trainer.kubeflow.org`）、`managedBy`、
  `trainer.image/command/args` 来自快照、`podTemplateOverrides` 挂 PVC。
- `validTrainingPlan` L226：**`len(s.Release.Runtime.TargetJobs) != 1` 直接失败** → 🧩 微调需扩展为
  三阶段（阶段序号）。
- `safeRuntime` L235-337 安全约束：`failurePolicy.maxRestarts==0`、replicatedJobs==1、
  `restartPolicy=Never`、`automountServiceAccountToken=false`、非 root、drop ALL、
  exit-evidence finalizer。

### 6.2 LLaMA-Factory 配方（🧩 锁定）

`finetune_cpu.yaml`：`stage: sft`、`finetuning_type: lora`、`lora_target: all`、`lora_rank: 8`、
`lora_alpha: 16`、`dataset: ani_cpu_smoke`、`cutoff_len: 128`、`max_steps: 20`、`use_cpu: true`、
`seed/data_seed: 42`；固化 `fp16: false`/`bf16: false`/`flash_attn: disabled`。
路径按 §4.5 映射为 `/workspace/...`。

### 6.3 三阶段职责

| 阶段 | 命令 | 产物 | 等待步 |
|---|---|---|---|
| ① train | `llamafactory-cli train /config/finetune_cpu.yaml` | Adapter（`training/adapter/`） | `train-wait` |
| ② export | `llamafactory-cli export /config/export_cpu.yaml` | 合并 HF 目录（`export/merged/`） | `export-wait` |
| ③ verify | `python /opt/lf/verify_cpu.py ...` | `reports/verify.json`（≤16 token 贪心） | `verify-wait` |

> **verify 与 evaluate 区分**（设计 §2.4）：verify 必做（独立加载合并模型 + 固定短输入一次推理，
> 不评效果）；evaluate（效果评测）首版不做。

### 6.4 训练执行监控

- 受管步骤 `train-wait`：`EnsureTraining` + 轮询 `GetTrainingStatus`（`component/runner.go` L142-160）。
- 训练程序产出 `metrics.jsonl`（`ani.metric.v1`，逐 step `train.loss`）与 `summary.json`。
- 用户侧日志：`GET .../logs`（限 1000 行/64KiB/5s），训练 Pod 回收后返回 503。

### 6.5 阶段五：输入 / 输出 / 依赖 / 分支

| 项 | 内容 |
|---|---|
| **输入** | TrainingInput（execution/spec、prepared_manifest_ref、program_ref/image_digest/command/args、resources、workspace_ref、output_contract/deadline） |
| **输出** | TrainingResult（SUCCEEDED/FAILED、output_kind、storage_state、文件清单+摘要、指标/日志引用） |
| **核心动作** | 幂等创建 TrainJob → 等真实终态 → 采集输出 |
| **依赖组件** | Kubeflow Trainer、锁定 LLaMA-Factory 镜像、PVC、S3（prepare 下载基模/数据） |
| **成功分支** | 三阶段均终态成功 → 进入 collect |
| **失败分支** | 训练失败 → `compute_state=FAILED`，workspace 保留供排查；导出损坏/缺文件 → verify 失败，**不得登记 READY**；创建结果不明 → **不换名重试**，观察原关联 |

---

## 7. 阶段六：产物收集与发布（C1–C2）

### 7.1 C1 collect

- `internal/data/workspace/collector.go` `Collect` L21-64：**严格遍历 `contract.RequiredFiles` 逐项收集**；
  collected `files` 就是 RequiredFiles 的镜像。
- 🔁 `collectFile` L66-101 现状 `Openat(root, name, O_NOFOLLOW|...)` **不支持多级路径**（如 `export/merged/config.json`）
  → 🧩 微调需扩展为**逐级安全打开**（逐级 `Openat` 目录 fd + 每级 `O_NOFOLLOW|O_DIRECTORY`，保持防穿越）。
- 🧩 `capturePipelineFile`（`pipeline_io.go` L283）现状**硬拒 `/`**，需放开多级路径。
- `OutputManifestBytes`（`contract/cpup01/output_manifest.go`）要求
  **`len(files) == len(RequiredFiles)`** 且逐条 path+role+size 精确匹配。

### 7.2 C2 publish

- `UploadOutput`（`pipeline_io.go` L126-195）：逐 file 预取 → `CreateTarBundle` 时
  `bundlePipelineFiles` → 上传各 file + manifest + bundle.tar。
- 🔁 `LogicalKey` 现状硬编码 `cpu-p01:`（L174）→ 🧩 微调按 kind 前缀 `lora-sft:`。
- 🧩 tar 打包调整（设计 §7.4）：
  - tar 成员名 = 该条目相对路径的**基名**（`path.Base`），使 tar 根直接是 `config.json`/`model.safetensors`…（**根不套层**）；
  - **`output-manifest.json` 不进模型 tar**（现状 `bundlePipelineFiles` L365 会写进根，微调需排除）；
  - 入包集合 = `in_tar=true` 子集（CPU 分支 = 全部 files，行为不变）。
- 🧩 **校验侧必须同步** `internal/data/objectstore/output_verifier.go`：微调分支**不把 manifest 加进 `wanted`**，
  且 tar 成员名（基名）与 `RequiredFiles[].RelativePath`（`export/merged/...`）做**反向映射**——
  打包侧与校验侧**必须用同一规则**。
- **双重发布说明**：`in_tar=true` 的合并文件会**既**作为独立对象上传**又**进 tar（与 CPU 现状一致）。

### 7.3 阶段六：输入 / 输出 / 依赖 / 分支

| 项 | 内容 |
|---|---|
| **输入** | 训练终态、输出合同（`RequiredFiles` + `in_tar`）、工作区 |
| **输出** | collected 文件集 + manifest；持久对象（各文件 + tar bundle）；发布记录 |
| **核心动作** | 逐项收集 → 清单校验 → 上传 → 打包 → 远端字节核对 |
| **依赖组件** | 工作区 PVC、S3/objectstore、临时 STS 凭据 |
| **成功分支** | `delivery_state=PUBLISHED` |
| **失败分支** | 路径越界/link/缺失必需输出 → 拒绝；合并目录实际文件与枚举**不一致（缺/多）必须明确失败**，不静默容忍；collect/publish 失败 = "计算成功、交付失败"，人工触发原步骤重试（同 publication 幂等键） |

---

## 8. 阶段七：模型登记 register —— 模型回写仓库（R1–R3）🧩

> 这是微调相对通用训练的**最大新增环节**。现状 modeldev 无 model 依赖、无登记代码。

### 8.1 R1 执行侧归属

- 微调链的 `register` 是**受管步骤**（v0.4 D02/D06/D10/D11），但**真正调用 Model 的是 modeldev 服务侧**
  （`RegisterPublishedModel` 内部 RPC），**KFP 的 register 步骤只是发起方** ——
  不让训练程序/训练 Pod 拿到模型仓库管理凭据（A11）。

### 8.2 R2 存储归属（最大缺口）

`ani-model-service` 的 `CreateModelVersion` 传 `storage_path` 时会在 **model-service 自己的 storage、
按 `p.TenantID` 作用域**校验对象：`ObjectExists`（`service/model.go` L283-289）+ `VerifyChecksum`
（L290-295），通过后 `CreateArtifact`（L296-303）+ `MarkReady`（L304-310），artifact 的 `reference`
即该 `storage_path`，`provider` 固定 `"upload"`。而 modeldev 的发布落在**自己的** publication scope。

二选一：
- **同存储**：直接映射 `objectRef → storage_path`；
- **异存储**：需 `internal/data/model/publisher.go` 做**受控复制**并核验。

### 8.3 R3 登记 RPC（逐条对齐真实契约）

| RPC | 用途 | 真实约束（`ani-model-service`） |
|---|---|---|
| `GetModelVersion` | 解析基模 | `service/model.go` L106 强制 `DeploymentInput()`：`status=="ready"` 且 provider/reference/sha256 齐全，否则 412 `MODEL_NOT_READY` |
| `GetModelDownloadURL` | 实际读取前签发 | L403 **5 分钟** TTL；响应仅填 `download_url/storage_path/expires_at`（`is_encrypted` 等字段未被填充） |
| `CreateModel` | `new_model_name` 路径先建容器 | L116-149；名称受 `ValidateModelName`（小写/数字/`-`/`.`，≤63）约束 |
| `CreateModelVersion` | 建版本并置 READY | L251-319 |

**关键规则**：
- `CreateModelVersionRequest.version` **必填非空**，且**不得"查最大版本+1"**；规则由 execution 推导
  （如 `exec-<execution_id>`）。
- `idempotency_key` 与 `version` 是**两个不同字段**；冲突（PgError 23505）→ **409 CONFLICT**。
- **重放语义**：`status=="ready"` 时直接返回、不再校验/插 artifact —— 重试须用**同一 `storage_path` + 同 key**。
- `format` 只接受 `safetensors|gguf|pytorch`；`checksum_sha256` 必须是**未压缩 tar 整体字节**的摘要。
- `new_model_name` → 先 `CreateModel` 再建版本；`target_model_id` → 直接 `CreateModelVersion`。
- **超时不证明没有执行**：登记超时后须用原 `registration_key`（`md-<execution_id>`）**查证**，不得盲目重建。

### 8.4 阶段七：输入 / 输出 / 依赖 / 分支

| 项 | 内容 |
|---|---|
| **输入** | 发布记录（objectRef / tar sha256 / size）、目标模型（`target_model_id` 或 `new_model_name`）、`registration_key` |
| **输出** | 新的 ModelVersion（READY）；receipt 保存 |
| **核心动作** | 存储归属映射/复制 → CreateModel（如需）→ CreateModelVersion → 对账 GetModelVersion |
| **依赖组件** | **modeldev → model gRPC（mTLS，待登记）**、model-service storage、PostgreSQL |
| **成功分支** | `delivery_state=REGISTERED`，Model 版本 READY |
| **失败分支** | 冲突 → 409（幂等键相同返原记录）；登记失败保留"计算成功、交付失败"真实状态，人工用同 key 重试原登记，**不重复训练** |

---

## 9. 阶段八：关闭与回收（CL）

- 受管 `close` 步骤（`managed_runtime.Close` L299-361 → `ConfirmRuntimeClosed` L356）：
  无活跃写者才 `CLOSED`。
- `execution_closer.go` `Reconcile` L56-209：未派发 → `CloseUndispatched` → `FenceOwnerClose`；
  **DEADLINE 时** `RequestRuntimeClose("DEADLINE")`（L118-125）；`StopManagedRun`；`ConfirmRuntimeClosed`。
- **close 成功后**按既有 `close_worker` 语义回收 PVC；**失败路径保留工作区**（"发布失败不删 PVC"）。
- 停止顺序（D12）：保存 stop_requested/monotonic generation → 禁止新创建 → 停止 KFP 与外部 TrainJob
  → 对账在途创建和历史 Pod → 无实际写者才 CLOSED → 按保留策略另行清理。
- 清理走 `data/cleanup/execution.go`（精确 UID，**不删 Pod/卷/发布**）；审计表 `modeldev_execution_cleanup_audits`。

---

## 10. model-service 能力调用时机总表

| 能力 | RPC | 调用时机 | 输入 | 输出 / 关键字段 | 失败分支 |
|---|---|---|---|---|---|
| **基模解析** | `GetModelVersion` | 受理 / prepare | tenant + `model_version_id`（或 `model_id`+`version`） | Model + Version（含 checksum/size/storage_path/is_encrypted） | 非 ready → 412 `MODEL_NOT_READY`；混用 selector → 400 |
| **基模下载签发** | `GetModelDownloadURL` | **每次实际读取前**（链接不进快照） | tenant + `model_version_id` + requester | `download_url`、`storage_path`、`expires_at`（**5 分钟**） | 未 ready → 412；`storage` 未配置 → 503 |
| **新模型容器** | `CreateModel` | register 的 `new_model_name` 分支 | tenant + name + idempotency_key（source 固定 `upload`） | Model | 名称非法 → 400；重名 → 409 |
| **版本登记** | `CreateModelVersion` | register（合并后 HF tar 回写） | tenant + model_id + version + format + storage_path + checksum_sha256 + size_bytes + 原幂等键 | ModelVersion（READY） | 对象不存在 → 404；校验和不符 → 400；幂等冲突 → 409 |
| **登记对账** | `GetModelVersion` | register 后 | 原 model_id + version | 逐字段比对 | — |

> ⚠️ **训练数据接入说明**：数据集**不走 model-service**。数据集是平台输入版本（S3 不可变对象），
> 经 `InputImport`/`VerifyAlpacaJSON`（微调）接入，由 prepare 物化到 `{ws}/inputs/data/`。
> model-service 只承载**基模（读）**与**成品模型（写）**。
>
> ⚠️ **checkpoint 生成**：训练/导出的 checkpoint（adapter、merged 模型）由 **Trainer 内的
> LLaMA-Factory** 产出，落在工作区；model-service 只在**登记**时接收**最终合并 tar**，
> 不参与中间 checkpoint 生成。

---

## 11. 通信协议与关键数据结构

### 11.1 协议矩阵

| 链路 | 协议 / 端口 | 身份 |
|---|---|---|
| 前端 → gover | HTTPS/REST :公网 | Bearer token（用户 Principal） |
| gover → modeldev | gRPC/mTLS（`ANI_MODELDEV_ADDR` 等） | 客户端 DNS `ani-governance` / 服务端 DNS `ani-modeldev-service`；默认超时 3s |
| modeldev → KFP | HTTPS | 受管 SA |
| modeldev → Trainer | K8s API | 受管 SA |
| 受管步骤 → modeldev | gRPC | TokenReview workload + KFP task 归属 |
| modeldev → model 🧩 | gRPC/mTLS | 客户端 DNS `ani-modeldev`（**待登记**） |
| modeldev → S3 | S3 API | 单执行单步骤临时 STS 凭据 |

### 11.2 关键数据结构

**创建意图（gover → modeldev `UserIntent`，现状）**：
```
name, kind, preset_id, dataset_version_id, image_version_id?,
general_parameters[] (name/type/value，值均为字符串), source_execution_id?
```
🧩 微调新增：`finetuning{learning_rate,max_steps}`、`base_model_version_id`、`target_model_id`/`new_model_name`。

**`AcceptExecutionRequest`**：`identity` + `resource_tenant_id` + `admitted_actor_id` +
`intent_hash` + `snapshot`(ExecutionSnapshot) + `accepted_at` + `intent`。

**`ExecutionSnapshot`**：`SnapshotSchemaVersion="ani.modeldev.execution-spec.v1"`、`Kind`、`PresetID`、
`DatasetVersionID`、`WorkspaceContract`、`RequiredOutput`/`OutputContract`、`ResolvedParameters`。
🧩 微调新增 `base_model_version_id`（以 omitempty/独立子结构承载，保证 CPU 规范字节零变化）。

**`OutputContract`（微调）**：`schema_version="ani.finetune.output.v1"`、`output_kind=LORA_ADAPTER`、
`create_tar_bundle=true`、`required_files[]`（每条含 `role`/`relative_path`/`max_size_bytes`/`in_tar`）、
`max_file_count`（= len，精确计数）、`max_total_bytes`（起测 4 GiB）。

**ModelVersion（model-service）**：`tenant_id/model_id/version/format/status/artifact_provider/
artifact_reference/artifact_sha256/idempotency_key/size_bytes/is_encrypted...`。

---

## 12. 任务状态流转完整路径

### 12.1 四轴状态（相互独立，前端渲染依据）

| 轴 | 取值 | 说明 |
|---|---|---|
| `compute_state` | `ACCEPTED → SUBMITTING → SUBMISSION_CONFIRMED → PREPARING → TRAINING → SUCCEEDED / FAILED`（+ `SUBMISSION_NOT_SENT`/`SUBMISSION_UNCERTAIN`） | 计算进展 |
| `delivery_state` | `PENDING / PUBLISHED`（现状）；🧩 微调扩展 `PUBLISHING / REGISTERING / REGISTERED / FAILED` | 结果交付 |
| `close_state` | `OPEN / CLOSING / CLOSED / NEEDS_REVIEW` | 关闭 |
| `resource_state` | `NOT_APPLICABLE`（CPU 恒为此值） | 无 GPU charge，不表示无需清理 |

🧩 v0.4 D12 的微调 `compute_state` 额外含 `EXPORTING` / `VERIFYING` / `CANCELED` / `QUEUED`。

### 12.2 状态投影

`internal/biz/execution_state.go`：
- `Project()` L56-84：admission + submission。
- `ProjectRuntimeStates()` L89-151：叠加 runtime —— Workspace→`PREPARING`；Training→`TRAINING`；
  Rejection→`FAILED`；Observation RUNNING/SUCCEEDED/FAILED；Publication→`PUBLISHED`；
  CloseGeneration→`CLOSING`/`NEEDS_REVIEW`；ClosedAt→`CLOSED`。
- 实现于 `internal/data/execution/state.go`（同事务读 submission+runtime，OwnerRevision 须一致）。

### 12.3 判定建议（前端）

- "训练成功" → `compute_state=SUCCEEDED`
- "可下载" → `delivery_state=PUBLISHED`
- "已彻底停止" → `close_state=CLOSED`
- `NEEDS_REVIEW` → 显示"需人工处理"，**不显示成功或失败**
- `SUBMISSION_NOT_SENT`/`SUBMISSION_UNCERTAIN` → 中间态，**不要当失败**

---

## 13. 异常处理节点总表

| 失败场景 | 阶段 | 状态 | 可重试 | 幂等键 / 动作 |
|---|---|---|---|---|
| 基模非 ready/加密/不在 allowlist | 受理/prepare | 受理拒绝或 prepare 失败 | 修正后重开 | 无 |
| 下载 URL 过期 | prepare | 组件失败 | 自动（组件内重签） | 每次实际读取前签发 |
| 训练失败（train-wait 非零） | train-wait | `FAILED` | 用户重开；workspace 保留 | — |
| 导出损坏/缺文件 | export-wait → verify-wait | verify 失败，**不得登记 READY** | 用户重开 | — |
| verify 不通过 | verify-wait | 失败，不登记 READY | 用户重开 | — |
| collect/publish 失败 | collect/publish | "计算成功、交付失败" | 人工触发原步骤重试 | 同 publication 幂等键 |
| register 冲突 | register | 交付失败 | 人工重试原登记 | 同 `registration_key`（`md-<execution_id>`）；fingerprint 相同返原记录、不同 409；**不重复训练** |
| 执行超时 | 任意 | `FAILED` | 先查证再重试 | **超时不证明没有执行**；登记前用原 key 查证 |
| CreateRun/TrainJob 结果不明 | 提交/训练 | `SUBMISSION_UNCERTAIN` | **不重复创建** | 观察原关联，只对账 |
| PVC 清理 | close | — | — | close 成功后回收；失败路径保留工作区 |
| 服务重启 | 任意 | — | — | 扫描未结执行/登记/关闭意图，不去数据库重置状态 |

---

## 14. 附录：关键文件索引

**ani-governance（BFF）**
- 路由：`app/admin/service/internal/server/modeldev_http.go`、`modeldev_query_http.go`、`modeldev_catalogue_http.go`、`modeldev_stop_http.go`
- 鉴权：`data/modeldev_authorization.go`（权限码 `modeldev:manage_execution`）、`data/tenant_access_checker.go`、`pkg/middleware/auth/auth.go`
- 受理/幂等：`data/modeldev_acceptance_repo.go`、`ent/schema/modeldev_acceptance.go`
- 投递：`service/modeldev_dispatch_worker.go`、`data/modeldev_dispatch_repo.go`、`data/modeldev_dispatch_client.go`
- 客户端/装配：`data/modeldev_client.go`、`data/modeldev_client_config.go`、`app/admin/service/cmd/server/wiring_ent.go`
- 前端合同：`ani-governance/docs/frontend-cpu-training-handoff.md`

**ani-modeldev-service**
- 传输：`internal/service/accept_execution.go`、`admission.go`、`command.go`
- 领域：`internal/biz/execution.go`、`execution_runtime.go`、`admission_resolution.go`、`pipeline_*.go`、`managed_runtime.go`、`managed_steps.go`、`execution_closer.go`、`execution_state.go`、`storage_credentials.go`
- 数据：`internal/data/{execution,lifecycle,submission,input,catalogue,objectstore,workspace,kfp,trainer,runtimeproof,storagecredentials}/`
- 步骤二进制：`cmd/ani-modeldev-step/`（`main.go`、`bootstrap.go`、`workspace_name.go`）、`internal/component/runner.go`
- 编排：`pipelines/cpu_p01.py`、`training/train_mlp.py`
- 契约：`contract/cpup01/{intent,snapshot,release,release_parts,output_manifest,admission,canonical,protobuf/codec}.go`
- 迁移：`migrations/0001`–`0017`
- 设计：`docs/design/finetuning-cpu.md`、`finetuning-cpu-review.md`

**ani-model-service（模型仓库）**
- RPC：`api/model/v1/model.proto`
- 实现：`internal/service/model.go`（GetModelVersion L77、CreateModel L116、CreateModelVersion L251、resolveModel L355、GetModelDownloadURL L384）
- 领域：`internal/biz/model/model.go`、`ports.go`
- 身份：`internal/identity/context.go`、`internal/server/{identity,observability}.go`、`cmd/ani-model-service/rpc_policy.go`、`grpc_tls.go`
- 存储：`internal/data/storage/{minio,grpc}.go`
- 迁移/查询：`migrations/000001`–`000006`、`queries/model.sql`

**基线文档**
- v0.4：`kubeflow文档/模型研发平台-v0.4/source/{architecture,development}.md`（D01–D18）
- 服务间调用规则：`service-to-service-calls.md`
- 样例配方：`kubeflow文档/模型研发平台-v0.4/cpu-baseline/{finetune_cpu.yaml,export_cpu.yaml,dataset_info.json,data.csv}`

---

## 15. 一句话结论

**微调链路 = 通用训练（CPU-P01）已验证的"受理→冻结→KFP→受管步骤→Trainer→收集→发布→关闭"骨架 + 微调专属的四项增量**：
① 基模从模型仓库**读入**（`base_model_version_id` + Model RPC）；
② Trainer **三阶段**（train/export/verify）；
③ 合并 HF 模型**未压缩 tar 回写**（含 `in_tar` 与校验侧映射）；
④ **register 登记**为新 ModelVersion（modeldev→model 新调用链）。

其中 ①②③④ **全部待实施**，且第 ④ 项还卡在"model-service 授权策略未放行 modeldev""无目录上传能力"
两个跨仓硬缺口上。**把这份流程当实施路线图用可以，当现状说明用会翻车。**

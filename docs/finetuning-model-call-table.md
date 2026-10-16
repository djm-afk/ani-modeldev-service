# 微调 Model 接入：本次实际调用表（P0 前置合同）

编制日期：2026-10-16
编制人：接手验证/开发方
对应任务：`finetuning-next-handoff.md` §3/§6 的 P0（Model 与 MR01–MR08 最小真实登记链）
依据：唯一 spec A04 / A06 / A09 / D08 / D09.3 / D11.6、两侧当前源码、**目标集群实际部署状态**、Gov《服务间调用规则》

> **本文性质**：合同固定稿 + 缺口清单。静态值来自本机源码；**部署值来自目标集群只读读回**。
> 无法确证的一律标 `TBD-待确认`，**不猜填**。本文不构成任何"已通过"结论。

---

## 0. 一句话结论

**P0 的真实阻塞是"ModelDev → Model 这条调用根本不存在"，而不是"Model 未部署"或"ModelDev 缺代码"。**

前置事实（集群读回）：Model **已部署**（`ani-system/ani-model-service`，1/1，helm v5，gRPC `:19090`）、部署版本 **`a21e0a7`**、与 ModelDev **共用同一个 RustFS**（仅 bucket 不同）。

**当前确认的四个真实缺口**（详见 §4）：

1. **Model 侧未登记 ModelDev 身份**——部署版本 `a21e0a7` 的策略里**依然只有 Governance / Inference 两个身份**（`git grep modeldev a21e0a7 -- cmd/ internal/` 无匹配），ModelDev 的 DNS SAN 不在其中 → 必被 `PermissionDenied`。
2. **ModelDev 侧根本没有配置 Model 端点**——部署的 `config.yaml` 只有 grpc/db/kfp/rustfs/dispatch，**没有任何 model service endpoint**；即调用方代码与配置都还没有。
3. **产物 format 与 Model 枚举不匹配**（`MERGED_HF_MODEL` vs `safetensors|gguf|pytorch`）——部署版本未改，仍成立。
4. **ModelDev 的登记派发仲裁（D09.3）未实现**——[RegisterModel](../internal/biz/finetuning_registration.go#L31-L51) 仍只返回 `ErrModelDependencyUnavailable`。

缺口 1 只能由 **Model 提供方**解决；缺口 3 需 **Model 定映射**；缺口 2/4 是**本地可写**的部分（但无缺口 1 就无法真实验收）。

---

## 1. 双方基线锁定（v2）

| 项 | 值 | 来源 |
| --- | --- | --- |
| ModelDev 仓库 | `zhangzhe-ctrl/ani-modeldev-service`，`origin/main` @ `ee46246` | 本机 git |
| ModelDev 已核 driver 源码 | `1ca8f03`（M06/M07）、`f7b852e`（A/B/M03）、`9c8ab03`（M04）；均为 `ee46246` 祖先 | 集群注解 + `git merge-base` |
| **ModelDev 部署镜像** | `localhost/ani-modeldev-service@sha256:ba17dc368a34d79baae6a8321757e774459ab3f20d125e87747a74d220e76769`（`ani-system/ani-modeldev`，1/1） | 集群 |
| Model 仓库 | `ani-model-service`，本地 `master` @ `4b9b3b5` | 本机 git |
| **Model 部署版本** | **`a21e0a7`**（`fix(auth): align Governance caller with exact DNS identity`），**是 `4b9b3b5` 的后继**（`4b9b3b5...a21e0a7` = `0 1`） | 集群 image tag + `git merge-base` |
| Model 远端分支 | `origin/codex/align-governance-dns-20261014` @ `437822a`（= `a21e0a7` + `test: remove opt-in cross-process environment harness`） | 本机 fetch |
| **Model 部署镜像** | `localhost/ani-model:a21e0a7d3982-20261014-identity@sha256:621d1489c4db7496b22f498a0e59fa1c101d80e8b034ca3dd732809ff63f6fd8` | 集群 |
| Model 交接快照源 | `71c10de`（spec S4 固定）——是 `4b9b3b5` 的祖先 | `git merge-base` |
| Model go module | `github.com/liangzai006/ani-model-service` | proto `go_package` |
| ModelDev go module | `github.com/zhangzhe-ctrl/ani-modeldev-service` | proto `go_package` |

⚠️ **关键点**：本地 `master@4b9b3b5` **不是**当前运行版本。实际运行的是 `a21e0a7`（其后继为 `437822a`）。本文所有 Model 侧结论已按 `a21e0a7` 复核；该提交只动鉴权相关文件，未改幂等与 format。

> 🔗 **链接约定**（为让本文在他人机器上也能打开）：
> - **本仓文件**（`ani-modeldev-service`）用**相对路径**，如在任意位置 checkout 均可打开。
> - **跨仓文件**（`ani-model-service` / `ani-governance`）用**钉到 commit 的 GitHub 链接**，不依赖本地 checkout。
>   - Model 侧统一钉到部署版 **`a21e0a7`**（`zhangzhe-ctrl/ani-model-service`）。该 commit 除 `cmd/ani-model-service/rpc_policy.go` 外，其余被引文件与本地 `master@4b9b3b5` **逐字节相同**，故行号一致；`rpc_policy.go` 因治理身份改动有 +2 行偏移，本文已按 `a21e0a7` 的行号标注。
>   - Gov 合同钉到 spec S2 固定 commit `34388cf`。

---

## 2. 本次需要的调用（按 spec MR 编号）

方向统一 **ModelDev → Model**，走 Model 的内部业务 mTLS listener（`:19090` → `ani-model-grpc`）。

**前置项 C0：Model API 模块版本与消费方式**（Gov §5 硬规则）

Gov《服务间调用规则》§5 明文：*"协议只消费接收方公布的固定版本 API 模块，不复制 Proto/生成代码，不用本地 `replace` 冒充正式互操作"*（[service-to-service-calls.md:239](https://github.com/zhangzhe-ctrl/ani-governance/blob/34388cf90d0834da76c9e5701ade5d92a248144f/docs/contracts/service-to-service-calls.md#L239)）。而 ModelDev 当前 go.mod **无 `ani-model-service` 依赖**、全仓无 `model.v1` 生成代码。**若 Model 侧只发布 API 而未发布可消费的模块版本，ModelDev 仍接不上，且会违反该规则。** 故下表所有 C1–C7 均以此为前置。

| # | 用途 | 目标 RPC | 时机 | 当前可用性 |
| --- | --- | --- | --- | --- |
| C1 | 基模精确版本只读解析（MR02） | `/model.v1.ModelService/GetModelVersion` | 受理前 | ❌ 无调用方身份 |
| C2 | 目标解析（existing） | `/model.v1.ModelService/GetModel` | 受理前/后 | ❌ 同上 |
| C3 | 新建容器（new 分支，MR03） | `/model.v1.ModelService/CreateModel` | 受理后 | ❌ 同上 |
| C4 | 版本登记（MR04） | `/model.v1.ModelService/CreateModelVersion` | 受理后 | ❌ 同上 |
| C5 | 原结果查证（MR05） | `/model.v1.ModelService/GetModelVersion` + `/model.v1.ModelService/ListModelVersions` | 受理后重放 | ❌ 同上 |
| C6 | 固定对象下载（MR06） | `/model.v1.ModelService/GetModelDownloadURL` | 登记后校验 | ❌ 同上 |
| C7 | 受控复制（MR07，二选一） | `/model.v1.ModelService/GetUploadURL` | 受理后 | ❌ 同上 |

> C7 是**二选一**（spec D08.4）：首个环境只交付它实际选择的模式，不允许运行时切换。
> 注意：C1/C2/C5/C6 **在 Model 侧对被允许的调用方已存在**（Inference 已有 GetModel/GetModelVersion/GetModelDownloadURL；C5 的 pending 观测能力也已在 `ListModelVersions` 上具备），**不是能力缺口，是身份/放行缺口**。其中 `ListModelVersions` 目前只给 Governance，需随 Q2 一并申请放行。
>
> **源码已定**：C7 不是"可选"——`CreateModelVersion` 校验对象走 `bucketForTenant`，在 `tenantBuckets=false` 时**恒为配置的单一 bucket**（`ani-models`），故 `storage_path` **无法表达跨 bucket 引用**，(A) 直接引用**在代码上不成立**，只能是 (B) 受控复制。详见 §4.6。

---

## 3. 逐条字段（本次实际调用表）

### C1 · 基模只读解析（MR02）

| 字段 | 值 | 状态 |
| --- | --- | --- |
| 调用方身份 | **不存在**——需新签发 DNS SAN 并登记进 Model 策略 | ❌ 待 Model 提供方 |
| **接收方身份（已确认）** | `ani-model.ani.internal`——集群 `ani-model-mtls` 证书实际 SAN 为 `DNS:ani-model.ani.internal`（critical） | ✅ **已证** |
| 接收方端点 | `ani-model-grpc`（ClusterIP `10.96.44.130`）`:19090` | ✅ 已证 |
| RPC | `/model.v1.ModelService/GetModelVersion` | ✅ 存在 |
| 请求选择器 | `model_version_id`（优先）或 `model_id`+`version`（互斥） | ✅ |
| tenant 来源 | 见下方注：**具体 metadata 合同取决于 Q3 选定的 Principal 分支**——若沿用 governance 风格，则只认 `x-ani-tenant-id`，且**强制** `x-ani-actor` 带固定前缀；inference 风格才允许 `x-tenant-id` 别名且无 `x-ani-actor` 要求 | ⚠️ 待 Q3 |
| 返回的固定事实 | `format`/`size_bytes`/`checksum_sha256`/`storage_path`；`DeploymentInput()` 要求 `status=ready` 且 provider/reference/SHA 齐全，否则 `412` | ✅ |
| 幂等键 | 只读，无 | — |
| 超时/预算 | `TBD`（见 §5 T5） | 待定 |
| 查证语义 | 只读，**不占名**（spec A06：预检仅提示/拒绝） | ✅ |

> **C1 metadata 合同注（B4）**：Model 现有两条 Principal 分支的 metadata 要求**不同**——inference 分支（[rpc_policy.go:172-191](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/cmd/ani-model-service/rpc_policy.go#L172-L191)）允许 `x-tenant-id` 别名、无 actor 要求、actor 固定为 `inference:service`；governance 分支（[rpc_policy.go:193-219](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/cmd/ani-model-service/rpc_policy.go#L193-L219)）只认 `x-ani-tenant-id`、**强制** `x-ani-actor` 以 `governance:` 开头、`Workload` 为固定身份。ModelDev 走的是**新增的第三条分支**（§4.1 / Q3），其 metadata 合同由 Model 决定，尚未固定。此外 `Principal.Valid()` 要求 `TenantID`/`Actor`/`Workload` 三者非空。

### C2 · 目标解析（existing 分支）

| 字段 | 值 | 状态 |
| --- | --- | --- |
| RPC | `/model.v1.ModelService/GetModel` | ✅ |
| 选择器 | `model_id`：**UUID 走内部主键；非 UUID 走 external id** | ✅ |
| 关键约束 | spec A06.1：微调请求的 `target_model_id` **用 Model 返回的内部模型 UUID** | ⚠️ 需固定用哪个 |
| 归属/可写检查 | Model 只校验 tenant 归属；**未见"目标可写"显式位**（`capabilities` 是 chat/embedding 等模型能力，非写权限） | **缺口，见 §4.4** |

### C3 · 新建容器（new 分支，MR03）

| 字段 | 值 | 状态 |
| --- | --- | --- |
| RPC | `/model.v1.ModelService/CreateModel` | ✅ |
| 幂等键 | `idempotency_key` + `request_fingerprint`；同 key 同内容返回原记录，异内容 `409` | ✅ **已具备**（migration [000004](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/migrations/000004_model_version_idempotency.sql)） |
| 业务键 | spec D08.3：`md-container:<execution_id>` | ✅ 设计已定 |
| `model_id` 传参语义 | 为空**默认取 `name`**——若直接传 `md-container:<exec_id>` 会写进 external id 列 | ⚠️ 待确认承载方式 |
| 名称唯一性 | ≤63、仅 `a-z0-9-.`；DB `UNIQUE(tenant_id,name)` | ✅ |
| 预算/仲裁 | 需与 Stop/deadline 共同仲裁（D09.3.1）；**ModelDev 无该实现** | **缺口，见 §4.5** |

### C4 · 版本登记（MR04）

| 字段 | 值 | 状态 |
| --- | --- | --- |
| RPC | `/model.v1.ModelService/CreateModelVersion` | ✅ |
| 业务键 | spec D08.3：`md-<execution_id>`；版本标签 `exec-<execution_id>` | ✅ |
| 幂等 | `idempotency_key` + `request_fingerprint`，重放同 key 同内容返回原版本 | ✅ **已具备** |
| 请求 `version` | = `exec-<execution_id>` | 待实现方填入 |
| 请求 `format` | **仅 `safetensors`/`gguf`/`pytorch`**（DB CHECK + 业务校验，`a21e0a7` 未改） | ⚠️ **与产物不符，见 §4.2** |
| `storage_path` 语义 | 若传：Model 走**自己的** storage adapter → `ObjectExists` → `VerifyChecksum` → 建 Artifact → `ready`；失败分别 `STORAGE_OBJECT_NOT_FOUND`/`CHECKSUM_MISMATCH` | ⚠️ **对象须在 Model 的 bucket scope 内，见 §4.6** |
| 已 `ready` 重放 | 直接返回原版本，不再插 Artifact | ✅ 已处理 |
| `ready` 判定 | SQL 要求存在 `model_artifacts` 且 `sha256 = checksum_sha256` | ✅ 有实际约束 |

### C5 · 原结果查证（MR05）

| 字段 | 值 | 状态 |
| --- | --- | --- |
| RPC | `GetModelVersion` / `ListModelVersions` | ✅ |
| 按 `(model_id, version)` 点查 | `GetVersionByExternalRef` → `GetReadyModelVersionByExternalRef`（[version_store.go:44-60](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/internal/data/postgres/version_store.go#L44-L60)） | ⚠️ **SQL 带 `v.status='ready'`，只返回 ready** |
| 按 `model_id` 列表 | `ListModelVersions` → SQL 只过滤 `status <> 'deleted'`（[model.sql:148-162](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/queries/model.sql#L148-L162)） | ✅ **能看到 pending**（见 §4.3） |
| 按幂等键点查 | data 层 `GetModelVersionByIdempotency`（[model.sql:132-133](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/queries/model.sql#L132-L133)）**不带 ready 过滤**，但**未暴露为 RPC** | ⚠️ 能力在 data 层、缺对外接口 |
| 结论 | **能区分"READY / pending / 未创建"**：点查答 READY/无，列表答 pending | ✅ 见 §4.3 |
| 404 语义 | spec：**404 不能单独证明"原写未发生"** | ⚠️ 实现须遵守 |

### C6 · 固定对象下载（MR06）

| 字段 | 值 | 状态 |
| --- | --- | --- |
| RPC | `/model.v1.ModelService/GetModelDownloadURL` | ✅ |
| 前置 | 版本须 `DeploymentInput()` 通过，否则 `412` | ✅ |
| 返回 | `download_url`（5 分钟 TTL）+ `storage_path` | ✅ |
| 约束 | 不得把 versioned object 降为 latest；须核 tar SHA/size 与原发布一致 | ⚠️ 见 §4.6 |

### C7 · 受控复制（MR07，仅当 C6 不可行）

| 字段 | 值 | 状态 |
| --- | --- | --- |
| RPC | `/model.v1.ModelService/GetUploadURL` | ✅ |
| 约束 | `file_name` 无 `/` `\`；`size_bytes > 0`；`checksum_sha256` 64 位 hex | ✅ |
| 路径 | `{tenant}/{model_id}/{version}/{file_name}` | ✅ |
| 语义 | upload 票据只传字节，不生成新版本（spec D08.4） | 设计已定 |

---

## 4. 缺口与必须澄清项

### 4.1 【阻塞】Model 未登记 ModelDev 的调用身份（MR01）

- 部署版本 `a21e0a7` 的策略仍只有两个身份：`ani-governance`、`ani-inference.ani.internal`（[rpc_policy.go:101-133](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/cmd/ani-model-service/rpc_policy.go#L101-L133)）。
- `git grep modeldev a21e0a7 -- cmd/ internal/` **无任何匹配** → ModelDev 身份未登记。
- `ValidatedPolicy` 对**未知身份直接 `false`**；即使登记进 map，`expectedIdentities` 白名单外仍拒。
- `ModelPrincipalMiddleware`：**非 Governance / 非 Inference 的身份不注入 Principal**（[rpc_policy.go:153-162](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/cmd/ani-model-service/rpc_policy.go#L153-L162)）→ 业务层 `RequireTenant` 必返回 `401`。
- **需 Model 提供方**：① 签发并登记 ModelDev 的 DNS SAN；② 登记允许的 RPC 方法清单；③ **新增一条 Principal 构造分支**（见下）。
- **禁止**冒用 `ani-governance`（spec A04/D08.1）。

> 源码细节（`a21e0a7`）：
> - `ModelServicePolicy` 是**硬编码 map**，且外层 `ValidatedPolicy.expectedIdentities` 是**独立白名单**——**只加 map 不加白名单仍拒**，两处都要加。
> - `ModelPrincipalMiddleware` 仅三个分支：governance → `governancePrincipal`；inference → `inferencePrincipal`；**其它 → 直接 `next(ctx, req)` 不注入 Principal** → `RequireTenant`/`FromContext` 必 `ErrMissingPrincipal`（[context.go:35-52](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/internal/identity/context.go#L35-L52)）。
> - `Principal.Valid()` 要求 `TenantID`、`Actor`、`Workload` **三者都非空**——所以不能只塞 tenant。

> 附带事实：`a21e0a7` 把常量从 `ani-governance.ani.internal` 改成 `ani-governance`（[rpc_policy.go:19](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/cmd/ani-model-service/rpc_policy.go#L19)），与集群 `governance-modeldev-client-tls` 证书实际 SAN（`DNS:ani-governance`）对齐——说明该环境**采用"证书 SAN = 策略身份"的精确匹配**。ModelDev 必须走同一约定。

### 4.2 【阻塞】产物 format 与 Model 枚举不一致

- 微调输出契约 `OutputKind = MERGED_HF_MODEL`，对外是 **HF 目录 + bundle tar**。
- Model 三处都只认 `safetensors|gguf|pytorch`：DB CHECK（[000001:25](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/migrations/000001_model_control_plane.sql#L25)）、`Version.Validate`、`Artifact.Validate`（[model.go:41-71](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/internal/biz/model/model.go#L41-L71)）。**没有第四个可能值**，`a21e0a7` 未改。
- 需固定映射：登记为 `safetensors` 版本而 `storage_path` 指向 tar/目录，还是由 Model 扩枚举。**必须由 Model 明确。**

### 4.3 【已复核】幂等与查证口径

- **写入侧幂等已具备**：`CreateVersion` 先按 `idempotency_key` 查（[version_store.go:73-82](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/internal/data/postgres/version_store.go#L73-L82)），同 key 同 `request_fingerprint` 返回原记录、异内容报 `23505` 冲突。`CreateModel` 同理。migration [000004](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/migrations/000004_model_version_idempotency.sql) 提供唯一索引。部署版本 `a21e0a7` 未改此逻辑。
- **读取侧分三种，能力不同**：
  - `GetModelVersion`（含 `(external_model_id, version)` 变体）→ SQL 带 `v.status='ready'`，**只返回 ready**（[model.sql:29-42](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/queries/model.sql#L29-L42)）。
  - `GetModelVersionByIdempotency` → **不带状态过滤**、能返回 pending，但**没有对外 RPC**，只在写入路径内部用（[model.sql:132-133](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/queries/model.sql#L132-L133)）。
  - `ListModelVersions` → **已注册的对外 RPC**，按 `model_id` 列版本时**只过滤 `status <> 'deleted'`、不过滤 ready**（[model.sql:148-162](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/queries/model.sql#L148-L162)），返回体含 `status` 字段（[model.go:426-432](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/internal/service/model.go#L426-L432)）→ **pending 能看到**。该 RPC 目前只给 Governance。
- **净结论**：MR05 的"区分 未创建 / pending / READY"——**按业务键点查**（`GetModelVersion`）只答 READY/没有；**按 `model_id` 列表**（`ListModelVersions`）能答出 pending（微调版本号固定为 `exec-<execution_id>`，可反查）。即能力不缺，缺的是把 `ListModelVersions` 放行给 ModelDev；若 Model 认为列表接口不便给，才需补一个按业务键/idempotency_key 的点查。→ 见 §5 T12。

### 4.4 【缺口】目标"是否可写"检查未见

- spec MR03 要求 Model 提供"目标归属/**可写**检查"。
- 源码核对：`GetModel`（[model.go:152-165](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/internal/service/model.go#L152-L165)）只做 `RequireTenant`（租户归属）+ `status <> 'deleted'`；`capabilities` 是模型能力标签（chat/embedding 等），**非写权限**。
- `CreateModelVersion` 建版本时也只 `FOR KEY SHARE` 锁父模型**存在性**（[model.sql:88-101](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/queries/model.sql#L88-L101)），**没有任何"该目标允许追加版本"的判定**。
- 结论：**"可写检查"当前完全不存在**（不是"没看到"，是没有）。需 Model 确认是复用现有字段还是补受限能力。

### 4.5 【缺口·本地可写】ModelDev 侧登记派发仲裁（D09.3）未实现

- [RegisterModel](../internal/biz/finetuning_registration.go#L31-L51) 仍只 `RecordRegistrationUnavailable` + 返回 `ErrModelDependencyUnavailable`。
- [FinetuningModelFactsReader](../internal/biz/finetuning_admission.go#L14-L20) 生产装配未接线（`NewManagedAdmissionResolver` 不含 models → `resolver.models == nil` → `ErrFinetuningNotEnabled`）。
- **新证据（部署读回）**：`ani-modeldev` 的 `config.yaml` **完全没有 model service endpoint**——连配置位都没留。可见调用方与配置都尚未开始。
- 需新增：与 Stop/deadline **共用执行行锁**的派发资格记录、事务外才做 RPC/I/O、区分"意图已存/已获准派发/结果未明"。
- **本地可写、可单测，但不能标通过**（无 §4.1）。

### 4.6 【源码已定】存储交接只能走受控复制（C7）

- Model 与 ModelDev 指向**同一个 RustFS**（`ani-rustfs-svc.ani-platform.svc.cluster.local:9000`），但 bucket 不同：
  - Model：bucket `ani-models`（`ANI_MINIO_TENANT_BUCKETS=false`，`secure=true`）
  - ModelDev：bucket `ani-ft-core-output-*`（fixed VersionID，`secure=true`）
- **代码已定死 (A) 不可行**：`CreateModelVersion` 校验对象走 `s.ObjectExists(ctx, tenant, storage_path)` → [`bucketForTenant`](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/internal/data/storage/minio.go#L229-L237) 在 `tenantBuckets=false` 时**恒返回 `ani-models`**。`storage_path` 只能表达"`ani-models` 里的 key"，**无法表达别的 bucket**。
- 因此只能 **C7 受控复制**：ModelDev 读原对象 → 经 [`GetUploadURL`](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/internal/service/model.go#L463-L483) 上传到 `ani-models` → `CreateModelVersion(storage_path=...)` 登记。落点路径由 Model 固定为 `{tenant_id}/{model_id}/{version}/{file_name}`。
- 上传票据约束：`file_name` 无 `/` `\`、`size_bytes > 0`、`checksum_sha256` 为 64 位 hex。
- **须冻结**（不得运行时切换）；仅需 Model 确认 bucket/配额约束。→ §5 T8。

---

## 5. 待确认清单

| # | 待确认项 | 状态 | 应由谁定 |
| --- | --- | --- | --- |
| T1 | ModelDev 的客户端 DNS SAN | **仍需** | Model 登记 + 部署签发 |
| T2 | 该身份被允许的 RPC 方法清单（本次申请 7 条，含 `ListModelVersions`） | **仍需**（当前策略无 modeldev） | Model |
| T3 | ModelDev 调用时 tenant/actor 传递方式 | **仍需**（取决于 Q3 选定的 Principal 分支；若沿用 governance 风格则只认 `x-ani-tenant-id` 且强制 `x-ani-actor` 前缀，见 §3 C1 注） | Model |
| T5 | 单次 RPC 超时与重试预算 | **仍需** | 双方 + 环境 owner |
| T6 | `MERGED_HF_MODEL` → Model `format` 映射 | **仍需**（`a21e0a7` 未改） | Model |
| T7 | 目标"可写"检查承载方式 | **仍需**（源码确认当前完全不存在，见 §4.4） | Model |
| T8 | 存储交接模式（源码已定只能 C7，见 §4.6） | **仅需确认** bucket/配额约束 | 双方 + 环境 owner |
| T10 | Model 的 tenant UUID 与 Gov 租户映射 | **仍需** | Gov + Model owner |
| T11 | ModelDev 为何未配置 model endpoint | **仍需**（是未开始，还是另有接缝？） | ModelDev 负责人 |
| T12 | MR05 查证：是否把已有 `ListModelVersions` 放行给 ModelDev | **仍需**（点查只答 READY/无；列表已能答 pending，见 §4.3） | Model |
| T13 | **Model API 模块版本与 proto 消费方式**（见 §2 前置项 C0） | **仍需**（Gov §5 要求消费已公布模块、不复制 Proto、不用本地 replace；ModelDev go.mod 当前无该依赖） | Model 发布 + ModelDev 消费 |

> 编号说明：**T4、T9 为保留号**（历史断层，无对应待确认项）。
> 已确认项（不再列为待定）：Model 服务器 DNS SAN = `ani-model.ani.internal`；部署版本 = `a21e0a7`（对幂等/format 无影响）。

---

## 5.1 与 spec 派生稿 S4 的 Model 基线偏差说明

spec 派生稿把 Model 源码固定在 commit `6473f1e`（[model-integration-requirements.md:239-243](design/ani-finetuning-v4.2.2/model-integration-requirements.md#L239-L243)，来源小节 S4），而本调用表用 `master@4b9b3b5` + 部署版 `a21e0a7`。**偏离是刻意的**：[finetuning-next-handoff.md §4](finetuning-next-handoff.md) 要求"核其当前正式版本和真实部署，不沿用旧文档的'尚缺 API'快照作当前结论"。`6473f1e` 是旧快照，本次按当前运行版复核。

---

## 6. 建议的推进顺序

1. **把 §3 + §5 发给 Model owner**，回填 T1/T2/T3/T5/T6/T7/T10/T12/T13、确认 T8 倾向 C7。这是**当前唯一不依赖 Fedora/容量**的推进动作。
2. Model 回填后，ModelDev 侧写：
   - `FinetuningModelFactsReader` 真实实现（C1/C2）+ 配置位（T11）；
   - 登记派发仲裁 D09.3 + `CreateModel`/`CreateModelVersion`/`GetModelVersion`/`ListModelVersions` 客户端（C3/C4/C5）；
   - 单测覆盖"同键同内容重放 / 同键异内容拒绝 / Stop 先于派发 / 派发先于 Stop"。
3. **真实联调（M1-B）** 需同时满足：Fedora 构建、集群容量（当前 **PVC 19/19 满**）、Model 侧身份签发。

---

## 7. 声明

- 本文静态结论来自**本机源码阅读**；部署结论来自**目标集群只读读回**（`kubectl get` / 证书 SAN 解析）。
- **未部署任何东西、未发起任何 mTLS 调用、未运行任何测试**。Model 侧调用**从未实际尝试**。
- 未对 FT/MR/PA 任一项做 PASS/BLOCKED 判定。
- 标 `TBD` 的项**未经确认**，不得当作已定合同引用。
- 本文不替代 spec；如与 spec 冲突，以 spec（SHA256 `5323612099bbfe54f2e13b3aea4a5c3c4f9a4cf143a64f7cc526b530f9925ec9`）为准。

> **spec anchor 说明（C2）**：上述 SHA 对应**仓内** `design/ani-finetuning-v4.2.2/spec.md`（本机实测匹配）。交接文档 [finetuning-next-handoff.md §1](finetuning-next-handoff.md) 另引 `.scratch/finetuning-cpu/spec.md`（SHA `60fabe6a1a40b9dc316f407e6b40cea2a1f8381749ac44fe2f14e35b5d03853e`），该文件不在 Git、本机不存在。本文以**仓内 v4.2.2 spec 为唯一 anchor**；`.scratch` 版视为 Fedora 侧同名派生，若二者内容不同须先行裁决后再引用。

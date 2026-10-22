# 微调登记链接入 Model：需要 model-service owner 拍板的几个点

日期：2026-10-16（**本轮实现后更新**：ModelDev 侧 D09.3 仲裁、写入编排、消费基线均已落地，Q2/Q3/Q4/Q6 已按实现收敛为"确认"）  
发起方：ModelDev 微调（P0 / MR01–MR08 最小真实登记链）

---

## 背景

微调闭环卡在最后一步：训练产物已发布，但登记不进 Model。ModelDev 要以**独立服务身份**调 Model 的登记类 RPC。

本文档核对了集群上运行的 Model（`a21e0a7`）与仓库源码，**部分结论源码已可定死，不需要 model-service owner 回答**；以下分为"本文档已定"与"需要 model-service owner 拍板"两部分。

前提说明：ModelDev **不会**冒用 `ani-governance` 身份，也不会绕开 Model 的授权校验。

## 一、环境事实（本文档已核，不需要 model-service owner 再查）

| 项        | 值                                                                                                             |
| -------- | ------------------------------------------------------------------------------------------------------------- |
| Model 部署 | `ani-system/ani-model-service`，1/1 Running                                                                    |
| 部署版本     | `a21e0a7`                                                                                                     |
| 服务端身份    | 集群 `ani-model-mtls` 证书 SAN = `DNS:ani-model.ani.internal`                                                     |
| 服务端点     | `ani-model-grpc` `:19090`                                                                                     |
| 现有被授权身份  | 只有 `ani-governance` 和 `ani-inference.ani.internal`                                                            |
| 存储       | Model bucket `ani-models`（`ANI_MINIO_TENANT_BUCKETS=false`）；ModelDev bucket `ani-ft-core-output-*`；同一个 RustFS |

## 二、源码已可定死的结论（不需要 model-service owner 回答，请确认本文档理解无误）

> 注：下文跨仓源码引用统一用**钉到部署版 `a21e0a7` 的 GitHub 链接**（`zhangzhe-ctrl/ani-model-service`），不依赖本地 checkout。
> 该版本相对本地 `master` 的改动集中在鉴权/TLS：除治理身份常量改为 `ani-governance` 外，同一提交还在**身份提取层新增两道校验**（TLS 1.3 强制、客户端证书链必须已验证），身份改从已验证链取值，并抽出 `clientDNSIdentity`（`grpc_tls.go:79`，要求**恰好一个 DNS SAN，且不得混入 URI/IP/email SAN、不得含通配符**）。这不影响 A–E 的结论方向，但对 **ModelDev 客户端证书是硬约束**（见 Q1）。
> 该提交的部署文档 [ani-model.md](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/docs/deploy/helm/ani-model.md) 也明确写明："Model 的 RPC 白名单和可信租户/actor 分支均使用这一精确身份""旧身份 `ani-governance.ani.internal` 不再授权"——即**新增调用方须同时改动这两处**（呼应 D/E）。

### A. 存储交接只能用「受控复制」（B），「直接引用」（A）在代码上不可能

[CreateModelVersion](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/internal/service/model.go#L321-L344) 校验对象时走 `s.storage.ObjectExists(ctx, p.TenantID, in.GetStoragePath())`，而 [bucketForTenant](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/internal/data/storage/minio.go#L229-L237) 在 `tenantBuckets=false` 时**恒返回配置的那个 bucket**（`ani-models`）。

也就是说 `storage_path` 只能表达 **`ani-models` 里的 key**，无法表达"别的 bucket"。ModelDev 的对象在 `ani-ft-core-output-*`，**直接引用表达不出来** → 只能走 (B) 受控复制。

而且路径也由 [GetUploadURL](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/internal/service/model.go#L474) 定死了：`{tenant_id}/{model_id}/{version}/{file_name}`。因此复制的落点是确定的。

**→ 需 model-service owner 确认：** 就按 (B) 走，落 `ani-models`？有没有 bucket/配额方面的额外约束？

### B. 「按 (model_id, version) 点查」只返回 ready；「列表版本」能看到 pending

[GetVersionByExternalRef](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/internal/data/postgres/version_store.go#L44-L60)（Go 方法）→ SQL 查询 `GetReadyModelVersionByExternalRef`（[model.sql:29](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/queries/model.sql#L29)）：用 `(external_model_id, version)` 点查，**SQL 带 `v.status='ready'` 过滤，只返回已 ready 的版本**。

但 [ListModelVersions](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/queries/model.sql#L148-L162)（已注册的对外 RPC）按 `model_id` 列版本时**只过滤 `status <> 'deleted'`、不过滤 ready**，返回体含 `status` 字段——**pending 是能看到的**。微调版本号固定为 `exec-<execution_id>`，可据此反查。该 RPC 目前只授权给 Governance，未给 Inference/ModelDev。

因此 MR05 的"未创建 / pending / READY"：**点查只答 READY/没有，列表能答出 pending**——能力并不缺，缺的是把 `ListModelVersions` 放行给调用方。

**→ 需 model-service owner 确认：** 把 `ListModelVersions` 一并放行给 ModelDev（见 Q2）是否足够？还是仍要补一个按业务键/idempotency_key 的点查接口？

### C. `format` 只能是三个值之一

DB CHECK（[000001](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/migrations/000001_model_control_plane.sql#L25)）+ 业务校验都是 `safetensors|gguf|pytorch`。微调产物是 merged HF 目录 + tar，**没有第四个选项**。

**→ 需 model-service owner 确认：** 填 `safetensors`（`storage_path` 指向 HF 包）行不行？还是会扩枚举？

### D. 新身份必须同时进两个地方

[ModelServicePolicy](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/cmd/ani-model-service/rpc_policy.go#L101-L133) 是**硬编码 map**，且外面还包了 `ValidatedPolicy.expectedIdentities`——只加 map 不加 `expectedIdentities` 也会被拒。两个都要加。

### E. 非 Gov / 非 Inference 的身份拿不到 Principal

[ModelPrincipalMiddleware](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/cmd/ani-model-service/rpc_policy.go#L153-L162) 只有三个分支：governance → 构造 principal；inference → 构造 principal；**其它 → 直接 `next(ctx, req)`，不注入 principal**。后面 `RequireTenant` 就必然 401。

因此 ModelDev 走进去只会落到第三个分支。

## 三、需要 model-service owner 拍板的

### Q1【身份登记·阻塞】

ModelDev 的调用身份要给什么值？

- **DNS SAN 具体填什么？**（这个环境是"证书 SAN = 策略身份"精确匹配，得唯一、不能用通配符）
- 把 D 里说的两处（policy map + expectedIdentities）加上，计划放在哪版发布？

（部署文档 [ani-model.md](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/docs/deploy/helm/ani-model.md) 在描述 Governance 接入时，正说明了"上线本变更时须同步客户端证书与配置"这一流程；ModelDev 接入预计走同一路径。）

### Q2【允许方法·阻塞】

申请放行以下 7 条（ModelDev 侧已按此清单实现客户端，**只要放行即可直接使用**）：

```
/model.v1.ModelService/GetModel
/model.v1.ModelService/GetModelVersion
/model.v1.ModelService/ListModelVersions
/model.v1.ModelService/CreateModel
/model.v1.ModelService/CreateModelVersion
/model.v1.ModelService/GetUploadURL
/model.v1.ModelService/GetModelDownloadURL
```

现状：`GetModel`/`GetModelVersion`/`GetModelDownloadURL` 是 **Inference 与 Governance 共有**；`CreateModel`/`CreateModelVersion`/`GetUploadURL` **仅 Governance**；`ListModelVersions` **仅 Governance**。这些方法 ModelDev 一条都没有。上面 7 条能放给 ModelDev 吗？还是有别的建议方法？

> **为何必须要 `ListModelVersions`**：它是**唯一能看见 `pending` 状态的接口**——`GetModelVersion`（含 `(external_model_id, version)` 变体）的 SQL 带 `v.status='ready'` 过滤，只返回已 ready 的版本；而 `ListModelVersions` 只过滤 `status <> 'deleted'`，返回体含 `status` 字段。微调版本号固定为 `exec-<execution_id>`，可据此反查。缺它就无法区分"未创建"与"写入进行中"。

### Q3【Principal 分支怎么加·阻塞】

按 E，ModelDev 需要一个**新的 principal 构造分支**。ModelDev 侧**已按 governance 风格实现**（可复用现有 governance 分支的形状，仅换身份常量）：

- 每次调用**重建** metadata（不转发入站元数据）：`x-ani-tenant-id`、`x-ani-actor`、`x-ani-request-id`；tenant 侧强制规范 UUID。
- **`x-ani-actor` 的取值请指定**：governance 分支强制 `governance:` 前缀，ModelDev 是否也要求一个自己的前缀（如 `modeldev:`）？还是用其它固定值？
- **Principal 的 `Workload` 填什么值？**（`Principal.Valid()` 要求 `TenantID`/`Actor`/`Workload` 三者非空）

即：请给出该分支的 **actor 前缀规则**与 **Workload 常量**，ModelDev 侧按此填配置即可。

### Q4【超时与预算】

ModelDev 侧**已定单次 RPC 超时上限 30 秒**（配置可小于、不可大于；超过即拒绝启动）。已核对**三方当前一致**：

| 侧 | 超时 |
| --- | --- |
| ModelDev 客户端（我方） | 30s（上限 30s） |
| Gov 客户端（现有先例） | 30s（上限 45s） |
| Model 服务端 | chart 部署显式设 `ANI_SERVER_GRPC_TIMEOUT=30s` |

请确认：

- 三方 30 秒一致是否可接受？Model 侧**是否还有其它隐含上限**（如负载均衡、Ingress、代理层）需要我方低于 30s？
- 登记类写入是否需要**额外重试预算**？（ModelDev 侧**不自行重试**——同键重放只做原结果查证，靠 Model 的 `idempotency_key` 幂等兜底。）

### Q5【租户映射】

Model 的 `tenant_id`（UUID）和 Gov / ModelDev 的租户体系怎么对应？这一点源码里看不出来，需 model-service owner 给出目标环境的映射关系或来源。

### Q6【API 模块版本与消费方式·已落地，待确认】

Gov《服务间调用规则》§5 要求：只消费接收方公布的**固定版本 API 模块**，不复制 Proto/生成代码，不用本地 `replace`。

ModelDev **已按 Gov 同一先例落地**：`go.mod` pin 了 `github.com/liangzai006/ani-model-service v0.0.0-20261013092523-8951c85bc150`（Gov 同款），go.sum 有校验和、生产码真实 import `api/model/v1`、**无任何 `replace`**。注意该模块**没有语义化 tag，只有 pseudo-version**。

需 model-service owner **确认**（不再是"请给出方案"）：

- 该 pin **是否合适**？若应改用别的 SHA，请指定（建议不早于部署版 `a21e0a7`）。
- module 路径确认为 go.mod 声明的 `github.com/liangzai006/ani-model-service`（而非仓库 remote `zhangzhe-ctrl/...`）。

### Q7【基模逐文件清单 · 阻塞 · **优先级高于 Q3/format**】

微调受理前要冻结基模的**逐文件字节清单**，用于 prepare 阶段逐文件核验并解 tar。本仓契约 `BaseModelRef` 要求：

- `Files []BaseModelFile`——每项含 `path` / `size_bytes` / `sha256`，**1–128 个文件**，且**必须包含** `config.json`、`model.safetensors`、`tokenizer_config.json`、`tokenizer.json`；
- `Object FixedObjectRef`——含**固定版本**（`VersionID`，或等价的不可变副本标记）、整包 `size_bytes` / `sha256`；
- `Scope`——存储连接 / bucket / 已批准前缀 / 凭据引用。

**而 Model 现有的 `GetModelVersion` 只返回单个 `storage_path` + 整包 `size_bytes` / `checksum_sha256`，没有任何逐文件字段**（`ModelVersion` 无 files 字段，`ModelArtifact` 只有整包 provider/reference/sha256）。

即：**这里缺的不是权限，是数据形状。** 请二选一并明确：

1. **Model 补一个窄接口**，能返回"固定版本 + 逐文件 path/size/sha256 清单"；或
2. **确认 ModelDev 改为只按整包 tar 读取**——但那需要同步修改本仓 prepare 阶段的逐文件核验契约（属本仓改动，但会放宽现有完整性约束，需双方认可）。

> **为什么它比 format 更靠前**：format（见上文 §二·C）只影响**登记写入**，而逐文件清单影响**受理入口**——拿不到清单，`BaseModelRef` 无法冻结，整条链从第一步就走不通。这也是本仓 `BaseModelRef` 与 Model 现有 RPC 之间的**唯一硬形状缺口**。

## 四、ModelDev 侧进展（非提问）

截至本文更新，ModelDev 侧已完成以下与 Model 对接直接相关的实现（**全部在独立验证宿主上通过，未部署、未发起任何真实 Model 调用**）：

- **登记派发仲裁（D09.3）已实现**：与 Stop/关闭/deadline **共用同一执行行锁**，先持久化派发资格、**事务外**才发 RPC；区分"意图已存 / 已获准派发 / 结果未明"，`UNKNOWN` 不当失败（含 6 例真实 PostgreSQL 单测）。
- **登记写入编排已实现**：`CreateModel` / `CreateModelVersion` / `GetModelVersion` / `ListModelVersions` 的调用编排与"原结果精确读回"验证已落地（8 例单测，用可观测替身，**尚未接真实 mTLS**）。
- **Model 消费基线已定**：`go.mod` 已 pin `github.com/liangzai006/ani-model-service v0.0.0-20261013092523-8951c85bc150`（与 Gov 同款、无 `replace`），并已按该模块实现 Q2 所列 7 个 RPC 的客户端。
- **调用配置位已加**：新增 Model 连接配置（endpoint / CA / 客户端身份 / 服务端身份 / actor / 超时），**缺省或部分配置一律拒绝**，不回退明文或环境凭据。

仍未完成（ModelDev 本地待办，不阻塞 owner 回复）：

- **受理前的基模只读解析（C1/C2）**——其真实实现依赖 Q1/Q2/Q3，无身份会被 `PermissionDenied` 直接拒。
- 顶层 `RegisterModel` 接入上述编排；生产装配接线。
- 部署侧配置填值（尚未部署）。

**故 Q6 的消费方式已按 Gov 先例自行落地，仅需确认 pin 是否合适（见 Q6）。真联调仍必须等 Q1/Q2/Q3。**

## 五、回复方式

**Q1–Q7 逐条回复即可。不确定的直接标注"待定"**，本文档不做假设性实现。  
第二节（A–E）如哪条理解有误，请指出。

**阻塞优先级**：Q1（身份）与 Q7（基模逐文件清单）是硬阻塞——前者卡住所有调用，后者卡住受理入口。Q2/Q3 紧随其后（身份确定后即可放行）。Q4/Q5/Q6 为确认项。

---

> 附：完整调用表和缺口分析见 `ani-modeldev-service/docs/finetuning-model-call-table.md`。  
> 本文档不替代 spec；有冲突以 spec 为准。

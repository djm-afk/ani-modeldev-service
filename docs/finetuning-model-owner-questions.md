# 微调登记链接入 Model：需要 model-service owner 拍板的几个点

日期：2026-10-16  
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

[GetVersionByExternalRef](https://github.com/zhangzhe-ctrl/ani-model-service/blob/a21e0a7/internal/data/postgres/version_store.go#L44-L60) + `GetReadyModelVersionByExternalRef`：用 `(external_model_id, version)` 点查，**SQL 带 `ready` 过滤，只返回已 ready 的版本**。

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

申请放行以下 7 条：

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

### Q3【Principal 分支怎么加·阻塞】

按 E，ModelDev 需要一个**新的 principal 构造分支**。需 model-service owner 确认它长什么样：

- `x-ani-tenant-id`、`x-ani-request-id` ModelDev 照传（和 governance 一样）。
- **`x-ani-actor` 填什么？** governance 那边强制 `governance:` 前缀，ModelDev 是否也要一个自己的前缀（比如 `modeldev:`）？
- Principal 的 `Workload` 填什么值？

### Q4【超时与预算】

登记类 RPC 单次超时、重试预算建议多少？需要跟 Release 总预算对齐吗？

### Q5【租户映射】

Model 的 `tenant_id`（UUID）和 Gov / ModelDev 的租户体系怎么对应？这一点源码里看不出来，需 model-service owner 给出目标环境的映射关系或来源。

### Q6【API 模块版本与消费方式】

Gov《服务间调用规则》§5 要求：只消费接收方公布的**固定版本 API 模块**，不复制 Proto/生成代码，不用本地 `replace`。ModelDev 当前 go.mod 里没有 `ani-model-service` 依赖、全仓也没有 `model.v1` 的生成代码，所以需要确认消费基线。

Gov 已有可运行的消费先例：`github.com/liangzai006/ani-model-service v0.0.0-20261013092523-8951c85bc150`，go.sum 有校验和、生产码真实 import `api/model/v1`、且无 `replace`。注意该模块**没有语义化 tag，只有 pseudo-version**。

需 model-service owner 确认：

- ModelDev 沿用 Gov 同一消费方式是否可行？请指定 **pin 的 SHA**（建议不早于部署版 `a21e0a7`）。
- module 路径按 go.mod 声明的 `github.com/liangzai006/ani-model-service` 解析（而非仓库 remote `zhangzhe-ctrl/...`），请确认以 module 路径为准。

## 四、ModelDev 侧不受阻塞的进度（非提问）

- ModelDev 侧读路径（C1/C2）、登记派发与 Stop/deadline 仲裁（D09.3）**尚未开发**，不阻塞 model-service owner 回复；
- 但**真联调必须等 Q1/Q2**，否则调用会被直接拒；Q6 只需确认消费基线。

## 五、回复方式

Q1–Q6 逐条回复即可。**不确定的直接标注"待定"**，本文档不做假设性实现。  
第二节（A–E）如哪条理解有误，请指出。

---

> 附：完整调用表和缺口分析见 `ani-modeldev-service/docs/finetuning-model-call-table.md`。  
> 本文档不替代 spec；有冲突以 spec 为准。

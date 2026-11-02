# Gov / Res main 源码核验

采集时间（UTC）：2026-10-31T14:39:05.955Z 至 2026-10-31T14:44:15.815Z。执行者：Gov/Res SOURCE 子代理；本地主机、只读 Git 源码检查。任务合同：包根 `spec.md` v1.1（已完整读取）。仅写本证据文件与 [SRC-version-metadata.json](SRC-version-metadata.json)；命令见 [commands.tsv](commands.tsv)，未实施、提交、构建、连接集群或数据库、读取凭据值。证据等级均为 **SOURCE**；下文“默认”不代表现场有效值。

## SRC-GR-00：版本与工作区

| 仓库 | 执行时 remote main（git ls-remote） | 本地 HEAD | 工作区 |
|---|---|---|---|
| Gov | `ddd98f4c79751ba848fa862bab6878dc7bc99bb7` | `323eb749b8a3d607b26687797b372919052895c7` | clean |
| Res | `23e25b33ca02890231cd273ef7246b0fc415476c` | `d32c4f438b43b8cb7534486c73e116e4d26c890a` | 有无关文档/历史材料修改与未跟踪目录，完整路径在 JSON；未覆盖 |

Gov 使用 origin SSH 查询成功。Res origin SSH 查询失败（`Connection closed by 198.18.0.53 port 22`，退出 128），随后同仓 HTTPS `ls-remote` 成功，main 与任务包基线一致。Res 本地原无该对象，执行 `git fetch --no-tags https://github.com/zhangzhe-ctrl/ani-resource-service.git 23e25b33ca02890231cd273ef7246b0fc415476c` 成功，仅取得对象与 FETCH_HEAD；未 checkout 或切换分支。业务源码统一通过 `git show <固定 SHA>:<path>` / `git grep <固定 SHA>` 读取。Git 2.55.0。

K00.SOURCE：PASS。K00.SOURCE_IMAGE_MAPPING：NOT_VERIFIED；此子任务未访问现场，不能把 main 或本地 HEAD 等同现场镜像。

## SRC-GR-01：Gov 资源租户 UUID 的可信链

1. 持久来源是 `sys_tenants.resource_tenant_id`。Schema 指定 `NotEmpty().Unique().Immutable().DefaultFunc(uuid.NewString)`；不是数字主键、code 或名字。[tenant.go:20–36](https://github.com/zhangzhe-ctrl/ani-governance/blob/ddd98f4c79751ba848fa862bab6878dc7bc99bb7/app/admin/service/internal/data/ent/schema/tenant.go#L20-L36)
2. `TenantRepo.ResourceTenantID` 拒绝数字 tenant 0；按已认证租户 ID 读持久 Tenant；拒绝 parse 失败、零 UUID、非小写完整标准字符串。缺行/读取失败返回 `TENANT_MAPPING_UNAVAILABLE`，非法映射返回 `TENANT_MAPPING_INVALID`。[tenant_resource.go:10–25](https://github.com/zhangzhe-ctrl/ani-governance/blob/ddd98f4c79751ba848fa862bab6878dc7bc99bb7/app/admin/service/internal/data/tenant_resource.go#L10-L25)
3. 创建执行从 `auth.PrincipalFromContext` 取用户身份，要求用户主体且 TenantID 非零；经过当前授权，再调用该持久 resolver，随后冻结数字 TenantID、资源 UUID 与 actor。查询路径同样从 Principal → 当前授权 → resolver 取得 UUID。[modeldev_service.go:101–162](https://github.com/zhangzhe-ctrl/ani-governance/blob/ddd98f4c79751ba848fa862bab6878dc7bc99bb7/app/admin/service/internal/service/modeldev_service.go#L101-L162)、[modeldev_query_service.go:20–42](https://github.com/zhangzhe-ctrl/ani-governance/blob/ddd98f4c79751ba848fa862bab6878dc7bc99bb7/app/admin/service/internal/service/modeldev_query_service.go#L20-L42)
4. 发往 ModelDev 的 `x-ani-tenant-id` 是上述资源 UUID；通过 `metadata.NewOutgoingContext` 重建出站 metadata。admission 同时验证 identity.TenantID 与 scope UUID 相等及规范 UUID；查询还加固定 method/data-scope。[modeldev_client.go:165–176](https://github.com/zhangzhe-ctrl/ani-governance/blob/ddd98f4c79751ba848fa862bab6878dc7bc99bb7/app/admin/service/internal/data/modeldev_client.go#L165-L176)、[304–310](https://github.com/zhangzhe-ctrl/ani-governance/blob/ddd98f4c79751ba848fa862bab6878dc7bc99bb7/app/admin/service/internal/data/modeldev_client.go#L304-L310)、[modeldev_query_client.go:30–49](https://github.com/zhangzhe-ctrl/ani-governance/blob/ddd98f4c79751ba848fa862bab6878dc7bc99bb7/app/admin/service/internal/data/modeldev_query_client.go#L30-L49)
5. 普通 update_mask 显式拒绝修改 `resource_tenant_id/resourceTenantId`。[tenant_repo.go:257–267](https://github.com/zhangzhe-ctrl/ani-governance/blob/ddd98f4c79751ba848fa862bab6878dc7bc99bb7/app/admin/service/internal/data/tenant_repo.go#L257-L267)

K01.GOV_TRUSTED_MAPPING.SOURCE：PASS。此证据证明实现的可信来源与传递，不证明现有数据库映射有效、现场认证已经通过或训练执行成功。

## SRC-GR-02：Res 命名与生效配置入口

- `CanonicalUUID` 要求非零、parse 后 String 完全相同；`NamespaceName` 直接拼 `prefix + tenant`，拒绝空前缀、非规范 UUID、超过 63 字符或不符合 DNS label，未去横杠、hash 或截断。[storage/validate.go:32–64](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/biz/storage/validate.go#L32-L64)
- 配置模板：`network.cluster_id: "${NETWORK_CLUSTER_ID:primary}"`，`network.namespace_prefix: "${NETWORK_NAMESPACE_PREFIX:tenant-}"`。这是**模板默认**；并非当前集群 ID 或前缀观察值。[configs/config.yaml:11–18](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/configs/config.yaml#L11-L18)
- 主入口 `-conf` 默认 `configs`；运行时加载该文件源和 `env.NewSource("ANI")` 后 Scan。现场须同时看实际启动参数指定的文件/挂载、相关环境配置，不能只看仓库模板。[main.go:34–41](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/cmd/ani-resource-service/main.go#L34-L41)、[122–134](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/cmd/ani-resource-service/main.go#L122-L134)
- Network placement 直接取 `bc.Network.ClusterId/NamespacePrefix`；其构造只限制非空 cluster 与 DNS 前缀（允许其他合法前缀），并未强制 `tenant-`。创建 VPC 的持久映射也使用配置 prefix + tenant。[app.go:27–37](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/cmd/ani-resource-service/app.go#L27-L37)、[network/postgres.go:25–46](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/data/network/postgres.go#L25-L46)、[177–186](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/data/network/postgres.go#L177-L186)

K01.RES_ALGORITHM.SOURCE：PASS（可收敛目标算法）；K01.RES_FIXED_PREFIX.SOURCE：FAIL（源码仍允许可配置前缀）。现场 observed_res_prefix/cluster_id 均未观察，保持 null。

## SRC-GR-03：权威归属合同与缺失自动创建分支

- Storage Namespace 权威常量是 `network.ani.io/managed-by=ani-network-service` 与 `network.ani.io/tenant-id=<UUID>`。Storage 对 Namespace 只 GET 并投影删除状态/归属；其 Namespace DTO 当前没有 UID。[storage/types.go:20–32,51–57](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/biz/storage/types.go#L20-L57)、[storage/k8s.go:57–70](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/data/storage/k8s.go#L57-L70)
- VM ResolveNamespace 按 prefix+tenant 精确 GET，并检查删除状态及同一组标签；返回实际 ObjectRef。[vm/environment.go:24–39](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/data/vm/environment.go#L24-L39)
- **Network 仍自动创建**：EnsureVPC/EnsureSubnet → ensure → ensureNamespace；GET NotFound 即 Create，AlreadyExists 再 GET，检查名称/删除状态/两项标签，未在此检查 UID 非空或冻结 UID。[network/kc.go:207–222](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/data/network/kc.go#L207-L222)、[283–300](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/data/network/kc.go#L283-L300)
- **Sandbox 规格解析仍可能创建**：ResolveSandboxNamespaceUID 用 ensure=false，EnsureSandboxNamespaceUID 用 true；后者在 NotFound 时 Create 并以固定名称再观察，最后检查真实 UID 非空、删除状态和同一组权威标签。RPC ResolveCreateSpec 的实际 service 路径调用的是 Ensure 版本，之后把 cluster/name/UID 放入结果。[sandbox/namespace.go:24–75](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/data/sandbox/namespace.go#L24-L75)、[sandbox/service.go:265–277](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/service/sandbox/service.go#L265-L277)

K01.NO_AUTOCREATE.SOURCE：FAIL。目标“查不到即报错”尚未统一；不要把上述源码默认命名与标签视为 Namespace 初始化已完成。

## SRC-GR-04：pull-secret writer、UID 与 caller 范围

- 既有 `ImageRuntimeService.EnsureTenantPullSecret` 是 Secret 写入 RPC；输入 tenant/cluster/namespace/namespace_uid/registry，输出非敏感引用及 generation/expiry/registry/space；不是 Namespace 创建/查询接口。[runtime.proto:8–16,46–64](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/api/image/v1/runtime.proto#L8-L64)
- Runtime transport 验证 TLS 1.3、已验证链与可信服务 SAN；要求单个规范资源租户与 request UUID，拒绝用户 actor/operator/scope 头；请求 tenant 必须等于 metadata tenant。[image_runtime.go:81–145](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/server/image_runtime.go#L81-L145)
- 部署 `image.pull_secret_cluster_id` 和 kubeconfig 决定单集群 writer；配置 caller 提供 subject、cluster_ids、**静态 namespaces 列表**、owner_labels、require_tenant_label。装配从配置复制 caller 表，非请求提供。[conf.proto:58–72](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/conf/v1/conf.proto#L58-L72)、[image.go:162–175](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/cmd/ani-resource-service/image.go#L162-L175)
- 外部 caller 必须在表中，cluster 与 namespace 都命中 allowlist，才生成 Verify=true 的 claim。无 wildcard 或动态 prefix 匹配分支；`contains` 是字符串忽略大小写的相等比较。Resource 自己的 instance 路径跳过实际 Namespace 读校验，但仍检查 writer cluster；这是现有内部路径区别，不能误称外部调用绕过。[pullsecret_caller.go:36–89](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/biz/image/pullsecret_caller.go#L36-L89)、[pullsecret.go:146–156](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/data/image/pullsecret.go#L146-L156)
- 外部 VerifyNamespace 精确 GET；要求 NamespaceUID 非空、未删除、实际 UID 等于 claim UID。**标签仍是另一套合同**：`ani.io/resource-tenant-id`，`ani.io/managed-by`，owner 值由 caller 配置允许；它并未采用目标 `network.ani.io/*`。[pullsecret.go:24–28](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/data/image/pullsecret.go#L24-L28)、[143–184](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/data/image/pullsecret.go#L143-L184)
- Secret 自身的 `image.ani.io/tenant-id/space-id` 与 `ani.io/managed-by=ani-resource-service` 是 Secret 对象归属，不能与 Namespace 归属混为一谈；未来统一 Namespace 标签时不应把整个租户 Namespace 改为 ModelDev 或 Secret writer 所有。[pullsecret.go:81–85,124–129](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/data/image/pullsecret.go#L81-L129)

K09.RES_NAMESPACE_UID_GUARD.SOURCE：PASS（外部 caller 路径）。K09.RES_NAMESPACE_LABEL_UNIFIED.SOURCE：FAIL。K09.RES_DYNAMIC_NAMESPACE_AUTH.SOURCE：FAIL（动态接入仍需逐 Namespace 静态名单）。未调用 Ensure，也未读 Secret；现场 caller/cluster/namespace 名单与 Secret 引用保持 NOT_VERIFIED。

## SRC-GR-05：供现场执行者使用的只读入口线索

1. Gov 受管 HTTP 已有 GET `/api/v1/admin/tenants` 和 `/api/v1/admin/tenants/{id}`，但其 Tenant Proto **没有 resource_tenant_id 字段**；不能声称这个接口已返回资源 UUID，也不要发明 Namespace 接口。[i_tenant.proto:12–25](https://github.com/zhangzhe-ctrl/ani-governance/blob/ddd98f4c79751ba848fa862bab6878dc7bc99bb7/api/protos/admin/service/v1/i_tenant.proto#L12-L25)、[tenant.proto:51–168](https://github.com/zhangzhe-ctrl/ani-governance/blob/ddd98f4c79751ba848fa862bab6878dc7bc99bb7/api/protos/identity/service/v1/tenant.proto#L51-L168)
2. 当前权威映射读取实现就是 Gov 内部 TenantRepo 与 `sys_tenants`；现场若没有另一个已授权受管读入口，可在**现有只读数据库通道**中仅取候选 A/B 的 `id, resource_tenant_id`，限定候选 ID，禁止整表/用户资料/DSN 导出。此处只定位表与字段，**未连接或查询业务库**。[tenant.go:20–36](https://github.com/zhangzhe-ctrl/ani-governance/blob/ddd98f4c79751ba848fa862bab6878dc7bc99bb7/app/admin/service/internal/data/ent/schema/tenant.go#L20-L36)、[tenant_resource.go:11–25](https://github.com/zhangzhe-ctrl/ani-governance/blob/ddd98f4c79751ba848fa862bab6878dc7bc99bb7/app/admin/service/internal/data/tenant_resource.go#L11-L25)
3. 平台 cluster 的现场发现应先追 Res 实际 Deployment 的 `-conf`、文件引用与环境来源，提取 `network.cluster_id/namespace_prefix` 和 kubeconfig **引用路径**；镜像 writer 另取 `image.pull_secret_cluster_id`。这两者不能未经核对当成同一个 ID。Gov Compute/Instance catalog 另用 `ANI_INSTANCE_CLUSTER_ID`；它是配置线索，不是当前 ModelDev 执行绑定的证据。[main.go:122–134](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/cmd/ani-resource-service/main.go#L122-L134)、[conf.proto:58–80](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/conf/v1/conf.proto#L58-L80)、[wiring_ent.go:487–488](https://github.com/zhangzhe-ctrl/ani-governance/blob/ddd98f4c79751ba848fa862bab6878dc7bc99bb7/app/admin/service/cmd/server/wiring_ent.go#L487-L488)
4. Res 内部 SQL 的 `network_tenant_namespaces(tenant_id,cluster_id,namespace)` 是已存在的 Network 持久映射线索，但不是公共查询 RPC，也不存储 Namespace UID；不能替代 live GET。SOURCE 已定位 `GetTenantNamespace` 的查询；**未执行**。[vpcs.sql:74–79](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/23e25b33ca02890231cd273ef7246b0fc415476c/internal/data/network/queries/vpcs.sql#L74-L79)

## SRC-GR-06：最小后续改造面（建议，未实施）

- Gov：复用现有 Principal → 当前授权 → 持久资源 UUID → ModelDev metadata 的链；本次未发现必须为命名新增 Gov Namespace 功能的理由。确保后续错误映射保留 Namespace 不存在/归属/UID/权限/不可用原因；不把业务请求中的 cluster/name 当受信选择。
- Res：固定系统 `tenant-` 规则；Network `ensureNamespace` 与 Sandbox ResolveCreateSpec 的 Ensure 分支改为仅 GET、缺失明确报错；保留并补齐 live UID/归属/删除状态校验与已有冻结 UID 一致性，不能补标签/收养/旧名回退。Storage 目前 Namespace 投影无 UID、Network 校验未含 UID，若要求全 owner 完整执行规则 v1，需一并补齐对应 DTO/绑定校验，不能只改 ModelDev。
- Res 镜像侧：Namespace 校验统一到既有 network 两键和固定 owner；ModelDev 训练 caller 的 Namespace 授权按可信 UUID 推导并精确 GET，避免每个租户追加静态 namespace。保留可信 caller、受信 cluster 范围及固定 UID 校验；Secret 对象自身归属标签独立保留。其他 owner 的现有路径按其合同核对，不通过删除 allowlist 或关闭 Verify 来制造动态接入。
- 不新增 Res Namespace 查询/开通 RPC。后续 owner 直接 GET；预置 Namespace 是独立初始化前置，缺少时返回未就绪。本轮不执行任何 Namespace/Secret/数据清理。
- 现场实际前缀、两租户 UUID/name/UID、有效 caller 表、运行镜像与源码映射仍缺证据，必须由现场证据填写。SOURCE 不决定 KFP 身份策略，也不证明单进程双租户派发。

命令、UTC、真实 shell 退出码与失败说明见 [SRC-version-metadata.json](SRC-version-metadata.json)；命令见 [commands.tsv](commands.tsv)。多命令历史 shell 的退出码是该 shell 最后命令状态；曾有一次 Gov `git show` 在 Res cwd 读取失败，已在正确 Gov cwd 重读，未据失败输出建立结论。


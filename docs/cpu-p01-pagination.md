# CPU-P01 列表分页与游标签名运维

- Status: service-owned CPU-P01 分页与游标密钥合同
- Scope: ModelDev 四个查询列表（executions / presets / input-versions / artifacts）的游标分页、签名绑定与密钥运维

本文记录 ModelDev 列表分页的对外形状、下游签名游标语义，以及**部署密钥的注入与维护要求**。与 Governance 契约文档 [pagination.md](../../ani-governance/docs/contracts/pagination.md)、登记 MODELDEV-10/11 对应。

## 1. 对外形状

四个列表统一为游标（keyset）分页，与 VPC 同款：

| 项 | 值 |
| --- | --- |
| 请求 | `limit`（默认 20、1～100）、`cursor`（不透明、上限 2048 字节） |
| 响应 | `{items, next_cursor, total}`（`total` 为该筛选下的匹配总数，不含 limit 窗口） |
| 翻页 | 首次不传 `cursor` 取首页；后续原样回传上一页 `next_cursor`；**只单向向后** |

不支持 `page`/`offset` 跳页——VPC 与 ModelDev 均如此，跳页能力不在本协议内。

## 2. 游标签名语义

游标由本服务（下游 `ani-modeldev-service`）签发，Governance BFF **纯透传**（仅校验长度 ≤ 2048）。

- 格式：`base64url(payload) + "." + base64url(mac)`，HMAC-SHA256。
- 载荷绑定：`{版本, 资源种类, 租户, 筛选指纹, 上页边界}`。
- 篡改、跨租户、跨资源种类、换筛选复用旧游标 → 一律 `INVALID_CURSOR`（映射为 `INVALID_MODELDEV_QUERY` 400），**不降级为首页**。
- 缺签名密钥 → fail-closed：查询监听器不装配，进程启动失败。

## 3. 密钥配置

签名密钥为 `cursor_signing_key`（`conf.Bootstrap` 字段），值为 **base64 编码的 32～128 字节**。

- 校验：`internal/conf/v1/validate.go` 在 `command` 块存在时强制校验；缺失或非法即启动失败。
- 装配：`cmd/ani-modeldev-service` 启动时解码并注入 `biz.CursorCodec`（`WithCursorCodec`），四个列表在 service 层签名/解签。
- 配置来源：`config.yaml` 顶层 `cursor_signing_key`，通过 Kratos 占位符从环境回填，推荐写法：

  ```yaml
  cursor_signing_key: "${CURSOR_SIGNING_KEY:}"
  ```

  配套环境变量 `ANI_CURSOR_SIGNING_KEY`（Kratos `env.NewSource("ANI")` 去前缀后得到顶层键 `CURSOR_SIGNING_KEY`）。这与 Resource 服务 `ANI_NETWORK_CURSOR_SIGNING_KEY` 是同一机制。

### 3.1 部署注入（ani-system / ani-modeldev）

密钥**不落明文**，走独立 Secret + env：

| 对象 | 内容 |
| --- | --- |
| Secret `modeldev-cursor-signing-key` | 键 `cursor_signing_key` = base64(32～128 字节) |
| Deployment env | `ANI_CURSOR_SIGNING_KEY` ← `secretKeyRef: modeldev-cursor-signing-key/cursor_signing_key` |
| ConfigMap `md-fixed-config` 的 `config.yaml` | 仅含占位符 `cursor_signing_key: "${CURSOR_SIGNING_KEY:}"` |

**多副本必须共享同一密钥**，否则跨副本翻页失败。密钥轮换会使所有旧游标失效（客户端应从首页重取）。

## 4. 运维注意事项（重要）

`md-fixed-config` 是**手工维护**的 ConfigMap。以下两点必须留意：

1. **`manual.py render` 会覆盖**：`scripts/cpu-p01-manual/manual.py` 的 render 会按种子重建该 ConfigMap 的 `config.yaml`，**会清除手工加入的 `cursor_signing_key` 占位符行**（与 `webhook timeoutSeconds` 会被打回 2s 属同类问题）。render 之后必须**重新补回占位符**，否则新镜像启动时因缺密钥 fail-closed。
2. **补回方式**（幂等）：

   ```bash
   # 在 ani-01 上，将占位符加回 config.yaml（不含密钥本体）
   kubectl get cm md-fixed-config -n ani-system -o jsonpath='{.data.config\.yaml}' \
     | { echo 'cursor_signing_key: "${CURSOR_SIGNING_KEY:}"'; cat; } > /tmp/config.yaml
   # 再以 merge patch 写回（注意 JSON 转义）
   ```

   或直接重跑本仓 `local-remote/deploy-work/apply-cursor-config.sh`（本机专用脚本，不入库）。

3. **换镜像顺序**：先确保 Secret + env 与 config 占位符就位，**再**替换镜像；否则新镜像因缺密钥起不来。

## 5. 验证要点

- 四个列表首页 `next_cursor` 为签名串（含 `.`，非裸 UUID）。
- 首页 + 回传 `next_cursor` 取第二页，两页不重叠。
- 篡改 `next_cursor` 任一字符、或跨列表复用 → 400。

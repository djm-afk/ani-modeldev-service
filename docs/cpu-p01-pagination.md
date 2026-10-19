# CPU-P01 列表分页

- Status: service-owned CPU-P01 分页合同
- Scope: ModelDev 四个查询列表（executions / presets / input-versions / artifacts）的页码分页

本文记录 ModelDev 列表分页的对外形状与运维要点，与 Governance 契约文档
[frontend-http-api.md](../../ani-governance/docs/contracts/frontend-http-api.md) 的页码协议一致。

## 1 对外形状

四个列表统一为页码（offset）分页：

| 项 | 值 |
| --- | --- |
| 请求 | `page`（默认 1）、`pageSize`（默认 20，合法范围 1～100）；两个参数各自独立取默认值 |
| 响应 | `{items, total}`（`total` 为该筛选下的匹配总数，不含分页窗口） |
| 翻页 | 支持直接跳到任意合法页；页码超出最后一页时 `items` 为空，`total` 保留实际匹配总数 |
| 非法值 | `page=0`、`pageSize=0`、`pageSize>100` 一律返回 `INVALID_ARGUMENT`，不静默修正 |

列表先按确定顺序排序再分页：executions 与 input-versions 按主键升序，presets 按 preset_id
升序，artifacts 按 artifact_id 升序。列表查询与计数使用同一可信租户与筛选范围，并在同一个
Repeatable Read 只读事务内执行。

各列表映射到内部 `PageRequest.page` / `PageRequest.page_size`，在 service 层解析为
`offset = (page-1) * pageSize` 的窗口；`next_cursor` 字段已在 proto 中 `reserved`。

## 2 与旧签名游标的关系

PR#4 曾把四个列表迁移为 HMAC 签名游标（`limit`/`cursor` → `next_cursor`，只单向向后）。
该方案与新页码协议不兼容（签名游标无法跳页），已整体移除：

- proto：`PageRequest` 的 `limit`/`cursor` 已 `reserved`，响应 `next_cursor` 已 `reserved`。
- 代码：`internal/biz/cursor.go`（`CursorCodec`）与其测试已删除，service 层不再签名/解签。
- 配置：`conf.Bootstrap.cursor_signing_key` 字段与校验已删除，`cmd/ani-modeldev-service`
  不再装配 `WithCursorCodec`。

## 3 部署注意事项

签名密钥不再需要，部署侧如曾按旧方案注入过下列对象，应同步清理：

| 对象 | 处理 |
| --- | --- |
| Secret `modeldev-cursor-signing-key` | 不再需要，可移除 |
| Deployment env `ANI_CURSOR_SIGNING_KEY` | 不再需要，可移除 |
| ConfigMap `md-fixed-config` 的 `config.yaml` 中 `cursor_signing_key` 占位符 | 不再需要，可移除 |

`md-fixed-config` 是手工维护的 ConfigMap，`scripts/cpu-p01-manual/manual.py` 的 render
会按种子重建 `config.yaml`。移除了占位符行后无需再补回。

## 4 验证要点

- 四个列表 `?page=3&pageSize=2` 直接取第三页，`items` 与 `total` 符合预期。
- `page=0`、`pageSize=0`、`pageSize=101` 返回 400。
- 超界页（如 `page` 远大于总页数）返回空 `items` 且 `total` 不变。
- 跨租户、跨列表筛选不被泄露；计数与列表使用相同筛选范围。

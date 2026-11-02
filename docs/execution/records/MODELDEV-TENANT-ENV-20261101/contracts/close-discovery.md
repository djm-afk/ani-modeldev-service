# 多租户关闭候选发现合同

状态：实施设计；本文件中的 SQL 尚未执行，未宣称通过 PostgreSQL 验证。执行 agent 必须在固定代码基线及真实测试数据库上实现、生成 sqlc 并完成本文件用例。不得把本文 SQL 直接向现场执行。

本合同只替换现有 close loop 的发现条件，不添加调度表、队列、lease、worker 或 GPU 释放状态机。原关闭、创建 fence、最多 3 次自动 compute cleanup、GPU seal/原 outbox/原 ACK 的权威不变。

## 1. 现有列、字段和新投影

现有表使用以下真实列：

- `modeldev_executions a`：`tenant_id`、`execution_id`、`operation_id`、`spec_hash`、`accepted_at`、`snapshot_canonical bytea`。
- `modeldev_execution_identities i`：相同四项身份，以及 `close_generation`。
- `modeldev_execution_runtimes r`：`tenant_id`、`execution_id`、`closed_at timestamptz`、`facts jsonb`、`gpu_release_sealed_at`。
- 本期 admission 同事务增加并填写的三个投影：`a.cluster_id`、`a.environment_binding_id`、`a.deadline_at`。不再加 GPU/kind 投影。

runtime JSON 的大小写必须保持：`TrainingWindow`、`StepTimings`、`FinetuningSteps`、`CloseEvidence.NoDispatch`、`StepExitCleanup.CompletedAt`、`ComputeCleanup.phase`、`ComputeCleanup.attempts`、`GPURelease.outbox.ack`、`GPURelease.outbox.delivered_at`。不能把它们统一改成 snake_case。快照设备路径为 `compute_origin.device_mode`。

新查询不再把 `snapshot_canonical` 直接 `convert_from(...)::jsonb`；不在 WHERE 中把 runtime JSON 文本转为 timestamp/integer，也不对未验证形状使用 `jsonb_array_elements`。

## 2. 唯一新增 SQL helper

本 helper 仅用于候选发现。它不检查 canonical 字节、spec_hash、snapshot schema、GPU 合同或关闭资格；不能用它的结果代替 Go 原严格解码。

`8388608` 来自当前 `contract/cpup01/snapshot.go` 的 `DecodeCanonicalSnapshot` 使用的 `MaxManifestBytes = 8 MiB`。v4 codec 和本函数必须保持同一个上限。使用现有 PostgreSQL 的 PL/pgSQL 与 JSONB，不依赖 `pg_input_is_valid`、JSONPath、新扩展或特定新版输入验证函数；部署前确认现有 PostgreSQL 提供 `plpgsql`。默认调用者权限，不使用 SECURITY DEFINER。

```sql
CREATE FUNCTION public.modeldev_scan_snapshot_jsonb(raw_snapshot bytea)
RETURNS jsonb
LANGUAGE plpgsql
IMMUTABLE
STRICT
SECURITY INVOKER
SET search_path = pg_catalog
AS $modeldev_scan$
DECLARE
    parsed jsonb;
BEGIN
    IF octet_length(raw_snapshot) = 0
       OR octet_length(raw_snapshot) > 8388608 THEN
        RETURN NULL;
    END IF;

    parsed := convert_from(raw_snapshot, 'UTF8')::jsonb;
    IF jsonb_typeof(parsed) IS DISTINCT FROM 'object' THEN
        RETURN NULL;
    END IF;
    RETURN parsed;
EXCEPTION
    WHEN data_exception OR program_limit_exceeded THEN
        RETURN NULL;
END;
$modeldev_scan$;
```

这里只吸收坏输入产生的数据异常和解析复杂度上限错误。查询取消、SQL 超时、连接故障、内存/磁盘等数据库故障不能吞成 NULL；不得改成 `WHEN OTHERS`。NULL 是“本行不能由此粗筛解释”，不是 CPU、不是无作业，更不是关闭或退款许可。

## 3. 两个查询共用的粗筛表达式

两个查询都先按 cluster 与原身份关联：

```sql
FROM modeldev_executions AS a
JOIN modeldev_execution_identities AS i
  ON i.tenant_id = a.tenant_id
 AND i.execution_id = a.execution_id
 AND i.operation_id = a.operation_id
 AND i.spec_hash = a.spec_hash
LEFT JOIN modeldev_execution_runtimes AS r
  ON r.tenant_id = a.tenant_id
 AND r.execution_id = a.execution_id
WHERE a.cluster_id = sqlc.arg(cluster_id)::uuid
```

下列一个完整布尔表达式称为 `CLOSE_COARSE`。它必须原样用于外层 tenant discovery 和内层 row page。该名称只是本文的代码片段标识，不是新的数据库对象或运行时 SQL 拼接入口。实际 `queries.sql` 写静态 SQL；两处表达式若重复，验收必须同时覆盖两处。

```sql
(
  -- U: 尚未 CLOSED。内部期限与 successor 的精确判断移到逐行 Go。
  (
    r.closed_at IS NULL
    AND (
      i.close_generation > 0
      OR a.deadline_at <= statement_timestamp()
      OR COALESCE(NULLIF(r.facts -> 'TrainingWindow', 'null'::jsonb),
                  '{}'::jsonb) <> '{}'::jsonb
      OR COALESCE(NULLIF(r.facts -> 'StepTimings', 'null'::jsonb),
                  '[]'::jsonb) <> '[]'::jsonb
      OR COALESCE(NULLIF(r.facts -> 'FinetuningSteps', 'null'::jsonb),
                  '[]'::jsonb) <> '[]'::jsonb
    )
  )
  OR
  -- C: 已 CLOSED，仍可能需要原步骤/compute cleanup。
  (
    r.closed_at IS NOT NULL
    AND (r.facts #> '{CloseEvidence,NoDispatch}')
          IS DISTINCT FROM 'true'::jsonb
    AND (
      jsonb_typeof(r.facts #> '{StepExitCleanup,CompletedAt}')
          IS DISTINCT FROM 'string'
      OR (r.facts #>> '{StepExitCleanup,CompletedAt}') = ''
      OR jsonb_typeof(r.facts -> 'ComputeCleanup')
          IS DISTINCT FROM 'object'
      OR (
        (r.facts #>> '{ComputeCleanup,phase}') IS DISTINCT FROM 'APPLIED'
        AND (
          (r.facts #>> '{ComputeCleanup,phase}') = 'STARTED'
          OR CASE
               WHEN jsonb_typeof(r.facts #> '{ComputeCleanup,attempts}')
                      = 'number'
                 THEN (r.facts #> '{ComputeCleanup,attempts}') < '3'::jsonb
               ELSE TRUE
             END
        )
      )
    )
  )
  OR
  -- G: 已 CLOSED，GPU 尚未 seal/完整 ACK，或快照无法安全分类。
  -- CASE 使完整 ACK 的正常闭合行无需再为此分支解析快照。
  CASE
    WHEN r.closed_at IS NULL THEN FALSE
    WHEN jsonb_typeof(r.facts #> '{GPURelease,outbox,ack}') = 'object'
     AND jsonb_typeof(r.facts #> '{GPURelease,outbox,delivered_at}') = 'string'
      THEN FALSE
    ELSE
      (public.modeldev_scan_snapshot_jsonb(a.snapshot_canonical)
         #>> '{compute_origin,device_mode}') IS DISTINCT FROM 'cpu'
  END
)
```

说明：

- `U` 允许将有活动 timing 事实、但其期限尚未到达的执行作为粗候选；它不提前关闭执行。
- 数组被写成对象、timestamp 字符串非法等情况不会在 SQL 中抛出转换错误。非空错误形状进入 Go 逐行检查。没有需要关闭事实的未到期普通等待项无需被本 loop 扫描；dispatch loop 仍有独立发现职责。
- `C` 的 JSONB 数值比较没有 `::integer`；错误类型进入 Go。合法 `attempts >= 3` 且非 `STARTED` 的自动 cleanup 不被此分支再次接纳。已有合法操作员重试路径仍按原协议处理，不借本项重置预算。
- `G` 将 NULL/不可分类快照作为需要逐行检查的粗候选。正常 CPU 且步骤/compute cleanup 已完成，或合法 NoDispatch CPU，不占候选预算；正常 GPU 的 cleanup 已完成且 ACK/delivered_at 都完整，也不占候选预算。CLOSED 本身并不表示 cleanup 已完成。
- 半 ACK（只有 ack 或只有 delivered_at）必须进入 `G`。之后按原 `ValidGPUReleaseFacts` 判为持久冲突；不能补半条记录或因此再次投递。
- 这不是对所有历史终态 JSON 的持续完整审计。完整终态的一切精确一致性仍由原严格读取器验证；粗筛不创造“已经安全”的新证明。

### 3.1 外层查询

```sql
-- name: ListPendingCloseTenants :many
SELECT DISTINCT a.tenant_id
/* 使用本节完整 FROM/JOIN/cluster 条件 */
  AND (sqlc.narg(after_tenant_id)::uuid IS NULL
       OR a.tenant_id > sqlc.narg(after_tenant_id)::uuid)
  AND /* 本节 CLOSE_COARSE 完整静态表达式 */
ORDER BY a.tenant_id
LIMIT sqlc.arg(batch_size)::integer;
```

### 3.2 内层查询

```sql
-- name: ListPendingClosures :many
SELECT a.tenant_id, a.execution_id, a.operation_id, a.spec_hash,
       a.accepted_at, a.cluster_id, a.environment_binding_id,
       a.deadline_at, a.snapshot_canonical,
       i.close_generation,
       r.closed_at, r.facts, r.gpu_release_sealed_at
/* 使用本节完整 FROM/JOIN/cluster 条件 */
  AND a.tenant_id = sqlc.arg(tenant_id)::uuid
  AND a.accepted_at <= sqlc.arg(sweep_before)::timestamptz
  AND (sqlc.narg(after_execution_id)::uuid IS NULL
       OR (a.accepted_at, a.execution_id)
          > (sqlc.narg(after_accepted_at)::timestamptz,
             sqlc.narg(after_execution_id)::uuid))
  AND /* 本节 CLOSE_COARSE 完整静态表达式 */
ORDER BY a.accepted_at, a.execution_id
LIMIT sqlc.arg(batch_size)::integer;
```

这两个展示块是引用同一静态片段的实现合同，不是可直接运行的完整 SQL。实现必须展开片段后运行 sqlc；不能把注释占位当作已实现查询。内层两个 after 参数必须同时为空或同时规范非空；`batch_size` 由服务固定上限控制，不能直接使用调用者数值。

## 4. Go 逐行精算与游标

候选发现只表达“需要检查”。每行先用原 v4 canonical/hash 解码，并比较 cluster_id/environment_binding_id/deadline_at 三个同事务投影；再严格解码原 runtime 事实，按当前 `internal/data/lifecycle/queries.sql` 中的原条件计算以下 Reasons：

- `NEEDS_CLOSE`：原 close generation、execution deadline、training window 及对应 observation、步骤期限及原 successor 条件。
- `NEEDS_STEP_OR_COMPUTE_CLEANUP`：原 NoDispatch、StepExitCleanup、ComputeCleanup/attempts/STARTED 条件。
- `PENDING_GPU_OUTBOX`：原 GPU release 有有效 scope/outbox，ack 与 delivered_at 均为空。
- `GPU_RELEASE_NOT_SEALED`：尚未 seal 的原 GPU 执行；具体 seal 是否成立仍由既有 repository 资格检查决定。

没有命中精确条件时 Reasons 为空，记为 examined 后推进游标，不访问 KFP/Kubernetes、不调用 closer。单行解码或事实一致性错误只生成该行的有限诊断并计 Unresolved，不返回整个 page error。错误信息不包含原 JSON 或凭据。

port 固定返回**每个实际扫描行**的 TenantID/ExecutionID/AcceptedAt、Reasons（允许为空）与 DecodeProblem（为空或有限错误码）；page另含ExaminedCount。不能过滤掉 NONE/坏行后只返回合格 IDs，否则无法推进实际分页。是否到末尾由原 SQL 行数 `< limit` 判断；不能用成功数判断。

每 tenant 最多扫描 4 行、每 tick 最多扫描 64 行等主方案预算均按 examined 计。单项出错照样推进；游标只由 loop 所属 goroutine 修改。内层有限 sweep 完成后重置；外层 tenant keyset 到末尾再回绕。首次接受、旧记录关闭或环境状态改变不创建额外 worker。

已 seal 合法 outbox 绕过 live Namespace、当前 READY、材料缓存和 closer 的前置条件；仍使用既有 `GPUReleaseReconciler` 的原事件及原 ACK 校验。ack/delivered_at 都有则已交付；一有一无或 scope 无 outbox 则持久冲突；均为空才可投递。未 seal 仍满足原 Closed/cleanup/NoDispatch/runtimeNeverMaterialized 条件。

## 5. 查询预算与真实性能边界

SQL 每次 3 秒，tenant page 16，row page 4；不会为得到 4 个精确 eligible 项在同一次调用内无界追加扫描。无 eligible 项的粗候选页同样消耗预算并推进。

上述方案使正常终态 CPU/GPU 不占 worker 候选配额，避免把所有已结束 CPU 永久交给 Go。它并不保证数据库只访问 pending 行：现有索引可能仍读取历史索引页并计算 predicate。首先保留主方案的 `(cluster_id, tenant_id, accepted_at, execution_id)` 索引；记录真实 `EXPLAIN (ANALYZE, BUFFERS)` 与正常终态数据量。只有真实计划表明有剩余历史页瓶颈时，再选择一个针对已证实分支的表达式/partial index；不因此新增调度表或复制第二份事实。

SQL 超时是这次查询失败；不能把它当空页重置游标。按主方案有限共享依赖退避继续，不能永久停止整个 close worker。一个 tenant 的网络失败不能使其他 tenant 的已发现候选停止。

## 6. 必须使用真实 PostgreSQL 的故障用例

以下是隔离测试数据库的用例，禁止直接篡改现场 execution 数据：

1. 候选 A 的 snapshot 为非法 UTF-8、合法 UTF-8 的坏 JSON、非 object JSON、超过 8 MiB、触发解析复杂度上限的输入；候选 B 正常。外层和内层均不因 A 抛输入异常；A 逐行失败且游标推进，B 被处理。
2. runtime 的 TrainingWindow/StepTimings/FinetuningSteps 中分别注入坏日期、错误数组形状、错误 attempts 类型。查询不 cast 失败；Go 精算失败隔离本行，不能提前关闭或退款。
3. 完整终态 CPU、NoDispatch CPU、完整 cleanup+ACK GPU 不占 row page；未 seal GPU、已 seal 未 ACK GPU、半 ACK GPU 均能发现。半 ACK 不发送、不自动修复。
4. 单 tenant 前 4 个都是未来 timing 的粗候选或坏行，第 5 个是到期关闭项；下一轮能到第 5 个。两个 tenant 中 A 一直失败，B 仍推进；新 accepted 行不延长当前 tenant 的 sweep。
5. 已 seal 有效 outbox 的 Namespace 已删、当前环境 BLOCKED、材料缓存无项，仍调用 Gov 原事件并保存原 ACK；未 seal 不能借此跳过原资格。
6. cleanup attempts=3 且非 STARTED 不重开自动预算；STARTED 恢复、NoDispatch、runtimeNeverMaterialized 与现有资格分别有正反用例。
7. SQL 超时/取消/连接故障不能被 helper 吞成空候选；模拟 worker 重启仍由原 Reserve/close fences 保证幂等，无第二次 Run 创建。

## 7. 代码依据

- ModelDev 固定 main `21bf4bd6540d462a61c91b629f26250899583223`：`migrations/0001_current_baseline.up.sql` 的 execution/identity/runtime 列；`internal/data/lifecycle/queries.sql:80–128` 的既有候选条件。
- 同提交 `internal/data/lifecycle/gpu_release.go:102–129` 的原 scope/outbox/半 ACK 校验，`242–254` 同事务记录 ACK+DeliveredAt；`internal/biz/gpu_release.go:104–123` 原投递顺序。
- 同提交 `contract/cpup01/snapshot.go:208–223` 的 8 MiB canonical 解码上限。

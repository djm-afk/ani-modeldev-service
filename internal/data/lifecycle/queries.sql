-- name: LockRuntimeIdentity :one
SELECT tenant_id, execution_id
FROM modeldev_execution_identities
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND execution_id = sqlc.arg(execution_id)::uuid
FOR UPDATE;

-- Called after the shared lock for writers, or in a repeatable-read snapshot.
-- name: GetRuntimeIdentity :one
SELECT close_generation, owner_revision,
    clock_timestamp()::timestamptz AS database_now
FROM modeldev_execution_identities
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND execution_id = sqlc.arg(execution_id)::uuid;

-- name: GetRuntimeFacts :one
SELECT facts FROM modeldev_execution_runtimes
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND execution_id = sqlc.arg(execution_id)::uuid;

-- name: GetGPUReleaseAnchor :one
SELECT gpu_release_sealed_at, gpu_release_scope_sha256
FROM modeldev_execution_runtimes
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND execution_id = sqlc.arg(execution_id)::uuid;

-- Called under the original identity lock, after durable close and cleanup.
-- Anchor and full JSON facts commit in this same first-seal statement.
-- name: SealGPUReleaseFacts :execrows
UPDATE modeldev_execution_runtimes
SET gpu_release_sealed_at = sqlc.arg(sealed_at)::timestamptz,
    gpu_release_scope_sha256 = sqlc.arg(scope_sha256)::text,
    facts = sqlc.arg(facts)::jsonb
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND execution_id = sqlc.arg(execution_id)::uuid
  AND gpu_release_sealed_at IS NULL AND gpu_release_scope_sha256 IS NULL
  AND closed_at IS NOT NULL;

-- The shared close fence and DB deadline are checked again in the statement
-- that persists a new workspace or training creation intent.
-- name: SaveRuntimeFacts :execrows
INSERT INTO modeldev_execution_runtimes (
    tenant_id, execution_id, operation_id, spec_hash, facts,
    training_name, training_request_sha256, training_uid, publication_id,
    close_generation, close_reason, close_requested_at, closed_at,
    gpu_release_sealed_at, gpu_release_scope_sha256
)
SELECT i.tenant_id, i.execution_id, i.operation_id, i.spec_hash, sqlc.arg(facts)::jsonb,
    sqlc.narg(training_name)::text, sqlc.narg(training_request_sha256)::text,
    sqlc.narg(training_uid)::text, sqlc.narg(publication_id)::uuid,
    sqlc.arg(close_generation)::numeric, sqlc.narg(close_reason)::text,
    sqlc.narg(close_requested_at)::timestamptz, sqlc.narg(closed_at)::timestamptz,
    original.gpu_release_sealed_at, original.gpu_release_scope_sha256
FROM modeldev_execution_identities AS i
LEFT JOIN modeldev_execution_runtimes AS original
  ON original.tenant_id = i.tenant_id AND original.execution_id = i.execution_id
WHERE i.tenant_id = sqlc.arg(tenant_id)::uuid AND i.execution_id = sqlc.arg(execution_id)::uuid
  AND (NOT sqlc.arg(require_creation_open)::boolean OR
       (i.close_generation = 0 AND clock_timestamp() < sqlc.arg(deadline_at)::timestamptz))
ON CONFLICT (tenant_id, execution_id) DO UPDATE SET
    facts = EXCLUDED.facts,
    training_name = EXCLUDED.training_name,
    training_request_sha256 = EXCLUDED.training_request_sha256,
    training_uid = EXCLUDED.training_uid,
    publication_id = EXCLUDED.publication_id,
    close_generation = EXCLUDED.close_generation,
    close_reason = EXCLUDED.close_reason,
    close_requested_at = EXCLUDED.close_requested_at,
    closed_at = EXCLUDED.closed_at;

-- name: AdvanceRuntimeRevision :one
UPDATE modeldev_execution_identities SET owner_revision = owner_revision + 1
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND execution_id = sqlc.arg(execution_id)::uuid
    AND owner_revision < 18446744073709551615
RETURNING owner_revision;

-- name: AdvanceRuntimeClose :one
UPDATE modeldev_execution_identities SET close_generation = close_generation + 1
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND execution_id = sqlc.arg(execution_id)::uuid
    AND close_generation < 18446744073709551615
RETURNING close_generation;

-- name: CloseDiscoveryTime :one
SELECT clock_timestamp()::timestamptz AS sweep_before;

-- name: ListPendingCloseTenants :many
SELECT DISTINCT a.tenant_id
FROM modeldev_executions AS a
JOIN modeldev_execution_identities AS i ON i.tenant_id=a.tenant_id AND i.execution_id=a.execution_id AND i.operation_id=a.operation_id AND i.spec_hash=a.spec_hash
LEFT JOIN modeldev_execution_runtimes AS r ON r.tenant_id=a.tenant_id AND r.execution_id=a.execution_id
WHERE a.cluster_id=sqlc.arg(cluster_id)::uuid AND (
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
 AND (sqlc.narg(after_tenant_id)::uuid IS NULL OR a.tenant_id>sqlc.narg(after_tenant_id)::uuid)
ORDER BY a.tenant_id LIMIT sqlc.arg(batch_size)::integer;

-- name: ListPendingClosures :many
SELECT a.*,i.close_generation,r.closed_at,r.facts,r.gpu_release_sealed_at
FROM modeldev_executions AS a
JOIN modeldev_execution_identities AS i ON i.tenant_id=a.tenant_id AND i.execution_id=a.execution_id AND i.operation_id=a.operation_id AND i.spec_hash=a.spec_hash
LEFT JOIN modeldev_execution_runtimes AS r ON r.tenant_id=a.tenant_id AND r.execution_id=a.execution_id
WHERE a.cluster_id=sqlc.arg(cluster_id)::uuid AND (
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
 AND a.tenant_id=sqlc.arg(tenant_id)::uuid AND a.accepted_at<=sqlc.arg(sweep_before)::timestamptz
 AND (sqlc.narg(after_execution_id)::uuid IS NULL OR (a.accepted_at,a.execution_id)>(sqlc.narg(after_accepted_at)::timestamptz,sqlc.narg(after_execution_id)::uuid))
ORDER BY a.accepted_at,a.execution_id LIMIT sqlc.arg(batch_size)::integer;

-- name: HasUnresolvedManualCleanup :one
SELECT EXISTS (
    SELECT 1 FROM modeldev_execution_cleanup_audits
    WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND execution_id = sqlc.arg(execution_id)::uuid
      AND phase IN ('STARTED', 'NEEDS_REVIEW')
) AS blocked;

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

-- The shared close fence and DB deadline are checked again in the statement
-- that persists a new workspace or training creation intent.
-- name: SaveRuntimeFacts :execrows
INSERT INTO modeldev_execution_runtimes (
    tenant_id, execution_id, operation_id, spec_hash, facts,
    training_name, training_request_sha256, training_uid, publication_id,
    close_generation, close_reason, close_requested_at, closed_at
)
SELECT i.tenant_id, i.execution_id, i.operation_id, i.spec_hash, sqlc.arg(facts)::jsonb,
    sqlc.narg(training_name)::text, sqlc.narg(training_request_sha256)::text,
    sqlc.narg(training_uid)::text, sqlc.narg(publication_id)::uuid,
    sqlc.arg(close_generation)::numeric, sqlc.narg(close_reason)::text,
    sqlc.narg(close_requested_at)::timestamptz, sqlc.narg(closed_at)::timestamptz
FROM modeldev_execution_identities AS i
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

-- The fixed 0012 migration reader predates the runtime schema. Check before
-- querying it; an undefined-table error would abort the aggregate transaction.
-- name: RuntimeSchemaAvailable :one
SELECT to_regclass('modeldev_execution_runtimes') IS NOT NULL AS available;

-- Discovery only. Reconcile rereads the shared fence and original submission.
-- Include admission delivered after a stop tombstone, before any Run exists.
-- name: ListPendingBoundClosures :many
SELECT admitted.execution_id
FROM modeldev_executions AS admitted
JOIN modeldev_execution_identities AS identity
  ON identity.tenant_id = admitted.tenant_id
 AND identity.execution_id = admitted.execution_id
LEFT JOIN modeldev_execution_runtimes AS runtime
  ON runtime.tenant_id = admitted.tenant_id
 AND runtime.execution_id = admitted.execution_id
WHERE admitted.tenant_id = sqlc.arg(tenant_id)::uuid
  AND (convert_from(admitted.snapshot_canonical, 'UTF8')::jsonb -> 'environment') = sqlc.arg(environment)::jsonb
  AND ((runtime.closed_at IS NULL AND
        (identity.close_generation > 0 OR
         (convert_from(admitted.snapshot_canonical, 'UTF8')::jsonb ->> 'deadline_at')::timestamptz <= clock_timestamp()))
       OR (runtime.closed_at IS NOT NULL
           AND COALESCE(runtime.facts -> 'CloseEvidence' ->> 'NoDispatch', 'false') <> 'true'
           AND runtime.facts -> 'StepExitCleanup' ->> 'CompletedAt' IS NULL))
  AND (sqlc.narg(after_execution_id)::uuid IS NULL OR admitted.execution_id > sqlc.narg(after_execution_id)::uuid)
ORDER BY admitted.execution_id
LIMIT sqlc.arg(batch_size)::integer;

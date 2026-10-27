-- name: InsertExecution :one
INSERT INTO modeldev_executions (
    tenant_id, execution_id, operation_id, actor,
    intent_canonical, intent_hash, snapshot_canonical, spec_hash, accepted_at
) VALUES (
    sqlc.arg(tenant_id)::uuid, sqlc.arg(execution_id)::uuid,
    sqlc.arg(operation_id)::uuid, sqlc.arg(actor),
    sqlc.arg(intent_canonical), sqlc.arg(intent_hash),
    sqlc.arg(snapshot_canonical), sqlc.arg(spec_hash), sqlc.arg(accepted_at)
)
ON CONFLICT DO NOTHING
RETURNING tenant_id, execution_id, operation_id, actor,
    intent_canonical, intent_hash, snapshot_canonical, spec_hash, accepted_at;

-- name: GetExecution :one
SELECT tenant_id, execution_id, operation_id, actor,
    intent_canonical, intent_hash, snapshot_canonical, spec_hash, accepted_at
FROM modeldev_executions
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND execution_id = sqlc.arg(execution_id)::uuid;

-- name: InsertExecutionIdentity :exec
INSERT INTO modeldev_execution_identities (tenant_id, execution_id, operation_id, spec_hash)
VALUES (
    sqlc.arg(tenant_id)::uuid, sqlc.arg(execution_id)::uuid,
    sqlc.arg(operation_id)::uuid, sqlc.arg(spec_hash)
)
ON CONFLICT DO NOTHING;

-- The tenant filter precedes returning any artifact association. Two matches
-- reject a duplicated artifact identity instead of choosing an arbitrary Run.
-- name: FindPublishedArtifactExecution :many
SELECT execution_id
FROM modeldev_execution_runtimes
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND publication_id IS NOT NULL
  AND (facts->'Publication'->'Files') @>
      jsonb_build_array(jsonb_build_object('ArtifactID', sqlc.arg(artifact_id)::text))
LIMIT 2;

-- A bounded scan within one tenant and snapshot. State filters use the same
-- validated aggregate projection as GetExecution before filling a page.
-- The walk follows acceptance time, newest first, so the first page of a list
-- is the most recent work. execution_id breaks ties; two rows admitted in the
-- same microsecond still have one stable order. A NULL cursor starts the walk.
-- name: ListTenantExecutionIDs :many
SELECT execution_id, accepted_at
FROM modeldev_executions
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND (
      sqlc.narg(after_accepted_at)::timestamptz IS NULL
      OR (accepted_at, execution_id) < (sqlc.narg(after_accepted_at)::timestamptz, sqlc.narg(after_execution_id)::uuid)
  )
ORDER BY accepted_at DESC, execution_id DESC
LIMIT 128;

-- name: LockExecutionIdentity :one
SELECT tenant_id, execution_id, operation_id, spec_hash, close_generation, owner_revision
FROM modeldev_execution_identities
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND execution_id = sqlc.arg(execution_id)::uuid
FOR UPDATE;

-- Read in the identity-locked writer or the aggregate's read-only snapshot.
-- name: GetOwnerRevision :one
SELECT owner_revision
FROM modeldev_execution_identities
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND execution_id = sqlc.arg(execution_id)::uuid;

-- Only a new fact advances the version. Saturation rejects the entire writer
-- transaction; a replay uses GetOwnerRevision and remains readable at Max.
-- name: AdvanceOwnerRevision :one
UPDATE modeldev_execution_identities
SET owner_revision = owner_revision + 1
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND execution_id = sqlc.arg(execution_id)::uuid
  AND owner_revision < 18446744073709551615
RETURNING owner_revision;

-- name: AdvanceCloseGeneration :one
UPDATE modeldev_execution_identities
SET close_generation = close_generation + 1
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND execution_id = sqlc.arg(execution_id)::uuid
RETURNING close_generation;

-- name: InsertCloseIntent :one
INSERT INTO modeldev_close_intents (
    tenant_id, execution_id, operation_id, spec_hash, source_kind,
    source_generation, owner_generation, reason, requested_at, requested_actor, close_state
) VALUES (
    sqlc.arg(tenant_id)::uuid, sqlc.arg(execution_id)::uuid,
    sqlc.arg(operation_id)::uuid, sqlc.arg(spec_hash), 'GOVERNANCE',
    sqlc.arg(source_generation), sqlc.arg(owner_generation), 'USER_STOP',
    sqlc.arg(requested_at), sqlc.arg(requested_actor), 'CLOSING'
)
RETURNING tenant_id, execution_id, operation_id, spec_hash, source_kind,
    source_generation, owner_generation, reason, requested_at, requested_actor, close_state;

-- name: GetCloseIntent :one
SELECT tenant_id, execution_id, operation_id, spec_hash, source_kind,
    source_generation, owner_generation, reason, requested_at, requested_actor, close_state
FROM modeldev_close_intents
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND execution_id = sqlc.arg(execution_id)::uuid
ORDER BY owner_generation DESC
LIMIT 1;

-- name: GetCloseIntentBySource :one
SELECT tenant_id, execution_id, operation_id, spec_hash, source_kind,
    source_generation, owner_generation, reason, requested_at, requested_actor, close_state
FROM modeldev_close_intents
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND execution_id = sqlc.arg(execution_id)::uuid
  AND source_kind = 'GOVERNANCE'
  AND source_generation = sqlc.arg(source_generation);

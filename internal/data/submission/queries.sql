-- name: LockExecutionIdentity :one
SELECT tenant_id, execution_id, operation_id, spec_hash, owner_revision,
    close_generation = 0 AS creation_open
FROM modeldev_execution_identities
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND execution_id = sqlc.arg(execution_id)::uuid
FOR UPDATE;

-- Read under the shared identity lock or in the existing repeatable-read Get.
-- name: GetOwnerRevision :one
SELECT owner_revision
FROM modeldev_execution_identities
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND execution_id = sqlc.arg(execution_id)::uuid;

-- Advance once for new facts, even when their summary state does not change.
-- Replays never use this query and remain readable when the version is Max.
-- name: AdvanceOwnerRevision :one
UPDATE modeldev_execution_identities
SET owner_revision = owner_revision + 1
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND execution_id = sqlc.arg(execution_id)::uuid
  AND owner_revision < 18446744073709551615
RETURNING owner_revision;

-- name: GetAdmission :one
SELECT *
FROM modeldev_executions
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND execution_id = sqlc.arg(execution_id)::uuid;

-- name: GetPipelineDispatch :one
SELECT tenant_id, execution_id, operation_id, spec_hash, attempt_id,
    plan_canonical, plan_hash, state, reserved_at, uncertain_at, not_sent_at
FROM modeldev_pipeline_dispatches
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND execution_id = sqlc.arg(execution_id)::uuid;

-- Sample this database's clock after the outbound call. The adapter compares
-- the returned immutable attempt/hash with the original permit and requires
-- observed_at >= reserved_at. Keeping the tenant-scoped row distinguishes a
-- missing reservation from a conflicting attempt without a second lookup.
-- This read neither authorizes a send nor locks across network work.
-- name: GetSubmissionObservationTime :one
SELECT attempt_id, plan_hash, reserved_at,
    clock_timestamp()::timestamptz AS observed_at
FROM modeldev_pipeline_dispatches
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND execution_id = sqlc.arg(execution_id)::uuid;

-- The caller holds this identity's row lock and has compared the complete
-- admitted command. Check the current database clock after acquiring the lock;
-- transaction-start time could predate a long wait behind a close transaction.
-- name: InsertPipelineDispatch :one
INSERT INTO modeldev_pipeline_dispatches (
    tenant_id, execution_id, operation_id, spec_hash, attempt_id,
    plan_canonical, plan_hash, state, reserved_at
)
SELECT tenant_id, execution_id, operation_id, spec_hash,
    sqlc.arg(attempt_id)::uuid, sqlc.arg(plan_canonical)::bytea,
    sqlc.arg(plan_hash)::text, 'SUBMITTING', clock_timestamp()
FROM modeldev_execution_identities
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND execution_id = sqlc.arg(execution_id)::uuid
  AND operation_id = sqlc.arg(operation_id)::uuid
  AND spec_hash = sqlc.arg(spec_hash)::text
  AND close_generation = 0
  AND clock_timestamp() < sqlc.arg(deadline_at)::timestamptz
RETURNING tenant_id, execution_id, operation_id, spec_hash, attempt_id,
    plan_canonical, plan_hash, state, reserved_at, uncertain_at, not_sent_at;

-- Call under the identity lock or in a repeatable-read aggregate snapshot.
-- The joined revision is current; it is not the version at first binding.
-- name: GetRunAuthorityRow :one
SELECT authority.tenant_id, authority.execution_id, authority.operation_id,
    authority.spec_hash, authority.attempt_id, authority.plan_hash,
    authority.run_id, authority.namespace_name, authority.namespace_uid,
    authority.workflow_name, authority.workflow_uid, authority.bound_at,
    identity.owner_revision
FROM modeldev_run_authorities AS authority
JOIN modeldev_execution_identities AS identity
  ON identity.tenant_id = authority.tenant_id
 AND identity.execution_id = authority.execution_id
 AND identity.operation_id = authority.operation_id
 AND identity.spec_hash = authority.spec_hash
WHERE authority.tenant_id = sqlc.arg(tenant_id)::uuid
  AND authority.execution_id = sqlc.arg(execution_id)::uuid;

-- Caller holds the shared identity lock and has checked the complete original
-- plan and admission. Sample the database clock after that lock, once, for both
-- eligibility and bound_at. No confirmed-response observation substitutes for
-- the authenticated upstream association supplied to the domain adapter.
-- name: InsertRunAuthority :execrows
WITH binding_clock AS MATERIALIZED (
    SELECT clock_timestamp()::timestamptz AS bound_at
)
INSERT INTO modeldev_run_authorities (
    tenant_id, execution_id, operation_id, spec_hash, attempt_id, plan_hash,
    run_id, namespace_name, namespace_uid, workflow_name, workflow_uid, bound_at
)
SELECT identity.tenant_id, identity.execution_id, identity.operation_id,
    identity.spec_hash, dispatch.attempt_id, dispatch.plan_hash,
    sqlc.arg(run_id)::uuid, sqlc.arg(namespace_name)::text,
    sqlc.arg(namespace_uid)::uuid, sqlc.arg(workflow_name)::text,
    sqlc.arg(workflow_uid)::text, binding_clock.bound_at
FROM modeldev_execution_identities AS identity
JOIN modeldev_pipeline_dispatches AS dispatch
  ON dispatch.tenant_id = identity.tenant_id
 AND dispatch.execution_id = identity.execution_id
 AND dispatch.operation_id = identity.operation_id
 AND dispatch.spec_hash = identity.spec_hash
CROSS JOIN binding_clock
WHERE identity.tenant_id = sqlc.arg(tenant_id)::uuid
  AND identity.execution_id = sqlc.arg(execution_id)::uuid
  AND identity.operation_id = sqlc.arg(operation_id)::uuid
  AND identity.spec_hash = sqlc.arg(spec_hash)::text
  AND dispatch.attempt_id = sqlc.arg(attempt_id)::uuid
  AND dispatch.plan_hash = sqlc.arg(plan_hash)::text
  AND identity.close_generation = 0
  AND binding_clock.bound_at >= dispatch.reserved_at
  AND binding_clock.bound_at < sqlc.arg(deadline_at)::timestamptz;

-- These immutable observations belong to one exact reservation, not to an
-- authoritative Run binding. Ordering gives stable output, never priority.
-- Readers combining this list with dispatch state must use one consistent
-- snapshot; writers hold the shared execution identity lock.
-- name: ListConfirmedPipelineRuns :many
SELECT tenant_id, execution_id, attempt_id, plan_hash, run_id, first_observed_at
FROM modeldev_pipeline_confirmed_runs
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND execution_id = sqlc.arg(execution_id)::uuid
  AND attempt_id = sqlc.arg(attempt_id)::uuid
  AND plan_hash = sqlc.arg(plan_hash)::text
ORDER BY run_id;

-- The caller has matched the original permit and complete frozen plan under
-- the identity lock. A late observation survives close/deadline. Replaying the
-- same Run never refreshes its first time; another Run is retained separately.
-- name: InsertConfirmedPipelineRun :execrows
INSERT INTO modeldev_pipeline_confirmed_runs (
    tenant_id, execution_id, attempt_id, plan_hash, run_id, first_observed_at
)
SELECT tenant_id, execution_id, attempt_id, plan_hash,
    sqlc.arg(run_id)::uuid, sqlc.arg(observed_at)::timestamptz
FROM modeldev_pipeline_dispatches
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND execution_id = sqlc.arg(execution_id)::uuid
  AND attempt_id = sqlc.arg(attempt_id)::uuid
  AND plan_hash = sqlc.arg(plan_hash)::text
  AND state IN ('SUBMITTING', 'SUBMISSION_NOT_SENT', 'SUBMISSION_UNCERTAIN', 'SUBMISSION_CONFIRMED')
  AND sqlc.arg(observed_at)::timestamptz >= reserved_at
ON CONFLICT (tenant_id, execution_id, attempt_id, run_id) DO NOTHING;

-- In the same transaction as the retained handle, mark the original attempt's
-- first confirmation without erasing earlier no-send/uncertainty observations.
-- name: MarkSubmissionConfirmed :one
UPDATE modeldev_pipeline_dispatches AS dispatch
SET state = 'SUBMISSION_CONFIRMED'
WHERE dispatch.tenant_id = sqlc.arg(tenant_id)::uuid
  AND dispatch.execution_id = sqlc.arg(execution_id)::uuid
  AND dispatch.attempt_id = sqlc.arg(attempt_id)::uuid
  AND dispatch.plan_hash = sqlc.arg(plan_hash)::text
  AND dispatch.state IN ('SUBMITTING', 'SUBMISSION_NOT_SENT', 'SUBMISSION_UNCERTAIN')
  AND EXISTS (
      SELECT 1
      FROM modeldev_pipeline_confirmed_runs AS observed
      WHERE observed.tenant_id = dispatch.tenant_id
        AND observed.execution_id = dispatch.execution_id
        AND observed.attempt_id = dispatch.attempt_id
        AND observed.plan_hash = dispatch.plan_hash
  )
RETURNING dispatch.tenant_id, dispatch.execution_id, dispatch.operation_id,
    dispatch.spec_hash, dispatch.attempt_id, dispatch.plan_canonical,
    dispatch.plan_hash, dispatch.state, dispatch.reserved_at, dispatch.uncertain_at, dispatch.not_sent_at;

-- The caller holds the shared identity lock and has matched the original
-- attempt and frozen plan. Close/deadline do not discard late observations.
-- An already uncertain row is read back unchanged rather than updated again.
-- name: MarkSubmissionUncertain :one
UPDATE modeldev_pipeline_dispatches
SET state = 'SUBMISSION_UNCERTAIN', uncertain_at = sqlc.arg(observed_at)::timestamptz
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND execution_id = sqlc.arg(execution_id)::uuid
  AND attempt_id = sqlc.arg(attempt_id)::uuid
  AND plan_hash = sqlc.arg(plan_hash)::text
  AND state IN ('SUBMITTING', 'SUBMISSION_NOT_SENT')
  AND uncertain_at IS NULL
  AND sqlc.arg(observed_at)::timestamptz >= reserved_at
RETURNING tenant_id, execution_id, operation_id, spec_hash, attempt_id,
    plan_canonical, plan_hash, state, reserved_at, uncertain_at, not_sent_at;

-- Keep the first local no-send observation even when a stronger observation
-- arrived first. Existing uncertainty/confirmation and all Run handles remain.
-- The caller holds the original identity lock and has matched the frozen plan.
-- name: MarkSubmissionNotSent :one
UPDATE modeldev_pipeline_dispatches
SET not_sent_at = sqlc.arg(observed_at)::timestamptz,
    state = CASE WHEN state = 'SUBMITTING' THEN 'SUBMISSION_NOT_SENT' ELSE state END
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND execution_id = sqlc.arg(execution_id)::uuid
  AND attempt_id = sqlc.arg(attempt_id)::uuid
  AND plan_hash = sqlc.arg(plan_hash)::text
  AND state IN ('SUBMITTING', 'SUBMISSION_NOT_SENT', 'SUBMISSION_UNCERTAIN', 'SUBMISSION_CONFIRMED')
  AND not_sent_at IS NULL
  AND sqlc.arg(observed_at)::timestamptz >= reserved_at
RETURNING tenant_id, execution_id, operation_id, spec_hash, attempt_id,
    plan_canonical, plan_hash, state, reserved_at, uncertain_at, not_sent_at;

-- Discovery grants no sending permission. Reserve is still authoritative.
-- name: ListPendingDispatchTenants :many
SELECT DISTINCT a.tenant_id
FROM modeldev_executions AS a
JOIN modeldev_execution_identities AS i ON i.tenant_id=a.tenant_id AND i.execution_id=a.execution_id AND i.operation_id=a.operation_id AND i.spec_hash=a.spec_hash
WHERE a.cluster_id=sqlc.arg(cluster_id)::uuid AND i.close_generation=0
 AND a.deadline_at>statement_timestamp()
 AND NOT EXISTS(SELECT 1 FROM modeldev_pipeline_dispatches d WHERE d.tenant_id=a.tenant_id AND d.execution_id=a.execution_id)
 AND (sqlc.narg(after_tenant_id)::uuid IS NULL OR a.tenant_id>sqlc.narg(after_tenant_id)::uuid)
ORDER BY a.tenant_id LIMIT sqlc.arg(batch_size)::integer;

-- name: DispatchDiscoveryTime :one
SELECT clock_timestamp()::timestamptz AS sweep_before;

-- name: ListPendingAdmissions :many
SELECT a.*
FROM modeldev_executions AS a
JOIN modeldev_execution_identities AS i ON i.tenant_id=a.tenant_id AND i.execution_id=a.execution_id AND i.operation_id=a.operation_id AND i.spec_hash=a.spec_hash
WHERE a.cluster_id=sqlc.arg(cluster_id)::uuid AND i.close_generation=0
 AND a.deadline_at>statement_timestamp()
 AND NOT EXISTS(SELECT 1 FROM modeldev_pipeline_dispatches d WHERE d.tenant_id=a.tenant_id AND d.execution_id=a.execution_id)
 AND a.tenant_id=sqlc.arg(tenant_id)::uuid AND a.accepted_at<=sqlc.arg(sweep_before)::timestamptz
 AND (sqlc.narg(after_execution_id)::uuid IS NULL OR (a.accepted_at,a.execution_id)>(sqlc.narg(after_accepted_at)::timestamptz,sqlc.narg(after_execution_id)::uuid))
ORDER BY a.accepted_at,a.execution_id LIMIT sqlc.arg(batch_size)::integer;

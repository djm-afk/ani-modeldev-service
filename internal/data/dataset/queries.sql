-- name: InsertDataset :one
INSERT INTO modeldev_datasets (
    tenant_id, dataset_id, name, description, created_by, updated_by
) VALUES (
    sqlc.arg(tenant_id)::uuid, sqlc.arg(dataset_id)::uuid, sqlc.arg(name),
    sqlc.arg(description), sqlc.arg(created_by), sqlc.arg(updated_by)
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetDataset :one
SELECT * FROM modeldev_datasets
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND dataset_id = sqlc.arg(dataset_id)::uuid;

-- name: LockDataset :one
SELECT * FROM modeldev_datasets
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND dataset_id = sqlc.arg(dataset_id)::uuid
FOR UPDATE;

-- name: LockDatasetShare :one
SELECT * FROM modeldev_datasets
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND dataset_id = sqlc.arg(dataset_id)::uuid
FOR SHARE;

-- name: DatasetNameTaken :one
SELECT dataset_id::text FROM modeldev_datasets
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND name = sqlc.arg(name);

-- name: UpdateDatasetMetadata :one
-- Revision is compared, not trusted: an update that loses the race writes
-- nothing and the caller reports a revision conflict.
UPDATE modeldev_datasets
SET name = sqlc.arg(name),
    description = sqlc.arg(description),
    updated_by = sqlc.arg(updated_by),
    revision = revision + 1,
    updated_at = clock_timestamp()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND dataset_id = sqlc.arg(dataset_id)::uuid
  AND revision = sqlc.arg(expected_revision)
RETURNING *;

-- name: ArchiveDataset :one
UPDATE modeldev_datasets
SET state = 'ARCHIVED',
    archived_at = clock_timestamp(),
    updated_by = sqlc.arg(updated_by),
    revision = revision + 1,
    updated_at = clock_timestamp()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND dataset_id = sqlc.arg(dataset_id)::uuid
  AND revision = sqlc.arg(expected_revision)
  AND state = 'ACTIVE'
RETURNING *;

-- name: RestoreDataset :one
UPDATE modeldev_datasets
SET state = 'ACTIVE',
    archived_at = NULL,
    updated_by = sqlc.arg(updated_by),
    revision = revision + 1,
    updated_at = clock_timestamp()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND dataset_id = sqlc.arg(dataset_id)::uuid
  AND revision = sqlc.arg(expected_revision)
  AND state = 'ARCHIVED'
RETURNING *;

-- name: TakeVersionNumber :one
-- The version sequence advances in the acceptance transaction; a failed or
-- cancelled task leaves a permanent gap and never recycles the number.
UPDATE modeldev_datasets
SET next_version_number = next_version_number + 1
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND dataset_id = sqlc.arg(dataset_id)::uuid
  AND state = 'ACTIVE'
RETURNING (next_version_number - 1)::bigint AS version_number;

-- name: ListDatasets :many
SELECT * FROM modeldev_datasets
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND (sqlc.arg(state)::text = '' OR state = sqlc.arg(state)::text)
  AND (sqlc.arg(name_filter)::text = '' OR position(sqlc.arg(name_filter)::text IN name) > 0)
ORDER BY updated_at DESC, dataset_id DESC
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: CountDatasets :one
SELECT count(*) FROM modeldev_datasets
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND (sqlc.arg(state)::text = '' OR state = sqlc.arg(state)::text)
  AND (sqlc.arg(name_filter)::text = '' OR position(sqlc.arg(name_filter)::text IN name) > 0);

-- name: InsertCommandReceipt :one
INSERT INTO modeldev_dataset_command_receipts (
    tenant_id, receipt_id, actor_id, operation_kind, idempotency_key,
    request_sha256, canonical_request, dataset_id, task_id, response_status, receipt, accepted_at
) VALUES (
    sqlc.arg(tenant_id)::uuid, sqlc.arg(receipt_id)::uuid, sqlc.arg(actor_id),
    sqlc.arg(operation_kind), sqlc.arg(idempotency_key), sqlc.arg(request_sha256),
    sqlc.arg(canonical_request)::jsonb, sqlc.arg(dataset_id)::uuid,
    sqlc.narg(task_id)::uuid, sqlc.arg(response_status)::int,
    sqlc.arg(receipt)::jsonb, sqlc.arg(accepted_at)
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetCommandReceipt :one
SELECT * FROM modeldev_dataset_command_receipts
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND actor_id = sqlc.arg(actor_id)
  AND operation_kind = sqlc.arg(operation_kind)
  AND idempotency_key = sqlc.arg(idempotency_key);

-- name: InsertImportTask :one
INSERT INTO modeldev_dataset_import_tasks (
    tenant_id, task_id, dataset_id, acceptance_receipt_id, reserved_input_version_id,
    input_request_id, version_number, created_by, accepted_at,
    source_provider, source_snapshot, source_snapshot_sha256,
    expected_size_bytes, accepted_limit_bytes,
    cluster_id, namespace_name, namespace_uid, target_snapshot, target_snapshot_sha256,
    storage_connection_id, bucket, approved_prefix, input_scope_sha256, credential_reference,
    authorization_scope, authorization_scope_sha256, recovery_policy
) VALUES (
    sqlc.arg(tenant_id)::uuid, sqlc.arg(task_id)::uuid, sqlc.arg(dataset_id)::uuid,
    sqlc.arg(acceptance_receipt_id)::uuid, sqlc.arg(reserved_input_version_id)::uuid,
    sqlc.arg(input_request_id)::uuid, sqlc.arg(version_number)::bigint,
    sqlc.arg(created_by), sqlc.arg(accepted_at),
    sqlc.arg(source_provider), sqlc.arg(source_snapshot)::jsonb, sqlc.arg(source_snapshot_sha256),
    sqlc.arg(expected_size_bytes)::bigint, sqlc.arg(accepted_limit_bytes)::bigint,
    sqlc.arg(cluster_id)::uuid, sqlc.arg(namespace_name), sqlc.arg(namespace_uid)::uuid,
    sqlc.arg(target_snapshot)::jsonb, sqlc.arg(target_snapshot_sha256),
    sqlc.arg(storage_connection_id), sqlc.arg(bucket), sqlc.arg(approved_prefix),
    sqlc.arg(input_scope_sha256), sqlc.arg(credential_reference),
    sqlc.arg(authorization_scope)::jsonb, sqlc.arg(authorization_scope_sha256),
    sqlc.arg(recovery_policy)::jsonb
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetImportTask :one
SELECT * FROM modeldev_dataset_import_tasks
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND task_id = sqlc.arg(task_id)::uuid;

-- name: LockImportTask :one
SELECT * FROM modeldev_dataset_import_tasks
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND task_id = sqlc.arg(task_id)::uuid
FOR UPDATE;

-- name: LockImportTasksByDataset :many
-- UUID order keeps concurrent archivals from deadlocking each other.
SELECT * FROM modeldev_dataset_import_tasks
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND dataset_id = sqlc.arg(dataset_id)::uuid
  AND state IN ('QUEUED', 'RUNNING', 'FAILED', 'CANCELING')
ORDER BY task_id
FOR UPDATE;

-- name: ListImportTasks :many
SELECT * FROM modeldev_dataset_import_tasks
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND dataset_id = sqlc.arg(dataset_id)::uuid
  AND (sqlc.arg(state)::text = '' OR state = sqlc.arg(state)::text)
ORDER BY accepted_at DESC, task_id DESC
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: CountImportTasks :one
SELECT count(*) FROM modeldev_dataset_import_tasks
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND dataset_id = sqlc.arg(dataset_id)::uuid
  AND (sqlc.arg(state)::text = '' OR state = sqlc.arg(state)::text);

-- name: RequestCancelTask :one
UPDATE modeldev_dataset_import_tasks
SET state = 'CANCELING',
    phase = 'STOPPING',
    cancel_requested_at = clock_timestamp(),
    lease_owner = NULL,
    lease_until = NULL,
    updated_at = clock_timestamp()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND task_id = sqlc.arg(task_id)::uuid
  AND state IN ('QUEUED', 'RUNNING')
RETURNING *;

-- name: FinishCancelledTask :one
UPDATE modeldev_dataset_import_tasks
SET state = 'CANCELLED',
    phase = 'STOPPING',
    finished_at = clock_timestamp(),
    lease_owner = NULL,
    lease_until = NULL,
    updated_at = clock_timestamp()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND task_id = sqlc.arg(task_id)::uuid
  AND state = 'CANCELING'
RETURNING *;

-- name: RequeueRetryableTask :one
-- A retry keeps the source, size, accepted limit, reserved ids and target: only
-- the recovery generation and the cycle's budget advance.
UPDATE modeldev_dataset_import_tasks
SET state = 'QUEUED',
    phase = 'WAITING',
    recovery_generation = recovery_generation + 1,
    validation_retry_count = 0,
    validation_deadline_at = NULL,
    failure_code = NULL,
    failure_detail = '{}'::jsonb,
    retryable = false,
    next_run_at = clock_timestamp(),
    finished_at = NULL,
    cancel_requested_at = NULL,
    updated_at = clock_timestamp()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND task_id = sqlc.arg(task_id)::uuid
  AND state = 'FAILED'
  AND retryable = true
  AND cancel_requested_at IS NULL
RETURNING *;

-- name: MarkTaskSucceeded :one
UPDATE modeldev_dataset_import_tasks
SET state = 'SUCCEEDED',
    phase = 'PUBLISHING',
    finished_at = clock_timestamp(),
    published_at = clock_timestamp(),
    lease_owner = NULL,
    lease_until = NULL,
    updated_at = clock_timestamp()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND task_id = sqlc.arg(task_id)::uuid
  AND state = 'RUNNING'
  AND cancel_requested_at IS NULL
RETURNING *;

-- name: InsertVersionMetadata :one
INSERT INTO modeldev_dataset_version_metadata (
    tenant_id, input_version_id, dataset_id, import_task_id, version_number, preview, published_at
) VALUES (
    sqlc.arg(tenant_id)::uuid, sqlc.arg(input_version_id)::uuid, sqlc.arg(dataset_id)::uuid,
    sqlc.arg(import_task_id)::uuid, sqlc.arg(version_number)::bigint,
    sqlc.arg(preview)::jsonb, sqlc.arg(published_at)
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetVersionMetadata :one
SELECT * FROM modeldev_dataset_version_metadata
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND input_version_id = sqlc.arg(input_version_id)::uuid;

-- name: ListVersionMetadata :many
SELECT * FROM modeldev_dataset_version_metadata
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND dataset_id = sqlc.arg(dataset_id)::uuid
ORDER BY version_number DESC, input_version_id DESC
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: CountVersionMetadata :one
SELECT count(*) FROM modeldev_dataset_version_metadata
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND dataset_id = sqlc.arg(dataset_id)::uuid;

-- name: InsertDatasetOperation :one
INSERT INTO modeldev_dataset_operations (
    tenant_id, operation_id, dataset_id, task_id, attempt_id, receipt_id, actor,
    event_type, result, before_state, after_state, before_revision, after_revision,
    recovery_generation, failure_code, detail
) VALUES (
    sqlc.arg(tenant_id)::uuid, sqlc.arg(operation_id)::uuid, sqlc.arg(dataset_id)::uuid,
    sqlc.narg(task_id)::uuid, sqlc.narg(attempt_id)::uuid, sqlc.narg(receipt_id)::uuid,
    sqlc.arg(actor), sqlc.arg(event_type), sqlc.arg(result),
    sqlc.arg(before_state), sqlc.arg(after_state),
    sqlc.narg(before_revision)::bigint, sqlc.narg(after_revision)::bigint,
    sqlc.narg(recovery_generation)::bigint, sqlc.narg(failure_code),
    sqlc.arg(detail)::jsonb
)
RETURNING *;

-- name: ListDatasetOperations :many
SELECT * FROM modeldev_dataset_operations
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND dataset_id = sqlc.arg(dataset_id)::uuid
  AND (sqlc.arg(event_type)::text = '' OR event_type = sqlc.arg(event_type)::text)
  AND (sqlc.narg(task_id)::uuid IS NULL OR task_id = sqlc.narg(task_id)::uuid)
ORDER BY occurred_at DESC, operation_id DESC
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: CountDatasetOperations :one
SELECT count(*) FROM modeldev_dataset_operations
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND dataset_id = sqlc.arg(dataset_id)::uuid
  AND (sqlc.arg(event_type)::text = '' OR event_type = sqlc.arg(event_type)::text)
  AND (sqlc.narg(task_id)::uuid IS NULL OR task_id = sqlc.narg(task_id)::uuid);

-- name: LockInputIdentity :exec
SELECT modeldev_lock_input_identity(
    sqlc.arg(tenant_id)::uuid, sqlc.arg(input_version_id)::uuid, sqlc.arg(request_id)::uuid
);

-- name: LockTargetCommit :exec
SELECT modeldev_lock_dataset_target_commit(
    sqlc.arg(tenant_id)::uuid, sqlc.arg(cluster_id)::uuid
);

-- name: ListDueTasks :many
SELECT * FROM modeldev_dataset_import_tasks
WHERE state IN ('QUEUED','RUNNING','CANCELING')
  AND next_run_at <= sqlc.arg(now)
  AND (lease_until IS NULL OR lease_until < sqlc.arg(now))
ORDER BY next_run_at, accepted_at, tenant_id, task_id
LIMIT sqlc.arg(batch_size);

-- name: AcquireTaskLease :one
-- The lease is granted only when it is free or already ours. The epoch advances
-- on every hand-over so a stale owner cannot act on the new lease.
UPDATE modeldev_dataset_import_tasks
SET lease_owner = sqlc.arg(owner),
    lease_epoch = CASE WHEN lease_owner = sqlc.arg(owner) THEN lease_epoch ELSE lease_epoch + 1 END,
    lease_until = clock_timestamp() + sqlc.arg(ttl)::interval,
    updated_at = clock_timestamp()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND task_id = sqlc.arg(task_id)::uuid
  AND state IN ('QUEUED','RUNNING','CANCELING')
  AND (lease_owner IS NULL OR lease_owner = sqlc.arg(owner) OR lease_until < clock_timestamp())
RETURNING *;

-- name: ReleaseTaskLease :exec
UPDATE modeldev_dataset_import_tasks
SET lease_owner = NULL, lease_until = NULL, updated_at = clock_timestamp()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND task_id = sqlc.arg(task_id)::uuid
  AND lease_owner = sqlc.arg(owner)
  AND lease_epoch = sqlc.arg(epoch);

-- name: CountRunningAttempts :one
SELECT count(*) FROM modeldev_dataset_import_attempts
WHERE cleanup_state NOT IN ('DONE')
  AND dispatch_closed_at IS NULL;

-- name: InsertAttempt :one
INSERT INTO modeldev_dataset_import_attempts (
    tenant_id, attempt_id, dataset_id, task_id, attempt_no, recovery_generation,
    cluster_id, namespace_name, namespace_uid,
    job_name, pvc_name, secret_name, runtime_plan, runtime_plan_sha256,
    data_object_key, result_object_key, deadline_at
) VALUES (
    sqlc.arg(tenant_id)::uuid, sqlc.arg(attempt_id)::uuid, sqlc.arg(dataset_id)::uuid,
    sqlc.arg(task_id)::uuid, sqlc.arg(attempt_no)::bigint, sqlc.arg(recovery_generation)::bigint,
    sqlc.arg(cluster_id)::uuid, sqlc.arg(namespace_name), sqlc.arg(namespace_uid)::uuid,
    sqlc.arg(job_name), sqlc.arg(pvc_name), sqlc.arg(secret_name),
    sqlc.arg(runtime_plan)::jsonb, sqlc.arg(runtime_plan_sha256),
    sqlc.arg(data_object_key), sqlc.arg(result_object_key), sqlc.arg(deadline_at)
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: SetCurrentAttempt :one
UPDATE modeldev_dataset_import_tasks
SET current_attempt_id = sqlc.arg(attempt_id)::uuid,
    phase = 'WAITING',
    next_run_at = clock_timestamp(),
    updated_at = clock_timestamp()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND task_id = sqlc.arg(task_id)::uuid
  AND state = 'RUNNING'
  AND cancel_requested_at IS NULL
  AND current_attempt_id IS NULL
RETURNING *;

-- name: ListDueAttempts :many
SELECT * FROM modeldev_dataset_import_attempts
WHERE reconcile_next_at IS NOT NULL
  AND reconcile_next_at <= sqlc.arg(now)
  AND (reconcile_until IS NULL OR reconcile_until < sqlc.arg(now))
ORDER BY reconcile_next_at, tenant_id, attempt_id
LIMIT sqlc.arg(batch_size);

-- name: AcquireAttemptLease :one
UPDATE modeldev_dataset_import_attempts
SET reconcile_owner = sqlc.arg(owner),
    reconcile_epoch = CASE WHEN reconcile_owner = sqlc.arg(owner) THEN reconcile_epoch ELSE reconcile_epoch + 1 END,
    reconcile_until = clock_timestamp() + sqlc.arg(ttl)::interval
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND attempt_id = sqlc.arg(attempt_id)::uuid
  AND (reconcile_owner IS NULL OR reconcile_owner = sqlc.arg(owner) OR reconcile_until < clock_timestamp())
RETURNING *;

-- name: ClaimAttemptCleanup :one
-- The cleanup claim is the only path that may delete an attempt's objects.
UPDATE modeldev_dataset_import_attempts
SET cleanup_state = 'RUNNING'
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND attempt_id = sqlc.arg(attempt_id)::uuid
  AND cleanup_state IN ('PENDING','WAITING')
  AND writers_stopped_at IS NOT NULL
  AND dispatch_closed_at IS NOT NULL
RETURNING *;

-- name: FinishAttemptCleanup :one
UPDATE modeldev_dataset_import_attempts
SET cleanup_state = 'DONE',
    cleanup_completed_at = clock_timestamp(),
    reconcile_next_at = NULL,
    reconcile_owner = NULL,
    reconcile_until = NULL
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND attempt_id = sqlc.arg(attempt_id)::uuid
  AND cleanup_state = 'RUNNING'
RETURNING *;

-- name: FreezeCandidate :one
-- Candidate fields go from all-empty to complete in one step and may only be
-- replayed at the same value afterwards.
UPDATE modeldev_dataset_import_attempts
SET result_object_version_id = sqlc.arg(version_id),
    result_sha256 = sqlc.arg(sha256),
    writers_stopped_at = clock_timestamp(),
    dispatch_closed_at = clock_timestamp(),
    close_reason = 'SUCCEEDED'
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND attempt_id = sqlc.arg(attempt_id)::uuid
  AND writers_stopped_at IS NULL
  AND result_object_version_id IS NULL
RETURNING *;

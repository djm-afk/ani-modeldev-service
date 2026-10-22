-- name: InsertFrozenImport :one
INSERT INTO modeldev_input_versions (
    tenant_id, input_version_id, request_id, actor, requested_at,
    storage_connection_id, bucket, approved_prefix, credential_reference, object_key,
    object_version_id, size_bytes, sha256, format
) VALUES (
    sqlc.arg(tenant_id)::uuid, sqlc.arg(input_version_id)::uuid,
    sqlc.arg(request_id)::uuid, sqlc.arg(actor), sqlc.arg(requested_at),
    sqlc.arg(storage_connection_id), sqlc.arg(bucket), sqlc.arg(approved_prefix),
    sqlc.arg(credential_reference), sqlc.arg(object_key), sqlc.arg(object_version_id), sqlc.arg(size_bytes), sqlc.arg(sha256), sqlc.arg(format)
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetInputVersion :one
SELECT * FROM modeldev_input_versions
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND input_version_id = sqlc.arg(input_version_id)::uuid;

-- name: LockInputVersion :one
SELECT * FROM modeldev_input_versions
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND input_version_id = sqlc.arg(input_version_id)::uuid
FOR UPDATE;

-- name: ListInputVersions :many
SELECT * FROM modeldev_input_versions
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND (sqlc.arg(state_filter)::text = '' OR state = sqlc.arg(state_filter)::text)
ORDER BY input_version_id
LIMIT sqlc.arg(row_limit)::integer
OFFSET sqlc.arg(row_offset)::integer;

-- name: CountInputVersions :one
SELECT count(*) FROM modeldev_input_versions
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND (sqlc.arg(state_filter)::text = '' OR state = sqlc.arg(state_filter)::text);

-- name: RecordVerifiedCSV :one
UPDATE modeldev_input_versions
SET state = 'READY',
    verified_at = sqlc.arg(verified_at),
    verified_schema_version = sqlc.arg(verified_schema_version),
    verified_row_count = sqlc.arg(verified_row_count),
    verified_feature_count = sqlc.arg(verified_feature_count),
    failure_code = NULL,
    failure_observed_at = NULL
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND input_version_id = sqlc.arg(input_version_id)::uuid
  AND request_id = sqlc.arg(request_id)::uuid
  AND state = 'VALIDATING'
RETURNING *;

-- name: RecordValidationFailure :one
UPDATE modeldev_input_versions
SET state = sqlc.arg(state),
    failure_code = sqlc.arg(failure_code),
    failure_observed_at = sqlc.arg(failure_observed_at)
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND input_version_id = sqlc.arg(input_version_id)::uuid
  AND request_id = sqlc.arg(request_id)::uuid
  AND state = 'VALIDATING'
RETURNING *;

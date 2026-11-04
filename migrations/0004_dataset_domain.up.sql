-- Dataset domain: managed dataset collections, their asynchronous online
-- imports, the credential ledger and the immutable operation history.
--
-- These tables extend the current schema. They do not rebuild
-- modeldev_input_versions or the tenant execution registry, and they add no
-- historical backfill: a Dataset version is an existing InputVersion that also
-- carries one row in modeldev_dataset_version_metadata.
--
-- Constraints here bound each row's own shape. Cross-row invariants (lease
-- fencing, immutable candidates, publish proofs, state transitions) are still
-- maintained by the transactions described in the design; a handful of CHECK
-- expressions is not the publish protocol.

-- Advisory-lock helper for reserved input identities. It only serializes
-- transactions; it grants nothing. Sync and online imports both call it in the
-- same order (ascending key) so they cannot deadlock each other.
CREATE FUNCTION modeldev_lock_input_identity(
    p_tenant uuid, p_input_version uuid, p_request uuid
) RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_lock bigint;
BEGIN
    IF p_tenant IS NULL OR p_input_version IS NULL OR p_request IS NULL
       OR p_tenant = '00000000-0000-0000-0000-000000000000'::uuid
       OR p_input_version = '00000000-0000-0000-0000-000000000000'::uuid
       OR p_request = '00000000-0000-0000-0000-000000000000'::uuid THEN
        RAISE EXCEPTION 'invalid input identity' USING ERRCODE = '22023';
    END IF;

    FOR v_lock IN
        SELECT DISTINCT k FROM (VALUES
            (hashtextextended('ani.modeldev.input-identity.v1:' || p_tenant::text ||
                             ':input:' || p_input_version::text, 0)),
            (hashtextextended('ani.modeldev.input-identity.v1:' || p_tenant::text ||
                             ':request:' || p_request::text, 0))
        ) AS keys(k) ORDER BY k
    LOOP
        PERFORM pg_advisory_xact_lock(v_lock);
    END LOOP;
END $$;

-- Advisory-lock helper for dataset import acceptance, publication and the
-- registry's own short transactions. It shares one key domain per
-- tenant+cluster so "registry has no row yet" and "first registration" cannot
-- race; it is not the long Ensure session lock.
--
-- The key bytes are the fixed prefix and the two UUIDs joined by NUL. PostgreSQL
-- text cannot hold NUL, so the separator is spliced in as bytea and the digest
-- is taken over the assembled bytes; that is the same byte string the Go side
-- hashes.
CREATE FUNCTION modeldev_lock_dataset_target_commit(
    p_tenant uuid, p_cluster uuid
) RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_key bigint;
BEGIN
    IF p_tenant IS NULL OR p_cluster IS NULL
       OR p_tenant = '00000000-0000-0000-0000-000000000000'::uuid
       OR p_cluster = '00000000-0000-0000-0000-000000000000'::uuid THEN
        RAISE EXCEPTION 'invalid dataset target identity' USING ERRCODE = '22023';
    END IF;
    v_key := ('x' || substr(encode(sha256(
        convert_to('ani.modeldev.dataset-target-commit.v1', 'UTF8') || decode('00', 'hex') ||
        convert_to(p_tenant::text, 'UTF8') || decode('00', 'hex') ||
        convert_to(p_cluster::text, 'UTF8')), 'hex'), 1, 16))::bit(64)::bigint;
    PERFORM pg_advisory_xact_lock(v_key);
END $$;

CREATE TABLE modeldev_datasets (
    tenant_id uuid NOT NULL,
    dataset_id uuid NOT NULL,
    name text COLLATE "C" NOT NULL,
    description text NOT NULL DEFAULT '',
    modality text NOT NULL DEFAULT 'TEXT' CHECK (modality = 'TEXT'),
    schema_kind text NOT NULL DEFAULT 'ALPACA' CHECK (schema_kind = 'ALPACA'),
    state text NOT NULL DEFAULT 'ACTIVE' CHECK (state IN ('ACTIVE','ARCHIVED')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    next_version_number bigint NOT NULL DEFAULT 1 CHECK (next_version_number > 0),
    created_by text NOT NULL CHECK (created_by <> ''),
    updated_by text NOT NULL CHECK (updated_by <> ''),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    archived_at timestamptz,
    PRIMARY KEY (tenant_id, dataset_id),
    UNIQUE (tenant_id, name),
    CHECK (tenant_id <> '00000000-0000-0000-0000-000000000000'::uuid),
    CHECK (dataset_id <> '00000000-0000-0000-0000-000000000000'::uuid),
    CHECK (char_length(name) BETWEEN 1 AND 128 AND name IS NFC NORMALIZED),
    CHECK (char_length(description) <= 4000),
    CHECK (isfinite(created_at) AND isfinite(updated_at) AND updated_at >= created_at),
    CHECK ((state = 'ACTIVE' AND archived_at IS NULL)
        OR (state = 'ARCHIVED' AND archived_at IS NOT NULL
            AND isfinite(archived_at) AND archived_at >= created_at))
);

CREATE TABLE modeldev_dataset_command_receipts (
    tenant_id uuid NOT NULL,
    receipt_id uuid NOT NULL,
    actor_id text COLLATE "C" NOT NULL CHECK (actor_id <> ''),
    operation_kind text NOT NULL CHECK (operation_kind IN (
        'CREATE_DATASET','UPDATE_DATASET','ARCHIVE_DATASET','RESTORE_DATASET',
        'IMPORT_DATASET_VERSION','CANCEL_DATASET_IMPORT','RETRY_DATASET_IMPORT')),
    idempotency_key text COLLATE "C" NOT NULL
        CHECK (idempotency_key ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    request_sha256 text NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
    canonical_request jsonb NOT NULL CHECK (jsonb_typeof(canonical_request) = 'object'),
    dataset_id uuid NOT NULL,
    task_id uuid,
    response_status integer NOT NULL CHECK (response_status IN (200,201,202)),
    receipt jsonb NOT NULL CHECK (jsonb_typeof(receipt) = 'object'),
    accepted_at timestamptz NOT NULL CHECK (isfinite(accepted_at)),
    PRIMARY KEY (tenant_id, receipt_id),
    UNIQUE (tenant_id, actor_id, operation_kind, idempotency_key),
    UNIQUE (tenant_id, dataset_id, receipt_id),
    UNIQUE (tenant_id, dataset_id, task_id, receipt_id),
    FOREIGN KEY (tenant_id, dataset_id)
        REFERENCES modeldev_datasets (tenant_id, dataset_id),
    CHECK ((operation_kind IN ('IMPORT_DATASET_VERSION','CANCEL_DATASET_IMPORT',
                               'RETRY_DATASET_IMPORT')) = (task_id IS NOT NULL))
);

CREATE TABLE modeldev_dataset_import_tasks (
    tenant_id uuid NOT NULL,
    task_id uuid NOT NULL,
    dataset_id uuid NOT NULL,
    acceptance_receipt_id uuid NOT NULL,
    reserved_input_version_id uuid NOT NULL,
    input_request_id uuid NOT NULL,
    version_number bigint NOT NULL CHECK (version_number > 0),
    created_by text NOT NULL CHECK (created_by <> ''),
    accepted_at timestamptz NOT NULL CHECK (isfinite(accepted_at)),
    source_provider text NOT NULL CHECK (source_provider IN ('HUGGINGFACE','MODELSCOPE')),
    source_snapshot jsonb NOT NULL CHECK (jsonb_typeof(source_snapshot) = 'object'),
    source_snapshot_sha256 text NOT NULL CHECK (source_snapshot_sha256 ~ '^[0-9a-f]{64}$'),
    expected_size_bytes bigint NOT NULL CHECK (expected_size_bytes > 0),
    accepted_limit_bytes bigint NOT NULL CHECK (accepted_limit_bytes > 0),
    cluster_id uuid NOT NULL,
    namespace_name text NOT NULL CHECK (namespace_name = 'tenant-' || tenant_id::text),
    namespace_uid uuid NOT NULL,
    target_snapshot jsonb NOT NULL CHECK (jsonb_typeof(target_snapshot) = 'object'),
    target_snapshot_sha256 text NOT NULL CHECK (target_snapshot_sha256 ~ '^[0-9a-f]{64}$'),
    storage_connection_id text NOT NULL CHECK (storage_connection_id <> ''),
    bucket text NOT NULL CHECK (bucket <> ''),
    approved_prefix text NOT NULL CHECK (approved_prefix = 'inputs/' || tenant_id::text),
    input_scope_sha256 text NOT NULL CHECK (input_scope_sha256 ~ '^[0-9a-f]{64}$'),
    credential_reference text NOT NULL CHECK (credential_reference <> ''),
    observed_environment_id uuid,
    observed_environment_binding_digest text
        CHECK (observed_environment_binding_digest IS NULL
            OR observed_environment_binding_digest ~ '^[0-9a-f]{64}$'),
    authorization_scope jsonb NOT NULL CHECK (jsonb_typeof(authorization_scope) = 'object'),
    authorization_scope_sha256 text NOT NULL CHECK (authorization_scope_sha256 ~ '^[0-9a-f]{64}$'),
    target_invalidated_at timestamptz,
    target_invalidated_reason text,
    state text NOT NULL DEFAULT 'QUEUED'
        CHECK (state IN ('QUEUED','RUNNING','FAILED','CANCELING','CANCELLED','SUCCEEDED')),
    phase text NOT NULL DEFAULT 'WAITING'
        CHECK (phase IN ('WAITING','DOWNLOADING','UPLOADING','VERIFYING','PUBLISHING','STOPPING')),
    recovery_generation bigint NOT NULL DEFAULT 1 CHECK (recovery_generation > 0),
    recovery_policy jsonb NOT NULL CHECK (jsonb_typeof(recovery_policy) = 'object'),
    validation_retry_count integer NOT NULL DEFAULT 0 CHECK (validation_retry_count >= 0),
    validation_deadline_at timestamptz,
    next_run_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(next_run_at)),
    current_attempt_id uuid,
    lease_owner text,
    lease_epoch bigint NOT NULL DEFAULT 0 CHECK (lease_epoch >= 0),
    lease_until timestamptz,
    cancel_requested_at timestamptz,
    failure_code text,
    failure_detail jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(failure_detail) = 'object'),
    retryable boolean NOT NULL DEFAULT false,
    candidate_attempt_id uuid,
    candidate_object_key text,
    candidate_version_id text,
    candidate_size_bytes bigint,
    candidate_sha256 text,
    candidate_frozen_at timestamptz,
    candidate_report_version_id text,
    candidate_report_sha256 text,
    finished_at timestamptz,
    published_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, task_id),
    UNIQUE (tenant_id, dataset_id, task_id),
    UNIQUE (tenant_id, reserved_input_version_id),
    UNIQUE (tenant_id, input_request_id),
    UNIQUE (tenant_id, dataset_id, version_number),
    UNIQUE (tenant_id, dataset_id, task_id, reserved_input_version_id, version_number),
    UNIQUE (tenant_id, dataset_id, task_id, cluster_id, namespace_name, namespace_uid),
    FOREIGN KEY (tenant_id, dataset_id)
        REFERENCES modeldev_datasets (tenant_id, dataset_id),
    FOREIGN KEY (tenant_id, dataset_id, task_id, acceptance_receipt_id)
        REFERENCES modeldev_dataset_command_receipts (tenant_id, dataset_id, task_id, receipt_id)
        DEFERRABLE INITIALLY DEFERRED,
    CHECK (task_id <> '00000000-0000-0000-0000-000000000000'::uuid
        AND reserved_input_version_id <> '00000000-0000-0000-0000-000000000000'::uuid
        AND input_request_id <> '00000000-0000-0000-0000-000000000000'::uuid
        AND cluster_id <> '00000000-0000-0000-0000-000000000000'::uuid
        AND namespace_uid <> '00000000-0000-0000-0000-000000000000'::uuid),
    CHECK (expected_size_bytes <= accepted_limit_bytes),
    CHECK (observed_environment_id IS NULL
        OR observed_environment_id <> '00000000-0000-0000-0000-000000000000'::uuid),
    CHECK (observed_environment_binding_digest IS NULL OR observed_environment_id IS NOT NULL),
    CHECK (target_snapshot = jsonb_build_object(
        'schema', 'ani.modeldev.dataset.import-target.v1',
        'tenant_id', tenant_id::text,
        'cluster_id', cluster_id::text,
        'namespace_name', namespace_name,
        'namespace_uid', namespace_uid::text,
        'input_scope', jsonb_build_object(
            'storage_connection_id', storage_connection_id,
            'bucket', bucket,
            'approved_prefix', approved_prefix,
            'credential_reference', credential_reference))),
    CHECK (COALESCE(
        authorization_scope ?& ARRAY['schema','tenant_id','input_scope_sha256','purpose','allowed_actions']
        AND authorization_scope - ARRAY['schema','tenant_id','input_scope_sha256','purpose','allowed_actions'] = '{}'::jsonb
        AND authorization_scope->>'schema' = 'ani.modeldev.dataset.import-authorization.v1'
        AND authorization_scope->>'tenant_id' = tenant_id::text
        AND authorization_scope->>'input_scope_sha256' = input_scope_sha256
        AND authorization_scope->>'purpose' = 'dataset-import'
        AND jsonb_typeof(authorization_scope->'allowed_actions') = 'array', false)),
    CHECK (COALESCE(
        source_snapshot ?& ARRAY['schema','provider','repo_type','repo_id','requested_revision',
            'file_path','lock_kind','resolved_revision','expected_size_bytes',
            'expected_file_sha256','resolved_at']
        AND source_snapshot - ARRAY['schema','provider','repo_type','repo_id','requested_revision',
            'file_path','lock_kind','resolved_revision','expected_size_bytes',
            'expected_file_sha256','resolved_at'] = '{}'::jsonb
        AND source_snapshot->>'schema' = 'ani.modeldev.dataset.source-snapshot.v1'
        AND source_snapshot->>'provider' = source_provider
        AND source_snapshot->>'repo_type' = 'dataset'
        AND jsonb_typeof(source_snapshot->'expected_size_bytes') = 'string'
        AND source_snapshot->>'expected_size_bytes' = expected_size_bytes::text,
        false)),
    CHECK ((target_invalidated_at IS NULL AND target_invalidated_reason IS NULL)
        OR (target_invalidated_at IS NOT NULL AND isfinite(target_invalidated_at)
            AND target_invalidated_reason IS NOT NULL AND target_invalidated_reason <> '')),
    CHECK (validation_deadline_at IS NULL OR isfinite(validation_deadline_at)),
    CHECK ((lease_owner IS NULL AND lease_until IS NULL)
        OR (lease_owner IS NOT NULL AND lease_owner <> '' AND lease_until IS NOT NULL
            AND isfinite(lease_until) AND lease_epoch > 0)),
    CHECK (NOT retryable OR state = 'FAILED'),
    CHECK (state <> 'FAILED' OR (failure_code IS NOT NULL AND failure_code <> '')),
    CHECK ((state IN ('SUCCEEDED','FAILED','CANCELLED')) = (finished_at IS NOT NULL)),
    CHECK ((state = 'SUCCEEDED') = (published_at IS NOT NULL)),
    CHECK (state NOT IN ('SUCCEEDED','FAILED','CANCELLED') OR lease_owner IS NULL),
    CHECK (state NOT IN ('CANCELING','CANCELLED') OR cancel_requested_at IS NOT NULL),
    CHECK (state NOT IN ('CANCELING','CANCELLED') OR phase = 'STOPPING'),
    CHECK (state <> 'SUCCEEDED' OR (phase = 'PUBLISHING'
        AND candidate_version_id IS NOT NULL AND cancel_requested_at IS NULL)),
    CHECK (
      (candidate_attempt_id IS NULL AND candidate_object_key IS NULL
       AND candidate_version_id IS NULL AND candidate_size_bytes IS NULL
       AND candidate_sha256 IS NULL AND candidate_frozen_at IS NULL
       AND candidate_report_version_id IS NULL AND candidate_report_sha256 IS NULL)
      OR
      (candidate_attempt_id IS NOT NULL AND candidate_object_key IS NOT NULL
       AND candidate_object_key <> '' AND candidate_version_id IS NOT NULL
       AND candidate_version_id <> '' AND lower(candidate_version_id) <> 'null'
       AND candidate_size_bytes IS NOT NULL AND candidate_size_bytes = expected_size_bytes
       AND candidate_sha256 IS NOT NULL AND candidate_sha256 ~ '^[0-9a-f]{64}$'
       AND candidate_frozen_at IS NOT NULL AND isfinite(candidate_frozen_at)
       AND candidate_report_version_id IS NOT NULL AND candidate_report_version_id <> ''
       AND lower(candidate_report_version_id) <> 'null'
       AND candidate_report_sha256 IS NOT NULL AND candidate_report_sha256 ~ '^[0-9a-f]{64}$')
    )
);

ALTER TABLE modeldev_dataset_command_receipts
    ADD CONSTRAINT dataset_receipt_task_fk
    FOREIGN KEY (tenant_id, dataset_id, task_id)
    REFERENCES modeldev_dataset_import_tasks (tenant_id, dataset_id, task_id)
    DEFERRABLE INITIALLY DEFERRED;

CREATE UNIQUE INDEX dataset_one_acceptance_receipt_per_task
    ON modeldev_dataset_command_receipts (tenant_id, task_id)
    WHERE operation_kind = 'IMPORT_DATASET_VERSION';

CREATE TABLE modeldev_dataset_import_attempts (
    tenant_id uuid NOT NULL,
    attempt_id uuid NOT NULL,
    dataset_id uuid NOT NULL,
    task_id uuid NOT NULL,
    attempt_no bigint NOT NULL CHECK (attempt_no > 0),
    recovery_generation bigint NOT NULL CHECK (recovery_generation > 0),
    cluster_id uuid NOT NULL,
    namespace_name text NOT NULL CHECK (namespace_name = 'tenant-' || tenant_id::text),
    namespace_uid uuid NOT NULL,
    job_name text NOT NULL CHECK (job_name <> ''),
    pvc_name text NOT NULL CHECK (pvc_name <> ''),
    secret_name text NOT NULL CHECK (secret_name <> ''),
    job_uid text,
    job_resource_version text,
    pvc_uid text,
    pvc_resource_version text,
    secret_uid text,
    secret_resource_version text,
    runtime_plan jsonb NOT NULL CHECK (jsonb_typeof(runtime_plan) = 'object'),
    runtime_plan_sha256 text NOT NULL CHECK (runtime_plan_sha256 ~ '^[0-9a-f]{64}$'),
    runtime_observation jsonb NOT NULL DEFAULT
        '{"schema":"ani.modeldev.dataset.attempt-observation.v1","operations":[],"pods":[],"closure":{}}'::jsonb,
    data_object_key text NOT NULL CHECK (data_object_key <> ''),
    result_object_key text NOT NULL CHECK (result_object_key <> ''),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    deadline_at timestamptz NOT NULL,
    dispatch_closed_at timestamptz,
    close_reason text CHECK (close_reason IN
        ('SUCCEEDED','FAILED','CANCELLED','RETRY','DEADLINE','ENVIRONMENT_CHANGED')),
    writers_stopped_at timestamptz,
    result_object_version_id text,
    result_sha256 text,
    last_credential_generation bigint NOT NULL DEFAULT 0 CHECK (last_credential_generation >= 0),
    secret_generation bigint CHECK (secret_generation > 0),
    reconcile_owner text,
    reconcile_epoch bigint NOT NULL DEFAULT 0 CHECK (reconcile_epoch >= 0),
    reconcile_until timestamptz,
    reconcile_next_at timestamptz DEFAULT clock_timestamp(),
    cleanup_state text NOT NULL DEFAULT 'PENDING'
        CHECK (cleanup_state IN ('PENDING','WAITING','RUNNING','RETAINED','DONE')),
    cleanup_completed_at timestamptz,
    PRIMARY KEY (tenant_id, attempt_id),
    UNIQUE (tenant_id, dataset_id, task_id, attempt_id),
    UNIQUE (tenant_id, task_id, attempt_no),
    UNIQUE (tenant_id, dataset_id, task_id, attempt_id, secret_uid),
    FOREIGN KEY (tenant_id, dataset_id, task_id, cluster_id, namespace_name, namespace_uid)
        REFERENCES modeldev_dataset_import_tasks
            (tenant_id, dataset_id, task_id, cluster_id, namespace_name, namespace_uid),
    CHECK (attempt_id <> '00000000-0000-0000-0000-000000000000'::uuid),
    CHECK (isfinite(created_at) AND isfinite(deadline_at) AND deadline_at > created_at),
    CHECK (data_object_key <> result_object_key),
    CHECK ((job_uid IS NULL AND job_resource_version IS NULL)
        OR (job_uid IS NOT NULL AND job_uid <> '' AND job_resource_version IS NOT NULL
            AND job_resource_version <> '')),
    CHECK ((pvc_uid IS NULL AND pvc_resource_version IS NULL)
        OR (pvc_uid IS NOT NULL AND pvc_uid <> '' AND pvc_resource_version IS NOT NULL
            AND pvc_resource_version <> '')),
    CHECK ((secret_uid IS NULL AND secret_resource_version IS NULL)
        OR (secret_uid IS NOT NULL AND secret_uid <> '' AND secret_resource_version IS NOT NULL
            AND secret_resource_version <> '')),
    CHECK (COALESCE(jsonb_typeof(runtime_observation) = 'object'
        AND runtime_observation ?& ARRAY['schema','operations','pods','closure']
        AND runtime_observation->>'schema' = 'ani.modeldev.dataset.attempt-observation.v1'
        AND jsonb_typeof(runtime_observation->'operations') = 'array'
        AND jsonb_typeof(runtime_observation->'pods') = 'array'
        AND jsonb_typeof(runtime_observation->'closure') = 'object', false)),
    CHECK ((dispatch_closed_at IS NULL AND close_reason IS NULL)
        OR (dispatch_closed_at IS NOT NULL AND isfinite(dispatch_closed_at)
            AND close_reason IS NOT NULL)),
    CHECK (writers_stopped_at IS NULL
        OR (isfinite(writers_stopped_at) AND writers_stopped_at >= created_at
            AND dispatch_closed_at IS NOT NULL)),
    CHECK ((result_object_version_id IS NULL AND result_sha256 IS NULL)
        OR (result_object_version_id IS NOT NULL AND result_object_version_id <> ''
            AND lower(result_object_version_id) <> 'null'
            AND result_sha256 IS NOT NULL AND result_sha256 ~ '^[0-9a-f]{64}$')),
    CHECK (secret_generation IS NULL
        OR (secret_uid IS NOT NULL AND secret_generation <= last_credential_generation)),
    CHECK ((reconcile_owner IS NULL AND reconcile_until IS NULL)
        OR (reconcile_owner IS NOT NULL AND reconcile_owner <> '' AND reconcile_until IS NOT NULL
            AND isfinite(reconcile_until) AND reconcile_epoch > 0)),
    CHECK (reconcile_next_at IS NULL OR isfinite(reconcile_next_at)),
    CHECK ((cleanup_state = 'DONE') = (cleanup_completed_at IS NOT NULL)),
    CHECK (cleanup_state NOT IN ('RUNNING','RETAINED','DONE') OR writers_stopped_at IS NOT NULL),
    CHECK (reconcile_next_at IS NOT NULL
        OR (writers_stopped_at IS NOT NULL AND cleanup_state IN ('RETAINED','DONE')
            AND reconcile_owner IS NULL))
);

ALTER TABLE modeldev_dataset_import_tasks
    ADD CONSTRAINT dataset_task_current_attempt_fk
    FOREIGN KEY (tenant_id, dataset_id, task_id, current_attempt_id)
    REFERENCES modeldev_dataset_import_attempts (tenant_id, dataset_id, task_id, attempt_id)
    DEFERRABLE INITIALLY DEFERRED,
    ADD CONSTRAINT dataset_task_candidate_attempt_fk
    FOREIGN KEY (tenant_id, dataset_id, task_id, candidate_attempt_id)
    REFERENCES modeldev_dataset_import_attempts (tenant_id, dataset_id, task_id, attempt_id)
    DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE modeldev_dataset_credential_issuances (
    tenant_id uuid NOT NULL,
    issuance_id uuid NOT NULL,
    dataset_id uuid NOT NULL,
    task_id uuid NOT NULL,
    attempt_id uuid NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    task_lease_epoch bigint NOT NULL CHECK (task_lease_epoch > 0),
    attempt_reconcile_epoch bigint NOT NULL CHECK (attempt_reconcile_epoch > 0),
    secret_uid text NOT NULL CHECK (secret_uid <> ''),
    policy jsonb NOT NULL CHECK (jsonb_typeof(policy) = 'object'),
    policy_sha256 text NOT NULL CHECK (policy_sha256 ~ '^[0-9a-f]{64}$'),
    requested_duration_seconds integer NOT NULL DEFAULT 900 CHECK (requested_duration_seconds = 900),
    intended_at timestamptz NOT NULL CHECK (isfinite(intended_at)),
    request_deadline_at timestamptz NOT NULL CHECK (isfinite(request_deadline_at)),
    authorization_deadline_at timestamptz NOT NULL CHECK (isfinite(authorization_deadline_at)),
    outcome text NOT NULL DEFAULT 'INTENT'
        CHECK (outcome IN ('INTENT','ISSUED','NOT_ISSUED','UNKNOWN')),
    result_recorded_at timestamptz,
    actual_expires_at timestamptz,
    unknown_expires_upper_bound timestamptz,
    expiry_bound_basis jsonb,
    delivery_state text NOT NULL DEFAULT 'NOT_ATTEMPTED'
        CHECK (delivery_state IN ('NOT_ATTEMPTED','PREPARED','APPLIED','UNKNOWN','CLOSED')),
    delivery_intended_at timestamptz,
    delivery_expected_resource_version text,
    delivery_observed_resource_version text,
    delivered_at timestamptz,
    PRIMARY KEY (tenant_id, issuance_id),
    UNIQUE (tenant_id, dataset_id, task_id, attempt_id, generation, secret_uid),
    UNIQUE (tenant_id, attempt_id, generation),
    FOREIGN KEY (tenant_id, dataset_id, task_id, attempt_id, secret_uid)
        REFERENCES modeldev_dataset_import_attempts
            (tenant_id, dataset_id, task_id, attempt_id, secret_uid),
    CHECK (issuance_id <> '00000000-0000-0000-0000-000000000000'::uuid),
    CHECK (request_deadline_at > intended_at
        AND authorization_deadline_at >= request_deadline_at),
    CHECK ((outcome = 'INTENT') = (result_recorded_at IS NULL)),
    CHECK (result_recorded_at IS NULL OR isfinite(result_recorded_at)),
    CHECK ((outcome = 'ISSUED') = (actual_expires_at IS NOT NULL)),
    CHECK (actual_expires_at IS NULL OR isfinite(actual_expires_at)),
    CHECK ((unknown_expires_upper_bound IS NULL AND expiry_bound_basis IS NULL)
        OR (unknown_expires_upper_bound IS NOT NULL AND isfinite(unknown_expires_upper_bound)
            AND unknown_expires_upper_bound >= request_deadline_at
            AND expiry_bound_basis IS NOT NULL AND jsonb_typeof(expiry_bound_basis) = 'object')),
    CHECK (delivery_state NOT IN ('PREPARED','APPLIED','UNKNOWN') OR outcome = 'ISSUED'),
    CHECK (delivery_state NOT IN ('PREPARED','APPLIED','UNKNOWN')
        OR (delivery_intended_at IS NOT NULL AND isfinite(delivery_intended_at)
            AND delivery_expected_resource_version IS NOT NULL
            AND delivery_expected_resource_version <> '')),
    CHECK ((delivery_state = 'APPLIED') = (delivered_at IS NOT NULL)),
    CHECK (delivered_at IS NULL
        OR (isfinite(delivered_at) AND delivery_observed_resource_version IS NOT NULL
            AND delivery_observed_resource_version <> ''))
);

ALTER TABLE modeldev_dataset_import_attempts
    ADD CONSTRAINT dataset_attempt_secret_generation_fk
    FOREIGN KEY (tenant_id, dataset_id, task_id, attempt_id, secret_generation, secret_uid)
    REFERENCES modeldev_dataset_credential_issuances
        (tenant_id, dataset_id, task_id, attempt_id, generation, secret_uid)
    DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE modeldev_dataset_version_metadata (
    tenant_id uuid NOT NULL,
    input_version_id uuid NOT NULL,
    dataset_id uuid NOT NULL,
    import_task_id uuid NOT NULL,
    version_number bigint NOT NULL CHECK (version_number > 0),
    preview jsonb NOT NULL CHECK (jsonb_typeof(preview) = 'object'),
    published_at timestamptz NOT NULL CHECK (isfinite(published_at)),
    PRIMARY KEY (tenant_id, input_version_id),
    UNIQUE (tenant_id, import_task_id),
    UNIQUE (tenant_id, dataset_id, version_number),
    FOREIGN KEY (tenant_id, input_version_id)
        REFERENCES modeldev_input_versions (tenant_id, input_version_id),
    FOREIGN KEY (tenant_id, dataset_id, import_task_id, input_version_id, version_number)
        REFERENCES modeldev_dataset_import_tasks
            (tenant_id, dataset_id, task_id, reserved_input_version_id, version_number)
        DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE modeldev_dataset_operations (
    tenant_id uuid NOT NULL,
    operation_id uuid NOT NULL,
    dataset_id uuid NOT NULL,
    task_id uuid,
    attempt_id uuid,
    receipt_id uuid,
    actor text NOT NULL CHECK (actor <> ''),
    event_type text NOT NULL CHECK (event_type IN (
        'DATASET_CREATED','DATASET_UPDATED','DATASET_ARCHIVED','DATASET_RESTORED',
        'IMPORT_ACCEPTED','IMPORT_RETRY_ACCEPTED','IMPORT_CANCEL_REQUESTED',
        'IMPORT_SUCCEEDED','IMPORT_FAILED','IMPORT_CANCELLED')),
    result text NOT NULL CHECK (result IN ('ACCEPTED','SUCCEEDED','FAILED','NO_CHANGE')),
    before_state text NOT NULL DEFAULT '',
    after_state text NOT NULL DEFAULT '',
    before_revision bigint CHECK (before_revision > 0),
    after_revision bigint CHECK (after_revision > 0),
    recovery_generation bigint CHECK (recovery_generation > 0),
    failure_code text,
    detail jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(detail) = 'object'),
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(occurred_at)),
    PRIMARY KEY (tenant_id, operation_id),
    FOREIGN KEY (tenant_id, dataset_id)
        REFERENCES modeldev_datasets (tenant_id, dataset_id),
    FOREIGN KEY (tenant_id, dataset_id, task_id)
        REFERENCES modeldev_dataset_import_tasks (tenant_id, dataset_id, task_id),
    FOREIGN KEY (tenant_id, dataset_id, task_id, attempt_id)
        REFERENCES modeldev_dataset_import_attempts (tenant_id, dataset_id, task_id, attempt_id),
    FOREIGN KEY (tenant_id, dataset_id, receipt_id)
        REFERENCES modeldev_dataset_command_receipts (tenant_id, dataset_id, receipt_id),
    FOREIGN KEY (tenant_id, dataset_id, task_id, receipt_id)
        REFERENCES modeldev_dataset_command_receipts (tenant_id, dataset_id, task_id, receipt_id),
    CHECK (attempt_id IS NULL OR task_id IS NOT NULL),
    CHECK (recovery_generation IS NULL OR task_id IS NOT NULL)
);

CREATE INDEX dataset_list_idx
    ON modeldev_datasets (tenant_id, state, updated_at DESC, dataset_id DESC);
CREATE INDEX dataset_task_list_idx
    ON modeldev_dataset_import_tasks (tenant_id, dataset_id, accepted_at DESC, task_id DESC);
CREATE INDEX dataset_task_due_idx
    ON modeldev_dataset_import_tasks (next_run_at, accepted_at, tenant_id, task_id)
    WHERE state IN ('QUEUED','RUNNING','CANCELING');
CREATE INDEX dataset_attempt_reconcile_due_idx
    ON modeldev_dataset_import_attempts (reconcile_next_at, tenant_id, attempt_id)
    WHERE reconcile_next_at IS NOT NULL;
CREATE INDEX dataset_version_list_idx
    ON modeldev_dataset_version_metadata (tenant_id, dataset_id, version_number DESC);
CREATE INDEX dataset_operation_list_idx
    ON modeldev_dataset_operations (tenant_id, dataset_id, occurred_at DESC, operation_id DESC);
CREATE INDEX dataset_task_state_list_idx
    ON modeldev_dataset_import_tasks (tenant_id, dataset_id, state, accepted_at DESC, task_id DESC);
CREATE INDEX dataset_operation_task_list_idx
    ON modeldev_dataset_operations (tenant_id, task_id, occurred_at DESC, operation_id DESC)
    WHERE task_id IS NOT NULL;

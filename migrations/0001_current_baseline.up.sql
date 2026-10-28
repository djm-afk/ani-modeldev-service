-- Complete current ModelDev application schema. Empty initialization only.
-- No historical writer, row conversion, upgrade or backfill path.
--
-- PostgreSQL database dump
--


-- Dumped from database version 16.10 (Debian 16.10-1.pgdg13+1)
-- Dumped by pg_dump version 16.10 (Debian 16.10-1.pgdg13+1)

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: public; Type: SCHEMA; Schema: -; Owner: -
--



--
-- Name: modeldev_gpu_release_scope_immutable(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION modeldev_gpu_release_scope_immutable() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
 IF OLD.gpu_release_sealed_at IS NOT NULL AND (
  NEW.gpu_release_sealed_at IS DISTINCT FROM OLD.gpu_release_sealed_at
  OR NEW.gpu_release_scope_sha256 IS DISTINCT FROM OLD.gpu_release_scope_sha256
  OR NEW.facts->'GPURelease'->'scope' IS DISTINCT FROM OLD.facts->'GPURelease'->'scope'
 ) THEN
  RAISE EXCEPTION 'immutable GPU release scope' USING ERRCODE='23514';
 END IF;
 IF OLD.facts->'GPURelease'->'outbox'->'event' IS NOT NULL AND (
  NEW.facts->'GPURelease'->'outbox'->'event' IS DISTINCT FROM OLD.facts->'GPURelease'->'outbox'->'event'
  OR NEW.facts->'GPURelease'->'outbox'->'created_at' IS DISTINCT FROM OLD.facts->'GPURelease'->'outbox'->'created_at'
  OR NEW.facts->'GPURelease'->'observation' IS DISTINCT FROM OLD.facts->'GPURelease'->'observation'
 ) THEN
  RAISE EXCEPTION 'immutable GPU release event' USING ERRCODE='23514';
 END IF;
 IF OLD.facts->'GPURelease'->'outbox'->'ack' IS NOT NULL AND (
  NEW.facts->'GPURelease'->'outbox'->'ack' IS DISTINCT FROM OLD.facts->'GPURelease'->'outbox'->'ack'
  OR NEW.facts->'GPURelease'->'outbox'->'delivered_at' IS DISTINCT FROM OLD.facts->'GPURelease'->'outbox'->'delivered_at'
 ) THEN
  RAISE EXCEPTION 'immutable GPU release ACK' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: modeldev_close_intents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE modeldev_close_intents (
    tenant_id uuid NOT NULL,
    execution_id uuid NOT NULL,
    operation_id uuid NOT NULL,
    spec_hash text NOT NULL,
    source_kind text NOT NULL,
    source_generation numeric(20,0) NOT NULL,
    owner_generation numeric(20,0) NOT NULL,
    reason text NOT NULL,
    requested_at timestamp with time zone NOT NULL,
    requested_actor text NOT NULL,
    close_state text NOT NULL,
    CONSTRAINT modeldev_close_intents_close_state_check CHECK ((close_state = 'CLOSING'::text)),
    CONSTRAINT modeldev_close_intents_owner_generation_check CHECK (((owner_generation > (0)::numeric) AND (owner_generation <= '18446744073709551615'::numeric))),
    CONSTRAINT modeldev_close_intents_reason_check CHECK ((reason = 'USER_STOP'::text)),
    CONSTRAINT modeldev_close_intents_requested_actor_check CHECK ((requested_actor <> ''::text)),
    CONSTRAINT modeldev_close_intents_source_generation_check CHECK (((source_generation > (0)::numeric) AND (source_generation <= '18446744073709551615'::numeric))),
    CONSTRAINT modeldev_close_intents_source_kind_check CHECK ((source_kind = 'GOVERNANCE'::text)),
    CONSTRAINT modeldev_close_intents_spec_hash_check CHECK ((spec_hash ~ '^[0-9a-f]{64}$'::text))
);


--
-- Name: modeldev_execution_cleanup_audits; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE modeldev_execution_cleanup_audits (
    tenant_id uuid NOT NULL,
    execution_id uuid NOT NULL,
    plan_sha256 text NOT NULL,
    actor text NOT NULL,
    phase text NOT NULL,
    plan jsonb NOT NULL,
    receipt jsonb NOT NULL,
    started_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT modeldev_execution_cleanup_audits_check CHECK ((((phase = 'STARTED'::text) AND (completed_at IS NULL)) OR ((phase = ANY (ARRAY['APPLIED'::text, 'NEEDS_REVIEW'::text])) AND (completed_at IS NOT NULL)) OR ((phase = 'RECONCILED'::text) AND ((receipt ->> 'reconciled_from_phase'::text) IS NOT NULL) AND ((receipt ->> 'reconciled_at'::text) IS NOT NULL) AND (((receipt ->> 'reconciled_at'::text))::timestamp with time zone >= started_at) AND ((((receipt ->> 'reconciled_from_phase'::text) = 'STARTED'::text) AND (completed_at IS NULL)) OR (((receipt ->> 'reconciled_from_phase'::text) = 'NEEDS_REVIEW'::text) AND (completed_at IS NOT NULL)))))),
    CONSTRAINT modeldev_execution_cleanup_audits_phase_check CHECK ((phase = ANY (ARRAY['STARTED'::text, 'APPLIED'::text, 'NEEDS_REVIEW'::text, 'RECONCILED'::text]))),
    CONSTRAINT modeldev_execution_cleanup_audits_plan_sha256_check CHECK ((plan_sha256 ~ '^[0-9a-f]{64}$'::text))
);


--
-- Name: modeldev_execution_identities; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE modeldev_execution_identities (
    tenant_id uuid NOT NULL,
    execution_id uuid NOT NULL,
    operation_id uuid NOT NULL,
    spec_hash text NOT NULL,
    close_generation numeric(20,0) DEFAULT 0 NOT NULL,
    owner_revision numeric DEFAULT 0 NOT NULL,
    CONSTRAINT modeldev_execution_identities_close_generation_check CHECK (((close_generation >= (0)::numeric) AND (close_generation <= '18446744073709551615'::numeric))),
    CONSTRAINT modeldev_execution_identities_spec_hash_check CHECK ((spec_hash ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT modeldev_execution_owner_revision_check CHECK (((owner_revision >= (0)::numeric) AND (owner_revision <= '18446744073709551615'::numeric) AND (owner_revision = trunc(owner_revision))))
);


--
-- Name: modeldev_execution_runtimes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE modeldev_execution_runtimes (
    tenant_id uuid NOT NULL,
    execution_id uuid NOT NULL,
    operation_id uuid NOT NULL,
    spec_hash text NOT NULL,
    facts jsonb NOT NULL,
    training_name text,
    training_request_sha256 text,
    training_uid text,
    publication_id uuid,
    close_generation numeric(20,0) DEFAULT 0 NOT NULL,
    close_reason text,
    close_requested_at timestamp with time zone,
    closed_at timestamp with time zone,
    authority_execution_id uuid GENERATED ALWAYS AS (
CASE
    WHEN ((close_generation = (0)::numeric) OR ((facts -> 'Workspace'::text) IS DISTINCT FROM 'null'::jsonb) OR ((facts -> 'Training'::text) IS DISTINCT FROM 'null'::jsonb) OR ((facts -> 'TrainingHandle'::text) IS DISTINCT FROM 'null'::jsonb) OR ((facts -> 'Observation'::text) IS DISTINCT FROM 'null'::jsonb) OR ((facts -> 'Publication'::text) IS DISTINCT FROM 'null'::jsonb)) THEN execution_id
    ELSE NULL::uuid
END) STORED,
    gpu_release_sealed_at timestamp with time zone,
    gpu_release_scope_sha256 text,
    CONSTRAINT modeldev_execution_runtimes_check CHECK (((training_name IS NULL) = (training_request_sha256 IS NULL))),
    CONSTRAINT modeldev_execution_runtimes_check1 CHECK (((training_name IS NULL) OR (training_name = ('md-'::text || (execution_id)::text)))),
    CONSTRAINT modeldev_execution_runtimes_check10 CHECK (((closed_at IS NULL) OR (closed_at >= close_requested_at))),
    CONSTRAINT modeldev_execution_runtimes_check2 CHECK (((training_uid IS NULL) OR (training_name IS NOT NULL))),
    CONSTRAINT modeldev_execution_runtimes_check3 CHECK (((publication_id IS NULL) OR (training_uid IS NOT NULL))),
    CONSTRAINT modeldev_execution_runtimes_check4 CHECK ((NOT (((facts -> 'Training'::text) ->> 'Name'::text) IS DISTINCT FROM training_name))),
    CONSTRAINT modeldev_execution_runtimes_check5 CHECK ((NOT (((facts -> 'Training'::text) ->> 'RequestSHA256'::text) IS DISTINCT FROM training_request_sha256))),
    CONSTRAINT modeldev_execution_runtimes_check6 CHECK ((NOT (((facts -> 'TrainingHandle'::text) ->> 'TrainJobUID'::text) IS DISTINCT FROM training_uid))),
    CONSTRAINT modeldev_execution_runtimes_check7 CHECK ((NOT ((((facts -> 'Publication'::text) ->> 'ID'::text))::uuid IS DISTINCT FROM publication_id))),
    CONSTRAINT modeldev_execution_runtimes_check8 CHECK ((((facts ->> 'CloseGeneration'::text))::numeric = close_generation)),
    CONSTRAINT modeldev_execution_runtimes_check9 CHECK ((((close_generation = (0)::numeric) AND (close_reason IS NULL) AND (close_requested_at IS NULL) AND (closed_at IS NULL)) OR ((close_generation > (0)::numeric) AND (close_reason IS NOT NULL) AND (close_requested_at IS NOT NULL)))),
    CONSTRAINT modeldev_execution_runtimes_close_generation_check CHECK (((close_generation >= (0)::numeric) AND (close_generation <= '18446744073709551615'::numeric))),
    CONSTRAINT modeldev_execution_runtimes_close_reason_check CHECK ((close_reason = ANY (ARRAY['NATURAL_TERMINAL'::text, 'STEP_FAILED'::text, 'DEADLINE'::text, 'USER_STOP'::text]))),
    CONSTRAINT modeldev_execution_runtimes_facts_check CHECK ((jsonb_typeof(facts) = 'object'::text)),
    CONSTRAINT modeldev_execution_runtimes_training_request_sha256_check CHECK ((training_request_sha256 ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT modeldev_execution_runtimes_training_uid_check CHECK (((length(training_uid) >= 1) AND (length(training_uid) <= 128))),
    CONSTRAINT modeldev_gpu_release_anchor CHECK (((((gpu_release_sealed_at IS NULL) AND (gpu_release_scope_sha256 IS NULL) AND (((facts -> 'GPURelease'::text) IS NULL) OR ((facts -> 'GPURelease'::text) = 'null'::jsonb))) OR ((gpu_release_sealed_at IS NOT NULL) AND (gpu_release_scope_sha256 ~ '^[0-9a-f]{64}$'::text) AND (closed_at IS NOT NULL) AND (gpu_release_sealed_at >= closed_at) AND (jsonb_typeof((facts -> 'GPURelease'::text)) = 'object'::text) AND (((facts -> 'GPURelease'::text) ->> 'scope_sha256'::text) = gpu_release_scope_sha256) AND (((((facts -> 'GPURelease'::text) -> 'scope'::text) ->> 'sealed_at'::text))::timestamp with time zone = gpu_release_sealed_at))) IS TRUE))
);


--
-- Name: modeldev_executions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE modeldev_executions (
    tenant_id uuid NOT NULL,
    execution_id uuid NOT NULL,
    operation_id uuid NOT NULL,
    actor text NOT NULL,
    intent_canonical bytea NOT NULL,
    intent_hash text NOT NULL,
    snapshot_canonical bytea NOT NULL,
    spec_hash text NOT NULL,
    accepted_at timestamp with time zone NOT NULL,
    CONSTRAINT modeldev_executions_actor_check CHECK ((actor <> ''::text)),
    CONSTRAINT modeldev_executions_intent_canonical_check CHECK ((octet_length(intent_canonical) > 0)),
    CONSTRAINT modeldev_executions_intent_hash_check CHECK ((intent_hash ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT modeldev_executions_snapshot_canonical_check CHECK ((octet_length(snapshot_canonical) > 0)),
    CONSTRAINT modeldev_executions_spec_hash_check CHECK ((spec_hash ~ '^[0-9a-f]{64}$'::text))
);


--
-- Name: modeldev_input_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE modeldev_input_versions (
    tenant_id uuid NOT NULL,
    input_version_id uuid NOT NULL,
    request_id uuid NOT NULL,
    actor text NOT NULL,
    requested_at timestamp with time zone NOT NULL,
    storage_connection_id text NOT NULL,
    bucket text NOT NULL,
    approved_prefix text NOT NULL,
    object_key text NOT NULL,
    object_version_id text NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 text NOT NULL,
    state text DEFAULT 'VALIDATING'::text NOT NULL,
    credential_reference text DEFAULT ''::text NOT NULL,
    verified_at timestamp with time zone,
    verified_schema_version text,
    verified_row_count bigint,
    prepared_format text,
    prepared_transform_id text,
    prepared_size_bytes bigint,
    prepared_sha256 text,
    prepared_row_count bigint,
    verified_feature_count integer,
    failure_code text,
    failure_observed_at timestamp with time zone,
    format text NOT NULL,
    CONSTRAINT modeldev_input_versions_actor_check CHECK ((actor <> ''::text)),
    CONSTRAINT modeldev_input_versions_approved_prefix_check CHECK ((approved_prefix <> ''::text)),
    CONSTRAINT modeldev_input_versions_bucket_check CHECK ((bucket <> ''::text)),
    CONSTRAINT modeldev_input_versions_failure_check CHECK ((((state = ANY (ARRAY['VALIDATING'::text, 'READY'::text])) AND (failure_code IS NULL) AND (failure_observed_at IS NULL)) OR ((failure_code IS NOT NULL) AND (failure_observed_at IS NOT NULL) AND isfinite(failure_observed_at) AND (failure_observed_at >= requested_at) AND (((state = 'VALIDATING'::text) AND (failure_code = 'SOURCE_UNAVAILABLE'::text)) OR ((state = 'REJECTED'::text) AND (failure_code = 'CONTENT_REJECTED'::text)))))),
    CONSTRAINT modeldev_input_versions_format_check CHECK ((format = ANY (ARRAY['CSV'::text, 'JSON'::text]))),
    CONSTRAINT modeldev_input_versions_input_version_id_check CHECK ((input_version_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT modeldev_input_versions_object_key_check CHECK ((object_key <> ''::text)),
    CONSTRAINT modeldev_input_versions_object_version_id_check CHECK (((object_version_id <> ''::text) AND (lower(object_version_id) <> 'null'::text))),
    CONSTRAINT modeldev_input_versions_request_id_check CHECK ((request_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT modeldev_input_versions_sha256_check CHECK ((sha256 ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT modeldev_input_versions_size_bytes_check CHECK ((size_bytes > 0)),
    CONSTRAINT modeldev_input_versions_storage_connection_id_check CHECK ((storage_connection_id <> ''::text)),
    CONSTRAINT modeldev_input_versions_tenant_id_check CHECK ((tenant_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT modeldev_input_versions_verification_check CHECK (
      (state IN ('VALIDATING','REJECTED') AND verified_at IS NULL
       AND verified_schema_version IS NULL AND verified_row_count IS NULL
       AND verified_feature_count IS NULL AND prepared_format IS NULL
       AND prepared_transform_id IS NULL AND prepared_size_bytes IS NULL
       AND prepared_sha256 IS NULL AND prepared_row_count IS NULL)
      OR
      (state = 'READY' AND verified_at IS NOT NULL AND isfinite(verified_at)
       AND verified_at >= requested_at AND verified_schema_version IS NOT NULL
       AND verified_row_count IS NOT NULL AND verified_row_count > 0
       AND verified_feature_count IS NOT NULL AND prepared_format IS NOT NULL
       AND prepared_transform_id IS NOT NULL AND prepared_size_bytes IS NOT NULL
       AND prepared_size_bytes > 0 AND prepared_size_bytes <= size_bytes
       AND prepared_sha256 IS NOT NULL AND prepared_sha256 ~ '^[0-9a-f]{64}$'
       AND prepared_row_count IS NOT NULL AND prepared_row_count = verified_row_count
       AND ((format = 'CSV' AND verified_schema_version = 'ani.cpu.csv.v1'
             AND verified_feature_count = 16 AND prepared_format = 'CPU_CSV'
             AND prepared_transform_id = 'csv-identity-v3'
             AND prepared_size_bytes = size_bytes AND prepared_sha256 = sha256)
         OR (format = 'JSON' AND verified_schema_version = 'ani.finetune.alpaca.v1'
             AND verified_feature_count = 0 AND prepared_format = 'ALPACA_JSONL'
             AND prepared_transform_id = 'alpaca-array-compact-jsonl-v3')))
    )
);


--
-- Name: modeldev_pipeline_confirmed_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE modeldev_pipeline_confirmed_runs (
    tenant_id uuid NOT NULL,
    execution_id uuid NOT NULL,
    attempt_id uuid NOT NULL,
    plan_hash text NOT NULL,
    run_id uuid NOT NULL,
    first_observed_at timestamp with time zone NOT NULL,
    CONSTRAINT modeldev_pipeline_confirmed_runs_attempt_id_check CHECK ((attempt_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT modeldev_pipeline_confirmed_runs_first_observed_at_check CHECK ((isfinite(first_observed_at) AND (first_observed_at >= '0001-01-01 00:00:00+00'::timestamp with time zone) AND (first_observed_at < '10000-01-01 00:00:00+00'::timestamp with time zone))),
    CONSTRAINT modeldev_pipeline_confirmed_runs_plan_hash_check CHECK ((plan_hash ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT modeldev_pipeline_confirmed_runs_run_id_check CHECK ((run_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);


--
-- Name: modeldev_pipeline_dispatches; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE modeldev_pipeline_dispatches (
    tenant_id uuid NOT NULL,
    execution_id uuid NOT NULL,
    operation_id uuid NOT NULL,
    spec_hash text NOT NULL,
    attempt_id uuid NOT NULL,
    plan_canonical bytea NOT NULL,
    plan_hash text NOT NULL,
    state text NOT NULL,
    reserved_at timestamp with time zone NOT NULL,
    uncertain_at timestamp with time zone,
    not_sent_at timestamp with time zone,
    CONSTRAINT modeldev_pipeline_dispatches_attempt_id_check CHECK ((attempt_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT modeldev_pipeline_dispatches_not_sent_check CHECK ((((not_sent_at IS NULL) OR (isfinite(not_sent_at) AND (not_sent_at >= reserved_at) AND (not_sent_at >= '0001-01-01 00:00:00+00'::timestamp with time zone) AND (not_sent_at < '10000-01-01 00:00:00+00'::timestamp with time zone))) AND (((state = 'SUBMITTING'::text) AND (not_sent_at IS NULL)) OR ((state = 'SUBMISSION_NOT_SENT'::text) AND (not_sent_at IS NOT NULL)) OR (state = ANY (ARRAY['SUBMISSION_UNCERTAIN'::text, 'SUBMISSION_CONFIRMED'::text]))))),
    CONSTRAINT modeldev_pipeline_dispatches_plan_canonical_check CHECK ((octet_length(plan_canonical) > 0)),
    CONSTRAINT modeldev_pipeline_dispatches_plan_hash_check CHECK ((plan_hash ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT modeldev_pipeline_dispatches_reserved_at_check CHECK (isfinite(reserved_at)),
    CONSTRAINT modeldev_pipeline_dispatches_spec_hash_check CHECK ((spec_hash ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT modeldev_pipeline_dispatches_state_check CHECK ((state = ANY (ARRAY['SUBMITTING'::text, 'SUBMISSION_NOT_SENT'::text, 'SUBMISSION_UNCERTAIN'::text, 'SUBMISSION_CONFIRMED'::text]))),
    CONSTRAINT modeldev_pipeline_dispatches_uncertainty_check CHECK ((((uncertain_at IS NULL) OR (isfinite(uncertain_at) AND (uncertain_at >= reserved_at) AND (uncertain_at >= '0001-01-01 00:00:00+00'::timestamp with time zone) AND (uncertain_at < '10000-01-01 00:00:00+00'::timestamp with time zone))) AND (((state = ANY (ARRAY['SUBMITTING'::text, 'SUBMISSION_NOT_SENT'::text])) AND (uncertain_at IS NULL)) OR ((state = 'SUBMISSION_UNCERTAIN'::text) AND (uncertain_at IS NOT NULL)) OR (state = 'SUBMISSION_CONFIRMED'::text))))
);


--
-- Name: modeldev_run_authorities; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE modeldev_run_authorities (
    tenant_id uuid NOT NULL,
    execution_id uuid NOT NULL,
    operation_id uuid NOT NULL,
    spec_hash text NOT NULL,
    attempt_id uuid NOT NULL,
    plan_hash text NOT NULL,
    run_id uuid NOT NULL,
    namespace_name text NOT NULL,
    namespace_uid uuid NOT NULL,
    workflow_name text NOT NULL,
    workflow_uid text NOT NULL,
    bound_at timestamp with time zone NOT NULL,
    CONSTRAINT modeldev_run_authorities_attempt_id_check CHECK ((attempt_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT modeldev_run_authorities_bound_at_check CHECK ((isfinite(bound_at) AND (bound_at >= '0001-01-01 00:00:00+00'::timestamp with time zone) AND (bound_at < '10000-01-01 00:00:00+00'::timestamp with time zone))),
    CONSTRAINT modeldev_run_authorities_namespace_name_check CHECK (((length(namespace_name) >= 1) AND (length(namespace_name) <= 63))),
    CONSTRAINT modeldev_run_authorities_namespace_uid_check CHECK ((namespace_uid <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT modeldev_run_authorities_plan_hash_check CHECK ((plan_hash ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT modeldev_run_authorities_run_id_check CHECK ((run_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT modeldev_run_authorities_spec_hash_check CHECK ((spec_hash ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT modeldev_run_authorities_workflow_name_check CHECK (((length(workflow_name) >= 1) AND (length(workflow_name) <= 253))),
    CONSTRAINT modeldev_run_authorities_workflow_uid_check CHECK (((length(workflow_uid) >= 1) AND (length(workflow_uid) <= 128)))
);


--
-- Name: modeldev_close_intents modeldev_close_intents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_close_intents
    ADD CONSTRAINT modeldev_close_intents_pkey PRIMARY KEY (tenant_id, execution_id, source_kind, source_generation);


--
-- Name: modeldev_close_intents modeldev_close_intents_tenant_id_execution_id_owner_generat_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_close_intents
    ADD CONSTRAINT modeldev_close_intents_tenant_id_execution_id_owner_generat_key UNIQUE (tenant_id, execution_id, owner_generation);


--
-- Name: modeldev_execution_cleanup_audits modeldev_execution_cleanup_audits_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_execution_cleanup_audits
    ADD CONSTRAINT modeldev_execution_cleanup_audits_pkey PRIMARY KEY (tenant_id, execution_id, plan_sha256);


--
-- Name: modeldev_execution_identities modeldev_execution_identities_execution_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_execution_identities
    ADD CONSTRAINT modeldev_execution_identities_execution_id_key UNIQUE (execution_id);


--
-- Name: modeldev_execution_identities modeldev_execution_identities_operation_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_execution_identities
    ADD CONSTRAINT modeldev_execution_identities_operation_id_key UNIQUE (operation_id);


--
-- Name: modeldev_execution_identities modeldev_execution_identities_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_execution_identities
    ADD CONSTRAINT modeldev_execution_identities_pkey PRIMARY KEY (tenant_id, execution_id);


--
-- Name: modeldev_execution_identities modeldev_execution_identities_tenant_id_execution_id_operat_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_execution_identities
    ADD CONSTRAINT modeldev_execution_identities_tenant_id_execution_id_operat_key UNIQUE (tenant_id, execution_id, operation_id, spec_hash);


--
-- Name: modeldev_execution_runtimes modeldev_execution_runtimes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_execution_runtimes
    ADD CONSTRAINT modeldev_execution_runtimes_pkey PRIMARY KEY (tenant_id, execution_id);


--
-- Name: modeldev_executions modeldev_executions_execution_id_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_executions
    ADD CONSTRAINT modeldev_executions_execution_id_unique UNIQUE (execution_id);


--
-- Name: modeldev_executions modeldev_executions_operation_id_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_executions
    ADD CONSTRAINT modeldev_executions_operation_id_unique UNIQUE (operation_id);


--
-- Name: modeldev_executions modeldev_executions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_executions
    ADD CONSTRAINT modeldev_executions_pkey PRIMARY KEY (tenant_id, execution_id);


--
-- Name: modeldev_executions modeldev_executions_tenant_id_operation_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_executions
    ADD CONSTRAINT modeldev_executions_tenant_id_operation_id_key UNIQUE (tenant_id, operation_id);


--
-- Name: modeldev_input_versions modeldev_input_versions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_input_versions
    ADD CONSTRAINT modeldev_input_versions_pkey PRIMARY KEY (tenant_id, input_version_id);


--
-- Name: modeldev_input_versions modeldev_input_versions_tenant_id_request_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_input_versions
    ADD CONSTRAINT modeldev_input_versions_tenant_id_request_id_key UNIQUE (tenant_id, request_id);


--
-- Name: modeldev_pipeline_confirmed_runs modeldev_pipeline_confirmed_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_pipeline_confirmed_runs
    ADD CONSTRAINT modeldev_pipeline_confirmed_runs_pkey PRIMARY KEY (tenant_id, execution_id, attempt_id, run_id);


--
-- Name: modeldev_pipeline_dispatches modeldev_pipeline_dispatches_attempt_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_pipeline_dispatches
    ADD CONSTRAINT modeldev_pipeline_dispatches_attempt_id_key UNIQUE (attempt_id);


--
-- Name: modeldev_pipeline_dispatches modeldev_pipeline_dispatches_observation_identity_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_pipeline_dispatches
    ADD CONSTRAINT modeldev_pipeline_dispatches_observation_identity_key UNIQUE (tenant_id, execution_id, attempt_id, plan_hash);


--
-- Name: modeldev_pipeline_dispatches modeldev_pipeline_dispatches_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_pipeline_dispatches
    ADD CONSTRAINT modeldev_pipeline_dispatches_pkey PRIMARY KEY (tenant_id, execution_id);


--
-- Name: modeldev_run_authorities modeldev_run_authorities_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_run_authorities
    ADD CONSTRAINT modeldev_run_authorities_pkey PRIMARY KEY (tenant_id, execution_id);


--
-- Name: modeldev_executions_tenant_accepted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX modeldev_executions_tenant_accepted ON modeldev_executions USING btree (tenant_id, accepted_at DESC, execution_id DESC);


--
-- Name: modeldev_execution_runtimes modeldev_gpu_release_scope_immutable; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER modeldev_gpu_release_scope_immutable BEFORE UPDATE ON modeldev_execution_runtimes FOR EACH ROW EXECUTE FUNCTION modeldev_gpu_release_scope_immutable();


--
-- Name: modeldev_close_intents modeldev_close_intents_tenant_id_execution_id_operation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_close_intents
    ADD CONSTRAINT modeldev_close_intents_tenant_id_execution_id_operation_id_fkey FOREIGN KEY (tenant_id, execution_id, operation_id, spec_hash) REFERENCES modeldev_execution_identities(tenant_id, execution_id, operation_id, spec_hash);


--
-- Name: modeldev_execution_cleanup_audits modeldev_execution_cleanup_audits_tenant_id_execution_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_execution_cleanup_audits
    ADD CONSTRAINT modeldev_execution_cleanup_audits_tenant_id_execution_id_fkey FOREIGN KEY (tenant_id, execution_id) REFERENCES modeldev_execution_identities(tenant_id, execution_id);


--
-- Name: modeldev_execution_runtimes modeldev_execution_runtimes_tenant_id_execution_id_operati_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_execution_runtimes
    ADD CONSTRAINT modeldev_execution_runtimes_tenant_id_execution_id_operati_fkey FOREIGN KEY (tenant_id, execution_id, operation_id, spec_hash) REFERENCES modeldev_execution_identities(tenant_id, execution_id, operation_id, spec_hash);


--
-- Name: modeldev_executions modeldev_executions_identity_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_executions
    ADD CONSTRAINT modeldev_executions_identity_fk FOREIGN KEY (tenant_id, execution_id, operation_id, spec_hash) REFERENCES modeldev_execution_identities(tenant_id, execution_id, operation_id, spec_hash);


--
-- Name: modeldev_pipeline_confirmed_runs modeldev_pipeline_confirmed_r_tenant_id_execution_id_attem_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_pipeline_confirmed_runs
    ADD CONSTRAINT modeldev_pipeline_confirmed_r_tenant_id_execution_id_attem_fkey FOREIGN KEY (tenant_id, execution_id, attempt_id, plan_hash) REFERENCES modeldev_pipeline_dispatches(tenant_id, execution_id, attempt_id, plan_hash);


--
-- Name: modeldev_pipeline_dispatches modeldev_pipeline_dispatches_tenant_id_execution_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_pipeline_dispatches
    ADD CONSTRAINT modeldev_pipeline_dispatches_tenant_id_execution_id_fkey FOREIGN KEY (tenant_id, execution_id) REFERENCES modeldev_executions(tenant_id, execution_id);


--
-- Name: modeldev_pipeline_dispatches modeldev_pipeline_dispatches_tenant_id_execution_id_operat_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_pipeline_dispatches
    ADD CONSTRAINT modeldev_pipeline_dispatches_tenant_id_execution_id_operat_fkey FOREIGN KEY (tenant_id, execution_id, operation_id, spec_hash) REFERENCES modeldev_execution_identities(tenant_id, execution_id, operation_id, spec_hash);


--
-- Name: modeldev_run_authorities modeldev_run_authorities_tenant_id_execution_id_attempt_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_run_authorities
    ADD CONSTRAINT modeldev_run_authorities_tenant_id_execution_id_attempt_id_fkey FOREIGN KEY (tenant_id, execution_id, attempt_id, plan_hash) REFERENCES modeldev_pipeline_dispatches(tenant_id, execution_id, attempt_id, plan_hash);


--
-- Name: modeldev_run_authorities modeldev_run_authorities_tenant_id_execution_id_operation__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_run_authorities
    ADD CONSTRAINT modeldev_run_authorities_tenant_id_execution_id_operation__fkey FOREIGN KEY (tenant_id, execution_id, operation_id, spec_hash) REFERENCES modeldev_execution_identities(tenant_id, execution_id, operation_id, spec_hash);


--
-- Name: modeldev_execution_runtimes modeldev_runtime_compute_authority_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY modeldev_execution_runtimes
    ADD CONSTRAINT modeldev_runtime_compute_authority_fkey FOREIGN KEY (tenant_id, authority_execution_id) REFERENCES modeldev_run_authorities(tenant_id, execution_id);


--
-- PostgreSQL database dump complete
--

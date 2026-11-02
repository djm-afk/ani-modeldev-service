CREATE TABLE modeldev_tenant_execution_environments (
 environment_id uuid PRIMARY KEY CHECK (environment_id <> '00000000-0000-0000-0000-000000000000'),
 tenant_id uuid NOT NULL CHECK (tenant_id <> '00000000-0000-0000-0000-000000000000'),
 cluster_id uuid NOT NULL CHECK (cluster_id <> '00000000-0000-0000-0000-000000000000'),
 namespace_name text NOT NULL CHECK(namespace_name = 'tenant-' || tenant_id::text),
 namespace_uid uuid,
 state text NOT NULL CHECK(state IN ('PROVISIONING','READY','BLOCKED','NEEDS_REVIEW')),
 phase text NOT NULL CHECK(phase IN ('NAMESPACE','RESOURCES','EXPERIMENT','VERIFY','PUBLISH','COMPLETE')),
 provision_spec_canonical bytea NOT NULL,
 provision_spec_hash char(64) NOT NULL CHECK(provision_spec_hash ~ '^[0-9a-f]{64}$'),
 binding_canonical bytea,
 binding_digest char(64),
 kfp_connection_ref text NOT NULL,
 experiment_name text NOT NULL CHECK(experiment_name = 'md-env-' || environment_id::text),
 experiment_id uuid,
 experiment_create_intent jsonb,
 resource_receipts jsonb NOT NULL DEFAULT '{"objects":[]}'::jsonb CHECK(jsonb_typeof(resource_receipts)='object'),
 first_operation_id uuid NOT NULL,
 last_operation_id uuid NOT NULL,
 first_actor_id text NOT NULL,
 last_actor_id text NOT NULL,
 reason_code text NOT NULL DEFAULT '',
 reason_stage text NOT NULL DEFAULT '',
 row_version bigint NOT NULL DEFAULT 1 CHECK(row_version>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 last_verified_at timestamptz,
 ready_at timestamptz,
 UNIQUE(tenant_id,cluster_id),
 CHECK(namespace_uid IS NULL OR namespace_uid <> '00000000-0000-0000-0000-000000000000'),
 CHECK(experiment_id IS NULL OR experiment_id <> '00000000-0000-0000-0000-000000000000'),
 CHECK((binding_canonical IS NULL) = (binding_digest IS NULL)),
 CHECK(binding_digest IS NULL OR binding_digest ~ '^[0-9a-f]{64}$'),
 CHECK(state <> 'READY' OR (phase='COMPLETE' AND namespace_uid IS NOT NULL AND experiment_id IS NOT NULL AND binding_canonical IS NOT NULL AND last_verified_at IS NOT NULL AND reason_code=''))
);
CREATE UNIQUE INDEX modeldev_environment_namespace_identity ON modeldev_tenant_execution_environments(cluster_id,namespace_uid) WHERE namespace_uid IS NOT NULL;
CREATE UNIQUE INDEX modeldev_environment_experiment_identity ON modeldev_tenant_execution_environments(kfp_connection_ref,experiment_id) WHERE experiment_id IS NOT NULL;
CREATE FUNCTION modeldev_environment_guard_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_receipt jsonb; new_receipt jsonb;
BEGIN
 IF (NEW.environment_id,NEW.tenant_id,NEW.cluster_id,NEW.namespace_name,NEW.provision_spec_canonical,NEW.provision_spec_hash,NEW.kfp_connection_ref,NEW.experiment_name,NEW.first_operation_id,NEW.first_actor_id)
 IS DISTINCT FROM (OLD.environment_id,OLD.tenant_id,OLD.cluster_id,OLD.namespace_name,OLD.provision_spec_canonical,OLD.provision_spec_hash,OLD.kfp_connection_ref,OLD.experiment_name,OLD.first_operation_id,OLD.first_actor_id) THEN
 RAISE EXCEPTION 'immutable tenant execution environment identity'; END IF;
 IF OLD.namespace_uid IS NOT NULL AND NEW.namespace_uid IS DISTINCT FROM OLD.namespace_uid THEN RAISE EXCEPTION 'immutable namespace uid'; END IF;
 IF OLD.experiment_id IS NOT NULL AND NEW.experiment_id IS DISTINCT FROM OLD.experiment_id THEN RAISE EXCEPTION 'immutable experiment id'; END IF;
 IF OLD.binding_canonical IS NOT NULL AND (NEW.binding_canonical,NEW.binding_digest) IS DISTINCT FROM (OLD.binding_canonical,OLD.binding_digest) THEN RAISE EXCEPTION 'immutable sealed environment binding'; END IF;
 FOR old_receipt IN SELECT value FROM jsonb_array_elements(OLD.resource_receipts->'objects') LOOP
   SELECT value INTO new_receipt FROM jsonb_array_elements(NEW.resource_receipts->'objects')
    WHERE value->>'kind'=old_receipt->>'kind' AND value->>'name'=old_receipt->>'name';
   IF new_receipt IS NULL OR new_receipt->>'spec_digest' IS DISTINCT FROM old_receipt->>'spec_digest'
      OR (COALESCE(old_receipt->>'uid','')<>'' AND new_receipt->>'uid' IS DISTINCT FROM old_receipt->>'uid') THEN
     RAISE EXCEPTION 'immutable environment object receipt identity';
   END IF;
 END LOOP;
 RETURN NEW;
END $$;
CREATE TRIGGER modeldev_environment_immutable BEFORE UPDATE ON modeldev_tenant_execution_environments FOR EACH ROW EXECUTE FUNCTION modeldev_environment_guard_immutable();

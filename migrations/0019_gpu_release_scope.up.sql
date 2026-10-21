-- First physical-release scope is an immutable part of the existing aggregate.
BEGIN;
ALTER TABLE modeldev_execution_runtimes
 ADD COLUMN gpu_release_sealed_at timestamptz,
 ADD COLUMN gpu_release_scope_sha256 text,
 ADD CONSTRAINT modeldev_gpu_release_anchor CHECK ((
  (gpu_release_sealed_at IS NULL AND gpu_release_scope_sha256 IS NULL
   AND (facts->'GPURelease' IS NULL OR facts->'GPURelease'='null'::jsonb))
  OR (gpu_release_sealed_at IS NOT NULL
   AND gpu_release_scope_sha256 ~ '^[0-9a-f]{64}$'
   AND closed_at IS NOT NULL AND gpu_release_sealed_at >= closed_at
   AND jsonb_typeof(facts->'GPURelease')='object'
   AND facts->'GPURelease'->>'scope_sha256'=gpu_release_scope_sha256
   AND (facts->'GPURelease'->'scope'->>'sealed_at')::timestamptz=gpu_release_sealed_at)
 ) IS TRUE);

CREATE FUNCTION modeldev_gpu_release_scope_immutable() RETURNS trigger
LANGUAGE plpgsql AS $body$
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
END $body$;
CREATE TRIGGER modeldev_gpu_release_scope_immutable
 BEFORE UPDATE ON modeldev_execution_runtimes
 FOR EACH ROW EXECUTE FUNCTION modeldev_gpu_release_scope_immutable();
COMMIT;

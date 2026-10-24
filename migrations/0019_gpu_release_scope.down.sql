BEGIN;
DO $body$
BEGIN
 IF EXISTS (SELECT 1 FROM modeldev_execution_runtimes WHERE gpu_release_sealed_at IS NOT NULL) THEN
  RAISE EXCEPTION 'GPU release facts require retained compatible schema';
 END IF;
END $body$;
DROP TRIGGER modeldev_gpu_release_scope_immutable ON modeldev_execution_runtimes;
DROP FUNCTION modeldev_gpu_release_scope_immutable();
ALTER TABLE modeldev_execution_runtimes DROP CONSTRAINT modeldev_gpu_release_anchor,
 DROP COLUMN gpu_release_sealed_at, DROP COLUMN gpu_release_scope_sha256;
COMMIT;

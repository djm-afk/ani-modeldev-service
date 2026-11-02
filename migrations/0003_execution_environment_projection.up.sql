-- Applied after the explicit v3 drain/cleanup. No defaults or old-row backfill.
ALTER TABLE modeldev_executions
 ADD COLUMN cluster_id uuid NOT NULL,
 ADD COLUMN environment_binding_id uuid NOT NULL,
 ADD COLUMN deadline_at timestamptz NOT NULL;
CREATE INDEX modeldev_executions_environment_scan
 ON modeldev_executions(cluster_id, tenant_id, accepted_at, execution_id);

CREATE FUNCTION public.modeldev_scan_snapshot_jsonb(raw_snapshot bytea)
RETURNS jsonb LANGUAGE plpgsql IMMUTABLE STRICT SECURITY INVOKER
SET search_path = pg_catalog
AS $modeldev_scan$
DECLARE parsed jsonb;
BEGIN
 IF octet_length(raw_snapshot)=0 OR octet_length(raw_snapshot)>8388608 THEN RETURN NULL; END IF;
 parsed := convert_from(raw_snapshot,'UTF8')::jsonb;
 IF jsonb_typeof(parsed) IS DISTINCT FROM 'object' THEN RETURN NULL; END IF;
 RETURN parsed;
EXCEPTION WHEN data_exception OR program_limit_exceeded THEN RETURN NULL;
END;
$modeldev_scan$;

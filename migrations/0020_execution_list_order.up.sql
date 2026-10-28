-- A list page is ordered by acceptance time, so the scan walks
-- (accepted_at DESC, execution_id DESC) and needs an index in that shape.
-- Without it the owner would sort the whole tenant on every page.
CREATE INDEX modeldev_executions_tenant_accepted
 ON modeldev_executions (tenant_id, accepted_at DESC, execution_id DESC);

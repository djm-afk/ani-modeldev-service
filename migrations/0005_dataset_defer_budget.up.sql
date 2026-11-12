-- Automatic retry budget for dataset imports.
--
-- recovery_generation only advances on an explicit user retry (design §9.3), so
-- it cannot bound the background retries the scheduler performs on transient
-- failures. Without a separate counter the scheduler either retried forever or
-- stopped after a fixed number of sweeps that had nothing to do with the task.
--
-- defer_count counts automatic deferrals in the current recovery cycle. An
-- explicit retry resets it, which is exactly the "new cycle, new budget"
-- semantics the design gives recovery_generation.
ALTER TABLE modeldev_dataset_import_tasks
    ADD COLUMN defer_count integer NOT NULL DEFAULT 0 CHECK (defer_count >= 0);

-- The runtime role may only write the columns it actually owns. defer_count is
-- written by DeferTask and reset by RequeueRetryableTask, both runtime paths.
DO $dataset_defer_budget$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'modeldev_v21_runtime') THEN
        EXECUTE 'GRANT UPDATE (defer_count) ON modeldev_dataset_import_tasks TO modeldev_v21_runtime';
    END IF;
END
$dataset_defer_budget$;

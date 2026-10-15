-- Keep all existing CSV rows and constraints. JSON READY comes only from the
-- trusted verifier; the Release applies its own sample-count requirement.
ALTER TABLE modeldev_input_versions
    ADD COLUMN format text NOT NULL DEFAULT 'CSV' CHECK (format IN ('CSV', 'JSON')),
    DROP CONSTRAINT modeldev_input_versions_verification_check,
    ADD CONSTRAINT modeldev_input_versions_verification_check CHECK (
        (state IN ('VALIDATING', 'REJECTED')
            AND verified_at IS NULL AND verified_schema_version IS NULL
            AND verified_row_count IS NULL AND verified_feature_count IS NULL)
        OR
        (state = 'READY'
            AND verified_at IS NOT NULL AND isfinite(verified_at)
            AND verified_at >= requested_at
            AND verified_schema_version IS NOT NULL
            AND verified_row_count IS NOT NULL
            AND verified_feature_count IS NOT NULL
            AND ((format = 'CSV' AND verified_schema_version = 'ani.cpu.csv.v1'
                    AND verified_row_count = 1024 AND verified_feature_count = 16)
                OR (format = 'JSON' AND verified_schema_version = 'ani.finetune.alpaca.v1'
                    AND verified_row_count > 0 AND verified_feature_count = 0)))
    );

-- Keep fixed-byte verification, tenant ownership and the registered schemas.
-- Sample counts are input facts, not a production training-program constant.
ALTER TABLE modeldev_input_versions
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
            AND verified_row_count IS NOT NULL AND verified_row_count > 0
            AND verified_feature_count IS NOT NULL
            AND ((format = 'CSV' AND verified_schema_version = 'ani.cpu.csv.v1'
                    AND verified_feature_count = 16)
                OR (format = 'JSON' AND verified_schema_version = 'ani.finetune.alpaca.v1'
                    AND verified_feature_count = 0)))
    );

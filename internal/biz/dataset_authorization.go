package biz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// ErrImportEnvironmentMissing reports a trusted target that cannot be built:
// a missing or non-standard Namespace, an unusable input scope, or a registry
// row that blocks new work. It is not an authorization failure.
var ErrImportEnvironmentMissing = errors.New("IMPORT_ENVIRONMENT_MISSING")

const ImportAuthorizationSchema = "ani.modeldev.dataset.import-authorization.v1"

// ImportAllowedActions is the fixed uploader whitelist frozen into a task. It is
// never taken from a user or an IAM policy document.
var ImportAllowedActions = []string{"s3:GetObject", "s3:PutObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"}

// DatasetImportTargetReader builds the frozen import target for a tenant. It
// observes the standard Namespace and the platform input scope; it never
// provisions anything and never calls the training admission reader.
type DatasetImportTargetReader interface {
	ReadDatasetImportTarget(ctx context.Context, tenantID string) (DatasetImportTarget, error)
}

// NewImportAuthorizationScope is the canonical authorization snapshot stored on
// the task. Fields are exactly schema/tenant/scope digest/purpose/actions.
func NewImportAuthorizationScope(tenantID, inputScopeSHA256 string) []byte {
	return []byte(`{"schema":"` + ImportAuthorizationSchema + `","tenant_id":"` + tenantID +
		`","input_scope_sha256":"` + inputScopeSHA256 + `","purpose":"dataset-import","allowed_actions":[` +
		quoted(ImportAllowedActions) + `]}`)
}

func quoted(values []string) string {
	result := ""
	for index, value := range values {
		if index > 0 {
			result += ","
		}
		result += `"` + value + `"`
	}
	return result
}

// NewImportAuthorizationScopeSHA256 is the digest of that snapshot. The database
// CHECK compares the stored JSON against the typed columns; this digest is what
// the task row carries for later comparison.
func NewImportAuthorizationScopeSHA256(tenantID, inputScopeSHA256 string) string {
	sum := sha256.Sum256(NewImportAuthorizationScope(tenantID, inputScopeSHA256))
	return hex.EncodeToString(sum[:])
}

// DefaultRecoveryPolicy is the per-cycle retry budget. It is operations policy,
// not a content limit, and a restart never resets it.
func DefaultRecoveryPolicy() []byte {
	return []byte(`{"max_download_attempts":3,"max_validation_retries":2,"validation_timeout_seconds":1800,"backoff_seconds":30}`)
}

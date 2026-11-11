package biz

import (
	"context"
	"time"
)

// DatasetWorkerRepository is the persistence the worker needs. Every method is
// a short transaction of its own; no method here performs external I/O.
type DatasetWorkerRepository interface {
	// ListDueTasks returns tasks whose next_run_at has passed and whose lease is
	// free. Terminal tasks are excluded; their work is attempt reconciliation.
	ListDueTasks(ctx context.Context, now time.Time, batch int32) ([]DatasetImportTask, error)
	// AcquireTaskLease takes or renews the task lease and returns the new epoch.
	AcquireTaskLease(ctx context.Context, tenantID, taskID, owner string, ttl time.Duration) (DatasetImportTask, error)
	// ReleaseTaskLease drops the lease this worker still holds. A held lease is
	// skipped by ListDueTasks until it expires, so a step that finishes must
	// release it instead of waiting out the TTL.
	ReleaseTaskLease(ctx context.Context, tenantID, taskID, owner string, epoch int64) error

	// CountRunningAttempts is the quota observation, read under the capacity lock.
	CountRunningAttempts(ctx context.Context) (int64, error)
	InsertAttempt(ctx context.Context, attempt DatasetImportAttempt) (DatasetImportAttempt, error)
	SetCurrentAttempt(ctx context.Context, tenantID, taskID, attemptID string) (DatasetImportTask, error)

	// ListDueAttempts scans reconciliation independently of the parent task state.
	ListDueAttempts(ctx context.Context, now time.Time, batch int32) ([]DatasetImportAttempt, error)
	// GetAttempt reads one attempt by id; recovery must re-read the row rather
	// than trust an in-memory copy.
	GetAttempt(ctx context.Context, tenantID, attemptID string) (DatasetImportAttempt, error)
	AcquireAttemptLease(ctx context.Context, tenantID, attemptID, owner string, ttl time.Duration) (DatasetImportAttempt, error)
	ClaimAttemptCleanup(ctx context.Context, tenantID, attemptID string) (DatasetImportAttempt, error)
	FinishAttemptCleanup(ctx context.Context, tenantID, attemptID string) (DatasetImportAttempt, error)
	FreezeCandidate(ctx context.Context, tenantID, attemptID, versionID, sha256 string) (DatasetImportAttempt, error)
}

// DatasetImportAttempt is one physical execution of a task. Its runtime plan and
// object keys are frozen at creation.
type DatasetImportAttempt struct {
	TenantID           string
	AttemptID          string
	DatasetID          string
	TaskID             string
	AttemptNo          int64
	RecoveryGeneration int64
	ClusterID          string
	NamespaceName      string
	NamespaceUID       string
	JobName            string
	PVCName            string
	SecretName         string
	JobUID             string
	JobResourceVersion string
	RuntimePlan        []byte
	RuntimePlanSHA256  string
	DataObjectKey      string
	ResultObjectKey    string
	DeadlineAt         time.Time
	CleanupState       string
	WritersStoppedAt   *time.Time
	DispatchClosedAt   *time.Time
	ReconcileOwner     string
	ReconcileEpoch     int64
	ReconcileUntil     *time.Time
	ReconcileNextAt    *time.Time
	ResultVersionID    string
	ResultSHA256       string
}

// ResourcePlan is the frozen importer material identity for one attempt. It is
// validated by the RuntimePolicy, never by the training profile set.
type ResourcePlan struct {
	ImporterImageDigest string
	ServiceAccountName  string
	ServiceAccountUID   string
	PullSecretRefs      []string
	CPURequest          string
	CPULimit            string
	MemoryRequest       string
	MemoryLimit         string
	StorageClass        string
	DeadlineSeconds     int64
	RuntimePolicySHA256 string
}

// ClaimsSlot reports whether taking one more attempt would exceed the limit.
// The caller holds the capacity lock while it decides, so two workers cannot
// both see the last free slot.
func ClaimsSlot(limit int, running int64) error {
	return QuotaSlot{Limit: limit, Running: int(running)}.Claim()
}

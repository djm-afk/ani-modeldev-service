package biz

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrImportWorkerConfiguration reports an unusable scheduler assembly.
	ErrImportWorkerConfiguration = errors.New("INVALID_IMPORT_WORKER_CONFIGURATION")
	// ErrImportDispatchUnavailable keeps a transport failure distinct from a
	// content verdict: the source may be fine and only the cluster unreachable.
	ErrImportDispatchUnavailable = errors.New("IMPORT_DISPATCH_UNAVAILABLE")
)

// CandidateFreeze is the one-way transition that pins a task to a verified
// attempt. Every field is an already-proven fact; this write only records it.
type CandidateFreeze struct {
	TenantID  string
	TaskID    string
	AttemptID string

	ObjectKey string
	VersionID string
	SizeBytes int64
	SHA256    string

	ReportVersionID string
	ReportSHA256    string
}

// TaskFailure is a terminal outcome. Retryable decides whether a user may open a
// new recovery cycle; it never means "keep retrying in the background".
type TaskFailure struct {
	TenantID  string
	TaskID    string
	Code      string
	Detail    []byte
	Retryable bool

	// OperationID and Actor identify the IMPORT_FAILED history entry, which is
	// written in the same transaction as the state change.
	OperationID string
	Actor       string
}

// TaskDeferral keeps a live task and pushes its next scan out, recording a
// waiting reason without writing a terminal failure.
type TaskDeferral struct {
	TenantID    string
	TaskID      string
	Delay       time.Duration
	FailureCode string
}

// TaskCancellation is the terminal cancel write. OperationID and Actor identify
// the IMPORT_CANCELLED history entry written in the same transaction.
type TaskCancellation struct {
	TenantID    string
	TaskID      string
	OperationID string
	Actor       string
}

// AttemptPlan is the frozen material identity of one physical attempt. The
// object keys are preallocated, never re-derived from a clock or a retry.
type AttemptPlan struct {
	JobName           string
	PVCName           string
	SecretName        string
	DataObjectKey     string
	ResultObjectKey   string
	DeadlineAt        time.Time
	RuntimePlan       []byte
	RuntimePlanSHA256 string
}

// AttemptAllocationRequest is one new physical attempt inside the caller's
// transaction. The data layer takes the capacity lock, counts live attempts,
// allocates the number and inserts the row, so two replicas cannot both take the
// last slot.
type AttemptAllocationRequest struct {
	Task      DatasetImportTask
	AttemptID string
	// Plan runs inside the allocation transaction, after the attempt number is
	// known. Passing a precomputed plan would force the caller to guess the
	// number, and a retry would then reuse the previous attempt's object keys.
	Plan func(ctx context.Context, attemptNo int64) (AttemptPlan, error)
}

// ImportSessionRequest is the fully checked intent of one credential session.
// It carries the policy digest and no credential value.
type ImportSessionRequest struct {
	TenantID   string
	DatasetID  string
	TaskID     string
	AttemptID  string
	IssuanceID string

	TaskLeaseEpoch int64
	ReconcileEpoch int64
	Policy         []byte
	PolicySHA256   string

	IntendedAt      time.Time
	RequestDeadline time.Time
	AuthDeadline    time.Time
}

// ImportSessionGrant bounds what the session may touch: exactly this attempt's
// data and result keys.
type ImportSessionGrant struct {
	StorageConnectionID string
	Bucket              string
	DataKey             string
	ResultKey           string
}

// ImportSession is one issued session. Issued means a real expiry was proven.
type ImportSession struct {
	Issued      bool
	Generation  int64
	ExpiresAt   time.Time
	Credentials TemporaryStorageCredentials
}

// ImportSessionIssuer is the STS boundary the scheduler sees. The data layer
// implements it over the accounting ledger.
type ImportSessionIssuer interface {
	IssueImportSession(ctx context.Context, request ImportSessionRequest, grant ImportSessionGrant) (ImportSession, error)
}

// ImportSchedulerRepository is the scheduler's persistence. Every method is one
// short transaction and performs no external I/O.
type ImportSchedulerRepository interface {
	// AllocateAttempt runs the whole capacity + attempt-number + insert
	// transaction. ErrImportQuotaExhausted means no slot was taken.
	AllocateAttempt(ctx context.Context, request AttemptAllocationRequest) (DatasetImportAttempt, error)

	StartTask(ctx context.Context, tenantID, taskID, leaseOwner string, leaseEpoch int64) (DatasetImportTask, error)
	// SetCurrentAttempt binds the allocated attempt to the now-RUNNING task.
	SetCurrentAttempt(ctx context.Context, tenantID, taskID, attemptID string) (DatasetImportTask, error)

	FreezeTaskCandidate(ctx context.Context, freeze CandidateFreeze) (DatasetImportTask, error)
	FailTask(ctx context.Context, failure TaskFailure) (DatasetImportTask, error)
	// FinishCancelledTask turns a CANCELING task into its terminal CANCELLED
	// state. It is only called after the attempt's writer has been observed as
	// finished or gone; a missing row is a state conflict, never a no-op.
	FinishCancelledTask(ctx context.Context, cancellation TaskCancellation) (DatasetImportTask, error)
	DeferTask(ctx context.Context, deferral TaskDeferral) error

	CloseAttempt(ctx context.Context, tenantID, attemptID, reason string) (DatasetImportAttempt, error)
	// ConfirmWritersStopped records the one fact that releases a capacity slot.
	// It is only legal after the dispatch gate closed, and only once.
	ConfirmWritersStopped(ctx context.Context, tenantID, attemptID string) (DatasetImportAttempt, error)
	// ClearCurrentAttempt detaches a closed attempt so the task can allocate the
	// next one. SetCurrentAttempt refuses to overwrite an existing binding.
	ClearCurrentAttempt(ctx context.Context, tenantID, taskID, attemptID string) (DatasetImportTask, error)
	// SetTaskEnvironment records the registry row the workload must name in its
	// environment-id label; the native admission policy requires a real one.
	SetTaskEnvironment(ctx context.Context, tenantID, taskID, environmentID string) (DatasetImportTask, error)
	RecordAttemptJob(ctx context.Context, tenantID, attemptID, jobUID, resourceVersion string) (DatasetImportAttempt, error)
	RecordAttemptObservation(ctx context.Context, tenantID, attemptID string, observation []byte) (DatasetImportAttempt, error)
}

// AttemptPlanner builds the material identity for one attempt. It is pure: no
// cluster or storage call happens here, so a plan can be recomputed and compared.
type AttemptPlanner interface {
	PlanAttempt(ctx context.Context, task DatasetImportTask, attemptID string, attemptNo int64) (AttemptPlan, error)
}

// ImportWorkload is the observed result of asking for an importer workload.
type ImportWorkload struct {
	JobUID          string
	ResourceVersion string
	Created         bool
}

// ImportWorkloadStatus is one bounded observation of the importer Job.
type ImportWorkloadStatus struct {
	// Present is false when the Job has disappeared.
	Present       bool
	JobUID        string
	Finished      bool
	Succeeded     bool
	Failed        bool
	PodPhase      string
	FailureReason string
}

// EnvironmentLookup reads the execution-registry row for the tenant. It is the
// only source of the environment-id label the native policy demands.
type EnvironmentLookup interface {
	EnvironmentID(ctx context.Context, tenantID, clusterID string) (string, error)
}

// DatasetImportWorkloadControl is the namespace-side boundary. It creates and
// observes only importer objects and never provisions a shared environment.
type DatasetImportWorkloadControl interface {
	// EnsureImporter creates the suspended Job, its PVC and its credential
	// Secret when absent and returns the real identity. A same-named object with
	// a different UID or plan is a conflict, never an adoption.
	EnsureImporter(ctx context.Context, attempt DatasetImportAttempt, plan AttemptPlan, task DatasetImportTask, credentials TemporaryStorageCredentials) (ImportWorkload, error)
	// ResumeImporter unsuspends exactly the observed Job UID.
	ResumeImporter(ctx context.Context, attempt DatasetImportAttempt, jobUID string) error
	// ObserveImporter reports the bounded Job status.
	ObserveImporter(ctx context.Context, attempt DatasetImportAttempt) (ImportWorkloadStatus, error)
	// DeleteImporter removes the attempt-owned objects. It is idempotent.
	DeleteImporter(ctx context.Context, attempt DatasetImportAttempt) error
}

// ImportReport is the decoded report plus the immutable identity of the report
// object itself. The task's candidate columns require both.
type ImportReport struct {
	Result    ImportResult
	VersionID string
	SHA256    string
}

// ImportObjectReader reads an attempt's fixed result report and re-verifies the
// candidate object in one exact-version read.
type ImportObjectReader interface {
	ReadResult(ctx context.Context, task DatasetImportTask, attempt DatasetImportAttempt) (ImportReport, error)
	VerifyCandidate(ctx context.Context, task DatasetImportTask, result ImportResult) (VerifiedAlpaca, []byte, error)
}

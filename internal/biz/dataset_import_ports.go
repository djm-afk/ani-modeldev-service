package biz

import (
	"context"
	"time"
)

// StatusImportAccepted, StatusCreated and StatusOK are the three receipt
// response codes the contract allows. A repeat always returns the original.
const (
	ReceiptStatusOK       = 200
	ReceiptStatusCreated  = 201
	ReceiptStatusAccepted = 202
)

// DatasetImportTask is one asynchronous import of a single file into a new
// version. Its source, target, reserved input identity and version number are
// frozen at acceptance and never rewritten by a retry.
type DatasetImportTask struct {
	TenantID               string
	TaskID                 string
	DatasetID              string
	AcceptanceReceiptID    string
	ReservedInputVersionID string
	InputRequestID         string
	VersionNumber          int64
	CreatedBy              string
	AcceptedAt             time.Time

	SourceProvider       string
	SourceSnapshot       []byte
	SourceSnapshotSHA256 string
	ExpectedSizeBytes    int64
	AcceptedLimitBytes   int64

	// EnvironmentID is the execution-registry row used for the workload's
	// environment-id label. Empty means no row was observed.
	EnvironmentID        string
	ClusterID            string
	NamespaceName        string
	NamespaceUID         string
	TargetSnapshot       []byte
	TargetSnapshotSHA256 string
	StorageConnectionID  string
	Bucket               string
	ApprovedPrefix       string
	InputScopeSHA256     string
	CredentialReference  string

	AuthorizationScope       []byte
	AuthorizationScopeSHA256 string

	State              ImportTaskState
	Phase              ImportPhase
	RecoveryGeneration int64
	RecoveryPolicy     []byte

	NextRunAt                time.Time
	LeaseOwner               string
	LeaseEpoch               int64
	CurrentAttemptID         string
	CandidateAttemptID       string
	CandidateObjectKey       string
	CandidateVersionID       string
	CandidateSizeBytes       int64
	CandidateSHA256          string
	CandidateReportVersionID string
	CandidateReportSHA256    string

	CancelRequestedAt *time.Time
	FailureCode       string
	Retryable         bool
	FinishedAt        *time.Time
	PublishedAt       *time.Time
	UpdatedAt         time.Time
}

// CanCancel reports whether a user cancel is still meaningful. A succeeded task
// keeps its version; cancelling then is a stable no-op, not a rollback.
func (task DatasetImportTask) CanCancel() bool {
	return task.State == TaskQueued || task.State == TaskRunning
}

// CanRetry reports whether an explicit retry is legal: only a retryable FAILED
// task whose cancel gate never closed.
func (task DatasetImportTask) CanRetry() bool {
	return task.State == TaskFailed && task.Retryable && task.CancelRequestedAt == nil
}

// DatasetImportTaskRepository persists tasks. Storage and publication of the
// InputVersion happen through the input repository inside the same transaction
// the caller owns.
type DatasetImportTaskRepository interface {
	GetImportTask(ctx context.Context, tenantID, taskID string) (DatasetImportTask, error)
	ListImportTasks(ctx context.Context, filter DatasetImportFilter) (DatasetImportTaskPage, error)
}

type DatasetImportFilter struct {
	TenantID  string
	DatasetID string
	State     string
	Page      int32
	PageSize  int32
}

type DatasetImportTaskPage struct {
	Total int64
	Items []DatasetImportTask
}

// DatasetVersion is the dataset-side index of one published InputVersion. It
// never copies the source snapshot: that is read from the permanent task row.
type DatasetVersion struct {
	TenantID       string
	InputVersionID string
	DatasetID      string
	ImportTaskID   string
	VersionNumber  int64
	Preview        []byte
	PublishedAt    time.Time
}

type DatasetVersionRepository interface {
	GetVersion(ctx context.Context, tenantID, inputVersionID string) (DatasetVersion, error)
	ListVersions(ctx context.Context, filter DatasetVersionFilter) (DatasetVersionPage, error)
}

type DatasetVersionFilter struct {
	TenantID  string
	DatasetID string
	Page      int32
	PageSize  int32
}

type DatasetVersionPage struct {
	Total int64
	Items []DatasetVersion
}

// DatasetAcceptanceRepository owns the whole acceptance transaction. One method
// keeps the receipt, the version-number reservation, the input identity
// reservation, the task row and the operation event in a single commit; a
// per-step API would let a caller accidentally commit half of it.
type DatasetAcceptanceRepository interface {
	// AcceptImport performs the short acceptance transaction and returns the
	// original receipt on replay. A duplicate idempotency key with a different
	// intent returns ErrIdempotencyConflict.
	AcceptImport(ctx context.Context, request AcceptanceRequest) (AcceptanceResult, error)
}

// AcceptanceRequest is the fully checked input of one acceptance. Source and
// target facts are already frozen by the caller; this transaction only reserves
// identity, checks the dataset is still ACTIVE and writes the rows.
type AcceptanceRequest struct {
	TenantID       string
	ActorID        string
	DatasetID      string
	IdempotencyKey string
	RequestSHA256  string

	TaskID                 string
	ReceiptID              string
	ReservedInputVersionID string
	InputRequestID         string
	CreatedBy              string
	AcceptedAt             time.Time

	SourceProvider       string
	SourceSnapshot       []byte
	SourceSnapshotSHA256 string
	ExpectedSizeBytes    int64
	AcceptedLimitBytes   int64

	Target               DatasetImportTarget
	TargetSnapshot       []byte
	TargetSnapshotSHA256 string

	AuthorizationScope       []byte
	AuthorizationScopeSHA256 string
	RecoveryPolicy           []byte
}

// AcceptanceResult is what the caller returns to the client unchanged.
type AcceptanceResult struct {
	Receipt        CommandReceipt
	Task           DatasetImportTask
	Replayed       bool
	ResponseStatus int
}

// DatasetPublicationRepository owns the whole publication transaction: the
// READY input proof, the version metadata row and the task transition commit
// together or not at all.
type DatasetPublicationRepository interface {
	// ReserveImport writes the immutable frozen import row that the publication
	// transaction later locks. Publication fails with INPUT_VERSION_NOT_FOUND
	// when that row is missing, and reserving is idempotent: the same request
	// returns the original row.
	ReserveImport(ctx context.Context, request InputImport) (InputVersion, error)
	PublishVersion(ctx context.Context, request PublicationRequest) (PublicationResult, error)
}

type PublicationRequest struct {
	TenantID string
	// Import and Verified are the exact facts the trusted verifier observed.
	Import   InputImport
	Verified VerifiedAlpaca

	DatasetID          string
	TaskID             string
	InputVersionID     string
	VersionNumber      int64
	Preview            []byte
	PublishedAt        time.Time
	OperationID        string
	Actor              string
	RecoveryGeneration int64

	// Target is the frozen target of the task; its cluster selects the commit lock.
	Target DatasetImportTarget
}

type PublicationResult struct {
	InputVersion InputVersion
	Version      DatasetVersion
	Task         DatasetImportTask
	Replayed     bool
}

// DatasetTaskTransitionRepository handles the remaining state transitions.
// Each is its own short transaction with its own fence.
type DatasetTaskTransitionRepository interface {
	// RequestCancel turns a live task into CANCELING and returns the updated
	// task. An already terminal task returns ErrImportStateConflict.
	RequestCancel(ctx context.Context, request TaskCommandRequest) (DatasetImportTask, error)
	// RetryTask requeues a retryable FAILED task.
	RetryTask(ctx context.Context, request TaskCommandRequest) (DatasetImportTask, error)
}

type TaskCommandRequest struct {
	TenantID         string
	ActorID          string
	DatasetID        string
	TaskID           string
	IdempotencyKey   string
	RequestSHA256    string
	ReceiptID        string
	AcceptedAt       time.Time
	OperationID      string
	ExpectedRevision int64
}

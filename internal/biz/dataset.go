// Dataset domain: collections of immutable, verified input versions, and the
// asynchronous online imports that produce them.
//
// This package owns the concepts and their legality rules. It must not import
// SQL drivers, storage SDKs, Kubernetes clients or transport packages; the data
// layer implements the ports declared here.
package biz

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var (
	ErrDatasetNotFound        = errors.New("DATASET_NOT_FOUND")
	ErrImportTaskNotFound     = errors.New("IMPORT_TASK_NOT_FOUND")
	ErrDatasetNameConflict    = errors.New("DATASET_NAME_CONFLICT")
	ErrRevisionConflict       = errors.New("REVISION_CONFLICT")
	ErrIdempotencyConflict    = errors.New("IDEMPOTENCY_CONFLICT")
	ErrDatasetArchived        = errors.New("DATASET_ARCHIVED")
	ErrImportStateConflict    = errors.New("IMPORT_STATE_CONFLICT")
	ErrDatasetInvalidMetadata = errors.New("INVALID_DATASET_METADATA")
)

const (
	DatasetNameMaxRunes        = 128
	DatasetDescriptionMaxRunes = 4000
	IdempotencyKeyMaxBytes     = 128
)

// DatasetState is the collection lifecycle. Archiving stops new imports and new
// training admission; it never rewrites an already published version.
type DatasetState string

const (
	DatasetActive   DatasetState = "ACTIVE"
	DatasetArchived DatasetState = "ARCHIVED"
)

// ImportTaskState is the task lifecycle from design §9.1.
type ImportTaskState string

const (
	TaskQueued    ImportTaskState = "QUEUED"
	TaskRunning   ImportTaskState = "RUNNING"
	TaskFailed    ImportTaskState = "FAILED"
	TaskCanceling ImportTaskState = "CANCELING"
	TaskCancelled ImportTaskState = "CANCELLED"
	TaskSucceeded ImportTaskState = "SUCCEEDED"
)

// ImportPhase is the observable stage inside a running attempt. It shares one
// vocabulary with the progress view so cancellation can be expressed.
type ImportPhase string

const (
	PhaseWaiting     ImportPhase = "WAITING"
	PhaseDownloading ImportPhase = "DOWNLOADING"
	PhaseUploading   ImportPhase = "UPLOADING"
	PhaseVerifying   ImportPhase = "VERIFYING"
	PhasePublishing  ImportPhase = "PUBLISHING"
	PhaseStopping    ImportPhase = "STOPPING"
)

// OperationKind enumerates idempotent commands. The value is part of the
// receipt's uniqueness key, so it must stay stable.
type OperationKind string

const (
	OpCreateDataset  OperationKind = "CREATE_DATASET"
	OpUpdateDataset  OperationKind = "UPDATE_DATASET"
	OpArchiveDataset OperationKind = "ARCHIVE_DATASET"
	OpRestoreDataset OperationKind = "RESTORE_DATASET"
	OpImportVersion  OperationKind = "IMPORT_DATASET_VERSION"
	OpCancelImport   OperationKind = "CANCEL_DATASET_IMPORT"
	OpRetryImport    OperationKind = "RETRY_DATASET_IMPORT"
)

func ValidOperationKind(kind OperationKind) bool {
	switch kind {
	case OpCreateDataset, OpUpdateDataset, OpArchiveDataset, OpRestoreDataset, OpImportVersion, OpCancelImport, OpRetryImport:
		return true
	}
	return false
}

// DatasetEventType is the immutable operation history vocabulary.
type DatasetEventType string

const (
	EventCreated        DatasetEventType = "DATASET_CREATED"
	EventUpdated        DatasetEventType = "DATASET_UPDATED"
	EventArchived       DatasetEventType = "DATASET_ARCHIVED"
	EventRestored       DatasetEventType = "DATASET_RESTORED"
	EventImportAccepted DatasetEventType = "IMPORT_ACCEPTED"
	EventRetryAccepted  DatasetEventType = "IMPORT_RETRY_ACCEPTED"
	EventCancelRequest  DatasetEventType = "IMPORT_CANCEL_REQUESTED"
	EventImportOK       DatasetEventType = "IMPORT_SUCCEEDED"
	EventImportFailed   DatasetEventType = "IMPORT_FAILED"
	EventImportCanceled DatasetEventType = "IMPORT_CANCELLED"
)

// Dataset is one collection. Revision covers name/description/lifecycle only;
// import progress and version numbering never advance it.
type Dataset struct {
	TenantID          string
	DatasetID         string
	Name              string
	Description       string
	State             DatasetState
	Revision          int64
	NextVersionNumber int64
	CreatedBy         string
	UpdatedBy         string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ArchivedAt        *time.Time
}

// NormalizeDatasetName applies the one normalization rule both the application
// and the database unique constraint rely on. It rejects rather than silently
// rewriting: a name that changes under NFC is a client mistake, and accepting
// it would make "same name" ambiguous.
func NormalizeDatasetName(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", ErrDatasetInvalidMetadata
	}
	name := norm.NFC.String(raw)
	if name != raw {
		return "", ErrDatasetInvalidMetadata
	}
	count := utf8.RuneCountInString(name)
	if count < 1 || count > DatasetNameMaxRunes {
		return "", ErrDatasetInvalidMetadata
	}
	if strings.TrimSpace(name) != name {
		return "", ErrDatasetInvalidMetadata
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", ErrDatasetInvalidMetadata
		}
	}
	return name, nil
}

// NormalizeDatasetDescription allows newlines but no other control characters.
// It is plain text; the page never executes it.
func NormalizeDatasetDescription(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", ErrDatasetInvalidMetadata
	}
	if utf8.RuneCountInString(raw) > DatasetDescriptionMaxRunes {
		return "", ErrDatasetInvalidMetadata
	}
	for _, r := range raw {
		if unicode.IsControl(r) && r != '\n' {
			return "", ErrDatasetInvalidMetadata
		}
	}
	return raw, nil
}

// ValidIdempotencyKey enforces the documented alphabet. The key is
// case-sensitive and scoped to (tenant, actor, operation).
func ValidIdempotencyKey(raw string) bool {
	if raw == "" || len(raw) > IdempotencyKeyMaxBytes {
		return false
	}
	for index, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case index > 0 && (r == '.' || r == '_' || r == ':' || r == '-'):
		default:
			return false
		}
	}
	return true
}

// DatasetCommand is the normalized intent of one idempotent write. Fields that
// do not apply to a kind stay zero and are excluded from the digest, so the
// same intent always hashes the same way.
type DatasetCommand struct {
	TenantID       string
	ActorID        string
	Kind           OperationKind
	IdempotencyKey string
	DatasetID      string
	TaskID         string

	Name        string
	Description string
	UpdateName  bool
	UpdateDesc  bool

	ExpectedRevision int64
}

// Digest is the canonical intent hash used to detect "same key, different
// intent". It excludes trace ids, current limits, observation timestamps and
// any server-generated id, per design §9.2.
func (command DatasetCommand) Digest() string {
	// Create names no target yet: the id is server-generated, so including it
	// would make every replay look like a different intent.
	parts := []string{string(command.Kind)}
	if command.Kind != OpCreateDataset {
		parts = append(parts, command.DatasetID, command.TaskID)
	}
	if command.UpdateName || command.Name != "" {
		parts = append(parts, "name="+command.Name)
	}
	if command.UpdateDesc || command.Description != "" {
		parts = append(parts, "description="+command.Description)
	}
	if command.ExpectedRevision > 0 {
		parts = append(parts, "revision="+strconv.FormatInt(command.ExpectedRevision, 10))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// CommandReceipt is the immutable accepted result of one command. It records
// what was decided, never the resource's current state.
type CommandReceipt struct {
	TenantID       string
	ReceiptID      string
	ActorID        string
	Kind           OperationKind
	IdempotencyKey string
	RequestSHA256  string
	DatasetID      string
	TaskID         string
	ResponseStatus int
	AcceptedAt     time.Time
}

// SameIntent reports whether an existing receipt answers the same request.
func (receipt CommandReceipt) SameIntent(command DatasetCommand) bool {
	return receipt.Kind == command.Kind && receipt.RequestSHA256 == command.Digest()
}

// DatasetOperation is one immutable history entry.
type DatasetOperation struct {
	TenantID           string
	OperationID        string
	DatasetID          string
	TaskID             string
	AttemptID          string
	ReceiptID          string
	Actor              string
	EventType          DatasetEventType
	Result             string
	BeforeState        string
	AfterState         string
	BeforeRevision     int64
	AfterRevision      int64
	RecoveryGeneration int64
	FailureCode        string
	OccurredAt         time.Time
}

// ImportTargetScope is the four-field input scope frozen into a task.
type ImportTargetScope struct {
	StorageConnectionID string `json:"storage_connection_id"`
	Bucket              string `json:"bucket"`
	ApprovedPrefix      string `json:"approved_prefix"`
	CredentialReference string `json:"credential_reference"`
}

// DatasetImportTarget is the frozen trusted target of one task. Its digest is
// computed over exactly these fields and nothing else: no Secret content, no
// registry row version, no KFP or training profile.
type DatasetImportTarget struct {
	Schema           string
	ClusterID        string
	NamespaceName    string
	NamespaceUID     string
	InputScope       ImportTargetScope
	InputScopeSHA256 string
	SnapshotSHA256   string
}

const DatasetImportTargetSchema = "ani.modeldev.dataset.import-target.v1"

// Canonical returns the frozen key/value layout of the target snapshot. Keys
// are sorted so the digest never depends on map iteration order.
func (target DatasetImportTarget) Canonical() ([]byte, error) {
	if !CanonicalEnvironmentUUID(target.InputScopeStorageTenant()) {
		return nil, ErrDatasetInvalidMetadata
	}
	fields := [][2]string{
		{"cluster_id", target.ClusterID},
		{"input_scope.storage_connection_id", target.InputScope.StorageConnectionID},
		{"input_scope.bucket", target.InputScope.Bucket},
		{"input_scope.approved_prefix", target.InputScope.ApprovedPrefix},
		{"input_scope.credential_reference", target.InputScope.CredentialReference},
		{"namespace_name", target.NamespaceName},
		{"namespace_uid", target.NamespaceUID},
		{"schema", DatasetImportTargetSchema},
		{"tenant_id", target.InputScopeStorageTenant()},
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i][0] < fields[j][0] })
	var builder strings.Builder
	builder.WriteByte('{')
	for index, field := range fields {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(`"` + field[0] + `":"` + field[1] + `"`)
	}
	builder.WriteByte('}')
	return []byte(builder.String()), nil
}

// InputScopeStorageTenant returns the tenant that owns the approved prefix.
// The prefix is the authoritative carrier: it must be exactly inputs/<tenant>.
func (target DatasetImportTarget) InputScopeStorageTenant() string {
	const prefix = "inputs/"
	if !strings.HasPrefix(target.InputScope.ApprovedPrefix, prefix) {
		return ""
	}
	return strings.TrimPrefix(target.InputScope.ApprovedPrefix, prefix)
}

// ImportTargetScopeDigest is the digest of exactly the four scope fields. The
// target snapshot covers namespace identity too; this one stays stable when only
// the namespace is re-created, so the two facts can be compared separately.
func ImportTargetScopeDigest(scope ImportTargetScope) string {
	fields := []string{
		"storage_connection_id=" + scope.StorageConnectionID,
		"bucket=" + scope.Bucket,
		"approved_prefix=" + scope.ApprovedPrefix,
		"credential_reference=" + scope.CredentialReference,
	}
	sum := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return hex.EncodeToString(sum[:])
}

// Digest is the SHA-256 of the canonical target snapshot.
func (target DatasetImportTarget) Digest() (string, error) {
	raw, err := target.Canonical()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

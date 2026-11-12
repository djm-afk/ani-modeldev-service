// Dataset domain: collections of immutable, verified input versions, and the
// asynchronous online imports that produce them.
//
// This package owns the concepts and their legality rules. It must not import
// SQL drivers, storage SDKs, Kubernetes clients or transport packages; the data
// layer implements the ports declared here.
package biz

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

	// ReadyVersionCount and LatestReadyVersionNumber describe published versions
	// only. They are read facts, not stored columns: a version is published by
	// the same transaction that writes its metadata row, so counting that table
	// is the definition.
	ReadyVersionCount        int64
	LatestReadyVersionNumber int64
	// LatestImport is the most recently accepted task, whatever its state. Nil
	// means the collection has never been imported into.
	LatestImport *DatasetImportTask
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

	// SelectionFingerprint is the confirmed source selection of an import. It is
	// part of the intent: the same key with a different file is a different
	// command, not a replay of the first one.
	SelectionFingerprint string
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
	// An import's source selection is part of what the user asked for. Without
	// it, reusing a key with a different file silently returned the first task.
	if command.SelectionFingerprint != "" {
		parts = append(parts, "selection="+command.SelectionFingerprint)
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
	// Outcome is the public receipt verdict: ACCEPTED for a command that took
	// effect, ALREADY_TERMINAL for a cancel that found nothing left to undo.
	Outcome    string
	AcceptedAt time.Time
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
	Schema string
	// EnvironmentID is the tenant execution registry row observed at acceptance.
	// It is an audit/label fact, not part of the frozen digest: a re-registered
	// environment must not invalidate an already accepted task.
	EnvironmentID    string
	ClusterID        string
	NamespaceName    string
	NamespaceUID     string
	InputScope       ImportTargetScope
	InputScopeSHA256 string
	SnapshotSHA256   string
}

const DatasetImportTargetSchema = "ani.modeldev.dataset.import-target.v1"

// Canonical returns the frozen layout of the target snapshot with keys sorted
// lexically, so the digest never depends on map iteration order. input_scope is
// a nested object, not dotted keys: the database compares the stored JSON
// against a nested jsonb_build_object, and the two forms are not equal.
func (target DatasetImportTarget) Canonical() ([]byte, error) {
	tenant := target.InputScopeStorageTenant()
	if !CanonicalEnvironmentUUID(tenant) || !CanonicalEnvironmentUUID(target.ClusterID) || !CanonicalEnvironmentUUID(target.NamespaceUID) {
		return nil, ErrDatasetInvalidMetadata
	}
	if target.NamespaceName != "tenant-"+tenant {
		return nil, ErrDatasetInvalidMetadata
	}
	value := map[string]any{
		"cluster_id": target.ClusterID,
		"input_scope": map[string]string{
			"approved_prefix":       target.InputScope.ApprovedPrefix,
			"bucket":                target.InputScope.Bucket,
			"credential_reference":  target.InputScope.CredentialReference,
			"storage_connection_id": target.InputScope.StorageConnectionID,
		},
		"namespace_name": target.NamespaceName,
		"namespace_uid":  target.NamespaceUID,
		"schema":         DatasetImportTargetSchema,
		"tenant_id":      tenant,
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, ErrDatasetInvalidMetadata
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
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

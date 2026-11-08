package biz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"time"
)

// ImportPlanSchema is the control document the importer decodes. The control
// plane owns the encoder so the two sides cannot drift.
const ImportPlanSchema = "ani.modeldev.dataset.import-plan.v1"

// ImporterDataObjectKey and ImporterResultObjectKey are the fixed per-attempt
// layout. Keys are derived from frozen identities only, so a retry at the same
// attempt number cannot land on another attempt's bytes.
func ImporterDataObjectKey(task DatasetImportTask, attemptNo int64) (string, error) {
	return importerObjectKey(task, attemptNo, "data.json")
}

// ImporterResultObjectKey is the report key for the same attempt.
func ImporterResultObjectKey(task DatasetImportTask, attemptNo int64) (string, error) {
	return importerObjectKey(task, attemptNo, "result.json")
}

func importerObjectKey(task DatasetImportTask, attemptNo int64, leaf string) (string, error) {
	if attemptNo <= 0 || !ValidStorageKey(task.ApprovedPrefix) || task.ApprovedPrefix != "inputs/"+task.TenantID {
		return "", ErrDatasetInvalidMetadata
	}
	key := path.Join(
		task.ApprovedPrefix, "datasets", task.DatasetID, "imports", task.TaskID,
		"attempts", fmt.Sprintf("%d", attemptNo), leaf,
	)
	if !ValidStorageKey(key) {
		return "", ErrDatasetInvalidMetadata
	}
	return key, nil
}

// ImportPlan is the frozen per-attempt control document. It carries no
// credential value and no storage version: those are proven after the upload.
type ImportPlan struct {
	TenantID  string
	DatasetID string
	TaskID    string
	// AttemptID is empty until allocation; the encoder never invents one.
	AttemptID            string
	InputVersionID       string
	SourceSnapshotSHA256 string
	RuntimePlanSHA256    string
	ImporterImageDigest  string

	Provider           SourceProvider
	RepoID             string
	RequestedRevision  string
	ResolvedRevision   string
	FilePath           string
	LockKind           SourceLockKind
	ExpectedSizeBytes  int64
	ExpectedFileSHA256 string

	Data   ImportPlanObject
	Result ImportPlanObject

	CredentialFile string
	Deadline       time.Time
}

// ImportPlanObject is one preallocated target key.
type ImportPlanObject struct {
	StorageConnectionID string
	Bucket              string
	Key                 string
}

// EncodeImportPlan renders the document with sorted keys and no whitespace, the
// same canonical shape the importer's strict decoder accepts.
func EncodeImportPlan(plan ImportPlan) ([]byte, error) {
	if err := plan.validate(); err != nil {
		return nil, err
	}
	deadline := plan.Deadline.UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z")
	value := map[string]any{
		"schema":                 ImportPlanSchema,
		"tenant_id":              plan.TenantID,
		"dataset_id":             plan.DatasetID,
		"task_id":                plan.TaskID,
		"attempt_id":             plan.AttemptID,
		"input_version_id":       plan.InputVersionID,
		"source_snapshot_sha256": plan.SourceSnapshotSHA256,
		"runtime_plan_sha256":    plan.RuntimePlanSHA256,
		"importer_image_digest":  plan.ImporterImageDigest,
		"provider":               string(plan.Provider),
		"repo_id":                plan.RepoID,
		"requested_revision":     plan.RequestedRevision,
		"resolved_revision":      plan.ResolvedRevision,
		"file_path":              plan.FilePath,
		"lock_kind":              string(plan.LockKind),
		"expected_size_bytes":    plan.ExpectedSizeBytes,
		"expected_file_sha256":   plan.ExpectedFileSHA256,
		"credential_file":        plan.CredentialFile,
		"deadline":               deadline,
		"data": map[string]string{
			"storage_connection_id": plan.Data.StorageConnectionID,
			"bucket":                plan.Data.Bucket,
			"key":                   plan.Data.Key,
		},
		"result": map[string]string{
			"storage_connection_id": plan.Result.StorageConnectionID,
			"bucket":                plan.Result.Bucket,
			"key":                   plan.Result.Key,
		},
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, ErrDatasetInvalidMetadata
	}
	return raw, nil
}

func (plan ImportPlan) validate() error {
	if plan.TenantID == "" || plan.DatasetID == "" || plan.TaskID == "" || plan.InputVersionID == "" ||
		plan.SourceSnapshotSHA256 == "" || plan.ImporterImageDigest == "" {
		return ErrDatasetInvalidMetadata
	}
	// RuntimePlanSHA256 is empty only while the caller computes the digest of the
	// unsigned form; the finished plan always carries it.
	if plan.RuntimePlanSHA256 != "" && len(plan.RuntimePlanSHA256) != 64 {
		return ErrDatasetInvalidMetadata
	}
	if !ValidSourceProvider(plan.Provider) || plan.RepoID == "" || plan.RequestedRevision == "" {
		return ErrDatasetInvalidMetadata
	}
	if err := ValidateSourceFilePath(plan.FilePath); err != nil {
		return err
	}
	if plan.ExpectedSizeBytes <= 0 || plan.Deadline.IsZero() {
		return ErrDatasetInvalidMetadata
	}
	if plan.Data.Key == "" || plan.Result.Key == "" || plan.Data.Key == plan.Result.Key ||
		plan.Data.Bucket == "" || plan.Result.Bucket == "" || plan.CredentialFile == "" {
		return ErrDatasetInvalidMetadata
	}
	return nil
}

// ImportPlanPlanner is the default AttemptPlanner. It resolves the object layout
// and the plan JSON from the frozen task; it touches no cluster or storage.
type ImportPlanPlanner struct {
	ImporterImageDigest string
	CredentialFile      string
	TaskTimeout         time.Duration
	now                 func() time.Time
}

// NewImportPlanPlanner validates the fixed material identity.
func NewImportPlanPlanner(importerImageDigest, credentialFile string, timeout time.Duration, now func() time.Time) (*ImportPlanPlanner, error) {
	if len(importerImageDigest) != 64 || credentialFile == "" || timeout <= 0 {
		return nil, ErrImportWorkerConfiguration
	}
	if now == nil {
		now = time.Now
	}
	return &ImportPlanPlanner{ImporterImageDigest: importerImageDigest, CredentialFile: credentialFile, TaskTimeout: timeout, now: now}, nil
}

// PlanAttempt builds the attempt's material identity. attemptNo must be the
// number the allocator will use; the scheduler reads it back for the next run.
func (planner *ImportPlanPlanner) PlanAttempt(ctx context.Context, task DatasetImportTask, attemptID string, attemptNo int64) (AttemptPlan, error) {
	if ctx == nil || attemptNo <= 0 || attemptID == "" {
		return AttemptPlan{}, ErrImportWorkerConfiguration
	}
	snapshot, err := DecodeSourceSnapshot(task.SourceSnapshot)
	if err != nil {
		return AttemptPlan{}, err
	}
	dataKey, err := ImporterDataObjectKey(task, attemptNo)
	if err != nil {
		return AttemptPlan{}, err
	}
	resultKey, err := ImporterResultObjectKey(task, attemptNo)
	if err != nil {
		return AttemptPlan{}, err
	}
	deadline := task.AcceptedAt.Add(planner.TaskTimeout)
	unsigned := ImportPlan{
		TenantID: task.TenantID, DatasetID: task.DatasetID, TaskID: task.TaskID,
		AttemptID:            attemptID,
		InputVersionID:       task.ReservedInputVersionID,
		SourceSnapshotSHA256: task.SourceSnapshotSHA256,
		ImporterImageDigest:  planner.ImporterImageDigest,
		Provider:             snapshot.Provider, RepoID: snapshot.RepoID,
		RequestedRevision: snapshot.RequestedRevision, ResolvedRevision: snapshot.ResolvedRevision,
		FilePath: snapshot.FilePath, LockKind: snapshot.LockKind,
		ExpectedSizeBytes: snapshot.ExpectedSizeBytes, ExpectedFileSHA256: snapshot.ExpectedFileSHA256,
		Data:           ImportPlanObject{StorageConnectionID: task.StorageConnectionID, Bucket: task.Bucket, Key: dataKey},
		Result:         ImportPlanObject{StorageConnectionID: task.StorageConnectionID, Bucket: task.Bucket, Key: resultKey},
		CredentialFile: planner.CredentialFile, Deadline: deadline,
	}
	// The digest covers the plan without itself: the importer recomputes it by
	// blanking runtime_plan_sha256, so neither side depends on a fixed point.
	raw, err := EncodeImportPlan(unsigned)
	if err != nil {
		return AttemptPlan{}, err
	}
	planSHA := digestBytes(raw)
	unsigned.RuntimePlanSHA256 = planSHA
	raw, err = EncodeImportPlan(unsigned)
	if err != nil {
		return AttemptPlan{}, err
	}
	return AttemptPlan{
		JobName:       importerObjectName(task, attemptNo, "job"),
		PVCName:       importerObjectName(task, attemptNo, "pvc"),
		SecretName:    importerObjectName(task, attemptNo, "credentials"),
		DataObjectKey: dataKey, ResultObjectKey: resultKey,
		DeadlineAt: deadline, RuntimePlan: raw, RuntimePlanSHA256: planSHA,
	}, nil
}

// importerObjectName is a deterministic DNS label under the 63-character limit.
func importerObjectName(task DatasetImportTask, attemptNo int64, suffix string) string {
	return fmt.Sprintf("md-import-%s-%d-%s", shortID(task.TaskID), attemptNo, suffix)
}

func shortID(id string) string {
	if len(id) < 8 {
		return id
	}
	return id[:8]
}

// DecodeSourceSnapshot reads the frozen selection facts back from the task row.
func DecodeSourceSnapshot(raw []byte) (SourceSnapshot, error) {
	if len(raw) == 0 {
		return SourceSnapshot{}, ErrDatasetInvalidMetadata
	}
	var value struct {
		Schema             string `json:"schema"`
		Provider           string `json:"provider"`
		RepoType           string `json:"repo_type"`
		RepoID             string `json:"repo_id"`
		RequestedRevision  string `json:"requested_revision"`
		FilePath           string `json:"file_path"`
		LockKind           string `json:"lock_kind"`
		ResolvedRevision   string `json:"resolved_revision"`
		ExpectedSizeBytes  string `json:"expected_size_bytes"`
		ExpectedFileSHA256 string `json:"expected_file_sha256"`
		ResolvedAt         string `json:"resolved_at"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return SourceSnapshot{}, ErrDatasetInvalidMetadata
	}
	if value.Schema != SourceSnapshotSchema || value.RepoType != "dataset" {
		return SourceSnapshot{}, ErrDatasetInvalidMetadata
	}
	if !ValidSourceProvider(SourceProvider(value.Provider)) || value.RepoID == "" || value.RequestedRevision == "" {
		return SourceSnapshot{}, ErrDatasetInvalidMetadata
	}
	if err := ValidateSourceFilePath(value.FilePath); err != nil {
		return SourceSnapshot{}, err
	}
	size, err := parseInt64(value.ExpectedSizeBytes)
	if err != nil || size <= 0 {
		return SourceSnapshot{}, ErrDatasetInvalidMetadata
	}
	lock := SourceLockKind(value.LockKind)
	if lock != LockImmutableRevision && lock != LockFileSHA256 {
		return SourceSnapshot{}, ErrDatasetInvalidMetadata
	}
	// resolved_at is part of the frozen snapshot identity, so a reader that
	// dropped it would report a zero time and a different digest than the one
	// the acceptance path computed.
	resolvedAt, err := time.Parse(time.RFC3339Nano, value.ResolvedAt)
	if err != nil || resolvedAt.IsZero() || resolvedAt.Nanosecond()%1000 != 0 {
		return SourceSnapshot{}, ErrDatasetInvalidMetadata
	}
	return SourceSnapshot{
		SourceSelection: SourceSelection{
			Provider: SourceProvider(value.Provider), RepoID: value.RepoID,
			RequestedRevision: value.RequestedRevision, FilePath: value.FilePath,
			LockKind: lock, ResolvedRevision: value.ResolvedRevision,
			ExpectedSizeBytes: size, ExpectedFileSHA256: value.ExpectedFileSHA256,
		},
		ResolvedAt: resolvedAt.UTC(),
	}, nil
}

func digestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func parseInt64(value string) (int64, error) {
	var parsed int64
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, ErrDatasetInvalidMetadata
		}
		parsed = parsed*10 + int64(r-'0')
		if parsed < 0 {
			return 0, ErrDatasetInvalidMetadata
		}
	}
	if value == "" {
		return 0, ErrDatasetInvalidMetadata
	}
	return parsed, nil
}

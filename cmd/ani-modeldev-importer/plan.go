package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// MaxPlanBytes bounds the mounted plan. It is a control document, so the same
// 64 KiB guard the result uses applies.
const MaxPlanBytes = 64 << 10

var (
	ErrPlanInvalid = errors.New("IMPORT_PLAN_INVALID")
	ErrPlanMissing = errors.New("IMPORT_PLAN_MISSING")
)

// PlanObject identifies one preallocated object key.
type PlanObject struct {
	StorageConnectionID string
	Bucket              string
	Key                 string
}

// ImportPlan is the immutable per-attempt control document the control plane
// mounts into the job. It carries no credential value and no row version.
type ImportPlan struct {
	TenantID             string
	DatasetID            string
	TaskID               string
	AttemptID            string
	InputVersionID       string
	SourceSnapshotSHA256 string
	RuntimePlanSHA256    string
	ImporterImageDigest  string

	Provider           biz.SourceProvider
	RepoID             string
	RequestedRevision  string
	ResolvedRevision   string
	FilePath           string
	LockKind           biz.SourceLockKind
	ExpectedSizeBytes  int64
	ExpectedFileSHA256 string

	Data   PlanObject
	Result PlanObject

	// CredentialFile is the mounted Secret path the S3 client reads. Only the
	// path is in the plan; the value lives in the Secret.
	CredentialFile string
	// Deadline is an RFC3339 microsecond UTC instant.
	Deadline string
}

// DecodeImportPlan reads the plan with the same strictness as the result: no
// duplicate keys, no unknown keys, exact types, no trailing content.
func DecodeImportPlan(raw []byte) (ImportPlan, error) {
	if len(raw) == 0 || len(raw) > MaxPlanBytes || !utf8.Valid(raw) {
		return ImportPlan{}, ErrPlanInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	fields, err := decodePlanObject(decoder)
	if err != nil {
		return ImportPlan{}, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ImportPlan{}, ErrPlanInvalid
	}
	return buildPlan(fields)
}

func LoadImportPlan(path string) (ImportPlan, error) {
	if path == "" {
		return ImportPlan{}, ErrPlanMissing
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return ImportPlan{}, ErrPlanMissing
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, MaxPlanBytes+1))
	if err != nil || len(raw) > MaxPlanBytes {
		return ImportPlan{}, ErrPlanInvalid
	}
	return DecodeImportPlan(raw)
}

type planField map[string]json.RawMessage

func decodePlanObject(decoder *json.Decoder) (planField, error) {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrPlanInvalid
	}
	result := planField{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return nil, ErrPlanInvalid
		}
		if _, duplicate := result[key]; duplicate {
			return nil, ErrPlanInvalid
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, ErrPlanInvalid
		}
		result[key] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrPlanInvalid
	}
	return result, nil
}

func buildPlan(fields planField) (ImportPlan, error) {
	allowed := map[string]bool{
		"schema": true, "tenant_id": true, "dataset_id": true, "task_id": true, "attempt_id": true,
		"input_version_id": true, "source_snapshot_sha256": true, "runtime_plan_sha256": true,
		"importer_image_digest": true,
		"provider":              true, "repo_id": true, "requested_revision": true, "resolved_revision": true, "file_path": true,
		"lock_kind": true, "expected_size_bytes": true, "expected_file_sha256": true,
		"data": true, "result": true, "credential_file": true, "deadline": true,
	}
	for key := range fields {
		if !allowed[key] {
			return ImportPlan{}, ErrPlanInvalid
		}
	}
	schema, err := planString(fields, "schema")
	if err != nil || schema != ImportPlanSchema {
		return ImportPlan{}, ErrPlanInvalid
	}
	plan := ImportPlan{}
	for target, key := range map[*string]string{
		&plan.TenantID: "tenant_id", &plan.DatasetID: "dataset_id", &plan.TaskID: "task_id",
		&plan.AttemptID: "attempt_id", &plan.InputVersionID: "input_version_id",
		&plan.SourceSnapshotSHA256: "source_snapshot_sha256", &plan.RuntimePlanSHA256: "runtime_plan_sha256",
		&plan.ImporterImageDigest: "importer_image_digest", &plan.RepoID: "repo_id",
		&plan.RequestedRevision: "requested_revision", &plan.ResolvedRevision: "resolved_revision", &plan.FilePath: "file_path",
		&plan.ExpectedFileSHA256: "expected_file_sha256", &plan.CredentialFile: "credential_file",
		&plan.Deadline: "deadline",
	} {
		value, err := planString(fields, key)
		if err != nil {
			return ImportPlan{}, err
		}
		*target = value
	}
	provider, err := planString(fields, "provider")
	if err != nil || !biz.ValidSourceProvider(biz.SourceProvider(provider)) {
		return ImportPlan{}, ErrPlanInvalid
	}
	plan.Provider = biz.SourceProvider(provider)
	lockKind, err := planString(fields, "lock_kind")
	if err != nil {
		return ImportPlan{}, err
	}
	plan.LockKind = biz.SourceLockKind(lockKind)
	if plan.LockKind != biz.LockImmutableRevision && plan.LockKind != biz.LockFileSHA256 {
		return ImportPlan{}, ErrPlanInvalid
	}
	sizeRaw, ok := fields["expected_size_bytes"]
	if !ok {
		return ImportPlan{}, ErrPlanInvalid
	}
	size, err := strconv.ParseInt(strings.Trim(strings.TrimSpace(string(sizeRaw)), `"`), 10, 64)
	if err != nil || size <= 0 {
		return ImportPlan{}, ErrPlanInvalid
	}
	plan.ExpectedSizeBytes = size
	for key, target := range map[string]*PlanObject{"data": &plan.Data, "result": &plan.Result} {
		raw, ok := fields[key]
		if !ok {
			return ImportPlan{}, ErrPlanInvalid
		}
		object, err := decodePlanObject(json.NewDecoder(bytes.NewReader(raw)))
		if err != nil {
			return ImportPlan{}, err
		}
		if err := requireExactKeys(object, "storage_connection_id", "bucket", "key"); err != nil {
			return ImportPlan{}, err
		}
		if target.StorageConnectionID, err = planString(object, "storage_connection_id"); err != nil {
			return ImportPlan{}, err
		}
		if target.Bucket, err = planString(object, "bucket"); err != nil {
			return ImportPlan{}, err
		}
		if target.Key, err = planString(object, "key"); err != nil {
			return ImportPlan{}, err
		}
		if target.Key == "" || target.Bucket == "" || target.StorageConnectionID == "" {
			return ImportPlan{}, ErrPlanInvalid
		}
	}
	if plan.Data.Key == plan.Result.Key {
		return ImportPlan{}, ErrPlanInvalid
	}
	if err := plan.Validate(); err != nil {
		return ImportPlan{}, err
	}
	return plan, nil
}

const ImportPlanSchema = "ani.modeldev.dataset.import-plan.v1"

func planString(fields planField, key string) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", ErrPlanInvalid
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", ErrPlanInvalid
	}
	return value, nil
}

func requireExactKeys(fields planField, keys ...string) error {
	if len(fields) != len(keys) {
		return ErrPlanInvalid
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			return ErrPlanInvalid
		}
	}
	return nil
}

// Validate checks the plan's own legality. It does not authorize anything.
func (plan ImportPlan) Validate() error {
	if plan.TenantID == "" || plan.DatasetID == "" || plan.TaskID == "" || plan.AttemptID == "" || plan.InputVersionID == "" {
		return ErrPlanInvalid
	}
	if !biz.ValidSourceProvider(plan.Provider) || plan.RepoID == "" {
		return ErrPlanInvalid
	}
	if err := biz.ValidateSourceFilePath(plan.FilePath); err != nil {
		return ErrPlanInvalid
	}
	if plan.ExpectedSizeBytes <= 0 {
		return ErrPlanInvalid
	}
	// An immutable-revision lock needs the resolved commit; a file-SHA lock
	// needs the full digest. Neither may stand in for the other.
	switch plan.LockKind {
	case biz.LockImmutableRevision:
		if plan.ResolvedRevision == "" {
			return ErrPlanInvalid
		}
	case biz.LockFileSHA256:
		if !isLowerHex64(plan.ExpectedFileSHA256) {
			return ErrPlanInvalid
		}
	}
	if plan.ExpectedFileSHA256 != "" && !isLowerHex64(plan.ExpectedFileSHA256) {
		return ErrPlanInvalid
	}
	return nil
}

func isLowerHex64(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

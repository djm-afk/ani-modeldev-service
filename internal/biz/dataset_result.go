package biz

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrImportResultInvalid = errors.New("IMPORT_RESULT_INVALID")
)

// MaxImportResultBytes is a control-message guard, unrelated to the dataset
// capacity L. Over-limit is detected by reading cap+1.
const MaxImportResultBytes = 64 << 10

const ImportResultSchema = "ani.modeldev.dataset.import-result.v1"

// ImportResultStatus is the attempt-level outcome. SUCCEEDED here does not make
// the Task SUCCEEDED: that requires trusted verification and publication.
type ImportResultStatus string

const (
	ImportResultSucceeded ImportResultStatus = "SUCCEEDED"
	ImportResultFailed    ImportResultStatus = "FAILED"
)

// ImportFailureStage is the coarse stage the importer reached.
type ImportFailureStage string

const (
	StageDownload ImportFailureStage = "DOWNLOAD"
	StageUpload   ImportFailureStage = "UPLOAD"
	StageReport   ImportFailureStage = "REPORT"
)

// ImportFailureCode is the importer's own vocabulary. The control plane decides
// the final task failure; this is only what the job observed.
type ImportFailureCode string

const (
	FailSourceChanged             ImportFailureCode = "SOURCE_CHANGED"
	FailSourceNotFound            ImportFailureCode = "SOURCE_NOT_FOUND"
	FailSourceAuthRequired        ImportFailureCode = "SOURCE_AUTH_REQUIRED"
	FailDependencyUnavailable     ImportFailureCode = "IMPORT_DEPENDENCY_UNAVAILABLE"
	FailResourceUnavailable       ImportFailureCode = "IMPORT_RESOURCE_UNAVAILABLE"
	FailTimeout                   ImportFailureCode = "IMPORT_TIMEOUT"
	FailStorageVersioningRequired ImportFailureCode = "STORAGE_VERSIONING_REQUIRED"
)

func validImportFailureCode(code ImportFailureCode) bool {
	switch code {
	case FailSourceChanged, FailSourceNotFound, FailSourceAuthRequired, FailDependencyUnavailable,
		FailResourceUnavailable, FailTimeout, FailStorageVersioningRequired:
		return true
	}
	return false
}

// Control-plane verdicts. These are decided by this service after reading the
// verified bytes, so a job can never report them: the importer's own vocabulary
// stops at what it observed, and content validity is the verifier's call.
const (
	FailDatasetInvalid        ImportFailureCode = "DATASET_INVALID"
	FailDatasetRecordTooLarge ImportFailureCode = "DATASET_RECORD_TOO_LARGE"
	FailImportResultInvalid   ImportFailureCode = "IMPORT_RESULT_INVALID"
	FailPersistence           ImportFailureCode = "IMPORT_PERSISTENCE_UNAVAILABLE"
	// FailDatasetArchived and FailImportStateConflict name the two lifecycle
	// races a publish can lose. Reporting them as IMPORT_PERSISTENCE_UNAVAILABLE
	// told operators the store was broken when the collection had simply been
	// archived or the task had already moved on.
	FailDatasetArchived     ImportFailureCode = "DATASET_ARCHIVED"
	FailImportStateConflict ImportFailureCode = "IMPORT_STATE_CONFLICT"
)

// ImportResultObject is the exact uploaded object identity.
type ImportResultObject struct {
	StorageConnectionID string
	Bucket              string
	Key                 string
	VersionID           string
	SizeBytes           int64
	SHA256              string
}

// ImportResult is one attempt's report. SUCCEEDED carries object and nothing
// else; FAILED carries code+stage and nothing else.
type ImportResult struct {
	Status               ImportResultStatus
	TenantID             string
	DatasetID            string
	TaskID               string
	AttemptID            string
	InputVersionID       string
	SourceSnapshotSHA256 string
	RuntimePlanSHA256    string
	ImporterImageDigest  string
	NamespaceUID         string
	JobUID               string
	PodUID               string
	CompletedAt          time.Time
	Object               *ImportResultObject
	FailureCode          ImportFailureCode
	FailureStage         ImportFailureStage
}

// DecodeImportResult parses a report with the documented strictness: bounded
// body, no duplicate keys, no unknown keys, no trailing content, exact types.
func DecodeImportResult(raw []byte) (ImportResult, error) {
	if len(raw) == 0 || len(raw) > MaxImportResultBytes {
		return ImportResult{}, ErrImportResultInvalid
	}
	if !utf8.Valid(raw) {
		return ImportResult{}, ErrImportResultInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	fields, err := decodeObject(decoder, "")
	if err != nil {
		return ImportResult{}, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ImportResult{}, ErrImportResultInvalid
	}
	return buildImportResult(fields)
}

// EncodeImportResult renders the report with the fixed field set. The importer
// job is the only writer; the control plane only ever decodes.
func EncodeImportResult(result ImportResult) ([]byte, error) {
	if result.Status != ImportResultSucceeded && result.Status != ImportResultFailed {
		return nil, ErrImportResultInvalid
	}
	fields := [][2]string{
		{"schema", `"` + ImportResultSchema + `"`},
		{"status", `"` + string(result.Status) + `"`},
		{"tenant_id", quote(result.TenantID)},
		{"dataset_id", quote(result.DatasetID)},
		{"task_id", quote(result.TaskID)},
		{"attempt_id", quote(result.AttemptID)},
		{"input_version_id", quote(result.InputVersionID)},
		{"source_snapshot_sha256", quote(result.SourceSnapshotSHA256)},
		{"runtime_plan_sha256", quote(result.RuntimePlanSHA256)},
		{"importer_image_digest", quote(result.ImporterImageDigest)},
		{"namespace_uid", quote(result.NamespaceUID)},
		{"job_uid", quote(result.JobUID)},
		{"pod_uid", quote(result.PodUID)},
		{"completed_at", `"` + result.CompletedAt.UTC().Format("2006-01-02T15:04:05.000000Z") + `"`},
	}
	if result.Status == ImportResultSucceeded {
		if result.Object == nil {
			return nil, ErrImportResultInvalid
		}
		fields = append(fields, [2]string{"object", `{"storage_connection_id":` + quote(result.Object.StorageConnectionID) +
			`,"bucket":` + quote(result.Object.Bucket) + `,"key":` + quote(result.Object.Key) +
			`,"version_id":` + quote(result.Object.VersionID) + `,"size_bytes":"` + strconv.FormatInt(result.Object.SizeBytes, 10) +
			`","sha256":` + quote(result.Object.SHA256) + `}`})
	} else {
		if result.FailureCode == "" || result.FailureStage == "" {
			return nil, ErrImportResultInvalid
		}
		fields = append(fields,
			[2]string{"failure_code", quote(string(result.FailureCode))},
			[2]string{"failure_stage", quote(string(result.FailureStage))},
		)
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i][0] < fields[j][0] })
	var builder strings.Builder
	builder.WriteByte('{')
	for index, field := range fields {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(quote(field[0]) + ":" + field[1])
	}
	builder.WriteByte('}')
	raw := []byte(builder.String())
	if len(raw) > MaxImportResultBytes {
		return nil, ErrImportResultInvalid
	}
	return raw, nil
}

// quote renders a JSON string with the standard escaping rules.
func quote(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

// ReadImportResult reads at most cap+1 bytes so an over-limit body is detected
// without buffering an unbounded stream.
func ReadImportResult(reader io.Reader) (ImportResult, error) {
	if reader == nil {
		return ImportResult{}, ErrImportResultInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(reader, MaxImportResultBytes+1))
	if err != nil {
		return ImportResult{}, ErrImportResultInvalid
	}
	if len(raw) > MaxImportResultBytes {
		return ImportResult{}, ErrImportResultInvalid
	}
	return DecodeImportResult(raw)
}

type importField map[string]json.RawMessage

func decodeObject(decoder *json.Decoder, path string) (importField, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, ErrImportResultInvalid
	}
	if token != json.Delim('{') {
		return nil, ErrImportResultInvalid
	}
	result := importField{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return nil, ErrImportResultInvalid
		}
		if _, duplicate := result[key]; duplicate {
			return nil, ErrImportResultInvalid
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, ErrImportResultInvalid
		}
		result[key] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrImportResultInvalid
	}
	return result, nil
}

func buildImportResult(fields importField) (ImportResult, error) {
	status, err := requiredString(fields, "status")
	if err != nil {
		return ImportResult{}, err
	}
	schema, err := requiredString(fields, "schema")
	if err != nil || schema != ImportResultSchema {
		return ImportResult{}, ErrImportResultInvalid
	}
	result := ImportResult{Status: ImportResultStatus(status)}
	if result.Status != ImportResultSucceeded && result.Status != ImportResultFailed {
		return ImportResult{}, ErrImportResultInvalid
	}
	for target, key := range map[*string]string{
		&result.TenantID: "tenant_id", &result.DatasetID: "dataset_id", &result.TaskID: "task_id",
		&result.AttemptID: "attempt_id", &result.InputVersionID: "input_version_id",
		&result.SourceSnapshotSHA256: "source_snapshot_sha256", &result.RuntimePlanSHA256: "runtime_plan_sha256",
		&result.ImporterImageDigest: "importer_image_digest", &result.NamespaceUID: "namespace_uid",
		&result.JobUID: "job_uid", &result.PodUID: "pod_uid",
	} {
		value, err := requiredString(fields, key)
		if err != nil {
			return ImportResult{}, err
		}
		*target = value
	}
	completed, err := requiredString(fields, "completed_at")
	if err != nil {
		return ImportResult{}, err
	}
	parsed, err := time.Parse("2006-01-02T15:04:05.000000Z", completed)
	if err != nil || parsed.Nanosecond()%1000 != 0 {
		return ImportResult{}, ErrImportResultInvalid
	}
	result.CompletedAt = parsed

	if result.Status == ImportResultSucceeded {
		if err := buildSucceeded(fields, &result); err != nil {
			return ImportResult{}, err
		}
	} else if err := buildFailed(fields, &result); err != nil {
		return ImportResult{}, err
	}
	return result, nil
}

func buildSucceeded(fields importField, result *ImportResult) error {
	allowed := map[string]bool{
		"schema": true, "status": true, "tenant_id": true, "dataset_id": true, "task_id": true,
		"attempt_id": true, "input_version_id": true, "source_snapshot_sha256": true,
		"runtime_plan_sha256": true, "importer_image_digest": true, "namespace_uid": true,
		"job_uid": true, "pod_uid": true, "completed_at": true, "object": true,
	}
	if err := rejectUnknown(fields, allowed); err != nil {
		return err
	}
	raw, ok := fields["object"]
	if !ok {
		return ErrImportResultInvalid
	}
	object, err := decodeObject(json.NewDecoder(bytes.NewReader(raw)), "object")
	if err != nil {
		return err
	}
	if err := rejectUnknown(object, map[string]bool{
		"storage_connection_id": true, "bucket": true, "key": true, "version_id": true,
		"size_bytes": true, "sha256": true,
	}); err != nil {
		return err
	}
	value := &ImportResultObject{}
	if value.StorageConnectionID, err = requiredString(object, "storage_connection_id"); err != nil {
		return err
	}
	if value.Bucket, err = requiredString(object, "bucket"); err != nil {
		return err
	}
	if value.Key, err = requiredString(object, "key"); err != nil {
		return err
	}
	if value.VersionID, err = requiredString(object, "version_id"); err != nil {
		return err
	}
	if value.VersionID == "" || strings.EqualFold(value.VersionID, "null") {
		return ErrImportResultInvalid
	}
	sizeRaw, ok := object["size_bytes"]
	if !ok {
		return ErrImportResultInvalid
	}
	size, err := strconv.ParseInt(trimQuotes(sizeRaw), 10, 64)
	if err != nil || size <= 0 {
		return ErrImportResultInvalid
	}
	value.SizeBytes = size
	if value.SHA256, err = requiredString(object, "sha256"); err != nil {
		return err
	}
	if !isLowerSHA256(value.SHA256) {
		return ErrImportResultInvalid
	}
	result.Object = value
	return nil
}

func buildFailed(fields importField, result *ImportResult) error {
	allowed := map[string]bool{
		"schema": true, "status": true, "tenant_id": true, "dataset_id": true, "task_id": true,
		"attempt_id": true, "input_version_id": true, "source_snapshot_sha256": true,
		"runtime_plan_sha256": true, "importer_image_digest": true, "namespace_uid": true,
		"job_uid": true, "pod_uid": true, "completed_at": true, "failure_code": true, "failure_stage": true,
	}
	if err := rejectUnknown(fields, allowed); err != nil {
		return err
	}
	code, err := requiredString(fields, "failure_code")
	if err != nil {
		return err
	}
	stage, err := requiredString(fields, "failure_stage")
	if err != nil {
		return err
	}
	result.FailureCode = ImportFailureCode(code)
	if !validImportFailureCode(result.FailureCode) {
		return ErrImportResultInvalid
	}
	result.FailureStage = ImportFailureStage(stage)
	switch result.FailureStage {
	case StageDownload, StageUpload, StageReport:
	default:
		return ErrImportResultInvalid
	}
	return nil
}

func rejectUnknown(fields importField, allowed map[string]bool) error {
	for key := range fields {
		if !allowed[key] {
			return ErrImportResultInvalid
		}
	}
	return nil
}

// requiredString reads a JSON string. Numbers, objects, arrays, booleans and
// null are rejected: the contract only allows strings for these fields.
func requiredString(fields importField, key string) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", ErrImportResultInvalid
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", ErrImportResultInvalid
	}
	return value, nil
}

func trimQuotes(raw json.RawMessage) string {
	return strings.Trim(strings.TrimSpace(string(raw)), `"`)
}

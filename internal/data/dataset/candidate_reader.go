package dataset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// CandidateReader reads an attempt's report and re-verifies the candidate in one
// exact-version read. It is the trusted side of §8.6: the importer's own report
// is never taken as proof of the bytes.
type CandidateReader struct {
	client       *s3.Client
	verifier     ObjectVerifier
	connectionID string
}

// ObjectVerifier is the narrow slice of the object verifier this reader needs.
type ObjectVerifier interface {
	WritePreparedAlpaca(ctx context.Context, scope cpup01.StorageScope, object cpup01.FixedObjectRef, destination io.Writer) (biz.VerifiedAlpaca, error)
}

func NewCandidateReader(client *s3.Client, verifier ObjectVerifier, connectionID string) (*CandidateReader, error) {
	if client == nil || verifier == nil || connectionID == "" {
		return nil, biz.ErrImportWorkerConfiguration
	}
	return &CandidateReader{client: client, verifier: verifier, connectionID: connectionID}, nil
}

// ReadResult fetches the job's report from the attempt's own result key. It
// bounds the read and refuses an unversioned result: an overwritten report must
// never masquerade as the original.
func (r *CandidateReader) ReadResult(ctx context.Context, task biz.DatasetImportTask, attempt biz.DatasetImportAttempt) (biz.ImportReport, error) {
	if r == nil || r.client == nil {
		return biz.ImportReport{}, biz.ErrImportWorkerConfiguration
	}
	if task.ApprovedPrefix != "inputs/"+task.TenantID {
		return biz.ImportReport{}, biz.ErrImportStateConflict
	}
	output, err := r.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(task.Bucket), Key: aws.String(attempt.ResultObjectKey),
	})
	if err != nil {
		return biz.ImportReport{}, biz.ErrInputSourceUnavailable
	}
	defer output.Body.Close()
	if aws.ToString(output.VersionId) == "" || aws.ToString(output.VersionId) == "null" {
		return biz.ImportReport{}, biz.ErrImportStateConflict
	}
	raw, err := io.ReadAll(io.LimitReader(output.Body, biz.MaxImportResultBytes+1))
	if err != nil || len(raw) > biz.MaxImportResultBytes {
		return biz.ImportReport{}, biz.ErrImportResultInvalid
	}
	result, err := biz.DecodeImportResult(raw)
	if err != nil {
		return biz.ImportReport{}, err
	}
	// The report must describe this exact attempt; a report naming another task
	// or attempt is a conflict even if its bytes verify.
	if result.TenantID != task.TenantID || result.TaskID != task.TaskID || result.AttemptID != attempt.AttemptID ||
		result.DatasetID != task.DatasetID || result.InputVersionID != task.ReservedInputVersionID {
		return biz.ImportReport{}, biz.ErrImportStateConflict
	}
	if result.RuntimePlanSHA256 != attempt.RuntimePlanSHA256 {
		return biz.ImportReport{}, biz.ErrImportStateConflict
	}
	// The report's own object identity is part of the frozen candidate: its
	// VersionID and digest must be durable facts, not inferred from the key.
	digest := sha256.Sum256(raw)
	return biz.ImportReport{
		Result: result, VersionID: aws.ToString(output.VersionId), SHA256: hex.EncodeToString(digest[:]),
	}, nil
}

// VerifyCandidate re-reads the exact object version the report claims and
// derives the trusted facts. The preview is the same bounded read's bytes.
func (r *CandidateReader) VerifyCandidate(ctx context.Context, task biz.DatasetImportTask, result biz.ImportResult) (biz.VerifiedAlpaca, []byte, error) {
	if r == nil || r.verifier == nil || result.Object == nil {
		return biz.VerifiedAlpaca{}, nil, biz.ErrImportResultInvalid
	}
	object := result.Object
	if object.StorageConnectionID != r.connectionID || object.Bucket != task.Bucket ||
		object.VersionID == "" || object.VersionID == "null" || object.SizeBytes <= 0 || len(object.SHA256) != 64 {
		return biz.VerifiedAlpaca{}, nil, biz.ErrImportStateConflict
	}
	scope := cpup01.StorageScope{
		StorageConnectionID: task.StorageConnectionID, Bucket: task.Bucket,
		ApprovedPrefix: task.ApprovedPrefix, CredentialReference: task.CredentialReference,
	}
	version := object.VersionID
	ref := cpup01.FixedObjectRef{
		StorageConnectionID: object.StorageConnectionID, Bucket: object.Bucket, Key: object.Key,
		VersionID: &version, SizeBytes: object.SizeBytes, SHA256: object.SHA256,
	}
	preview := &boundedPreview{}
	verified, err := r.verifier.WritePreparedAlpaca(ctx, scope, ref, preview)
	if err != nil {
		// Keep the concrete cause: the verification error alone cannot tell a
		// structural rejection from a transport or size mismatch.
		return biz.VerifiedAlpaca{}, nil, fmt.Errorf("verify candidate %s: %w", object.Key, err)
	}
	// The version row stores a jsonb object, so the preview must be rendered from
	// the collected records rather than passed through as raw JSONL bytes.
	document, err := preview.Object(verified.RowCount)
	if err != nil {
		return biz.VerifiedAlpaca{}, nil, fmt.Errorf("render candidate preview %s: %w", object.Key, err)
	}
	return verified, document, nil
}

// Preview budgets from design §11.1: at most 20 records, 2000 code points per
// display field, and a bounded serialised body.
const (
	previewMaxRecords = 20
	previewFieldRunes = 2000
	previewLimitBytes = 256 << 10
)

// boundedPreview collects the leading derived records and renders the preview
// object the version row stores. It never keeps the whole file: after the record
// budget is reached it keeps consuming writes and reporting the full length, so
// the verification stream and its digest are unaffected.
type boundedPreview struct {
	records  []map[string]string
	partial  []byte
	overflow bool
}

func (preview *boundedPreview) Write(p []byte) (int, error) {
	if len(preview.records) >= previewMaxRecords {
		preview.overflow = true
		return len(p), nil
	}
	// The writer receives arbitrary chunks, so a record may span several writes.
	preview.partial = append(preview.partial, p...)
	for {
		// The converter emits one JSON object per line; LF is the only separator.
		index := bytes.IndexByte(preview.partial, 0x0a)
		if index < 0 {
			break
		}
		line := preview.partial[:index]
		preview.partial = preview.partial[index+1:]
		if len(preview.records) >= previewMaxRecords {
			preview.overflow = true
			break
		}
		var record map[string]string
		if err := json.Unmarshal(line, &record); err != nil {
			// The converter already validated every record; an unparsable line
			// here means the stream is not what was verified.
			return 0, biz.ErrInputVerification
		}
		preview.records = append(preview.records, record)
	}
	return len(p), nil
}

// Object renders the preview as the JSON object the version row requires. The
// preview is display-only: truncation is stated explicitly and never affects the
// published facts.
func (preview *boundedPreview) Object(totalRows int64) ([]byte, error) {
	samples := make([]map[string]any, 0, len(preview.records))
	for index, record := range preview.records {
		display := map[string]any{}
		truncated := make([]string, 0, 3)
		for _, key := range []string{"instruction", "input", "output"} {
			value := record[key]
			if runes := []rune(value); len(runes) > previewFieldRunes {
				value = string(runes[:previewFieldRunes])
				truncated = append(truncated, key)
			}
			display[key] = value
		}
		display["sample_index"] = index + 1
		display["truncated_fields"] = truncated
		samples = append(samples, display)
	}
	value := map[string]any{
		"schema":            "ani.modeldev.dataset.version-preview.v1",
		"total_rows":        totalRows,
		"samples":           samples,
		"preview_truncated": preview.overflow || int64(len(preview.records)) < totalRows,
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, biz.ErrInputVerification
	}
	if len(raw) > previewLimitBytes {
		// Shrinking the sample count is allowed; exceeding the body budget is not.
		value["samples"] = samples[:0]
		value["preview_truncated"] = true
		if raw, err = json.Marshal(value); err != nil {
			return nil, biz.ErrInputVerification
		}
	}
	return raw, nil
}

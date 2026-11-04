package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

const (
	planTenant  = "11111111-1111-4111-8111-111111111111"
	planDataset = "22222222-2222-4222-8222-222222222222"
	planTask    = "33333333-3333-4333-8333-333333333333"
	planAttempt = "44444444-4444-4444-8444-444444444444"
	planInput   = "55555555-5555-4555-8555-555555555555"
	planSHA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func basePlanMap() map[string]any {
	return map[string]any{
		"schema": ImportPlanSchema, "tenant_id": planTenant, "dataset_id": planDataset,
		"task_id": planTask, "attempt_id": planAttempt, "input_version_id": planInput,
		"source_snapshot_sha256": planSHA, "runtime_plan_sha256": planSHA,
		"importer_image_digest": "registry.example.test/importer@sha256:" + planSHA,
		"provider":              "HUGGINGFACE", "repo_id": "example/support-alpaca",
		"requested_revision": "main", "resolved_revision": "abcdef0123456789",
		"file_path": "alpaca.json", "lock_kind": "IMMUTABLE_REVISION",
		"expected_size_bytes": "4096", "expected_file_sha256": "",
		"data":            map[string]any{"storage_connection_id": "s", "bucket": "inputs", "key": "inputs/t/data/a.json"},
		"result":          map[string]any{"storage_connection_id": "s", "bucket": "inputs", "key": "inputs/t/result/a.json"},
		"credential_file": "/var/run/ani/creds.json",
		"deadline":        "2026-11-04T01:00:00.000000Z",
	}
}

func planJSON(m map[string]any) []byte {
	raw, _ := json.Marshal(m)
	return raw
}

func TestDecodeImportPlanAcceptsValidPlan(t *testing.T) {
	plan, err := DecodeImportPlan(planJSON(basePlanMap()))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if plan.Provider != biz.ProviderHuggingFace || plan.ExpectedSizeBytes != 4096 {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.Data.Key == plan.Result.Key {
		t.Fatal("data and result keys collapsed")
	}
}

func TestDecodeImportPlanRejectsUnknownAndDuplicate(t *testing.T) {
	withUnknown := basePlanMap()
	withUnknown["retryable"] = true
	if _, err := DecodeImportPlan(planJSON(withUnknown)); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("unknown key accepted: %v", err)
	}

	raw := strings.Replace(string(planJSON(basePlanMap())), `"repo_id":`, `"repo_id":"x","repo_id":`, 1)
	if _, err := DecodeImportPlan([]byte(raw)); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("duplicate key accepted: %v", err)
	}
}

func TestDecodeImportPlanRejectsWeakLocks(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"immutable without revision": func(m map[string]any) { m["resolved_revision"] = "" },
		"file sha without digest":    func(m map[string]any) { m["lock_kind"] = "FILE_SHA256"; m["expected_file_sha256"] = "" },
		"file sha with short digest": func(m map[string]any) { m["lock_kind"] = "FILE_SHA256"; m["expected_file_sha256"] = "abc" },
		"unknown lock":               func(m map[string]any) { m["lock_kind"] = "LATEST" },
		"unknown provider":           func(m map[string]any) { m["provider"] = "GITHUB" },
		"zero size":                  func(m map[string]any) { m["expected_size_bytes"] = "0" },
		"absolute path":              func(m map[string]any) { m["file_path"] = "/abs.json" },
		"traversal":                  func(m map[string]any) { m["file_path"] = "a/../b.json" },
		"same keys": func(m map[string]any) {
			m["result"] = map[string]any{"storage_connection_id": "s", "bucket": "inputs", "key": "inputs/t/data/a.json"}
		},
	} {
		plan := basePlanMap()
		mutate(plan)
		if _, err := DecodeImportPlan(planJSON(plan)); !errors.Is(err, ErrPlanInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

func TestDecodeImportPlanRejectsOversizeAndTrailing(t *testing.T) {
	big := planJSON(basePlanMap())
	padded := append(big, bytes.Repeat([]byte(" "), MaxPlanBytes+1)...)
	if _, err := DecodeImportPlan(padded); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("oversize plan accepted: %v", err)
	}
	if _, err := DecodeImportPlan(append(big, []byte("{}")...)); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("trailing content accepted: %v", err)
	}
}

type fakeFetcher struct {
	body string
	err  error
}

func (f *fakeFetcher) Stream(ctx context.Context, plan ImportPlan) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.body)), nil
}

type fakeUploader struct {
	version string
	fail    error
	gotSize int64
	gotData string
}

func (u *fakeUploader) Put(ctx context.Context, target PlanObject, body io.Reader, size int64) (string, string, error) {
	if u.fail != nil {
		return "", "", u.fail
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		return "", "", err
	}
	u.gotSize = size
	u.gotData = string(raw)
	sum := sha256Hex(raw)
	return u.version, sum, nil
}

func planFor(t *testing.T, body string) ImportPlan {
	t.Helper()
	plan, err := DecodeImportPlan(planJSON(basePlanMap()))
	if err != nil {
		t.Fatal(err)
	}
	plan.ExpectedSizeBytes = int64(len(body))
	return plan
}

func TestRunImportUploadsExactBytes(t *testing.T) {
	body := `[{"instruction":"a","input":"","output":"b"}]`
	plan := planFor(t, body)
	uploader := &fakeUploader{version: "version-1"}
	outcome := RunImport(context.Background(), plan, &fakeFetcher{body: body}, uploader, t.TempDir())
	if outcome.Object.VersionID != "version-1" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if uploader.gotData != body || uploader.gotSize != int64(len(body)) {
		t.Fatalf("uploaded %d bytes, size field %d", len(uploader.gotData), uploader.gotSize)
	}
	if outcome.Object.SHA256 != sha256Hex([]byte(body)) {
		t.Fatal("reported digest differs from uploaded bytes")
	}
}

func TestRunImportDetectsShortAndLongStreams(t *testing.T) {
	plan := planFor(t, "0123456789")
	for name, body := range map[string]string{
		"short": "01234",
		"long":  "0123456789ABC",
	} {
		outcome := RunImport(context.Background(), plan, &fakeFetcher{body: body}, &fakeUploader{version: "v"}, t.TempDir())
		if outcome.Failure != biz.FailSourceChanged || outcome.Stage != biz.StageDownload {
			t.Fatalf("%s stream returned %+v", name, outcome)
		}
	}
}

func TestRunImportDetectsSHAMismatch(t *testing.T) {
	plan := planFor(t, "0123456789")
	plan.ExpectedFileSHA256 = strings.Repeat("b", 64)
	outcome := RunImport(context.Background(), plan, &fakeFetcher{body: "0123456789"}, &fakeUploader{version: "v"}, t.TempDir())
	if outcome.Failure != biz.FailSourceChanged {
		t.Fatalf("sha mismatch returned %+v", outcome)
	}
}

func TestRunImportClassifiesSourceErrors(t *testing.T) {
	plan := planFor(t, "0123456789")
	for source, want := range map[error]biz.ImportFailureCode{
		biz.ErrSourceNotFound:     biz.FailSourceNotFound,
		biz.ErrSourceAuthRequired: biz.FailSourceAuthRequired,
	} {
		outcome := RunImport(context.Background(), plan, &fakeFetcher{err: source}, &fakeUploader{version: "v"}, t.TempDir())
		if outcome.Failure != want || outcome.Stage != biz.StageDownload {
			t.Fatalf("%v returned %+v", source, outcome)
		}
	}
}

func TestRunImportRejectsMissingUploaderVersion(t *testing.T) {
	body := "0123456789"
	outcome := RunImport(context.Background(), planFor(t, body), &fakeFetcher{body: body}, &fakeUploader{version: ""}, t.TempDir())
	if outcome.Failure != biz.FailDependencyUnavailable {
		t.Fatalf("missing version returned %+v", outcome)
	}
}

func TestReportRoundTripsThroughEncoderAndDecoder(t *testing.T) {
	report := biz.ImportResult{
		Status: biz.ImportResultSucceeded, TenantID: planTenant, DatasetID: planDataset,
		TaskID: planTask, AttemptID: planAttempt, InputVersionID: planInput,
		SourceSnapshotSHA256: planSHA, RuntimePlanSHA256: planSHA,
		ImporterImageDigest: "registry.example.test/importer@sha256:" + planSHA,
		NamespaceUID:        "66666666-6666-4666-8666-666666666666",
		JobUID:              "77777777-7777-4777-8777-777777777777",
		PodUID:              "88888888-8888-4888-8888-888888888888",
		CompletedAt:         time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC),
		Object: &biz.ImportResultObject{
			StorageConnectionID: "s", Bucket: "inputs", Key: "inputs/t/data/a.json",
			VersionID: "version-1", SizeBytes: 4096, SHA256: planSHA,
		},
	}
	raw, err := biz.EncodeImportResult(report)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := biz.DecodeImportResult(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Object == nil || decoded.Object.VersionID != "version-1" || decoded.Object.SizeBytes != 4096 {
		t.Fatalf("round trip lost the object: %+v", decoded)
	}
}

func sha256Hex(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

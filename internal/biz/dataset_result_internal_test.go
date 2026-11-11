package biz

import (
	"errors"
	"strings"
	"testing"
)

func succeededResult(overrides map[string]string) string {
	fields := map[string]string{
		"schema":                 `"` + ImportResultSchema + `"`,
		"status":                 `"SUCCEEDED"`,
		"tenant_id":              `"11111111-1111-4111-8111-111111111111"`,
		"dataset_id":             `"22222222-2222-4222-8222-222222222222"`,
		"task_id":                `"33333333-3333-4333-8333-333333333333"`,
		"attempt_id":             `"44444444-4444-4444-8444-444444444444"`,
		"input_version_id":       `"55555555-5555-4555-8555-555555555555"`,
		"source_snapshot_sha256": `"` + repeatHex("a") + `"`,
		"runtime_plan_sha256":    `"` + repeatHex("b") + `"`,
		"importer_image_digest":  `"registry.example.test/importer@sha256:` + repeatHex("c") + `"`,
		"namespace_uid":          `"66666666-6666-4666-8666-666666666666"`,
		"job_uid":                `"77777777-7777-4777-8777-777777777777"`,
		"pod_uid":                `"88888888-8888-4888-8888-888888888888"`,
		"completed_at":           `"2026-11-04T00:00:00.000000Z"`,
	}
	for key, value := range overrides {
		fields[key] = value
	}
	return "{" + joinFields(fields) + "}"
}

func failedResult(overrides map[string]string) string {
	fields := map[string]string{
		"schema":                 `"` + ImportResultSchema + `"`,
		"status":                 `"FAILED"`,
		"tenant_id":              `"11111111-1111-4111-8111-111111111111"`,
		"dataset_id":             `"22222222-2222-4222-8222-222222222222"`,
		"task_id":                `"33333333-3333-4333-8333-333333333333"`,
		"attempt_id":             `"44444444-4444-4444-8444-444444444444"`,
		"input_version_id":       `"55555555-5555-4555-8555-555555555555"`,
		"source_snapshot_sha256": `"` + repeatHex("a") + `"`,
		"runtime_plan_sha256":    `"` + repeatHex("b") + `"`,
		"importer_image_digest":  `"registry.example.test/importer@sha256:` + repeatHex("c") + `"`,
		"namespace_uid":          `"66666666-6666-4666-8666-666666666666"`,
		"job_uid":                `"77777777-7777-4777-8777-777777777777"`,
		"pod_uid":                `"88888888-8888-4888-8888-888888888888"`,
		"completed_at":           `"2026-11-04T00:00:00.000000Z"`,
		"failure_code":           `"SOURCE_CHANGED"`,
		"failure_stage":          `"DOWNLOAD"`,
	}
	for key, value := range overrides {
		fields[key] = value
	}
	return "{" + joinFields(fields) + "}"
}

func repeatHex(fill string) string {
	out := ""
	for len(out) < 64 {
		out += fill
	}
	return out[:64]
}

func joinFields(fields map[string]string) string {
	parts := make([]string, 0, len(fields))
	for key, value := range fields {
		parts = append(parts, `"`+key+`":`+value)
	}
	return strings.Join(parts, ",")
}

const successObject = `"object":{"storage_connection_id":"input-store-v1","bucket":"inputs","key":"inputs/11111111-1111-4111-8111-111111111111/data/77777777-7777-4777-8777-777777777777/data.json","version_id":"version-1","size_bytes":4096,"sha256":"` + `REPLACE_SHA` + `"}`

func TestDecodeImportResultSuccess(t *testing.T) {
	raw := strings.TrimSuffix(succeededResult(nil), "}") + "," + strings.ReplaceAll(successObject, "REPLACE_SHA", repeatHex("d")) + "}"
	result, err := DecodeImportResult([]byte(raw))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Status != ImportResultSucceeded || result.Object == nil || result.Object.SizeBytes != 4096 {
		t.Fatalf("result = %+v", result)
	}
	if result.FailureCode != "" || result.FailureStage != "" {
		t.Fatal("SUCCEEDED carried failure fields")
	}
}

func TestDecodeImportResultFailure(t *testing.T) {
	result, err := DecodeImportResult([]byte(failedResult(nil)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Status != ImportResultFailed || result.FailureCode != FailSourceChanged || result.FailureStage != StageDownload {
		t.Fatalf("result = %+v", result)
	}
	if result.Object != nil {
		t.Fatal("FAILED carried an object")
	}
}

func TestDecodeImportResultRejectsDuplicatesAndUnknown(t *testing.T) {
	base := strings.TrimSuffix(failedResult(nil), "}")
	for name, raw := range map[string]string{
		"duplicate":      base + `,"status":"FAILED"}`,
		"unknown":        base + `,"retryable":true}`,
		"trailing":       failedResult(nil) + `{}`,
		"data-ish field": base + `,"row_count":"10"}`,
	} {
		if _, err := DecodeImportResult([]byte(raw)); !errors.Is(err, ErrImportResultInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

func TestDecodeImportResultRejectsStatusSpecificDrift(t *testing.T) {
	base := strings.TrimSuffix(failedResult(nil), "}")
	for name, raw := range map[string]string{
		"success without object": succeededResult(nil),
		"failure with object":    base + "," + successObject + "}",
		"success with failure":   strings.TrimSuffix(succeededResult(nil), "}") + `,"failure_code":"SOURCE_CHANGED"}`,
	} {
		if _, err := DecodeImportResult([]byte(raw)); !errors.Is(err, ErrImportResultInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

func TestDecodeImportResultRejectsBadEnumAndTypes(t *testing.T) {
	for name, overrides := range map[string]map[string]string{
		"unknown status":  {"status": `"PARTIAL"`},
		"unknown code":    {"failure_code": `"SOMETHING"`},
		"unknown stage":   {"failure_stage": `"PUBLISH"`},
		"numeric tenant":  {"tenant_id": `123`},
		"null completed":  {"completed_at": `null`},
		"bad time":        {"completed_at": `"2026-11-04"`},
		"non-microsecond": {"completed_at": `"2026-11-04T00:00:00.000000001Z"`},
		"wrong schema":    {"schema": `"ani.modeldev.dataset.import-result.v2"`},
	} {
		overrides := overrides
		if _, err := DecodeImportResult([]byte(failedResult(overrides))); !errors.Is(err, ErrImportResultInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

func TestDecodeImportResultRejectsObjectDrift(t *testing.T) {
	good := strings.ReplaceAll(successObject, "REPLACE_SHA", repeatHex("d"))
	cases := map[string]string{
		"empty version": strings.Replace(good, `"version_id":"version-1"`, `"version_id":""`, 1),
		"null version":  strings.Replace(good, `"version_id":"version-1"`, `"version_id":"null"`, 1),
		"zero size":     strings.Replace(good, `"size_bytes":4096`, `"size_bytes":0`, 1),
		"negative size": strings.Replace(good, `"size_bytes":4096`, `"size_bytes":-1`, 1),
		"upper sha":     strings.Replace(good, repeatHex("d"), strings.Repeat("D", 64), 1),
		"short sha":     strings.Replace(good, repeatHex("d"), repeatHex("d")[:32], 1),
		"unknown field": strings.Replace(good, `"sha256"`, `"etag":"x","sha256"`, 1),
	}
	for name, object := range cases {
		raw := strings.TrimSuffix(succeededResult(nil), "}") + "," + object + "}"
		if _, err := DecodeImportResult([]byte(raw)); !errors.Is(err, ErrImportResultInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

func TestReadImportResultEnforcesCap(t *testing.T) {
	// Exactly at the cap is rejected only if the content itself is invalid;
	// one byte over must be rejected regardless of content.
	big := strings.Replace(failedResult(nil), `"SOURCE_CHANGED"`, `"`+strings.Repeat("x", MaxImportResultBytes)+`"`, 1)
	if _, err := ReadImportResult(strings.NewReader(big)); !errors.Is(err, ErrImportResultInvalid) {
		t.Fatalf("oversize body accepted: %v", err)
	}
	if _, err := ReadImportResult(strings.NewReader(failedResult(nil))); err != nil {
		t.Fatalf("valid body rejected: %v", err)
	}
	if _, err := ReadImportResult(nil); !errors.Is(err, ErrImportResultInvalid) {
		t.Fatal("nil reader accepted")
	}
}

func TestDecodeImportResultRejectsNonUTF8(t *testing.T) {
	raw := append([]byte(failedResult(nil)), 0xff)
	if _, err := DecodeImportResult(raw); !errors.Is(err, ErrImportResultInvalid) {
		t.Fatalf("invalid UTF-8 accepted: %v", err)
	}
}

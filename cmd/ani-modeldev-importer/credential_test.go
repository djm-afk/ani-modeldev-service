package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// writeCredentialFile writes the mounted Secret shape the importer reads.
func writeCredentialFile(t *testing.T, expires time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	payload, err := json.Marshal(map[string]string{
		"access_key_id": "temporary-key", "secret_access_key": "temporary-secret",
		"session_token": "temporary-session", "expiration": expires.UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A session that has already lapsed must be refused before it signs anything.
// Letting the store answer AccessDenied turned a deadline problem into an opaque
// dependency failure.
func TestLoadCredentialRejectsAnExpiredSession(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	path := writeCredentialFile(t, now.Add(-time.Minute))
	if _, err := loadCredential(path, now); !errors.Is(err, errCredentialsExpired) {
		t.Fatalf("an expired session was accepted: %v", err)
	}
}

// A session that still has time left must be usable: the check is about the
// clock, not about the session being short.
func TestLoadCredentialAcceptsALiveSession(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	path := writeCredentialFile(t, now.Add(time.Minute))
	credential, err := loadCredential(path, now)
	if err != nil {
		t.Fatalf("a live session was refused: %v", err)
	}
	if credential.AccessKeyID != "temporary-key" || credential.SessionToken != "temporary-session" {
		t.Fatalf("credential = %+v", credential)
	}
}

// The expiry is re-read on every call, so a session that lapses between the
// download and the upload is caught at the upload rather than at the store.
func TestLoadCredentialRechecksTheClockOnEveryCall(t *testing.T) {
	issued := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	path := writeCredentialFile(t, issued.Add(15*time.Minute))

	// The download finishes inside the session window.
	if _, err := loadCredential(path, issued.Add(10*time.Minute)); err != nil {
		t.Fatalf("a session inside its window was refused: %v", err)
	}
	// The upload starts after the session lapsed.
	if _, err := loadCredential(path, issued.Add(16*time.Minute)); !errors.Is(err, errCredentialsExpired) {
		t.Fatalf("a lapsed session was accepted at upload time: %v", err)
	}
}

// A credential file without an expiry keeps working: the field is optional so an
// older Secret shape is not rejected outright.
func TestLoadCredentialAcceptsAFileWithoutExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	payload, err := json.Marshal(map[string]string{
		"access_key_id": "temporary-key", "secret_access_key": "temporary-secret",
		"session_token": "temporary-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCredential(path, time.Now()); err != nil {
		t.Fatalf("a file without expiry was refused: %v", err)
	}
}

// An expired session is a deadline problem, not a store outage: the report must
// say IMPORT_TIMEOUT so the page does not send operators after the object store.
func TestClassifyUploadErrorMapsAnExpiredSessionToTimeout(t *testing.T) {
	if got := classifyUploadError(errCredentialsExpired); got != biz.FailTimeout {
		t.Fatalf("an expired session reported %s, want IMPORT_TIMEOUT", got)
	}
}

// A staging volume that fills up is a resource problem, not a hub problem.
func TestRunImportReportsAFullStagingVolumeAsResourceUnavailable(t *testing.T) {
	body := `[{"instruction":"a","input":"","output":"b"}]`
	plan := planFor(t, body)
	// A work directory that cannot be created stands in for a volume that cannot
	// take the bytes: both are local staging failures.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	outcome := RunImport(context.Background(), plan, &fakeFetcher{body: body}, &fakeUploader{version: "v"}, blocked)
	if outcome.Failure != biz.FailResourceUnavailable || outcome.Stage != biz.StageDownload {
		t.Fatalf("a staging failure returned %+v", outcome)
	}
}

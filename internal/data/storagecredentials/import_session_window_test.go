package storagecredentials_test

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/storagecredentials"
)

const (
	importTenant    = "11111111-1111-4111-8111-111111111111"
	importDataKey   = "inputs/11111111-1111-4111-8111-111111111111/data/44444444-4444-4444-8444-444444444444/data.json"
	importResultKey = "inputs/11111111-1111-4111-8111-111111111111/result/44444444-4444-4444-8444-444444444444/result.json"
	importPolicySHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func importIssuerConfig(server *httptest.Server) storagecredentials.Config {
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	return storagecredentials.Config{
		Endpoint: server.URL, Region: "us-east-1", RootCAs: roots, Timeout: 5 * time.Second,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "synthetic-owner", SecretAccessKey: "synthetic-owner-secret"}, nil
		}),
		InputConnectionID: "input-store-v1", InputBucket: "cpu-inputs",
		PublicationConnectionID: "artifact-store-v1", PublicationBucket: "cpu-artifacts",
		ModelConnectionID: "model-store-v1", ModelBucket: "ani-models",
	}
}

func importSessionRequest(deadline time.Time) biz.ImportSessionRequest {
	now := time.Now().UTC()
	return biz.ImportSessionRequest{
		TenantID: importTenant, DatasetID: "22222222-2222-4222-8222-222222222222",
		TaskID: "33333333-3333-4333-8333-333333333333", AttemptID: "44444444-4444-4444-8444-444444444444",
		IssuanceID:     "55555555-5555-4555-8555-555555555555",
		TaskLeaseEpoch: 3, ReconcileEpoch: 2,
		Policy: []byte(`{"Statement":[]}`), PolicySHA256: importPolicySHA,
		IntendedAt: now, RequestDeadline: now.Add(time.Minute), AuthDeadline: deadline,
	}
}

func importSessionGrant() biz.ImportSessionGrant {
	return biz.ImportSessionGrant{
		StorageConnectionID: "input-store-v1", Bucket: "cpu-inputs",
		DataKey: importDataKey, ResultKey: importResultKey,
	}
}

// The session must cover the attempt's frozen deadline. The Secret is written
// once and never refreshed, so a session that lapses before the deadline fails
// the upload after a slow download with an opaque AccessDenied.
func TestIssueImportSessionRejectsASessionShorterThanTheDeadline(t *testing.T) {
	deadline := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The backend returns fifteen minutes for a one-hour attempt.
		writeCredentials(w, "temporary-key", time.Now().Add(15*time.Minute))
	}))
	defer server.Close()
	issuer, err := storagecredentials.New(importIssuerConfig(server))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.IssueImportSession(context.Background(), importSessionRequest(deadline), importSessionGrant()); err == nil {
		t.Fatal("a session shorter than the attempt deadline was accepted")
	}
}

// A session that covers the deadline is usable.
func TestIssueImportSessionAcceptsASessionThatCoversTheDeadline(t *testing.T) {
	deadline := time.Now().UTC().Add(30 * time.Minute).Truncate(time.Second)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeCredentials(w, "temporary-key", deadline.Add(15*time.Minute))
	}))
	defer server.Close()
	issuer, err := storagecredentials.New(importIssuerConfig(server))
	if err != nil {
		t.Fatal(err)
	}
	session, err := issuer.IssueImportSession(context.Background(), importSessionRequest(deadline), importSessionGrant())
	if err != nil {
		t.Fatalf("a covering session was refused: %v", err)
	}
	if !session.Issued || session.Credentials.AccessKeyID != "temporary-key" {
		t.Fatalf("session = %+v", session)
	}
}

// The requested duration must follow the attempt window, not a fixed 900s. A
// fixed value made every import longer than fifteen minutes fail at upload time.
func TestIssueImportSessionRequestsTheRemainingWindow(t *testing.T) {
	// The expiry is written at second precision, so the deadline is rounded down
	// to keep the covering check exact.
	deadline := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	var requested string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("invalid STS form: %v", err)
		}
		requested = r.Form.Get("DurationSeconds")
		writeCredentials(w, "temporary-key", deadline)
	}))
	defer server.Close()
	issuer, err := storagecredentials.New(importIssuerConfig(server))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.IssueImportSession(context.Background(), importSessionRequest(deadline), importSessionGrant()); err != nil {
		t.Fatalf("issue: %v", err)
	}
	// Two hours minus the few milliseconds this test has already spent.
	if requested == "900" || requested == "" {
		t.Fatalf("the session asked for %q, want the remaining window", requested)
	}
}

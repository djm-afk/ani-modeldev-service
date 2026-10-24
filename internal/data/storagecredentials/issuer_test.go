package storagecredentials_test

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/storagecredentials"
)

func TestIssuerScopesPublishCredentialsToExecutionAndRenews(t *testing.T) {
	execution := executionFixture(t)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/" || r.URL.RawQuery != "" || r.ParseForm() != nil {
			t.Error("STS must use a signed form POST to the configured root")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=synthetic-owner/") || !strings.Contains(r.Header.Get("Authorization"), "/us-east-1/s3/aws4_request") || r.Header.Get("X-Amz-Security-Token") != "" {
			t.Error("STS must use the restricted owner's long-term SigV4 credential")
		}
		digest := sha256.Sum256([]byte(r.Form.Encode()))
		if r.Header.Get("X-Amz-Content-Sha256") != hex.EncodeToString(digest[:]) {
			t.Error("STS must expose its signed S3 payload hash")
		}
		if r.Form.Get("Action") != "AssumeRole" || r.Form.Get("Version") != "2011-06-15" || r.Form.Get("DurationSeconds") != "900" || r.Form.Get("RoleSessionName") != "modeldev-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee-publish" {
			t.Error("STS action, bounded duration or execution session identity changed")
		}
		want := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject","s3:GetObjectVersion","s3:PutObject"],"Resource":["arn:aws:s3:::cpu-artifacts/tenant-fixed/executions/aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee/*"]}]}`
		if r.Form.Get("Policy") != want {
			t.Errorf("publication session must be limited to this execution: %s", r.Form.Get("Policy"))
		}
		id := requests.Add(1)
		writeCredentials(w, fmt.Sprintf("temporary-%d", id), time.Now().Add(15*time.Minute))
	}))
	defer server.Close()
	issuer, err := storagecredentials.New(configFixture(server, execution))
	if err != nil {
		t.Fatal(err)
	}
	first, err := issuer.IssueForExecution(context.Background(), execution, "publish")
	if err != nil || first.AccessKeyID != "temporary-1" || first.SessionToken != "temporary-session" || !first.ExpiresAt.After(time.Now()) {
		t.Fatalf("issue a usable expiring session: %+v / %v", first, err)
	}
	renewed, err := issuer.IssueForExecution(context.Background(), execution, "publish")
	if err != nil || renewed.AccessKeyID != "temporary-2" || requests.Load() != 2 {
		t.Fatalf("renew with the same execution restrictions: %+v / %v", renewed, err)
	}
}

func TestIssuerPrepareLimitsReadingToFrozenInputVersion(t *testing.T) {
	execution := executionFixture(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ParseForm() != nil {
			t.Fatal("invalid STS form")
		}
		want := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObjectVersion"],"Resource":["arn:aws:s3:::cpu-inputs/fixed/input/data.csv"],"Condition":{"StringEquals":{"s3:VersionId":["input-version-1"]}}}]}`
		if r.Form.Get("Policy") != want {
			t.Errorf("prepare may read only the frozen input version: %s", r.Form.Get("Policy"))
		}
		writeCredentials(w, "temporary-input", time.Now().Add(15*time.Minute))
	}))
	defer server.Close()
	issuer, err := storagecredentials.New(configFixture(server, execution))
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := issuer.IssueForExecution(context.Background(), execution, "prepare")
	if err != nil || credentials.ObjectKey != "fixed/input/data.csv" || credentials.ObjectPrefix != "" || credentials.Bucket != "cpu-inputs" {
		t.Fatalf("fixed input session scope changed: %+v / %v", credentials, err)
	}
}

func TestIssuerRejectsUnapprovedExecutionScopesBeforeAssumeRole(t *testing.T) {
	for _, mode := range []string{"other tenant", "other bucket", "other connection", "sibling prefix", "wildcard key", "expired execution", "unsupported step", "temporary owner"} {
		t.Run(mode, func(t *testing.T) {
			execution := executionFixture(t)
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				writeCredentials(w, "unexpected", time.Now().Add(15*time.Minute))
			}))
			defer server.Close()
			config := configFixture(server, execution)
			purpose := "prepare"
			switch mode {
			case "other tenant":
				execution.TenantID = "99999999-2222-4333-8444-555555555555"
			case "other bucket":
				execution.Snapshot.Input.Object.Bucket = "other-inputs"
			case "other connection":
				execution.Snapshot.Input.Object.StorageConnectionID = "other-store"
			case "sibling prefix":
				execution.Snapshot.Input.Object.Key = "fixed/input-other/data.csv"
			case "wildcard key":
				execution.Snapshot.Input.Object.Key = "fixed/input/*.csv"
			case "expired execution":
				execution.Snapshot.DeadlineAt = time.Now().UTC().Truncate(time.Microsecond).Add(-time.Second)
				execution.AcceptedAt = execution.Snapshot.DeadlineAt.Add(-time.Hour)
				execution.Admission = admissionfixture.Bind(t, execution.Admission)
			case "unsupported step":
				purpose = "train-wait"
			case "temporary owner":
				config.Credentials = aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{AccessKeyID: "temporary-owner", SecretAccessKey: "temporary-owner-secret", SessionToken: "temporary-token", CanExpire: true, Expires: time.Now().Add(time.Hour)}, nil
				})
			}
			var err error
			execution.SpecHash, err = execution.Snapshot.Digest()
			if err != nil && mode != "wildcard key" {
				t.Fatal(err)
			}
			issuer, err := storagecredentials.New(config)
			if err != nil {
				t.Fatal(err)
			}
			got, err := issuer.IssueForExecution(context.Background(), execution, purpose)
			if !errors.Is(err, storagecredentials.ErrUnavailable) || got != (biz.TemporaryStorageCredentials{}) || calls.Load() != 0 {
				t.Fatalf("unapproved scope must fail before STS: calls=%d, error=%v", calls.Load(), err)
			}
		})
	}
}

func TestIssuerRejectsExpiredIncompleteAndFailedSessionsWithoutLeakingBodies(t *testing.T) {
	for _, mode := range []string{"expired", "too long", "missing token", "upstream failure"} {
		t.Run(mode, func(t *testing.T) {
			execution := executionFixture(t)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "expired":
					writeCredentials(w, "secret-key-must-not-leak", time.Now().Add(-time.Second))
				case "too long":
					writeCredentials(w, "secret-key-must-not-leak", time.Now().Add(time.Hour))
				case "missing token":
					_, _ = fmt.Fprint(w, `<AssumeRoleResponse><AssumeRoleResult><Credentials><AccessKeyId>secret-key-must-not-leak</AccessKeyId><SecretAccessKey>private-secret</SecretAccessKey></Credentials></AssumeRoleResult></AssumeRoleResponse>`)
				case "upstream failure":
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = fmt.Fprint(w, "private-secret https://signed.example/token")
				}
			}))
			defer server.Close()
			issuer, err := storagecredentials.New(configFixture(server, execution))
			if err != nil {
				t.Fatal(err)
			}
			got, err := issuer.IssueForExecution(context.Background(), execution, "prepare")
			if !errors.Is(err, storagecredentials.ErrUnavailable) || got != (biz.TemporaryStorageCredentials{}) || err.Error() != "execution storage credentials unavailable" {
				t.Fatalf("unusable session must expose only the finite failure: %v", err)
			}
		})
	}
}

func executionFixture(t *testing.T) biz.Execution {
	t.Helper()
	snapshot := conformance.SnapshotV1()
	snapshot.DeadlineAt = time.Now().UTC().Add(30 * time.Minute).Truncate(time.Microsecond)
	intent := cpup01.Intent{Name: "execution-storage-session", Kind: "GENERAL_TRAINING", ImplementationID: snapshot.Implementation.ImplementationID, ComputePresetID: snapshot.ComputeOrigin.ComputePresetID, MLP: &cpup01.MLPParameters{}, DatasetVersionID: snapshot.Input.InputVersionID}
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	specHash := ""
	return biz.Execution{Admission: admissionfixture.Bind(t, biz.Admission{
		TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:user:42",
		OperationID: "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff", ExecutionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		Intent: intent, IntentHash: intentHash, SpecHash: specHash, Snapshot: snapshot, AcceptedAt: snapshot.DeadlineAt.Add(-30 * time.Minute),
	})}
}

func configFixture(server *httptest.Server, execution biz.Execution) storagecredentials.Config {
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	return storagecredentials.Config{
		Endpoint: server.URL, Region: "us-east-1", RootCAs: roots, Timeout: 5 * time.Second,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "synthetic-owner", SecretAccessKey: "synthetic-owner-secret"}, nil
		}),
		Bindings: []storagecredentials.Binding{
			{TenantID: execution.TenantID, Purpose: "prepare", ConnectionID: "input-store-v1", Bucket: "cpu-inputs", Prefix: "fixed/input"},
			{TenantID: execution.TenantID, Purpose: "publish", ConnectionID: "artifact-store-v1", Bucket: "cpu-artifacts", Prefix: "tenant-fixed/executions"},
		},
	}
}

func writeCredentials(w http.ResponseWriter, key string, expires time.Time) {
	w.Header().Set("Content-Type", "application/xml")
	_, _ = fmt.Fprintf(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>temporary-secret</SecretAccessKey><SessionToken>temporary-session</SessionToken><Expiration>%s</Expiration></Credentials></AssumeRoleResult></AssumeRoleResponse>`, key, expires.UTC().Format(time.RFC3339))
}

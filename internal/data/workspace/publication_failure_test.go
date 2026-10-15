package workspace_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/workspace"
)

func TestPublicationFailureRetainsOnlySafePhaseAndStorageFacts(t *testing.T) {
	const secret = "private-key credential-value https://private.invalid/object?token=signed-secret"
	for _, test := range []struct {
		name, target, remoteCode, phase, reason, code string
		reconcile                                     string
		status, wantStatus                            int
		captureMissing                                bool
	}{
		{name: "SDK access denied", status: 403, remoteCode: "AccessDenied", phase: "put", reason: "storage", wantStatus: 403, code: "AccessDenied"},
		{name: "SDK arbitrary code", status: 500, remoteCode: "private-untrusted-code", phase: "put", reason: "storage", wantStatus: 500, code: "unknown"},
		{name: "PUT lacks version", status: 200, phase: "put", reason: "version", code: "none"},
		{name: "manifest PUT denied", target: "/output-manifest.json", status: 403, remoteCode: "AccessDenied", phase: "put-manifest", reason: "storage", wantStatus: 403, code: "AccessDenied"},
		{name: "bundle PUT denied", target: "/bundle.tar", status: 403, remoteCode: "AccessDenied", phase: "put-bundle", reason: "storage", wantStatus: 403, code: "AccessDenied"},
		{name: "existing HEAD denied", reconcile: "head", phase: "put", reason: "head-existing", wantStatus: 403, code: "unknown"},
		{name: "existing GET denied", reconcile: "verify", phase: "put", reason: "verify-existing", code: "none"},
		{name: "local capture missing", captureMissing: true, phase: "capture", reason: "content", code: "none"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory, execution, _ := finetuningOutputWorkspace(t)
			output, err := workspace.Collect(context.Background(), directory, execution)
			if err != nil {
				t.Fatal(err)
			}
			puts := 0
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				puts++
				if test.reconcile != "" {
					w.Header().Set("Content-Type", "application/xml")
					if r.Method == http.MethodPut {
						w.WriteHeader(http.StatusPreconditionFailed)
						_, _ = io.WriteString(w, "<Error><Code>PreconditionFailed</Code></Error>")
					} else if r.Method == http.MethodHead && test.reconcile == "verify" {
						w.Header().Set("x-amz-version-id", "approved-version")
					} else {
						w.WriteHeader(http.StatusForbidden)
						_, _ = fmt.Fprintf(w, "<Error><Code>AccessDenied</Code><Message>%s</Message></Error>", secret)
					}
					return
				}
				if r.Method != http.MethodPut || r.Header.Get("If-None-Match") != "*" {
					t.Error("actual SDK request lost create-only PUT")
				}
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Error("request body unreadable")
				}
				if test.target != "" && !strings.HasSuffix(r.URL.Path, test.target) {
					w.Header().Set("x-amz-version-id", "approved-version")
					return
				}
				if test.status == 200 {
					w.WriteHeader(http.StatusOK)
					return
				}
				w.Header().Set("Content-Type", "application/xml")
				w.Header().Set("x-amz-request-id", secret)
				w.WriteHeader(test.status)
				_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message></Error>", test.remoteCode, secret)
			}))
			defer peer.Close()
			client := s3.New(s3.Options{Region: "test-region", BaseEndpoint: aws.String(peer.URL), UsePathStyle: true, HTTPClient: peer.Client(), Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired})
			if test.captureMissing {
				if err := os.Remove(filepath.Join(directory, "export/merged/model.safetensors")); err != nil {
					t.Fatal(err)
				}
			}
			_, err = workspace.UploadOutput(context.Background(), client, execution, directory, output)
			wantCause := biz.ErrObjectSourceUnavailable
			if test.captureMissing {
				wantCause = biz.ErrInvalidWorkspaceOutput
			}
			if !errors.Is(err, wantCause) {
				t.Fatalf("original error identity lost: %v", err)
			}
			var diagnostic interface {
				PublicationDiagnostic() (string, string, int, string)
			}
			if !errors.As(err, &diagnostic) {
				t.Fatalf("publication failure has no bounded phase diagnostic: %v", err)
			}
			phase, reason, status, code := diagnostic.PublicationDiagnostic()
			if phase != test.phase || reason != test.reason || status != test.wantStatus || code != test.code {
				t.Fatalf("wrong safe diagnostic: phase=%s reason=%s status=%d code=%s", phase, reason, status, code)
			}
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				for _, value := range []string{"private-key", "credential-value", "https://", "signed-secret", "private-untrusted-code"} {
					if strings.Contains(cause.Error(), value) {
						t.Fatal("publication error retained untrusted remote details")
					}
				}
			}
			if test.captureMissing && puts != 0 || !test.captureMissing && puts == 0 {
				t.Fatal("failure was not observed at the intended HTTP/capture boundary")
			}
		})
	}
}

func TestPublicationFailurePreservesParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, execution, _ := finetuningOutputWorkspace(t)
	_, err := workspace.UploadOutput(ctx, s3.New(s3.Options{}), execution, t.TempDir(), biz.CollectedOutput{})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("publication lost its parent cancellation")
	}
}

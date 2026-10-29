package objectstore_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
)

// These TLS handlers exercise the real SDK and HTTP body reader. They are
// isolated module fixtures, not evidence of access to the production S3 store.
func TestVerificationSourceUnavailableRemainsRetryableAndSanitized(t *testing.T) {
	valid := inputCSVFixture(t)
	for _, mode := range verificationModes() {
		t.Run(mode.name, func(t *testing.T) {
			for _, fixture := range []struct {
				name   string
				status int
				body   string
				length int
			}{
				{"unavailable", http.StatusServiceUnavailable, `<Error><Code>ServiceUnavailable</Code><Message>secret-token https://private.example/signed?credential=secret</Message></Error>`, 0},
				{"denied", http.StatusForbidden, `<Error><Code>AccessDenied</Code><Message>secret-token https://private.example/signed?credential=secret</Message></Error>`, 0},
				{"body disconnect before CSV header", http.StatusOK, "x0,", len(valid)},
				{"body disconnect inside CSV row", http.StatusOK, valid[:len(valid)-10], len(valid)},
				{"EOF probe disconnect", http.StatusOK, valid, len(valid) + 1},
				{"early malformed CSV followed by disconnect", http.StatusOK, "\r\n" + valid[:len(valid)-10], len(valid)},
			} {
				t.Run(fixture.name, func(t *testing.T) {
					scope, object := objectFixture(valid)
					var requests atomic.Int64
					server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						requests.Add(1)
						w.Header().Set("x-amz-version-id", "version-1")
						if fixture.length != 0 {
							w.Header().Set("Content-Length", strconv.Itoa(fixture.length))
						}
						w.WriteHeader(fixture.status)
						_, _ = io.WriteString(w, fixture.body)
					}))
					defer server.Close()
					verified, err := mode.verify(boundedVerificationContext(t), objectstore.NewVerifier(noRetryS3Client(server), scope.StorageConnectionID), scope, object)
					if !errors.Is(err, mode.unavailable) || errors.Is(err, mode.verification) || err.Error() != mode.unavailable.Error() || verified || requests.Load() != 1 {
						t.Fatalf("incomplete source observation classified incorrectly: error=%v verified=%t requests=%d", err, verified, requests.Load())
					}
				})
			}
		})
	}
}

func TestCompleteInvalidSourceRemainsContentRejected(t *testing.T) {
	valid := inputCSVFixture(t)
	for _, mode := range verificationModes() {
		t.Run(mode.name, func(t *testing.T) {
			for _, fixture := range []struct{ name, body, version string }{
				{"wrong digest", strings.Replace(valid, "0.25", "0.75", 1), "version-1"},
				{"complete shorter object", valid[:len(valid)-10], "version-1"},
				{"complete longer object", valid + "extra", "version-1"},
				{"different version", valid, "version-2"},
				{"missing version", valid, ""},
			} {
				t.Run(fixture.name, func(t *testing.T) {
					scope, object := objectFixture(valid)
					server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("x-amz-version-id", fixture.version)
						_, _ = io.WriteString(w, fixture.body)
					}))
					defer server.Close()
					verified, err := mode.verify(boundedVerificationContext(t), objectstore.NewVerifier(noRetryS3Client(server), scope.StorageConnectionID), scope, object)
					if !errors.Is(err, mode.verification) || errors.Is(err, mode.unavailable) || verified {
						t.Fatalf("complete bad object did not remain a content failure: error=%v verified=%t", err, verified)
					}
				})
			}
		})
	}
	// Fix the expected digest to the malformed bytes so CSV structure, rather
	// than a byte-identity mismatch, is the independent permanent failure.
	malformed := strings.Replace(valid, "x0,", "feature0,", 1)
	scope, object := objectFixture(malformed)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-amz-version-id", "version-1")
		_, _ = io.WriteString(w, malformed)
	}))
	defer server.Close()
	got, err := objectstore.NewVerifier(noRetryS3Client(server), scope.StorageConnectionID).VerifyCSV(boundedVerificationContext(t), scope, object)
	if !errors.Is(err, biz.ErrInputVerification) || errors.Is(err, biz.ErrInputSourceUnavailable) || !got.VerifiedAt.IsZero() {
		t.Fatalf("complete malformed CSV did not remain a content failure: %v", err)
	}
}

func TestVerificationOwnerOrScopeMisconfigurationNeverRejectsContentOrFetches(t *testing.T) {
	valid := inputCSVFixture(t)
	for _, mode := range verificationModes() {
		t.Run(mode.name, func(t *testing.T) {
			for _, fixture := range []struct {
				name   string
				mutate func(*cpup01.StorageScope, *string, *int64)
			}{
				{"unconfigured connection", func(_ *cpup01.StorageScope, connection *string, _ *int64) { *connection = "" }},
				{"different configured connection", func(_ *cpup01.StorageScope, connection *string, _ *int64) { *connection = "another-owner-store" }},
				{"different scope connection", func(scope *cpup01.StorageScope, _ *string, _ *int64) {
					scope.StorageConnectionID = "another-scope-store"
				}},
				{"different scope bucket", func(scope *cpup01.StorageScope, _ *string, _ *int64) { scope.Bucket = "another-bucket" }},
				{"different scope prefix", func(scope *cpup01.StorageScope, _ *string, _ *int64) { scope.ApprovedPrefix = "another-prefix" }},
				{"invalid scope prefix", func(scope *cpup01.StorageScope, _ *string, _ *int64) { scope.ApprovedPrefix = "../input" }},
			} {
				t.Run(fixture.name, func(t *testing.T) {
					scope, object := objectFixture(valid)
					connection, limit := scope.StorageConnectionID, int64(32*1024*1024)
					fixture.mutate(&scope, &connection, &limit)
					var requests atomic.Int64
					server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						requests.Add(1)
						w.WriteHeader(http.StatusForbidden)
					}))
					defer server.Close()
					verified, err := mode.verify(boundedVerificationContext(t), objectstore.NewVerifier(noRetryS3Client(server), connection), scope, object)
					if !errors.Is(err, mode.unavailable) || errors.Is(err, mode.verification) || verified || requests.Load() != 0 {
						t.Fatalf("configuration failure fetched or rejected source content: error=%v verified=%t requests=%d", err, verified, requests.Load())
					}
				})
			}
		})
	}
}

func TestVerificationCancellationPreservesContextCause(t *testing.T) {
	valid := inputCSVFixture(t)
	for _, mode := range verificationModes() {
		t.Run(mode.name, func(t *testing.T) {
			scope, object := objectFixture(valid)
			started := make(chan struct{})
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("x-amz-version-id", "version-1")
				_, _ = io.WriteString(w, "x0,")
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(boundedVerificationContext(t))
			defer cancel()
			type result struct {
				verified bool
				err      error
			}
			finished := make(chan result, 1)
			go func() {
				verified, err := mode.verify(ctx, objectstore.NewVerifier(noRetryS3Client(server), scope.StorageConnectionID), scope, object)
				finished <- result{verified, err}
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("source read did not start")
			}
			cancel()
			select {
			case got := <-finished:
				if !errors.Is(got.err, context.Canceled) || errors.Is(got.err, mode.unavailable) || errors.Is(got.err, mode.verification) || got.verified {
					t.Fatalf("cancellation changed into a stored validation observation: error=%v verified=%t", got.err, got.verified)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("source read did not stop after cancellation")
			}
		})
	}
}

type verificationMode struct {
	name         string
	unavailable  error
	verification error
	verify       func(context.Context, *objectstore.Verifier, cpup01.StorageScope, cpup01.FixedObjectRef) (bool, error)
}

func verificationModes() []verificationMode {
	return []verificationMode{
		{"object", biz.ErrObjectSourceUnavailable, biz.ErrObjectVerification, func(ctx context.Context, verifier *objectstore.Verifier, scope cpup01.StorageScope, object cpup01.FixedObjectRef) (bool, error) {
			got, err := verifier.Verify(ctx, scope, object)
			return !got.VerifiedAt.IsZero(), err
		}},
		{"CSV", biz.ErrInputSourceUnavailable, biz.ErrInputVerification, func(ctx context.Context, verifier *objectstore.Verifier, scope cpup01.StorageScope, object cpup01.FixedObjectRef) (bool, error) {
			got, err := verifier.VerifyCSV(ctx, scope, object)
			return !got.VerifiedAt.IsZero(), err
		}},
	}
}

func noRetryS3Client(server *httptest.Server) *s3.Client {
	return s3.New(s3.Options{Region: "test-region", BaseEndpoint: aws.String(server.URL), UsePathStyle: true, HTTPClient: server.Client(), Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1})
}

func boundedVerificationContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

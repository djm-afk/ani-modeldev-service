package objectstore_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestVerifyObjectReadsActualVersionedBytesNotETag(t *testing.T) {
	payload := "real remote test bytes"
	scope, object := objectFixture(payload)
	var gets atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/cpu-artifacts/tenant/execution/model.pt" || r.URL.Query().Get("versionId") != "version-1" {
			t.Errorf("unexpected object request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		gets.Add(1)
		w.Header().Set("x-amz-version-id", "version-1")
		w.Header().Set("ETag", `"not-a-sha256-5"`)
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()
	client := testS3Client(server)
	got, err := objectstore.NewVerifier(client, scope.StorageConnectionID).Verify(context.Background(), scope, object)
	if err != nil {
		t.Fatalf("verify actual object bytes: %v", err)
	}
	if gets.Load() != 1 || got.Object.SHA256 != object.SHA256 || got.Object.SizeBytes != object.SizeBytes || got.Object.VersionID == nil || *got.Object.VersionID != "version-1" || got.VerifiedAt.IsZero() {
		t.Fatalf("verification lost actual object identity: %+v", got)
	}
}

func TestVerifyObjectRejectsWrongBytesAndVersion(t *testing.T) {
	for _, test := range []struct{ name, body, version string }{
		{"same length wrong content", "bad!", "version-1"},
		{"truncated", "goo", "version-1"},
		{"extra bytes", "good!", "version-1"},
		{"different version", "good", "version-2"},
		{"unconfirmed version", "good", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			scope, object := objectFixture("good")
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("x-amz-version-id", test.version)
				w.Header().Set("ETag", object.SHA256)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			got, err := objectstore.NewVerifier(testS3Client(server), scope.StorageConnectionID).Verify(context.Background(), scope, object)
			if !errors.Is(err, biz.ErrObjectVerification) || !got.VerifiedAt.IsZero() {
				t.Fatal("invalid object produced verification observation")
			}
		})
	}
}

func TestVerifyObjectRejectsLeadingParentScopeBeforeNetwork(t *testing.T) {
	for _, prefix := range []string{"..", "../input"} {
		t.Run(prefix, func(t *testing.T) {
			scope, object := objectFixture("good")
			scope.ApprovedPrefix = prefix
			object.Key = prefix + "/data.csv"
			var requests atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("x-amz-version-id", *object.VersionID)
				_, _ = io.WriteString(w, "good")
			}))
			defer server.Close()
			got, err := objectstore.NewVerifier(testS3Client(server), scope.StorageConnectionID).Verify(context.Background(), scope, object)
			if !errors.Is(err, biz.ErrObjectSourceUnavailable) || errors.Is(err, biz.ErrObjectVerification) || !got.VerifiedAt.IsZero() || requests.Load() != 0 {
				t.Fatal("parent-relative scope reached the object store or produced verification")
			}
		})
	}
}

func TestVerifyObjectRejectsUnapprovedReferencesBeforeNetwork(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*cpup01.FixedObjectRef)
		want   error
	}{
		{"wrong connection", func(o *cpup01.FixedObjectRef) { o.StorageConnectionID = "another-store" }, biz.ErrObjectSourceUnavailable},
		{"wrong bucket", func(o *cpup01.FixedObjectRef) { o.Bucket = "another-bucket" }, biz.ErrObjectSourceUnavailable},
		{"prefix sibling", func(o *cpup01.FixedObjectRef) { o.Key = "tenant/execution-other/model.pt" }, biz.ErrObjectSourceUnavailable},
		{"traversal", func(o *cpup01.FixedObjectRef) { o.Key = "tenant/execution/../model.pt" }, biz.ErrObjectSourceUnavailable},
		{"no fixed version", func(o *cpup01.FixedObjectRef) { o.VersionID = nil }, biz.ErrObjectVerification},
		{"null version", func(o *cpup01.FixedObjectRef) { o.VersionID = aws.String("null") }, biz.ErrObjectVerification},
		{"immutable-copy assertion", func(o *cpup01.FixedObjectRef) { o.VersionID = nil; o.ImmutableCopy = aws.Bool(true) }, biz.ErrObjectVerification},
		{"uppercase sha", func(o *cpup01.FixedObjectRef) { o.SHA256 = strings.Repeat("A", 64) }, biz.ErrObjectVerification},
	} {
		t.Run(test.name, func(t *testing.T) {
			scope, object := objectFixture("good")
			test.mutate(&object)
			var requests atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(http.StatusForbidden) }))
			defer server.Close()
			got, err := objectstore.NewVerifier(testS3Client(server), scope.StorageConnectionID).Verify(context.Background(), scope, object)
			rejectedAsContent := errors.Is(err, biz.ErrObjectVerification)
			sourceUnavailable := errors.Is(err, biz.ErrObjectSourceUnavailable)
			if !errors.Is(err, test.want) || rejectedAsContent != (test.want == biz.ErrObjectVerification) || sourceUnavailable != (test.want == biz.ErrObjectSourceUnavailable) || !got.VerifiedAt.IsZero() || requests.Load() != 0 {
				t.Fatalf("unapproved reference returned the wrong classification or reached object store: error=%v verified=%t requests=%d", err, !got.VerifiedAt.IsZero(), requests.Load())
			}
		})
	}
}

func TestVerifyRejectsNegativeFrozenObjectSizeBeforeNetwork(t *testing.T) {
	scope, object := objectFixture("good")
	object.SizeBytes = -1
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(http.StatusForbidden) }))
	defer server.Close()
	got, err := objectstore.NewVerifier(testS3Client(server), scope.StorageConnectionID).Verify(context.Background(), scope, object)
	if !errors.Is(err, biz.ErrObjectVerification) || !got.VerifiedAt.IsZero() || requests.Load() != 0 {
		t.Fatal("negative frozen size fetched remote content")
	}
}

func TestVerifyCancelsAnIncompleteRemoteBodyWithoutProducingReceipt(t *testing.T) {
	scope, object := objectFixture("good")
	started := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-amz-version-id", "version-1")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		observed biz.VerifiedObject
		err      error
	}
	finished := make(chan result, 1)
	go func() {
		observed, err := objectstore.NewVerifier(testS3Client(server), scope.StorageConnectionID).Verify(ctx, scope, object)
		finished <- result{observed, err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("remote read did not start")
	}
	cancel()
	select {
	case got := <-finished:
		if !errors.Is(got.err, context.Canceled) || !got.observed.VerifiedAt.IsZero() {
			t.Fatal("cancellation produced a receipt or lost its cause")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("remote body did not stop after cancellation")
	}
}

func testS3Client(server *httptest.Server) *s3.Client {
	// Anonymous credentials and this server CA are test-only. Production gets its
	// authenticated client exclusively from the trusted composition root.
	return s3.New(s3.Options{Region: "test-region", BaseEndpoint: aws.String(server.URL), UsePathStyle: true, HTTPClient: server.Client(), Credentials: aws.AnonymousCredentials{}})
}

func objectFixture(payload string) (cpup01.StorageScope, cpup01.FixedObjectRef) {
	digest := sha256.Sum256([]byte(payload))
	scope := cpup01.StorageScope{StorageConnectionID: "artifact-store-v1", Bucket: "cpu-artifacts", ApprovedPrefix: "tenant/execution", CredentialReference: "owner-controlled"}
	object := cpup01.FixedObjectRef{StorageConnectionID: scope.StorageConnectionID, Bucket: scope.Bucket, Key: scope.ApprovedPrefix + "/model.pt", VersionID: aws.String("version-1"), SizeBytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}
	return scope, object
}

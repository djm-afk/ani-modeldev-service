package input_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestManagedInputImportFreezesBeforeReadingBytesAndCommitsReady(t *testing.T) {
	openPool := postgres.Prepare(t)
	repository, independentReader := input.New(openPool()), input.New(openPool())
	request, payload := managedImportFixture()
	var reads atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/"+request.Object.Bucket+"/"+request.Object.Key || r.URL.Query().Get("versionId") != *request.Object.VersionID {
			t.Error("import did not read the frozen object version")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		frozen, err := independentReader.Get(r.Context(), request.TenantID, request.InputVersionID)
		if err != nil || frozen.State != biz.InputStateValidating || frozen.Verification != nil || !reflect.DeepEqual(frozen.Import, request) {
			t.Error("object read occurred before the original request was committed")
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.Header().Set("x-amz-version-id", *request.Object.VersionID)
		w.Header().Set("ETag", `"metadata-is-not-byte-verification"`)
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()
	verifier := managedImportVerifier(server, request.Scope.StorageConnectionID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready, err := managedTestImporter(repository, verifier).ImportCSV(ctx, request)
	if err != nil {
		t.Fatalf("import using real PostgreSQL and the S3 SDK boundary: %v", err)
	}
	if reads.Load() != 1 || ready.State != biz.InputStateReady || !reflect.DeepEqual(ready.Import, request) || ready.Verification == nil || ready.Verification.ValidateFor(request) != nil {
		t.Fatalf("input became ready without the expected durable byte proof: %+v", ready)
	}
	recovered, err := input.New(openPool()).Get(ctx, request.TenantID, request.InputVersionID)
	if err != nil || !reflect.DeepEqual(recovered, ready) {
		t.Fatalf("READY was returned before its observation became durable: %+v, %v", recovered, err)
	}
}

func TestManagedInputImportRejectsBadRemoteProofWithoutExposingReady(t *testing.T) {
	for _, name := range []string{"different bytes", "different version", "invalid CSV shape"} {
		t.Run(name, func(t *testing.T) {
			openPool := postgres.Prepare(t)
			request, payload := managedImportFixture()
			version := *request.Object.VersionID
			switch name {
			case "different bytes":
				payload = strings.Replace(payload, "0.25", "0.26", 1)
			case "different version":
				version = "unexpected-source-version"
			case "invalid CSV shape":
				payload = strings.Replace(payload, "x0,", "wrong_feature,", 1)
				request.Object.SizeBytes = int64(len(payload))
				hash := sha256.Sum256([]byte(payload))
				request.Object.SHA256 = hex.EncodeToString(hash[:])
			}
			var reads atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reads.Add(1)
				w.Header().Set("x-amz-version-id", version)
				_, _ = io.WriteString(w, payload)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			got, err := managedTestImporter(input.New(openPool()), managedImportVerifier(server, request.Scope.StorageConnectionID)).ImportCSV(ctx, request)
			if !errors.Is(err, biz.ErrInputVerification) || reads.Load() != 1 || got.State != biz.InputStateRejected || got.Verification != nil || got.Failure == nil || got.Failure.Code != biz.InputFailureContentRejected {
				t.Fatalf("invalid remote proof exposed a ready input: %+v, %v", got, err)
			}
			stored, err := input.New(openPool()).Get(ctx, request.TenantID, request.InputVersionID)
			if err != nil || !reflect.DeepEqual(stored, got) || !reflect.DeepEqual(stored.Import, request) {
				t.Fatalf("failed verification lost or promoted the frozen request: %+v, %v", stored, err)
			}
			replayed, err := managedTestImporter(input.New(openPool()), managedImportVerifier(server, request.Scope.StorageConnectionID)).ImportCSV(ctx, request)
			if !errors.Is(err, biz.ErrInputVerification) || !reflect.DeepEqual(replayed, stored) || reads.Load() != 1 {
				t.Fatalf("replaying rejected content fetched it again or lost the first failure: %+v, %v", replayed, err)
			}
		})
	}
}

func TestManagedInputImportReplayAndConflictDoNotReadAnotherObject(t *testing.T) {
	openPool := postgres.Prepare(t)
	request, payload := managedImportFixture()
	var reads atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		w.Header().Set("x-amz-version-id", *request.Object.VersionID)
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()
	verifier := managedImportVerifier(server, request.Scope.StorageConnectionID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	writer := openPool()
	first, err := managedTestImporter(input.New(writer), verifier).ImportCSV(ctx, request)
	if err != nil || first.State != biz.InputStateReady {
		t.Fatalf("initial verified import: %+v, %v", first, err)
	}
	writer.Close()
	reader := input.New(openPool())
	importer := managedTestImporter(reader, verifier)
	replayed, err := importer.ImportCSV(ctx, request)
	if err != nil || !reflect.DeepEqual(replayed, first) || reads.Load() != 1 {
		t.Fatalf("replay changed the fixed version, proof, or repeated verification: %+v, %v", replayed, err)
	}
	conflicting := request
	conflicting.Object.Key = "tenant/input/replacement.csv"
	got, err := importer.ImportCSV(ctx, conflicting)
	if !errors.Is(err, biz.ErrInputConflict) || !reflect.DeepEqual(got, biz.InputVersion{}) || reads.Load() != 1 {
		t.Fatalf("conflicting retry reached another object or overwrote the import: %+v, %v", got, err)
	}
	requireStoredInputVersion(t, ctx, reader, first)
}

func TestManagedInputImportRetriesSourceFailureAgainstTheSameFrozenObject(t *testing.T) {
	openPool := postgres.Prepare(t)
	request, payload := managedImportFixture()
	var reads atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := reads.Add(1)
		if r.URL.Path != "/"+request.Object.Bucket+"/"+request.Object.Key || r.URL.Query().Get("versionId") != *request.Object.VersionID {
			t.Error("retry changed the frozen source object")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if attempt == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `<Error><Code>ServiceUnavailable</Code><Message>private-endpoint secret-token</Message></Error>`)
			return
		}
		w.Header().Set("x-amz-version-id", *request.Object.VersionID)
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	writer := openPool()
	verifier := managedImportVerifier(server, request.Scope.StorageConnectionID)
	failed, err := managedTestImporter(input.New(writer), verifier).ImportCSV(ctx, request)
	if !errors.Is(err, biz.ErrInputSourceUnavailable) || err.Error() != "INPUT_SOURCE_UNAVAILABLE" || failed.State != biz.InputStateValidating || failed.Failure == nil || failed.Failure.Code != biz.InputFailureSourceUnavailable || failed.Verification != nil || reads.Load() != 1 {
		t.Fatalf("temporary source error rejected content or was not saved: %+v, %v", failed, err)
	}
	if failed.Failure.ValidateFor(request) != nil || !reflect.DeepEqual(failed.Import, request) {
		t.Fatal("source failure lost original request or finite observation")
	}
	writer.Close()
	reader := input.New(openPool())
	requireStoredInputVersion(t, ctx, reader, failed)
	ready, err := managedTestImporter(reader, verifier).ImportCSV(ctx, request)
	if err != nil || ready.State != biz.InputStateReady || ready.Failure != nil || ready.Verification == nil || !reflect.DeepEqual(ready.Import, request) || reads.Load() != 2 {
		t.Fatalf("retry did not verify the original fixed object: %+v, %v", ready, err)
	}
	requireStoredInputVersion(t, ctx, input.New(openPool()), ready)
}

func TestManagedInputImportCancellationRetainsFrozenRequestWithoutContentRejection(t *testing.T) {
	openPool := postgres.Prepare(t)
	request, _ := managedImportFixture()
	started := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-amz-version-id", *request.Object.VersionID)
		_, _ = io.WriteString(w, "x0,")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		version biz.InputVersion
		err     error
	}
	finished := make(chan result, 1)
	importer := managedTestImporter(input.New(openPool()), managedImportVerifier(server, request.Scope.StorageConnectionID))
	go func() { got, err := importer.ImportCSV(ctx, request); finished <- result{got, err} }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("source read did not begin")
	}
	cancel()
	select {
	case got := <-finished:
		if !errors.Is(got.err, context.Canceled) || got.version.State != biz.InputStateValidating || got.version.Failure != nil || got.version.Verification != nil {
			t.Fatalf("canceled read fabricated a validation failure: %+v, %v", got.version, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled import did not finish")
	}
	readContext, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	requireStoredInputVersion(t, readContext, input.New(openPool()), biz.InputVersion{Import: request, State: biz.InputStateValidating})
}

// The HTTPS server supplies module fixture bytes. It is not a real managed S3
// deployment, administrator identity, product endpoint or target-cluster proof.
func managedImportFixture() (biz.InputImport, string) {
	var payload strings.Builder
	for column := 0; column < 16; column++ {
		fmt.Fprintf(&payload, "x%d,", column)
	}
	payload.WriteString("label\n")
	for sample := 0; sample < 1024; sample++ {
		payload.WriteString(strings.Repeat("0.25,", 16))
		fmt.Fprintf(&payload, "%d\n", sample%2)
	}
	request := importFixture()
	request.RequestedAt = time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	request.Object.SizeBytes = int64(payload.Len())
	hash := sha256.Sum256([]byte(payload.String()))
	request.Object.SHA256 = hex.EncodeToString(hash[:])
	return request, payload.String()
}

func managedImportVerifier(server *httptest.Server, connectionID string) *objectstore.Verifier {
	client := s3.New(s3.Options{
		Region: "fixture-region", BaseEndpoint: aws.String(server.URL), UsePathStyle: true,
		Credentials: aws.AnonymousCredentials{}, HTTPClient: server.Client(), RetryMaxAttempts: 1,
	})
	return objectstore.NewVerifier(client, connectionID)
}

func managedTestImporter(repository biz.ManagedInputRepository, verifier biz.CSVVerifier) *biz.InputImporter {
	return biz.NewInputImporter(repository, verifier, cpup01.ContentLimits{MaxContentBytes: 17179869184}, 1800*time.Second)
}

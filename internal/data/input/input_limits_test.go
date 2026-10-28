package input_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestInputPreparedFactsAndReplaySurviveContentLimitReduction(t *testing.T) {
	open := postgres.Prepare(t)
	request, contents := managedImportFixture()
	var reads atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		w.Header().Set("x-amz-version-id", *request.Object.VersionID)
		_, _ = io.WriteString(w, contents)
	}))
	defer server.Close()
	ctx := context.Background()
	verifier := managedImportVerifier(server, request.Scope.StorageConnectionID)
	accepted, err := biz.NewInputImporter(input.New(open()), verifier, cpup01.ContentLimits{MaxContentBytes: request.Object.SizeBytes}, time.Minute).ImportCSV(ctx, request)
	if err != nil || accepted.Verification == nil || accepted.Verification.PreparedDataset.ValidateForSource("CSV", request.Object.SizeBytes, request.Object.SHA256, 1024) != nil {
		t.Fatalf("real input did not persist the complete identity proof: %+v %v", accepted, err)
	}
	reader := input.New(open())
	stored, err := reader.Get(ctx, request.TenantID, request.InputVersionID)
	if err != nil || stored.Verification == nil || stored.Verification.PreparedDataset != accepted.Verification.PreparedDataset {
		t.Fatalf("new database connection lost derived facts: %+v %v", stored, err)
	}
	small := biz.NewInputImporter(reader, verifier, cpup01.ContentLimits{MaxContentBytes: 1}, time.Minute)
	replayed, err := small.ImportCSV(ctx, request)
	if err != nil || reads.Load() != 1 || replayed.Verification == nil || replayed.Verification.PreparedDataset != stored.Verification.PreparedDataset {
		t.Fatalf("a new policy rewrote or reread an accepted current input: %+v %v", replayed, err)
	}
	conflict := request
	conflict.InputVersionID = "66666666-6666-4666-8666-666666666666"
	if _, err := small.ImportCSV(ctx, conflict); !errors.Is(err, biz.ErrInputConflict) || reads.Load() != 1 {
		t.Fatalf("current L hid the existing request-id conflict: %v reads=%d", err, reads.Load())
	}
	changed := request
	changed.RequestID = "44444444-4444-4444-8444-444444444444"
	changed.InputVersionID = "55555555-5555-4555-8555-555555555555"
	if _, err := small.ImportCSV(ctx, changed); err == nil {
		t.Fatal("new import ignored the current content limit")
	} else {
		var violation *cpup01.LimitViolation
		if !errors.As(err, &violation) || violation.Reason != "CONTENT_LIMIT_EXCEEDED" || violation.Actual != request.Object.SizeBytes || violation.Limit != 1 {
			t.Fatalf("new import lost actual/limit content reason: %v", err)
		}
	}
	if _, err := reader.Get(ctx, changed.TenantID, changed.InputVersionID); !errors.Is(err, biz.ErrInputNotFound) || reads.Load() != 1 {
		t.Fatal("rejected first admission reserved an input or fetched source bytes", err)
	}
}

func TestInputValidationSessionLockIsExclusiveAndBusyCreatesNoReceipt(t *testing.T) {
	open := postgres.Prepare(t)
	request, contents := managedImportFixture()
	owner := input.New(open())
	release, err := owner.AcquireValidationLock(context.Background(), request.TenantID, request.InputVersionID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	var reads atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		w.Header().Set("x-amz-version-id", *request.Object.VersionID)
		_, _ = io.WriteString(w, contents)
	}))
	defer server.Close()
	repository := input.New(open())
	importer := managedTestImporter(repository, managedImportVerifier(server, request.Scope.StorageConnectionID))
	if _, err := importer.ImportCSV(context.Background(), request); !errors.Is(err, biz.ErrInputValidationBusy) || reads.Load() != 0 {
		t.Fatalf("session lock allowed a second validator: %v reads=%d", err, reads.Load())
	}
	if _, err := repository.Get(context.Background(), request.TenantID, request.InputVersionID); !errors.Is(err, biz.ErrInputNotFound) {
		t.Fatal("busy validation created a business state", err)
	}
	release()
	release = nil
	ready, err := importer.ImportCSV(context.Background(), request)
	if err != nil || ready.State != biz.InputStateReady || reads.Load() != 1 {
		t.Fatalf("released session lock did not permit the one real import: %+v %v", ready, err)
	}
}

func TestImportDeadlineLeavesOriginalReceiptRetryableUnderReducedLimit(t *testing.T) {
	open := postgres.Prepare(t)
	request, contents := managedImportFixture()
	var reads atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := reads.Add(1)
		w.Header().Set("x-amz-version-id", *request.Object.VersionID)
		if attempt == 1 {
			_, _ = io.WriteString(w, "x0,")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, contents)
	}))
	defer server.Close()
	repo := input.New(open())
	verifier := managedImportVerifier(server, request.Scope.StorageConnectionID)
	first := biz.NewInputImporter(repo, verifier, cpup01.ContentLimits{MaxContentBytes: request.Object.SizeBytes}, 500*time.Millisecond)
	value, err := first.ImportCSV(context.Background(), request)
	if !errors.Is(err, context.DeadlineExceeded) || value.State != biz.InputStateValidating || value.Failure != nil || value.Verification != nil {
		t.Fatalf("method deadline fabricated a final content verdict: %+v %v", value, err)
	}
	stored, err := input.New(open()).Get(context.Background(), request.TenantID, request.InputVersionID)
	if err != nil || stored.State != biz.InputStateValidating || stored.Failure != nil {
		t.Fatalf("deadline did not preserve the frozen receipt: %+v %v", stored, err)
	}
	retry := biz.NewInputImporter(input.New(open()), verifier, cpup01.ContentLimits{MaxContentBytes: 1}, time.Minute)
	ready, err := retry.ImportCSV(context.Background(), request)
	if err != nil || ready.State != biz.InputStateReady || reads.Load() != 2 || !reflect.DeepEqual(ready.Import.Object, value.Import.Object) {
		t.Fatalf("identical resumed import was blocked by new policy or retained capacity: %+v %v reads=%d", ready, err, reads.Load())
	}
}

func TestContentCapacityDoesNotBlockReadyReplayOrCreateBusyReceipt(t *testing.T) {
	open := postgres.Prepare(t)
	request, contents := managedImportFixture()
	blocked, proceed := make(chan struct{}), make(chan struct{})
	defer close(proceed)
	var reads atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		w.Header().Set("x-amz-version-id", *request.Object.VersionID)
		if strings.Contains(r.URL.Path, "holding.csv") {
			close(blocked)
			select {
			case <-proceed:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = io.WriteString(w, contents)
	}))
	defer server.Close()
	repo := input.New(open())
	importer := managedTestImporter(repo, managedImportVerifier(server, request.Scope.StorageConnectionID))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := importer.ImportCSV(ctx, request); err != nil {
		t.Fatal(err)
	}
	holding := request
	holding.RequestID = "88888888-8888-4888-8888-888888888888"
	holding.InputVersionID = holding.RequestID
	holding.Object.Key = request.Scope.ApprovedPrefix + "/holding.csv"
	finished := make(chan error, 1)
	go func() { _, err := importer.ImportCSV(ctx, holding); finished <- err }()
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("capacity owner did not reach real source read")
	}
	ready, err := importer.ImportCSV(ctx, request)
	if err != nil || ready.State != biz.InputStateReady || reads.Load() != 2 {
		t.Fatalf("READY replay took content capacity or reread source: %+v %v reads=%d", ready, err, reads.Load())
	}
	busy := request
	busy.RequestID = "99999999-9999-4999-8999-999999999999"
	busy.InputVersionID = busy.RequestID
	if _, err := importer.ImportCSV(ctx, busy); !errors.Is(err, biz.ErrInputValidationBusy) {
		t.Fatalf("second new content request was not busy: %v", err)
	}
	if _, err := repo.Get(ctx, busy.TenantID, busy.InputVersionID); !errors.Is(err, biz.ErrInputNotFound) || reads.Load() != 2 {
		t.Fatalf("busy content request wrote a receipt or fetched source: %v reads=%d", err, reads.Load())
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("capacity owner did not cancel cleanly: %v", err)
	}
}

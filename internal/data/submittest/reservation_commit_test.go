package submittest_test

import (
	"context"
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestSubmitReservationCommitFailureCannotSendOrReturnReceipt(t *testing.T) {
	openPool := postgres.Prepare(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	request := dispatchRequest(t)
	request.Admission = admissionfixture.EnsureEnvironment(t, openPool(), request.Admission)
	request.Owner = request.Admission.Snapshot.DispatchOwner
	fixturePool := openPool()
	if _, err := execution.New(fixturePool).Accept(ctx, request.Admission); err != nil {
		t.Fatalf("CPU07_COMMIT_PREFLIGHT: real Admission unavailable; behavior NOT_RUN: %v", err)
	}
	trace := &dispatchCommitTrace{}
	config := fixturePool.Config()
	config.ConnConfig.Tracer = trace
	writerPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("CPU07_COMMIT_PREFLIGHT: traced writer pool unavailable; behavior NOT_RUN")
	}
	t.Cleanup(writerPool.Close)
	if err := writerPool.Ping(ctx); err != nil {
		t.Fatal("CPU07_COMMIT_PREFLIGHT: traced writer connection unavailable; behavior NOT_RUN")
	}
	removeCommitFailure := postgres.RejectDispatchCommit(t, writerPool)
	var posts, tokens atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/apis/v2beta1/runs" {
			t.Error("unexpected KFP operation")
		}
		writeConfirmedSubmitResponse(t, w, request)
	}))
	t.Cleanup(server.Close)
	provider := tokenProviderFunc(func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
		tokens.Add(1)
		return "synthetic-submit-token", nil
	})
	failed, err := newSubmitter(t, submission.New(writerPool), server, provider).Submit(ctx, request)
	if !trace.failed.Load() {
		t.Fatal("CPU07_COMMIT_PREFLIGHT: exact deferred COMMIT/23514/marker not observed; commit-boundary behavior NOT_RUN")
	}
	if !errors.Is(err, biz.ErrPersistence) || !reflect.DeepEqual(failed, biz.PipelineSubmitResult{}) || posts.Load() != 0 || tokens.Load() != 0 {
		t.Fatalf("failed reservation COMMIT authorized a provider/POST/observation/receipt: %v", err)
	}
	writerPool.Close()
	fixturePool.Close()
	readerPool := openPool()
	reader := submission.New(readerPool)
	absent, err := reader.Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if !errors.Is(err, biz.ErrExecutionNotFound) || !reflect.DeepEqual(absent, biz.PipelineDispatch{}) {
		t.Fatalf("failed reservation left a durable dispatch after reconnect: %v", err)
	}
	admitted, err := execution.New(readerPool).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || !reflect.DeepEqual(admitted.Admission, request.Admission) || admitted.Close != nil {
		t.Fatal("failed dispatch commit changed original Admission")
	}
	t.Log("CPU07_DISPATCH_COMMIT_FAULT PASS: real COMMIT reached isolated 23514 marker; no provider/POST/receipt or durable dispatch")

	removeCommitFailure()
	first, err := newSubmitter(t, reader, server, provider).Submit(ctx, request)
	if err != nil || first.Observation == nil || first.Observation.State != biz.PipelineSubmissionConfirmed || first.Observation.RunID != "55555555-6666-4777-8888-999999999999" || first.Dispatch.State != biz.PipelineDispatchConfirmed || len(first.Dispatch.ConfirmedRuns) != 1 || first.Dispatch.ConfirmedRuns[0].RunID != first.Observation.RunID || posts.Load() != 1 || tokens.Load() != 1 {
		t.Fatalf("first valid reservation after fault removal did not send and persist once: %v", err)
	}
	assertOriginalDispatch(t, first.Dispatch, request)
	readerPool.Close()
	server.CloseClientConnections()
	reconnectedPool := openPool()
	reconnected := submission.New(reconnectedPool)
	stored, err := reconnected.Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || !reflect.DeepEqual(stored, first.Dispatch) {
		t.Fatalf("post-fault valid receipt was not durable: %v", err)
	}
	replayed, err := newSubmitter(t, reconnected, server, provider).Submit(ctx, request)
	if err != nil || replayed.Observation != nil || !reflect.DeepEqual(replayed.Dispatch, stored) || posts.Load() != 1 || tokens.Load() != 1 {
		t.Fatalf("reconnect after valid reservation repeated a send or changed facts: %v", err)
	}
	admitted, err = execution.New(reconnectedPool).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || !reflect.DeepEqual(admitted.Admission, request.Admission) || admitted.Close != nil {
		t.Fatal("valid retry or replay changed original Admission")
	}
}

// The trace proves the fault was raised by the real deferred constraint at
// COMMIT. It retains only a boolean, never query arguments or raw driver errors.
type dispatchCommitTrace struct{ failed atomic.Bool }
type dispatchCommitTraceKey struct{}

func (*dispatchCommitTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, dispatchCommitTraceKey{}, strings.EqualFold(strings.TrimSpace(data.SQL), "commit"))
}

func (trace *dispatchCommitTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	commit, _ := ctx.Value(dispatchCommitTraceKey{}).(bool)
	var databaseError *pgconn.PgError
	if commit && errors.As(data.Err, &databaseError) && databaseError.Code == "23514" && databaseError.Message == "injected_dispatch_commit_failure" {
		trace.failed.Store(true)
	}
}

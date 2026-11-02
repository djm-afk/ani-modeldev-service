package submission_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestConfirmedRunCommitFailureReturnsNoReceiptOrPartialFacts(t *testing.T) {
	openPool := postgres.Prepare(t)
	fixturePool := openPool()
	request := validDispatchRequest(t, openPool())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	first := reserveUncertaintyAttempt(t, ctx, fixturePool, request)
	uncertainAt := first.Dispatch.ReservedAt.Add(time.Microsecond)
	original := markOriginalUncertain(t, ctx, submission.New(fixturePool), first, uncertainAt)
	closed, err := execution.New(fixturePool).ApplyCloseIntent(ctx, dispatchCloseIntent(request))
	if err != nil {
		t.Fatalf("real close fixture failed; confirmed commit behavior NOT_RUN: %v", err)
	}
	original.OwnerRevision = 4 // Admission, reservation, uncertainty, close.

	trace := &confirmedCommitTrace{}
	config := fixturePool.Config()
	config.ConnConfig.Tracer = trace
	writerPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("CPU07_DB_PREFLIGHT: observed writer pool unavailable; behavior NOT_RUN")
	}
	t.Cleanup(writerPool.Close)
	if err := writerPool.Ping(ctx); err != nil {
		t.Fatal("CPU07_DB_PREFLIGHT: observed writer connection unavailable; behavior NOT_RUN")
	}
	removeCommitFailure := postgres.RejectConfirmedRunCommit(t, writerPool)
	observedAt := uncertainAt.Add(time.Microsecond)
	observation := confirmedObservation(confirmedRunID)
	failed, err := submission.New(writerPool).RecordSubmissionConfirmed(ctx, *first.SendPermit, observation, observedAt)
	assertConfirmationRejected(t, failed, err, biz.ErrPersistence)
	if !trace.failed.Load() {
		t.Fatal("CPU07_DB_PREFLIGHT: the exact deferred COMMIT failure was not observed; commit-boundary behavior NOT_RUN")
	}
	t.Log("CPU07_COMMIT_FAULT: real COMMIT reached the isolated deferred confirmed-run constraint")
	writerPool.Close()

	reconnected := submission.New(openPool())
	assertDispatchReplay(t, ctx, reconnected, request, original)
	removeCommitFailure()
	receipt, err := reconnected.RecordSubmissionConfirmed(ctx, *first.SendPermit, observation, observedAt)
	want := expectedConfirmation(original, 5, biz.PipelineConfirmedRun{RunID: confirmedRunID, FirstObservedAt: observedAt})
	if err != nil || receipt.ConflictingRuns || !reflect.DeepEqual(receipt.Dispatch, want) {
		t.Fatalf("original observation did not commit after the isolated failure was removed: %v", err)
	}
	assertDispatchReplay(t, ctx, submission.New(openPool()), request, want)
	replayed, err := submission.New(openPool()).RecordSubmissionConfirmed(ctx, *first.SendPermit, observation, observedAt.Add(time.Microsecond))
	if err != nil || replayed.ConflictingRuns || !reflect.DeepEqual(replayed.Dispatch, want) {
		t.Fatalf("successful observation replay changed the first committed facts: %v", err)
	}
	admitted, err := execution.New(openPool()).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || admitted.Close == nil || !reflect.DeepEqual(*admitted.Close, closed.CloseRecord) {
		t.Fatalf("failed or retried confirmation changed the committed close: %v", err)
	}
	assertSameDispatchAdmission(t, admitted.Admission, request.Admission)
}

// Trace only the database fault marker at COMMIT; never collect query arguments
// or raw driver errors. Business assertions above use repository interfaces.
type confirmedCommitTrace struct {
	failed atomic.Bool
}

type confirmedCommitTraceKey struct{}

func (*confirmedCommitTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, confirmedCommitTraceKey{}, strings.EqualFold(strings.TrimSpace(data.SQL), "commit"))
}

func (trace *confirmedCommitTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	commit, _ := ctx.Value(confirmedCommitTraceKey{}).(bool)
	var databaseError *pgconn.PgError
	if commit && errors.As(data.Err, &databaseError) && databaseError.Code == "23514" && databaseError.Message == "injected_confirmed_run_commit_failure" {
		trace.failed.Store(true)
	}
}

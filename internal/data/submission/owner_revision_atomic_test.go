package submission_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
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

func TestOwnerRevisionSerializesDuplicateAndMixedWriters(t *testing.T) {
	openPool := postgres.Prepare(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	request := validDispatchRequest(t)
	request.Admission.TenantID = "aaaaaaaa-2222-4333-8444-555555555555"
	refreshDispatchHashes(t, &request.Admission)
	alias := request
	alias.Admission.TenantID = strings.ToUpper(alias.Admission.TenantID)
	alias.Admission.ExecutionID = strings.ToUpper(alias.Admission.ExecutionID)
	alias.Admission.OperationID = strings.ToUpper(alias.Admission.OperationID)
	pools := []*pgxpool.Pool{openPool(), openPool(), openPool(), openPool()}
	requests := []biz.PipelineDispatchRequest{request, alias, request, alias}
	accepted := make([]biz.AcceptReceipt, len(pools))
	writers := make([]func() error, len(pools))
	for i, pool := range pools {
		writers[i] = func() error {
			var err error
			accepted[i], err = execution.New(pool).Accept(ctx, requests[i].Admission)
			return err
		}
	}
	runRevisionWriters(t, ctx, writers...)
	firstAccepts := 0
	for _, receipt := range accepted {
		assertOwnerRevision(t, "concurrent same Admission", receipt.OwnerRevision, 1)
		assertSameDispatchAdmission(t, receipt.Admission, request.Admission)
		if receipt.Close != nil {
			t.Fatal("duplicate Admission invented a close")
		}
		if !receipt.Replayed {
			firstAccepts++
		}
	}
	if firstAccepts != 1 {
		t.Fatalf("new Admission receipts = %d, want 1", firstAccepts)
	}

	reservations := make([]biz.PipelineDispatchReservation, len(pools))
	for i, pool := range pools {
		writers[i] = func() error {
			var err error
			reservations[i], err = submission.New(pool).Reserve(ctx, requests[i])
			return err
		}
	}
	runRevisionWriters(t, ctx, writers...)
	var permit biz.PipelineSendPermit
	permits := 0
	first := reservations[0].Dispatch
	for _, receipt := range reservations {
		assertOwnerRevision(t, "concurrent same reservation", receipt.Dispatch.OwnerRevision, 2)
		if !reflect.DeepEqual(receipt.Dispatch, first) {
			t.Fatal("duplicate reservation changed the original attempt, plan or time")
		}
		if receipt.SendPermit != nil {
			permits++
			permit = *receipt.SendPermit
		}
	}
	if permits != 1 || permit.AttemptID != first.AttemptID || permit.PlanHash != first.PlanHash || permit.TenantID != request.Admission.TenantID || permit.ExecutionID != request.Admission.ExecutionID {
		t.Fatal("duplicate reservation must issue one original, correctly scoped permit")
	}

	runID := "abcdefab-cdef-4abc-8def-abcdefabcdef"
	runAt := first.ReservedAt.Add(time.Microsecond)
	confirmed := make([]biz.PipelineConfirmationReceipt, len(pools))
	for i, pool := range pools {
		writers[i] = func() error {
			originalPermit, observedRun := permit, runID
			if i%2 == 1 {
				originalPermit.TenantID = strings.ToUpper(originalPermit.TenantID)
				originalPermit.ExecutionID = strings.ToUpper(originalPermit.ExecutionID)
				originalPermit.AttemptID = strings.ToUpper(originalPermit.AttemptID)
				observedRun = strings.ToUpper(observedRun)
			}
			var err error
			confirmed[i], err = submission.New(pool).RecordSubmissionConfirmed(ctx, originalPermit, confirmedObservation(observedRun), runAt)
			return err
		}
	}
	runRevisionWriters(t, ctx, writers...)
	want := expectedConfirmation(first, 3, biz.PipelineConfirmedRun{RunID: runID, FirstObservedAt: runAt})
	for _, receipt := range confirmed {
		if receipt.ConflictingRuns || !reflect.DeepEqual(receipt.Dispatch, want) {
			t.Fatal("same Run aliases must add one fact at revision 3 and retain canonical identity")
		}
	}

	// Four distinct facts contend on the same owner identity. Their receipt
	// versions must form one serial order, independently of completion order.
	closeInputs := []biz.CloseIntent{dispatchCloseIntent(request), dispatchCloseIntent(request)}
	closeInputs[1].SourceGeneration = 2
	closeInputs[1].RequestedAt = closeInputs[0].RequestedAt.Add(time.Microsecond)
	closes := make([]biz.CloseReceipt, 2)
	runs := []biz.PipelineConfirmedRun{
		{RunID: "bcdefabc-defa-4bcd-8efa-bcdefabcdefa", FirstObservedAt: runAt.Add(time.Microsecond)},
		{RunID: "cdefabcd-efab-4cde-8fab-cdefabcdefab", FirstObservedAt: runAt.Add(2 * time.Microsecond)},
	}
	newRuns := make([]biz.PipelineConfirmationReceipt, 2)
	for i := range 2 {
		writers[i] = func() error {
			var err error
			closes[i], err = execution.New(pools[i]).ApplyCloseIntent(ctx, closeInputs[i])
			return err
		}
		writers[i+2] = func() error {
			var err error
			newRuns[i], err = submission.New(pools[i+2]).RecordSubmissionConfirmed(ctx, permit, confirmedObservation(runs[i].RunID), runs[i].FirstObservedAt)
			return err
		}
	}
	runRevisionWriters(t, ctx, writers...)
	seen := make(map[uint64]bool)
	for _, revision := range []uint64{closes[0].OwnerRevision, closes[1].OwnerRevision, newRuns[0].Dispatch.OwnerRevision, newRuns[1].Dispatch.OwnerRevision} {
		if revision < 4 || revision > 7 || seen[revision] {
			t.Fatalf("four new facts require distinct revisions 4 through 7, got duplicate/out-of-range %d", revision)
		}
		seen[revision] = true
	}
	var latestClose biz.CloseRecord
	for i, receipt := range closes {
		if receipt.Replayed || receipt.State != biz.CloseStateClosing || !reflect.DeepEqual(receipt.CloseIntent, closeInputs[i]) ||
			(receipt.Generation != 1 && receipt.Generation != 2) || closes[0].Generation == closes[1].Generation {
			t.Fatal("mixed writers lost an original close or confused source and owner generations")
		}
		if receipt.Generation == 2 {
			latestClose = receipt.CloseRecord
		}
	}
	for i, receipt := range newRuns {
		if !receipt.ConflictingRuns || receipt.Dispatch.State != biz.PipelineDispatchConfirmed {
			t.Fatal("mixed writer failed to retain its distinct Run as a conflict")
		}
		found := false
		for _, run := range receipt.Dispatch.ConfirmedRuns {
			if reflect.DeepEqual(run, runs[i]) {
				found = true
			}
		}
		if !found {
			t.Fatal("new Run receipt did not retain its own exact observation")
		}
	}
	want.OwnerRevision = 7
	want.ConfirmedRuns = append(want.ConfirmedRuns, runs...)
	assertDispatchReplay(t, ctx, submission.New(openPool()), request, want)
	reopened := execution.New(openPool())
	stored, err := reopened.Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || stored.OwnerRevision != 7 || !reflect.DeepEqual(stored.Close, &latestClose) {
		t.Fatalf("mixed writers lost the latest close or aggregate version: %v", err)
	}
	assertSameDispatchAdmission(t, stored.Admission, request.Admission)
	for _, original := range closes {
		replayed, err := reopened.ApplyCloseIntent(ctx, original.CloseIntent)
		if err != nil || !replayed.Replayed || replayed.OwnerRevision != 7 || !reflect.DeepEqual(replayed.CloseRecord, original.CloseRecord) {
			t.Fatalf("mixed close replay changed facts or consumed a revision: %v", err)
		}
	}
}

// Each wave starts together and is fully joined before assertions or the next
// wave. Only public repository calls run in the goroutines.
func runRevisionWriters(t *testing.T, ctx context.Context, writers ...func() error) {
	t.Helper()
	start := make(chan struct{})
	results := make(chan error, len(writers))
	for _, write := range writers {
		go func() {
			<-start
			results <- write()
		}()
	}
	close(start)
	var firstError error
	for range writers {
		select {
		case err := <-results:
			if err != nil && firstError == nil {
				firstError = err
			}
		case <-ctx.Done():
			t.Fatal("CPU04_REVISION_FACTS: concurrent writers exceeded their bounded context")
		}
	}
	if firstError != nil {
		t.Fatalf("CPU04_REVISION_FACTS: concurrent writer failed: %v", firstError)
	}
}

func TestOwnerRevisionRollsBackWithDeferredCommitFailures(t *testing.T) {
	t.Run("Admission preserves the prior close tombstone", func(t *testing.T) {
		openPool := postgres.Prepare(t)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		request := validDispatchRequest(t)
		fixture := openPool()
		trace := &revisionCommitTrace{}
		writer := revisionTracePool(t, ctx, fixture, trace)
		executions := execution.New(writer)
		stop := dispatchCloseIntent(request)
		closed, err := executions.ApplyCloseIntent(ctx, stop)
		if err != nil || closed.OwnerRevision != 1 || closed.Replayed {
			t.Fatalf("CPU04_REVISION_PREFLIGHT: initial tombstone failed: %v", err)
		}
		remove := postgres.RejectAdmissionCommit(t, writer)
		failed, err := executions.Accept(ctx, request.Admission)
		if !errors.Is(err, biz.ErrPersistence) || !reflect.DeepEqual(failed, biz.AcceptReceipt{}) {
			t.Fatal("failed Admission COMMIT must return a zero receipt")
		}
		trace.require(t, "injected_admission_commit_failure")
		reader := execution.New(openPool())
		absent, err := reader.Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
		if !errors.Is(err, biz.ErrExecutionNotFound) || !reflect.DeepEqual(absent, biz.Execution{}) {
			t.Fatal("failed Admission left a visible execution")
		}
		replayedClose, err := reader.ApplyCloseIntent(ctx, stop)
		if err != nil || !replayedClose.Replayed || replayedClose.OwnerRevision != 1 || !reflect.DeepEqual(replayedClose.CloseRecord, closed.CloseRecord) {
			t.Fatalf("failed Admission changed the prior close or its revision: %v", err)
		}
		remove()
		accepted, err := reader.Accept(ctx, request.Admission)
		if err != nil || accepted.Replayed || accepted.OwnerRevision != 2 || !reflect.DeepEqual(accepted.Close, &closed.CloseRecord) {
			t.Fatalf("recovered Admission must add exactly one fact: %v", err)
		}
		assertSameDispatchAdmission(t, accepted.Admission, request.Admission)
		replay, err := reader.Accept(ctx, request.Admission)
		if err != nil || !replay.Replayed || !reflect.DeepEqual(replay.Execution, accepted.Execution) {
			t.Fatalf("recovered Admission replay changed the revision or original facts: %v", err)
		}
	})

	t.Run("reservation Run and close preserve their prior aggregate", func(t *testing.T) {
		openPool := postgres.Prepare(t)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		request := validDispatchRequest(t)
		fixture := openPool()
		trace := &revisionCommitTrace{}
		writer := revisionTracePool(t, ctx, fixture, trace)
		executions, dispatches := execution.New(writer), submission.New(writer)
		accepted, err := executions.Accept(ctx, request.Admission)
		if err != nil || accepted.OwnerRevision != 1 || accepted.Replayed || accepted.Close != nil {
			t.Fatalf("CPU04_REVISION_PREFLIGHT: initial Admission failed: %v", err)
		}
		remove := postgres.RejectDispatchCommit(t, writer)
		failedReservation, err := dispatches.Reserve(ctx, request)
		assertRejectedReservation(t, failedReservation, err, biz.ErrPersistence)
		trace.require(t, "injected_dispatch_commit_failure")
		assertNoDispatch(t, ctx, submission.New(openPool()), request.Admission.TenantID, request.Admission.ExecutionID)
		original, err := execution.New(openPool()).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
		if err != nil || !reflect.DeepEqual(original, accepted.Execution) {
			t.Fatalf("failed reservation changed Admission or consumed revision 2: %v", err)
		}
		remove()
		reserved, err := dispatches.Reserve(ctx, request)
		if err != nil || reserved.SendPermit == nil || reserved.Dispatch.OwnerRevision != 2 {
			t.Fatalf("recovered reservation must add one fact and issue its only permit: %v", err)
		}
		assertDispatchReplay(t, ctx, submission.New(openPool()), request, reserved.Dispatch)
		original.OwnerRevision = 2
		original.States = biz.ExecutionStates{
			Compute: biz.ComputeStateSubmitting, Delivery: biz.DeliveryStatePending,
			Resource: biz.ResourceStateNotApplicable, Close: biz.CloseStateOpen,
		}

		remove = postgres.RejectConfirmedRunCommit(t, writer)
		at := reserved.Dispatch.ReservedAt.Add(time.Microsecond)
		observation := confirmedObservation(confirmedRunID)
		failedRun, err := dispatches.RecordSubmissionConfirmed(ctx, *reserved.SendPermit, observation, at)
		assertConfirmationRejected(t, failedRun, err, biz.ErrPersistence)
		trace.require(t, "injected_confirmed_run_commit_failure")
		assertDispatchReplay(t, ctx, submission.New(openPool()), request, reserved.Dispatch)
		stored, err := execution.New(openPool()).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
		if err != nil || !reflect.DeepEqual(stored, original) {
			t.Fatalf("failed Run changed Admission or consumed revision 3: %v", err)
		}
		remove()
		confirmed, err := dispatches.RecordSubmissionConfirmed(ctx, *reserved.SendPermit, observation, at)
		wantDispatch := expectedConfirmation(reserved.Dispatch, 3, biz.PipelineConfirmedRun{RunID: confirmedRunID, FirstObservedAt: at})
		if err != nil || confirmed.ConflictingRuns || !reflect.DeepEqual(confirmed.Dispatch, wantDispatch) {
			t.Fatalf("recovered Run and parent state must add one aggregate fact: %v", err)
		}
		replayedRun, err := dispatches.RecordSubmissionConfirmed(ctx, *reserved.SendPermit, observation, at.Add(time.Microsecond))
		if err != nil || replayedRun.ConflictingRuns || !reflect.DeepEqual(replayedRun.Dispatch, wantDispatch) {
			t.Fatalf("recovered Run replay changed facts or revision: %v", err)
		}
		original.OwnerRevision = 3
		original.States = biz.ExecutionStates{
			Compute: biz.ComputeStateSubmissionConfirmed, Delivery: biz.DeliveryStatePending,
			Resource: biz.ResourceStateNotApplicable, Close: biz.CloseStateOpen,
		}

		remove = postgres.RejectCloseCommit(t, writer)
		stop := dispatchCloseIntent(request)
		failedClose, err := executions.ApplyCloseIntent(ctx, stop)
		if !errors.Is(err, biz.ErrPersistence) || !reflect.DeepEqual(failedClose, biz.CloseReceipt{}) {
			t.Fatal("failed close COMMIT must return a zero receipt")
		}
		trace.require(t, "injected_close_commit_failure")
		assertDispatchReplay(t, ctx, submission.New(openPool()), request, wantDispatch)
		stored, err = execution.New(openPool()).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
		if err != nil || !reflect.DeepEqual(stored, original) {
			t.Fatalf("failed close left a close/fence or consumed revision 4: %v", err)
		}
		remove()
		closed, err := executions.ApplyCloseIntent(ctx, stop)
		wantClose := biz.CloseRecord{CloseIntent: stop, Generation: 1, State: biz.CloseStateClosing}
		if err != nil || closed.Replayed || closed.OwnerRevision != 4 || !reflect.DeepEqual(closed.CloseRecord, wantClose) {
			t.Fatalf("recovered close must add one fact with the first owner fence: %v", err)
		}
		replayedClose, err := executions.ApplyCloseIntent(ctx, stop)
		if err != nil || !replayedClose.Replayed || replayedClose.OwnerRevision != 4 || !reflect.DeepEqual(replayedClose.CloseRecord, wantClose) {
			t.Fatalf("recovered close replay consumed a revision or fence: %v", err)
		}
		wantDispatch.OwnerRevision = 4
		assertDispatchReplay(t, ctx, submission.New(openPool()), request, wantDispatch)
		original.OwnerRevision, original.Close = 4, &wantClose
		original.States = biz.ExecutionStates{
			Compute: biz.ComputeStateSubmissionConfirmed, Delivery: biz.DeliveryStatePending,
			Resource: biz.ResourceStateNotApplicable, Close: biz.CloseStateClosing,
		}
		stored, err = execution.New(openPool()).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
		if err != nil || !reflect.DeepEqual(stored, original) {
			t.Fatalf("recovered writes did not retain one exact committed aggregate: %v", err)
		}
	})
}

type revisionCommitTrace struct {
	mu   sync.Mutex
	seen map[string]bool
}

type revisionCommitTraceKey struct{}

func (*revisionCommitTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, revisionCommitTraceKey{}, strings.EqualFold(strings.TrimSpace(data.SQL), "commit"))
}

func (trace *revisionCommitTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	commit, _ := ctx.Value(revisionCommitTraceKey{}).(bool)
	var fault *pgconn.PgError
	if !commit || !errors.As(data.Err, &fault) || fault.Code != "23514" {
		return
	}
	// Keep only these fixed test markers, never query arguments or raw errors.
	switch fault.Message {
	case "injected_admission_commit_failure", "injected_dispatch_commit_failure", "injected_confirmed_run_commit_failure", "injected_close_commit_failure":
		trace.mu.Lock()
		defer trace.mu.Unlock()
		if trace.seen == nil {
			trace.seen = make(map[string]bool)
		}
		trace.seen[fault.Message] = true
	}
}

func (trace *revisionCommitTrace) require(t *testing.T, marker string) {
	t.Helper()
	trace.mu.Lock()
	seen := trace.seen[marker]
	trace.mu.Unlock()
	if !seen {
		t.Fatalf("CPU04_REVISION_PREFLIGHT: exact deferred COMMIT marker %s was not observed; atomic behavior NOT_RUN", marker)
	}
	t.Log("CPU04_REVISION_COMMIT_FAULT: observed " + marker + " at real COMMIT")
}

func revisionTracePool(t *testing.T, ctx context.Context, fixture *pgxpool.Pool, trace pgx.QueryTracer) *pgxpool.Pool {
	t.Helper()
	config := fixture.Config()
	config.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("CPU04_REVISION_PREFLIGHT: traced runtime pool unavailable; behavior NOT_RUN")
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal("CPU04_REVISION_PREFLIGHT: traced runtime connection unavailable; behavior NOT_RUN")
	}
	return pool
}

func TestOwnerRevisionGetReturnsFactsFromOneDatabaseSnapshot(t *testing.T) {
	for _, reader := range []string{"Execution.Get", "Submission.Get"} {
		t.Run(reader, func(t *testing.T) {
			openPool := postgres.Prepare(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			request := validDispatchRequest(t)
			writer := openPool()
			executions, dispatches := execution.New(writer), submission.New(writer)
			accepted, err := executions.Accept(ctx, request.Admission)
			if err != nil || accepted.OwnerRevision != 1 || accepted.Replayed {
				t.Fatalf("CPU04_REVISION_PREFLIGHT: snapshot Admission failed: %v", err)
			}
			reserved, err := dispatches.Reserve(ctx, request)
			if err != nil || reserved.SendPermit == nil || reserved.Dispatch.OwnerRevision != 2 {
				t.Fatalf("CPU04_REVISION_PREFLIGHT: snapshot reservation failed: %v", err)
			}
			wantExecution := accepted.Execution
			wantExecution.OwnerRevision = 2
			wantExecution.States = biz.ExecutionStates{
				Compute: biz.ComputeStateSubmitting, Delivery: biz.DeliveryStatePending,
				Resource: biz.ResourceStateNotApplicable, Close: biz.CloseStateOpen,
			}
			query := "-- name: GetExecution :one\n"
			if reader == "Submission.Get" {
				query = "-- name: GetPipelineDispatch :one\n"
			}
			trace := &revisionReadTrace{query: query, reached: make(chan struct{}), resume: make(chan struct{})}
			readerPool := revisionTracePool(t, ctx, writer, trace)
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(trace.resume) }) }
			defer release()
			type readResult struct {
				execution biz.Execution
				dispatch  biz.PipelineDispatch
				err       error
			}
			done := make(chan readResult, 1)
			go func() {
				var result readResult
				if reader == "Execution.Get" {
					result.execution, result.err = execution.New(readerPool).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
				} else {
					result.dispatch, result.err = submission.New(readerPool).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
				}
				done <- result
			}()
			select {
			case <-trace.reached:
				t.Log("CPU04_REVISION_SNAPSHOT_PREFLIGHT PASS: Get completed its first real SELECT before the independent writers")
			case <-ctx.Done():
				t.Fatal("CPU04_REVISION_PREFLIGHT: first-read SQL boundary was not reached; snapshot behavior NOT_RUN")
			case <-done:
				t.Fatal("CPU04_REVISION_PREFLIGHT: Get finished without the coordinated first-read boundary; snapshot behavior NOT_RUN")
			}

			// The reader is between actual SQL reads in one public Get call.
			// These independent commits must remain outside that existing snapshot.
			closed, err := executions.ApplyCloseIntent(ctx, dispatchCloseIntent(request))
			if err != nil || closed.Replayed || closed.OwnerRevision != 3 {
				t.Fatalf("concurrent close did not commit before resuming Get: %v", err)
			}
			at := reserved.Dispatch.ReservedAt.Add(time.Microsecond)
			confirmed, err := dispatches.RecordSubmissionConfirmed(ctx, *reserved.SendPermit, confirmedObservation(confirmedRunID), at)
			wantDispatch := expectedConfirmation(reserved.Dispatch, 4, biz.PipelineConfirmedRun{RunID: confirmedRunID, FirstObservedAt: at})
			if err != nil || confirmed.ConflictingRuns || !reflect.DeepEqual(confirmed.Dispatch, wantDispatch) {
				t.Fatalf("concurrent Run did not commit before resuming Get: %v", err)
			}
			release()
			var got readResult
			select {
			case got = <-done:
			case <-ctx.Done():
				t.Fatal("coordinated Get did not finish within its bounded context")
			}
			if got.err != nil {
				t.Fatalf("Get mixed incompatible rows from different committed versions: %v", got.err)
			}
			if reader == "Execution.Get" {
				if !reflect.DeepEqual(got.execution, wantExecution) {
					t.Fatal("Execution.Get must return the original no-close facts with revision 2 from its existing snapshot")
				}
			} else if !reflect.DeepEqual(got.dispatch, reserved.Dispatch) {
				t.Fatal("Submission.Get must return the original SUBMITTING/no-Run facts with revision 2 from its existing snapshot")
			}
			// These are separate new reads; they prove later visibility, not the
			// consistency of the blocked call asserted immediately above.
			wantExecution.OwnerRevision, wantExecution.Close = 4, &closed.CloseRecord
			wantExecution.States = biz.ExecutionStates{
				Compute: biz.ComputeStateSubmissionConfirmed, Delivery: biz.DeliveryStatePending,
				Resource: biz.ResourceStateNotApplicable, Close: biz.CloseStateClosing,
			}
			latest, err := execution.New(openPool()).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
			if err != nil || !reflect.DeepEqual(latest, wantExecution) {
				t.Fatalf("new execution read did not see both committed writers: %v", err)
			}
			assertDispatchReplay(t, ctx, submission.New(openPool()), request, wantDispatch)
		})
	}
}

// The tracer coordinates only a completed SQL boundary. It does not supply
// rows, choose isolation, or inspect arguments; the returned value still comes
// from the unmodified public Get and real PostgreSQL transaction.
type revisionReadTrace struct {
	query   string
	reached chan struct{}
	resume  chan struct{}
	paused  atomic.Bool
}

type revisionReadTraceKey struct{}

func (trace *revisionReadTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, revisionReadTraceKey{}, strings.HasPrefix(data.SQL, trace.query))
}

func (trace *revisionReadTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	matched, _ := ctx.Value(revisionReadTraceKey{}).(bool)
	if !matched || data.Err != nil || !trace.paused.CompareAndSwap(false, true) {
		return
	}
	close(trace.reached)
	select {
	case <-trace.resume:
	case <-ctx.Done():
	}
}

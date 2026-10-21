package submission_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestUncertaintyRejectsOtherIdentityOrPlanWithoutChangingOriginal(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*biz.PipelineSendPermit)
		want   error
	}{
		{"other tenant", func(p *biz.PipelineSendPermit) { p.TenantID = otherDispatchTenant }, biz.ErrExecutionNotFound},
		{"other execution", func(p *biz.PipelineSendPermit) { p.ExecutionID = "cccccccc-dddd-4eee-8fff-aaaaaaaaaaaa" }, biz.ErrExecutionNotFound},
		{"other attempt", func(p *biz.PipelineSendPermit) { p.AttemptID = "cccccccc-dddd-4eee-8fff-aaaaaaaaaaaa" }, biz.ErrAdmissionConflict},
		{"other plan hash", func(p *biz.PipelineSendPermit) { p.PlanHash = strings.Repeat("0", 64) }, biz.ErrAdmissionConflict},
		{"zero tenant", func(p *biz.PipelineSendPermit) { p.TenantID = uuid.Nil.String() }, biz.ErrInvalidAdmission},
		{"malformed execution", func(p *biz.PipelineSendPermit) { p.ExecutionID = strings.ReplaceAll(p.ExecutionID, "-", "X") }, biz.ErrInvalidAdmission},
		{"zero attempt", func(p *biz.PipelineSendPermit) { p.AttemptID = uuid.Nil.String() }, biz.ErrInvalidAdmission},
		{"malformed attempt", func(p *biz.PipelineSendPermit) { p.AttemptID = strings.ReplaceAll(p.AttemptID, "-", "X") }, biz.ErrInvalidAdmission},
		{"short plan hash", func(p *biz.PipelineSendPermit) { p.PlanHash = "a" }, biz.ErrInvalidAdmission},
		{"nonhex plan hash", func(p *biz.PipelineSendPermit) { p.PlanHash = strings.Repeat("g", 64) }, biz.ErrInvalidAdmission},
		{"uppercase plan hash", func(p *biz.PipelineSendPermit) { p.PlanHash = "A" + p.PlanHash[1:] }, biz.ErrInvalidAdmission},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			openPool := postgres.Prepare(t)
			request := validDispatchRequest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			first := reserveUncertaintyAttempt(t, ctx, openPool(), request)
			candidate := *first.SendPermit
			testCase.mutate(&candidate)
			observedAt := first.Dispatch.ReservedAt.Add(time.Microsecond)
			repository := submission.New(openPool())
			got, err := repository.MarkSubmissionUncertain(ctx, candidate, observedAt)
			assertUncertaintyRejected(t, got, err, testCase.want)
			assertDispatchReplay(t, ctx, repository, request, first.Dispatch)
			original := markOriginalUncertain(t, ctx, repository, first, observedAt)
			got, err = submission.New(openPool()).MarkSubmissionUncertain(ctx, candidate, observedAt.Add(time.Microsecond))
			assertUncertaintyRejected(t, got, err, testCase.want)
			assertDispatchReplay(t, ctx, submission.New(openPool()), request, original)
			if candidate.TenantID != first.SendPermit.TenantID || candidate.ExecutionID != first.SendPermit.ExecutionID {
				if testCase.want == biz.ErrExecutionNotFound {
					assertNoDispatch(t, ctx, repository, candidate.TenantID, candidate.ExecutionID)
				}
			}
		})
	}
}

func TestUncertaintyCannotCreateAMissingReservation(t *testing.T) {
	for _, setup := range []string{"no identity", "admission only", "close tombstone only"} {
		t.Run(setup, func(t *testing.T) {
			openPool := postgres.Prepare(t)
			request := validDispatchRequest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			admissions := execution.New(openPool())
			switch setup {
			case "admission only":
				acceptDispatchAdmission(t, ctx, admissions, request)
			case "close tombstone only":
				if _, err := admissions.ApplyCloseIntent(ctx, dispatchCloseIntent(request)); err != nil {
					t.Fatalf("close fixture failed; uncertainty behavior NOT_RUN: %v", err)
				}
			}
			plan, err := request.Freeze()
			if err != nil {
				t.Fatalf("plan fixture: %v", err)
			}
			hash, err := plan.Digest()
			if err != nil {
				t.Fatalf("plan hash fixture: %v", err)
			}
			// Syntactically valid fields are not proof that any permit was issued.
			unissued := biz.PipelineSendPermit{TenantID: request.Admission.TenantID, ExecutionID: request.Admission.ExecutionID, AttemptID: uuid.NewString(), PlanHash: hash}
			repository := submission.New(openPool())
			got, err := repository.MarkSubmissionUncertain(ctx, unissued, request.Admission.AcceptedAt)
			assertUncertaintyRejected(t, got, err, biz.ErrExecutionNotFound)
			assertNoDispatch(t, ctx, submission.New(openPool()), unissued.TenantID, unissued.ExecutionID)
		})
	}
}

func TestUncertaintyRejectsInvalidObservationTimeWithoutRoundingOrReplacing(t *testing.T) {
	cases := []struct {
		name  string
		value func(time.Time) time.Time
	}{
		{"zero", func(time.Time) time.Time { return time.Time{} }},
		{"year zero", func(time.Time) time.Time { return time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC) }},
		{"year ten thousand", func(time.Time) time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }},
		{"nanosecond precision", func(at time.Time) time.Time { return at.Add(time.Nanosecond) }},
		{"before reservation", func(at time.Time) time.Time { return at.Add(-time.Microsecond) }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			openPool := postgres.Prepare(t)
			request := validDispatchRequest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			first := reserveUncertaintyAttempt(t, ctx, openPool(), request)
			repository := submission.New(openPool())
			badTime := testCase.value(first.Dispatch.ReservedAt)
			got, err := repository.MarkSubmissionUncertain(ctx, *first.SendPermit, badTime)
			assertUncertaintyRejected(t, got, err, biz.ErrInvalidAdmission)
			assertDispatchReplay(t, ctx, repository, request, first.Dispatch)
			original := markOriginalUncertain(t, ctx, repository, first, first.Dispatch.ReservedAt)
			got, err = submission.New(openPool()).MarkSubmissionUncertain(ctx, *first.SendPermit, badTime)
			assertUncertaintyRejected(t, got, err, biz.ErrInvalidAdmission)
			assertDispatchReplay(t, ctx, submission.New(openPool()), request, original)
		})
	}
}

func TestUncertaintyAcceptsCanonicalUUIDAndUTCOffsetAliases(t *testing.T) {
	openPool := postgres.Prepare(t)
	request := validDispatchRequest(t)
	request.Admission.TenantID = "aaaaaaaa-2222-4333-8444-555555555555"
	refreshDispatchHashes(t, &request.Admission)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first := reserveUncertaintyAttempt(t, ctx, openPool(), request)
	alias := *first.SendPermit
	alias.TenantID, alias.ExecutionID, alias.AttemptID = strings.ToUpper(alias.TenantID), strings.ToUpper(alias.ExecutionID), strings.ToUpper(alias.AttemptID)
	// The same instant at the exact lower bound is legal in another offset;
	// normalization must not change it or add precision that was not supplied.
	observedAt := first.Dispatch.ReservedAt.In(time.FixedZone("fixture-offset", 8*60*60))
	got, err := submission.New(openPool()).MarkSubmissionUncertain(ctx, alias, observedAt)
	if err != nil {
		t.Fatalf("legal typed UUID/time aliases rejected: %v", err)
	}
	want := expectedUncertainty(first.Dispatch, 3, first.Dispatch.ReservedAt)
	if !reflect.DeepEqual(got, want) || got.UncertainAt.Location() != time.UTC {
		t.Fatal("uncertainty did not retain canonical UUIDs and the exact UTC instant")
	}
	assertDispatchReplay(t, ctx, submission.New(openPool()), request, want)
	for _, at := range []time.Time{first.Dispatch.ReservedAt, observedAt.Add(time.Microsecond)} {
		replayed, err := submission.New(openPool()).MarkSubmissionUncertain(ctx, *first.SendPermit, at)
		if err != nil || !reflect.DeepEqual(replayed, want) {
			t.Fatalf("alias replay changed the first observation: %v", err)
		}
	}
}

func TestUncertaintyAfterDatabaseDeadlineRetainsOriginalAttempt(t *testing.T) {
	openPool := postgres.Prepare(t)
	observer := openPool()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := databaseTime(t, ctx, observer)
	request := validDispatchRequest(t)
	request.Admission.AcceptedAt = now.Add(-time.Minute)
	request.Admission.Snapshot.DeadlineAt = now.Add(3 * time.Second)
	refreshDispatchHashes(t, &request.Admission)
	first := reserveUncertaintyAttempt(t, ctx, openPool(), request)
	waitForDatabaseDeadline(t, ctx, observer, request.Admission.Snapshot.DeadlineAt)
	observedAt := databaseTime(t, ctx, observer)
	original := markOriginalUncertain(t, ctx, submission.New(openPool()), first, observedAt)
	assertDispatchReplay(t, ctx, submission.New(openPool()), request, original)
}

func TestConcurrentUncertaintyObserversKeepOneFirstCommittedObservation(t *testing.T) {
	openPool := postgres.Prepare(t)
	request := validDispatchRequest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first := reserveUncertaintyAttempt(t, ctx, openPool(), request)
	repositories := []*submission.Repository{submission.New(openPool()), submission.New(openPool())}
	observedTimes := []time.Time{first.Dispatch.ReservedAt.Add(time.Microsecond), first.Dispatch.ReservedAt.Add(2 * time.Microsecond)}
	type result struct {
		dispatch biz.PipelineDispatch
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, len(repositories))
	for index, repository := range repositories {
		go func(repository *submission.Repository, observedAt time.Time) {
			<-start
			dispatch, err := repository.MarkSubmissionUncertain(ctx, *first.SendPermit, observedAt)
			results <- result{dispatch: dispatch, err: err}
		}(repository, observedTimes[index])
	}
	close(start)
	var original biz.PipelineDispatch
	for range repositories {
		select {
		case got := <-results:
			if got.err != nil || got.dispatch.UncertainAt == nil {
				t.Fatalf("concurrent original observation rejected: %v", got.err)
			}
			if original.AttemptID == "" {
				original = got.dispatch
				if !original.UncertainAt.Equal(observedTimes[0]) && !original.UncertainAt.Equal(observedTimes[1]) {
					t.Fatal("concurrent observers invented another observation timestamp")
				}
				if !reflect.DeepEqual(original, expectedUncertainty(first.Dispatch, 3, *original.UncertainAt)) {
					t.Fatal("concurrent observation replaced the original attempt or frozen plan")
				}
				visible, err := submission.New(openPool()).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
				if err != nil || !reflect.DeepEqual(visible, original) {
					t.Fatalf("observation returned before independent committed visibility: %v", err)
				}
			} else if !reflect.DeepEqual(got.dispatch, original) {
				t.Fatal("concurrent observers did not retain the same first committed observation")
			}
		case <-ctx.Done():
			t.Fatal("concurrent original observations did not finish within the bounded context")
		}
	}
	assertDispatchReplay(t, ctx, submission.New(openPool()), request, original)
	// An earlier but still valid reported timestamp does not rewrite which
	// uncertainty observation this owner first acknowledged and committed.
	replayed, err := submission.New(openPool()).MarkSubmissionUncertain(ctx, *first.SendPermit, first.Dispatch.ReservedAt)
	if err != nil || !reflect.DeepEqual(replayed, original) {
		t.Fatalf("later delivery rewrote the first committed observation: %v", err)
	}
}

func TestConcurrentCloseAndUncertaintyBothRemainDurable(t *testing.T) {
	openPool := postgres.Prepare(t)
	request := validDispatchRequest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first := reserveUncertaintyAttempt(t, ctx, openPool(), request)
	observedAt := first.Dispatch.ReservedAt.Add(time.Microsecond)
	closes := execution.New(openPool())
	observations := submission.New(openPool())
	type closeResult struct {
		receipt biz.CloseReceipt
		err     error
	}
	type observationResult struct {
		dispatch biz.PipelineDispatch
		err      error
	}
	start := make(chan struct{})
	closeDone := make(chan closeResult, 1)
	observationDone := make(chan observationResult, 1)
	go func() {
		<-start
		receipt, err := closes.ApplyCloseIntent(ctx, dispatchCloseIntent(request))
		closeDone <- closeResult{receipt: receipt, err: err}
	}()
	go func() {
		<-start
		dispatch, err := observations.MarkSubmissionUncertain(ctx, *first.SendPermit, observedAt)
		observationDone <- observationResult{dispatch: dispatch, err: err}
	}()
	close(start)
	var closed closeResult
	select {
	case closed = <-closeDone:
	case <-ctx.Done():
		t.Fatal("concurrent close did not finish")
	}
	var observed observationResult
	select {
	case observed = <-observationDone:
	case <-ctx.Done():
		t.Fatal("concurrent uncertainty did not finish")
	}
	if !((observed.dispatch.OwnerRevision == 3 && closed.receipt.OwnerRevision == 4) ||
		(observed.dispatch.OwnerRevision == 4 && closed.receipt.OwnerRevision == 3)) {
		t.Fatalf("close and uncertainty must occupy distinct revisions 3 and 4: close=%d uncertainty=%d", closed.receipt.OwnerRevision, observed.dispatch.OwnerRevision)
	}
	want := expectedUncertainty(first.Dispatch, 3, observedAt)
	if closed.receipt.OwnerRevision == 3 {
		want.OwnerRevision = 4
	}
	if closed.err != nil || observed.err != nil || closed.receipt.Replayed || closed.receipt.State != biz.CloseStateClosing || !reflect.DeepEqual(observed.dispatch, want) {
		t.Fatalf("both original facts must commit: close=%v uncertainty=%v", closed.err, observed.err)
	}
	admitted, err := execution.New(openPool()).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || admitted.OwnerRevision != 4 || admitted.Close == nil || !reflect.DeepEqual(*admitted.Close, closed.receipt.CloseRecord) {
		t.Fatalf("uncertainty lost or reopened the concurrent close: %v", err)
	}
	assertSameDispatchAdmission(t, admitted.Admission, request.Admission)
	want.OwnerRevision = 4
	assertDispatchReplay(t, ctx, submission.New(openPool()), request, want)
}

func reserveUncertaintyAttempt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, request biz.PipelineDispatchRequest) biz.PipelineDispatchReservation {
	t.Helper()
	acceptDispatchAdmission(t, ctx, execution.New(pool), request)
	first, err := submission.New(pool).Reserve(ctx, request)
	if err != nil || first.SendPermit == nil || first.Dispatch.State != biz.PipelineDispatchSubmitting || first.Dispatch.UncertainAt != nil {
		t.Fatalf("real reservation fixture failed; uncertainty behavior NOT_RUN: %v", err)
	}
	return first
}

func markOriginalUncertain(t *testing.T, ctx context.Context, repository *submission.Repository, first biz.PipelineDispatchReservation, observedAt time.Time) biz.PipelineDispatch {
	t.Helper()
	got, err := repository.MarkSubmissionUncertain(ctx, *first.SendPermit, observedAt)
	if err != nil || !reflect.DeepEqual(got, expectedUncertainty(first.Dispatch, 3, observedAt)) {
		t.Fatalf("valid original observation changed immutable facts: %v", err)
	}
	return got
}

func expectedUncertainty(original biz.PipelineDispatch, ownerRevision uint64, observedAt time.Time) biz.PipelineDispatch {
	observedAt = observedAt.UTC()
	original.OwnerRevision = ownerRevision
	original.State = biz.PipelineDispatchUncertain
	original.UncertainAt = &observedAt
	return original
}

func assertUncertaintyRejected(t *testing.T, got biz.PipelineDispatch, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || !reflect.DeepEqual(got, biz.PipelineDispatch{}) {
		t.Fatalf("CPU07_UNCERTAIN_BEHAVIOR: want %v with no acknowledged fact, got %v nonempty=%t", want, err, !reflect.DeepEqual(got, biz.PipelineDispatch{}))
	}
}

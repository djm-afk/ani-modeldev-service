package submission_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

const confirmedRunID = "55555555-6666-4777-8888-999999999999"

type confirmationInput struct {
	permit      biz.PipelineSendPermit
	observation biz.PipelineSubmissionObservation
	at          time.Time
}

func TestConfirmationRejectsInvalidOrMismatchedObservationWithoutMutation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*confirmationInput)
		want   error
	}{
		{"other tenant", func(v *confirmationInput) { v.permit.TenantID = otherDispatchTenant }, biz.ErrExecutionNotFound},
		{"other execution", func(v *confirmationInput) { v.permit.ExecutionID = "cccccccc-dddd-4eee-8fff-aaaaaaaaaaaa" }, biz.ErrExecutionNotFound},
		{"other attempt", func(v *confirmationInput) { v.permit.AttemptID = "cccccccc-dddd-4eee-8fff-aaaaaaaaaaaa" }, biz.ErrAdmissionConflict},
		{"other plan", func(v *confirmationInput) { v.permit.PlanHash = strings.Repeat("0", 64) }, biz.ErrAdmissionConflict},
		{"zero tenant", func(v *confirmationInput) { v.permit.TenantID = uuid.Nil.String() }, biz.ErrInvalidAdmission},
		{"malformed execution", func(v *confirmationInput) { v.permit.ExecutionID = strings.ReplaceAll(v.permit.ExecutionID, "-", "X") }, biz.ErrInvalidAdmission},
		{"zero attempt", func(v *confirmationInput) { v.permit.AttemptID = uuid.Nil.String() }, biz.ErrInvalidAdmission},
		{"nonhex plan", func(v *confirmationInput) { v.permit.PlanHash = strings.Repeat("g", 64) }, biz.ErrInvalidAdmission},
		{"uppercase plan", func(v *confirmationInput) { v.permit.PlanHash = "A" + v.permit.PlanHash[1:] }, biz.ErrInvalidAdmission},
		{"empty run", func(v *confirmationInput) { v.observation.RunID = "" }, biz.ErrInvalidAdmission},
		{"zero run", func(v *confirmationInput) { v.observation.RunID = uuid.Nil.String() }, biz.ErrInvalidAdmission},
		{"malformed run", func(v *confirmationInput) { v.observation.RunID = strings.ReplaceAll(v.observation.RunID, "-", "X") }, biz.ErrInvalidAdmission},
		{"uncertain outcome", func(v *confirmationInput) { v.observation.State = biz.PipelineSubmissionUncertain }, biz.ErrInvalidAdmission},
		{"not sent outcome", func(v *confirmationInput) { v.observation.State = biz.PipelineSubmissionNotSent }, biz.ErrInvalidAdmission},
		{"unknown outcome", func(v *confirmationInput) { v.observation.State = "SUCCESS" }, biz.ErrInvalidAdmission},
		{"zero time", func(v *confirmationInput) { v.at = time.Time{} }, biz.ErrInvalidAdmission},
		{"year zero", func(v *confirmationInput) { v.at = time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC) }, biz.ErrInvalidAdmission},
		{"year ten thousand", func(v *confirmationInput) { v.at = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }, biz.ErrInvalidAdmission},
		{"submicrosecond time", func(v *confirmationInput) { v.at = v.at.Add(time.Nanosecond) }, biz.ErrInvalidAdmission},
		{"before reservation", func(v *confirmationInput) { v.at = v.at.Add(-2 * time.Microsecond) }, biz.ErrInvalidAdmission},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			openPool := postgres.Prepare(t)
			request := validDispatchRequest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			first := reserveUncertaintyAttempt(t, ctx, openPool(), request)
			valid := confirmationInput{permit: *first.SendPermit, observation: confirmedObservation(confirmedRunID), at: first.Dispatch.ReservedAt.Add(time.Microsecond)}
			invalid := valid
			testCase.mutate(&invalid)
			repository := submission.New(openPool())
			got, err := repository.RecordSubmissionConfirmed(ctx, invalid.permit, invalid.observation, invalid.at)
			assertConfirmationRejected(t, got, err, testCase.want)
			assertDispatchReplay(t, ctx, repository, request, first.Dispatch)
			original := expectedConfirmation(first.Dispatch, 3, biz.PipelineConfirmedRun{RunID: confirmedRunID, FirstObservedAt: valid.at})
			got, err = repository.RecordSubmissionConfirmed(ctx, valid.permit, valid.observation, valid.at)
			if err != nil || got.ConflictingRuns || !reflect.DeepEqual(got.Dispatch, original) {
				t.Fatalf("valid original confirmation failed: %v", err)
			}
			got, err = submission.New(openPool()).RecordSubmissionConfirmed(ctx, invalid.permit, invalid.observation, invalid.at)
			assertConfirmationRejected(t, got, err, testCase.want)
			assertDispatchReplay(t, ctx, submission.New(openPool()), request, original)
			if testCase.want == biz.ErrExecutionNotFound {
				assertNoDispatch(t, ctx, repository, invalid.permit.TenantID, invalid.permit.ExecutionID)
			}
		})
	}
}

func TestConfirmationCannotCreateAMissingReservation(t *testing.T) {
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
					t.Fatalf("close fixture failed; confirmation behavior NOT_RUN: %v", err)
				}
			}
			plan, err := request.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			hash, err := plan.Digest()
			if err != nil {
				t.Fatal(err)
			}
			unissued := biz.PipelineSendPermit{TenantID: request.Admission.TenantID, ExecutionID: request.Admission.ExecutionID, AttemptID: uuid.NewString(), PlanHash: hash}
			got, err := submission.New(openPool()).RecordSubmissionConfirmed(ctx, unissued, confirmedObservation(confirmedRunID), request.Admission.AcceptedAt)
			assertConfirmationRejected(t, got, err, biz.ErrExecutionNotFound)
			assertNoDispatch(t, ctx, submission.New(openPool()), unissued.TenantID, unissued.ExecutionID)
		})
	}
}

func TestConfirmationAliasesAndLateUncertaintyPreserveOriginalObservations(t *testing.T) {
	for _, priorUncertainty := range []bool{false, true} {
		name := "direct confirmation"
		if priorUncertainty {
			name = "confirmation after uncertainty"
		}
		t.Run(name, func(t *testing.T) {
			openPool := postgres.Prepare(t)
			request := validDispatchRequest(t)
			request.Admission.TenantID = "aaaaaaaa-2222-4333-8444-555555555555"
			refreshDispatchHashes(t, &request.Admission)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			first := reserveUncertaintyAttempt(t, ctx, openPool(), request)
			repository := submission.New(openPool())
			original := first.Dispatch
			observedAt := first.Dispatch.ReservedAt
			if priorUncertainty {
				original = markOriginalUncertain(t, ctx, repository, first, observedAt.Add(2*time.Microsecond))
				// A later delivery may describe an instant before uncertainty.
				observedAt = observedAt.Add(time.Microsecond)
			}
			alias := *first.SendPermit
			alias.TenantID, alias.ExecutionID, alias.AttemptID = strings.ToUpper(alias.TenantID), strings.ToUpper(alias.ExecutionID), strings.ToUpper(alias.AttemptID)
			runID := "abcdefab-cdef-4abc-8def-abcdefabcdef"
			observation := confirmedObservation(strings.ToUpper(runID))
			offsetTime := observedAt.In(time.FixedZone("fixture-offset", 8*60*60))
			got, err := repository.RecordSubmissionConfirmed(ctx, alias, observation, offsetTime)
			want := expectedConfirmation(original, 3, biz.PipelineConfirmedRun{RunID: runID, FirstObservedAt: observedAt})
			if priorUncertainty {
				want.OwnerRevision = 4
			}
			if err != nil || got.ConflictingRuns || !reflect.DeepEqual(got.Dispatch, want) {
				t.Fatalf("canonical UUID and UTC instant were not retained: %v", err)
			}
			for _, at := range []time.Time{first.Dispatch.ReservedAt, observedAt.Add(time.Microsecond)} {
				replayed, err := submission.New(openPool()).RecordSubmissionConfirmed(ctx, *first.SendPermit, confirmedObservation(runID), at)
				if err != nil || replayed.ConflictingRuns || !reflect.DeepEqual(replayed.Dispatch, want) {
					t.Fatalf("replayed confirmation replaced its first committed time: %v", err)
				}
			}
			late, err := submission.New(openPool()).MarkSubmissionUncertain(ctx, *first.SendPermit, observedAt.Add(3*time.Microsecond))
			if err != nil || !reflect.DeepEqual(late, want) {
				t.Fatalf("late uncertainty downgraded confirmation or changed its original observations: %v", err)
			}
			assertDispatchReplay(t, ctx, submission.New(openPool()), request, want)
		})
	}
}

func TestConcurrentConfirmationsRetainSameOrConflictingRuns(t *testing.T) {
	for _, differentRuns := range []bool{false, true} {
		name := "same Run"
		if differentRuns {
			name = "different Runs"
		}
		t.Run(name, func(t *testing.T) {
			openPool := postgres.Prepare(t)
			request := validDispatchRequest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			first := reserveUncertaintyAttempt(t, ctx, openPool(), request)
			runIDs := []string{confirmedRunID, confirmedRunID}
			if differentRuns {
				runIDs[1] = "66666666-7777-4888-8999-aaaaaaaaaaaa"
			}
			times := []time.Time{first.Dispatch.ReservedAt.Add(time.Microsecond), first.Dispatch.ReservedAt.Add(2 * time.Microsecond)}
			repositories := []*submission.Repository{submission.New(openPool()), submission.New(openPool())}
			type result struct {
				receipt biz.PipelineConfirmationReceipt
				err     error
			}
			start := make(chan struct{})
			results := make(chan result, len(repositories))
			for index, repository := range repositories {
				go func(repository *submission.Repository, runID string, at time.Time) {
					<-start
					receipt, err := repository.RecordSubmissionConfirmed(ctx, *first.SendPermit, confirmedObservation(runID), at)
					results <- result{receipt: receipt, err: err}
				}(repository, runIDs[index], times[index])
			}
			close(start)
			var receipts []biz.PipelineConfirmationReceipt
			for range repositories {
				select {
				case got := <-results:
					if got.err != nil {
						t.Fatalf("original concurrent observation rejected: %v", got.err)
					}
					receipts = append(receipts, got.receipt)
				case <-ctx.Done():
					t.Fatal("concurrent confirmations did not finish")
				}
			}
			var want biz.PipelineDispatch
			if differentRuns {
				want = expectedConfirmation(first.Dispatch, 4,
					biz.PipelineConfirmedRun{RunID: runIDs[0], FirstObservedAt: times[0]},
					biz.PipelineConfirmedRun{RunID: runIDs[1], FirstObservedAt: times[1]})
				conflicts := 0
				for _, receipt := range receipts {
					if receipt.ConflictingRuns {
						conflicts++
						if !reflect.DeepEqual(receipt.Dispatch, want) {
							t.Fatal("conflict receipt lost one of the two original Run observations")
						}
					} else {
						if len(receipt.Dispatch.ConfirmedRuns) != 1 {
							t.Fatal("first confirmation did not retain exactly its Run")
						}
						run := receipt.Dispatch.ConfirmedRuns[0]
						index := 0
						if run.RunID == runIDs[1] {
							index = 1
						}
						if !reflect.DeepEqual(receipt.Dispatch, expectedConfirmation(first.Dispatch, 3, biz.PipelineConfirmedRun{RunID: runIDs[index], FirstObservedAt: times[index]})) {
							t.Fatal("first confirmation changed the original attempt or Run")
						}
					}
				}
				if conflicts != 1 {
					t.Fatal("distinct Run observations must commit once each and report the retained conflict")
				}
			} else {
				if len(receipts[0].Dispatch.ConfirmedRuns) != 1 {
					t.Fatal("same Run race did not retain one observation")
				}
				at := receipts[0].Dispatch.ConfirmedRuns[0].FirstObservedAt
				if !at.Equal(times[0]) && !at.Equal(times[1]) {
					t.Fatal("same Run race invented another first observation time")
				}
				want = expectedConfirmation(first.Dispatch, 3, biz.PipelineConfirmedRun{RunID: confirmedRunID, FirstObservedAt: at})
				for _, receipt := range receipts {
					if receipt.ConflictingRuns || !reflect.DeepEqual(receipt.Dispatch, want) {
						t.Fatal("same Run race refreshed the first time or falsely reported conflicting Runs")
					}
				}
			}
			// The independent reader must see both committed facts even when
			// they disagree; no last-writer replacement or second permit exists.
			assertDispatchReplay(t, ctx, submission.New(openPool()), request, want)
			replayed, err := submission.New(openPool()).RecordSubmissionConfirmed(ctx, *first.SendPermit, confirmedObservation(runIDs[0]), first.Dispatch.ReservedAt)
			if err != nil || replayed.ConflictingRuns != differentRuns || !reflect.DeepEqual(replayed.Dispatch, want) {
				t.Fatalf("replay dropped a conflict or refreshed an earlier Run: %v", err)
			}
		})
	}
}

func TestConcurrentCloseAndConfirmedRunBothRemainDurable(t *testing.T) {
	openPool := postgres.Prepare(t)
	request := validDispatchRequest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first := reserveUncertaintyAttempt(t, ctx, openPool(), request)
	observedAt := first.Dispatch.ReservedAt.Add(time.Microsecond)
	closes, confirmations := execution.New(openPool()), submission.New(openPool())
	type closeResult struct {
		receipt biz.CloseReceipt
		err     error
	}
	type confirmationResult struct {
		receipt biz.PipelineConfirmationReceipt
		err     error
	}
	start := make(chan struct{})
	closed := make(chan closeResult, 1)
	confirmed := make(chan confirmationResult, 1)
	go func() {
		<-start
		receipt, err := closes.ApplyCloseIntent(ctx, dispatchCloseIntent(request))
		closed <- closeResult{receipt: receipt, err: err}
	}()
	go func() {
		<-start
		receipt, err := confirmations.RecordSubmissionConfirmed(ctx, *first.SendPermit, confirmedObservation(confirmedRunID), observedAt)
		confirmed <- confirmationResult{receipt: receipt, err: err}
	}()
	close(start)
	var closeFact closeResult
	select {
	case closeFact = <-closed:
	case <-ctx.Done():
		t.Fatal("concurrent close did not finish")
	}
	var confirmation confirmationResult
	select {
	case confirmation = <-confirmed:
	case <-ctx.Done():
		t.Fatal("concurrent confirmation did not finish")
	}
	if !((confirmation.receipt.Dispatch.OwnerRevision == 3 && closeFact.receipt.OwnerRevision == 4) ||
		(confirmation.receipt.Dispatch.OwnerRevision == 4 && closeFact.receipt.OwnerRevision == 3)) {
		t.Fatalf("close and Run must occupy distinct revisions 3 and 4: close=%d Run=%d", closeFact.receipt.OwnerRevision, confirmation.receipt.Dispatch.OwnerRevision)
	}
	want := expectedConfirmation(first.Dispatch, 3, biz.PipelineConfirmedRun{RunID: confirmedRunID, FirstObservedAt: observedAt})
	if closeFact.receipt.OwnerRevision == 3 {
		want.OwnerRevision = 4
	}
	if closeFact.err != nil || closeFact.receipt.Replayed || closeFact.receipt.State != biz.CloseStateClosing || confirmation.err != nil || confirmation.receipt.ConflictingRuns || !reflect.DeepEqual(confirmation.receipt.Dispatch, want) {
		t.Fatalf("both original facts must commit: close=%v confirmation=%v", closeFact.err, confirmation.err)
	}
	admitted, err := execution.New(openPool()).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || admitted.OwnerRevision != 4 || admitted.Close == nil || !reflect.DeepEqual(*admitted.Close, closeFact.receipt.CloseRecord) {
		t.Fatalf("confirmation lost or reopened the concurrent close: %v", err)
	}
	assertSameDispatchAdmission(t, admitted.Admission, request.Admission)
	want.OwnerRevision = 4
	assertDispatchReplay(t, ctx, submission.New(openPool()), request, want)
}

func TestConfirmedRunObservationsKeepOriginalTenantScope(t *testing.T) {
	openPool := postgres.Prepare(t)
	request := validDispatchRequest(t)
	other := request
	other.Admission.TenantID = otherDispatchTenant
	other.Admission.ExecutionID = "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff"
	other.Admission.OperationID = "cccccccc-dddd-4eee-8fff-aaaaaaaaaaaa"
	refreshDispatchHashes(t, &other.Admission)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first := reserveUncertaintyAttempt(t, ctx, openPool(), request)
	second := reserveUncertaintyAttempt(t, ctx, openPool(), other)
	firstTime, secondTime := first.Dispatch.ReservedAt.Add(time.Microsecond), second.Dispatch.ReservedAt.Add(time.Microsecond)
	wantFirst := expectedConfirmation(first.Dispatch, 3, biz.PipelineConfirmedRun{RunID: confirmedRunID, FirstObservedAt: firstTime})
	wantSecond := expectedConfirmation(second.Dispatch, 3, biz.PipelineConfirmedRun{RunID: confirmedRunID, FirstObservedAt: secondTime})
	// A Run UUID alone is not a global ownership claim. Each internal
	// observation stays attached to its original tenant/attempt/frozen plan.
	for _, testCase := range []struct {
		permit biz.PipelineSendPermit
		at     time.Time
		want   biz.PipelineDispatch
	}{
		{*first.SendPermit, firstTime, wantFirst},
		{*second.SendPermit, secondTime, wantSecond},
	} {
		got, err := submission.New(openPool()).RecordSubmissionConfirmed(ctx, testCase.permit, confirmedObservation(confirmedRunID), testCase.at)
		if err != nil || got.ConflictingRuns || !reflect.DeepEqual(got.Dispatch, testCase.want) {
			t.Fatalf("independent scoped observation was rejected or mixed with another tenant: %v", err)
		}
	}
	repository := submission.New(openPool())
	wrongTenant := *first.SendPermit
	wrongTenant.TenantID = second.SendPermit.TenantID
	got, err := repository.RecordSubmissionConfirmed(ctx, wrongTenant, confirmedObservation(confirmedRunID), secondTime)
	assertConfirmationRejected(t, got, err, biz.ErrExecutionNotFound)
	assertNoDispatch(t, ctx, repository, wrongTenant.TenantID, wrongTenant.ExecutionID)
	mixedAttempt := *second.SendPermit
	mixedAttempt.AttemptID = first.SendPermit.AttemptID
	got, err = repository.RecordSubmissionConfirmed(ctx, mixedAttempt, confirmedObservation(confirmedRunID), secondTime)
	assertConfirmationRejected(t, got, err, biz.ErrAdmissionConflict)
	assertDispatchReplay(t, ctx, submission.New(openPool()), request, wantFirst)
	assertDispatchReplay(t, ctx, submission.New(openPool()), other, wantSecond)
}

func TestConfirmedRunAfterDatabaseDeadlineRetainsOriginalAttempt(t *testing.T) {
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
	got, err := submission.New(openPool()).RecordSubmissionConfirmed(ctx, *first.SendPermit, confirmedObservation(confirmedRunID), observedAt)
	want := expectedConfirmation(first.Dispatch, 3, biz.PipelineConfirmedRun{RunID: confirmedRunID, FirstObservedAt: observedAt})
	if err != nil || got.ConflictingRuns || !reflect.DeepEqual(got.Dispatch, want) {
		t.Fatalf("expired deadline discarded the original attempt's late handle: %v", err)
	}
	assertDispatchReplay(t, ctx, submission.New(openPool()), request, want)
}

func confirmedObservation(runID string) biz.PipelineSubmissionObservation {
	return biz.PipelineSubmissionObservation{State: biz.PipelineSubmissionConfirmed, RunID: runID}
}

func expectedConfirmation(original biz.PipelineDispatch, ownerRevision uint64, runs ...biz.PipelineConfirmedRun) biz.PipelineDispatch {
	original.OwnerRevision = ownerRevision
	original.State = biz.PipelineDispatchConfirmed
	original.ConfirmedRuns = runs
	return original
}

func assertConfirmationRejected(t *testing.T, got biz.PipelineConfirmationReceipt, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || !reflect.DeepEqual(got, biz.PipelineConfirmationReceipt{}) {
		t.Fatalf("CPU07_CONFIRMED_BEHAVIOR: want %v with no receipt, got %v nonempty=%t", want, err, !reflect.DeepEqual(got, biz.PipelineConfirmationReceipt{}))
	}
}

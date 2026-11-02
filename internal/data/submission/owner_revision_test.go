package submission_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestOwnerRevisionTracksCommittedFactsAndNotReplayedDeliveries(t *testing.T) {
	openPool := postgres.Prepare(t)
	request := validDispatchRequest(t, openPool())
	plan, err := request.Freeze()
	if err != nil {
		t.Fatalf("CPU04_REVISION_PREFLIGHT: invalid frozen request; behavior NOT_RUN: %v", err)
	}
	canonicalPlan, err := plan.Canonical()
	if err != nil {
		t.Fatalf("CPU04_REVISION_PREFLIGHT: invalid canonical plan; behavior NOT_RUN: %v", err)
	}
	planHash, err := plan.Digest()
	if err != nil {
		t.Fatalf("CPU04_REVISION_PREFLIGHT: invalid plan digest; behavior NOT_RUN: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admissionPool, dispatchPool := openPool(), openPool()
	admissions, dispatches := execution.New(admissionPool), submission.New(dispatchPool)
	t.Log("CPU04_REVISION_PREFLIGHT PASS: real restricted PostgreSQL, versioned isolated schema, independent pools and valid frozen request")

	accepted, err := admissions.Accept(ctx, request.Admission)
	if err != nil || accepted.Replayed || accepted.Close != nil {
		t.Fatalf("CPU04_REVISION_FACTS: first Admission was not a new committed fact: %v", err)
	}
	assertSameDispatchAdmission(t, accepted.Admission, request.Admission)
	assertOwnerRevision(t, "first Admission", accepted.OwnerRevision, 1)

	reserved, err := dispatches.Reserve(ctx, request)
	if err != nil || reserved.SendPermit == nil {
		t.Fatalf("CPU04_REVISION_FACTS: first reservation lacks its sole send permit: %v", err)
	}
	assertFrozenDispatch(t, reserved.Dispatch, request, canonicalPlan, planHash)
	if reserved.Dispatch.UncertainAt != nil || reserved.Dispatch.NotSentAt != nil || len(reserved.Dispatch.ConfirmedRuns) != 0 {
		t.Fatal("CPU04_REVISION_FACTS: first reservation invented an observation")
	}
	permit := *reserved.SendPermit
	if permit.TenantID != request.Admission.TenantID || permit.ExecutionID != request.Admission.ExecutionID ||
		permit.AttemptID != reserved.Dispatch.AttemptID || permit.PlanHash != planHash {
		t.Fatal("CPU04_REVISION_FACTS: send permit differs from the original reservation")
	}
	assertOwnerRevision(t, "first reservation", reserved.Dispatch.OwnerRevision, 2)

	// Each expected version follows one committed public operation. Identity
	// allocation and parent/child SQL statements are not separate events here.
	wantDispatch := reserved.Dispatch
	uncertainAt := reserved.Dispatch.ReservedAt.Add(time.Microsecond)
	wantDispatch.State, wantDispatch.UncertainAt = biz.PipelineDispatchUncertain, &uncertainAt
	uncertain, err := dispatches.MarkSubmissionUncertain(ctx, permit, uncertainAt)
	if err != nil {
		t.Fatalf("CPU04_REVISION_FACTS: first uncertainty could not be recorded: %v", err)
	}
	assertRevisionDispatchFacts(t, uncertain, wantDispatch)
	assertOwnerRevision(t, "first uncertainty", uncertain.OwnerRevision, 3)

	notSentAt := reserved.Dispatch.ReservedAt.Add(2 * time.Microsecond)
	wantDispatch.NotSentAt = &notSentAt
	notSent, err := dispatches.MarkSubmissionNotSent(ctx, permit, notSentAt)
	if err != nil {
		t.Fatalf("CPU04_REVISION_FACTS: first no-send observation could not be recorded: %v", err)
	}
	// The stronger summary remains UNCERTAIN, but retaining this first time
	// is a new committed fact and must advance the aggregate revision.
	assertRevisionDispatchFacts(t, notSent, wantDispatch)
	assertOwnerRevision(t, "first no-send after uncertainty", notSent.OwnerRevision, 4)

	firstRunAt := reserved.Dispatch.ReservedAt.Add(3 * time.Microsecond)
	firstRun := confirmedObservation(confirmedRunID)
	wantDispatch.State = biz.PipelineDispatchConfirmed
	wantDispatch.ConfirmedRuns = []biz.PipelineConfirmedRun{{RunID: firstRun.RunID, FirstObservedAt: firstRunAt}}
	confirmed, err := dispatches.RecordSubmissionConfirmed(ctx, permit, firstRun, firstRunAt)
	if err != nil || confirmed.ConflictingRuns {
		t.Fatalf("CPU04_REVISION_FACTS: first Run was rejected or reported as conflicting: %v", err)
	}
	assertRevisionDispatchFacts(t, confirmed.Dispatch, wantDispatch)
	assertOwnerRevision(t, "first confirmed Run and state", confirmed.Dispatch.OwnerRevision, 5)

	secondRunAt := reserved.Dispatch.ReservedAt.Add(4 * time.Microsecond)
	secondRun := confirmedObservation("66666666-7777-4888-8999-aaaaaaaaaaaa")
	wantDispatch.ConfirmedRuns = append(wantDispatch.ConfirmedRuns, biz.PipelineConfirmedRun{RunID: secondRun.RunID, FirstObservedAt: secondRunAt})
	confirmed, err = dispatches.RecordSubmissionConfirmed(ctx, permit, secondRun, secondRunAt)
	if err != nil || !confirmed.ConflictingRuns {
		t.Fatalf("CPU04_REVISION_FACTS: second distinct Run was not retained as a conflict: %v", err)
	}
	assertRevisionDispatchFacts(t, confirmed.Dispatch, wantDispatch)
	assertOwnerRevision(t, "second confirmed Run without state change", confirmed.Dispatch.OwnerRevision, 6)

	firstClose := dispatchCloseIntent(request)
	wantFirstClose := biz.CloseRecord{CloseIntent: firstClose, Generation: 1, State: biz.CloseStateClosing}
	closed, err := admissions.ApplyCloseIntent(ctx, firstClose)
	if err != nil || closed.Replayed || !reflect.DeepEqual(closed.CloseRecord, wantFirstClose) {
		t.Fatalf("CPU04_REVISION_FACTS: first close changed its source or owner fence: %v", err)
	}
	assertOwnerRevision(t, "first source close", closed.OwnerRevision, 7)

	secondClose := firstClose
	secondClose.SourceGeneration = 2
	secondClose.RequestedAt = firstClose.RequestedAt.Add(time.Microsecond)
	wantSecondClose := biz.CloseRecord{CloseIntent: secondClose, Generation: 2, State: biz.CloseStateClosing}
	closed, err = admissions.ApplyCloseIntent(ctx, secondClose)
	if err != nil || closed.Replayed || !reflect.DeepEqual(closed.CloseRecord, wantSecondClose) {
		t.Fatalf("CPU04_REVISION_FACTS: second source close was lost or confused with the first: %v", err)
	}
	assertOwnerRevision(t, "second source close while already closing", closed.OwnerRevision, 8)

	// Replays report the current aggregate version, not their original receipt
	// version. Their original payloads and first observation times stay fixed.
	replayedAdmission, err := admissions.Accept(ctx, request.Admission)
	if err != nil || !replayedAdmission.Replayed || !reflect.DeepEqual(replayedAdmission.Close, &wantSecondClose) {
		t.Fatalf("CPU04_REVISION_FACTS: Admission replay lost its original fact or latest close: %v", err)
	}
	assertSameDispatchAdmission(t, replayedAdmission.Admission, request.Admission)
	assertOwnerRevision(t, "Admission replay", replayedAdmission.OwnerRevision, 8)

	replayedReservation, err := dispatches.Reserve(ctx, request)
	if err != nil || replayedReservation.SendPermit != nil {
		t.Fatalf("CPU04_REVISION_FACTS: reservation replay failed or granted another send permit: %v", err)
	}
	assertRevisionDispatchFacts(t, replayedReservation.Dispatch, wantDispatch)
	assertOwnerRevision(t, "reservation replay", replayedReservation.Dispatch.OwnerRevision, 8)

	replayAt := secondRunAt.Add(10 * time.Microsecond)
	uncertain, err = dispatches.MarkSubmissionUncertain(ctx, permit, replayAt)
	if err != nil {
		t.Fatalf("CPU04_REVISION_FACTS: late uncertainty replay failed: %v", err)
	}
	assertRevisionDispatchFacts(t, uncertain, wantDispatch)
	assertOwnerRevision(t, "uncertainty replay after confirmation", uncertain.OwnerRevision, 8)
	notSent, err = dispatches.MarkSubmissionNotSent(ctx, permit, replayAt)
	if err != nil {
		t.Fatalf("CPU04_REVISION_FACTS: late no-send replay failed: %v", err)
	}
	assertRevisionDispatchFacts(t, notSent, wantDispatch)
	assertOwnerRevision(t, "no-send replay", notSent.OwnerRevision, 8)

	for _, observation := range []biz.PipelineSubmissionObservation{firstRun, secondRun} {
		replayedRun, err := dispatches.RecordSubmissionConfirmed(ctx, permit, observation, replayAt)
		if err != nil || !replayedRun.ConflictingRuns {
			t.Fatalf("CPU04_REVISION_FACTS: Run replay lost the retained conflict: %v", err)
		}
		assertRevisionDispatchFacts(t, replayedRun.Dispatch, wantDispatch)
		assertOwnerRevision(t, "Run replay "+observation.RunID, replayedRun.Dispatch.OwnerRevision, 8)
	}
	for _, original := range []biz.CloseRecord{wantFirstClose, wantSecondClose} {
		replayedClose, err := admissions.ApplyCloseIntent(ctx, original.CloseIntent)
		if err != nil || !replayedClose.Replayed || !reflect.DeepEqual(replayedClose.CloseRecord, original) {
			t.Fatalf("CPU04_REVISION_FACTS: source close replay changed its own original owner fence: %v", err)
		}
		assertOwnerRevision(t, "source close replay", replayedClose.OwnerRevision, 8)
	}

	admissionPool.Close()
	dispatchPool.Close()
	reconnectedPool := openPool()
	reconnectedAdmission, err := execution.New(reconnectedPool).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || !reflect.DeepEqual(reconnectedAdmission.Close, &wantSecondClose) {
		t.Fatalf("CPU04_REVISION_FACTS: reconnect lost the Admission or latest close: %v", err)
	}
	assertSameDispatchAdmission(t, reconnectedAdmission.Admission, request.Admission)
	assertOwnerRevision(t, "reconnected Execution.Get", reconnectedAdmission.OwnerRevision, 8)
	reconnectedDispatch, err := submission.New(reconnectedPool).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil {
		t.Fatalf("CPU04_REVISION_FACTS: reconnect lost the original dispatch: %v", err)
	}
	assertRevisionDispatchFacts(t, reconnectedDispatch, wantDispatch)
	assertOwnerRevision(t, "reconnected Submission.Get", reconnectedDispatch.OwnerRevision, 8)
}

func assertOwnerRevision(t *testing.T, operation string, got, want uint64) {
	t.Helper()
	if got != want {
		// Continue so the RED identifies every existing writer returning the
		// missing version, rather than stopping after the first Admission.
		t.Errorf("CPU04_OWNER_REVISION: %s returned revision %d, want %d", operation, got, want)
	}
}

func assertRevisionDispatchFacts(t *testing.T, got, want biz.PipelineDispatch) {
	t.Helper()
	// Revision has its own independent assertion at every call. Compare all
	// remaining facts without aborting the sequence on the expected RED alone.
	got.OwnerRevision, want.OwnerRevision = 0, 0
	if !reflect.DeepEqual(got, want) {
		t.Fatal("CPU04_REVISION_FACTS: original attempt, plan, state or first observations changed")
	}
}

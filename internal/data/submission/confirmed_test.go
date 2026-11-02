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

func TestConfirmedRunSurvivesReconnectAfterCloseWithoutAnotherPermit(t *testing.T) {
	openPool := postgres.Prepare(t)
	request := validDispatchRequest(t, openPool())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admissionPool := openPool()
	admissions := execution.New(admissionPool)
	if _, err := admissions.Accept(ctx, request.Admission); err != nil {
		t.Fatalf("real Admission fixture failed; confirmed behavior NOT_RUN: %v", err)
	}
	writerPool := openPool()
	repository := submission.New(writerPool)
	first, err := repository.Reserve(ctx, request)
	if err != nil || first.SendPermit == nil {
		t.Fatalf("real reservation fixture failed; confirmed behavior NOT_RUN: %v", err)
	}
	if first.Dispatch.State != biz.PipelineDispatchSubmitting || first.Dispatch.UncertainAt != nil || len(first.Dispatch.ConfirmedRuns) != 0 {
		t.Fatal("real reservation fixture is not the original SUBMITTING fact; confirmed behavior NOT_RUN")
	}
	uncertainAt := first.Dispatch.ReservedAt.Add(time.Microsecond)
	uncertain, err := repository.MarkSubmissionUncertain(ctx, *first.SendPermit, uncertainAt)
	wantUncertain := first.Dispatch
	wantUncertain.OwnerRevision = 3
	wantUncertain.State = biz.PipelineDispatchUncertain
	wantUncertain.UncertainAt = &uncertainAt
	if err != nil || !reflect.DeepEqual(uncertain, wantUncertain) {
		t.Fatalf("real uncertainty fixture failed; confirmed behavior NOT_RUN: %v", err)
	}
	closed, err := admissions.ApplyCloseIntent(ctx, dispatchCloseIntent(request))
	if err != nil || closed.Replayed || closed.State != biz.CloseStateClosing {
		t.Fatalf("real close fixture failed; confirmed behavior NOT_RUN: %v", err)
	}
	t.Log("CPU07_CONFIRMED_PREFLIGHT: real Admission, one reservation, uncertainty and close committed")

	// This is a synthetic internal observation, not evidence that an HTTP call
	// occurred or a KFP/Pod identity was verified. Persistence cannot grant Run
	// authority, training permission or completion of the existing close.
	observation := biz.PipelineSubmissionObservation{
		State: biz.PipelineSubmissionConfirmed,
		RunID: "55555555-6666-4777-8888-999999999999",
	}
	observedAt := uncertainAt.Add(time.Microsecond)
	expected := wantUncertain
	expected.OwnerRevision = 5 // Uncertainty, then close, then the first Run.
	expected.State = biz.PipelineDispatchConfirmed
	expected.ConfirmedRuns = []biz.PipelineConfirmedRun{{RunID: observation.RunID, FirstObservedAt: observedAt}}
	receipt, err := repository.RecordSubmissionConfirmed(ctx, *first.SendPermit, observation, observedAt)
	if err != nil {
		t.Fatalf("CPU07_CONFIRMED_BEHAVIOR: original attempt's late confirmed Run was not durably recorded: %v", err)
	}
	if receipt.ConflictingRuns || !reflect.DeepEqual(receipt.Dispatch, expected) {
		t.Fatal("CPU07_CONFIRMED_BEHAVIOR: confirmation lost the handle or changed the original attempt, plan, reservation or uncertainty")
	}

	// A third independent pool must see the receipt's facts before the writer
	// is closed; visibility here verifies that success follows commit.
	readerPool := openPool()
	visible, err := submission.New(readerPool).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || !reflect.DeepEqual(visible, expected) {
		t.Fatalf("CPU07_CONFIRMED_BEHAVIOR: confirmation receipt preceded independently readable facts: %v", err)
	}
	readerPool.Close()
	writerPool.Close()
	admissionPool.Close()

	reconnected := submission.New(openPool())
	stored, err := reconnected.Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || !reflect.DeepEqual(stored, expected) {
		t.Fatalf("CPU07_CONFIRMED_BEHAVIOR: confirmed Run was lost after reconnect: %v", err)
	}
	assertDispatchReplay(t, ctx, reconnected, request, expected)
	for _, replayTime := range []time.Time{observedAt, observedAt.Add(time.Microsecond)} {
		replayed, err := reconnected.RecordSubmissionConfirmed(ctx, *first.SendPermit, observation, replayTime)
		if err != nil || replayed.ConflictingRuns || !reflect.DeepEqual(replayed.Dispatch, expected) {
			t.Fatalf("CPU07_CONFIRMED_BEHAVIOR: repeated confirmation replaced or refreshed the original Run observation: %v", err)
		}
	}
	assertDispatchReplay(t, ctx, submission.New(openPool()), request, expected)
	admitted, err := execution.New(openPool()).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil {
		t.Fatalf("read original Admission after confirmation: %v", err)
	}
	assertSameDispatchAdmission(t, admitted.Admission, request.Admission)
	if admitted.Close == nil || !reflect.DeepEqual(*admitted.Close, closed.CloseRecord) {
		t.Fatal("CPU07_CONFIRMED_BEHAVIOR: late confirmation erased or reopened the committed close")
	}
}

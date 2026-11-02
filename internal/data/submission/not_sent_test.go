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

func TestNotSentRetainsLateConfirmedRunAfterCloseWithoutAnotherPermit(t *testing.T) {
	openPool := postgres.Prepare(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := validDispatchRequest(t, openPool())
	writerPool := openPool()
	executions := execution.New(writerPool)
	if _, err := executions.Accept(ctx, request.Admission); err != nil {
		t.Fatalf("CPU07_NOT_SENT_LATE_PREFLIGHT: Admission failed; behavior NOT_RUN: %v", err)
	}
	repository := submission.New(writerPool)
	reservation, err := repository.Reserve(ctx, request)
	if err != nil || reservation.SendPermit == nil {
		t.Fatalf("CPU07_NOT_SENT_LATE_PREFLIGHT: reservation failed; behavior NOT_RUN: %v", err)
	}
	permit := *reservation.SendPermit
	observedAt, err := repository.SubmissionObservationTime(ctx, permit)
	if err != nil {
		t.Fatalf("CPU07_NOT_SENT_LATE_PREFLIGHT: database observation time unavailable; behavior NOT_RUN: %v", err)
	}
	t.Log("CPU07_NOT_SENT_LATE_PREFLIGHT PASS: real Admission, original permit and database timestamp")
	notSent, err := repository.MarkSubmissionNotSent(ctx, permit, observedAt)
	if err != nil || notSent.State != biz.PipelineDispatchNotSent || notSent.NotSentAt == nil || !notSent.NotSentAt.Equal(observedAt) {
		t.Fatalf("CPU07_NOT_SENT_LATE_BEHAVIOR: original no-send observation was not persisted: %v", err)
	}
	closed, err := executions.ApplyCloseIntent(ctx, biz.CloseIntent{
		TenantID: request.Admission.TenantID, ExecutionID: request.Admission.ExecutionID, OperationID: request.Admission.OperationID, SpecHash: request.Admission.SpecHash,
		SourceGeneration: 1, Reason: biz.CloseReasonUserStop, RequestedActor: "governance:user:42", RequestedAt: request.Admission.AcceptedAt.Add(time.Microsecond),
	})
	if err != nil {
		t.Fatalf("CPU07_NOT_SENT_LATE_BEHAVIOR: close failed: %v", err)
	}
	// This is a trusted internal contradictory observation, not a second POST
	// or proof of a real Run. The original permit is used only for association.
	confirmed, err := repository.RecordSubmissionConfirmed(ctx, permit, confirmedObservation("55555555-6666-4777-8888-999999999999"), observedAt.Add(time.Microsecond))
	if err != nil || confirmed.ConflictingRuns || confirmed.Dispatch.State != biz.PipelineDispatchConfirmed || confirmed.Dispatch.NotSentAt == nil || !confirmed.Dispatch.NotSentAt.Equal(observedAt) ||
		confirmed.Dispatch.UncertainAt != nil || len(confirmed.Dispatch.ConfirmedRuns) != 1 || confirmed.Dispatch.ConfirmedRuns[0].RunID != "55555555-6666-4777-8888-999999999999" {
		t.Fatalf("CPU07_NOT_SENT_LATE_BEHAVIOR: NOT_SENT/close discarded a late confirmed Run: %v", err)
	}
	repeated, err := repository.MarkSubmissionNotSent(ctx, permit, observedAt.Add(2*time.Microsecond))
	if err != nil || !reflect.DeepEqual(repeated, confirmed.Dispatch) {
		t.Fatalf("CPU07_NOT_SENT_LATE_BEHAVIOR: replay refreshed no-send time or downgraded confirmation: %v", err)
	}
	writerPool.Close()
	reopenedPool := openPool()
	reopened := submission.New(reopenedPool)
	stored, err := reopened.Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || !reflect.DeepEqual(stored, confirmed.Dispatch) {
		t.Fatalf("CPU07_NOT_SENT_LATE_BEHAVIOR: reconnect lost the original no-send fact or late Run: %v", err)
	}
	replay, err := reopened.Reserve(ctx, request)
	if err != nil || replay.SendPermit != nil || !reflect.DeepEqual(replay.Dispatch, stored) {
		t.Fatalf("CPU07_NOT_SENT_LATE_BEHAVIOR: stronger observation granted another send or changed facts: %v", err)
	}
	admitted, err := execution.New(reopenedPool).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || !reflect.DeepEqual(admitted.Admission, request.Admission) || admitted.Close == nil || !reflect.DeepEqual(*admitted.Close, closed.CloseRecord) {
		t.Fatal("CPU07_NOT_SENT_LATE_BEHAVIOR: late observations changed original Admission/close")
	}
}

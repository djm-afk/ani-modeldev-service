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

func TestSubmissionUncertaintySurvivesReconnectWithoutAnotherPermit(t *testing.T) {
	for _, closeBeforeObservation := range []bool{false, true} {
		name := "original open reservation"
		if closeBeforeObservation {
			name = "late observation after committed close"
		}
		t.Run(name, func(t *testing.T) {
			openPool := postgres.Prepare(t)
			request := validDispatchRequest(t, openPool())
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			admissions := execution.New(openPool())
			acceptDispatchAdmission(t, ctx, admissions, request)
			writerPool := openPool()
			repository := submission.New(writerPool)
			first, err := repository.Reserve(ctx, request)
			if err != nil || first.SendPermit == nil {
				t.Fatalf("real reservation fixture failed; uncertainty behavior NOT_RUN: %v", err)
			}
			if first.Dispatch.State != biz.PipelineDispatchSubmitting || first.Dispatch.UncertainAt != nil {
				t.Fatal("real reservation fixture is not the original SUBMITTING fact; uncertainty behavior NOT_RUN")
			}
			var closed biz.CloseRecord
			if closeBeforeObservation {
				receipt, closeErr := admissions.ApplyCloseIntent(ctx, dispatchCloseIntent(request))
				if closeErr != nil {
					t.Fatalf("real close fixture failed; late uncertainty behavior NOT_RUN: %v", closeErr)
				}
				closed = receipt.CloseRecord
			}
			// Observation input is supplied by the original sender, separately
			// from reservation/commit time, and has database timestamp precision.
			observedAt := first.Dispatch.ReservedAt.Add(time.Microsecond)
			expected := first.Dispatch
			expected.OwnerRevision = 3
			if closeBeforeObservation {
				expected.OwnerRevision = 4
			}
			expected.State = biz.PipelineDispatchUncertain
			expected.UncertainAt = &observedAt
			marked, err := repository.MarkSubmissionUncertain(ctx, *first.SendPermit, observedAt)
			if err != nil {
				t.Fatalf("CPU07_UNCERTAIN_BEHAVIOR: original submission uncertainty was not durably recorded: %v", err)
			}
			if !reflect.DeepEqual(marked, expected) {
				t.Fatal("CPU07_UNCERTAIN_BEHAVIOR: uncertainty changed the original attempt, frozen plan or reservation time")
			}
			writerPool.Close()
			reconnected := submission.New(openPool())
			stored, err := reconnected.Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
			if err != nil || !reflect.DeepEqual(stored, expected) {
				t.Fatalf("CPU07_UNCERTAIN_BEHAVIOR: uncertainty was lost after reconnect: %v", err)
			}
			assertDispatchReplay(t, ctx, reconnected, request, expected)
			for _, replayTime := range []time.Time{observedAt, observedAt.Add(time.Microsecond)} {
				replayed, err := reconnected.MarkSubmissionUncertain(ctx, *first.SendPermit, replayTime)
				if err != nil || !reflect.DeepEqual(replayed, expected) {
					t.Fatalf("CPU07_UNCERTAIN_BEHAVIOR: replay refreshed or replaced the first uncertainty observation: %v", err)
				}
			}
			assertDispatchReplay(t, ctx, submission.New(openPool()), request, expected)
			admitted, err := execution.New(openPool()).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
			if err != nil {
				t.Fatalf("read original Admission after uncertainty: %v", err)
			}
			assertSameDispatchAdmission(t, admitted.Admission, request.Admission)
			if closeBeforeObservation {
				if admitted.Close == nil || !reflect.DeepEqual(*admitted.Close, closed) {
					t.Fatal("CPU07_UNCERTAIN_BEHAVIOR: late uncertainty erased or reopened the committed close")
				}
			} else if admitted.Close != nil {
				t.Fatal("CPU07_UNCERTAIN_BEHAVIOR: uncertainty invented a close fact")
			}
		})
	}
}

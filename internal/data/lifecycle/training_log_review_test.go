package lifecycle_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
)

func TestTrainingLogReviewKeepsOriginalStopUnclosedAndRetryable(t *testing.T) {
	open, admission, authority, workspace := runtimeFixture(t)
	pool := open()
	defer pool.Close()
	r, ctx := lifecycle.New(pool), context.Background()
	if _, _, err := r.RecordPrepared(ctx, authority, workspace); err != nil {
		t.Fatal(err)
	}
	reserved, err := reserveTrainingV21(t, r, ctx, authority)
	if err != nil {
		t.Fatal(err)
	}
	handle := biz.TrainingHandle{NamespaceUID: workspace.NamespaceUID, PVCUID: workspace.PVCUID, TrainJobUID: "log-review-original-training"}
	if _, err := r.RecordTrainingHandle(ctx, authority, handle); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.New(pool).ApplyCloseIntent(ctx, biz.CloseIntent{TenantID: admission.TenantID, ExecutionID: admission.ExecutionID, OperationID: admission.OperationID, SpecHash: admission.SpecHash, SourceGeneration: 1, Reason: biz.CloseReasonUserStop, RequestedAt: time.Now().UTC().Truncate(time.Microsecond), RequestedActor: admission.Actor}); err != nil {
		t.Fatal(err)
	}
	closing, err := r.GetRuntime(ctx, admission.TenantID, admission.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	review, err := r.MarkOwnerCloseReview(ctx, admission.TenantID, admission.ExecutionID, "TRAINING_LOGS_UNAVAILABLE")
	if err != nil {
		t.Fatalf("LOG_LOSS_REVIEW_NOT_DURABLE: %v", err)
	}
	if review.CloseReviewReason != "TRAINING_LOGS_UNAVAILABLE" || review.CloseGeneration != closing.CloseGeneration || !review.CloseRequestedAt.Equal(closing.CloseRequestedAt) || (closing.CloseOperationID != "" && review.CloseOperationID != closing.CloseOperationID) || review.CloseOperationID == "" || review.CloseOperationID == admission.OperationID || review.TrainingHandle == nil || *review.TrainingHandle != handle || review.ClosedAt != nil || review.ComputeCleanup != nil {
		t.Fatalf("log review invented closure or changed original identity: %+v", review)
	}
	again, err := r.MarkOwnerCloseReview(ctx, admission.TenantID, admission.ExecutionID, "TRAINING_LOGS_UNAVAILABLE")
	if err != nil || again.CloseOperationID != review.CloseOperationID || again.OwnerRevision != review.OwnerRevision {
		t.Fatal("same log review renewed original close identity/revision", err)
	}
	visible, err := execution.New(pool).GetQueryRecord(ctx, admission.TenantID, admission.ExecutionID)
	if err != nil || visible.Execution.States.Close != biz.CloseStateNeedsReview {
		t.Fatalf("actual query did not expose NEEDS_REVIEW: %+v %v", visible.Execution.States, err)
	}
	binding := biz.PipelineDispatchBinding{TenantID: admission.TenantID, Environment: admission.Snapshot.Environment, Owner: biz.PipelineOwnerConfiguration{Reference: "runtime-fixture-owner", RevisionSHA256: strings.Repeat("a", 64), PipelineRoot: "s3://fixture-kfp-artifacts/managed-root"}}
	pending, err := r.ListPendingClosures(ctx, binding, "", 10)
	if err != nil || len(pending) != 1 || pending[0] != admission.ExecutionID {
		t.Fatalf("review silently disabled normal owner retries: %v %v", pending, err)
	}
	if _, err := r.ConfirmRuntimeClosed(ctx, authority, closing.CloseGeneration, biz.TrainingRuntimeObservation{}, biz.ManagedCloseEvidence{}); err == nil {
		t.Fatal("log review authorized missing writer/log evidence")
	}
	if _, err := r.BeginComputeCleanup(ctx, authority, closing.CloseGeneration); err == nil {
		t.Fatal("log review authorized cleanup before CLOSED")
	}
	if _, err := r.MarkOwnerCloseReview(ctx, admission.TenantID, admission.ExecutionID, "TRAINING_LOG_LOSS_ACKNOWLEDGED"); err == nil {
		t.Fatal("new implicit log-loss acknowledgement accepted")
	}
	if reserved.State.Training == nil {
		t.Fatal("original training plan missing")
	}
}

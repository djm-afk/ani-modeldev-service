package lifecycle_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestStopAfterNaturalClosedReturnsObservedCloseWithoutReplacingFacts(t *testing.T) {
	open, admission, authority, workspace := runtimeFixture(t)
	ctx := context.Background()
	pool := open()
	closed := naturalClosedFixture(t, lifecycle.New(pool), admission, authority, workspace)
	request := closedStopRequest(admission, 1)
	delivery := service.WithVerifiedGovernanceDelivery(ctx, service.GovernanceDelivery{TenantID: admission.TenantID, Actor: admission.Actor})
	response, err := service.NewCommand(execution.New(open())).ApplyCloseIntent(delivery, request)
	if err != nil || response.GetCloseState() != modeldevv1.CloseState_CLOSE_STATE_CLOSED || response.GetCloseGeneration() != 1 || response.GetReplayed() || !response.GetDurablyRecorded() {
		t.Fatalf("STOP_AFTER_NATURAL_CLOSED: first Stop must report the proven original CLOSED generation 1: %v; %v", response, err)
	}
	visible, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || visible.CloseGeneration != 1 || visible.CloseReason != "NATURAL_TERMINAL" || !reflect.DeepEqual(visible.ClosedAt, closed.ClosedAt) || !reflect.DeepEqual(visible.CloseEvidence, closed.CloseEvidence) || !reflect.DeepEqual(visible.Publication, closed.Publication) {
		t.Fatalf("Stop replaced immutable terminal or publication facts: %+v; %v", visible, err)
	}
	replay, err := service.NewCommand(execution.New(open())).ApplyCloseIntent(delivery, request)
	if err != nil || replay.GetCloseState() != modeldevv1.CloseState_CLOSE_STATE_CLOSED || replay.GetCloseGeneration() != 1 || !replay.GetReplayed() || !replay.GetDurablyRecorded() || !reflect.DeepEqual(replay.GetIdentity(), response.GetIdentity()) {
		t.Fatalf("the exact Stop replay must report the same proven closure and command identity: %v; %v", replay, err)
	}
}

func TestExistingStopFenceAfterNaturalClosedPreservesReadAndCleanupGeneration(t *testing.T) {
	open, admission, authority, workspace := runtimeFixture(t)
	ctx := context.Background()
	pool := open()
	closed := naturalClosedFixture(t, lifecycle.New(pool), admission, authority, workspace)
	// This internal boundary observation retains the original reserved attempt;
	// it neither creates a Run nor grants another submission permit.
	permit := biz.PipelineSendPermit{TenantID: authority.TenantID, ExecutionID: authority.ExecutionID, AttemptID: authority.AttemptID, PlanHash: authority.PlanHash}
	if _, err := submission.New(pool).RecordSubmissionConfirmed(ctx, permit, biz.PipelineSubmissionObservation{State: biz.PipelineSubmissionConfirmed, RunID: authority.RunID}, time.Now().UTC().Truncate(time.Microsecond)); err != nil {
		t.Fatal(err)
	}
	first := closeIntentForRequest(closedStopRequest(admission, 1))
	committed, err := execution.New(pool).ApplyCloseIntent(ctx, first)
	if err != nil || committed.Generation != 2 || committed.Replayed {
		t.Fatal("original source intent must commit its independent owner fence 2", err)
	}
	// The durable database now has exactly the pre-fix LIVE shape: desired
	// identity/source fence 2 and observed natural runtime close 1. Reconnect,
	// rather than rewriting either row, must recover that observed closure.
	record, err := execution.New(open()).GetOperationRecord(ctx, admission.TenantID, admission.ExecutionID)
	if err != nil || record.Execution.States.Close != biz.CloseStateClosed || record.Runtime.CloseGeneration != 1 || record.Runtime.CloseReason != "NATURAL_TERMINAL" || !reflect.DeepEqual(record.Runtime.ClosedAt, closed.ClosedAt) || !reflect.DeepEqual(record.Runtime.CloseEvidence, closed.CloseEvidence) || !reflect.DeepEqual(record.Runtime.Publication, closed.Publication) {
		t.Fatalf("EXISTING_DESIRED_OBSERVED_SPLIT: reconnect replaced original closure: %+v; %v", record.Runtime, err)
	}
	if record.Execution.Close == nil || record.Execution.Close.Generation != 2 || !reflect.DeepEqual(record.Execution.Close.CloseIntent, first) {
		t.Fatal("observed closure must not erase or rewrite the Stop audit fence")
	}
	plan, err := biz.CleanupPlanFor(record)
	if err != nil || plan.CloseGeneration != 1 || plan.PublicationID != closed.Publication.ID {
		t.Fatal("cleanup must retain the proven close and publication, not the later desired fence", err)
	}
	second := first
	second.SourceGeneration, second.RequestedAt = 2, first.RequestedAt.Add(time.Microsecond)
	next, err := execution.New(open()).ApplyCloseIntent(ctx, second)
	if err != nil || next.Generation != 3 || next.ClosedGeneration != 1 || next.Replayed {
		t.Fatalf("a second source must retain unique owner fence 3 and observed close 1: %+v; %v", next, err)
	}
	replay, err := execution.New(open()).ApplyCloseIntent(ctx, first)
	if err != nil || replay.Generation != 2 || replay.ClosedGeneration != 1 || !replay.Replayed || !reflect.DeepEqual(replay.CloseIntent, first) {
		t.Fatal("earlier source replay must retain its original fence and the original observed close", err)
	}
}

func TestConcurrentStopsAfterNaturalClosedKeepDistinctAuditsAndOneObservedClose(t *testing.T) {
	open, admission, authority, workspace := runtimeFixture(t)
	ctx := context.Background()
	closed := naturalClosedFixture(t, lifecycle.New(open()), admission, authority, workspace)
	commands := []biz.CloseIntent{closeIntentForRequest(closedStopRequest(admission, 1)), closeIntentForRequest(closedStopRequest(admission, 2))}
	type outcome struct {
		receipt biz.CloseReceipt
		err     error
	}
	start, results := make(chan struct{}), make(chan outcome, 2)
	for _, command := range commands {
		repository := execution.New(open())
		go func(intent biz.CloseIntent) {
			<-start
			receipt, err := repository.ApplyCloseIntent(ctx, intent)
			results <- outcome{receipt, err}
		}(command)
	}
	close(start)
	fences := make(map[uint64]bool)
	for range commands {
		result := <-results
		if result.err != nil || result.receipt.ClosedGeneration != 1 || result.receipt.Replayed || fences[result.receipt.Generation] {
			t.Fatalf("concurrent Stop lost its unique audit or terminal proof: %+v; %v", result.receipt, result.err)
		}
		fences[result.receipt.Generation] = true
	}
	visible, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || !fences[2] || !fences[3] || visible.CloseGeneration != 1 || !reflect.DeepEqual(visible.CloseEvidence, closed.CloseEvidence) || !reflect.DeepEqual(visible.Publication, closed.Publication) {
		t.Fatal("concurrent Stops changed the terminal outcome or publication", err)
	}
}

func TestStopCannotTreatInvalidPersistedEvidenceAsAlreadyClosed(t *testing.T) {
	open, admission, authority, workspace := runtimeFixture(t)
	ctx := context.Background()
	pool := open()
	naturalClosedFixture(t, lifecycle.New(pool), admission, authority, workspace)
	// Deliberately corrupt only the isolated PostgreSQL test fixture at the
	// persistence boundary. LIVE runtime rows are never rewritten by this fix.
	if _, err := pool.Exec(ctx, "UPDATE modeldev_execution_runtimes SET facts=jsonb_set(facts,'{CloseEvidence,Resources,0,Terminal}','false'::jsonb) WHERE tenant_id=$1::uuid AND execution_id=$2::uuid", admission.TenantID, admission.ExecutionID); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.New(open()).GetRuntime(ctx, admission.TenantID, admission.ExecutionID); !errors.Is(err, biz.ErrPersistence) {
		t.Fatal("a ClosedAt timestamp with an unproved writer must not become a closed read", err)
	}
	receipt, err := execution.New(open()).ApplyCloseIntent(ctx, closeIntentForRequest(closedStopRequest(admission, 1)))
	if !errors.Is(err, biz.ErrPersistence) || !reflect.DeepEqual(receipt, biz.CloseReceipt{}) {
		t.Fatal("invalid terminal evidence must fail without a successful command receipt", err)
	}
}

func closeIntentForRequest(request *modeldevv1.ApplyCloseIntentRequest) biz.CloseIntent {
	return biz.CloseIntent{TenantID: request.ResourceTenantId, ExecutionID: request.Identity.ExecutionId, OperationID: request.Identity.OperationId, SpecHash: request.Identity.ExecutionSpecHash, SourceGeneration: request.IntentGeneration, Reason: biz.CloseReasonUserStop, RequestedAt: request.RequestedAt.AsTime(), RequestedActor: request.RequestedActorId}
}

func closedStopRequest(admission biz.Admission, generation uint64) *modeldevv1.ApplyCloseIntentRequest {
	return &modeldevv1.ApplyCloseIntentRequest{ResourceTenantId: admission.TenantID, Identity: &trainingv1.ExecutionIdentity{ExecutionId: admission.ExecutionID, OperationId: admission.OperationID, ExecutionSpecHash: admission.SpecHash}, IntentGeneration: generation, Reason: modeldevv1.CloseReason_CLOSE_REASON_USER_STOP, RequestedAt: timestamppb.New(time.Now().UTC().Truncate(time.Microsecond)), RequestedActorId: admission.Actor}
}

func naturalClosedFixture(t *testing.T, repository *lifecycle.Repository, admission biz.Admission, authority biz.RunAuthorityCandidate, workspace biz.WorkspaceBinding) biz.ExecutionRuntime {
	t.Helper()
	ctx := context.Background()
	if _, _, err := repository.RecordPrepared(ctx, authority, workspace); err != nil {
		t.Fatal(err)
	}
	reservation, err := reserveTrainingV21(t, repository, ctx, authority)
	if err != nil || !reservation.SendPermit {
		t.Fatal("original creation permit", err)
	}
	handle := biz.TrainingHandle{NamespaceUID: workspace.NamespaceUID, PVCUID: workspace.PVCUID, TrainJobUID: "training-closed-stop-uid"}
	if _, err := repository.RecordTrainingHandle(ctx, authority, handle); err != nil {
		t.Fatal(err)
	}
	zero := int32(0)
	observation := biz.TrainingRuntimeObservation{Handle: handle, Outcome: "SUCCEEDED", WritersAbsent: true, ObservedAt: time.Now().UTC(), Resources: []biz.RuntimeResource{
		{APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainJob", Namespace: workspace.NamespaceName, Name: reservation.State.Training.Name, UID: handle.TrainJobUID, APIObjectPresent: true, Terminal: true},
		{APIVersion: "jobset.x-k8s.io/v1alpha2", Kind: "JobSet", Namespace: workspace.NamespaceName, Name: "closed-training-set", UID: "closed-set-uid", OwnerUID: handle.TrainJobUID, APIObjectPresent: true, Terminal: true},
		{APIVersion: "v1", Kind: "Pod", Namespace: workspace.NamespaceName, Name: "closed-training-pod", UID: "closed-pod-uid", OwnerUID: "closed-set-uid", APIObjectPresent: true, Terminal: true, ExitCode: &zero},
	}}
	if _, err := repository.RecordTrainingObservation(ctx, authority, observation); err != nil {
		t.Fatal(err)
	}
	if _, _, err := recordPublicationV21(t, repository, ctx, authority, publicationFixture(t, admission, authority)); err != nil {
		t.Fatal(err)
	}
	closing, _, err := repository.RequestRuntimeClose(ctx, authority, "NATURAL_TERMINAL")
	if err != nil {
		t.Fatal(err)
	}
	evidence := biz.ManagedCloseEvidence{RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, ObservedAt: time.Now().UTC()}
	for _, step := range []string{"prepare", "train-wait", "collect", "publish"} {
		evidence.Resources = append(evidence.Resources, biz.RuntimeResource{APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: "closed-" + step, UID: "closed-" + step + "-uid", OwnerUID: authority.WorkflowUID, APIObjectPresent: true, Terminal: true, ExitCode: &zero})
	}
	closed, err := repository.ConfirmRuntimeClosed(ctx, authority, closing.CloseGeneration, observation, evidence)
	if err != nil || closed.ClosedAt == nil {
		t.Fatal("original terminal proof", err)
	}
	return closed
}

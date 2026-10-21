package lifecycle_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func preparedRuntimeV21(t *testing.T, stepSeconds uint64) (func() *pgxpool.Pool, biz.Admission, biz.RunAuthorityCandidate, biz.WorkspaceBinding) {
	t.Helper()
	open := postgres.Prepare(t)
	ctx := context.Background()
	s := conformance.SnapshotV1()
	intent := conformance.IntentV1()
	accepted := time.Now().UTC().Truncate(time.Microsecond)
	timing, err := (cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: 60, ExecutionTimeoutMaxSeconds: 120, StepTimeoutSeconds: stepSeconds}).Resolve(accepted, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Timing, s.DeadlineAt = timing, timing.ExecutionDeadlineAt
	s.RuntimePlan, err = biz.CompileRuntimePlan(s)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := s.Digest()
	if err != nil {
		t.Fatal(err)
	}
	i := s.IdentityRef
	a := biz.Admission{TenantID: i.TenantID, Actor: i.Actor, OperationID: i.OperationID, ExecutionID: i.ExecutionID, Intent: intent, IntentHash: i.IntentHash, Snapshot: s, SpecHash: hash, AcceptedAt: accepted}
	pool := open()
	if _, err := execution.New(pool).Accept(ctx, a); err != nil {
		t.Fatal("accept", err)
	}
	dispatch, err := submission.New(pool).Reserve(ctx, biz.PipelineDispatchRequest{Admission: a, Owner: biz.PipelineOwnerConfiguration{Reference: "runtime-v21-owner", RevisionSHA256: strings.Repeat("a", 64), PipelineRoot: "s3://fixture-kfp-artifacts/managed-root"}})
	if err != nil || dispatch.SendPermit == nil {
		t.Fatal("dispatch", err)
	}
	authority := biz.RunAuthorityCandidate{TenantID: a.TenantID, ExecutionID: a.ExecutionID, OperationID: a.OperationID, SpecHash: a.SpecHash, AttemptID: dispatch.Dispatch.AttemptID, PlanHash: dispatch.Dispatch.PlanHash, RunID: "55555555-6666-4777-8888-999999999999", NamespaceName: s.Environment.NamespaceName, NamespaceUID: s.Environment.NamespaceUID, WorkflowName: "runtime-v21-workflow", WorkflowUID: "cccccccc-dddd-4eee-8fff-111111111111"}
	if _, err := submission.New(pool).BindRunAuthority(ctx, authority); err != nil {
		t.Fatal("authority", err)
	}
	repository := lifecycle.New(pool)
	binding := biz.FinetuningStepBinding{Role: "prepare", SpecHash: a.SpecHash, RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, TaskID: "prepare-v21", PodName: "prepare-v21", PodUID: uuid.NewString()}
	if _, _, err := repository.ReserveManagedStep(ctx, authority, binding); err != nil {
		t.Fatal("prepare window", err)
	}
	workspace := biz.WorkspaceBinding{Mode: s.Workspace.Mode, NamespaceName: s.Environment.NamespaceName, NamespaceUID: s.Environment.NamespaceUID, PVCName: biz.ExpectedWorkspacePVCName(a.ExecutionID), PVCUID: "dddddddd-eeee-4fff-8111-222222222222", InputSubpath: s.Workspace.InputSubpath, TrainingSubpath: s.Workspace.TrainingSubpath, ReportsSubpath: s.Workspace.ReportsSubpath, PublicationSubpath: s.Workspace.PublicationSubpath, PreparedManifestSHA256: strings.Repeat("1", 64), PreparedManifestBytes: 256}
	if _, _, err := repository.RecordPrepared(ctx, authority, workspace); err != nil {
		t.Fatal("prepared", err)
	}
	return open, a, authority, workspace
}

func runtimeBindingV21(a biz.Admission, w biz.WorkspaceBinding) biz.RuntimeTemplateBinding {
	return biz.RuntimeTemplateBinding{NamespaceUID: w.NamespaceUID, Name: a.Snapshot.RuntimePlan.Name, UID: "eeeeeeee-ffff-4111-8222-333333333333", ObservedSpecSHA256: a.Snapshot.RuntimePlan.ExpectedSpecSHA256}
}

func TestRuntimeV21CrashAfterDispatchCommitBeforeHTTPKeepsFenceAndReview(t *testing.T) {
	open, a, authority, w := preparedRuntimeV21(t, 30)
	ctx := context.Background()
	first, err := lifecycle.New(open()).ReserveRuntime(ctx, authority)
	if err != nil || !first.SendPermit || len(first.State.RuntimeTemplate.Attempts) != 1 {
		t.Fatal("initial durable dispatch", err)
	}
	// No transport was called. A new process has no durable unsent receipt;
	// therefore even a genuine 404 cannot reconstruct the consumed permit.
	recovered, err := lifecycle.New(open()).ReserveRuntime(ctx, authority)
	if err != nil || recovered.SendPermit || len(recovered.State.RuntimeTemplate.Attempts) != 1 {
		t.Fatal("crash renewed permission", err)
	}
	review, err := lifecycle.New(open()).RecordRuntimeReview(ctx, authority)
	if err != nil || !strings.Contains(review.RuntimeTemplate.ReviewReason, "NEEDS_REVIEW") || review.RuntimeTemplate.NextAction == "" {
		t.Fatal("missing actionable unknown receipt", err)
	}
	closed, _, err := lifecycle.New(open()).RequestRuntimeClose(ctx, authority, "STEP_FAILED")
	if err != nil || closed.CloseGeneration == 0 {
		t.Fatal("original fence", err)
	}
	// A delayed original API receipt is retained for cleanup after the fence.
	bound, err := lifecycle.New(open()).RecordRuntimeBinding(ctx, authority, runtimeBindingV21(a, w))
	if err != nil || bound.RuntimeTemplate.Binding == nil {
		t.Fatal("late original UID lost", err)
	}
	if _, err := lifecycle.New(open()).ReserveTraining(ctx, authority); !errors.Is(err, biz.ErrPipelineDispatchBlocked) {
		t.Fatal("late receipt bypassed Stop", err)
	}
	if _, err := lifecycle.New(open()).ReserveRuntime(ctx, authority); err != nil {
		t.Fatal("bound observation replay", err)
	}
}

func TestRuntimeV21OriginalLostResponseBindsOnceWithoutAnotherPOSTPermit(t *testing.T) {
	open, a, authority, w := preparedRuntimeV21(t, 30)
	ctx := context.Background()
	first, err := lifecycle.New(open()).ReserveRuntime(ctx, authority)
	if err != nil || !first.SendPermit {
		t.Fatal(err)
	}
	if next, err := lifecycle.New(open()).ReserveRuntime(ctx, authority); err != nil || next.SendPermit {
		t.Fatal("lost response sent again", err)
	}
	binding := runtimeBindingV21(a, w)
	if _, err := lifecycle.New(open()).RecordRuntimeBinding(ctx, authority, binding); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.New(open()).RecordRuntimeBinding(ctx, authority, binding); err != nil {
		t.Fatal("original replay", err)
	}
	binding.UID = uuid.NewString()
	if _, err := lifecycle.New(open()).RecordRuntimeBinding(ctx, authority, binding); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("replacement UID adopted", err)
	}
	training, err := lifecycle.New(open()).ReserveTraining(ctx, authority)
	if err != nil || !training.SendPermit || training.State.Training.RuntimeBinding != runtimeBindingV21(a, w) || *training.State.Training.Timing != *first.State.TrainingWindow {
		t.Fatal("TRAIN did not retain original binding/window", err)
	}
	if replay, err := lifecycle.New(open()).ReserveTraining(ctx, authority); err != nil || replay.SendPermit {
		t.Fatal("retrained", err)
	}
}

func TestRuntimeV21OnlyOriginalPreCreateReceiptAllowsRetryAndStopStillWins(t *testing.T) {
	open, a, authority, w := preparedRuntimeV21(t, 30)
	ctx := context.Background()
	r := lifecycle.New(open())
	first, err := r.ReserveRuntime(ctx, authority)
	if err != nil || !first.SendPermit {
		t.Fatal(err)
	}
	proof := biz.RuntimeCreationNotSent{NamespaceUID: w.NamespaceUID, Name: a.Snapshot.RuntimePlan.Name, ExpectedSpecSHA256: a.Snapshot.RuntimePlan.ExpectedSpecSHA256, Reason: "PRECONDITION_FAILED_BEFORE_CREATE"}
	proof.AttemptDispatchedAt = first.State.RuntimeTemplate.Attempts[0].DispatchedAt
	if _, err := r.RecordRuntimeNotSent(ctx, authority, proof); err != nil {
		t.Fatal(err)
	}
	second, err := lifecycle.New(open()).ReserveRuntime(ctx, authority)
	if err != nil || !second.SendPermit || len(second.State.RuntimeTemplate.Attempts) != 2 || second.State.RuntimeTemplate.Attempts[0].NotSentAt == nil {
		t.Fatal("lost original proof/history", err)
	}
	if _, err := r.RecordRuntimeNotSent(ctx, authority, proof); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("old receipt proved a later request unsent", err)
	}
	proof.AttemptDispatchedAt = second.State.RuntimeTemplate.Attempts[1].DispatchedAt
	if _, err := r.RecordRuntimeNotSent(ctx, authority, proof); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.RequestRuntimeClose(ctx, authority, "STEP_FAILED"); err != nil {
		t.Fatal(err)
	}
	if third, err := r.ReserveRuntime(ctx, authority); !errors.Is(err, biz.ErrPipelineDispatchBlocked) || third.SendPermit {
		t.Fatal("Stop lost to safe retry", err)
	}
}

func TestRuntimeV21ConcurrentOwnersShareOneDurableAttempt(t *testing.T) {
	open, _, authority, _ := preparedRuntimeV21(t, 30)
	start := make(chan struct{})
	var wg sync.WaitGroup
	permits := make(chan bool, 6)
	for n := 0; n < 6; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := lifecycle.New(open())
			<-start
			result, err := r.ReserveRuntime(context.Background(), authority)
			if err != nil {
				t.Error(err)
			}
			permits <- result.SendPermit
		}()
	}
	close(start)
	wg.Wait()
	close(permits)
	count := 0
	for permit := range permits {
		if permit {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("concurrent owners issued %d permits", count)
	}
	state, err := lifecycle.New(open()).GetRuntime(context.Background(), authority.TenantID, authority.ExecutionID)
	if err != nil || len(state.RuntimeTemplate.Attempts) != 1 {
		t.Fatal("original history changed", err)
	}
}

func TestRuntimeV21ExpiredPrepareClosesWithStableIndependentUUID(t *testing.T) {
	open, a, authority, _ := preparedRuntimeV21(t, 1)
	r := lifecycle.New(open())
	ctx := context.Background()
	state, err := r.GetRuntime(ctx, a.TenantID, a.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	original := *biz.TimingForManagedRole(state, "prepare")
	timer := time.NewTimer(time.Until(original.DeadlineAt) + 20*time.Millisecond)
	defer timer.Stop()
	<-timer.C
	closing, _, err := r.RequestRuntimeClose(ctx, authority, "DEADLINE")
	if err != nil || closing.CloseReason != "DEADLINE" || closing.CloseOperationID == a.OperationID {
		t.Fatal("expired original step was not fenced", err)
	}
	if _, err := uuid.Parse(closing.CloseOperationID); err != nil {
		t.Fatal("missing independent close UUID", err)
	}
	replay, _, err := lifecycle.New(open()).RequestRuntimeClose(ctx, authority, "DEADLINE")
	if err != nil || replay.CloseOperationID != closing.CloseOperationID || *biz.TimingForManagedRole(replay, "prepare") != original {
		t.Fatal("recovery changed close UUID or renewed window", err)
	}
}

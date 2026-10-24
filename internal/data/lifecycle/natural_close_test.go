package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
)

func TestNaturalClosingEscalatesOnlyAtDatabaseDeadlineAndPreservesOriginalFence(t *testing.T) {
	open, admission, authority, workspace := runtimeFixture(t, 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := open()
	repository := lifecycle.New(pool)
	if _, _, err := repository.RecordPrepared(ctx, authority, workspace); err != nil {
		t.Fatal(err)
	}
	reserved, err := reserveTrainingV21(t, repository, ctx, authority)
	if err != nil || !reserved.SendPermit {
		t.Fatal("original training permit", err)
	}
	handle := biz.TrainingHandle{NamespaceUID: workspace.NamespaceUID, PVCUID: workspace.PVCUID, TrainJobUID: "fixture-training-uid"}
	if _, err := repository.RecordTrainingHandle(ctx, authority, handle); err != nil {
		t.Fatal(err)
	}
	zero := int32(0)
	observation := biz.TrainingRuntimeObservation{Handle: handle, Outcome: "SUCCEEDED", WritersAbsent: true, ObservedAt: time.Now().UTC(), Resources: []biz.RuntimeResource{{APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainJob", Namespace: workspace.NamespaceName, Name: reserved.State.Training.Name, UID: handle.TrainJobUID, APIObjectPresent: true, Terminal: true}, {APIVersion: "jobset.x-k8s.io/v1alpha2", Kind: "JobSet", Namespace: workspace.NamespaceName, Name: "training-set", UID: "fixture-set-uid", OwnerUID: handle.TrainJobUID, APIObjectPresent: true, Terminal: true}, {APIVersion: "v1", Kind: "Pod", Namespace: workspace.NamespaceName, Name: "training-pod", UID: "fixture-pod-uid", OwnerUID: "fixture-set-uid", APIObjectPresent: true, Terminal: true, ExitCode: &zero}}}
	if _, err := repository.RecordTrainingObservation(ctx, authority, observation); err != nil {
		t.Fatal(err)
	}
	publication := publicationFixture(t, admission, authority)
	if _, _, err := recordPublicationV21(t, repository, ctx, authority, publication); err != nil {
		t.Fatal(err)
	}
	original, _, err := repository.RequestRuntimeClose(ctx, authority, "NATURAL_TERMINAL")
	if err != nil {
		t.Fatal(err)
	}
	before, replayed, err := repository.RequestRuntimeClose(ctx, authority, "DEADLINE")
	if err != nil || !replayed || before.CloseReason != "NATURAL_TERMINAL" || before.CloseGeneration != original.CloseGeneration || !before.CloseRequestedAt.Equal(original.CloseRequestedAt) {
		t.Fatal("deadline observation changed a natural fence before frozen deadline", err)
	}
	// Wait for the real DB clock; no fake business clock or raw state UPDATE.
	for {
		var now time.Time
		if err := pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
			t.Fatal(err)
		}
		if !now.Before(admission.Snapshot.DeadlineAt) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	pool.Close()
	repository = lifecycle.New(open())
	escalated, replayed, err := repository.RequestRuntimeClose(ctx, authority, "DEADLINE")
	if err != nil || replayed || escalated.CloseReason != "DEADLINE" || escalated.CloseGeneration != original.CloseGeneration || !escalated.CloseRequestedAt.Equal(original.CloseRequestedAt) || escalated.ClosedAt != nil || escalated.Publication == nil || escalated.Publication.ID != publication.ID || escalated.Training.RequestSHA256 != reserved.State.Training.RequestSHA256 || escalated.OwnerRevision <= original.OwnerRevision {
		t.Fatalf("NATURAL_DEADLINE_ESCALATION_NOT_IMPLEMENTED: %+v %v", escalated, err)
	}
	if retry, err := reserveTrainingV21(t, repository, ctx, authority); err != nil || retry.SendPermit || retry.State.Training.RequestSHA256 != reserved.State.Training.RequestSHA256 {
		t.Fatal("deadline escalation regenerated training", err)
	}
	if state, replayed, err := repository.RequestRuntimeClose(ctx, authority, "DEADLINE"); err != nil || !replayed || state.OwnerRevision != escalated.OwnerRevision {
		t.Fatal("deadline replay rewrote the original fence", err)
	}
}

package lifecycle_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
)

func TestNoWorkerClosureRequiresOriginalFenceAndSurvivesPostgresReconnect(t *testing.T) {
	open, admission, authority, workspace := runtimeFixture(t)
	ctx := context.Background()
	pool := open()
	repository := lifecycle.New(pool)
	if _, _, err := repository.RecordPrepared(ctx, authority, workspace); err != nil {
		t.Fatal(err)
	}
	reserved, err := reserveTrainingV21(t, repository, ctx, authority)
	if err != nil || !reserved.SendPermit {
		t.Fatal(err)
	}
	handle := biz.TrainingHandle{NamespaceUID: workspace.NamespaceUID, PVCUID: workspace.PVCUID, TrainJobUID: uuid.NewString()}
	if _, err := repository.RecordTrainingHandle(ctx, authority, handle); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	observation := biz.TrainingRuntimeObservation{Handle: handle, Outcome: "UNKNOWN", WritersAbsent: true,
		ObservedAt: now, NoWorkerPodListResourceVersion: "complete-original-inventory-101",
		Resources: []biz.RuntimeResource{
			{APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainJob", Namespace: workspace.NamespaceName, Name: reserved.State.Training.Name, UID: handle.TrainJobUID, APIObjectPresent: true, CreationDisabled: true},
			{APIVersion: "jobset.x-k8s.io/v1alpha2", Kind: "JobSet", Namespace: workspace.NamespaceName, Name: reserved.State.Training.Name, UID: "original-set", OwnerUID: handle.TrainJobUID, APIObjectPresent: true, CreationDisabled: true},
			{APIVersion: "batch/v1", Kind: "Job", Namespace: workspace.NamespaceName, Name: reserved.State.Training.Name + "-node-0", UID: "original-job", OwnerUID: "original-set", APIObjectPresent: true, CreationDisabled: true},
		}}
	for i := range observation.Resources {
		observation.Resources[i].Suspension = &biz.TrainingControllerSuspension{Generation: 2, AcknowledgedAt: now, WorkerExitRetention: observation.Resources[i].Kind == "Job"}
	}
	if _, err := repository.RecordTrainingObservation(ctx, authority, observation); err == nil {
		t.Fatal("no-worker inventory bypassed original close fence")
	}
	if _, err := execution.New(pool).ApplyCloseIntent(ctx, biz.CloseIntent{TenantID: admission.TenantID, ExecutionID: admission.ExecutionID, OperationID: admission.OperationID, SpecHash: admission.SpecHash, SourceGeneration: 1, Reason: biz.CloseReasonUserStop, RequestedAt: time.Now().UTC().Truncate(time.Microsecond), RequestedActor: admission.Actor}); err != nil {
		t.Fatal(err)
	}
	closing, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || closing.CloseGeneration != 1 {
		t.Fatal(err)
	}
	observation.ObservedAt = time.Now().UTC().Truncate(time.Microsecond)
	for i := range observation.Resources {
		observation.Resources[i].Suspension.AcknowledgedAt = observation.ObservedAt
	}
	for _, scenario := range []string{"missing_ack", "old_ack", "no_inventory", "success", "unretained", "unobserved_generation"} {
		t.Run(scenario, func(t *testing.T) {
			encoded, _ := json.Marshal(observation)
			var changed biz.TrainingRuntimeObservation
			if err := json.Unmarshal(encoded, &changed); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "missing_ack":
				changed.Resources[0].Suspension = nil
			case "old_ack":
				changed.Resources[1].Suspension.AcknowledgedAt = closing.CloseRequestedAt.Add(-2 * time.Second)
			case "no_inventory":
				changed.NoWorkerPodListResourceVersion = ""
			case "success":
				changed.Outcome = "SUCCEEDED"
			case "unretained":
				changed.Resources[2].Suspension.WorkerExitRetention = false
			case "unobserved_generation":
				changed.Resources[2].Suspension.Generation = 3
			}
			if _, err := lifecycle.New(open()).RecordTrainingObservation(ctx, authority, changed); err == nil {
				t.Fatal("invalid zero-worker close committed:", scenario)
			}
		})
	}
	if _, err := repository.RecordTrainingObservation(ctx, authority, observation); err != nil {
		t.Fatal("original acknowledged empty-worker proof", err)
	}
	zero := int32(0)
	evidence := biz.ManagedCloseEvidence{RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, ObservedAt: time.Now().UTC()}
	for _, step := range []string{"prepare", "train-wait", "collect", "publish"} {
		evidence.Resources = append(evidence.Resources, biz.RuntimeResource{APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: "stopped-" + step, UID: uuid.NewString(), OwnerUID: authority.WorkflowUID, APIObjectPresent: true, Terminal: true, ExitCode: &zero})
	}
	closed, err := repository.ConfirmRuntimeClosed(ctx, authority, closing.CloseGeneration, observation, evidence)
	if err != nil || closed.ClosedAt == nil || closed.Publication != nil || closed.CloseOperationID == admission.OperationID || uuid.Validate(closed.CloseOperationID) != nil {
		t.Fatal("zero-worker original close failed", err)
	}
	pool.Close()
	read, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || read.ClosedAt == nil || read.Observation == nil || !read.Observation.WritersAbsent || read.Observation.NoWorkerPodListResourceVersion != observation.NoWorkerPodListResourceVersion {
		t.Fatal("reconnect lost original no-worker proof", err)
	}
	for _, resource := range read.Observation.Resources {
		if resource.Kind == "Pod" {
			t.Fatal("invented a training Pod or exit")
		}
	}
}

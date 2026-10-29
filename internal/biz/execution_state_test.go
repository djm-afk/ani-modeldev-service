package biz_test

import (
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func TestProjectRuntimeStatesReportsOwnerFailureBeforeAnyTrainingPod(t *testing.T) {
	runtime := closedRootAdmissionFailure()
	base := submittedExecutionStates()
	got, err := biz.ProjectRuntimeStates(base, runtime)
	want := base
	want.Compute, want.Close = biz.ComputeStateFailed, biz.CloseStateClosed
	if err != nil || got != want {
		t.Fatalf("verified pre-Pod pipeline failure lost its outcome: got %+v, want %+v, err %v", got, want, err)
	}
	if runtime.Workspace != nil || runtime.Training != nil || runtime.TrainingHandle != nil || runtime.Observation != nil || runtime.Publication != nil {
		t.Fatal("early failure projection invented training or publication facts")
	}
}

func TestProjectRuntimeStatesRequiresCommittedFailedOwnerEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*biz.ExecutionRuntime)
		close  biz.CloseState
	}{
		{"stop intent only", func(r *biz.ExecutionRuntime) { r.ClosedAt, r.CloseEvidence = nil, nil }, biz.CloseStateClosing},
		{"uncommitted owner evidence", func(r *biz.ExecutionRuntime) { r.ClosedAt = nil }, biz.CloseStateClosing},
		{"closed without owner termination", func(r *biz.ExecutionRuntime) { r.CloseEvidence.OwnerTermination = nil }, biz.CloseStateClosed},
		{"step failed reason alone", func(r *biz.ExecutionRuntime) { r.CloseReason = "STEP_FAILED"; r.CloseEvidence.OwnerTermination = nil }, biz.CloseStateClosed},
		{"undispatched close", func(r *biz.ExecutionRuntime) {
			r.CloseEvidence.NoDispatch = true
			r.CloseEvidence.RunID, r.CloseEvidence.WorkflowUID, r.CloseEvidence.OwnerTermination = "", "", nil
		}, biz.CloseStateClosed},
		{"canceled run", func(r *biz.ExecutionRuntime) { r.CloseEvidence.OwnerTermination.RunState = "CANCELED" }, biz.CloseStateClosed},
		{"unknown run", func(r *biz.ExecutionRuntime) { r.CloseEvidence.OwnerTermination.RunState = "UNKNOWN" }, biz.CloseStateClosed},
		{"unknown workflow", func(r *biz.ExecutionRuntime) { r.CloseEvidence.OwnerTermination.WorkflowPhase = "Unknown" }, biz.CloseStateClosed},
		{"successful workflow", func(r *biz.ExecutionRuntime) { r.CloseEvidence.OwnerTermination.WorkflowPhase = "Succeeded" }, biz.CloseStateClosed},
		{"successful run with failed workflow", func(r *biz.ExecutionRuntime) {
			r.CloseEvidence.OwnerTermination.RunState = "SUCCEEDED"
			r.CloseEvidence.OwnerTermination.WorkflowPhase = "Failed"
		}, biz.CloseStateClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := closedRootAdmissionFailure()
			test.change(&runtime)
			base := submittedExecutionStates()
			got, err := biz.ProjectRuntimeStates(base, runtime)
			want := base
			want.Close = test.close
			if err != nil || got != want {
				t.Fatalf("close/cancel/unknown facts invented compute failure: got %+v, want %+v, err %v", got, want, err)
			}
		})
	}
}

func TestProjectRuntimeStatesAcceptsBothFailedWorkflowPhases(t *testing.T) {
	for _, phase := range []string{"Error", "Failed"} {
		t.Run(phase, func(t *testing.T) {
			runtime := closedRootAdmissionFailure()
			runtime.CloseEvidence.OwnerTermination.WorkflowPhase = phase
			got, err := biz.ProjectRuntimeStates(submittedExecutionStates(), runtime)
			if err != nil || got.Compute != biz.ComputeStateFailed || got.Delivery != biz.DeliveryStatePending || got.Close != biz.CloseStateClosed {
				t.Fatalf("terminal owner failure was lost: %+v, %v", got, err)
			}
		})
	}
}

func TestProjectRuntimeStatesPreservesSuccessfulTrainingAfterLaterPipelineFailure(t *testing.T) {
	for _, published := range []bool{false, true} {
		name := "unpublished"
		if published {
			name = "published"
		}
		t.Run(name, func(t *testing.T) {
			runtime := closedRootAdmissionFailure()
			runtime.CloseReason = "STEP_FAILED"
			runtime.Workspace = &biz.WorkspaceBinding{NamespaceUID: "namespace", PVCUID: "workspace"}
			runtime.Training = &biz.TrainingPlan{Workspace: *runtime.Workspace}
			runtime.TrainingHandle = &biz.TrainingHandle{NamespaceUID: "namespace", PVCUID: "workspace", TrainJobUID: "train-job"}
			runtime.Observation = &biz.TrainingRuntimeObservation{Handle: *runtime.TrainingHandle, Outcome: "SUCCEEDED", WritersAbsent: true}
			wantDelivery := biz.DeliveryStatePending
			if published {
				runtime.Publication = &biz.RuntimePublication{ID: "verified-publication"}
				wantDelivery = biz.DeliveryStatePublished
			}
			got, err := biz.ProjectRuntimeStates(submittedExecutionStates(), runtime)
			if err != nil || got.Compute != biz.ComputeStateSucceeded || got.Delivery != wantDelivery || got.Close != biz.CloseStateClosed {
				t.Fatalf("pipeline failure overwrote an independent training/delivery outcome: %+v, %v", got, err)
			}
		})
	}
}

func submittedExecutionStates() biz.ExecutionStates {
	return biz.ExecutionStates{Compute: biz.ComputeStateSubmissionConfirmed, Delivery: biz.DeliveryStatePending, Resource: biz.ResourceStateNotApplicable, Close: biz.CloseStateOpen}
}

func closedRootAdmissionFailure() biz.ExecutionRuntime {
	// Actual persisted owner history of execution 4534a7de, whose root-driver
	// admission failed before any Pod, workspace or training fact existed.
	// Runtime readers validate this committed evidence against its admission
	// and Run authority before passing it to the state projection.
	finished := time.Date(2026, 10, 28, 22, 22, 14, 0, time.UTC)
	observed := time.Date(2026, 10, 28, 23, 11, 15, 542005840, time.UTC)
	closed := time.Date(2026, 10, 28, 23, 11, 15, 545652000, time.UTC)
	return biz.ExecutionRuntime{
		CloseGeneration: 1, CloseReason: "USER_STOP", CloseRequestedAt: time.Date(2026, 10, 28, 22, 25, 5, 0, time.UTC), ClosedAt: &closed,
		CloseEvidence: &biz.ManagedCloseEvidence{
			RunID: "beed531c-d16d-47fb-99e3-030e93d089ac", WorkflowUID: "dcac8e3a-7eb9-4417-8a9d-f32722947c07", ObservedAt: observed,
			OwnerTermination: &biz.ManagedOwnerTermination{RunState: "FAILED", RunFinishedAt: finished, WorkflowPhase: "Error", WorkflowFinishedAt: finished, WorkflowResourceVersion: "11957475", WorkflowNodesSHA256: "bdddd265e5a649726eb9faa26439707ae8935155cd81c3391e2b78a0908f030a"},
		},
	}
}

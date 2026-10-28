package service

import (
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func TestRejectedTrainingQueryUsesCommittedObservationTime(t *testing.T) {
	snapshot := conformance.SnapshotV1()
	intent := conformance.IntentV1()
	hash, _ := snapshot.Digest()
	accepted := snapshot.Timing.AcceptedAt
	rejected := accepted.Add(time.Minute)
	closed := rejected.Add(time.Minute)
	for _, test := range []struct {
		name     string
		closedAt *time.Time
		want     time.Time
	}{
		{name: "rejected before close", want: rejected},
		{name: "closed after rejection", closedAt: &closed, want: closed},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := biz.QueryRecord{
				Execution: biz.Execution{Admission: biz.Admission{TenantID: snapshot.IdentityRef.TenantID, Actor: snapshot.IdentityRef.Actor, OperationID: snapshot.IdentityRef.OperationID, ExecutionID: snapshot.IdentityRef.ExecutionID, Intent: intent, IntentHash: snapshot.IdentityRef.IntentHash, Snapshot: snapshot, SpecHash: hash, AcceptedAt: accepted}, States: biz.ExecutionStates{
					Compute: biz.ComputeStateFailed, Delivery: biz.DeliveryStatePending, Resource: biz.ResourceStateNotApplicable, Close: biz.CloseStateClosing,
				}},
				Runtime: biz.ExecutionRuntime{TrainingRejection: &biz.TrainingCreationRejection{ObservedAt: rejected}, ClosedAt: test.closedAt},
			}
			view, err := executionView(record)
			if err != nil {
				t.Fatal(err)
			}
			if view.States.ComputeState != modeldevv1.ComputeState_COMPUTE_STATE_FAILED || !view.ObservedAt.AsTime().Equal(test.want) {
				t.Fatalf("rejected query state=%v observed_at=%s, want FAILED at %s", view.States.ComputeState, view.ObservedAt.AsTime(), test.want)
			}
		})
	}
}

// A running collect must be reported by both current_step and the timeline so a
// client cannot see TRAIN_WAIT while collect is admitted.
func TestExecutionViewReportsRunningCollect(t *testing.T) {
	snapshot := conformance.SnapshotV1()
	hash, _ := snapshot.Digest()
	record := biz.QueryRecord{
		Execution: biz.Execution{Admission: biz.Admission{TenantID: snapshot.IdentityRef.TenantID, Actor: snapshot.IdentityRef.Actor, OperationID: snapshot.IdentityRef.OperationID, ExecutionID: snapshot.IdentityRef.ExecutionID, Intent: conformance.IntentV1(), IntentHash: snapshot.IdentityRef.IntentHash, Snapshot: snapshot, SpecHash: hash, AcceptedAt: snapshot.Timing.AcceptedAt}, States: biz.ExecutionStates{
			Compute: biz.ComputeStateTraining, Delivery: biz.DeliveryStatePending, Resource: biz.ResourceStateNotApplicable, Close: biz.CloseStateOpen,
		}},
		Runtime: biz.ExecutionRuntime{
			Workspace:      &biz.WorkspaceBinding{},
			StepTimings:    []biz.ManagedStepTiming{managedTiming(stepRolePrepare, 0), managedTiming(stepRoleCollect, 10*time.Minute)},
			Training:       &biz.TrainingPlan{},
			TrainingWindow: &biz.TrainingBudgetTiming{AdmittedAt: stepTestStart.Add(time.Minute), DeadlineAt: stepTestStart.Add(time.Hour)},
			Observation:    &biz.TrainingRuntimeObservation{Outcome: "SUCCEEDED", ObservedAt: stepTestStart.Add(9 * time.Minute)},
		},
	}
	view, err := executionView(record)
	if err != nil {
		t.Fatal(err)
	}
	if view.CurrentStep != modeldevv1.PipelineStep_PIPELINE_STEP_COLLECT {
		t.Fatalf("current_step=%v, want COLLECT", view.CurrentStep)
	}
	if len(view.Steps) != 5 {
		t.Fatalf("steps=%d, want 5", len(view.Steps))
	}
	collect := stepByRole(t, view.Steps, stepRoleCollect)
	if collect.State != stepStateRunning {
		t.Fatalf("collect=%v, want RUNNING", collect)
	}
}

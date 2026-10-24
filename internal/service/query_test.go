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

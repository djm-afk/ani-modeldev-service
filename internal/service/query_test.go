package service

import (
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func TestRejectedTrainingQueryUsesCommittedObservationTime(t *testing.T) {
	accepted := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
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
				Execution: biz.Execution{Admission: biz.Admission{AcceptedAt: accepted}, States: biz.ExecutionStates{
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

func TestPresetDisplayNameDistinguishesPresetIdentity(t *testing.T) {
	first := presetDisplayName("aaaaaaaa-1111-4111-8111-bbbbbbbbbbbb")
	second := presetDisplayName("cccccccc-2222-4222-8222-dddddddddddd")
	if first == second {
		t.Fatalf("distinct preset IDs must not share a display name: %q", first)
	}
	if first != "General CPU MLP · aaaaaaaa" {
		t.Fatalf("display name = %q, want stable preset-derived label", first)
	}
	if short := presetDisplayName("abcd"); short != "General CPU MLP" {
		t.Fatalf("short ID display name = %q, want base label", short)
	}
}


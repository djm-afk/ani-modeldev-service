package lifecycle_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
)

func TestManagedPrepareLostTimingReplySurvivesOwnerReconnectAndReadOnlyReplay(t *testing.T) {
	// The real repository has committed PREPARE; the owner receives no timing
	// reply from that first call. Only a fresh owner's persisted reads remain.
	open, admission, authority, workspace := preparedRuntimeV21(t, 30)
	ctx := context.Background()
	state, err := lifecycle.New(open()).GetRuntime(ctx, admission.TenantID, admission.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	stored := biz.TimingForManagedRole(state, "prepare")
	if stored == nil {
		t.Fatal("first PREPARE timing was not persisted")
	}
	original := *stored
	wantDeadline, err := admission.Snapshot.Timing.DeadlineFor(original.AdmittedAt)
	if err != nil || original.AdmittedAt.Before(admission.AcceptedAt) || !original.DeadlineAt.Equal(wantDeadline) || original.DeadlineAt.After(admission.Snapshot.DeadlineAt) {
		t.Fatalf("PREPARE did not use the original finite execution window: %+v / %v", original, err)
	}
	wantBinding := biz.FinetuningStepBinding{Role: "prepare", SpecHash: admission.SpecHash, RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, TaskID: "prepare-v21", PodName: "prepare-v21", PodUID: original.Binding.PodUID}
	if original.Binding != wantBinding || original.Binding.PodUID == "" || state.RuntimeTemplate != nil || state.Training != nil {
		t.Fatal("unexpected original binding or an unsolicited compute dispatch")
	}
	for n := 0; n < 3; n++ {
		repository := lifecycle.New(open())
		queried, err := repository.GetRuntime(ctx, admission.TenantID, admission.ExecutionID)
		if err != nil || !reflect.DeepEqual(queried, state) {
			t.Fatalf("read-only owner query changed persisted state: %v", err)
		}
		replayed, wasReplay, err := repository.ReserveManagedStep(ctx, authority, wantBinding)
		if err != nil || !wasReplay || replayed != original {
			t.Fatalf("lost reply renewed PREPARE clock or binding: %+v / %t / %v", replayed, wasReplay, err)
		}
	}
	for _, field := range []string{"pod UID", "task ID"} {
		changed := wantBinding
		if field == "pod UID" {
			changed.PodUID = uuid.NewString()
		} else {
			changed.TaskID = "replacement-task"
		}
		if _, _, err := lifecycle.New(open()).ReserveManagedStep(ctx, authority, changed); !errors.Is(err, biz.ErrRuntimeConflict) {
			t.Fatalf("a replay replaced the original %s: %v", field, err)
		}
	}
	first, err := lifecycle.New(open()).ReserveRuntime(ctx, authority)
	if err != nil || !first.SendPermit || first.State.RuntimeTemplate == nil || len(first.State.RuntimeTemplate.Attempts) != 1 {
		t.Fatal("first Runtime dispatch", err)
	}
	if replay, err := lifecycle.New(open()).ReserveRuntime(ctx, authority); err != nil || replay.SendPermit || len(replay.State.RuntimeTemplate.Attempts) != 1 {
		t.Fatal("owner restart resent the Runtime request", err)
	}
	if _, err := lifecycle.New(open()).RecordRuntimeBinding(ctx, authority, runtimeBindingV21(admission, workspace)); err != nil {
		t.Fatal(err)
	}
	final, err := lifecycle.New(open()).GetRuntime(ctx, admission.TenantID, admission.ExecutionID)
	if err != nil || biz.TimingForManagedRole(final, "prepare") == nil || *biz.TimingForManagedRole(final, "prepare") != original || final.RuntimeTemplate.Binding == nil || *final.RuntimeTemplate.Binding != runtimeBindingV21(admission, workspace) || final.Training != nil {
		t.Fatal("subsequent binding changed PREPARE timing or dispatched training", err)
	}
}

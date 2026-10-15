package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
)

func TestFinetuningTrainingDeadlineIsDatabaseFixedAcrossReplayAndReconnect(t *testing.T) {
	open, admission, authority, _ := finetuningRuntimeFixture(t, 3*time.Hour)
	repository := lifecycle.New(open())
	state, err := repository.GetRuntime(context.Background(), admission.TenantID, admission.ExecutionID)
	if err != nil || state.Training == nil || state.Training.FinetuningTiming == nil {
		t.Fatalf("missing first TRAIN budget: %v", err)
	}
	original := *state.Training.FinetuningTiming
	if !original.AdmittedAt.Before(original.DeadlineAt) || !original.DeadlineAt.Equal(original.AdmittedAt.Add(time.Hour)) || original.DeadlineAt.After(admission.Snapshot.DeadlineAt) {
		t.Fatalf("incorrect original shared TRAIN/train-wait budget: %+v", original)
	}
	replay, err := lifecycle.New(open()).ReserveTraining(context.Background(), authority)
	if err != nil || replay.SendPermit || replay.State.Training == nil || replay.State.Training.FinetuningTiming == nil || *replay.State.Training.FinetuningTiming != original || replay.State.Training.RequestSHA256 != state.Training.RequestSHA256 {
		t.Fatalf("reconnect renewed TRAIN budget or permit: %+v / %v", replay, err)
	}
}

func TestFinetuningTrainingBudgetCannotOutliveOriginalExecutionDeadline(t *testing.T) {
	open, admission, _, _ := finetuningRuntimeFixture(t, 20*time.Minute)
	state, err := lifecycle.New(open()).GetRuntime(context.Background(), admission.TenantID, admission.ExecutionID)
	if err != nil || state.Training == nil || state.Training.FinetuningTiming == nil || !state.Training.FinetuningTiming.DeadlineAt.Equal(admission.Snapshot.DeadlineAt) {
		t.Fatalf("TRAIN exceeded the original overall deadline: %+v / %v", state.Training, err)
	}
}

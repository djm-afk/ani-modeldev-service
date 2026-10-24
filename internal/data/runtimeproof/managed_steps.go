package runtimeproof

import (
	"context"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

var _ biz.ManagedStepPredecessorVerifier = (*Verifier)(nil)

func (verifier *Verifier) VerifyManagedStepPredecessor(ctx context.Context, execution biz.Execution, association biz.ManagedStepAssociation, role string, state biz.ExecutionRuntime) error {
	predecessor, uid, taskID, podName := "", "", "", ""
	var deadline time.Time
	switch role {
	case "collect":
		if state.Observation == nil || state.TrainingHandle == nil || state.Observation.Handle != *state.TrainingHandle || state.Observation.Outcome != "SUCCEEDED" || !state.Observation.WritersAbsent {
			return biz.ErrRuntimeNotReady
		}
		predecessor = "train-wait"
		if state.TrainingWindow != nil {
			deadline = state.TrainingWindow.DeadlineAt
		}
		if execution.Snapshot.Kind == "FINETUNING" {
			predecessor = "verify"
			for _, start := range state.FinetuningSteps {
				if start.Binding.Role == predecessor && start.Result != nil && start.Result.ExitCode == 0 {
					uid, taskID, podName, deadline = start.Binding.PodUID, start.Binding.TaskID, start.Binding.PodName, start.DeadlineAt
				}
			}
			if uid == "" {
				return biz.ErrRuntimeNotReady
			}
		}
	case "publish", "register":
		predecessor = "collect"
		if role == "register" {
			predecessor = "publish"
		}
		original := biz.TimingForManagedRole(state, predecessor)
		if original == nil {
			return biz.ErrRuntimeNotReady
		}
		uid, taskID, podName, deadline = original.Binding.PodUID, original.Binding.TaskID, original.Binding.PodName, original.DeadlineAt
	default:
		return biz.ErrRuntimeConflict
	}
	if deadline.IsZero() {
		return biz.ErrRuntimeNotReady
	}
	tasks, err := verifier.currentTasks(ctx, execution, association)
	if err != nil {
		return err
	}
	task, err := oneTask(tasks, predecessor)
	if err != nil || task.RunID != association.RunID || task.State != "SUCCEEDED" || taskID != "" && task.ID != taskID {
		return biz.ErrRuntimeNotReady
	}
	pod, err := verifier.currentTaskPod(ctx, execution, association, task, uid)
	if err != nil {
		return err
	}
	terminal, zero, completed, code := terminatedPod(pod)
	if !terminal || !zero || code == nil || *code != 0 || completed.IsZero() || completed.After(time.Now().UTC()) || completed.After(deadline) || podName != "" && pod.GetName() != podName {
		return biz.ErrRuntimeNotReady
	}
	return nil
}

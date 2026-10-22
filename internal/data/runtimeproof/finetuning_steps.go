package runtimeproof

import (
	"context"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

var _ biz.FinetuningStepPredecessorVerifier = (*Verifier)(nil)

// VerifyFinetuningStepPredecessor reads current KFP and Pod evidence. A Run is
// expected to remain RUNNING while its next computation begins.
func (verifier *Verifier) VerifyFinetuningStepPredecessor(ctx context.Context, execution biz.Execution, association biz.ManagedStepAssociation, role string, state biz.ExecutionRuntime) error {
	if execution.Snapshot.Kind != "FINETUNING" || execution.Snapshot.Finetuning == nil || state.Observation == nil || state.Observation.Outcome != "SUCCEEDED" || !state.Observation.WritersAbsent || state.TrainingHandle == nil || state.Observation.Handle != *state.TrainingHandle {
		return biz.ErrRuntimeNotReady
	}
	predecessor, expectedUID := "train-wait", ""
	if role == "verify" {
		predecessor = "export"
		for _, start := range state.FinetuningSteps {
			if start.Binding.Role != "export" {
				continue
			}
			if start.Binding.SpecHash != execution.SpecHash || start.Binding.RunID != association.RunID || start.Binding.WorkflowUID != association.WorkflowUID || start.Result == nil || start.Result.ExitCode != 0 {
				return biz.ErrRuntimeNotReady
			}
			expectedUID = start.Binding.PodUID
		}
		if expectedUID == "" {
			return biz.ErrRuntimeNotReady
		}
	} else if role != "export" {
		return biz.ErrRuntimeConflict
	}
	tasks, err := verifier.currentTasks(ctx, execution, association)
	if err != nil {
		return err
	}
	task, err := oneTask(tasks, predecessor)
	if err != nil || task.RunID != association.RunID || task.State != "SUCCEEDED" {
		return biz.ErrRuntimeNotReady
	}
	pod, err := verifier.currentTaskPod(ctx, execution, association, task, expectedUID)
	if err != nil {
		return err
	}
	terminal, zero, completed, code := terminatedPod(pod)
	if !terminal || !zero || code == nil || *code != 0 || completed.After(time.Now().UTC()) {
		return biz.ErrRuntimeNotReady
	}
	if role == "verify" {
		for _, start := range state.FinetuningSteps {
			if start.Binding.Role == "export" && (task.ID != start.Binding.TaskID || pod.GetName() != start.Binding.PodName || start.Result.FinishedAt.After(completed)) {
				return biz.ErrRuntimeConflict
			}
		}
	}
	return nil
}

package lifecycle

import (
	"context"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/util/validation"
)

func validManagedTimingBinding(authority biz.RunAuthorityCandidate, binding biz.FinetuningStepBinding) bool {
	role := binding.Role
	return (role == "prepare" || role == "collect" || role == "publish" || role == "register") && binding.SpecHash == authority.SpecHash && binding.RunID == authority.RunID && binding.WorkflowUID == authority.WorkflowUID && validOpaqueID(binding.TaskID) && validOpaqueID(binding.PodUID) && len(validation.IsDNS1123Subdomain(binding.PodName)) == 0
}

func (repository *Repository) ReserveManagedStep(ctx context.Context, authority biz.RunAuthorityCandidate, binding biz.FinetuningStepBinding) (biz.ManagedStepTiming, bool, error) {
	var timing biz.ManagedStepTiming
	replayed := false
	_, _, err := repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		if !validManagedTimingBinding(authority, binding) || binding.Role == "register" && current.execution.Snapshot.Kind != "FINETUNING" {
			return false, biz.ErrRuntimeConflict
		}
		if original := biz.TimingForManagedRole(current.state, binding.Role); original != nil {
			if original.Binding != binding {
				return false, biz.ErrRuntimeConflict
			}
			timing, replayed = *original, true
			return false, nil
		}
		if err := requireOpen(current); err != nil {
			return false, err
		}
		if binding.Role == "collect" && (current.state.Observation == nil || current.state.Observation.Outcome != "SUCCEEDED" || !current.state.Observation.WritersAbsent) {
			return false, biz.ErrRuntimeNotReady
		}
		if binding.Role == "publish" && biz.TimingForManagedRole(current.state, "collect") == nil || binding.Role == "register" && biz.TimingForManagedRole(current.state, "publish") == nil {
			return false, biz.ErrRuntimeNotReady
		}
		for _, previous := range current.state.StepTimings {
			if previous.Binding.TaskID == binding.TaskID || previous.Binding.PodUID == binding.PodUID {
				return false, biz.ErrRuntimeConflict
			}
		}
		deadline, err := current.execution.Snapshot.Timing.DeadlineFor(current.now)
		if err != nil || !current.now.Before(deadline) {
			return false, biz.ErrPipelineDispatchBlocked
		}
		current.writeDeadline = deadline
		timing = biz.ManagedStepTiming{Binding: binding, AdmittedAt: current.now, DeadlineAt: deadline}
		current.state.StepTimings = append(current.state.StepTimings, timing)
		return true, nil
	})
	return timing, replayed, err
}

func requireRoleWindow(current *runtimeTransaction, role string) error {
	timing := biz.TimingForManagedRole(current.state, role)
	if timing == nil {
		return biz.ErrRuntimeNotReady
	}
	if !current.now.Before(timing.DeadlineAt) {
		return biz.ErrPipelineDispatchBlocked
	}
	current.writeDeadline = timing.DeadlineAt
	return nil
}

func validManagedTimings(current *runtimeTransaction) bool {
	if len(current.state.StepTimings) > 4 {
		return false
	}
	seen, tasks, pods := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, timing := range current.state.StepTimings {
		b := timing.Binding
		a := biz.RunAuthorityCandidate{SpecHash: current.execution.SpecHash, RunID: b.RunID, WorkflowUID: b.WorkflowUID}
		deadline, err := current.execution.Snapshot.Timing.DeadlineFor(timing.AdmittedAt)
		if err != nil || !deadline.Equal(timing.DeadlineAt) || timing.AdmittedAt.After(current.now) || !timing.AdmittedAt.Before(deadline) || !validManagedTimingBinding(a, b) || seen[b.Role] || tasks[b.TaskID] || pods[b.PodUID] || b.Role == "register" && current.execution.Snapshot.Kind != "FINETUNING" {
			return false
		}
		seen[b.Role], tasks[b.TaskID], pods[b.PodUID] = true, true, true
	}
	return true
}

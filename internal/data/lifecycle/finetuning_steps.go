package lifecycle

import (
	"context"
	"reflect"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/util/validation"
)

var _ biz.FinetuningStepRepository = (*Repository)(nil)

func validFinetuningBinding(authority biz.RunAuthorityCandidate, binding biz.FinetuningStepBinding) bool {
	return (binding.Role == "export" || binding.Role == "verify") && binding.SpecHash == authority.SpecHash && binding.RunID == authority.RunID && binding.WorkflowUID == authority.WorkflowUID && validOpaqueID(binding.TaskID) && validOpaqueID(binding.PodUID) && len(validation.IsDNS1123Subdomain(binding.PodName)) == 0
}

func stepForRole(state biz.ExecutionRuntime, role string) *biz.FinetuningStepStart {
	for i := range state.FinetuningSteps {
		if state.FinetuningSteps[i].Binding.Role == role {
			return &state.FinetuningSteps[i]
		}
	}
	return nil
}

func (repository *Repository) ReserveFinetuningStep(ctx context.Context, authority biz.RunAuthorityCandidate, binding biz.FinetuningStepBinding) (biz.FinetuningStepReservation, error) {
	var start biz.FinetuningStepStart
	send := false
	state, _, err := repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		if current.execution.Snapshot.Kind != "FINETUNING" || current.execution.Snapshot.Finetuning == nil || !validFinetuningBinding(authority, binding) {
			return false, biz.ErrRuntimeConflict
		}
		if original := stepForRole(current.state, binding.Role); original != nil {
			if original.Binding != binding {
				return false, biz.ErrRuntimeConflict
			}
			start = *original
			return false, nil
		}
		for _, original := range current.state.FinetuningSteps {
			if original.Binding.TaskID == binding.TaskID || original.Binding.PodUID == binding.PodUID || original.Binding.PodName == binding.PodName {
				return false, biz.ErrRuntimeConflict
			}
		}
		if err := requireOpen(current); err != nil {
			return false, err
		}
		if current.state.Observation == nil || current.state.Observation.Outcome != "SUCCEEDED" || !current.state.Observation.WritersAbsent {
			return false, biz.ErrRuntimeNotReady
		}
		fine := current.execution.Snapshot.Finetuning
		budget := fine.Budgets.ExportTimeoutSeconds
		if binding.Role == "verify" {
			budget = fine.Budgets.VerifyTimeoutSeconds
			export := stepForRole(current.state, "export")
			if export == nil || export.Result == nil || export.Result.ExitCode != 0 {
				return false, biz.ErrRuntimeNotReady
			}
		}
		if budget == 0 {
			return false, biz.ErrRuntimeConflict
		}
		deadline := current.now.Add(time.Duration(budget) * time.Second)
		if current.execution.Snapshot.DeadlineAt.Before(deadline) {
			deadline = current.execution.Snapshot.DeadlineAt
		}
		start = biz.FinetuningStepStart{Binding: binding, AdmittedAt: current.now, DeadlineAt: deadline, ObservedCloseGeneration: current.state.CloseGeneration}
		current.state.FinetuningSteps = append(current.state.FinetuningSteps, start)
		send = true
		return true, nil
	})
	if err != nil {
		return biz.FinetuningStepReservation{}, err
	}
	return biz.FinetuningStepReservation{State: state, Start: start, SendPermit: send}, nil
}

func (repository *Repository) RecordFinetuningStepResult(ctx context.Context, authority biz.RunAuthorityCandidate, binding biz.FinetuningStepBinding, result biz.FinetuningStepResult) (biz.ExecutionRuntime, bool, error) {
	return repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		if !validFinetuningBinding(authority, binding) {
			return false, biz.ErrRuntimeConflict
		}
		start := stepForRole(current.state, binding.Role)
		if start == nil || start.Binding != binding {
			return false, biz.ErrRuntimeConflict
		}
		if original := start.Result; original != nil {
			result.ObservedAt = original.ObservedAt
			if !reflect.DeepEqual(*original, result) {
				return false, biz.ErrRuntimeConflict
			}
			return false, nil
		}
		result.ObservedAt = current.now
		if !validFinetuningResult(*start, result, current.now) {
			return false, biz.ErrRuntimeConflict
		}
		start.Result = &result
		return true, nil
	})
}

func validFinetuningResult(start biz.FinetuningStepStart, result biz.FinetuningStepResult, now time.Time) bool {
	if result.StartedAt.IsZero() || result.FinishedAt.IsZero() || result.StartedAt.Before(start.AdmittedAt) || result.FinishedAt.Before(result.StartedAt) || result.FinishedAt.After(now) || result.ObservedAt.IsZero() || result.ObservedAt.After(now) {
		return false
	}
	if result.ExitCode != 0 && result.ManifestRelativePath == "" && result.ManifestBytes == 0 && result.ManifestSHA256 == "" {
		return true
	}
	path := "reports/export/merged_manifest.json"
	if start.Binding.Role == "verify" {
		path = "reports/verify/verify.json"
	}
	return result.ManifestRelativePath == path && result.ManifestBytes > 0 && result.ManifestBytes <= 65536 && validDigest(result.ManifestSHA256)
}

// Reading stored facts validates the finite start inventory, not a permission.
func validFinetuningStarts(current *runtimeTransaction) bool {
	if len(current.state.FinetuningSteps) == 0 {
		return true
	}
	if current.execution.Snapshot.Kind != "FINETUNING" || current.execution.Snapshot.Finetuning == nil || len(current.state.FinetuningSteps) > 2 {
		return false
	}
	seen := make(map[string]bool)
	seenTasks, seenPods := make(map[string]bool), make(map[string]bool)
	for _, start := range current.state.FinetuningSteps {
		authority := biz.RunAuthorityCandidate{SpecHash: current.execution.SpecHash, RunID: start.Binding.RunID, WorkflowUID: start.Binding.WorkflowUID}
		if !validFinetuningBinding(authority, start.Binding) || seen[start.Binding.Role] || seenTasks[start.Binding.TaskID] || seenPods[start.Binding.PodUID] || start.ObservedCloseGeneration != 0 || start.AdmittedAt.IsZero() || !start.AdmittedAt.Before(start.DeadlineAt) || start.DeadlineAt.After(current.execution.Snapshot.DeadlineAt) || start.AdmittedAt.After(current.now) {
			return false
		}
		seen[start.Binding.Role] = true
		seenTasks[start.Binding.TaskID], seenPods[start.Binding.PodUID] = true, true
		budget := current.execution.Snapshot.Finetuning.Budgets.ExportTimeoutSeconds
		if start.Binding.Role == "verify" {
			budget = current.execution.Snapshot.Finetuning.Budgets.VerifyTimeoutSeconds
		}
		deadline := start.AdmittedAt.Add(time.Duration(budget) * time.Second)
		if current.execution.Snapshot.DeadlineAt.Before(deadline) {
			deadline = current.execution.Snapshot.DeadlineAt
		}
		if !start.DeadlineAt.Equal(deadline) || start.Result != nil && !validFinetuningResult(start, *start.Result, current.now) {
			return false
		}
	}
	return true
}

func finetuningStartsTerminated(state biz.ExecutionRuntime, authority biz.RunAuthorityCandidate, evidence biz.ManagedCloseEvidence) bool {
	for _, start := range state.FinetuningSteps {
		found := false
		for _, resource := range evidence.Resources {
			if resource.UID == start.Binding.PodUID && resource.Name == start.Binding.PodName && resource.Kind == "Pod" && resource.APIVersion == "v1" && resource.OwnerUID == authority.WorkflowUID && resource.Namespace == authority.NamespaceName && resource.APIObjectPresent && resource.Terminal && resource.ExitCode != nil {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func validExecutionCloseEvidence(execution biz.Execution, authority biz.RunAuthorityCandidate, reason string, evidence biz.ManagedCloseEvidence) bool {
	if execution.Snapshot.Kind != "FINETUNING" {
		return validCloseEvidence(authority, reason, evidence)
	}
	// Only the new execution type extends its skipped-task inventory. Reuse the
	// original Pod/controller validation and preserve the CPU rejection set.
	base := evidence
	base.SkippedTasks = nil
	seenNames, seenIDs := make(map[string]bool), make(map[string]bool)
	for _, skipped := range evidence.SkippedTasks {
		if seenNames[skipped.TaskName] || seenIDs[skipped.TaskID] || !validOpaqueID(skipped.TaskID) {
			return false
		}
		seenNames[skipped.TaskName], seenIDs[skipped.TaskID] = true, true
		switch skipped.TaskName {
		case "prepare", "train-wait", "collect", "publish":
			base.SkippedTasks = append(base.SkippedTasks, skipped)
		case "export", "verify", "register":
		default:
			return false
		}
	}
	if reason == "NATURAL_TERMINAL" && len(evidence.SkippedTasks) != 0 {
		return false
	}
	if evidence.OwnerTermination == nil && len(evidence.Resources)+len(evidence.SkippedTasks) < 7 {
		return false
	}
	return validCloseEvidence(authority, reason, base)
}

func executionDataPlaneTaskCount(execution biz.Execution) int {
	if execution.Snapshot.Kind == "FINETUNING" {
		return 7
	}
	return 4
}

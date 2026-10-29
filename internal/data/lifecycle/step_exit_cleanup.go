package lifecycle

import (
	"context"
	"reflect"
	"sort"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

var _ biz.StepExitCleanupRepository = (*Repository)(nil)

// BeginStepExitCleanup commits the complete original Pod exits under the same
// identity lock as CLOSED, before any finalizer release or exact-UID disposal.
func (repository *Repository) BeginStepExitCleanup(ctx context.Context, authority biz.RunAuthorityCandidate, generation uint64, evidence biz.ManagedCloseEvidence) (biz.ExecutionRuntime, error) {
	state, _, err := repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		if current.state.ClosedAt == nil || generation == 0 || generation != current.state.CloseGeneration || current.state.CloseEvidence == nil || current.state.CloseEvidence.NoDispatch {
			return false, biz.ErrRuntimeNotReady
		}
		if !biz.SameUnpreparedWorkspace(current.state.CloseEvidence.UnpreparedWorkspace, evidence.UnpreparedWorkspace) || evidence.OwnerTermination == nil || !validDigest(evidence.OwnerTermination.WorkflowNodesSHA256) || !validSandboxTerminationScope(current.state, evidence) || !validExecutionCloseEvidence(current.execution, authority, current.state.CloseReason, evidence) || evidence.ObservedAt.After(current.now) {
			return false, biz.ErrRuntimeNotReady
		}
		for _, original := range current.state.CloseEvidence.Resources {
			found := false
			for _, proposed := range evidence.Resources {
				found = found || sameExitedStepPod(original, proposed)
			}
			if !found {
				return false, biz.ErrRuntimeConflict
			}
		}
		if existing := current.state.StepExitCleanup; existing != nil {
			if !reflect.DeepEqual(existing.Evidence, evidence) {
				return false, biz.ErrRuntimeConflict
			}
			return false, nil
		}
		current.state.StepExitCleanup = &biz.StepExitCleanupReceipt{CloseGeneration: generation, Evidence: evidence, StartedAt: current.now, RemovedResources: []biz.RuntimeResource{}}
		return true, nil
	})
	return state, err
}

func (repository *Repository) CompleteStepExitCleanup(ctx context.Context, authority biz.RunAuthorityCandidate, generation uint64, removed []biz.RuntimeResource) (biz.ExecutionRuntime, error) {
	state, _, err := repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		receipt := current.state.StepExitCleanup
		if current.state.ClosedAt == nil || receipt == nil || generation == 0 || generation != current.state.CloseGeneration || receipt.CloseGeneration != generation || !allOriginalStepsRemoved(receipt.Evidence.Resources, removed) {
			return false, biz.ErrRuntimeConflict
		}
		if receipt.CompletedAt != nil {
			return false, nil
		}
		receipt.RemovedResources = append([]biz.RuntimeResource{}, removed...)
		sort.Slice(receipt.RemovedResources, func(i, j int) bool { return receipt.RemovedResources[i].UID < receipt.RemovedResources[j].UID })
		completed := current.now
		receipt.CompletedAt = &completed
		return true, nil
	})
	return state, err
}

func sameExitedStepPod(left, right biz.RuntimeResource) bool {
	if left.SandboxTermination != nil && right.SandboxTermination != nil {
		leftProof, rightProof := *left.SandboxTermination, *right.SandboxTermination
		leftProof.PodResourceVersion, rightProof.PodResourceVersion = "", ""
		left.SandboxTermination, right.SandboxTermination = &leftProof, &rightProof
	}
	if left.InitializationAbort != nil && right.InitializationAbort != nil {
		leftAbort, rightAbort := *left.InitializationAbort, *right.InitializationAbort
		// ResourceVersion protects each API mutation separately. Controller
		// bookkeeping may change it without changing the durable sandbox facts.
		leftAbort.PodResourceVersion, rightAbort.PodResourceVersion = "", ""
		left.InitializationAbort, right.InitializationAbort = &leftAbort, &rightAbort
	}
	return reflect.DeepEqual(left, right)
}

func allOriginalStepsRemoved(expected, removed []biz.RuntimeResource) bool {
	if len(expected) != len(removed) {
		return false
	}
	seen := map[string]bool{}
	for _, current := range removed {
		if current.APIObjectPresent || seen[current.UID] {
			return false
		}
		seen[current.UID] = true
		current.APIObjectPresent = true
		found := false
		for _, original := range expected {
			found = found || reflect.DeepEqual(original, current)
		}
		if !found {
			return false
		}
	}
	return true
}

func validStepExitCleanup(current *runtimeTransaction, authority biz.RunAuthorityCandidate) bool {
	receipt := current.state.StepExitCleanup
	if receipt == nil {
		return true
	}
	if current.state.ClosedAt == nil || receipt.CloseGeneration == 0 || receipt.CloseGeneration != current.state.CloseGeneration || receipt.StartedAt.IsZero() || receipt.StartedAt.Before(*current.state.ClosedAt) || receipt.StartedAt.After(current.now) || receipt.Evidence.ObservedAt.After(receipt.StartedAt) || receipt.Evidence.OwnerTermination == nil || !validDigest(receipt.Evidence.OwnerTermination.WorkflowNodesSHA256) || !validSandboxTerminationScope(current.state, receipt.Evidence) || !validExecutionCloseEvidence(current.execution, authority, current.state.CloseReason, receipt.Evidence) {
		return false
	}
	if !biz.SameUnpreparedWorkspace(current.state.CloseEvidence.UnpreparedWorkspace, receipt.Evidence.UnpreparedWorkspace) {
		return false
	}
	for _, original := range current.state.CloseEvidence.Resources {
		found := false
		for _, proposed := range receipt.Evidence.Resources {
			found = found || sameExitedStepPod(original, proposed)
		}
		if !found {
			return false
		}
	}
	if receipt.CompletedAt == nil {
		return len(receipt.RemovedResources) == 0
	}
	return !receipt.CompletedAt.Before(receipt.StartedAt) && !receipt.CompletedAt.After(current.now) && allOriginalStepsRemoved(receipt.Evidence.Resources, receipt.RemovedResources)
}

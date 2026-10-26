package lifecycle

import (
	"context"
	"reflect"
	"sort"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	lifecyclesql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle/sqlc"
)

var _ biz.ComputeCleanupRepository = (*Repository)(nil)

// AdmitComputeCleanupRetry commits fresh exact-scope logs and the one-shot
// authenticated operator audit under the same original aggregate lock.
func (repository *Repository) AdmitComputeCleanupRetry(ctx context.Context, owner biz.RunAuthorityCandidate, generation uint64, actor string, logs []biz.RecordedTrainingLog) (biz.ExecutionRuntime, error) {
	state, _, err := repository.mutate(ctx, owner, func(current *runtimeTransaction) (bool, error) {
		r := current.state.ComputeCleanup
		if !cpup01.ValidAuditActor(actor) || generation != current.state.CloseGeneration || r == nil {
			return false, biz.ErrCleanupBlocked
		}
		if r.OperatorRetry != nil {
			return false, nil
		}
		if !biz.ComputeCleanupRetryEligible(current.execution, owner, current.state) || !biz.ComputeCleanupLogsComplete(current.execution, current.state, logs) {
			return false, biz.ErrCleanupBlocked
		}
		r.OperatorRetry = &biz.ComputeCleanupOperatorRetry{Actor: actor, AdmittedAt: current.now, DeadlineAt: current.now.Add(biz.ComputeCleanupOperatorRetryWindow), OriginalAttempts: r.Attempts, OriginalPhase: r.Phase, OriginalError: r.LastError}
		r.Logs = append([]biz.RecordedTrainingLog{}, logs...)
		r.LogsReady = true
		r.Requested = append([]biz.RuntimeResource{}, r.Targets...)
		return true, nil
	})
	return state, err
}

func (repository *Repository) BeginComputeCleanup(ctx context.Context, owner biz.RunAuthorityCandidate, generation uint64) (biz.ExecutionRuntime, error) {
	state, _, err := repository.mutate(ctx, owner, func(current *runtimeTransaction) (bool, error) {
		targets, hash, err := biz.ComputeCleanupTargets(current.execution, owner, current.state)
		if err != nil || generation != current.state.CloseGeneration {
			return false, biz.ErrCleanupBlocked
		}
		if current.state.ComputeCleanup != nil {
			return false, nil
		}
		blocked, err := current.queries.HasUnresolvedManualCleanup(ctx, lifecyclesql.HasUnresolvedManualCleanupParams{TenantID: current.tenantID, ExecutionID: current.executionID})
		if err != nil {
			return false, biz.ErrPersistence
		}
		if blocked {
			return false, biz.ErrCleanupUncertain
		}
		current.state.ComputeCleanup = &biz.ComputeCleanupReceipt{CleanupReceipt: biz.CleanupReceipt{ExecutionID: current.execution.ExecutionID, PlanHash: hash, Phase: "STARTED", Actor: biz.ComputeCleanupActor, StartedAt: current.now, Requested: []biz.RuntimeResource{}, ConfirmedAbsent: []biz.RuntimeResource{}}, CloseGeneration: generation, Targets: targets, Logs: []biz.RecordedTrainingLog{}}
		return true, nil
	})
	return state, err
}

func (repository *Repository) StartComputeCleanupAttempt(ctx context.Context, owner biz.RunAuthorityCandidate, generation uint64) (biz.ExecutionRuntime, error) {
	state, _, err := repository.mutate(ctx, owner, func(current *runtimeTransaction) (bool, error) {
		receipt := current.state.ComputeCleanup
		if receipt == nil || generation != current.state.CloseGeneration || receipt.CloseGeneration != generation {
			return false, biz.ErrCleanupConflict
		}
		if receipt.Phase == "APPLIED" {
			return false, nil
		}
		if retry := receipt.OperatorRetry; retry != nil && (receipt.Attempts >= biz.ComputeCleanupAttemptLimit(receipt) || !current.now.Before(retry.DeadlineAt)) {
			return false, biz.ErrCleanupUncertain
		}
		if receipt.Attempts >= biz.ComputeCleanupAttemptLimit(receipt) {
			if receipt.Phase == "NEEDS_REVIEW" && receipt.LastError == "ATTEMPTS_EXHAUSTED" {
				return false, nil
			}
			receipt.Phase, receipt.LastError = "NEEDS_REVIEW", "ATTEMPTS_EXHAUSTED"
			return true, nil
		}
		receipt.Attempts++
		receipt.Phase, receipt.LastError = "STARTED", ""
		return true, nil
	})
	if err == nil && state.ComputeCleanup.Phase != "APPLIED" && state.ComputeCleanup.LastError == "ATTEMPTS_EXHAUSTED" {
		return state, biz.ErrCleanupUncertain
	}
	return state, err
}

func (repository *Repository) RecordComputeCleanupLogs(ctx context.Context, owner biz.RunAuthorityCandidate, generation uint64, logs []biz.RecordedTrainingLog) (biz.ExecutionRuntime, error) {
	state, _, err := repository.mutate(ctx, owner, func(current *runtimeTransaction) (bool, error) {
		receipt := current.state.ComputeCleanup
		if receipt == nil || generation != current.state.CloseGeneration || receipt.CloseGeneration != generation || receipt.Attempts == 0 || !biz.ComputeCleanupLogsComplete(current.execution, current.state, logs) {
			return false, biz.ErrCleanupConflict
		}
		if receipt.LogsReady {
			if !reflect.DeepEqual(receipt.Logs, logs) {
				return false, biz.ErrCleanupConflict
			}
			return false, nil
		}
		receipt.Logs = append([]biz.RecordedTrainingLog{}, logs...)
		receipt.LogsReady = true
		// This committed intent authorizes only these already-terminal original
		// identities. Recovery may observe their absence, never invent an exit.
		receipt.Requested = append([]biz.RuntimeResource{}, receipt.Targets...)
		return true, nil
	})
	return state, err
}

func (repository *Repository) RecordComputeCleanupResult(ctx context.Context, owner biz.RunAuthorityCandidate, generation uint64, attempt uint32, removed []biz.RuntimeResource, code string) (biz.ExecutionRuntime, error) {
	state, _, err := repository.mutate(ctx, owner, func(current *runtimeTransaction) (bool, error) {
		receipt := current.state.ComputeCleanup
		if receipt == nil || generation != current.state.CloseGeneration || receipt.CloseGeneration != generation {
			return false, biz.ErrCleanupConflict
		}
		if receipt.Phase == "APPLIED" {
			return false, nil
		}
		if attempt == 0 || attempt != receipt.Attempts || (code != "" && code != "BLOCKED" && code != "IDENTITY_CONFLICT" && code != "BOUNDARY_UNCERTAIN") || !biz.ComputeCleanupRemovedValid(receipt.Targets, removed, code == "") || (len(removed) != 0 && !receipt.LogsReady) {
			return false, biz.ErrCleanupConflict
		}
		byUID := map[string]biz.RuntimeResource{}
		for _, resource := range append(append([]biz.RuntimeResource{}, receipt.ConfirmedAbsent...), removed...) {
			byUID[resource.UID] = resource
		}
		receipt.ConfirmedAbsent = []biz.RuntimeResource{}
		for _, resource := range byUID {
			receipt.ConfirmedAbsent = append(receipt.ConfirmedAbsent, resource)
		}
		sort.Slice(receipt.ConfirmedAbsent, func(i, j int) bool { return receipt.ConfirmedAbsent[i].UID < receipt.ConfirmedAbsent[j].UID })
		receipt.LastError = code
		if code == "" {
			receipt.Phase, receipt.CompletedAt = "APPLIED", current.now
		} else {
			receipt.Phase = "NEEDS_REVIEW"
		}
		return true, nil
	})
	return state, err
}

func validComputeCleanup(current *runtimeTransaction, owner biz.RunAuthorityCandidate) bool {
	receipt := current.state.ComputeCleanup
	if receipt == nil {
		return true
	}
	targets, hash, err := biz.ComputeCleanupTargets(current.execution, owner, current.state)
	if err != nil || receipt.ExecutionID != current.execution.ExecutionID || receipt.Actor != biz.ComputeCleanupActor || receipt.CloseGeneration != current.state.CloseGeneration || receipt.PlanHash != hash || !reflect.DeepEqual(receipt.Targets, targets) || receipt.StartedAt.IsZero() || receipt.StartedAt.Before(*current.state.ClosedAt) || receipt.StartedAt.After(current.now) || receipt.Attempts > biz.ComputeCleanupAttemptLimit(receipt) || !biz.ComputeCleanupRemovedValid(targets, receipt.ConfirmedAbsent, receipt.Phase == "APPLIED") || !receipt.ReconciledAt.IsZero() || receipt.ReconciledFromPhase != "" || len(receipt.ResolvedAbsent) != 0 {
		return false
	}
	if r := receipt.OperatorRetry; r != nil {
		if !cpup01.ValidAuditActor(r.Actor) || r.AdmittedAt.Before(receipt.StartedAt) || r.AdmittedAt.After(current.now) || r.DeadlineAt.Sub(r.AdmittedAt) != biz.ComputeCleanupOperatorRetryWindow || r.OriginalAttempts != biz.ComputeCleanupMaximumAttempts || r.OriginalPhase != "NEEDS_REVIEW" || r.OriginalError != "ATTEMPTS_EXHAUSTED" || receipt.Attempts < r.OriginalAttempts || !receipt.LogsReady {
			return false
		}
	}
	if receipt.LogsReady {
		if receipt.Attempts == 0 || !biz.ComputeCleanupLogsComplete(current.execution, current.state, receipt.Logs) || !reflect.DeepEqual(receipt.Requested, targets) {
			return false
		}
	} else if len(receipt.Logs) != 0 || len(receipt.Requested) != 0 || len(receipt.ConfirmedAbsent) != 0 {
		return false
	}
	switch receipt.Phase {
	case "STARTED":
		return receipt.CompletedAt.IsZero() && receipt.LastError == ""
	case "NEEDS_REVIEW":
		return receipt.Attempts > 0 && receipt.CompletedAt.IsZero() && (receipt.LastError == "BLOCKED" || receipt.LastError == "IDENTITY_CONFLICT" || receipt.LastError == "BOUNDARY_UNCERTAIN" || receipt.LastError == "ATTEMPTS_EXHAUSTED")
	case "APPLIED":
		return receipt.LogsReady && receipt.Attempts > 0 && receipt.LastError == "" && !receipt.CompletedAt.IsZero() && !receipt.CompletedAt.Before(receipt.StartedAt) && !receipt.CompletedAt.After(current.now)
	}
	return false
}

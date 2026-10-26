package biz

import (
	"context"
	"errors"
	"time"
)

// WithComputeCleanup explicitly enables the existing owner's post-close work.
// Older test assemblies retain their scoped step-only behavior; production
// must provide both the persistent receipt port and the real boundary.
func (closer *ExecutionCloser) WithComputeCleanup(boundary ComputeCleanupBoundary) (*ExecutionCloser, error) {
	if closer == nil || closer.runtime == nil || closer.cleanup == nil || boundary == nil {
		return nil, ErrRuntimeNotReady
	}
	if _, ok := closer.runtime.repository.(ComputeCleanupRepository); !ok {
		return nil, ErrRuntimeNotReady
	}
	closer.compute = boundary
	return closer, nil
}

func (closer *ExecutionCloser) reconcileComputeCleanup(ctx context.Context, result ManagedRuntimeResult, owner RunAuthorityCandidate, replayed bool) (ManagedRuntimeResult, error) {
	state := result.Runtime
	if closer.compute == nil || state.CloseEvidence == nil || state.CloseEvidence.NoDispatch {
		return runtimeResult(result, state, replayed)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	repository, ok := closer.runtime.repository.(ComputeCleanupRepository)
	if !ok || state.StepExitCleanup == nil || state.StepExitCleanup.CompletedAt == nil {
		return result, ErrRuntimeNotReady
	}
	if state.ComputeCleanup == nil {
		var err error
		state, err = repository.BeginComputeCleanup(ctx, owner, state.CloseGeneration)
		if err != nil {
			return result, err
		}
	}
	result.Runtime = state
	if state.ComputeCleanup.Phase == "APPLIED" {
		return runtimeResult(result, state, replayed)
	}
	// An exhausted or expired attempt grants no further deletion. It may still
	// finish from fresh absence of every original target, retaining its audit.
	receipt := state.ComputeCleanup
	if receipt.LogsReady && receipt.Attempts > 0 && (receipt.Attempts >= ComputeCleanupAttemptLimit(receipt) || receipt.OperatorRetry != nil && !time.Now().Before(receipt.OperatorRetry.DeadlineAt)) {
		absent, err := closer.compute.ObserveComputeCleanupAbsence(ctx, result.Execution, owner, state)
		if err != nil {
			return result, err
		}
		stored, err := repository.RecordComputeCleanupResult(ctx, owner, state.CloseGeneration, receipt.Attempts, absent, "")
		if err != nil {
			return result, err
		}
		result.Runtime = stored
		return runtimeResult(result, stored, replayed)
	}
	state, err := repository.StartComputeCleanupAttempt(ctx, owner, state.CloseGeneration)
	if err != nil {
		return result, err
	}
	result.Runtime = state
	if state.ComputeCleanup.Phase == "APPLIED" {
		return runtimeResult(result, state, replayed)
	}
	attempt := state.ComputeCleanup.Attempts
	var removed []RuntimeResource
	if !state.ComputeCleanup.LogsReady {
		logs, captureErr := closer.compute.CaptureComputeCleanupLogs(ctx, result.Execution, owner, state)
		if captureErr == nil {
			var logged ExecutionRuntime
			logged, captureErr = repository.RecordComputeCleanupLogs(ctx, owner, state.CloseGeneration, logs)
			if captureErr == nil {
				state, result.Runtime = logged, logged
			}
		}
		err = captureErr
	}
	if err == nil {
		removed, err = closer.compute.ReconcileComputeCleanup(ctx, result.Execution, owner, state)
	}
	errorCode := ""
	if err != nil {
		errorCode = "BOUNDARY_UNCERTAIN"
		if errors.Is(err, ErrRuntimeConflict) || errors.Is(err, ErrCleanupConflict) {
			errorCode = "IDENTITY_CONFLICT"
		} else if errors.Is(err, ErrCleanupBlocked) {
			errorCode = "BLOCKED"
		}
	}
	stored, recordErr := repository.RecordComputeCleanupResult(ctx, owner, state.CloseGeneration, attempt, removed, errorCode)
	if recordErr != nil {
		return result, errors.Join(err, recordErr)
	}
	result.Runtime = stored
	if stored.ComputeCleanup.Phase == "APPLIED" {
		return runtimeResult(result, stored, replayed)
	}
	return result, err
}

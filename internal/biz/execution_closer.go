package biz

import (
	"context"
	"errors"
	"strings"
)

// These ports are called by the configured execution owner, after a durable
// creation fence. They do not authorize a caller-supplied Run or workspace.
type ManagedRunCloser interface {
	StopManagedRun(context.Context, PipelineDispatchPlan, RunAuthorityCandidate) error
}

type OwnerWriterVerifier interface {
	VerifyOwnerWritersAbsent(context.Context, Execution, RunAuthorityCandidate, *WorkspaceBinding) (ManagedCloseEvidence, error)
}

type StepExitCleanupRepository interface {
	BeginStepExitCleanup(context.Context, RunAuthorityCandidate, uint64, ManagedCloseEvidence) (ExecutionRuntime, error)
	CompleteStepExitCleanup(context.Context, RunAuthorityCandidate, uint64, []RuntimeResource) (ExecutionRuntime, error)
}

// StepExitCleanupBoundary operates only on the exact original Pods whose exits
// have already been committed. Missing Pods are never evidence for closing.
type StepExitCleanupBoundary interface {
	ReconcileStepExitCleanup(context.Context, Execution, RunAuthorityCandidate, *WorkspaceBinding, StepExitCleanupReceipt) ([]RuntimeResource, error)
}

type RecordedOwnerWriterVerifier interface {
	VerifyRecordedOwnerWritersAbsent(context.Context, Execution, RunAuthorityCandidate, *WorkspaceBinding, ManagedCloseEvidence) error
}

type UndispatchedCloser interface {
	CloseUndispatched(context.Context, string, string) (ExecutionRuntime, error)
}

type OwnerCloseRecovery interface {
	FenceOwnerClose(context.Context, string, string) (ExecutionRuntime, error)
	RecordClosingRun(context.Context, RunAuthorityCandidate) (ExecutionRuntime, error)
	MarkOwnerCloseReview(context.Context, string, string, string) (ExecutionRuntime, error)
	RecordClosingRunAmbiguity(context.Context, PipelineDispatch, []string) (ExecutionRuntime, error)
}

// AmbiguousClosingRunsError contains independently verified KFP Run identities.
// The owner retains them in the original submission history before failing closed.
type AmbiguousClosingRunsError struct{ RunIDs []string }

func (*AmbiguousClosingRunsError) Error() string { return "MULTIPLE_KFP_RUNS" }
func (*AmbiguousClosingRunsError) Unwrap() error { return ErrRunAuthorityConflict }

type ClosingRunVerifier interface {
	VerifyClosingRun(context.Context, Execution, PipelineDispatch, string) (RunAuthorityCandidate, error)
}

type ExecutionCloser struct {
	runtime *ManagedRuntime
	runs    ManagedRunCloser
	writers OwnerWriterVerifier
	cleanup StepExitCleanupBoundary
	compute ComputeCleanupBoundary
}

func NewExecutionCloser(runtime *ManagedRuntime, runs ManagedRunCloser, writers OwnerWriterVerifier, cleanup ...StepExitCleanupBoundary) (*ExecutionCloser, error) {
	if runtime == nil || runs == nil || writers == nil || len(cleanup) > 1 {
		return nil, ErrRuntimeNotReady
	}
	closer := &ExecutionCloser{runtime: runtime, runs: runs, writers: writers}
	if len(cleanup) == 1 {
		if _, ok := runtime.repository.(StepExitCleanupRepository); !ok || cleanup[0] == nil {
			return nil, ErrRuntimeNotReady
		}
		closer.cleanup = cleanup[0]
	}
	return closer, nil
}

// Reconcile resumes closing the original durable execution. It must never
// advance normal pipeline steps or reconstruct a creation permit.
func (closer *ExecutionCloser) Reconcile(ctx context.Context, tenant, execution string) (ManagedRuntimeResult, error) {
	if ctx == nil || !validAdmissionID(tenant) || !validAdmissionID(execution) || strings.ToLower(tenant) != tenant || strings.ToLower(execution) != execution {
		return ManagedRuntimeResult{}, ErrInvalidAdmission
	}
	if closer == nil || closer.runtime == nil || closer.runs == nil || closer.writers == nil {
		return ManagedRuntimeResult{}, ErrRuntimeNotReady
	}
	if err := ctx.Err(); err != nil {
		return ManagedRuntimeResult{}, err
	}
	runtime := closer.runtime
	admitted, err := runtime.steps.executions.Get(ctx, tenant, execution)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	authority, err := runtime.steps.repository.GetRunAuthority(ctx, tenant, execution)
	if errors.Is(err, ErrExecutionNotFound) {
		// Early owner recovery intentionally never grants normal step authority.
		// Its already-committed close and complete exit receipt remain usable
		// after step Pods disappear; rediscovering that owner would lose liveness.
		recorded, readErr := runtime.repository.GetRuntime(ctx, tenant, execution)
		if readErr != nil && !errors.Is(readErr, ErrExecutionNotFound) {
			return ManagedRuntimeResult{}, readErr
		}
		if readErr == nil && recorded.CloseAuthority != nil {
			if _, _, proofErr := ComputeCleanupTargets(admitted, *recorded.CloseAuthority, recorded); proofErr == nil {
				authority = RunAuthority{RunAuthorityCandidate: *recorded.CloseAuthority, OwnerRevision: recorded.OwnerRevision}
				err = nil
			}
		}
	}
	if errors.Is(err, ErrExecutionNotFound) {
		unbound, ok := runtime.repository.(UndispatchedCloser)
		if !ok {
			return ManagedRuntimeResult{}, ErrRuntimeNotReady
		}
		state, err := unbound.CloseUndispatched(ctx, tenant, execution)
		if err == nil {
			return runtimeResult(ManagedRuntimeResult{Execution: admitted}, state, false)
		}
		if !errors.Is(err, ErrRuntimeNotReady) {
			return ManagedRuntimeResult{}, err
		}
		recovery, ok := runtime.repository.(OwnerCloseRecovery)
		if !ok {
			return ManagedRuntimeResult{}, ErrRuntimeNotReady
		}
		if _, err := recovery.FenceOwnerClose(ctx, tenant, execution); err != nil {
			return ManagedRuntimeResult{}, err
		}
		dispatch, err := runtime.steps.repository.Get(ctx, tenant, execution)
		if err != nil {
			return ManagedRuntimeResult{}, err
		}
		authority, err = closer.recoverRun(ctx, admitted, dispatch, "")
		if err != nil {
			return ManagedRuntimeResult{}, err
		}
	} else if err != nil {
		return ManagedRuntimeResult{}, err
	}
	dispatch, err := runtime.steps.repository.Get(ctx, tenant, execution)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	plan, owner := dispatch.Plan, authority.RunAuthorityCandidate
	if owner.TenantID != admitted.TenantID || owner.ExecutionID != admitted.ExecutionID || owner.OperationID != admitted.OperationID || owner.SpecHash != admitted.SpecHash || owner.AttemptID != dispatch.AttemptID || owner.PlanHash != dispatch.PlanHash || owner.NamespaceName != admitted.Snapshot.Environment.NamespaceName || owner.NamespaceUID != admitted.Snapshot.Environment.NamespaceUID || plan.TenantID != admitted.TenantID || plan.ExecutionID != admitted.ExecutionID || plan.OperationID != admitted.OperationID || plan.SpecHash != admitted.SpecHash || plan.Environment != admitted.Snapshot.Environment {
		return ManagedRuntimeResult{}, ErrRunAuthorityConflict
	}
	state, err := runtime.repository.GetRuntime(ctx, tenant, execution)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	result := ManagedRuntimeResult{Execution: admitted, Authority: authority, Runtime: state}
	if state.ClosedAt != nil {
		return closer.reconcileStepExits(ctx, result, owner, true)
	}
	if state.CloseGeneration == 0 || state.CloseReason == "NATURAL_TERMINAL" {
		// The database clock checks the original frozen deadline while holding
		// the creation lock. An existing natural fence escalates only at expiry.
		state, _, err = runtime.repository.RequestRuntimeClose(ctx, owner, "DEADLINE")
		if err != nil {
			return ManagedRuntimeResult{}, err
		}
	}
	result.Runtime = state
	var recoveryErr error
	if dispatch.State != PipelineDispatchConfirmed || len(dispatch.ConfirmedRuns) == 0 {
		recovered, findErr := closer.recoverRun(ctx, admitted, dispatch, owner.RunID)
		recoveryErr = findErr
		if findErr == nil && recovered.RunAuthorityCandidate != owner {
			return ManagedRuntimeResult{}, ErrRunAuthorityConflict
		}
		if findErr == nil {
			authority = recovered
			state, err = runtime.repository.GetRuntime(ctx, tenant, execution)
			if err != nil {
				return ManagedRuntimeResult{}, err
			}
			dispatch, err = runtime.steps.repository.Get(ctx, tenant, execution)
			if err != nil {
				return ManagedRuntimeResult{}, err
			}
			result.Authority, result.Runtime = authority, state
		}
	}
	// A stopped KFP waiter cannot stop external training by itself. Even when
	// KFP is temporarily unavailable, make the independent training stop attempt
	// after the committed fence; neither failure may imply CLOSED.
	runErr := recoveryErr
	if state.CloseReason != "NATURAL_TERMINAL" {
		runErr = errors.Join(runErr, closer.runs.StopManagedRun(ctx, plan, owner))
	}
	if fact := state.RuntimeTemplate; fact != nil && fact.Binding == nil && len(fact.Attempts) > 0 && fact.Attempts[len(fact.Attempts)-1].NotSentAt == nil {
		if state.Workspace == nil || state.TrainingWindow == nil {
			return ManagedRuntimeResult{}, ErrRuntimeConflict
		}
		original, err := FreezeTrainingPlan(admitted, *state.Workspace, state.TrainingWindow.AdmittedAt)
		if err != nil {
			return ManagedRuntimeResult{}, err
		}
		binding, findErr := runtime.trainer.FindRuntime(ctx, original)
		if findErr != nil {
			if _, err := runtime.repository.RecordRuntimeReview(ctx, owner); err != nil {
				return ManagedRuntimeResult{}, errors.Join(runErr, findErr, err)
			}
			return ManagedRuntimeResult{}, errors.Join(runErr, ErrTrainingUncertain, findErr)
		}
		state, err = runtime.repository.RecordRuntimeBinding(ctx, owner, binding)
		if err != nil {
			return ManagedRuntimeResult{}, errors.Join(runErr, err)
		}
		result.Runtime = state
	}
	if state.TrainingRejection != nil {
		if err := runtime.verifyRejectedTrainingAbsent(ctx, state); err != nil {
			return ManagedRuntimeResult{}, closer.review(ctx, tenant, execution, "TRAINJOB_CREATE_UNRESOLVED", errors.Join(runErr, err))
		}
	} else if state.Training != nil {
		if state.TrainingHandle == nil {
			find := runtime.trainer.FindTraining
			if recovery, ok := runtime.trainer.(TrainingCloseRecovery); ok {
				find = recovery.FindTrainingForClose
			}
			handle, findErr := find(ctx, *state.Training)
			if findErr != nil {
				return ManagedRuntimeResult{}, closer.review(ctx, tenant, execution, "TRAINJOB_CREATE_UNRESOLVED", errors.Join(runErr, findErr))
			}
			state, err = runtime.repository.RecordTrainingHandle(ctx, owner, handle)
			if err != nil {
				return ManagedRuntimeResult{}, errors.Join(runErr, err)
			}
		}
		if state.Observation == nil || !state.Observation.WritersAbsent {
			// Suspend may make kubelet container logs disappear before the next
			// terminal observation. Offer a bounded live tail for durable owner
			// storage first; capture failure must not prevent a legal Stop.
			result.Runtime = state
			if observed, observeErr := runtime.observe(ctx, result); observeErr == nil {
				state = observed
			}
			if err = runtime.trainer.StopTraining(ctx, *state.Training, *state.TrainingHandle); err != nil {
				return ManagedRuntimeResult{}, errors.Join(runErr, err)
			}
		}
		result.Runtime = state
		state, err = runtime.observe(ctx, result)
		if err != nil {
			if errors.Is(err, ErrTrainingLogsUnavailable) {
				return ManagedRuntimeResult{}, closer.review(ctx, tenant, execution, "TRAINING_LOGS_UNAVAILABLE", errors.Join(runErr, err))
			}
			return ManagedRuntimeResult{}, errors.Join(runErr, err)
		}
		if state.Observation == nil || !state.Observation.WritersAbsent {
			return ManagedRuntimeResult{}, errors.Join(runErr, ErrRuntimeNotReady)
		}
	}
	if runErr != nil {
		return ManagedRuntimeResult{}, runErr
	}
	// Extra observed Runs cannot be erased by closing only the authority. They
	// remain a reconciliation concern, with the shared creation fence retained.
	for _, run := range dispatch.ConfirmedRuns {
		if run.RunID != owner.RunID {
			return ManagedRuntimeResult{}, closer.review(ctx, tenant, execution, "MULTIPLE_RUNS", ErrRunAuthorityConflict)
		}
	}
	evidence, err := closer.writers.VerifyOwnerWritersAbsent(ctx, admitted, owner, state.Workspace)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	observation := TrainingRuntimeObservation{}
	if state.Observation != nil {
		observation = *state.Observation
	}
	state, err = runtime.repository.ConfirmRuntimeClosed(ctx, owner, state.CloseGeneration, observation, evidence)
	if err != nil {
		if errors.Is(err, ErrRunAuthorityConflict) {
			return ManagedRuntimeResult{}, closer.review(ctx, tenant, execution, "MULTIPLE_RUNS", err)
		}
		return ManagedRuntimeResult{}, err
	}
	result.Runtime = state
	return closer.reconcileStepExits(ctx, result, owner, false)
}

func (closer *ExecutionCloser) reconcileStepExits(ctx context.Context, result ManagedRuntimeResult, owner RunAuthorityCandidate, replayed bool) (ManagedRuntimeResult, error) {
	state := result.Runtime
	if closer.cleanup == nil {
		return runtimeResult(result, state, replayed)
	}
	if state.CloseEvidence == nil {
		return result, ErrRuntimeNotReady
	}
	if state.CloseEvidence.NoDispatch || state.StepExitCleanup != nil && state.StepExitCleanup.CompletedAt != nil {
		return closer.reconcileComputeCleanup(ctx, result, owner, replayed)
	}
	repository, ok := closer.runtime.repository.(StepExitCleanupRepository)
	if !ok || state.ClosedAt == nil {
		return result, ErrRuntimeNotReady
	}
	if state.StepExitCleanup == nil {
		// Managed close excluded its still-running control Pod. Obtain the full
		// terminal owner graph independently before committing its cleanup plan.
		evidence, err := closer.writers.VerifyOwnerWritersAbsent(ctx, result.Execution, owner, state.Workspace)
		if err != nil {
			return result, err
		}
		state, err = repository.BeginStepExitCleanup(ctx, owner, state.CloseGeneration, evidence)
		if err != nil {
			return result, err
		}
		result.Runtime = state
	}
	removed, err := closer.cleanup.ReconcileStepExitCleanup(ctx, result.Execution, owner, state.Workspace, *state.StepExitCleanup)
	if err != nil {
		return result, err
	}
	state, err = repository.CompleteStepExitCleanup(ctx, owner, state.CloseGeneration, removed)
	if err != nil {
		return result, err
	}
	result.Runtime = state
	return closer.reconcileComputeCleanup(ctx, result, owner, replayed)
}

func (closer *ExecutionCloser) recoverRun(ctx context.Context, admitted Execution, dispatch PipelineDispatch, runID string) (RunAuthority, error) {
	recovery, ok := closer.runtime.repository.(OwnerCloseRecovery)
	verifier, verifies := closer.writers.(ClosingRunVerifier)
	if !ok || !verifies {
		return RunAuthority{}, ErrRuntimeNotReady
	}
	if len(dispatch.ConfirmedRuns) > 1 {
		return RunAuthority{}, closer.review(ctx, admitted.TenantID, admitted.ExecutionID, "MULTIPLE_RUNS", ErrRunAuthorityConflict)
	}
	if runID == "" && len(dispatch.ConfirmedRuns) == 1 {
		runID = dispatch.ConfirmedRuns[0].RunID
	}
	owner, err := verifier.VerifyClosingRun(ctx, admitted, dispatch, runID)
	if err != nil {
		var ambiguous *AmbiguousClosingRunsError
		if errors.As(err, &ambiguous) {
			_, recordErr := recovery.RecordClosingRunAmbiguity(ctx, dispatch, ambiguous.RunIDs)
			return RunAuthority{}, errors.Join(err, recordErr)
		}
		return RunAuthority{}, closer.review(ctx, admitted.TenantID, admitted.ExecutionID, "KFP_CREATE_UNRESOLVED", err)
	}
	state, err := recovery.RecordClosingRun(ctx, owner)
	if err != nil {
		return RunAuthority{}, err
	}
	return RunAuthority{RunAuthorityCandidate: owner, OwnerRevision: state.OwnerRevision}, nil
}

func (closer *ExecutionCloser) review(ctx context.Context, tenant, execution, reason string, cause error) error {
	recovery, ok := closer.runtime.repository.(OwnerCloseRecovery)
	if !ok {
		return cause
	}
	_, err := recovery.MarkOwnerCloseReview(ctx, tenant, execution, reason)
	return errors.Join(cause, err)
}

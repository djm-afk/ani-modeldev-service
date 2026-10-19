package biz

import (
	"context"
	"time"
)

// FinetuningStepBinding fixes the original authenticated KFP task and Pod. It
// is not a bearer capability and cannot transfer a consumed start to a retry.
type FinetuningStepBinding struct {
	Role, SpecHash, RunID, WorkflowUID, TaskID, PodName, PodUID string
}

type FinetuningStepResult struct {
	StartedAt, FinishedAt, ObservedAt time.Time
	ExitCode                          int32
	ManifestRelativePath              string
	ManifestBytes                     int64
	ManifestSHA256                    string
}

type FinetuningStepStart struct {
	Binding                 FinetuningStepBinding
	AdmittedAt, DeadlineAt  time.Time
	ObservedCloseGeneration uint64
	Result                  *FinetuningStepResult `json:",omitempty"`
}

type FinetuningStepReservation struct {
	State      ExecutionRuntime
	Start      FinetuningStepStart
	SendPermit bool
}

// Kept separate from the original CPU runtime repository: existing CPU
// implementations do not acquire a new optional compute capability.
type FinetuningStepRepository interface {
	ReserveFinetuningStep(context.Context, RunAuthorityCandidate, FinetuningStepBinding) (FinetuningStepReservation, error)
	RecordFinetuningStepResult(context.Context, RunAuthorityCandidate, FinetuningStepBinding, FinetuningStepResult) (ExecutionRuntime, bool, error)
}

type FinetuningStepPredecessorVerifier interface {
	VerifyFinetuningStepPredecessor(context.Context, Execution, ManagedStepAssociation, string, ExecutionRuntime) error
}

func (runtime *ManagedRuntime) BeginFinetuningStep(ctx context.Context, token string, request BeginManagedExecutionRequest, role string) (FinetuningStepReservation, error) {
	if role != "export" && role != "verify" {
		return FinetuningStepReservation{}, ErrManagedStepUnauthorized
	}
	result, err := runtime.Configuration(ctx, token, request, role)
	if err != nil {
		return FinetuningStepReservation{}, err
	}
	if result.Execution.Snapshot.Kind != "FINETUNING" || result.Execution.Snapshot.Finetuning == nil || result.TaskID == "" {
		return FinetuningStepReservation{}, ErrRuntimeConflict
	}
	repository, ok := runtime.repository.(FinetuningStepRepository)
	if !ok {
		return FinetuningStepReservation{}, ErrRuntimeNotReady
	}
	a := request.Association
	binding := FinetuningStepBinding{Role: role, SpecHash: result.Execution.SpecHash, RunID: a.RunID, WorkflowUID: a.WorkflowUID, TaskID: result.TaskID, PodName: a.PodName, PodUID: a.PodUID}
	// A replay must still authenticate, but does not depend on re-running an
	// already consumed predecessor or reconstruct permission from stored facts.
	for _, start := range result.Runtime.FinetuningSteps {
		if start.Binding.Role == role {
			return repository.ReserveFinetuningStep(ctx, result.Authority.RunAuthorityCandidate, binding)
		}
	}
	if role == "export" {
		state, err := runtime.observe(ctx, result)
		if err != nil {
			return FinetuningStepReservation{}, err
		}
		result.Runtime = state
	}
	verifier, ok := runtime.publications.(FinetuningStepPredecessorVerifier)
	if !ok {
		return FinetuningStepReservation{}, ErrRuntimeNotReady
	}
	if err := verifier.VerifyFinetuningStepPredecessor(ctx, result.Execution, a, role, result.Runtime); err != nil {
		return FinetuningStepReservation{}, err
	}
	return repository.ReserveFinetuningStep(ctx, result.Authority.RunAuthorityCandidate, binding)
}

// Result reports belong to the original start even after Stop. This retains
// late process facts; independent Pod termination is still required for close.
func (runtime *ManagedRuntime) ReportFinetuningStepResult(ctx context.Context, token string, request BeginManagedExecutionRequest, role string, report FinetuningStepResult) (ManagedRuntimeResult, error) {
	if role != "export" && role != "verify" {
		return ManagedRuntimeResult{}, ErrManagedStepUnauthorized
	}
	result, err := runtime.Configuration(ctx, token, request, role)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	repository, ok := runtime.repository.(FinetuningStepRepository)
	if !ok || result.Execution.Snapshot.Kind != "FINETUNING" || result.TaskID == "" {
		return ManagedRuntimeResult{}, ErrRuntimeConflict
	}
	a := request.Association
	binding := FinetuningStepBinding{Role: role, SpecHash: result.Execution.SpecHash, RunID: a.RunID, WorkflowUID: a.WorkflowUID, TaskID: result.TaskID, PodName: a.PodName, PodUID: a.PodUID}
	state, replayed, err := repository.RecordFinetuningStepResult(ctx, result.Authority.RunAuthorityCandidate, binding, report)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	return runtimeResult(result, state, replayed)
}

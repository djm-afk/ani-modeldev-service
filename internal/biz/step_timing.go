package biz

import (
	"context"
	"time"
)

// ManagedStepTiming is a finite authenticated role fact, not a command engine.
// TRAIN and export/verify retain their existing one-shot computation records.
type ManagedStepTiming struct {
	Binding                FinetuningStepBinding
	AdmittedAt, DeadlineAt time.Time
}

type ManagedStepTimingRepository interface {
	ReserveManagedStep(context.Context, RunAuthorityCandidate, FinetuningStepBinding) (ManagedStepTiming, bool, error)
}

type ManagedStepPredecessorVerifier interface {
	VerifyManagedStepPredecessor(context.Context, Execution, ManagedStepAssociation, string, ExecutionRuntime) error
}

func TimingForManagedRole(state ExecutionRuntime, role string) *ManagedStepTiming {
	for _, timing := range state.StepTimings {
		if timing.Binding.Role == role {
			value := timing
			return &value
		}
	}
	return nil
}

func (runtime *ManagedRuntime) BeginManagedStep(ctx context.Context, token string, request BeginManagedExecutionRequest, role string) (ManagedStepTiming, bool, error) {
	if role != "prepare" && role != "collect" && role != "publish" && role != "register" {
		return ManagedStepTiming{}, false, ErrManagedStepUnauthorized
	}
	result, err := runtime.Configuration(ctx, token, request, role)
	if err != nil {
		return ManagedStepTiming{}, false, err
	}
	repository, ok := runtime.repository.(ManagedStepTimingRepository)
	if !ok || result.TaskID == "" {
		return ManagedStepTiming{}, false, ErrRuntimeNotReady
	}
	a := request.Association
	binding := FinetuningStepBinding{Role: role, SpecHash: request.SpecHash, RunID: a.RunID, WorkflowUID: a.WorkflowUID, TaskID: result.TaskID, PodName: a.PodName, PodUID: a.PodUID}
	if TimingForManagedRole(result.Runtime, role) == nil && role != "prepare" {
		if role == "collect" {
			state, err := runtime.observe(ctx, result)
			if err != nil {
				return ManagedStepTiming{}, false, err
			}
			result.Runtime = state
		}
		verifier, ok := runtime.publications.(ManagedStepPredecessorVerifier)
		if !ok {
			return ManagedStepTiming{}, false, ErrRuntimeNotReady
		}
		if err := verifier.VerifyManagedStepPredecessor(ctx, result.Execution, a, role, result.Runtime); err != nil {
			return ManagedStepTiming{}, false, err
		}
	}
	return repository.ReserveManagedStep(ctx, result.Authority.RunAuthorityCandidate, binding)
}

// ActiveBusinessDeadline uses only original committed role windows. Successor
// starts require independent predecessor exits; a reported result alone cannot
// stop the expiry owner from fencing an unfinished computation.
func ActiveBusinessDeadline(execution Execution, state ExecutionRuntime) time.Time {
	deadline := execution.Snapshot.DeadlineAt
	consider := func(at time.Time) {
		if !at.IsZero() && at.Before(deadline) {
			deadline = at
		}
	}
	for _, timing := range state.StepTimings {
		active := false
		switch timing.Binding.Role {
		case "prepare":
			active = state.TrainingWindow == nil
		case "collect":
			active = TimingForManagedRole(state, "publish") == nil
		case "publish":
			if execution.Snapshot.Kind == "FINETUNING" {
				active = TimingForManagedRole(state, "register") == nil
			} else {
				active = state.Publication == nil
			}
		case "register":
			active = state.FinetuningRegistration == nil || state.FinetuningRegistration.State != RegistrationStateReady && state.FinetuningRegistration.State != RegistrationStateUnavailable
		}
		if active {
			consider(timing.DeadlineAt)
		}
	}
	if state.TrainingWindow != nil && (state.Observation == nil || !state.Observation.WritersAbsent || state.Observation.Outcome == "RUNNING" || state.Observation.Outcome == "UNKNOWN") {
		consider(state.TrainingWindow.DeadlineAt)
	}
	for _, start := range state.FinetuningSteps {
		active := start.Binding.Role == "verify" && TimingForManagedRole(state, "collect") == nil
		if start.Binding.Role == "export" {
			active = true
			for _, next := range state.FinetuningSteps {
				if next.Binding.Role == "verify" {
					active = false
				}
			}
		}
		if active {
			consider(start.DeadlineAt)
		}
	}
	return deadline
}

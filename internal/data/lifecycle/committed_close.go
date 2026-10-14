package lifecycle

import (
	"context"
	"errors"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
)

// Reuse the close writer's proof before preserving an observed close across a
// newer command fence. A timestamp alone never establishes writer absence.
func validateCommittedClose(ctx context.Context, current *runtimeTransaction) error {
	state, admitted := current.state, current.execution
	if state.CloseGeneration == 0 || state.CloseRequestedAt.IsZero() || state.ClosedAt.Before(state.CloseRequestedAt) || state.CloseEvidence == nil || state.CloseReviewReason != "" {
		return biz.ErrPersistence
	}
	dispatch, err := submission.ReadInTransaction(ctx, current.transaction, admitted.TenantID, admitted.ExecutionID)
	if state.CloseEvidence.NoDispatch {
		if !errors.Is(err, biz.ErrExecutionNotFound) || state.CloseReason == "NATURAL_TERMINAL" || state.Workspace != nil || state.Training != nil || state.TrainingHandle != nil || state.Observation != nil || state.Publication != nil || state.CloseAuthority != nil || state.StepExitCleanup != nil || state.CloseEvidence.RunID != "" || state.CloseEvidence.WorkflowUID != "" || state.CloseEvidence.ObservedAt.IsZero() || len(state.CloseEvidence.Resources) != 0 || len(state.CloseEvidence.SkippedTasks) != 0 || state.CloseEvidence.OwnerTermination != nil {
			return biz.ErrPersistence
		}
		return nil
	}
	if err != nil {
		return biz.ErrPersistence
	}
	authority, err := submission.ReadAuthorityInTransaction(ctx, current.transaction, admitted.TenantID, admitted.ExecutionID)
	candidate := authority.RunAuthorityCandidate
	if errors.Is(err, biz.ErrExecutionNotFound) && state.CloseAuthority != nil {
		candidate, err = *state.CloseAuthority, nil
	}
	if err != nil || !validExecutionCloseEvidence(admitted, candidate, state.CloseReason, *state.CloseEvidence) || !finetuningStartsTerminated(state, candidate, *state.CloseEvidence) || len(dispatch.ConfirmedRuns) > 1 || (len(dispatch.ConfirmedRuns) == 1 && dispatch.ConfirmedRuns[0].RunID != candidate.RunID) || (state.CloseAuthority != nil && len(dispatch.ConfirmedRuns) != 1) {
		return biz.ErrPersistence
	}
	if !validStepExitCleanup(current, candidate) {
		return biz.ErrPersistence
	}
	if state.Training != nil && state.TrainingRejection == nil {
		if state.TrainingHandle == nil || state.Observation == nil {
			return biz.ErrPersistence
		}
		observed, err := mergeObservation(state, *state.Observation)
		if err != nil || !observed.WritersAbsent {
			return biz.ErrPersistence
		}
	}
	if state.CloseReason == "NATURAL_TERMINAL" {
		if state.Training == nil || state.TrainingRejection != nil || state.Observation == nil || state.Observation.Outcome != "SUCCEEDED" || state.Publication == nil || validPublication(admitted, candidate, state, *state.Publication, false) != nil {
			return biz.ErrPersistence
		}
	}
	return nil
}

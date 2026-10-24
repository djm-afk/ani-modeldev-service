package lifecycle

import (
	"context"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func runtimeCreationSettled(state biz.ExecutionRuntime) bool {
	fact := state.RuntimeTemplate
	return fact == nil || fact.Binding != nil || len(fact.Attempts) > 0 && fact.Attempts[len(fact.Attempts)-1].NotSentAt != nil
}

func validRuntimeMaterialization(execution biz.Execution, state biz.ExecutionRuntime, now time.Time) bool {
	window, fact := state.TrainingWindow, state.RuntimeTemplate
	if window == nil {
		return fact == nil && state.Training == nil
	}
	deadline, err := execution.Snapshot.Timing.DeadlineFor(window.AdmittedAt)
	if err != nil || window.AdmittedAt.After(now) || !window.DeadlineAt.Equal(deadline) || !deadline.After(window.AdmittedAt) {
		return false
	}
	if fact == nil {
		return false
	}
	plan := execution.Snapshot.RuntimePlan
	if fact.Name != plan.Name || fact.NamespaceUID != execution.Snapshot.Environment.NamespaceUID || fact.ExpectedSpecSHA256 != plan.ExpectedSpecSHA256 || len(fact.Attempts) == 0 {
		return false
	}
	previous := window.AdmittedAt
	for i, attempt := range fact.Attempts {
		if attempt.DispatchedAt.Before(previous) || attempt.DispatchedAt.After(now) || !attempt.DispatchedAt.Before(window.DeadlineAt) {
			return false
		}
		if attempt.NotSentAt != nil {
			if attempt.NotSentAt.Before(attempt.DispatchedAt) || attempt.NotSentAt.After(now) || attempt.NotSentReason != "PRECONDITION_FAILED_BEFORE_CREATE" {
				return false
			}
			previous = *attempt.NotSentAt
		} else if i != len(fact.Attempts)-1 || attempt.NotSentReason != "" {
			return false
		}
	}
	if fact.Binding != nil {
		b := fact.Binding
		if b.NamespaceUID != fact.NamespaceUID || b.Name != fact.Name || !validOpaqueID(b.UID) || b.ObservedSpecSHA256 != fact.ExpectedSpecSHA256 || fact.ReviewReason != "" || fact.NextAction != "" {
			return false
		}
	}
	if state.Training != nil && (fact.Binding == nil || state.Training.RuntimeBinding != *fact.Binding || state.Training.Timing == nil || *state.Training.Timing != *window) {
		return false
	}
	return true
}

// ReserveRuntime uses the original execution lock, Stop fence and DB clock.
// Only a durably recorded pre-Create refusal can authorize another unsent
// attempt. A process crash after commit retains the original unknown attempt.
func (repository *Repository) ReserveRuntime(ctx context.Context, authority biz.RunAuthorityCandidate) (biz.TrainingReservation, error) {
	send := false
	state, _, err := repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		if current.state.Workspace == nil {
			return false, biz.ErrRuntimeNotReady
		}
		if fact := current.state.RuntimeTemplate; fact != nil {
			if fact.Binding != nil {
				return false, nil
			}
			if len(fact.Attempts) == 0 {
				return false, biz.ErrRuntimeConflict
			}
			if fact.Attempts[len(fact.Attempts)-1].NotSentAt == nil {
				return false, nil
			}
		}
		if err := requireOpen(current); err != nil {
			return false, err
		}
		if current.state.TrainingWindow == nil {
			if err := requireRoleWindow(current, "prepare"); err != nil {
				return false, err
			}
			deadline, err := current.execution.Snapshot.Timing.DeadlineFor(current.now)
			if err != nil || !current.now.Before(deadline) {
				return false, biz.ErrPipelineDispatchBlocked
			}
			current.state.TrainingWindow = &biz.TrainingBudgetTiming{AdmittedAt: current.now, DeadlineAt: deadline}
		}
		if !current.now.Before(current.state.TrainingWindow.DeadlineAt) {
			return false, biz.ErrPipelineDispatchBlocked
		}
		current.writeDeadline = current.state.TrainingWindow.DeadlineAt
		plan := current.execution.Snapshot.RuntimePlan
		if plan.Validate() != nil {
			return false, biz.ErrRuntimeConflict
		}
		if current.state.RuntimeTemplate == nil {
			current.state.RuntimeTemplate = &biz.RuntimeCreation{Name: plan.Name, NamespaceUID: current.execution.Snapshot.Environment.NamespaceUID, ExpectedSpecSHA256: plan.ExpectedSpecSHA256}
		}
		fact := current.state.RuntimeTemplate
		fact.Attempts = append(fact.Attempts, biz.RuntimeDispatchAttempt{DispatchedAt: current.now})
		fact.ReviewReason, fact.NextAction = "", ""
		send = true
		return true, nil
	})
	return biz.TrainingReservation{State: state, SendPermit: send}, err
}

func (repository *Repository) RecordRuntimeBinding(ctx context.Context, authority biz.RunAuthorityCandidate, binding biz.RuntimeTemplateBinding) (biz.ExecutionRuntime, error) {
	state, _, err := repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		fact := current.state.RuntimeTemplate
		if fact == nil || len(fact.Attempts) == 0 || binding.Name != fact.Name || binding.NamespaceUID != fact.NamespaceUID || !validOpaqueID(binding.UID) || binding.ObservedSpecSHA256 != fact.ExpectedSpecSHA256 {
			return false, biz.ErrRuntimeConflict
		}
		if fact.Binding != nil {
			if *fact.Binding != binding {
				return false, biz.ErrRuntimeConflict
			}
			return false, nil
		}
		// A late API receipt is retained after Stop for original-UID cleanup;
		// ReserveTraining independently rechecks the fence before any TrainJob.
		fact.Binding = &binding
		fact.ReviewReason, fact.NextAction = "", ""
		return true, nil
	})
	return state, err
}

func (repository *Repository) RecordRuntimeNotSent(ctx context.Context, authority biz.RunAuthorityCandidate, proof biz.RuntimeCreationNotSent) (biz.ExecutionRuntime, error) {
	state, _, err := repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		fact := current.state.RuntimeTemplate
		if fact == nil || fact.Binding != nil || len(fact.Attempts) == 0 || proof.NamespaceUID != fact.NamespaceUID || proof.Name != fact.Name || proof.ExpectedSpecSHA256 != fact.ExpectedSpecSHA256 || proof.Reason != "PRECONDITION_FAILED_BEFORE_CREATE" {
			return false, biz.ErrRuntimeConflict
		}
		attempt := &fact.Attempts[len(fact.Attempts)-1]
		if !proof.AttemptDispatchedAt.Equal(attempt.DispatchedAt) {
			return false, biz.ErrRuntimeConflict
		}
		if attempt.NotSentAt != nil {
			return false, nil
		}
		at := current.now
		attempt.NotSentAt, attempt.NotSentReason = &at, proof.Reason
		return true, nil
	})
	return state, err
}

func (repository *Repository) RecordRuntimeReview(ctx context.Context, authority biz.RunAuthorityCandidate) (biz.ExecutionRuntime, error) {
	state, _, err := repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		fact := current.state.RuntimeTemplate
		if fact == nil || fact.Binding != nil || len(fact.Attempts) == 0 || fact.Attempts[len(fact.Attempts)-1].NotSentAt != nil {
			return false, biz.ErrRuntimeConflict
		}
		if fact.ReviewReason != "" {
			return false, nil
		}
		fact.ReviewReason = "NEEDS_REVIEW: RUNTIME_DISPATCH_OUTCOME_UNKNOWN"
		fact.NextAction = "Verify original API audit/transport receipt for namespace/name; bind and clean only the original object UID if found. A 404 does not authorize resend or refund."
		return true, nil
	})
	return state, err
}

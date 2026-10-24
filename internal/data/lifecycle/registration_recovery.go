package lifecycle

import (
	"context"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func (r *Repository) BeginRegistrationRecovery(ctx context.Context, a biz.RunAuthorityCandidate) (biz.ExecutionRuntime, error) {
	state, _, err := r.mutate(ctx, a, func(current *runtimeTransaction) (bool, error) {
		if current.execution.Snapshot.Kind == "FINETUNING" && !current.execution.Snapshot.Finetuning.RequiresModelRegistration() {
			return false, biz.ErrModelRegistrationNotApplicable
		}
		registration := current.state.FinetuningRegistration
		if current.state.ClosedAt == nil || current.state.CloseEvidence == nil || registration == nil {
			return false, biz.ErrRuntimeNotReady
		}
		if registration.State == biz.RegistrationStateReady {
			return false, nil
		}
		if registration.Recovery != nil {
			// A repeat preserves the original independently admitted budget.
			if !current.now.Before(registration.Recovery.DeadlineAt) || registration.Recovery.CommandFence != current.commandFence {
				return false, biz.ErrPipelineDispatchBlocked
			}
			return false, nil
		}
		if registration.State != biz.RegistrationStatePending && registration.State != biz.RegistrationStateUnavailable {
			return false, biz.ErrRuntimeConflict
		}
		duration := time.Duration(current.execution.Snapshot.Timing.StepTimeoutSeconds) * time.Second
		if duration <= 0 || duration > 24*time.Hour {
			return false, biz.ErrRuntimeConflict
		}
		registration.Recovery = &biz.FinetuningRegistrationRecovery{AdmittedAt: current.now, DeadlineAt: current.now.Add(duration), CommandFence: current.commandFence}
		registration.State, registration.FailureCode = biz.RegistrationStatePending, ""
		return true, nil
	})
	return state, err
}

func requireRegistrationWindow(current *runtimeTransaction, recovery bool) error {
	if !recovery {
		if err := requireOpen(current); err != nil {
			return err
		}
		return requireRoleWindow(current, "register")
	}
	registration := current.state.FinetuningRegistration
	if current.state.ClosedAt == nil || registration == nil || registration.Recovery == nil || registration.Recovery.CommandFence != current.commandFence || !current.now.Before(registration.Recovery.DeadlineAt) {
		return biz.ErrPipelineDispatchBlocked
	}
	return nil
}

func registrationDispatchDeadline(registration *biz.FinetuningRegistration, dispatch biz.FinetuningRegistrationDispatch) time.Time {
	if recovery := registration.Recovery; recovery != nil && !dispatch.PermittedAt.Before(recovery.AdmittedAt) {
		return recovery.DeadlineAt
	}
	return registration.DeadlineAt
}

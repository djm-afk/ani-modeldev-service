package biz

import "context"

// ModelRegistrationRecovery restores only original durable registration facts.
// It cannot choose new targets, objects, keys or another compute attempt.
type ModelRegistrationRecovery interface {
	ResumeModelRegistration(context.Context, string, string) (Execution, error)
}

func (r *ManagedRuntime) ResumeModelRegistration(ctx context.Context, tenant, executionID string) (Execution, error) {
	if ctx == nil || !validAdmissionID(tenant) || !validAdmissionID(executionID) {
		return Execution{}, ErrInvalidAdmission
	}
	if r == nil || r.steps == nil {
		return Execution{}, ErrModelDependencyUnavailable
	}
	execution, err := r.steps.executions.Get(ctx, tenant, executionID)
	if err != nil {
		return Execution{}, err
	}
	if execution.Snapshot.Kind != "FINETUNING" {
		return Execution{}, ErrRuntimeConflict
	}
	if !execution.Snapshot.Finetuning.RequiresModelRegistration() {
		return Execution{}, ErrModelRegistrationNotApplicable
	}
	if r.registrations == nil {
		return Execution{}, ErrModelDependencyUnavailable
	}
	repository, ok := r.repository.(FinetuningRegistrationIntentRepository)
	if !ok {
		return Execution{}, ErrRuntimeNotReady
	}
	authority, err := r.steps.repository.GetRunAuthority(ctx, tenant, executionID)
	if err != nil {
		return Execution{}, err
	}
	state, err := repository.BeginRegistrationRecovery(ctx, authority.RunAuthorityCandidate)
	if err != nil {
		return Execution{}, err
	}
	if state.ClosedAt == nil {
		return Execution{}, ErrRuntimeConflict
	}
	if _, err = r.registrations.Ensure(ctx, authority.RunAuthorityCandidate, execution, state, true); err != nil {
		return Execution{}, err
	}
	return r.steps.executions.Get(ctx, tenant, executionID)
}

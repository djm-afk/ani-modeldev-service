package lifecycle

import (
	"context"
	"reflect"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

var _ biz.FinetuningRegistrationDispatchRepository = (*Repository)(nil)

// D09.3. A stored registration intent fixes the original target and content; it
// is not a permanent permit for the writes that follow. Each concrete external
// write takes its own permit under the same execution row lock that Stop, close
// and the deadline use. The RPC itself runs outside this transaction.

// RecordRegistrationIntent fixes the original delivery content before the first
// external write. It consumes the single registration budget, so a later permit
// never re-claims a fresh window.
func (repository *Repository) RecordRegistrationIntent(ctx context.Context, authority biz.RunAuthorityCandidate) (biz.ExecutionRuntime, bool, error) {
	return repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		content, err := registrationContent(current)
		if err != nil {
			return false, err
		}
		if existing := current.state.FinetuningRegistration; existing != nil {
			// A provider that became available later cannot replace the original
			// content, key or budget; the intent is only ever written once.
			if existing.State != biz.RegistrationStatePending {
				return false, biz.ErrRuntimeConflict
			}
			return false, nil
		}
		if err := requireOpen(current); err != nil {
			return false, err
		}
		content.RecordedAt = current.now
		content.DeadlineAt = registrationDeadline(current, current.now)
		content.State = biz.RegistrationStatePending
		current.state.FinetuningRegistration = &content
		return true, nil
	})
}

// PermitRegistrationDispatch records the permit for one concrete write and
// reports whether the caller may now perform it. A repeat call for an operation
// that already holds a permit never re-sends: it returns the stored in-flight
// fact so the caller can verify the original attempt instead.
func (repository *Repository) PermitRegistrationDispatch(ctx context.Context, authority biz.RunAuthorityCandidate, operation biz.FinetuningRegistrationOperation, requestID string) (biz.FinetuningRegistrationDispatchResult, error) {
	var result biz.FinetuningRegistrationDispatchResult
	send := false
	state, _, err := repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		registration := current.state.FinetuningRegistration
		if registration == nil || registration.State == biz.RegistrationStateUnavailable {
			return false, biz.ErrRuntimeNotReady
		}
		if !biz.ValidRegistrationOperation(operation) || !validTransitionRequestID(requestID) {
			return false, biz.ErrRuntimeConflict
		}
		if existing := biz.RegistrationDispatchFor(registration, operation); existing != nil {
			result.Dispatch = *existing
			return false, nil
		}
		if registration.State != biz.RegistrationStatePending {
			// A verified registration already holds its original result; it
			// cannot start a new external write.
			return false, biz.ErrRuntimeConflict
		}
		// The new branch must hold a verified container before a version write
		// can be permitted; a late container success never authorizes it.
		newBranch := current.execution.Snapshot.Finetuning.TargetModelID == nil
		if operation == biz.OpCreateModel && !newBranch {
			return false, biz.ErrRuntimeConflict
		}
		if operation == biz.OpCreateModelVersion && newBranch {
			container := biz.RegistrationDispatchFor(registration, biz.OpCreateModel)
			if container == nil || container.State != biz.DispatchConfirmed {
				return false, biz.ErrRuntimeNotReady
			}
		}
		if err := requireOpen(current); err != nil {
			return false, err
		}
		permit := biz.FinetuningRegistrationDispatch{Operation: operation, State: biz.DispatchInFlight,
			PermittedAt: current.now, DeadlineAt: registration.DeadlineAt, RequestID: requestID, ContentSHA256: registration.ContentSHA256}
		if permit.DeadlineAt.Before(permit.PermittedAt) {
			return false, biz.ErrPipelineDispatchBlocked
		}
		registration.Dispatches = append(registration.Dispatches, permit)
		result.Dispatch = permit
		send = true
		return true, nil
	})
	if err != nil {
		return biz.FinetuningRegistrationDispatchResult{}, err
	}
	result.State, result.SendPermit = state, send
	return result, nil
}

// RecordRegistrationOutcome stores the real observed outcome of a permitted
// call. It does not require an open execution: a response that arrives after a
// close fence is still a real fact and must be preserved, never upgraded to a
// fresh permit. CONFIRMED may only be reported after the caller has verified
// the original result against the original target, version and object.
func (repository *Repository) RecordRegistrationOutcome(ctx context.Context, authority biz.RunAuthorityCandidate, operation biz.FinetuningRegistrationOperation, outcome biz.FinetuningRegistrationDispatch) (biz.ExecutionRuntime, error) {
	state, _, err := repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		registration := current.state.FinetuningRegistration
		if registration == nil {
			return false, biz.ErrRuntimeNotReady
		}
		original := biz.RegistrationDispatchFor(registration, operation)
		// A confirmation may settle an unresolved permit; a confirmed result is
		// final and can never be rewritten by a later response.
		if original == nil || (original.State != biz.DispatchInFlight && original.State != biz.DispatchUnknown) {
			return false, biz.ErrRuntimeConflict
		}
		if outcome.Operation != operation || outcome.PermittedAt != original.PermittedAt || outcome.DeadlineAt != original.DeadlineAt ||
			outcome.RequestID != original.RequestID || outcome.ContentSHA256 != original.ContentSHA256 {
			return false, biz.ErrRuntimeConflict
		}
		if outcome.State != biz.DispatchConfirmed && outcome.State != biz.DispatchUnknown {
			return false, biz.ErrRuntimeConflict
		}
		if !biz.ValidRegistrationDispatch(outcome, registration.RecordedAt, current.now, registration.DeadlineAt) {
			return false, biz.ErrRuntimeConflict
		}
		if *original == outcome {
			// A repeated read-back yields the same stored fact; it is a replay,
			// not a new observation, and must not advance the revision.
			return false, nil
		}
		// A confirmed result is final. Only an unresolved permit may be settled
		// by a later verification.
		if original.State == biz.DispatchConfirmed {
			return false, biz.ErrRuntimeConflict
		}
		// A confirmed version write is the verified original result the whole
		// registration exists for. Only then does the projected state become READY.
		if operation == biz.OpCreateModelVersion && outcome.State == biz.DispatchConfirmed {
			registration.State = biz.RegistrationStateReady
		}
		*original = outcome
		return true, nil
	})
	return state, err
}

func validTransitionRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character < '!' || character > '~' {
			return false
		}
	}
	return true
}

// validRegistrationDispatches validates the per-operation in-flight facts
// against the original intent. Dispatches share the intent's single deadline and
// content binding, and their order is fixed: a version write cannot be in flight
// before the new-branch container is verified.
func validRegistrationDispatches(current *runtimeTransaction, stored biz.FinetuningRegistration) bool {
	if stored.State == biz.RegistrationStateUnavailable {
		return stored.FailureCode == biz.RegistrationUnavailableFailureCode && len(stored.Dispatches) == 0
	}
	if stored.State != biz.RegistrationStatePending && stored.State != biz.RegistrationStateReady {
		return false
	}
	if stored.State == biz.RegistrationStatePending && stored.FailureCode != "" {
		return false
	}
	seen := make(map[biz.FinetuningRegistrationOperation]bool, len(stored.Dispatches))
	for _, dispatch := range stored.Dispatches {
		if seen[dispatch.Operation] || dispatch.ContentSHA256 != stored.ContentSHA256 ||
			!biz.ValidRegistrationDispatch(dispatch, stored.RecordedAt, current.now, stored.DeadlineAt) {
			return false
		}
		seen[dispatch.Operation] = true
	}
	newBranch := current.execution.Snapshot.Finetuning.TargetModelID == nil
	container := biz.RegistrationDispatchFor(&stored, biz.OpCreateModel)
	if version := biz.RegistrationDispatchFor(&stored, biz.OpCreateModelVersion); version != nil && newBranch {
		if container == nil || container.State != biz.DispatchConfirmed {
			return false
		}
	}
	if stored.State != biz.RegistrationStateReady {
		return true
	}
	version := biz.RegistrationDispatchFor(&stored, biz.OpCreateModelVersion)
	if stored.FailureCode != "" || version == nil || version.State != biz.DispatchConfirmed {
		return false
	}
	return !newBranch || container.State == biz.DispatchConfirmed
}

// registrationDeadline fixes the single registration budget at the database
// time the intent was recorded. It is never re-claimed by a later dispatch.
func registrationDeadline(current *runtimeTransaction, recordedAt time.Time) time.Time {
	deadline := recordedAt.Add(time.Duration(current.execution.Snapshot.Finetuning.Budgets.RPCTimeoutSeconds) * time.Second)
	if current.execution.Snapshot.DeadlineAt.Before(deadline) {
		deadline = current.execution.Snapshot.DeadlineAt
	}
	return deadline
}

func registrationIntentUnchanged(expected, stored biz.FinetuningRegistration) bool {
	expected.RecordedAt, expected.DeadlineAt = stored.RecordedAt, stored.DeadlineAt
	expected.State, expected.FailureCode = stored.State, stored.FailureCode
	expected.Dispatches = stored.Dispatches
	return reflect.DeepEqual(expected, stored)
}

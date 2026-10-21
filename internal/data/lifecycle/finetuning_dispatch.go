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
			if !registrationIntentUnchanged(content, *existing) {
				return false, biz.ErrRuntimeConflict
			}
			if existing.State == biz.RegistrationStatePending || existing.State == biz.RegistrationStateReady {
				return false, nil
			}
			if existing.State != biz.RegistrationStateUnavailable || len(existing.Dispatches) != 0 {
				return false, biz.ErrRuntimeConflict
			}
			if err := requireRegistrationWindow(current, false); err != nil {
				return false, err
			}
			existing.State, existing.FailureCode = biz.RegistrationStatePending, ""
			return true, nil
		}
		if err := requireOpen(current); err != nil {
			return false, err
		}
		if err := requireRoleWindow(current, "register"); err != nil {
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
	return repository.permitRegistrationDispatch(ctx, authority, operation, requestID, false)
}

func (repository *Repository) PermitRegistrationRecoveryDispatch(ctx context.Context, authority biz.RunAuthorityCandidate, operation biz.FinetuningRegistrationOperation, requestID string) (biz.FinetuningRegistrationDispatchResult, error) {
	return repository.permitRegistrationDispatch(ctx, authority, operation, requestID, true)
}

func (repository *Repository) permitRegistrationDispatch(ctx context.Context, authority biz.RunAuthorityCandidate, operation biz.FinetuningRegistrationOperation, requestID string, recovery bool) (biz.FinetuningRegistrationDispatchResult, error) {
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
		if operation != biz.OpPrepareTrainedArtifact {
			prepared := biz.RegistrationDispatchFor(registration, biz.OpPrepareTrainedArtifact)
			if prepared == nil || prepared.State != biz.DispatchConfirmed || !biz.ValidTrainedArtifactSession(authority.TenantID, registration.Session) {
				return false, biz.ErrRuntimeNotReady
			}
		}
		if operation == biz.OpCompleteTrainedArtifact {
			copied := biz.RegistrationDispatchFor(registration, biz.OpControlledCopy)
			if copied == nil || copied.State != biz.DispatchConfirmed {
				return false, biz.ErrRuntimeNotReady
			}
		}
		if err := requireRegistrationWindow(current, recovery); err != nil {
			return false, err
		}
		deadline := registration.DeadlineAt
		if recovery {
			deadline = registration.Recovery.DeadlineAt
		}
		permit := biz.FinetuningRegistrationDispatch{Operation: operation, State: biz.DispatchInFlight,
			PermittedAt: current.now, DeadlineAt: deadline, RequestID: requestID, ContentSHA256: registration.ContentSHA256}
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
	// An identifier by itself cannot establish a provider association or READY.
	if outcome.State == biz.DispatchConfirmed {
		return biz.ExecutionRuntime{}, biz.ErrRuntimeConflict
	}
	return repository.recordRegistrationOutcome(ctx, authority, operation, outcome, nil)
}

func (repository *Repository) RecordRegistrationObservation(ctx context.Context, authority biz.RunAuthorityCandidate, operation biz.FinetuningRegistrationOperation, outcome biz.FinetuningRegistrationDispatch, observation biz.TrainedArtifactObservation) (biz.ExecutionRuntime, error) {
	return repository.recordRegistrationOutcome(ctx, authority, operation, outcome, &observation)
}

func (repository *Repository) recordRegistrationOutcome(ctx context.Context, authority biz.RunAuthorityCandidate, operation biz.FinetuningRegistrationOperation, outcome biz.FinetuningRegistrationDispatch, observation *biz.TrainedArtifactObservation) (biz.ExecutionRuntime, error) {
	state, _, err := repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		registration := current.state.FinetuningRegistration
		if registration == nil {
			return false, biz.ErrRuntimeNotReady
		}
		original := biz.RegistrationDispatchFor(registration, operation)
		// A confirmation may settle an unresolved permit; a confirmed result is
		// final and can never be rewritten by a later response.
		if original == nil {
			return false, biz.ErrRuntimeConflict
		}
		if outcome.Operation != operation || outcome.PermittedAt != original.PermittedAt || outcome.DeadlineAt != original.DeadlineAt ||
			outcome.RequestID != original.RequestID || outcome.ContentSHA256 != original.ContentSHA256 {
			return false, biz.ErrRuntimeConflict
		}
		if outcome.State != biz.DispatchConfirmed && outcome.State != biz.DispatchUnknown {
			return false, biz.ErrRuntimeConflict
		}
		if !biz.ValidRegistrationDispatch(outcome, registration.RecordedAt, current.now, registrationDispatchDeadline(registration, outcome)) {
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
		if outcome.State == biz.DispatchConfirmed {
			if observation == nil || !biz.ValidTrainedArtifactObservation(authority.TenantID, *registration, *observation) {
				return false, biz.ErrRuntimeConflict
			}
			if operation == biz.OpPrepareTrainedArtifact {
				if outcome.ReceiptID != observation.Session.RegistrationID {
					return false, biz.ErrRuntimeConflict
				}
				session := observation.Session
				registration.Session = &session
			} else if registration.Session == nil || *registration.Session != observation.Session || !observation.ObjectVerified {
				return false, biz.ErrRuntimeConflict
			}
			if operation == biz.OpControlledCopy && outcome.ReceiptID != registration.Session.RegistrationID {
				return false, biz.ErrRuntimeConflict
			}
			if operation == biz.OpCompleteTrainedArtifact {
				if observation.State != "ready" || !biz.ValidFinetuningModelReceipt(authority.TenantID, *registration, observation.Receipt) || outcome.ReceiptID != observation.Receipt.ModelVersionID {
					return false, biz.ErrRuntimeConflict
				}
				receipt := *observation.Receipt
				registration.Receipt = &receipt
				registration.State, registration.FailureCode = biz.RegistrationStateReady, ""
			}
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
		return stored.FailureCode == biz.RegistrationUnavailableFailureCode && len(stored.Dispatches) == 0 && stored.Session == nil && stored.Receipt == nil && stored.Recovery == nil
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
			!biz.ValidRegistrationDispatch(dispatch, stored.RecordedAt, current.now, registrationDispatchDeadline(&stored, dispatch)) {
			return false
		}
		seen[dispatch.Operation] = true
	}
	prepared := biz.RegistrationDispatchFor(&stored, biz.OpPrepareTrainedArtifact)
	copied := biz.RegistrationDispatchFor(&stored, biz.OpControlledCopy)
	completed := biz.RegistrationDispatchFor(&stored, biz.OpCompleteTrainedArtifact)
	if stored.Session != nil && (!biz.ValidTrainedArtifactSession(current.execution.TenantID, stored.Session) || prepared == nil || prepared.State != biz.DispatchConfirmed || prepared.ReceiptID != stored.Session.RegistrationID) {
		return false
	}
	if prepared != nil && prepared.State == biz.DispatchConfirmed && stored.Session == nil {
		return false
	}
	if copied != nil && (stored.Session == nil || prepared.State != biz.DispatchConfirmed || copied.State == biz.DispatchConfirmed && copied.ReceiptID != stored.Session.RegistrationID) {
		return false
	}
	if completed != nil && (copied == nil || copied.State != biz.DispatchConfirmed) {
		return false
	}
	if stored.State != biz.RegistrationStateReady {
		return stored.Receipt == nil && (completed == nil || completed.State != biz.DispatchConfirmed)
	}
	if stored.FailureCode != "" || completed == nil || completed.State != biz.DispatchConfirmed || !biz.ValidFinetuningModelReceipt(current.execution.TenantID, stored, stored.Receipt) || completed.ReceiptID != stored.Receipt.ModelVersionID {
		return false
	}
	return true
}

// registrationDeadline fixes the single registration budget at the database
// time the intent was recorded. It is never re-claimed by a later dispatch.
func registrationDeadline(current *runtimeTransaction, recordedAt time.Time) time.Time {
	timing := biz.TimingForManagedRole(current.state, "register")
	if timing == nil || recordedAt.Before(timing.AdmittedAt) || !recordedAt.Before(timing.DeadlineAt) {
		return time.Time{}
	}
	return timing.DeadlineAt
}

func registrationIntentUnchanged(expected, stored biz.FinetuningRegistration) bool {
	expected.RecordedAt, expected.DeadlineAt = stored.RecordedAt, stored.DeadlineAt
	expected.State, expected.FailureCode = stored.State, stored.FailureCode
	expected.Dispatches = stored.Dispatches
	expected.Session, expected.Receipt, expected.Recovery = stored.Session, stored.Receipt, stored.Recovery
	return reflect.DeepEqual(expected, stored)
}

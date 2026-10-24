package biz

import (
	"context"
	"github.com/google/uuid"
	"strings"
	"time"
)

const FinetuningModelVersionReady = "ready"
const FinetuningWriteUnresolved = "MODEL_WRITE_UNRESOLVED"

type FinetuningRegistrationOrchestrator struct {
	dispatches FinetuningRegistrationDispatchRepository
	registry   TrainedArtifactRegistry
	copier     TrainedArtifactCopier
}

func NewFinetuningRegistrationOrchestrator(dispatches FinetuningRegistrationDispatchRepository, registry TrainedArtifactRegistry, copier TrainedArtifactCopier) (*FinetuningRegistrationOrchestrator, error) {
	if dispatches == nil || registry == nil || copier == nil {
		return nil, ErrModelDependencyUnavailable
	}
	return &FinetuningRegistrationOrchestrator{dispatches: dispatches, registry: registry, copier: copier}, nil
}

// Ensure follows the provider's three concrete writes. A pending upload session
// never stands in for a model container. Every repeated/unknown dispatch only
// observes its original key; an absent object is never permission to resend.
func (o *FinetuningRegistrationOrchestrator) Ensure(ctx context.Context, authority RunAuthorityCandidate, execution Execution, state ExecutionRuntime, recovery bool) (ExecutionRuntime, error) {
	if o == nil || ctx == nil || execution.TenantID != authority.TenantID || execution.ExecutionID != authority.ExecutionID || execution.OperationID != authority.OperationID || execution.SpecHash != authority.SpecHash || state.FinetuningRegistration == nil {
		return state, ErrRuntimeConflict
	}
	if _, _, err := execution.CanonicalPayloads(); err != nil {
		return state, ErrRuntimeConflict
	}
	for _, operation := range []FinetuningRegistrationOperation{OpPrepareTrainedArtifact, OpControlledCopy, OpCompleteTrainedArtifact} {
		registration := state.FinetuningRegistration
		if registration.State == RegistrationStateReady {
			if !ValidFinetuningModelReceipt(execution.TenantID, *registration, registration.Receipt) {
				return state, ErrPersistence
			}
			return state, nil
		}
		if original := RegistrationDispatchFor(registration, operation); original != nil && original.State == DispatchConfirmed {
			continue
		}
		request := TrainedArtifactRequest{TenantID: execution.TenantID, ExecutionID: execution.ExecutionID, Registration: *registration}
		var grant TrainedArtifactUploadGrant
		if operation == OpControlledCopy && RegistrationDispatchFor(registration, operation) == nil {
			if !validTrainedArtifactSession(execution.TenantID, registration.Session) {
				return state, ErrRuntimeNotReady
			}
			// A ticket read does not consume a never-sent PUT. Stop is rechecked below.
			deadline := registration.DeadlineAt
			if recovery {
				if registration.Recovery == nil {
					return state, ErrRuntimeConflict
				}
				deadline = registration.Recovery.DeadlineAt
			}
			ticketRead, cancelTicketRead := context.WithDeadline(ctx, deadline)
			var err error
			grant, err = o.registry.ArtifactUploadGrant(ticketRead, request, *registration.Session)
			cancelTicketRead()
			if err != nil {
				return state, ErrModelDependencyUnavailable
			}
			if grant.Session != *registration.Session || grant.URL == "" || !grant.ExpiresAt.After(time.Now()) {
				return state, ErrRuntimeConflict
			}
		}
		var permitted FinetuningRegistrationDispatchResult
		var err error
		if recovery {
			permitted, err = o.dispatches.PermitRegistrationRecoveryDispatch(ctx, authority, operation, uuid.NewString())
		} else {
			permitted, err = o.dispatches.PermitRegistrationDispatch(ctx, authority, operation, uuid.NewString())
		}
		if err != nil {
			return state, err
		}
		state = permitted.State
		registration = state.FinetuningRegistration
		if registration == nil {
			return state, ErrPersistence
		}
		request.Registration = *registration
		deadline := permitted.Dispatch.DeadlineAt
		if recovery && !permitted.SendPermit && registration.Recovery != nil {
			deadline = registration.Recovery.DeadlineAt
		}
		business, cancel := context.WithDeadline(ctx, deadline)
		if permitted.SendPermit {
			switch operation {
			case OpPrepareTrainedArtifact:
				_, err = o.registry.PrepareArtifact(business, request)
			case OpControlledCopy:
				err = o.copier.CopyTrainedArtifact(business, execution, registration.Bundle, grant)
			case OpCompleteTrainedArtifact:
				if !validTrainedArtifactSession(execution.TenantID, registration.Session) {
					cancel()
					return state, ErrPersistence
				}
				_, err = o.registry.CompleteArtifact(business, request, *registration.Session)
			}
		}
		// A lost write response may still have succeeded. Only the exact original
		// key can settle it; an absent observation never grants another send.
		observation, readErr := o.registry.ReadArtifact(business, request)
		cancel()
		outcome := permitted.Dispatch
		outcome.State, outcome.ReceiptID, outcome.FailureCode = DispatchUnknown, "", FinetuningWriteUnresolved
		if readErr == nil && validTrainedArtifactObservation(execution.TenantID, *registration, observation) {
			confirmed := operation == OpPrepareTrainedArtifact || operation == OpControlledCopy && observation.ObjectVerified || operation == OpCompleteTrainedArtifact && observation.State == FinetuningModelVersionReady && observation.ObjectVerified && ValidFinetuningModelReceipt(execution.TenantID, *registration, observation.Receipt)
			if confirmed {
				outcome.State, outcome.ReceiptID, outcome.FailureCode = DispatchConfirmed, observation.Session.RegistrationID, ""
				if operation == OpCompleteTrainedArtifact {
					outcome.ReceiptID = observation.Receipt.ModelVersionID
				}
			}
		}
		// A bounded DB-only settlement preserves late facts; it grants no Model write.
		settle, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		state, err = o.dispatches.RecordRegistrationObservation(settle, authority, operation, outcome, observation)
		done()
		if err != nil {
			return state, err
		}
		if outcome.State != DispatchConfirmed {
			return state, ErrModelDependencyUnavailable
		}
	}
	if state.FinetuningRegistration == nil || state.FinetuningRegistration.State != RegistrationStateReady || !ValidFinetuningModelReceipt(execution.TenantID, *state.FinetuningRegistration, state.FinetuningRegistration.Receipt) {
		return state, ErrModelDependencyUnavailable
	}
	return state, nil
}

func ValidTrainedArtifactSession(tenant string, session *TrainedArtifactSession) bool {
	if session == nil || !validAdmissionID(session.RegistrationID) || !ValidStorageKey(session.StoragePath) || !strings.HasPrefix(session.StoragePath, tenant+"/") {
		return false
	}
	id, err := uuid.Parse(session.RegistrationID)
	return err == nil && id != uuid.Nil && id.String() == session.RegistrationID
}

func validTrainedArtifactSession(tenant string, session *TrainedArtifactSession) bool {
	return ValidTrainedArtifactSession(tenant, session)
}

func ValidTrainedArtifactObservation(tenant string, registration FinetuningRegistration, observation TrainedArtifactObservation) bool {
	if !validTrainedArtifactSession(tenant, &observation.Session) || registration.Session != nil && *registration.Session != observation.Session {
		return false
	}
	switch observation.State {
	case "pending", "uploaded", "ready":
	default:
		return false
	}
	return observation.State != "ready" || observation.ObjectVerified && ValidFinetuningModelReceipt(tenant, registration, observation.Receipt)
}
func validTrainedArtifactObservation(tenant string, registration FinetuningRegistration, observation TrainedArtifactObservation) bool {
	return ValidTrainedArtifactObservation(tenant, registration, observation)
}

func ValidFinetuningModelReceipt(tenant string, registration FinetuningRegistration, receipt *FinetuningModelReceipt) bool {
	if receipt == nil || !validTrainedArtifactSession(tenant, &TrainedArtifactSession{RegistrationID: receipt.RegistrationID, StoragePath: receipt.StoragePath}) || receipt.SizeBytes != registration.Bundle.SizeBytes || receipt.SHA256 != registration.Bundle.SHA256 || registration.TargetModelID != nil && receipt.ModelID != *registration.TargetModelID {
		return false
	}
	if registration.Session != nil && (receipt.RegistrationID != registration.Session.RegistrationID || receipt.StoragePath != registration.Session.StoragePath) {
		return false
	}
	for _, value := range []string{receipt.ModelID, receipt.ModelVersionID} {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil || id.String() != value {
			return false
		}
	}
	return true
}

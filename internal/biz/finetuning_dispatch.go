package biz

import (
	"context"
	"time"
)

// D09.3 dispatch arbitration. A stored registration intent fixes the original
// target and content; it is not a permanent permit for every side effect that
// follows. Each operation that can produce an external side effect must obtain
// its own permit under the same execution row lock that Stop, close and the
// deadline use, and the RPC itself must run outside that transaction.
//
// These types live beside the registration record rather than in a new
// platform: the record already exists, and the spec forbids introducing a
// general authorization service or permit platform for this.

// FinetuningRegistrationOperation names one concrete side-effect dispatch.
// New-branch container creation and version registration are arbitrated
// separately; a controlled copy into the Model scope is its own operation.
type FinetuningRegistrationOperation string

const (
	// OpCreateModel creates the version container used by the new-model branch.
	OpCreateModel FinetuningRegistrationOperation = "CREATE_MODEL"
	// OpCreateModelVersion registers the output version under a resolved target.
	OpCreateModelVersion FinetuningRegistrationOperation = "CREATE_MODEL_VERSION"
	// OpControlledCopy writes the published object into the Model-approved scope.
	OpControlledCopy FinetuningRegistrationOperation = "CONTROLLED_COPY"
)

func ValidRegistrationOperation(operation FinetuningRegistrationOperation) bool {
	switch operation {
	case OpCreateModel, OpCreateModelVersion, OpControlledCopy:
		return true
	default:
		return false
	}
}

// FinetuningDispatchState is the durable outcome of one operation's dispatch.
// UNKNOWN is not a synonym for failure: a permitted request whose response was
// lost may still succeed remotely, and only original-key verification settles
// it. A NotFound never proves that no write happened.
type FinetuningDispatchState string

const (
	// DispatchInFlight means a permit was granted and the request may be live.
	DispatchInFlight FinetuningDispatchState = "IN_FLIGHT"
	// DispatchConfirmed means the original operation is verified complete.
	DispatchConfirmed FinetuningDispatchState = "CONFIRMED"
	// DispatchUnknown means a permit was granted but the result is unresolved.
	DispatchUnknown FinetuningDispatchState = "UNKNOWN"
)

// FinetuningRegistrationDispatch is the per-operation in-flight fact.
// ContentSHA256 binds the permit to the frozen content, so a later replay under
// the same key cannot silently change what is being written.
type FinetuningRegistrationDispatch struct {
	Operation     FinetuningRegistrationOperation `json:"operation"`
	State         FinetuningDispatchState         `json:"state"`
	PermittedAt   time.Time                       `json:"permitted_at"`
	DeadlineAt    time.Time                       `json:"deadline_at"`
	RequestID     string                          `json:"request_id"`
	ContentSHA256 string                          `json:"content_sha256"`
	ReceiptID     string                          `json:"receipt_id,omitempty"`
	FailureCode   string                          `json:"failure_code,omitempty"`
}

// FinetuningRegistrationDispatchRepository arbitrates one concrete external
// write against Stop, close and the deadline. Implementations must take the
// same execution row lock as Stop, and must not perform network or object I/O
// inside the transaction.
type FinetuningRegistrationDispatchRepository interface {
	// PermitRegistrationDispatch records the permit for one operation and
	// reports whether the caller may now perform the external write. A false
	// permit with a nil error means this operation is already permitted or has
	// an unresolved outcome; the caller must verify the original attempt rather
	// than re-send it.
	PermitRegistrationDispatch(context.Context, RunAuthorityCandidate, FinetuningRegistrationOperation, string) (FinetuningRegistrationDispatchResult, error)
	// RecordRegistrationOutcome stores the real observed outcome of a permitted call.
	RecordRegistrationOutcome(context.Context, RunAuthorityCandidate, FinetuningRegistrationOperation, FinetuningRegistrationDispatch) (ExecutionRuntime, error)
}

type FinetuningRegistrationDispatchResult struct {
	State ExecutionRuntime
	// Dispatch is the persisted in-flight fact for this operation.
	Dispatch FinetuningRegistrationDispatch
	// SendPermit is true only for the first permit of this operation. A repeat
	// call never re-sends: it observes and verifies the original attempt.
	SendPermit bool
}

// ValidRegistrationDispatch checks a persisted dispatch record against the
// original registration intent. Every dispatch shares the single registration
// budget: a new permit never re-claims a fresh window, and the permit can never
// outlive the execution's own deadline (already folded into the intent).
func ValidRegistrationDispatch(dispatch FinetuningRegistrationDispatch, intentRecordedAt, observedAt, intentDeadline time.Time) bool {
	if !ValidRegistrationOperation(dispatch.Operation) || dispatch.PermittedAt.IsZero() ||
		dispatch.PermittedAt.Before(intentRecordedAt) || dispatch.PermittedAt.After(observedAt) ||
		!validDispatchRequestID(dispatch.RequestID) || len(dispatch.ContentSHA256) != 64 ||
		!dispatch.DeadlineAt.Equal(intentDeadline) {
		return false
	}
	switch dispatch.State {
	case DispatchInFlight:
		return dispatch.ReceiptID == "" && dispatch.FailureCode == ""
	case DispatchUnknown:
		return dispatch.ReceiptID == ""
	case DispatchConfirmed:
		return dispatch.ReceiptID != "" && dispatch.FailureCode == ""
	default:
		return false
	}
}

// The permit's trace id is echoed to Model as the request id; it cannot be
// replayed as a fresh permit under a different value.
func validDispatchRequestID(value string) bool {
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

// RegistrationDispatchFor returns the stored dispatch for one operation.
func RegistrationDispatchFor(registration *FinetuningRegistration, operation FinetuningRegistrationOperation) *FinetuningRegistrationDispatch {
	if registration == nil {
		return nil
	}
	for index := range registration.Dispatches {
		if registration.Dispatches[index].Operation == operation {
			return &registration.Dispatches[index]
		}
	}
	return nil
}

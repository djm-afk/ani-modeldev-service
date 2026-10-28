package biz

import "errors"

var ErrInvalidExecutionStateFacts = errors.New("INVALID_EXECUTION_STATE_FACTS")

type ComputeState string

const (
	ComputeStateAccepted            ComputeState = "ACCEPTED"
	ComputeStateSubmitting          ComputeState = "SUBMITTING"
	ComputeStateSubmissionNotSent   ComputeState = "SUBMISSION_NOT_SENT"
	ComputeStateSubmissionUncertain ComputeState = "SUBMISSION_UNCERTAIN"
	ComputeStateSubmissionConfirmed ComputeState = "SUBMISSION_CONFIRMED"
	ComputeStatePreparing           ComputeState = "PREPARING"
	ComputeStateTraining            ComputeState = "TRAINING"
	ComputeStateSucceeded           ComputeState = "SUCCEEDED"
	ComputeStateFailed              ComputeState = "FAILED"
)

type DeliveryState string

const (
	DeliveryStatePending   DeliveryState = "PENDING"
	DeliveryStatePublished DeliveryState = "PUBLISHED"
)

const CloseStateClosed CloseState = "CLOSED"
const CloseStateNeedsReview CloseState = "NEEDS_REVIEW"

type ResourceState string

const ResourceStateNotApplicable ResourceState = "NOT_APPLICABLE"

// ExecutionStates reports independent axes of observed execution facts. It
// grants neither a creation permit nor authority to an observed Pipeline Run.
type ExecutionStates struct {
	Compute  ComputeState
	Delivery DeliveryState
	Resource ResourceState
	Close    CloseState
}

// ExecutionStateFacts contains validated facts read with an existing Admission
// in one transaction. Nil means no persisted dispatch or close respectively;
// a present value must be a supported persisted state, never a default value.
type ExecutionStateFacts struct {
	Submission *PipelineDispatchState
	Close      *CloseState
}

// Project describes the CPU admission/submission facts. ProjectRuntimeStates
// adds the independently persisted lifecycle facts from the same transaction.
// A close affects only its own axis; submission observations establish neither
// queued computation nor a training outcome.
func (facts ExecutionStateFacts) Project() (ExecutionStates, error) {
	states := ExecutionStates{
		Compute:  ComputeStateAccepted,
		Delivery: DeliveryStatePending,
		Resource: ResourceStateNotApplicable,
		Close:    CloseStateOpen,
	}
	if facts.Submission != nil {
		switch *facts.Submission {
		case PipelineDispatchSubmitting:
			states.Compute = ComputeStateSubmitting
		case PipelineDispatchNotSent:
			states.Compute = ComputeStateSubmissionNotSent
		case PipelineDispatchUncertain:
			states.Compute = ComputeStateSubmissionUncertain
		case PipelineDispatchConfirmed:
			states.Compute = ComputeStateSubmissionConfirmed
		default:
			return ExecutionStates{}, ErrInvalidExecutionStateFacts
		}
	}
	if facts.Close != nil {
		if *facts.Close != CloseStateClosing {
			return ExecutionStates{}, ErrInvalidExecutionStateFacts
		}
		states.Close = CloseStateClosing
	}
	return states, nil
}

// ProjectRuntimeStates overlays committed runtime facts on admission/submission
// axes. It does not schedule a step, grant creation permission, or infer a
// compute outcome from a close intent.
func ProjectRuntimeStates(base ExecutionStates, runtime ExecutionRuntime) (ExecutionStates, error) {
	states := base
	if runtime.Workspace != nil {
		states.Compute = ComputeStatePreparing
	}
	if runtime.Training != nil {
		if runtime.Workspace == nil {
			return ExecutionStates{}, ErrInvalidExecutionStateFacts
		}
		states.Compute = ComputeStateTraining
	}
	if runtime.TrainingRejection != nil {
		rejected := runtime.TrainingRejection
		if runtime.Training == nil || runtime.TrainingHandle != nil || runtime.Observation != nil || runtime.Publication != nil ||
			rejected.RequestSHA256 != runtime.Training.RequestSHA256 || rejected.NamespaceUID != runtime.Training.Workspace.NamespaceUID ||
			(rejected.StatusCode != 400 && rejected.StatusCode != 403 && rejected.StatusCode != 422) || rejected.ObservedAt.IsZero() {
			return ExecutionStates{}, ErrInvalidExecutionStateFacts
		}
		states.Compute = ComputeStateFailed
	}
	if runtime.TrainingHandle != nil && runtime.Training == nil {
		return ExecutionStates{}, ErrInvalidExecutionStateFacts
	}
	if runtime.Observation != nil {
		if runtime.TrainingHandle == nil || runtime.Observation.Handle != *runtime.TrainingHandle {
			return ExecutionStates{}, ErrInvalidExecutionStateFacts
		}
		switch runtime.Observation.Outcome {
		case "RUNNING", "UNKNOWN":
			states.Compute = ComputeStateTraining
		case "SUCCEEDED":
			if !runtime.Observation.WritersAbsent {
				return ExecutionStates{}, ErrInvalidExecutionStateFacts
			}
			states.Compute = ComputeStateSucceeded
		case "FAILED":
			states.Compute = ComputeStateFailed
		default:
			return ExecutionStates{}, ErrInvalidExecutionStateFacts
		}
	}
	if runtime.Publication != nil {
		if states.Compute != ComputeStateSucceeded || runtime.Observation == nil || !runtime.Observation.WritersAbsent {
			return ExecutionStates{}, ErrInvalidExecutionStateFacts
		}
		states.Delivery = DeliveryStatePublished
	}
	if runtime.CloseGeneration > 0 {
		if runtime.CloseRequestedAt.IsZero() {
			return ExecutionStates{}, ErrInvalidExecutionStateFacts
		}
		states.Close = CloseStateClosing
		if runtime.CloseReviewReason != "" {
			states.Close = CloseStateNeedsReview
		}
	}
	if runtime.ClosedAt != nil {
		if runtime.CloseGeneration == 0 || runtime.CloseEvidence == nil {
			return ExecutionStates{}, ErrInvalidExecutionStateFacts
		}
		states.Close = CloseStateClosed
		// Runtime readers validate committed owner termination independently
		// of the close intent. It covers failures before a training observation
		// exists, but a later pipeline failure cannot erase successful training.
		owner := runtime.CloseEvidence.OwnerTermination
		if owner != nil && owner.RunState == "FAILED" && (owner.WorkflowPhase == "Failed" || owner.WorkflowPhase == "Error") && states.Compute != ComputeStateSucceeded {
			states.Compute = ComputeStateFailed
		}
	}
	return states, nil
}

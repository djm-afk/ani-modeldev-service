package biz

import (
	"context"
	"errors"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// This is an original delivery intent and dependency observation, not a Model
// receipt, write permit or second workflow. A missing provider cannot be READY.
type FinetuningRegistration struct {
	PublicationID                        string
	ContainerKey, VersionKey, VersionTag string
	TargetModelID, NewModelName          *string
	BaseModelVersionID                   string
	Bundle                               cpup01.FixedObjectRef
	VerifyManifestSHA256                 string
	ContentSHA256                        string
	State, FailureCode                   string
	RecordedAt, DeadlineAt               time.Time
	// Dispatches holds one in-flight fact per concrete external write. The
	// intent above is not itself a permit for any of them.
	Dispatches []FinetuningRegistrationDispatch `json:",omitempty"`
	// Receipt is the provider-verified original Complete result. It does not
	// participate in the content hash fixed before any external dispatch.
	Receipt  *FinetuningModelReceipt         `json:",omitempty"`
	Session  *TrainedArtifactSession         `json:",omitempty"`
	Recovery *FinetuningRegistrationRecovery `json:",omitempty"`
}

// An explicit user-authorized recovery is admitted once after durable close.
// It preserves the original compute/REGISTER clocks and the closed Run history.
// A later Stop advances the existing command fence and blocks its next write.
type FinetuningRegistrationRecovery struct {
	AdmittedAt, DeadlineAt time.Time
	CommandFence           uint64
}

type FinetuningModelReceipt struct {
	RegistrationID string
	ModelID        string
	ModelVersionID string
	StoragePath    string
	SizeBytes      int64
	SHA256         string
}

// Registration states. PENDING carries a persisted original intent whose
// concrete dispatches are still being arbitrated; READY means the target
// version write is a verified original result. UNKNOWN is never a synonym for
// success and never proves that no write happened.
const (
	RegistrationStatePending     = "PENDING"
	RegistrationStateReady       = "READY"
	RegistrationStateUnavailable = "DEPENDENCY_UNAVAILABLE"
	// RegistrationUnavailableFailureCode is the only failure code that a
	// whole-intent unavailability record may carry.
	RegistrationUnavailableFailureCode = "MODEL_DEPENDENCY_UNAVAILABLE"
)

type FinetuningRegistrationRepository interface {
	RecordRegistrationUnavailable(context.Context, RunAuthorityCandidate) (ExecutionRuntime, bool, error)
}

var ErrModelRegistrationNotApplicable = errors.New("MODEL_REGISTRATION_NOT_APPLICABLE")

// FinetuningArtifactsRepository validates the original publication and frozen
// register window without recording or dispatching a Model write intent.
type FinetuningArtifactsRepository interface {
	ConfirmArtifactsOnly(context.Context, RunAuthorityCandidate) (ExecutionRuntime, error)
}

// RegisterModel persists the original published content before the provider
// writes. An absent provider records an explicit dependency failure; recovery
// keeps the same training, target, fixed object and idempotency key.
func (r *ManagedRuntime) RegisterModel(ctx context.Context, token string, claim BeginManagedExecutionRequest) (ManagedRuntimeResult, error) {
	result, err := r.Configuration(ctx, token, claim, "register")
	if err != nil {
		return result, err
	}
	if result.Execution.Snapshot.Kind != "FINETUNING" || result.Runtime.Publication == nil || result.Runtime.Publication.Bundle == nil {
		return ManagedRuntimeResult{}, ErrRuntimeNotReady
	}
	if !result.Execution.Snapshot.Finetuning.RequiresModelRegistration() {
		repository, ok := r.repository.(FinetuningArtifactsRepository)
		if !ok {
			return ManagedRuntimeResult{}, ErrRuntimeNotReady
		}
		state, err := repository.ConfirmArtifactsOnly(ctx, result.Authority.RunAuthorityCandidate)
		if err != nil {
			return ManagedRuntimeResult{}, err
		}
		return runtimeResult(result, state, true)
	}
	repo, ok := r.repository.(FinetuningRegistrationRepository)
	if !ok {
		return ManagedRuntimeResult{}, ErrRuntimeNotReady
	}
	if r.registrations != nil {
		intentRepository, ok := r.repository.(FinetuningRegistrationIntentRepository)
		if !ok {
			return ManagedRuntimeResult{}, ErrRuntimeNotReady
		}
		state, replayed, err := intentRepository.RecordRegistrationIntent(ctx, result.Authority.RunAuthorityCandidate)
		if err != nil {
			return ManagedRuntimeResult{}, err
		}
		state, writeErr := r.registrations.Ensure(ctx, result.Authority.RunAuthorityCandidate, result.Execution, state, false)
		result, err = runtimeResult(result, state, replayed)
		if err != nil {
			return result, err
		}
		return result, writeErr
	}
	state, replayed, err := repo.RecordRegistrationUnavailable(ctx, result.Authority.RunAuthorityCandidate)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	result, err = runtimeResult(result, state, replayed)
	if err != nil {
		return result, err
	}
	return result, ErrModelDependencyUnavailable
}

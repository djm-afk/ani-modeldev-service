package biz

import (
	"context"
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

// Model registration remains explicitly unavailable in the current composition.
// First persist the original fixed delivery content after publication; this can
// later be recovered without any new training or a changed target/object/key.
func (r *ManagedRuntime) RegisterModel(ctx context.Context, token string, claim BeginManagedExecutionRequest) (ManagedRuntimeResult, error) {
	result, err := r.Configuration(ctx, token, claim, "register")
	if err != nil {
		return result, err
	}
	if result.Execution.Snapshot.Kind != "FINETUNING" || result.Runtime.Publication == nil || result.Runtime.Publication.Bundle == nil {
		return ManagedRuntimeResult{}, ErrRuntimeNotReady
	}
	repo, ok := r.repository.(FinetuningRegistrationRepository)
	if !ok {
		return ManagedRuntimeResult{}, ErrRuntimeNotReady
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

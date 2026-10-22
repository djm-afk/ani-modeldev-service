package biz

import (
	"context"
	"strings"
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

// FinetuningRegistrationIntentRepository fixes the original delivery content
// before the first external write. It is separate from the dispatch
// arbitration port because the intent is written once, ahead of any permit.
type FinetuningRegistrationIntentRepository interface {
	RecordRegistrationIntent(context.Context, RunAuthorityCandidate) (ExecutionRuntime, bool, error)
}

// FinetuningArtifactPlan carries the provider-decided values for one
// registration. The container metadata and the output format mapping are
// Model's decisions, so they are pinned by deployment rather than inferred from
// the training output here. The frozen intent still supplies the identity and
// the exact object; nothing in this plan can change those.
type FinetuningArtifactPlan struct {
	FileName     string
	Format       string
	DisplayName  string
	Description  string
	Capabilities []string
}

// validArtifactPlan rejects a plan that cannot describe one provider artifact.
// It does not invent the provider's format vocabulary: an unknown format is the
// provider's call to reject, not this layer's.
func validArtifactPlan(plan FinetuningArtifactPlan) error {
	if !boundedArtifactText(plan.FileName, 255) || !boundedArtifactText(plan.Format, 64) ||
		len(plan.DisplayName) > 128 || len(plan.Description) > 512 || len(plan.Capabilities) > 16 {
		return ErrRuntimeConflict
	}
	for _, capability := range plan.Capabilities {
		if !boundedArtifactText(capability, 64) {
			return ErrRuntimeConflict
		}
	}
	return nil
}

func boundedArtifactText(value string, limit int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= limit &&
		!strings.ContainsAny(value, " \t\r\n\x00/\\")
}

// WithFinetuningRegistration enables the trained-artifact registration session
// for FINETUNING executions. Composition supplies the provider port, the
// controlled-copy transport and the provider-decided artifact plan. While it is
// absent, RegisterModel keeps its original boundary: it records the frozen
// intent as unavailable instead of inventing a provider result.
func (runtime *ManagedRuntime) WithFinetuningRegistration(orchestrator *TrainedArtifactRegistrationOrchestrator, plan FinetuningArtifactPlan) (*ManagedRuntime, error) {
	if runtime == nil || orchestrator == nil {
		return nil, ErrRuntimeNotReady
	}
	if err := validArtifactPlan(plan); err != nil {
		return nil, err
	}
	runtime.registration = orchestrator
	runtime.artifactPlan = plan
	return runtime, nil
}

// RegisterModel persists the original intent and then drives the trained
// artifact registration session to a settled outcome. The frozen target, key,
// version and exact object come from the persisted intent, so a caller can
// neither re-point the registration nor register different bytes.
//
// Unwired composition keeps the original behaviour: the intent is recorded as
// unavailable and no provider call is attempted.
func (runtime *ManagedRuntime) RegisterModel(ctx context.Context, token string, claim BeginManagedExecutionRequest) (ManagedRuntimeResult, error) {
	result, err := runtime.Configuration(ctx, token, claim, "register")
	if err != nil {
		return result, err
	}
	if result.Execution.Snapshot.Kind != "FINETUNING" || result.Runtime.Publication == nil || result.Runtime.Publication.Bundle == nil {
		return ManagedRuntimeResult{}, ErrRuntimeNotReady
	}
	if runtime.registration == nil {
		return runtime.recordUnavailableRegistration(ctx, result)
	}
	intents, ok := runtime.repository.(FinetuningRegistrationIntentRepository)
	if !ok || runtime.artifactPlan.FileName == "" {
		return ManagedRuntimeResult{}, ErrRuntimeNotReady
	}
	state, replayed, err := intents.RecordRegistrationIntent(ctx, result.Authority.RunAuthorityCandidate)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	result, err = runtimeResult(result, state, replayed)
	if err != nil {
		return result, err
	}
	if state.FinetuningRegistration == nil {
		return ManagedRuntimeResult{}, ErrRuntimeNotReady
	}
	registration := *state.FinetuningRegistration
	if registration.State == RegistrationStateReady {
		// The session already holds a verified original result.
		return result, nil
	}
	authority := result.Authority.RunAuthorityCandidate
	dispatch, err := runtime.registration.RegisterTrainedArtifact(ctx, authority, registration, result.Execution.Snapshot.PublicationScope, artifactPrepare(registration, runtime.artifactPlan))
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	// Re-read the committed state so the projection reflects the durable outcome
	// rather than an in-memory assumption.
	state, err = runtime.repository.GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	result, err = runtimeResult(result, state, false)
	if err != nil {
		return result, err
	}
	if dispatch.State != DispatchConfirmed || state.FinetuningRegistration == nil || state.FinetuningRegistration.State != RegistrationStateReady {
		// An unresolved attempt is not a failure fact: the session may still be
		// completed under its original id on the next call.
		return result, ErrModelWriteUnresolved
	}
	return result, nil
}

// recordUnavailableRegistration preserves the original dependency boundary: the
// fixed delivery content is persisted first so it can be recovered later
// without new training or a changed target/object/key.
func (runtime *ManagedRuntime) recordUnavailableRegistration(ctx context.Context, result ManagedRuntimeResult) (ManagedRuntimeResult, error) {
	repo, ok := runtime.repository.(FinetuningRegistrationRepository)
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

// artifactPrepare binds the provider request to the frozen intent. Every
// identity value comes from the persisted record; only the provider-decided
// metadata comes from the pinned plan.
func artifactPrepare(registration FinetuningRegistration, plan FinetuningArtifactPlan) TrainedArtifactPrepare {
	prepare := TrainedArtifactPrepare{
		IdempotencyKey:       registration.VersionKey,
		SourceModelVersionID: registration.BaseModelVersionID,
		Version:              registration.VersionTag,
		FileName:             plan.FileName,
		Format:               plan.Format,
		DisplayName:          plan.DisplayName,
		Description:          plan.Description,
		Capabilities:         plan.Capabilities,
		SizeBytes:            registration.Bundle.SizeBytes,
		ChecksumSHA256:       registration.Bundle.SHA256,
	}
	if registration.TargetModelID != nil {
		prepare.TargetModelID = *registration.TargetModelID
	}
	if registration.NewModelName != nil {
		prepare.ModelName = *registration.NewModelName
	}
	return prepare
}

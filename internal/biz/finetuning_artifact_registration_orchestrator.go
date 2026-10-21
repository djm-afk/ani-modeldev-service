package biz

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// TrainedArtifactRegistrationOrchestrator drives one registration session to a
// settled outcome. It never writes outside the durable permit: the whole
// session takes one permit under the shared execution row lock, and the provider
// calls run outside that transaction.
//
// The session is one idempotent unit. A permitted attempt whose result is
// unresolved is never re-sent under a fresh permit: it is re-completed under the
// original session id, which the provider treats as a replay.
type TrainedArtifactRegistrationOrchestrator struct {
	dispatches FinetuningRegistrationDispatchRepository
	provider   TrainedArtifactRegistration
	copy       ArtifactCopyUploader
}

func NewTrainedArtifactRegistrationOrchestrator(dispatches FinetuningRegistrationDispatchRepository, provider TrainedArtifactRegistration, copy ArtifactCopyUploader) (*TrainedArtifactRegistrationOrchestrator, error) {
	if dispatches == nil || provider == nil || copy == nil {
		return nil, ErrFinetuningNotEnabled
	}
	return &TrainedArtifactRegistrationOrchestrator{dispatches: dispatches, provider: provider, copy: copy}, nil
}

// RegisterTrainedArtifact prepares, copies and completes one session, then
// verifies the original result before reporting CONFIRMED. The frozen intent
// supplied by the caller fixes the target, version, format and object; nothing
// here can change them.
func (orchestrator *TrainedArtifactRegistrationOrchestrator) RegisterTrainedArtifact(ctx context.Context, authority RunAuthorityCandidate, registration FinetuningRegistration, scope cpup01.StorageScope, prepare TrainedArtifactPrepare) (FinetuningRegistrationDispatch, error) {
	if err := validArtifactPrepare(registration, prepare); err != nil {
		return FinetuningRegistrationDispatch{}, err
	}
	permit, err := orchestrator.dispatches.PermitRegistrationDispatch(ctx, authority, OpRegisterTrainedArtifact, uuid.NewString())
	if err != nil {
		return FinetuningRegistrationDispatch{}, err
	}
	if permit.Dispatch.State == DispatchConfirmed {
		// The session already holds a verified original result.
		return permit.Dispatch, nil
	}
	ticket, err := orchestrator.provider.PrepareTrainedArtifact(ctx, authority.TenantID, prepare)
	if err != nil {
		return orchestrator.settle(ctx, authority, permit.Dispatch, "", FinetuningWriteUnresolved)
	}
	if ticket.RegistrationID == "" {
		return orchestrator.settle(ctx, authority, permit.Dispatch, "", FinetuningWriteUnresolved)
	}
	// The permit fixes the deadline. A provider ticket that outlives it cannot
	// extend this execution's budget.
	if !ticketUsable(ticket.Upload, time.Now()) || (!permit.Dispatch.DeadlineAt.IsZero() && ticket.Upload.ExpiresAt.After(permit.Dispatch.DeadlineAt)) {
		return orchestrator.settle(ctx, authority, permit.Dispatch, "", FinetuningWriteUnresolved)
	}
	// The ticket must describe the same frozen object the registration holds.
	if ticket.Upload.SizeBytes != registration.Bundle.SizeBytes || ticket.Upload.SHA256 != registration.Bundle.SHA256 {
		return orchestrator.settle(ctx, authority, permit.Dispatch, "", FinetuningWriteUnresolved)
	}
	if _, err := orchestrator.copy.Upload(ctx, scope, registration.Bundle, ticket.Upload); err != nil {
		// The object may or may not have landed. Terminate the session so it
		// cannot be completed later by accident; the failure stays unresolved
		// because only the provider can settle what it observed.
		_ = orchestrator.provider.AbortTrainedArtifact(ctx, authority.TenantID, ticket.RegistrationID, "artifact copy failed")
		return orchestrator.settle(ctx, authority, permit.Dispatch, "", FinetuningWriteUnresolved)
	}
	receipt, err := orchestrator.provider.CompleteTrainedArtifact(ctx, authority.TenantID, ticket.RegistrationID, prepare.IdempotencyKey)
	if err != nil {
		return orchestrator.settle(ctx, authority, permit.Dispatch, "", FinetuningWriteUnresolved)
	}
	if !matchesTrainedArtifact(registration, prepare, receipt) {
		return orchestrator.settle(ctx, authority, permit.Dispatch, "", FinetuningWriteUnresolved)
	}
	return orchestrator.settle(ctx, authority, permit.Dispatch, receipt.ModelVersionID, "")
}

// settle records the real observed outcome. An unresolved result is UNKNOWN,
// never a failure fact: the session may still be completable under its original
// id, and only the provider can settle that. CONFIRMED requires a real receipt.
func (orchestrator *TrainedArtifactRegistrationOrchestrator) settle(ctx context.Context, authority RunAuthorityCandidate, permitted FinetuningRegistrationDispatch, receipt, failure string) (FinetuningRegistrationDispatch, error) {
	outcome := permitted
	if receipt == "" {
		outcome.State, outcome.ReceiptID, outcome.FailureCode = DispatchUnknown, "", failure
	} else {
		outcome.State, outcome.ReceiptID, outcome.FailureCode = DispatchConfirmed, receipt, ""
	}
	state, err := orchestrator.dispatches.RecordRegistrationOutcome(ctx, authority, OpRegisterTrainedArtifact, outcome)
	if err != nil {
		return FinetuningRegistrationDispatch{}, err
	}
	if stored := RegistrationDispatchFor(state.FinetuningRegistration, OpRegisterTrainedArtifact); stored != nil {
		return *stored, nil
	}
	return outcome, nil
}

// validArtifactPrepare binds the provider request to the frozen intent. The
// session key is derived from the execution so a replay cannot open a second
// version, and the target/version/object all come from the frozen record.
func validArtifactPrepare(registration FinetuningRegistration, prepare TrainedArtifactPrepare) error {
	if prepare.IdempotencyKey != artifactIdempotencyKey(registration) ||
		prepare.Version != registration.VersionTag ||
		prepare.Format == "" || prepare.FileName == "" ||
		prepare.SizeBytes != registration.Bundle.SizeBytes ||
		prepare.ChecksumSHA256 != registration.Bundle.SHA256 ||
		prepare.SizeBytes <= 0 {
		return ErrRuntimeConflict
	}
	newBranch := registration.TargetModelID == nil
	if newBranch {
		if registration.NewModelName == nil || prepare.TargetModelID != "" ||
			prepare.ModelName != *registration.NewModelName {
			return ErrRuntimeConflict
		}
	} else if prepare.TargetModelID != *registration.TargetModelID || prepare.ModelName != "" {
		return ErrRuntimeConflict
	}
	if prepare.SourceModelVersionID != "" && prepare.SourceModelVersionID != registration.BaseModelVersionID {
		// Runtime inheritance may only reference the frozen base version.
		return ErrRuntimeConflict
	}
	if strings.TrimSpace(prepare.ModelName) != prepare.ModelName || len(prepare.DisplayName) > 128 || len(prepare.Description) > 512 || len(prepare.Capabilities) > 16 {
		return ErrRuntimeConflict
	}
	return nil
}

// artifactIdempotencyKey is the frozen business key for the registration
// session. It is derived from the execution, never chosen by a caller, so the
// same execution always maps to the same session and version.
func artifactIdempotencyKey(registration FinetuningRegistration) string {
	return registration.VersionKey
}

// matchesTrainedArtifact is the READY gate. A transport success is not READY:
// the receipt must describe the frozen target, version and exact object, and the
// provider must report the version as ready.
func matchesTrainedArtifact(registration FinetuningRegistration, prepare TrainedArtifactPrepare, receipt TrainedArtifactReceipt) bool {
	if receipt.ModelVersionID == "" || receipt.RegistrationID == "" ||
		receipt.StoragePath == "" || receipt.ModelVersionStatus != TrainedArtifactReady {
		return false
	}
	if receipt.SizeBytes != registration.Bundle.SizeBytes || receipt.ChecksumSHA256 != registration.Bundle.SHA256 {
		return false
	}
	if registration.TargetModelID != nil && receipt.ModelID != *registration.TargetModelID {
		return false
	}
	return true
}

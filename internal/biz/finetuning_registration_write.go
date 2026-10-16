package biz

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// D08.3 + D09.3 registration orchestration. This performs the concrete external
// writes for one registration, but it never invents provider policy: the
// container name/metadata, the output format mapping and the storage handover
// are inputs, because Model owns those decisions.
//
// Every write is arbitrated durably first (the shared execution row lock) and
// the RPC runs outside that arbitration. A successful response is never treated
// as READY: only reading back the original result settles the operation.

// FinetuningModelVersionReady is the only Model status that may become READY.
const FinetuningModelVersionReady = "ready"

// FinetuningWriteUnresolved is the bounded failure code stored when a permitted
// write's outcome could not be settled. It carries no provider text.
const FinetuningWriteUnresolved = "MODEL_WRITE_UNRESOLVED"

// FinetuningContainerWrite carries the provider-decided container fields for the
// new branch. It does not carry the frozen business key: that is read from the
// persisted registration intent.
type FinetuningContainerWrite struct {
	Name         string
	DisplayName  string
	Description  string
	Capabilities []string
}

// FinetuningVersionWrite carries the output version content. Format is the
// provider-decided mapping for the frozen output kind; StoragePath is the
// resolved handover target. Neither is guessed here.
type FinetuningVersionWrite struct {
	ModelID        string
	Version        string
	Format         string
	StoragePath    string
	ChecksumSHA256 string
	SizeBytes      int64
	IsEncrypted    bool
	EncryptAlgo    string
}

// FinetuningContainerFact is a read-back container result.
type FinetuningContainerFact struct {
	ModelID string
	Name    string
	Status  string
}

// FinetuningVersionFact is a read-back version result. Status distinguishes a
// pending version from a ready one, so absence is never confused with progress.
type FinetuningVersionFact struct {
	ModelVersionID string
	ModelID        string
	Version        string
	Format         string
	StoragePath    string
	ChecksumSHA256 string
	SizeBytes      int64
	Status         string
}

// FinetuningModelRegistry is the narrow outbound port for registration writes
// and their original-result verification. Implementations own only transport,
// caller identity and request metadata; they never decide whether a write is
// permitted, and they never retry by themselves.
type FinetuningModelRegistry interface {
	// CreateContainer writes the new-branch container under the original key.
	CreateContainer(context.Context, string, string, FinetuningContainerWrite) (FinetuningContainerFact, error)
	// RegisterVersion writes the output version under a resolved target.
	RegisterVersion(context.Context, string, string, FinetuningVersionWrite) (FinetuningVersionFact, error)
	// FindContainer reads back the original container by its business key. A
	// missing answer never proves that a permitted write did not happen.
	FindContainer(context.Context, string, string) (FinetuningContainerFact, bool, error)
	// FindVersion reads back the original version by target and version tag.
	FindVersion(context.Context, string, string, string) (FinetuningVersionFact, bool, error)
}

type FinetuningRegistrationOrchestrator struct {
	dispatches FinetuningRegistrationDispatchRepository
	registry   FinetuningModelRegistry
}

func NewFinetuningRegistrationOrchestrator(dispatches FinetuningRegistrationDispatchRepository, registry FinetuningModelRegistry) (*FinetuningRegistrationOrchestrator, error) {
	if dispatches == nil || registry == nil {
		return nil, ErrFinetuningNotEnabled
	}
	return &FinetuningRegistrationOrchestrator{dispatches: dispatches, registry: registry}, nil
}

// EnsureContainer writes the new-branch container at most once. A repeat call
// verifies the original attempt by its business key instead of re-sending it.
// The original key and request id are derived from the persisted intent, so a
// caller can never select a different container or key.
func (orchestrator *FinetuningRegistrationOrchestrator) EnsureContainer(ctx context.Context, authority RunAuthorityCandidate, registration FinetuningRegistration, write FinetuningContainerWrite) (FinetuningRegistrationDispatch, error) {
	if err := validContainerWrite(registration, write); err != nil {
		return FinetuningRegistrationDispatch{}, err
	}
	permit, err := orchestrator.dispatches.PermitRegistrationDispatch(ctx, authority, OpCreateModel, uuid.NewString())
	if err != nil {
		return FinetuningRegistrationDispatch{}, err
	}
	switch {
	case permit.Dispatch.State == DispatchConfirmed:
		return permit.Dispatch, nil
	case !permit.SendPermit:
		// A previous permit may still be live. Verify it; never re-send it.
		fact, found, err := orchestrator.registry.FindContainer(ctx, authority.TenantID, registration.ContainerKey)
		if err != nil || !found || fact.ModelID == "" {
			return orchestrator.settle(ctx, authority, OpCreateModel, permit.Dispatch, "", FinetuningWriteUnresolved)
		}
		return orchestrator.settle(ctx, authority, OpCreateModel, permit.Dispatch, fact.ModelID, "")
	default:
		fact, err := orchestrator.registry.CreateContainer(ctx, authority.TenantID, registration.ContainerKey, write)
		if err != nil || fact.ModelID == "" {
			return orchestrator.settle(ctx, authority, OpCreateModel, permit.Dispatch, "", FinetuningWriteUnresolved)
		}
		return orchestrator.settle(ctx, authority, OpCreateModel, permit.Dispatch, fact.ModelID, "")
	}
}

// EnsureVersion writes the output version and then verifies the original result.
// A transport success is not READY: only an exact read-back does that. The
// frozen target, version tag, size and SHA come from the persisted intent, so a
// caller cannot register a different object or target.
func (orchestrator *FinetuningRegistrationOrchestrator) EnsureVersion(ctx context.Context, authority RunAuthorityCandidate, registration FinetuningRegistration, write FinetuningVersionWrite) (FinetuningRegistrationDispatch, error) {
	if err := validVersionWrite(registration, write); err != nil {
		return FinetuningRegistrationDispatch{}, err
	}
	permit, err := orchestrator.dispatches.PermitRegistrationDispatch(ctx, authority, OpCreateModelVersion, uuid.NewString())
	if err != nil {
		return FinetuningRegistrationDispatch{}, err
	}
	if permit.Dispatch.State == DispatchConfirmed {
		return permit.Dispatch, nil
	}
	if permit.SendPermit {
		if _, err := orchestrator.registry.RegisterVersion(ctx, authority.TenantID, registration.VersionKey, write); err != nil {
			return orchestrator.settle(ctx, authority, OpCreateModelVersion, permit.Dispatch, "", FinetuningWriteUnresolved)
		}
	}
	fact, found, err := orchestrator.registry.FindVersion(ctx, authority.TenantID, write.ModelID, write.Version)
	if err != nil || !found || !matchesFrozenVersion(registration, write, fact) {
		return orchestrator.settle(ctx, authority, OpCreateModelVersion, permit.Dispatch, "", FinetuningWriteUnresolved)
	}
	return orchestrator.settle(ctx, authority, OpCreateModelVersion, permit.Dispatch, fact.ModelVersionID, "")
}

// settle records the real observed outcome. An unresolved result is UNKNOWN,
// never a failure fact: the permitted write may still have succeeded remotely,
// and only original-key verification can settle it. CONFIRMED requires a real
// receipt from the read-back.
func (orchestrator *FinetuningRegistrationOrchestrator) settle(ctx context.Context, authority RunAuthorityCandidate, operation FinetuningRegistrationOperation, permitted FinetuningRegistrationDispatch, receipt, failure string) (FinetuningRegistrationDispatch, error) {
	outcome := permitted
	if receipt == "" {
		outcome.State, outcome.ReceiptID, outcome.FailureCode = DispatchUnknown, "", failure
	} else {
		outcome.State, outcome.ReceiptID, outcome.FailureCode = DispatchConfirmed, receipt, ""
	}
	state, err := orchestrator.dispatches.RecordRegistrationOutcome(ctx, authority, operation, outcome)
	if err != nil {
		return FinetuningRegistrationDispatch{}, err
	}
	if stored := RegistrationDispatchFor(state.FinetuningRegistration, operation); stored != nil {
		return *stored, nil
	}
	return outcome, nil
}

// validContainerWrite binds the provider-decided fields to the frozen intent.
// The container write exists only for the new branch.
func validContainerWrite(registration FinetuningRegistration, write FinetuningContainerWrite) error {
	if registration.NewModelName == nil || registration.TargetModelID != nil {
		return ErrRuntimeConflict
	}
	name := strings.TrimSpace(write.Name)
	if name == "" || len(name) > 63 || len(write.DisplayName) > 128 || len(write.Description) > 512 || len(write.Capabilities) > 16 {
		return ErrRuntimeConflict
	}
	return nil
}

// validVersionWrite binds the version content to the frozen publication and the
// original keys. A caller cannot change the target, the tag or the object.
func validVersionWrite(registration FinetuningRegistration, write FinetuningVersionWrite) error {
	if write.ModelID == "" || write.Version != registration.VersionTag || write.Format == "" ||
		write.StoragePath == "" || write.ChecksumSHA256 != registration.Bundle.SHA256 ||
		write.SizeBytes != registration.Bundle.SizeBytes || write.SizeBytes <= 0 {
		return ErrRuntimeConflict
	}
	if registration.TargetModelID != nil && write.ModelID != *registration.TargetModelID {
		return ErrRuntimeConflict
	}
	return nil
}

// matchesFrozenVersion is the READY gate. It compares the read-back result
// against the frozen target, tag, exact object, size and format.
func matchesFrozenVersion(registration FinetuningRegistration, write FinetuningVersionWrite, fact FinetuningVersionFact) bool {
	return fact.ModelVersionID != "" && fact.ModelID == write.ModelID && fact.Version == write.Version &&
		fact.Format == write.Format && fact.StoragePath == write.StoragePath &&
		fact.ChecksumSHA256 == registration.Bundle.SHA256 && fact.SizeBytes == registration.Bundle.SizeBytes &&
		fact.Status == FinetuningModelVersionReady
}

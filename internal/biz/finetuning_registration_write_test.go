package biz

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// countingRegistry is an observable Model stand-in. It records real call counts
// so a test can prove that a rejected dispatch produced no external write. It
// never proves Model permissions and is not evidence for any MR.
type countingRegistry struct {
	containers      map[string]FinetuningContainerFact
	versions        map[string]FinetuningVersionFact
	createModel     int
	createVersion   int
	findContainer   int
	findVersion     int
	createModelErr  error
	remoteCommit    bool
	createVersionFn func()
	versionStatus   string
}

func newCountingRegistry() *countingRegistry {
	return &countingRegistry{containers: map[string]FinetuningContainerFact{}, versions: map[string]FinetuningVersionFact{}}
}

func (r *countingRegistry) CreateContainer(_ context.Context, _ string, key string, write FinetuningContainerWrite) (FinetuningContainerFact, error) {
	r.createModel++
	fact := FinetuningContainerFact{ModelID: "11111111-1111-4111-8111-111111111111", Name: key, Status: "active"}
	// The remote write may commit even when its response is lost; the stand-in
	// keeps the committed fact so a read-back can settle the original attempt.
	if r.remoteCommit {
		r.containers[key] = fact
	}
	if r.createModelErr != nil {
		return FinetuningContainerFact{}, r.createModelErr
	}
	r.containers[key] = fact
	return fact, nil
}

func (r *countingRegistry) RegisterVersion(_ context.Context, _ string, key string, write FinetuningVersionWrite) (FinetuningVersionFact, error) {
	r.createVersion++
	if r.createVersionFn != nil {
		r.createVersionFn()
	}
	status := r.versionStatus
	if status == "" {
		status = FinetuningModelVersionReady
	}
	fact := FinetuningVersionFact{ModelVersionID: "22222222-2222-4222-8222-222222222222", ModelID: write.ModelID, Version: write.Version, Format: write.Format, StoragePath: write.StoragePath, ChecksumSHA256: write.ChecksumSHA256, SizeBytes: write.SizeBytes, Status: status}
	r.versions[key] = fact
	return fact, nil
}

func (r *countingRegistry) FindContainer(_ context.Context, _ string, key string) (FinetuningContainerFact, bool, error) {
	r.findContainer++
	fact, found := r.containers[key]
	return fact, found, nil
}

func (r *countingRegistry) FindVersion(_ context.Context, _ string, model, version string) (FinetuningVersionFact, bool, error) {
	r.findVersion++
	for _, fact := range r.versions {
		if fact.ModelID == model && fact.Version == version {
			return fact, true, nil
		}
	}
	return FinetuningVersionFact{}, false, nil
}

type dispatchStoreStub struct {
	state   ExecutionRuntime
	permits int
	records []FinetuningRegistrationDispatch
	block   error
}

func (s *dispatchStoreStub) PermitRegistrationDispatch(_ context.Context, _ RunAuthorityCandidate, operation FinetuningRegistrationOperation, requestID string) (FinetuningRegistrationDispatchResult, error) {
	if s.block != nil {
		return FinetuningRegistrationDispatchResult{}, s.block
	}
	if s.state.FinetuningRegistration == nil {
		return FinetuningRegistrationDispatchResult{}, ErrRuntimeNotReady
	}
	if existing := RegistrationDispatchFor(s.state.FinetuningRegistration, operation); existing != nil {
		return FinetuningRegistrationDispatchResult{State: s.state, Dispatch: *existing}, nil
	}
	permit := FinetuningRegistrationDispatch{Operation: operation, State: DispatchInFlight, RequestID: requestID, ContentSHA256: s.state.FinetuningRegistration.ContentSHA256}
	s.state.FinetuningRegistration.Dispatches = append(s.state.FinetuningRegistration.Dispatches, permit)
	s.permits++
	return FinetuningRegistrationDispatchResult{State: s.state, Dispatch: permit, SendPermit: true}, nil
}

func (s *dispatchStoreStub) RecordRegistrationOutcome(_ context.Context, _ RunAuthorityCandidate, operation FinetuningRegistrationOperation, outcome FinetuningRegistrationDispatch) (ExecutionRuntime, error) {
	stored := RegistrationDispatchFor(s.state.FinetuningRegistration, operation)
	if stored == nil {
		return ExecutionRuntime{}, ErrRuntimeConflict
	}
	if *stored == outcome {
		// Mirrors the durable repository: an identical read-back is a replay.
		return s.state, nil
	}
	*stored = outcome
	s.records = append(s.records, outcome)
	return s.state, nil
}

func registrationFixture() (RunAuthorityCandidate, FinetuningRegistration) {
	versionID := "55555555-5555-4555-8555-555555555555"
	name := "cpu-ft-contract"
	bundle := cpup01.FixedObjectRef{StorageConnectionID: "store-v1", Bucket: "output", Key: "approved/aaaaaaaa/bundle.tar", VersionID: &versionID, SizeBytes: 2048, SHA256: strings.Repeat("3", 64)}
	registration := FinetuningRegistration{ContainerKey: "md-container:aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", VersionKey: "md-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", VersionTag: "exec-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", NewModelName: &name, ContentSHA256: bundle.SHA256, Bundle: bundle, State: RegistrationStatePending}
	return RunAuthorityCandidate{TenantID: "11111111-2222-4333-8444-555555555555", ExecutionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}, registration
}

func orchestratorFixture() (*FinetuningRegistrationOrchestrator, *dispatchStoreStub, *countingRegistry) {
	_, registration := registrationFixture()
	store := &dispatchStoreStub{state: ExecutionRuntime{FinetuningRegistration: &registration}}
	registry := newCountingRegistry()
	orchestrator, err := NewFinetuningRegistrationOrchestrator(store, registry)
	if err != nil {
		panic(err)
	}
	return orchestrator, store, registry
}

func TestOrchestratorContainerWritesOnceAndConfirms(t *testing.T) {
	orchestrator, _, registry := orchestratorFixture()
	authority, registration := registrationFixture()
	write := FinetuningContainerWrite{Name: registration.ContainerKey}
	dispatch, err := orchestrator.EnsureContainer(context.Background(), authority, registration, write)
	if err != nil || dispatch.State != DispatchConfirmed || registry.createModel != 1 {
		t.Fatalf("container write: %v %+v creates=%d", err, dispatch, registry.createModel)
	}
	// A repeat verifies the original key instead of writing again.
	replay, err := orchestrator.EnsureContainer(context.Background(), authority, registration, write)
	if err != nil || replay.State != DispatchConfirmed || registry.createModel != 1 {
		t.Fatalf("container replay wrote again: %v creates=%d", err, registry.createModel)
	}
}

func TestOrchestratorContainerLostResponseStaysUnknown(t *testing.T) {
	orchestrator, store, registry := orchestratorFixture()
	authority, registration := registrationFixture()
	// The permitted write's response is lost and nothing is found on read-back:
	// the outcome must stay UNKNOWN, never "no write happened".
	registry.createModelErr = errors.New("connection reset")
	dispatch, err := orchestrator.EnsureContainer(context.Background(), authority, registration, FinetuningContainerWrite{Name: registration.ContainerKey})
	if err != nil || dispatch.State != DispatchUnknown || dispatch.FailureCode != FinetuningWriteUnresolved {
		t.Fatalf("lost container response: %v %+v", err, dispatch)
	}
	// A later verification still must not re-send the original permit.
	replay, err := orchestrator.EnsureContainer(context.Background(), authority, registration, FinetuningContainerWrite{Name: registration.ContainerKey})
	if err != nil || replay.State != DispatchUnknown || registry.createModel != 1 || registry.findContainer != 1 {
		t.Fatalf("unknown permit was re-sent: %v creates=%d finds=%d", err, registry.createModel, registry.findContainer)
	}
	if len(store.records) != 1 || store.records[0].State != DispatchUnknown {
		t.Fatalf("outcome history: %+v", store.records)
	}
}

func TestOrchestratorContainerLateSuccessIsVerifiedNotResent(t *testing.T) {
	orchestrator, _, registry := orchestratorFixture()
	authority, registration := registrationFixture()
	// The permitted write committed remotely but its response was lost. Only a
	// read-back may confirm it; the permit must never be re-sent.
	registry.remoteCommit, registry.createModelErr = true, errors.New("connection reset")
	first, err := orchestrator.EnsureContainer(context.Background(), authority, registration, FinetuningContainerWrite{Name: registration.ContainerKey})
	if err != nil || first.State != DispatchUnknown {
		t.Fatalf("first attempt: %v %+v", err, first)
	}
	registry.createModelErr = nil
	second, err := orchestrator.EnsureContainer(context.Background(), authority, registration, FinetuningContainerWrite{Name: registration.ContainerKey})
	if err != nil || second.State != DispatchConfirmed || registry.createModel != 1 || registry.findContainer != 1 {
		t.Fatalf("late success was re-sent instead of verified: %v creates=%d finds=%d", err, registry.createModel, registry.findContainer)
	}
}

func TestOrchestratorVersionRequiresExactReadBack(t *testing.T) {
	orchestrator, _, registry := orchestratorFixture()
	authority, registration := registrationFixture()
	if _, err := orchestrator.EnsureVersion(context.Background(), authority, registration, FinetuningVersionWrite{}); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("empty version write was accepted: %v", err)
	}
	// The target version write binds the frozen tag and the exact frozen object.
	write := FinetuningVersionWrite{ModelID: "11111111-1111-4111-8111-111111111111", Version: registration.VersionTag, Format: "safetensors", StoragePath: "11111111/22222222/" + registration.VersionTag + "/bundle.tar", ChecksumSHA256: registration.Bundle.SHA256, SizeBytes: registration.Bundle.SizeBytes}
	dispatch, err := orchestrator.EnsureVersion(context.Background(), authority, registration, write)
	if err != nil || dispatch.State != DispatchConfirmed || registry.createVersion != 1 {
		t.Fatalf("version write: %v %+v", err, dispatch)
	}
}

func TestOrchestratorRejectsChangedFrozenContent(t *testing.T) {
	orchestrator, _, registry := orchestratorFixture()
	authority, registration := registrationFixture()
	write := FinetuningVersionWrite{ModelID: "11111111-1111-4111-8111-111111111111", Version: registration.VersionTag, Format: "safetensors", StoragePath: "path", ChecksumSHA256: registration.Bundle.SHA256, SizeBytes: registration.Bundle.SizeBytes}
	// A different SHA or size must never be registered under the frozen key.
	changed := write
	changed.ChecksumSHA256 = "9999999999999999999999999999999999999999999999999999999999999999"
	if _, err := orchestrator.EnsureVersion(context.Background(), authority, registration, changed); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("changed checksum was accepted: %v", err)
	}
	changed = write
	changed.SizeBytes = registration.Bundle.SizeBytes + 1
	if _, err := orchestrator.EnsureVersion(context.Background(), authority, registration, changed); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("changed size was accepted: %v", err)
	}
	changed = write
	changed.Version = "exec-other"
	if _, err := orchestrator.EnsureVersion(context.Background(), authority, registration, changed); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("changed version tag was accepted: %v", err)
	}
	if registry.createVersion != 0 {
		t.Fatalf("rejected writes reached the provider: %d", registry.createVersion)
	}
}

func TestOrchestratorExistingTargetUsesFrozenTarget(t *testing.T) {
	orchestrator, store, registry := orchestratorFixture()
	authority, registration := registrationFixture()
	target := "66666666-6666-4666-8666-666666666666"
	registration.TargetModelID, registration.NewModelName = &target, nil
	store.state.FinetuningRegistration = &registration
	// The existing branch never writes a container.
	if _, err := orchestrator.EnsureContainer(context.Background(), authority, registration, FinetuningContainerWrite{Name: "any"}); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("existing branch wrote a container: %v", err)
	}
	// A version write to a different target than the frozen one is rejected.
	write := FinetuningVersionWrite{ModelID: "77777777-7777-4777-8777-777777777777", Version: registration.VersionTag, Format: "safetensors", StoragePath: "path", ChecksumSHA256: registration.Bundle.SHA256, SizeBytes: registration.Bundle.SizeBytes}
	if _, err := orchestrator.EnsureVersion(context.Background(), authority, registration, write); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("changed target was accepted: %v", err)
	}
	write.ModelID = target
	if _, err := orchestrator.EnsureVersion(context.Background(), authority, registration, write); err != nil || registry.createVersion != 1 {
		t.Fatalf("frozen target version write: %v", err)
	}
}

func TestOrchestratorPendingVersionIsNotReady(t *testing.T) {
	orchestrator, _, registry := orchestratorFixture()
	authority, registration := registrationFixture()
	registry.versionStatus = "pending"
	write := FinetuningVersionWrite{ModelID: "11111111-1111-4111-8111-111111111111", Version: registration.VersionTag, Format: "safetensors", StoragePath: "path", ChecksumSHA256: registration.Bundle.SHA256, SizeBytes: registration.Bundle.SizeBytes}
	dispatch, err := orchestrator.EnsureVersion(context.Background(), authority, registration, write)
	if err != nil || dispatch.State != DispatchUnknown {
		t.Fatalf("a pending version was treated as READY: %v %+v", err, dispatch)
	}
	// The registration record must not project READY while the version is pending.
	state, err := orchestrator.dispatches.RecordRegistrationOutcome(context.Background(), authority, OpCreateModelVersion, dispatch)
	if err != nil || state.FinetuningRegistration.State == RegistrationStateReady {
		t.Fatalf("pending version projected READY: %v %+v", err, state.FinetuningRegistration)
	}
}

func TestOrchestratorStopBeforeWriteProducesNoProviderCall(t *testing.T) {
	orchestrator, store, registry := orchestratorFixture()
	authority, registration := registrationFixture()
	store.block = ErrPipelineDispatchBlocked
	if _, err := orchestrator.EnsureContainer(context.Background(), authority, registration, FinetuningContainerWrite{Name: registration.ContainerKey}); !errors.Is(err, ErrPipelineDispatchBlocked) {
		t.Fatalf("blocked dispatch was not surfaced: %v", err)
	}
	if registry.createModel != 0 || registry.createVersion != 0 {
		t.Fatalf("a rejected dispatch reached the provider: %d %d", registry.createModel, registry.createVersion)
	}
}

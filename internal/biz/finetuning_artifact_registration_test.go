package biz

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// This exercises the trained-artifact registration session against an observable
// provider stand-in. It proves the orchestration contract, not any Model
// permission or a real provider receipt.
type countingArtifactProvider struct {
	prepares   int
	completes  int
	aborts     int
	prepareErr error
	copyErr    error
	abortErr   error
	status     string
	receipt    TrainedArtifactReceipt
	ticket     TrainedArtifactTicket
}

func (p *countingArtifactProvider) PrepareTrainedArtifact(_ context.Context, _ string, _ TrainedArtifactPrepare) (TrainedArtifactTicket, error) {
	p.prepares++
	if p.prepareErr != nil {
		return TrainedArtifactTicket{}, p.prepareErr
	}
	return p.ticket, nil
}

func (p *countingArtifactProvider) CompleteTrainedArtifact(_ context.Context, _ string, _ string, _ string) (TrainedArtifactReceipt, error) {
	p.completes++
	receipt := p.receipt
	status := p.status
	if status == "" {
		status = TrainedArtifactReady
	}
	receipt.ModelVersionStatus = status
	return receipt, nil
}

func (p *countingArtifactProvider) AbortTrainedArtifact(_ context.Context, _ string, _ string, _ string) error {
	p.aborts++
	return p.abortErr
}

type countingCopier struct {
	calls int
	err   error
}

func (c *countingCopier) Upload(_ context.Context, _ cpup01.StorageScope, _ cpup01.FixedObjectRef, _ UploadTicket) (UploadReceipt, error) {
	c.calls++
	if c.err != nil {
		return UploadReceipt{}, c.err
	}
	return UploadReceipt{}, nil
}

func artifactRegistrationFixture() (RunAuthorityCandidate, FinetuningRegistration, cpup01.StorageScope, TrainedArtifactPrepare) {
	versionID := "55555555-5555-4555-8555-555555555555"
	name := "cpu-ft-contract"
	executionID := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	bundle := cpup01.FixedObjectRef{StorageConnectionID: "store-v1", Bucket: "output", Key: "approved/" + executionID + "/bundle.tar", VersionID: &versionID, SizeBytes: 2048, SHA256: strings.Repeat("3", 64)}
	base := "44444444-4444-4444-8444-444444444444"
	registration := FinetuningRegistration{ContainerKey: "md-container:" + executionID, VersionKey: "md-" + executionID, VersionTag: "exec-" + executionID, NewModelName: &name, BaseModelVersionID: base, ContentSHA256: bundle.SHA256, Bundle: bundle, State: RegistrationStatePending}
	authority := RunAuthorityCandidate{TenantID: "11111111-2222-4333-8444-555555555555", ExecutionID: executionID}
	scope := cpup01.StorageScope{StorageConnectionID: bundle.StorageConnectionID, Bucket: bundle.Bucket, ApprovedPrefix: "approved/" + executionID}
	prepare := TrainedArtifactPrepare{IdempotencyKey: registration.VersionKey, ModelName: name, SourceModelVersionID: base, Version: registration.VersionTag, FileName: "model.tar", Format: "safetensors", SizeBytes: bundle.SizeBytes, ChecksumSHA256: bundle.SHA256}
	return authority, registration, scope, prepare
}

func artifactOrchestratorFixture() (*TrainedArtifactRegistrationOrchestrator, *dispatchStoreStub, *countingArtifactProvider, *countingCopier, RunAuthorityCandidate, FinetuningRegistration, cpup01.StorageScope, TrainedArtifactPrepare) {
	authority, registration, scope, prepare := artifactRegistrationFixture()
	store := &dispatchStoreStub{state: ExecutionRuntime{FinetuningRegistration: &registration}}
	provider := &countingArtifactProvider{
		ticket:  TrainedArtifactTicket{RegistrationID: "reg-1", Upload: UploadTicket{URL: "https://model.example.test/u", StoragePath: "11111111/22222222/exec-1/model.tar", Headers: map[string]string{"X-Amz-Server-Side-Encryption": "AES256"}, SizeBytes: registration.Bundle.SizeBytes, SHA256: registration.Bundle.SHA256, ExpiresAt: time.Now().Add(15 * time.Minute)}},
		receipt: TrainedArtifactReceipt{RegistrationID: "reg-1", ModelID: "66666666-6666-4666-8666-666666666666", ModelVersionID: "77777777-7777-4777-8777-777777777777", StoragePath: "11111111/22222222/exec-1/model.tar", SizeBytes: registration.Bundle.SizeBytes, ChecksumSHA256: registration.Bundle.SHA256},
	}
	copier := &countingCopier{}
	orchestrator, err := NewTrainedArtifactRegistrationOrchestrator(store, provider, copier)
	if err != nil {
		panic(err)
	}
	return orchestrator, store, provider, copier, authority, registration, scope, prepare
}

func TestArtifactSessionRegistersOnceAndConfirms(t *testing.T) {
	orchestrator, _, provider, copier, authority, registration, scope, prepare := artifactOrchestratorFixture()
	dispatch, err := orchestrator.RegisterTrainedArtifact(context.Background(), authority, registration, scope, prepare)
	if err != nil || dispatch.State != DispatchConfirmed {
		t.Fatalf("session did not confirm: %v %+v", err, dispatch)
	}
	if provider.prepares != 1 || provider.completes != 1 || copier.calls != 1 || provider.aborts != 0 {
		t.Fatalf("session calls: prepare=%d complete=%d copy=%d abort=%d", provider.prepares, provider.completes, copier.calls, provider.aborts)
	}
	// A repeat verifies the stored original result instead of writing again.
	replay, err := orchestrator.RegisterTrainedArtifact(context.Background(), authority, registration, scope, prepare)
	if err != nil || replay.State != DispatchConfirmed || provider.prepares != 1 {
		t.Fatalf("confirmed session was re-sent: %v prepares=%d", err, provider.prepares)
	}
}

func TestArtifactSessionAbortsWhenTheCopyFails(t *testing.T) {
	orchestrator, _, provider, copier, authority, registration, scope, prepare := artifactOrchestratorFixture()
	copier.err = errors.New("connection reset")
	dispatch, err := orchestrator.RegisterTrainedArtifact(context.Background(), authority, registration, scope, prepare)
	if err != nil || dispatch.State != DispatchUnknown {
		t.Fatalf("failed copy did not stay unresolved: %v %+v", err, dispatch)
	}
	if provider.completes != 0 || provider.aborts != 1 {
		t.Fatalf("a failed copy completed or left the session open: complete=%d abort=%d", provider.completes, provider.aborts)
	}
}

func TestArtifactSessionStaysUnresolvedWhenPrepareFails(t *testing.T) {
	orchestrator, _, provider, copier, authority, registration, scope, prepare := artifactOrchestratorFixture()
	provider.prepareErr = errors.New("connection reset")
	dispatch, err := orchestrator.RegisterTrainedArtifact(context.Background(), authority, registration, scope, prepare)
	if err != nil || dispatch.State != DispatchUnknown || dispatch.FailureCode != FinetuningWriteUnresolved {
		t.Fatalf("failed prepare did not stay unresolved: %v %+v", err, dispatch)
	}
	if copier.calls != 0 {
		t.Fatalf("a copy ran without an opened session: %d", copier.calls)
	}
}

func TestArtifactSessionRejectsAPendingVersion(t *testing.T) {
	orchestrator, _, provider, _, authority, registration, scope, prepare := artifactOrchestratorFixture()
	provider.status = "pending"
	dispatch, err := orchestrator.RegisterTrainedArtifact(context.Background(), authority, registration, scope, prepare)
	if err != nil || dispatch.State != DispatchUnknown {
		t.Fatalf("a pending version was treated as READY: %v %+v", err, dispatch)
	}
}

func TestArtifactSessionRejectsChangedFrozenContent(t *testing.T) {
	orchestrator, _, provider, copier, authority, registration, scope, prepare := artifactOrchestratorFixture()
	changed := prepare
	changed.ChecksumSHA256 = strings.Repeat("9", 64)
	if _, err := orchestrator.RegisterTrainedArtifact(context.Background(), authority, registration, scope, changed); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("changed digest was accepted: %v", err)
	}
	changed = prepare
	changed.Version = "exec-other"
	if _, err := orchestrator.RegisterTrainedArtifact(context.Background(), authority, registration, scope, changed); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("changed version tag was accepted: %v", err)
	}
	changed = prepare
	changed.IdempotencyKey = "md-other-execution"
	if _, err := orchestrator.RegisterTrainedArtifact(context.Background(), authority, registration, scope, changed); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("a caller-chosen session key was accepted: %v", err)
	}
	changed = prepare
	changed.ModelName = "other-name"
	if _, err := orchestrator.RegisterTrainedArtifact(context.Background(), authority, registration, scope, changed); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("a changed container name was accepted: %v", err)
	}
	changed = prepare
	changed.SourceModelVersionID = "99999999-9999-4999-8999-999999999999"
	if _, err := orchestrator.RegisterTrainedArtifact(context.Background(), authority, registration, scope, changed); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("runtime inheritance from a non-base version was accepted: %v", err)
	}
	if provider.prepares != 0 || copier.calls != 0 {
		t.Fatalf("rejected sessions reached the provider: prepare=%d copy=%d", provider.prepares, copier.calls)
	}
}

func TestArtifactSessionExistingTargetUsesFrozenTarget(t *testing.T) {
	orchestrator, _, provider, _, authority, registration, scope, prepare := artifactOrchestratorFixture()
	target := "88888888-8888-4888-8888-888888888888"
	registration.TargetModelID, registration.NewModelName = &target, nil
	prepare.TargetModelID, prepare.ModelName = target, ""
	provider.receipt.ModelID = target
	dispatch, err := orchestrator.RegisterTrainedArtifact(context.Background(), authority, registration, scope, prepare)
	if err != nil || dispatch.State != DispatchConfirmed {
		t.Fatalf("existing-target session: %v %+v", err, dispatch)
	}
	// A session that tries to open a different container must be rejected.
	changed := prepare
	changed.TargetModelID = "99999999-9999-4999-8999-999999999999"
	if _, err := orchestrator.RegisterTrainedArtifact(context.Background(), authority, registration, scope, changed); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("a changed target was accepted: %v", err)
	}
}

func TestArtifactSessionStopBeforeWriteProducesNoProviderCall(t *testing.T) {
	orchestrator, store, provider, copier, authority, registration, scope, prepare := artifactOrchestratorFixture()
	store.block = ErrPipelineDispatchBlocked
	if _, err := orchestrator.RegisterTrainedArtifact(context.Background(), authority, registration, scope, prepare); !errors.Is(err, ErrPipelineDispatchBlocked) {
		t.Fatalf("blocked dispatch was not surfaced: %v", err)
	}
	if provider.prepares != 0 || copier.calls != 0 {
		t.Fatalf("a rejected dispatch reached the provider: prepare=%d copy=%d", provider.prepares, copier.calls)
	}
}

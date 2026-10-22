package biz_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// These exercise the RegisterModel wiring against observable outbound ports.
// They prove the orchestration and the READY gate, not any Model permission or
// a real provider receipt.

func TestRegisterModelDrivesTheSessionAndConfirms(t *testing.T) {
	register := newRegisterRuntime()
	result, err := register.runtime.RegisterModel(context.Background(), "current-bound-token", register.request)
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if register.provider.prepares != 1 || register.provider.completes != 1 || register.copier.calls != 1 {
		t.Fatalf("session calls: prepare=%d complete=%d copy=%d", register.provider.prepares, register.provider.completes, register.copier.calls)
	}
	if result.Runtime.FinetuningRegistration == nil || result.Runtime.FinetuningRegistration.State != biz.RegistrationStateReady {
		t.Fatalf("registration did not project READY: %+v", result.Runtime.FinetuningRegistration)
	}
	if result.Runtime.Publication == nil || result.Runtime.Publication.Bundle == nil {
		t.Fatal("the verified publication was not preserved")
	}
	// The provider request must carry the frozen identity, not a caller value.
	prepare := register.provider.lastPrepare
	intent := register.store.state.FinetuningRegistration
	if prepare.IdempotencyKey != intent.VersionKey || prepare.Version != intent.VersionTag ||
		prepare.ChecksumSHA256 != intent.Bundle.SHA256 || prepare.SizeBytes != intent.Bundle.SizeBytes ||
		prepare.ModelName != *intent.NewModelName || prepare.SourceModelVersionID != intent.BaseModelVersionID {
		t.Fatalf("provider request was not bound to the frozen intent: %+v", prepare)
	}
	// A repeat returns the stored verified result without a second session.
	replay, err := register.runtime.RegisterModel(context.Background(), "current-bound-token", register.request)
	if err != nil || replay.Runtime.FinetuningRegistration.State != biz.RegistrationStateReady || register.provider.prepares != 1 {
		t.Fatalf("READY registration was re-sent: %v prepares=%d", err, register.provider.prepares)
	}
}

func TestRegisterModelRejectsChangedPlanBeforeAnyCall(t *testing.T) {
	register := newRegisterRuntime()
	// A plan that cannot describe one provider artifact is rejected at wiring
	// time, so composition fails closed rather than sending an unusable request.
	for _, plan := range []biz.FinetuningArtifactPlan{
		{},
		{FileName: "model.tar"},
		{FileName: "model.tar", Format: "safetensors", Capabilities: []string{"has space"}},
	} {
		if _, err := register.runtime.WithFinetuningRegistration(register.orchestrator, plan); !errors.Is(err, biz.ErrRuntimeConflict) {
			t.Fatalf("unusable artifact plan was accepted: %+v", plan)
		}
	}
	if register.provider.prepares != 0 {
		t.Fatalf("a rejected plan reached the provider: %d", register.provider.prepares)
	}
}

func TestRegisterModelStaysUnresolvedWhenTheProviderFails(t *testing.T) {
	register := newRegisterRuntime()
	register.provider.prepareErr = errors.New("connection reset")
	result, err := register.runtime.RegisterModel(context.Background(), "current-bound-token", register.request)
	if !errors.Is(err, biz.ErrModelWriteUnresolved) {
		t.Fatalf("provider failure was not surfaced as unresolved: %v", err)
	}
	if register.store.state.FinetuningRegistration.State == biz.RegistrationStateReady {
		t.Fatal("an unresolved session projected READY")
	}
	if register.store.state.FinetuningRegistration.State != biz.RegistrationStatePending {
		t.Fatalf("the original intent was not preserved: %+v", register.store.state.FinetuningRegistration)
	}
	// The result still carries the committed projection for the caller.
	if result.Runtime.FinetuningRegistration == nil {
		t.Fatal("unresolved register dropped the committed facts")
	}
}

func TestRegisterModelPendingVersionIsNotReady(t *testing.T) {
	register := newRegisterRuntime()
	register.provider.status = "pending"
	if _, err := register.runtime.RegisterModel(context.Background(), "current-bound-token", register.request); !errors.Is(err, biz.ErrModelWriteUnresolved) {
		t.Fatalf("a pending version was reported as a settled result: %v", err)
	}
	if register.store.state.FinetuningRegistration.State == biz.RegistrationStateReady {
		t.Fatal("a pending provider version projected READY")
	}
}

func TestRegisterModelUnwiredKeepsTheOriginalBoundary(t *testing.T) {
	register := newRegisterRuntime()
	// Without the registration session, the frozen intent is still persisted as
	// the original dependency boundary; no provider call is attempted.
	register.runtime, _ = register.runtime.WithFinetuningRegistration(nil, biz.FinetuningArtifactPlan{})
	if register.runtime != nil {
		t.Fatal("an absent session must not produce a configured runtime")
	}
	unwired := newRegisterRuntime()
	unwired.runtime, _ = biz.NewManagedRuntime(unwired.steps, unwired.store, &taskIdentityUnused{}, &taskIdentityUnused{}, &taskIdentityUnused{})
	if _, err := unwired.runtime.RegisterModel(context.Background(), "current-bound-token", unwired.request); !errors.Is(err, biz.ErrModelDependencyUnavailable) {
		t.Fatalf("unwired register did not keep its boundary: %v", err)
	}
	if unwired.provider.prepares != 0 || unwired.copier.calls != 0 {
		t.Fatalf("unwired register reached the provider: prepare=%d copy=%d", unwired.provider.prepares, unwired.copier.calls)
	}
	if unwired.store.state.FinetuningRegistration == nil || unwired.store.state.FinetuningRegistration.State != biz.RegistrationStateUnavailable {
		t.Fatalf("unwired register did not persist the unavailable intent: %+v", unwired.store.state.FinetuningRegistration)
	}
}

type registerFixture struct {
	runtime      *biz.ManagedRuntime
	steps        *biz.ManagedSteps
	orchestrator *biz.TrainedArtifactRegistrationOrchestrator
	store        *registerRuntimeStore
	provider     *registerProvider
	copier       *registerCopier
	request      biz.BeginManagedExecutionRequest
}

func strptr(value string) *string { return &value }

func newRegisterRuntime() *registerFixture {
	snapshot := conformance.FinetuningSnapshotV1()
	request := biz.BeginManagedExecutionRequest{TenantID: "11111111-1111-4111-8111-111111111111", ExecutionID: "22222222-2222-4222-8222-222222222222", OperationID: "44444444-4444-4444-8444-444444444444", SpecHash: conformance.SnapshotSHA256V1, Association: biz.ManagedStepAssociation{RunID: "55555555-5555-4555-8555-555555555555", NamespaceName: snapshot.Environment.NamespaceName, NamespaceUID: snapshot.Environment.NamespaceUID, WorkflowName: "register-flow", WorkflowUID: "66666666-6666-4666-8666-666666666666", PodName: "register-pod", PodUID: "77777777-7777-4777-8777-777777777777"}}
	plan := biz.PipelineDispatchPlan{TenantID: request.TenantID, ExecutionID: request.ExecutionID, OperationID: request.OperationID, SpecHash: request.SpecHash, Environment: snapshot.Environment}
	dispatch := biz.PipelineDispatch{Plan: plan, AttemptID: "synthetic-attempt", PlanHash: strings.Repeat("a", 64)}
	a := request.Association
	authority := biz.RunAuthority{RunAuthorityCandidate: biz.RunAuthorityCandidate{TenantID: plan.TenantID, ExecutionID: plan.ExecutionID, OperationID: plan.OperationID, SpecHash: plan.SpecHash, AttemptID: dispatch.AttemptID, PlanHash: dispatch.PlanHash, RunID: a.RunID, NamespaceName: a.NamespaceName, NamespaceUID: a.NamespaceUID, WorkflowName: a.WorkflowName, WorkflowUID: a.WorkflowUID}}
	// The execution is already published: RegisterModel only runs after the
	// publication was durably verified.
	version := "88888888-8888-4888-8888-888888888888"
	bundle := cpup01.FixedObjectRef{StorageConnectionID: snapshot.PublicationScope.StorageConnectionID, Bucket: snapshot.PublicationScope.Bucket, Key: snapshot.PublicationScope.ApprovedPrefix + "/" + request.ExecutionID + "/bundle.tar", VersionID: &version, SizeBytes: 4096, SHA256: strings.Repeat("3", 64)}
	execution := biz.Execution{Admission: biz.Admission{Snapshot: snapshot}}
	// RegisterModel only runs after a verified publication, and the projection
	// requires the whole chain behind it: prepared workspace, reserved training,
	// a succeeded observation with no writers, then the publication.
	workspace := biz.WorkspaceBinding{Mode: snapshot.Workspace.Mode, NamespaceName: a.NamespaceName, NamespaceUID: a.NamespaceUID, PVCName: "pvc", PVCUID: "99999999-9999-4999-8999-999999999999", PreparedManifestBytes: 1, PreparedManifestSHA256: strings.Repeat("b", 64)}
	training := biz.TrainingPlan{TenantID: request.TenantID, ExecutionID: request.ExecutionID, SpecHash: request.SpecHash, Snapshot: snapshot, Workspace: workspace}
	handle := biz.TrainingHandle{NamespaceUID: a.NamespaceUID, TrainJobUID: "aaaaaaaa-1111-4111-8111-111111111111", PVCUID: workspace.PVCUID}
	state := biz.ExecutionRuntime{
		Workspace:      &workspace,
		Training:       &training,
		TrainingHandle: &handle,
		Observation:    &biz.TrainingRuntimeObservation{Handle: handle, Outcome: "SUCCEEDED", WritersAbsent: true, ObservedAt: time.Now()},
		Publication:    &biz.RuntimePublication{ID: "publication-1", Bundle: &bundle},
	}
	store := &registerRuntimeStore{state: state}
	provider := &registerProvider{status: biz.TrainedArtifactReady}
	copier := &registerCopier{}
	orchestrator, err := biz.NewTrainedArtifactRegistrationOrchestrator(store, provider, copier)
	if err != nil {
		panic(err)
	}
	steps, err := biz.NewManagedSteps(&registerAuthority{dispatch: dispatch, authority: authority}, &registerExecutions{execution: execution}, &registerWorkload{}, &registerRuns{})
	if err != nil {
		panic(err)
	}
	runtime, err := biz.NewManagedRuntime(steps, store, &taskIdentityUnused{}, &taskIdentityUnused{}, &taskIdentityUnused{})
	if err != nil {
		panic(err)
	}
	runtime, err = runtime.WithFinetuningRegistration(orchestrator, biz.FinetuningArtifactPlan{FileName: "model.tar", Format: "safetensors", DisplayName: "CPU FT", Capabilities: []string{"text-generation"}})
	if err != nil {
		panic(err)
	}
	return &registerFixture{runtime: runtime, steps: steps, orchestrator: orchestrator, store: store, provider: provider, copier: copier, request: request}
}

type registerAuthority struct {
	biz.RunAuthorityRepository
	dispatch  biz.PipelineDispatch
	authority biz.RunAuthority
}

func (p *registerAuthority) Get(context.Context, string, string) (biz.PipelineDispatch, error) {
	return p.dispatch, nil
}
func (p *registerAuthority) GetRunAuthority(context.Context, string, string) (biz.RunAuthority, error) {
	return p.authority, nil
}

type registerExecutions struct {
	biz.ExecutionRepository
	execution biz.Execution
}

func (p *registerExecutions) Get(context.Context, string, string) (biz.Execution, error) {
	return p.execution, nil
}

type registerWorkload struct{ biz.ManagedWorkloadVerifier }

func (*registerWorkload) Verify(context.Context, string, biz.PipelineDispatchPlan, biz.ManagedStepAssociation) error {
	return nil
}

type registerRuns struct{ biz.ManagedRunVerifier }

func (*registerRuns) VerifyManagedRun(context.Context, biz.PipelineDispatchPlan, biz.ManagedStepAssociation, string) error {
	return nil
}

// registerRuntimeStore is the durable-state stand-in. It applies the intent and
// dispatch arbitration the way the repository contract requires, so the wiring
// is exercised against real state transitions rather than blindly approved.
type registerRuntimeStore struct {
	biz.ExecutionRuntimeRepository
	state  biz.ExecutionRuntime
	intent int
}

func (s *registerRuntimeStore) GetRuntime(context.Context, string, string) (biz.ExecutionRuntime, error) {
	return s.state, nil
}
func (s *registerRuntimeStore) RecordRegistrationIntent(_ context.Context, _ biz.RunAuthorityCandidate) (biz.ExecutionRuntime, bool, error) {
	if s.state.FinetuningRegistration != nil {
		return s.state, true, nil
	}
	s.intent++
	s.state.FinetuningRegistration = &biz.FinetuningRegistration{VersionKey: "md-22222222-2222-4222-8222-222222222222", VersionTag: "exec-22222222-2222-4222-8222-222222222222", ContainerKey: "md-container:22222222-2222-4222-8222-222222222222", NewModelName: strptr("cpu-ft-contract"), ContentSHA256: s.state.Publication.Bundle.SHA256, Bundle: *s.state.Publication.Bundle, State: biz.RegistrationStatePending}
	return s.state, false, nil
}
func (s *registerRuntimeStore) PermitRegistrationDispatch(_ context.Context, _ biz.RunAuthorityCandidate, operation biz.FinetuningRegistrationOperation, requestID string) (biz.FinetuningRegistrationDispatchResult, error) {
	registration := s.state.FinetuningRegistration
	if registration == nil {
		return biz.FinetuningRegistrationDispatchResult{}, biz.ErrRuntimeNotReady
	}
	if existing := biz.RegistrationDispatchFor(registration, operation); existing != nil {
		return biz.FinetuningRegistrationDispatchResult{State: s.state, Dispatch: *existing}, nil
	}
	permit := biz.FinetuningRegistrationDispatch{Operation: operation, State: biz.DispatchInFlight, RequestID: requestID, ContentSHA256: registration.ContentSHA256}
	registration.Dispatches = append(registration.Dispatches, permit)
	return biz.FinetuningRegistrationDispatchResult{State: s.state, Dispatch: permit, SendPermit: true}, nil
}
func (s *registerRuntimeStore) RecordRegistrationOutcome(_ context.Context, _ biz.RunAuthorityCandidate, operation biz.FinetuningRegistrationOperation, outcome biz.FinetuningRegistrationDispatch) (biz.ExecutionRuntime, error) {
	stored := biz.RegistrationDispatchFor(s.state.FinetuningRegistration, operation)
	if stored == nil {
		return biz.ExecutionRuntime{}, biz.ErrRuntimeConflict
	}
	if *stored != outcome {
		*stored = outcome
	}
	if operation == biz.OpRegisterTrainedArtifact && outcome.State == biz.DispatchConfirmed {
		s.state.FinetuningRegistration.State = biz.RegistrationStateReady
	}
	return s.state, nil
}
func (s *registerRuntimeStore) RecordRegistrationUnavailable(_ context.Context, _ biz.RunAuthorityCandidate) (biz.ExecutionRuntime, bool, error) {
	if s.state.FinetuningRegistration != nil {
		return s.state, false, biz.ErrRuntimeConflict
	}
	s.state.FinetuningRegistration = &biz.FinetuningRegistration{State: biz.RegistrationStateUnavailable, FailureCode: biz.RegistrationUnavailableFailureCode}
	return s.state, false, nil
}

type registerProvider struct {
	prepares    int
	completes   int
	aborts      int
	prepareErr  error
	status      string
	lastPrepare biz.TrainedArtifactPrepare
}

func (p *registerProvider) PrepareTrainedArtifact(_ context.Context, _ string, prepare biz.TrainedArtifactPrepare) (biz.TrainedArtifactTicket, error) {
	p.prepares++
	p.lastPrepare = prepare
	if p.prepareErr != nil {
		return biz.TrainedArtifactTicket{}, p.prepareErr
	}
	return biz.TrainedArtifactTicket{RegistrationID: "reg-1", Upload: biz.UploadTicket{URL: "https://storage.example.test/copy", StoragePath: "scope/exec-1/model.tar", Headers: map[string]string{"X-Amz-Server-Side-Encryption": "AES256"}, SizeBytes: prepare.SizeBytes, SHA256: prepare.ChecksumSHA256, ExpiresAt: time.Now().Add(15 * time.Minute)}}, nil
}
func (p *registerProvider) CompleteTrainedArtifact(_ context.Context, _ string, _ string, _ string) (biz.TrainedArtifactReceipt, error) {
	p.completes++
	status := p.status
	if status == "" {
		status = biz.TrainedArtifactReady
	}
	return biz.TrainedArtifactReceipt{RegistrationID: "reg-1", ModelID: "99999999-9999-4999-8999-999999999999", ModelVersionID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", ModelVersionStatus: status, StoragePath: "scope/exec-1/model.tar", SizeBytes: p.lastPrepare.SizeBytes, ChecksumSHA256: p.lastPrepare.ChecksumSHA256}, nil
}
func (p *registerProvider) AbortTrainedArtifact(_ context.Context, _ string, _ string, _ string) error {
	p.aborts++
	return nil
}

type registerCopier struct {
	calls int
	err   error
}

func (c *registerCopier) Upload(_ context.Context, _ cpup01.StorageScope, _ cpup01.FixedObjectRef, _ biz.UploadTicket) (biz.UploadReceipt, error) {
	c.calls++
	if c.err != nil {
		return biz.UploadReceipt{}, c.err
	}
	return biz.UploadReceipt{}, nil
}

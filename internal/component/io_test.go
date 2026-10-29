package component_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/component"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestComponentPublishAsksOwnerForTaskIdentityBeforeRequiringIt(t *testing.T) {
	config := taskConfiguration(t, modeldevv1.PipelineStep_PIPELINE_STEP_PUBLISH)
	config.PublicationRefFile = filepath.Join(t.TempDir(), "candidate.json")
	ownerFailure := errors.New("synthetic owner boundary unavailable")
	client := &taskConfigurationRPC{err: ownerFailure}
	runner, err := component.New(config, client, nil, s3.New(s3.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), "publish"); !errors.Is(err, ownerFailure) || client.configurationCalls != 1 {
		t.Fatalf("publish rejected a missing callback task ID before asking its authenticated owner: calls=%d err=%v", client.configurationCalls, err)
	}
}

func TestComponentConfigurationChecksOwnerResolvedTaskIdentity(t *testing.T) {
	for _, test := range []struct {
		name, resolved string
		denied         bool
	}{
		{name: "owner resolved", resolved: "real-backend-task"},
		{name: "missing owner task", denied: true},
		{name: "malformed returned", resolved: "task with space", denied: true},
		{name: "oversized returned", resolved: strings.Repeat("a", 257), denied: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := taskConfiguration(t, modeldevv1.PipelineStep_PIPELINE_STEP_TRAIN_WAIT)
			response := taskConfigurationResponse(t, config)
			response.KfpTaskId = test.resolved
			client := &taskConfigurationRPC{response: response}
			runner, err := component.New(config, client, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = runner.Run(context.Background(), "train-wait")
			if test.denied {
				if !errors.Is(err, component.ErrConfiguration) || client.ensureCalls != 0 {
					t.Fatalf("unverified/mismatching task ID reached training RPC: calls=%d err=%v", client.ensureCalls, err)
				}
			} else if !errors.Is(err, taskConfigurationStop) || client.ensureCalls != 1 {
				t.Fatalf("authenticated configuration failed before training boundary: calls=%d err=%v", client.ensureCalls, err)
			}
		})
	}
}

var taskConfigurationStop = errors.New("synthetic training boundary stop")

type taskConfigurationRPC struct {
	modeldevv1.ModelDevStepServiceClient
	response                        *modeldevv1.GetExecutionConfigurationResponse
	err                             error
	configurationCalls, ensureCalls int
	timingOverride                  *modeldevv1.StepTiming
	configurationDeadline           time.Time
}

func (p *taskConfigurationRPC) GetExecutionConfiguration(ctx context.Context, _ *modeldevv1.GetExecutionConfigurationRequest, _ ...grpc.CallOption) (*modeldevv1.GetExecutionConfigurationResponse, error) {
	p.configurationCalls++
	p.configurationDeadline, _ = ctx.Deadline()
	return p.response, p.err
}
func (p *taskConfigurationRPC) EnsureTraining(context.Context, *modeldevv1.EnsureTrainingRequest, ...grpc.CallOption) (*modeldevv1.EnsureTrainingResponse, error) {
	p.ensureCalls++
	return nil, taskConfigurationStop
}
func (p *taskConfigurationRPC) BeginManagedStep(_ context.Context, r *modeldevv1.BeginManagedStepRequest, _ ...grpc.CallOption) (*modeldevv1.BeginManagedStepResponse, error) {
	if p.timingOverride != nil {
		return &modeldevv1.BeginManagedStepResponse{Timing: p.timingOverride}, nil
	}
	if p.response == nil {
		return &modeldevv1.BeginManagedStepResponse{}, nil
	}
	timing := p.response.Snapshot.Timing
	start := timing.AcceptedAt.AsTime()
	deadline := start.Add(time.Duration(timing.StepTimeoutSeconds) * time.Second)
	if timing.ExecutionDeadlineAt.AsTime().Before(deadline) {
		deadline = timing.ExecutionDeadlineAt.AsTime()
	}
	return &modeldevv1.BeginManagedStepResponse{Timing: &modeldevv1.StepTiming{Role: r.Context.Step, AdmittedAt: timestamppb.New(start), DeadlineAt: timestamppb.New(deadline)}}, nil
}
func (p *taskConfigurationRPC) BeginExecution(ctx context.Context, r *modeldevv1.BeginExecutionRequest, opts ...grpc.CallOption) (*modeldevv1.BeginExecutionResponse, error) {
	response, err := p.BeginManagedStep(ctx, &modeldevv1.BeginManagedStepRequest{Context: r.Context}, opts...)
	if err != nil {
		return nil, err
	}
	return &modeldevv1.BeginExecutionResponse{Timing: response.Timing}, nil
}

func taskConfiguration(t *testing.T, step modeldevv1.PipelineStep) component.Config {
	t.Helper()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("synthetic-current-pod-token"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := conformance.SnapshotV1()
	return component.Config{TenantID: "11111111-1111-4111-8111-111111111111", TokenFile: token, Context: &modeldevv1.StepContext{Identity: &trainingv1.ExecutionIdentity{ExecutionId: "22222222-2222-4222-8222-222222222222", ExecutionSpecHash: conformance.SnapshotSHA256V1}, Association: &modeldevv1.RunAssociation{KfpRunId: "44444444-4444-4444-8444-444444444444", NamespaceName: snapshot.Environment.NamespaceName, NamespaceUid: snapshot.Environment.NamespaceUID, WorkflowName: "captured-flow", WorkflowUid: "66666666-6666-4666-8666-666666666666", PodName: "captured-current", PodUid: "77777777-7777-4777-8777-777777777777"}, Step: step}}
}

func taskConfigurationResponse(t *testing.T, config component.Config) *modeldevv1.GetExecutionConfigurationResponse {
	t.Helper()
	start := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	timing, err := (cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: 3600, ExecutionTimeoutMaxSeconds: 7200, StepTimeoutSeconds: 900}).Resolve(start, nil)
	if err != nil {
		t.Fatal(err)
	}
	return timedConfigurationResponse(t, config, timing)
}
func timedConfigurationResponse(t *testing.T, config component.Config, timing cpup01.Timing) *modeldevv1.GetExecutionConfigurationResponse {
	t.Helper()
	domainSnapshot := conformance.SnapshotV1()
	domainSnapshot.Environment.NamespaceName = config.Context.Association.NamespaceName
	domainSnapshot.Environment.NamespaceUID = config.Context.Association.NamespaceUid
	domainSnapshot.Timing = timing
	domainSnapshot.DeadlineAt = timing.ExecutionDeadlineAt
	domainIntent := cpup01.Intent{Name: "task identity fixture", Kind: domainSnapshot.Kind, ImplementationID: domainSnapshot.Implementation.ImplementationID, ComputePresetID: domainSnapshot.ComputeOrigin.ComputePresetID, MLP: &cpup01.MLPParameters{}, DatasetVersionID: domainSnapshot.Input.InputVersionID}
	operation := config.Context.Identity.OperationId
	if operation == "" {
		operation = "33333333-3333-4333-8333-333333333333"
	}
	admission := admissionfixture.Bind(t, biz.Admission{TenantID: config.TenantID, Actor: "governance:user:42", OperationID: operation, ExecutionID: config.Context.Identity.ExecutionId, Intent: domainIntent, Snapshot: domainSnapshot, AcceptedAt: timing.AcceptedAt})
	config.Context.Identity.ExecutionSpecHash = admission.SpecHash
	snapshot, err := contractpb.EncodeSnapshot(admission.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := contractpb.EncodeIntent(domainIntent)
	if err != nil {
		t.Fatal(err)
	}
	identity := &trainingv1.ExecutionIdentity{OperationId: operation, ExecutionId: config.Context.Identity.ExecutionId, ExecutionSpecHash: admission.SpecHash}
	a := config.Context.Association
	return &modeldevv1.GetExecutionConfigurationResponse{KfpTaskId: "authenticated-fixture-task", Identity: identity, Snapshot: snapshot, Authority: &modeldevv1.AuthorityBinding{KfpRunId: a.KfpRunId, NamespaceUid: a.NamespaceUid, WorkflowUid: a.WorkflowUid}, Admission: &modeldevv1.AcceptExecutionRequest{Identity: identity, ResourceTenantId: config.TenantID, AdmittedActorId: "governance:user:42", IntentHash: admission.IntentHash, Snapshot: snapshot, Intent: intent, AcceptedAt: timestamppb.New(admission.AcceptedAt)}}
}

func TestComponentCloseMissingCandidatePreservesFailureAfterCloseAttempt(t *testing.T) {
	directory := t.TempDir()
	tokenFile := filepath.Join(directory, "token")
	if err := os.WriteFile(tokenFile, []byte("projected-close-token"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &failedCloseRPC{t: t}
	config := component.Config{TenantID: "11111111-1111-4111-8111-111111111111", TokenFile: tokenFile, PublicationRefFile: filepath.Join(directory, "missing-candidate.json"), Context: &modeldevv1.StepContext{
		Identity:    &trainingv1.ExecutionIdentity{OperationId: "33333333-3333-4333-8333-333333333333", ExecutionId: "22222222-2222-4222-8222-222222222222", ExecutionSpecHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		Association: &modeldevv1.RunAssociation{KfpRunId: "44444444-4444-4444-8444-444444444444", NamespaceName: "cpu-execution", NamespaceUid: "55555555-5555-4555-8555-555555555555", WorkflowName: "main-flow", WorkflowUid: "66666666-6666-4666-8666-666666666666", PodName: "main-close", PodUid: "77777777-7777-4777-8777-777777777777"},
		Step:        modeldevv1.PipelineStep_PIPELINE_STEP_CLOSE,
	}}
	client.configuration = taskConfigurationResponse(t, config)
	runner, err := component.New(config, client, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Run(context.Background(), "close")
	if !errors.Is(err, component.ErrConfiguration) {
		t.Fatalf("missing publication candidate must remain the original failure, not cleanup success or error: %v", err)
	}
	if client.closeCalls != 1 {
		t.Fatalf("FAILED_CLOSE_FENCE: missing publication candidate must still request owner close: calls=%d", client.closeCalls)
	}
}

func TestComponentCloseOnlyFencesFailureWithoutPublicationAndReplaysClosed(t *testing.T) {
	directory := t.TempDir()
	tokenFile := filepath.Join(directory, "token")
	if err := os.WriteFile(tokenFile, []byte("first-close-token"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &closeOnlyRPC{t: t, tokenFile: tokenFile}
	config := component.Config{TenantID: "11111111-1111-4111-8111-111111111111", TokenFile: tokenFile, CloseOnly: true, PollInterval: time.Millisecond, Context: &modeldevv1.StepContext{
		Identity:    &trainingv1.ExecutionIdentity{ExecutionId: "22222222-2222-4222-8222-222222222222", ExecutionSpecHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		Association: &modeldevv1.RunAssociation{KfpRunId: "44444444-4444-4444-8444-444444444444", NamespaceName: "cpu-execution", NamespaceUid: "55555555-5555-4555-8555-555555555555", WorkflowName: "main-flow", WorkflowUid: "66666666-6666-4666-8666-666666666666", PodName: "main-finalizer", PodUid: "77777777-7777-4777-8777-777777777777"},
		Step:        modeldevv1.PipelineStep_PIPELINE_STEP_CLOSE,
	}}
	runner, err := component.New(config, client, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := runner.Run(context.Background(), "close"); err != nil {
			t.Fatalf("failure-only finalizer must fence without a candidate and accept CLOSED replay: %v", err)
		}
	}
	if client.closeCalls != 2 {
		t.Fatalf("close finalizer must deliver the durable fence without waiting on its own Workflow: calls=%d", client.closeCalls)
	}
}

func TestComponentCloseOnlyExitsOnDurableClosingAndRejectsIncompleteReceipt(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		config := taskConfiguration(t, modeldevv1.PipelineStep_PIPELINE_STEP_CLOSE)
		config.CloseOnly, config.PollInterval = true, time.Millisecond
		if err := os.WriteFile(config.TokenFile, []byte("first-close-token"), 0600); err != nil {
			t.Fatal(err)
		}
		client := &closeOnlyRPC{t: t, tokenFile: config.TokenFile, alwaysClosing: true, incomplete: incomplete}
		runner, err := component.New(config, client, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err = runner.Run(ctx, "close")
		cancel()
		if (!incomplete && err != nil) || (incomplete && !errors.Is(err, component.ErrConfiguration)) || client.closeCalls != 1 {
			t.Fatalf("DURABLE_CLOSE_HANDOFF_NOT_IMPLEMENTED: finalizer waited on Workflow or accepted missing fence: incomplete=%v calls=%d err=%v", incomplete, client.closeCalls, err)
		}
	}
}

// Only the external owner RPC is replaced. No publication or admitted business
// success is fabricated; the test checks the finalizer's authenticated request.
type closeOnlyRPC struct {
	modeldevv1.ModelDevStepServiceClient
	t                         *testing.T
	tokenFile                 string
	closeCalls                int
	alwaysClosing, incomplete bool
}

func (client *closeOnlyRPC) RequestExecutionClose(ctx context.Context, request *modeldevv1.RequestExecutionCloseRequest, _ ...grpc.CallOption) (*modeldevv1.RequestExecutionCloseResponse, error) {
	client.closeCalls++
	token := "rotated-close-token"
	if client.closeCalls == 1 {
		token = "first-close-token"
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	if len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer "+token || len(md.Get("x-ani-tenant-id")) != 1 || md.Get("x-ani-tenant-id")[0] != "11111111-1111-4111-8111-111111111111" || request.GetContext().GetStep() != modeldevv1.PipelineStep_PIPELINE_STEP_CLOSE || request.GetReason() != modeldevv1.CloseReason_CLOSE_REASON_STEP_FAILED || request.GetContext().GetIdentity().GetOperationId() != "" {
		client.t.Fatal("finalizer must retain current token, tenant, unresolved admitted operation and CLOSE role")
	}
	if client.closeCalls == 1 {
		if err := os.WriteFile(client.tokenFile, []byte("rotated-close-token"), 0600); err != nil {
			client.t.Fatal(err)
		}
		return client.receipt(modeldevv1.CloseState_CLOSE_STATE_CLOSING), nil
	}
	if client.alwaysClosing {
		return client.receipt(modeldevv1.CloseState_CLOSE_STATE_CLOSING), nil
	}
	return client.receipt(modeldevv1.CloseState_CLOSE_STATE_CLOSED), nil
}

func (client *closeOnlyRPC) receipt(state modeldevv1.CloseState) *modeldevv1.RequestExecutionCloseResponse {
	receipt := &modeldevv1.RequestExecutionCloseResponse{CloseState: state, Replayed: client.closeCalls > 1}
	if !client.incomplete {
		receipt.CloseGeneration = 7
		receipt.AcceptedAt = timestamppb.New(time.Date(2026, 9, 30, 9, 30, 0, 0, time.UTC))
	}
	return receipt
}

func (client *closeOnlyRPC) GetExecutionConfiguration(context.Context, *modeldevv1.GetExecutionConfigurationRequest, ...grpc.CallOption) (*modeldevv1.GetExecutionConfigurationResponse, error) {
	client.t.Fatal("pre-Begin failure finalizer must request authenticated close without requiring an existing authority")
	return nil, status.Error(codes.Unavailable, "unbound configuration")
}

func (client *closeOnlyRPC) ReportStepResult(context.Context, *modeldevv1.ReportStepResultRequest, ...grpc.CallOption) (*modeldevv1.ReportStepResultResponse, error) {
	client.t.Fatal("failure-only finalizer must never fabricate publication proof")
	return nil, status.Error(codes.InvalidArgument, "missing publication proof")
}

// Only the transport boundary is replaced; the component reads a real missing
// file and must retain that failure even when its close request fails too.
type failedCloseRPC struct {
	modeldevv1.ModelDevStepServiceClient
	t             *testing.T
	closeCalls    int
	configuration *modeldevv1.GetExecutionConfigurationResponse
}

func (client *failedCloseRPC) GetExecutionConfiguration(context.Context, *modeldevv1.GetExecutionConfigurationRequest, ...grpc.CallOption) (*modeldevv1.GetExecutionConfigurationResponse, error) {
	return client.configuration, nil
}

func (client *failedCloseRPC) RequestExecutionClose(ctx context.Context, request *modeldevv1.RequestExecutionCloseRequest, _ ...grpc.CallOption) (*modeldevv1.RequestExecutionCloseResponse, error) {
	client.closeCalls++
	deadline, ok := ctx.Deadline()
	if !ok || ctx.Err() != nil || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
		client.t.Error("failure cleanup must have an active bounded five-second context")
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	if len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer projected-close-token" || request.GetContext().GetStep() != modeldevv1.PipelineStep_PIPELINE_STEP_CLOSE || request.GetReason() != modeldevv1.CloseReason_CLOSE_REASON_STEP_FAILED {
		client.t.Error("failure cleanup must retain the authenticated close identity and STEP_FAILED reason")
	}
	return nil, status.Error(codes.Unavailable, "close observation unavailable")
}

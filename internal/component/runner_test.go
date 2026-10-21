package component_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/component"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// This is only a component/RPC-boundary test. Complete-flow integration uses
// the real Step server, repositories, adapters and external API substitutes.
func TestComponentTrainWaitUsesOnlyEnsureAndStatusWithRotatedToken(t *testing.T) {
	directory := t.TempDir()
	tokenFile := filepath.Join(directory, "token")
	if err := os.WriteFile(tokenFile, []byte("first-projected-token"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &trainingRPC{t: t, tokenFile: tokenFile}
	config := component.Config{TenantID: "11111111-1111-4111-8111-111111111111", TokenFile: tokenFile, PollInterval: time.Millisecond, Context: &modeldevv1.StepContext{
		Identity:    &trainingv1.ExecutionIdentity{OperationId: "33333333-3333-4333-8333-333333333333", ExecutionId: "22222222-2222-4222-8222-222222222222", ExecutionSpecHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		Association: &modeldevv1.RunAssociation{KfpRunId: "44444444-4444-4444-8444-444444444444", NamespaceName: "cpu-execution", NamespaceUid: "55555555-5555-4555-8555-555555555555", WorkflowName: "main-flow", WorkflowUid: "66666666-6666-4666-8666-666666666666", PodName: "main-train-wait", PodUid: "77777777-7777-4777-8777-777777777777"},
		Step:        modeldevv1.PipelineStep_PIPELINE_STEP_TRAIN_WAIT,
	}}
	client.configuration = taskConfigurationResponse(t, config)
	runner, err := component.New(config, client, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.Run(ctx, "train-wait"); err != nil {
		t.Fatalf("one managed train-wait invocation must reach observed success: %v", err)
	}
	if client.ensure != 1 || client.status != 1 {
		t.Fatalf("component must not create/restart training: ensure=%d status=%d", client.ensure, client.status)
	}
}

func TestComponentRejectsDifferentStepBeforeRPC(t *testing.T) {
	client := &trainingRPC{t: t}
	runner, err := component.New(component.Config{TenantID: "tenant", TokenFile: "unused", Context: &modeldevv1.StepContext{Identity: &trainingv1.ExecutionIdentity{}, Association: &modeldevv1.RunAssociation{}, Step: modeldevv1.PipelineStep_PIPELINE_STEP_TRAIN_WAIT}}, client, nil, nil)
	if err != nil {
		return
	}
	if err := runner.Run(context.Background(), "publish"); err == nil {
		t.Fatal("step name cannot override the verified invocation role")
	}
	if client.ensure != 0 || client.status != 0 {
		t.Fatal("invalid invocation performed RPC")
	}
}

func TestComponentTrainWaitRetriesOriginalEnsureAfterLostReplyWithinFrozenWindow(t *testing.T) {
	config := taskConfiguration(t, modeldevv1.PipelineStep_PIPELINE_STEP_TRAIN_WAIT)
	config.PollInterval = time.Millisecond
	if err := os.WriteFile(config.TokenFile, []byte("first-projected-token"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &trainingRPC{t: t, tokenFile: config.TokenFile, transientEnsure: true}
	timing, err := (cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: 3600, ExecutionTimeoutMaxSeconds: 7200, StepTimeoutSeconds: 62}).Resolve(time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute), nil)
	if err != nil {
		t.Fatal(err)
	}
	client.configuration = timedConfigurationResponse(t, config, timing)
	runner, err := component.New(config, client, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := runner.Run(ctx, "train-wait"); err != nil {
		t.Fatal("original Ensure never recovered", err)
	}
	if client.ensure != 2 || client.status != 1 || client.configurationCalls < 2 {
		t.Fatalf("Ensure must recover before status-only observation: ensure=%d status=%d configuration=%d", client.ensure, client.status, client.configurationCalls)
	}
	if client.retryDeadline.IsZero() || !client.retryDeadline.Equal(client.originalDeadline) {
		t.Fatal("uncertain Ensure renewed its original TRAIN window")
	}
}

type trainingRPC struct {
	modeldevv1.ModelDevStepServiceClient
	t                               *testing.T
	tokenFile                       string
	ensure, status                  int
	configuration                   *modeldevv1.GetExecutionConfigurationResponse
	transientEnsure                 bool
	configurationCalls              int
	originalIdentity                *trainingv1.ExecutionIdentity
	originalDeadline, retryDeadline time.Time
}

func (client *trainingRPC) GetExecutionConfiguration(ctx context.Context, r *modeldevv1.GetExecutionConfigurationRequest, _ ...grpc.CallOption) (*modeldevv1.GetExecutionConfigurationResponse, error) {
	client.configurationCalls++
	token := "first-projected-token"
	if client.ensure > 0 {
		token = "rotated-projected-token"
	}
	client.check(ctx, token, r.Context)
	response := proto.Clone(client.configuration).(*modeldevv1.GetExecutionConfigurationResponse)
	if client.ensure > 0 {
		timing := client.trainingStatus(r.Context, modeldevv1.ComputeState_COMPUTE_STATE_TRAINING)
		response.Timing = &modeldevv1.StepTiming{Role: modeldevv1.PipelineStep_PIPELINE_STEP_TRAIN_WAIT, AdmittedAt: timing.TrainingAdmittedAt, DeadlineAt: timing.TrainingDeadlineAt}
	}
	return response, nil
}
func (client *trainingRPC) trainingStatus(request *modeldevv1.StepContext, state modeldevv1.ComputeState) *modeldevv1.TrainingStatus {
	timing := client.configuration.Snapshot.Timing
	start := timing.AcceptedAt.AsTime()
	deadline := start.Add(time.Duration(timing.StepTimeoutSeconds) * time.Second)
	return &modeldevv1.TrainingStatus{Identity: request.Identity, States: &modeldevv1.ExecutionStates{ComputeState: state}, TrainingAdmittedAt: timestamppb.New(start), TrainingDeadlineAt: timestamppb.New(deadline)}
}

func (client *trainingRPC) EnsureTraining(ctx context.Context, request *modeldevv1.EnsureTrainingRequest, _ ...grpc.CallOption) (*modeldevv1.EnsureTrainingResponse, error) {
	client.ensure++
	token := "first-projected-token"
	if client.ensure > 1 {
		token = "rotated-projected-token"
	}
	client.check(ctx, token, request.Context)
	if client.originalIdentity == nil {
		client.originalIdentity = proto.Clone(request.Context.Identity).(*trainingv1.ExecutionIdentity)
	} else if !proto.Equal(client.originalIdentity, request.Context.Identity) {
		client.t.Fatal("uncertain Ensure changed original identity")
	}
	clock := client.trainingStatus(request.Context, modeldevv1.ComputeState_COMPUTE_STATE_TRAINING)
	client.originalDeadline = clock.TrainingDeadlineAt.AsTime()
	if client.ensure > 1 {
		client.retryDeadline, _ = ctx.Deadline()
	}
	if err := os.WriteFile(client.tokenFile, []byte("rotated-projected-token"), 0600); err != nil {
		client.t.Fatal(err)
	}
	if client.transientEnsure && client.ensure == 1 {
		return nil, status.Error(codes.Unavailable, "original response lost at external RPC boundary")
	}
	return &modeldevv1.EnsureTrainingResponse{Status: client.trainingStatus(request.Context, modeldevv1.ComputeState_COMPUTE_STATE_TRAINING)}, nil
}

func (client *trainingRPC) GetTrainingStatus(ctx context.Context, request *modeldevv1.GetTrainingStatusRequest, _ ...grpc.CallOption) (*modeldevv1.GetTrainingStatusResponse, error) {
	client.status++
	client.check(ctx, "rotated-projected-token", request.Context)
	return &modeldevv1.GetTrainingStatusResponse{Status: client.trainingStatus(request.Context, modeldevv1.ComputeState_COMPUTE_STATE_SUCCEEDED)}, nil
}

func (client *trainingRPC) check(ctx context.Context, token string, claim *modeldevv1.StepContext) {
	client.t.Helper()
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer "+token || len(md.Get("x-ani-tenant-id")) != 1 || md.Get("x-ani-tenant-id")[0] != "11111111-1111-4111-8111-111111111111" {
		client.t.Error("RPC must use current projected token and configured tenant")
	}
	if claim == nil || claim.Step != modeldevv1.PipelineStep_PIPELINE_STEP_TRAIN_WAIT {
		client.t.Error("wrong step claim")
	}
}

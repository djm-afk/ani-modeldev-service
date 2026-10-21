package component_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/component"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fixedTrainingBudgetRPC struct {
	modeldevv1.ModelDevStepServiceClient
	start, deadline  time.Time
	observedDeadline time.Time
	statusCalls      int
	renew            bool
	configuration    *modeldevv1.GetExecutionConfigurationResponse
}

func (c *fixedTrainingBudgetRPC) GetExecutionConfiguration(context.Context, *modeldevv1.GetExecutionConfigurationRequest, ...grpc.CallOption) (*modeldevv1.GetExecutionConfigurationResponse, error) {
	return c.configuration, nil
}

func (c *fixedTrainingBudgetRPC) EnsureTraining(_ context.Context, r *modeldevv1.EnsureTrainingRequest, _ ...grpc.CallOption) (*modeldevv1.EnsureTrainingResponse, error) {
	return &modeldevv1.EnsureTrainingResponse{Status: c.result(r.Context, modeldevv1.ComputeState_COMPUTE_STATE_TRAINING)}, nil
}
func (c *fixedTrainingBudgetRPC) GetTrainingStatus(ctx context.Context, r *modeldevv1.GetTrainingStatusRequest, _ ...grpc.CallOption) (*modeldevv1.GetTrainingStatusResponse, error) {
	c.statusCalls++
	c.observedDeadline, _ = ctx.Deadline()
	result := c.result(r.Context, modeldevv1.ComputeState_COMPUTE_STATE_SUCCEEDED)
	if c.renew {
		result.TrainingDeadlineAt = timestamppb.New(c.deadline.Add(time.Minute))
	}
	return &modeldevv1.GetTrainingStatusResponse{Status: result}, nil
}
func (c *fixedTrainingBudgetRPC) result(claim *modeldevv1.StepContext, state modeldevv1.ComputeState) *modeldevv1.TrainingStatus {
	return &modeldevv1.TrainingStatus{Identity: claim.Identity, States: &modeldevv1.ExecutionStates{ComputeState: state}, TrainingAdmittedAt: timestamppb.New(c.start), TrainingDeadlineAt: timestamppb.New(c.deadline)}
}

func TestFinetuningTrainWaitConsumesOriginalSharedDeadline(t *testing.T) {
	for _, name := range []string{"original-hour-budget", "expired", "replay-cannot-renew"} {
		t.Run(name, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Microsecond)
			client := &fixedTrainingBudgetRPC{start: now.Add(-time.Minute), deadline: now.Add(50 * time.Minute), renew: name == "replay-cannot-renew"}
			if name == "expired" {
				client.deadline = now.Add(-time.Second)
			}
			token := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(token, []byte("projected-workload-token"), 0600); err != nil {
				t.Fatal(err)
			}
			claim := &modeldevv1.StepContext{Identity: &trainingv1.ExecutionIdentity{OperationId: "33333333-3333-4333-8333-333333333333", ExecutionId: "22222222-2222-4222-8222-222222222222", ExecutionSpecHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, Association: &modeldevv1.RunAssociation{KfpRunId: "44444444-4444-4444-8444-444444444444", NamespaceName: "fine-execution", NamespaceUid: "55555555-5555-4555-8555-555555555555", WorkflowName: "main-flow", WorkflowUid: "66666666-6666-4666-8666-666666666666", PodName: "main-train-wait", PodUid: "77777777-7777-4777-8777-777777777777"}, Step: modeldevv1.PipelineStep_PIPELINE_STEP_TRAIN_WAIT}
			config := component.Config{TenantID: "11111111-1111-4111-8111-111111111111", TokenFile: token, Context: claim, PollInterval: time.Millisecond}
			timing, err := (cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: 7200, ExecutionTimeoutMaxSeconds: 10800, StepTimeoutSeconds: uint64(client.deadline.Sub(client.start) / time.Second)}).Resolve(now.Add(-2*time.Minute), nil)
			if err != nil {
				t.Fatal(err)
			}
			client.configuration = timedConfigurationResponse(t, config, timing)
			runner, err := component.New(config, client, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = runner.Run(context.Background(), "train-wait")
			switch name {
			case "expired":
				if !errors.Is(err, context.DeadlineExceeded) || client.statusCalls != 0 {
					t.Fatal("expired original budget performed another observation", err)
				}
			case "replay-cannot-renew":
				if !errors.Is(err, component.ErrConfiguration) {
					t.Fatal("changed original deadline was accepted", err)
				}
			default:
				if err != nil || client.statusCalls != 1 || !client.observedDeadline.Equal(client.deadline) {
					t.Fatal("wait used a new duration or old 1800s cap", err)
				}
			}
		})
	}
}

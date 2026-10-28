// Package component implements one invocation of one managed KFP component.
// KFP chooses the next step; this package never advances a Go workflow or
// creates training resources directly.
package component

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"k8s.io/client-go/dynamic"
)

var ErrConfiguration = errors.New("MANAGED_COMPONENT_INVALID_CONFIGURATION")
var ErrTrainingFailed = errors.New("MANAGED_COMPONENT_TRAINING_FAILED")
var ErrTrainingCanceled = errors.New("MANAGED_COMPONENT_TRAINING_CANCELED")
var ErrNeedsReview = errors.New("MANAGED_COMPONENT_CLOSE_NEEDS_REVIEW")

var bearerPattern = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)

type Config struct {
	TenantID           string
	Context            *modeldevv1.StepContext
	TokenFile          string
	WorkspaceDirectory string
	PVCName            string
	InventoryFile      string
	PublicationRefFile string
	PublicationRef     string
	CloseOnly          bool
	PollInterval       time.Duration
}

type Runner struct {
	taskID        string
	config        Config
	client        modeldevv1.ModelDevStepServiceClient
	kube          dynamic.Interface
	store         *s3.Client
	baseStore     *s3.Client
	executionKind string
	stepTiming    *modeldevv1.StepTiming
}

func New(config Config, client modeldevv1.ModelDevStepServiceClient, kube dynamic.Interface, store *s3.Client, baseStores ...*s3.Client) (*Runner, error) {
	if client == nil || config.Context == nil || config.Context.Identity == nil || config.Context.Association == nil || config.TenantID == "" || config.TokenFile == "" {
		return nil, ErrConfiguration
	}
	if config.CloseOnly && config.Context.Step != modeldevv1.PipelineStep_PIPELINE_STEP_CLOSE {
		return nil, ErrConfiguration
	}
	identifiers := []string{config.TenantID, config.Context.Identity.ExecutionId, config.Context.Association.KfpRunId, config.Context.Association.NamespaceUid, config.Context.Association.PodUid}
	if config.Context.Identity.OperationId != "" {
		identifiers = append(identifiers, config.Context.Identity.OperationId)
	}
	for _, value := range identifiers {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil || id.String() != value {
			return nil, ErrConfiguration
		}
	}
	if !filepath.IsAbs(config.TokenFile) || config.Context.Association.NamespaceName == "" || config.Context.Association.WorkflowName == "" || config.Context.Association.WorkflowUid == "" || config.Context.Association.PodName == "" || len(config.Context.Identity.ExecutionSpecHash) != 64 {
		return nil, ErrConfiguration
	}
	if _, err := hex.DecodeString(config.Context.Identity.ExecutionSpecHash); err != nil || strings.ToLower(config.Context.Identity.ExecutionSpecHash) != config.Context.Identity.ExecutionSpecHash {
		return nil, ErrConfiguration
	}
	if config.PollInterval == 0 {
		config.PollInterval = time.Second
	}
	if config.PollInterval < time.Millisecond || config.PollInterval > 30*time.Second {
		return nil, ErrConfiguration
	}
	config.Context = proto.Clone(config.Context).(*modeldevv1.StepContext)
	if len(baseStores) > 1 {
		return nil, ErrConfiguration
	}
	var baseStore *s3.Client
	if len(baseStores) == 1 {
		baseStore = baseStores[0]
	}
	return &Runner{config: config, client: client, kube: kube, store: store, baseStore: baseStore}, nil
}

func (runner *Runner) Run(ctx context.Context, step string) error {
	if runner == nil || ctx == nil {
		return ErrConfiguration
	}
	wanted, ok := map[string]modeldevv1.PipelineStep{"prepare": modeldevv1.PipelineStep_PIPELINE_STEP_PREPARE, "train-wait": modeldevv1.PipelineStep_PIPELINE_STEP_TRAIN_WAIT, "collect": modeldevv1.PipelineStep_PIPELINE_STEP_COLLECT, "publish": modeldevv1.PipelineStep_PIPELINE_STEP_PUBLISH, "close": modeldevv1.PipelineStep_PIPELINE_STEP_CLOSE, "export": modeldevv1.PipelineStep_PIPELINE_STEP_EXPORT, "verify": modeldevv1.PipelineStep_PIPELINE_STEP_VERIFY, "register": modeldevv1.PipelineStep_PIPELINE_STEP_REGISTER}[step]
	if !ok || runner.config.Context.Step != wanted {
		return ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if step == "train-wait" {
		return runner.trainWait(ctx)
	}
	ctx, cancel, err := runner.roleContext(ctx, step)
	if err != nil {
		return err
	}
	defer cancel()
	switch step {
	case "prepare":
		return runner.prepare(ctx)
	case "train-wait":
		return runner.trainWait(ctx)
	case "collect":
		return runner.collect(ctx)
	case "publish":
		return runner.publish(ctx)
	case "export", "verify":
		return runner.finetuningCompute(ctx, step)
	case "register":
		return runner.register(ctx)
	case "close":
		if runner.config.CloseOnly {
			// RequestExecutionClose verifies the current workload and Run even
			// when a failed CreatePVC prevented Begin from binding an authority.
			// The owner preserves an existing close fence and publication.
			return runner.requestClose(ctx, modeldevv1.CloseReason_CLOSE_REASON_STEP_FAILED)
		}
		return runner.close(ctx)
	default:
		return ErrConfiguration
	}
}

func (runner *Runner) callContext(ctx context.Context) (context.Context, error) {
	data, err := readToken(runner.config.TokenFile)
	if err != nil {
		return nil, ErrConfiguration
	}
	token := strings.TrimSpace(string(data))
	if !bearerPattern.MatchString(token) {
		return nil, ErrConfiguration
	}
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token, "x-ani-tenant-id", runner.config.TenantID)), nil
}

func (runner *Runner) trainWait(ctx context.Context) error {
	parent := ctx
	execution, _, err := runner.configuration(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(parent, execution.Snapshot.DeadlineAt)
	defer func() { cancel() }()
	var admittedAt, trainingDeadline time.Time
	bindBudget := func(training *modeldevv1.TrainingStatus) error {
		if training == nil {
			return ErrConfiguration
		}
		if training.TrainingAdmittedAt == nil || training.TrainingDeadlineAt == nil || training.TrainingAdmittedAt.CheckValid() != nil || training.TrainingDeadlineAt.CheckValid() != nil {
			return ErrConfiguration
		}
		start, deadline := training.TrainingAdmittedAt.AsTime(), training.TrainingDeadlineAt.AsTime()
		expected, err := execution.Snapshot.Timing.DeadlineFor(start)
		if err != nil || !deadline.After(start) || !deadline.Equal(expected) {
			return ErrConfiguration
		}
		if !trainingDeadline.IsZero() {
			if !trainingDeadline.Equal(deadline) || !admittedAt.Equal(start) {
				return ErrConfiguration
			}
			return nil
		}
		admittedAt, trainingDeadline = start, deadline
		cancel()
		ctx, cancel = context.WithDeadline(parent, deadline)
		return ctx.Err()
	}
	if runner.config.Context.Identity.OperationId == "" {
		if _, _, err := runner.configuration(ctx); err != nil {
			return err
		}
	}
	call, err := runner.callContext(ctx)
	if err != nil {
		return err
	}
	ensured, err := runner.client.EnsureTraining(call, &modeldevv1.EnsureTrainingRequest{Context: runner.config.Context})
	ensureConfirmed := err == nil
	if err != nil && !retryObservation(err) {
		return err
	}
	if err == nil {
		if err := bindBudget(ensured.GetStatus()); err != nil {
			return err
		}
		done, err := runner.trainingResult(ensured.GetStatus())
		if done || err != nil {
			return err
		}
	}
	for {
		if !ensureConfirmed {
			// An unknown Runtime response may precede the original TrainJob
			// reservation. Retry the authenticated Ensure command: its durable
			// Runtime/TrainJob fences permit lookup only after an uncertain POST.
			// Read the already admitted TRAIN window so transport recovery never
			// falls back to a fresh local S budget.
			_, _, readErr := runner.configuration(ctx)
			if readErr != nil && !retryObservation(readErr) {
				return readErr
			}
			if readErr == nil && runner.stepTiming != nil {
				t := runner.stepTiming
				if t.Role != modeldevv1.PipelineStep_PIPELINE_STEP_TRAIN_WAIT {
					return ErrConfiguration
				}
				if err := bindBudget(&modeldevv1.TrainingStatus{TrainingAdmittedAt: t.AdmittedAt, TrainingDeadlineAt: t.DeadlineAt}); err != nil {
					return err
				}
			}
		}
		if err := runner.wait(ctx); err != nil {
			return err
		}
		call, err := runner.callContext(ctx)
		if err != nil {
			return err
		}
		if !ensureConfirmed {
			recovered, err := runner.client.EnsureTraining(call, &modeldevv1.EnsureTrainingRequest{Context: runner.config.Context})
			if err != nil {
				if retryObservation(err) {
					continue
				}
				return err
			}
			if err := bindBudget(recovered.GetStatus()); err != nil {
				return err
			}
			ensureConfirmed = true
			if done, err := runner.trainingResult(recovered.GetStatus()); done || err != nil {
				return err
			}
			continue
		}
		observed, err := runner.client.GetTrainingStatus(call, &modeldevv1.GetTrainingStatusRequest{Context: runner.config.Context})
		if err != nil {
			if retryObservation(err) {
				continue
			}
			return err
		}
		if err := bindBudget(observed.GetStatus()); err != nil {
			return err
		}
		done, err := runner.trainingResult(observed.GetStatus())
		if done || err != nil {
			return err
		}
	}
}

func (runner *Runner) trainingResult(training *modeldevv1.TrainingStatus) (bool, error) {
	if training == nil || !proto.Equal(training.Identity, runner.config.Context.Identity) || training.States == nil {
		return false, ErrConfiguration
	}
	switch training.States.ComputeState {
	case modeldevv1.ComputeState_COMPUTE_STATE_SUCCEEDED:
		return true, nil
	case modeldevv1.ComputeState_COMPUTE_STATE_FAILED:
		return true, ErrTrainingFailed
	case modeldevv1.ComputeState_COMPUTE_STATE_CANCELED:
		return true, ErrTrainingCanceled
	case modeldevv1.ComputeState_COMPUTE_STATE_UNSPECIFIED:
		return false, ErrConfiguration
	default:
		return false, nil
	}
}

func (runner *Runner) wait(ctx context.Context) error {
	timer := time.NewTimer(runner.config.PollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryObservation(err error) bool {
	code := status.Code(err)
	return code == codes.Unavailable || code == codes.FailedPrecondition
}

func (runner *Runner) reportID(kind string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(runner.config.TenantID+"\x00"+runner.config.Context.Identity.ExecutionId+"\x00"+runner.config.Context.Identity.ExecutionSpecHash+"\x00"+kind)).String()
}

package service

import (
	"context"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (step *Step) BeginManagedStep(ctx context.Context, request *modeldevv1.BeginManagedStepRequest) (*modeldevv1.BeginManagedStepResponse, error) {
	role, ok := map[modeldevv1.PipelineStep]string{modeldevv1.PipelineStep_PIPELINE_STEP_COLLECT: "collect", modeldevv1.PipelineStep_PIPELINE_STEP_PUBLISH: "publish", modeldevv1.PipelineStep_PIPELINE_STEP_REGISTER: "register"}[request.GetContext().GetStep()]
	if !ok {
		return nil, runtimeError(biz.ErrManagedStepUnauthorized)
	}
	token, claim, err := step.runtimeRequest(ctx, request, request.GetContext(), request.GetContext().GetStep())
	if err != nil {
		return nil, err
	}
	timing, replayed, err := step.runtime.BeginManagedStep(ctx, token, claim, role)
	if err != nil {
		return nil, runtimeError(err)
	}
	return &modeldevv1.BeginManagedStepResponse{Timing: encodeStepTiming(timing), Replayed: replayed}, nil
}

func encodeStepTiming(timing biz.ManagedStepTiming) *modeldevv1.StepTiming {
	role := map[string]modeldevv1.PipelineStep{"prepare": modeldevv1.PipelineStep_PIPELINE_STEP_PREPARE, "collect": modeldevv1.PipelineStep_PIPELINE_STEP_COLLECT, "publish": modeldevv1.PipelineStep_PIPELINE_STEP_PUBLISH, "register": modeldevv1.PipelineStep_PIPELINE_STEP_REGISTER, "train-wait": modeldevv1.PipelineStep_PIPELINE_STEP_TRAIN_WAIT, "export": modeldevv1.PipelineStep_PIPELINE_STEP_EXPORT, "verify": modeldevv1.PipelineStep_PIPELINE_STEP_VERIFY}[timing.Binding.Role]
	return &modeldevv1.StepTiming{Role: role, AdmittedAt: timestamppb.New(timing.AdmittedAt), DeadlineAt: timestamppb.New(timing.DeadlineAt)}
}

func existingStepTiming(result biz.ManagedRuntimeResult, role string) *modeldevv1.StepTiming {
	if role == "close" {
		role = "publish"
		if result.Execution.Snapshot.Kind == "FINETUNING" {
			role = "register"
		}
	}
	if t := biz.TimingForManagedRole(result.Runtime, role); t != nil {
		return encodeStepTiming(*t)
	}
	if role == "train-wait" && result.Runtime.TrainingWindow != nil {
		t := result.Runtime.TrainingWindow
		return encodeStepTiming(biz.ManagedStepTiming{Binding: biz.FinetuningStepBinding{Role: role}, AdmittedAt: t.AdmittedAt, DeadlineAt: t.DeadlineAt})
	}
	for _, start := range result.Runtime.FinetuningSteps {
		if start.Binding.Role == role {
			return encodeStepTiming(biz.ManagedStepTiming{Binding: start.Binding, AdmittedAt: start.AdmittedAt, DeadlineAt: start.DeadlineAt})
		}
	}
	return nil
}

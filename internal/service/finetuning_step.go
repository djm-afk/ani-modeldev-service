package service

import (
	"context"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func finetuningRole(step modeldevv1.PipelineStep) (string, error) {
	switch step {
	case modeldevv1.PipelineStep_PIPELINE_STEP_EXPORT:
		return "export", nil
	case modeldevv1.PipelineStep_PIPELINE_STEP_VERIFY:
		return "verify", nil
	default:
		return "", biz.ErrManagedStepUnauthorized
	}
}

func (s *Step) BeginFinetuningStep(ctx context.Context, r *modeldevv1.BeginFinetuningStepRequest) (*modeldevv1.BeginFinetuningStepResponse, error) {
	role, err := finetuningRole(r.GetContext().GetStep())
	if err != nil {
		return nil, runtimeError(err)
	}
	token, claim, err := s.runtimeRequest(ctx, r, r.GetContext(), r.GetContext().GetStep())
	if err != nil {
		return nil, err
	}
	result, err := s.runtime.BeginFinetuningStep(ctx, token, claim, role)
	if err != nil {
		return nil, runtimeError(err)
	}
	admitted, ok := runtimeTimestamp(result.Start.AdmittedAt)
	deadline, valid := runtimeTimestamp(result.Start.DeadlineAt)
	if !ok || !valid || !result.Start.DeadlineAt.After(result.Start.AdmittedAt) {
		return nil, runtimeError(biz.ErrPersistence)
	}
	return &modeldevv1.BeginFinetuningStepResponse{StartPermitted: result.SendPermit, AdmittedAt: admitted, DeadlineAt: deadline}, nil
}

func (s *Step) ReportFinetuningStepResult(ctx context.Context, r *modeldevv1.ReportFinetuningStepResultRequest) (*modeldevv1.ReportFinetuningStepResultResponse, error) {
	role, err := finetuningRole(r.GetContext().GetStep())
	if err != nil {
		return nil, runtimeError(err)
	}
	token, claim, err := s.runtimeRequest(ctx, r, r.GetContext(), r.GetContext().GetStep())
	if err != nil {
		return nil, err
	}
	if r.StartedAt == nil || r.FinishedAt == nil || r.StartedAt.CheckValid() != nil || r.FinishedAt.CheckValid() != nil {
		return nil, runtimeError(biz.ErrInvalidAdmission)
	}
	report := biz.FinetuningStepResult{StartedAt: r.StartedAt.AsTime(), FinishedAt: r.FinishedAt.AsTime(), ExitCode: r.ExitCode}
	if r.ResultManifest != nil {
		report.ManifestRelativePath = r.ResultManifest.RelativePath
		report.ManifestBytes = r.ResultManifest.SizeBytes
		report.ManifestSHA256 = r.ResultManifest.Sha256
	}
	result, err := s.runtime.ReportFinetuningStepResult(ctx, token, claim, role, report)
	if err != nil {
		return nil, runtimeError(err)
	}
	states, _, err := runtimeReply(result, claim)
	if err != nil {
		return nil, err
	}
	return &modeldevv1.ReportFinetuningStepResultResponse{States: states}, nil
}

func (s *Step) RegisterModel(ctx context.Context, r *modeldevv1.RegisterModelRequest) (*modeldevv1.RegisterModelResponse, error) {
	token, claim, err := s.runtimeRequest(ctx, r, r.GetContext(), modeldevv1.PipelineStep_PIPELINE_STEP_REGISTER)
	if err != nil {
		return nil, err
	}
	result, err := s.runtime.RegisterModel(ctx, token, claim)
	if err != nil {
		return nil, runtimeError(err)
	}
	states, _, err := runtimeReply(result, claim)
	if err != nil {
		return nil, err
	}
	publication, err := encodeRuntimePublication(result)
	if err != nil {
		return nil, err
	}
	return &modeldevv1.RegisterModelResponse{States: states, Publication: publication}, nil
}

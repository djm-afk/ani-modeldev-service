package service

import (
	"context"
	"errors"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
)

func (query *Query) WithFinetuningReadiness(source biz.FinetuningSourceFactsReader, limits cpup01.ContentLimits) *Query {
	query.sourceFacts = source
	query.contentLimits = limits
	return query
}

func (query *Query) CheckFinetuningReadiness(ctx context.Context, request *modeldevv1.CheckFinetuningReadinessRequest) (*modeldevv1.CheckFinetuningReadinessResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevQueryService_CheckFinetuningReadiness_FullMethodName)
	if err != nil {
		return nil, err
	}
	bad := func() (*modeldevv1.CheckFinetuningReadinessResponse, error) {
		return nil, commandError(codes.InvalidArgument, modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT, "invalid finetuning readiness request", scope.RequestID)
	}
	if request == nil || !validMaterialMessage(request) || !queryID(request.BaseModelVersionId) || !queryID(request.ComputePresetId) || request.FinetuningTemplate != nil && cpup01.ValidateTemplateID(*request.FinetuningTemplate) != nil {
		return bad()
	}
	origin, err := contractpb.DecodeComputeOrigin(request.ComputeOrigin)
	if err != nil || origin.ComputePresetID != request.ComputePresetId {
		return bad()
	}
	if query == nil || query.sourceFacts == nil || query.contentLimits.Validate() != nil {
		return nil, finetuningDependencyError(scope.RequestID)
	}
	material, _, err := query.materials.Implementation(ctx, scope.TenantID, "builtin.sft_lora", origin.DeviceMode)
	if err != nil || material.Finetuning == nil {
		if err != nil && ctx.Err() != nil {
			return nil, queryError(ctx.Err(), scope.RequestID)
		}
		return nil, finetuningDependencyError(scope.RequestID)
	}
	capabilities, err := contractpb.EncodeRuntimeCapabilities(material.Finetuning.RuntimeCapabilities)
	if err != nil {
		return nil, finetuningDependencyError(scope.RequestID)
	}
	digest, err := material.Finetuning.RuntimeCapabilities.Digest()
	if err != nil {
		return nil, finetuningDependencyError(scope.RequestID)
	}
	response := &modeldevv1.CheckFinetuningReadinessResponse{
		RuntimeCapabilities: capabilities, RuntimeCapabilitiesSha256: digest, TemplateCandidates: capabilities.RegisteredTemplates,
		Limits: &modeldevv1.ContentLimits{MaxContentBytes: query.contentLimits.MaxContentBytes}, StructureLimits: currentStructureLimits(), RuntimeValidationRequired: true,
	}
	assessment := func(failure error) (*modeldevv1.CheckFinetuningReadinessResponse, error) {
		if err := ctx.Err(); err != nil {
			return nil, queryError(err, scope.RequestID)
		}
		reason, ok := finetuningBusinessReason(failure)
		if !ok {
			return nil, admissionError(failure, scope.RequestID)
		}
		response.Reason = reason
		response.Assessment = modeldevv1.FinetuningAssessment_FINETUNING_ASSESSMENT_UNSUPPORTED
		if errors.Is(failure, biz.ErrFinetuningTemplateRequired) {
			response.Assessment = modeldevv1.FinetuningAssessment_FINETUNING_ASSESSMENT_TEMPLATE_REQUIRED
		}
		return response, nil
	}
	// Only the selected source is read: no dataset, target, execution, admission
	// candidate or GPU allocation is required for this query.
	base, err := query.sourceFacts.ResolveFinetuningSourceFacts(ctx, scope.TenantID, request.BaseModelVersionId, material)
	if err != nil {
		return assessment(err)
	}
	if base.VersionID != request.BaseModelVersionId || !queryID(base.ModelID) {
		return nil, finetuningDependencyError(scope.RequestID)
	}
	response.ModelId, response.ModelVersionId = base.ModelID, base.VersionID
	response.ModelType, response.ContextLimit = base.Facts.ModelType, base.Facts.ContextLimit
	// Prepare uses this same selector. Only its explicit selection input is
	// present here, without manufacturing a complete training intent.
	plan, err := biz.ResolveFinetuningTemplate(cpup01.Intent{FinetuningTemplate: request.FinetuningTemplate}, base, material)
	if err != nil {
		return assessment(err)
	}
	response.TemplatePlan, err = contractpb.EncodeTemplatePlan(plan)
	if err != nil {
		return nil, finetuningDependencyError(scope.RequestID)
	}
	if err := ctx.Err(); err != nil {
		return nil, queryError(err, scope.RequestID)
	}
	response.Assessment = modeldevv1.FinetuningAssessment_FINETUNING_ASSESSMENT_ELIGIBLE
	return response, nil
}

func currentStructureLimits() *modeldevv1.StructureLimits {
	return &modeldevv1.StructureLimits{
		MaxModelFiles: cpup01.MaxModelFiles, MaxOutputFiles: cpup01.MaxOutputFiles,
		MaxArchiveEntries: cpup01.MaxArchiveEntries, MaxRelativePathBytes: cpup01.MaxRelativePathBytes,
		MaxStorageKeyBytes: cpup01.MaxStorageKeyBytes, MaxManifestBytes: cpup01.MaxManifestBytes,
		MaxModelConfigBytes: cpup01.MaxModelConfigBytes, MaxMaterialBytes: cpup01.MaxMaterialBytes,
		MaxArchiveMetadataBytes: cpup01.MaxArchiveMetadataBytes, MaxRecordBytes: cpup01.MaxRecordBytes,
		MaxJsonDepth: cpup01.MaxJSONDepth, MaxControlRpcBytes: cpup01.MaxControlRPCBytes,
		MaxPublicationCandidateBytes: cpup01.MaxPublicationCandidateBytes, MaxPublicationRefBytes: cpup01.MaxPublicationRefBytes,
	}
}

func finetuningDependencyError(requestID string) error {
	return commandError(codes.Unavailable, modeldevv1.ErrorReason_ERROR_REASON_FINETUNING_RUNTIME_UNAVAILABLE, "finetuning runtime unavailable", requestID)
}

// Model source failures retain their finite owner meaning on both the query
// and Create paths. Private upstream messages never become public details.
func finetuningModelError(err error, requestID string) (error, bool) {
	var violation *cpup01.LimitViolation
	if errors.As(err, &violation) && violation != nil {
		if _, ok := finetuningBusinessReason(err); ok {
			return materialsError(err, requestID), true
		}
	}
	if reason, ok := finetuningBusinessReason(err); ok {
		return commandError(codes.FailedPrecondition, reason, reason.String(), requestID), true
	}
	switch {
	case errors.Is(err, biz.ErrModelSourceNotFound):
		return commandError(codes.NotFound, modeldevv1.ErrorReason_ERROR_REASON_RESOURCE_NOT_FOUND, "source model unavailable", requestID), true
	case errors.Is(err, biz.ErrModelSourceForbidden):
		return commandError(codes.PermissionDenied, modeldevv1.ErrorReason_ERROR_REASON_FORBIDDEN, "source access denied", requestID), true
	case errors.Is(err, biz.ErrModelTargetConflict):
		return commandError(codes.AlreadyExists, modeldevv1.ErrorReason_ERROR_REASON_COMMAND_CONFLICT, "model registration target conflict", requestID), true
	case errors.Is(err, biz.ErrModelDependencyUnavailable), errors.Is(err, biz.ErrFinetuningNotEnabled):
		return finetuningDependencyError(requestID), true
	}
	return nil, false
}

func finetuningBusinessReason(err error) (modeldevv1.ErrorReason, bool) {
	var violation *cpup01.LimitViolation
	if errors.As(err, &violation) && violation != nil {
		switch violation.Reason {
		case "CONTENT_LIMIT_EXCEEDED":
			return modeldevv1.ErrorReason_ERROR_REASON_CONTENT_LIMIT_EXCEEDED, true
		case "STRUCTURE_LIMIT_EXCEEDED":
			return modeldevv1.ErrorReason_ERROR_REASON_STRUCTURE_LIMIT_EXCEEDED, true
		}
	}
	for _, item := range []struct {
		err    error
		reason modeldevv1.ErrorReason
	}{
		{biz.ErrBaseModelInvalid, modeldevv1.ErrorReason_ERROR_REASON_BASE_MODEL_INVALID},
		{biz.ErrBaseModelUnsupported, modeldevv1.ErrorReason_ERROR_REASON_BASE_MODEL_UNSUPPORTED},
		{biz.ErrContextLimitExceeded, modeldevv1.ErrorReason_ERROR_REASON_CONTEXT_LIMIT_EXCEEDED},
		{biz.ErrContextLimitUnknown, modeldevv1.ErrorReason_ERROR_REASON_CONTEXT_LIMIT_UNKNOWN},
		{biz.ErrFinetuningTemplateRequired, modeldevv1.ErrorReason_ERROR_REASON_FINETUNING_TEMPLATE_REQUIRED},
		{biz.ErrFinetuningTemplateUnsupported, modeldevv1.ErrorReason_ERROR_REASON_FINETUNING_TEMPLATE_UNSUPPORTED},
	} {
		if errors.Is(err, item.err) {
			return item.reason, true
		}
	}
	return modeldevv1.ErrorReason_ERROR_REASON_UNSPECIFIED, false
}

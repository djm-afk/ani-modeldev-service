package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Management struct {
	modeldevv1.UnimplementedModelDevManagementServiceServer
	materials *biz.ManagedMaterials
}

func NewManagement(materials *biz.ManagedMaterials) *Management {
	return &Management{materials: materials}
}
func materialsError(err error, requestID string) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	case errors.Is(err, biz.ErrInvalidAdmission), errors.Is(err, biz.ErrInvalidInput), errors.Is(err, cpup01.ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, "invalid managed material request")
	case errors.Is(err, biz.ErrInputNotFound):
		return status.Error(codes.NotFound, "input version unavailable")
	case errors.Is(err, biz.ErrInputConflict), errors.Is(err, biz.ErrAdmissionConflict):
		return status.Error(codes.AlreadyExists, "immutable material conflict")
	case errors.Is(err, biz.ErrImplementationUnavailable):
		return status.Error(codes.FailedPrecondition, "implementation unavailable")
	default:
		return queryError(err, requestID)
	}
}
func validMaterialMessage(message proto.Message) bool {
	return message != nil && validRuntimeMessage(message.ProtoReflect(), 0)
}
func (m *Management) ImportCSV(ctx context.Context, r *modeldevv1.ImportCSVRequest) (*modeldevv1.ImportCSVResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevManagementService_ImportCSV_FullMethodName)
	if err != nil {
		return nil, err
	}
	if r == nil || !validMaterialMessage(r) || r.Object == nil || r.RequestedAt == nil || r.RequestedAt.CheckValid() != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid CSV import")
	}
	version, ok := r.Object.Immutability.(*trainingv1.FixedObjectRef_VersionId)
	if !ok || version == nil {
		return nil, status.Error(codes.InvalidArgument, "fixed object version required")
	}
	object := cpup01.FixedObjectRef{StorageConnectionID: r.Object.StorageConnectionId, Bucket: r.Object.Bucket, Key: r.Object.Key, VersionID: &version.VersionId, SizeBytes: r.Object.SizeBytes, SHA256: r.Object.Sha256}
	value, err := m.materials.ImportCSV(ctx, biz.InputImport{TenantID: scope.TenantID, RequestID: scope.RequestID, Actor: scope.Actor, InputVersionID: r.InputVersionId, RequestedAt: r.RequestedAt.AsTime(), Object: object}, r.ImplementationId)
	if err != nil {
		return nil, materialsError(err, scope.RequestID)
	}
	return &modeldevv1.ImportCSVResponse{InputVersion: inputVersionView(value)}, nil
}
func inputVersionView(v biz.InputVersion) *modeldevv1.InputVersionView {
	out := &modeldevv1.InputVersionView{InputVersionId: v.Import.InputVersionID, Format: v.Import.InputFormat(), SizeBytes: v.Import.Object.SizeBytes, Sha256: v.Import.Object.SHA256, CreatedAt: timestamppb.New(v.Import.RequestedAt)}
	switch v.State {
	case biz.InputStateReady:
		out.State = modeldevv1.InputState_INPUT_STATE_READY
	case biz.InputStateRejected:
		out.State = modeldevv1.InputState_INPUT_STATE_FAILED
	default:
		out.State = modeldevv1.InputState_INPUT_STATE_VALIDATING
	}
	if v.Verification != nil {
		out.RowCount = v.Verification.RowCount
		out.FeatureCount = v.Verification.FeatureCount
	}
	if v.AlpacaVerification != nil {
		out.RowCount = v.AlpacaVerification.RowCount
	}
	if v.Failure != nil {
		out.Failure = &modeldevv1.ErrorDetail{Reason: modeldevv1.ErrorReason_ERROR_REASON_INPUT_NOT_READY, SafeMessage: string(v.Failure.Code)}
	}
	return out
}

// modelDevMaxOffset saturates the derived window offset so an out-of-range high
// page cannot overflow into an earlier window; such a page simply yields no rows.
const modelDevMaxOffset = 1<<31 - 1

// resolveModelDevPage resolves the independent page/pageSize defaults and
// validates them against Governance's frontend page contract. An absent field
// keeps its own default (1 or 20); an explicit zero or a size above 100 is
// rejected rather than silently corrected.
func resolveModelDevPage(p *modeldevv1.PageRequest) (uint32, uint32, error) {
	page, size := uint32(1), uint32(20)
	if p != nil {
		if !validMaterialMessage(p) {
			return 0, 0, status.Error(codes.InvalidArgument, "invalid page window")
		}
		if p.Page != nil {
			page = *p.Page
		}
		if p.PageSize != nil {
			size = *p.PageSize
		}
	}
	if page == 0 || size == 0 || size > 100 {
		return 0, 0, status.Error(codes.InvalidArgument, "invalid page window")
	}
	return page, size, nil
}

// modelDevPageWindow derives the zero-based offset and size for a validated
// page/pageSize pair.
func modelDevPageWindow(page, pageSize uint32) (int, int) {
	offset := (uint64(page) - 1) * uint64(pageSize)
	if offset > modelDevMaxOffset {
		return modelDevMaxOffset, int(pageSize)
	}
	return int(offset), int(pageSize)
}
func (q *Query) GetInputVersion(ctx context.Context, r *modeldevv1.GetInputVersionRequest) (*modeldevv1.GetInputVersionResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevQueryService_GetInputVersion_FullMethodName)
	if err != nil {
		return nil, err
	}
	if r == nil || !validMaterialMessage(r) || !queryID(r.InputVersionId) {
		return nil, status.Error(codes.InvalidArgument, "invalid input version query")
	}
	value, err := q.materials.Input(ctx, scope.TenantID, r.InputVersionId)
	if err != nil {
		return nil, materialsError(err, scope.RequestID)
	}
	return &modeldevv1.GetInputVersionResponse{InputVersion: inputVersionView(value)}, nil
}
func (q *Query) ListInputVersions(ctx context.Context, r *modeldevv1.ListInputVersionsRequest) (*modeldevv1.ListInputVersionsResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevQueryService_ListInputVersions_FullMethodName)
	if err != nil {
		return nil, err
	}
	if r == nil || !validMaterialMessage(r) {
		return nil, status.Error(codes.InvalidArgument, "invalid input query")
	}
	page, pageSize, err := resolveModelDevPage(r.Page)
	if err != nil {
		return nil, err
	}
	var state biz.InputState
	if r.State != nil {
		switch *r.State {
		case modeldevv1.InputState_INPUT_STATE_READY:
			state = biz.InputStateReady
		case modeldevv1.InputState_INPUT_STATE_VALIDATING:
			state = biz.InputStateValidating
		case modeldevv1.InputState_INPUT_STATE_FAILED:
			state = biz.InputStateRejected
		default:
			return nil, status.Error(codes.InvalidArgument, "invalid input state")
		}
	}
	offset, limit := modelDevPageWindow(page, pageSize)
	values, err := q.materials.Inputs(ctx, scope.TenantID, state, offset, limit)
	if err != nil {
		return nil, materialsError(err, scope.RequestID)
	}
	total, err := q.materials.InputsCount(ctx, scope.TenantID, state)
	if err != nil {
		return nil, materialsError(err, scope.RequestID)
	}
	response := &modeldevv1.ListInputVersionsResponse{Total: total}
	for _, value := range values {
		response.InputVersions = append(response.InputVersions, inputVersionView(value))
	}
	return response, nil
}
func (q *Query) ListImplementations(ctx context.Context, r *modeldevv1.ListImplementationsRequest) (*modeldevv1.ListImplementationsResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevQueryService_ListImplementations_FullMethodName)
	if err != nil {
		return nil, err
	}
	if r == nil || !validMaterialMessage(r) {
		return nil, status.Error(codes.InvalidArgument, "invalid implementation query")
	}
	page, size, err := resolveModelDevPage(r.Page)
	if err != nil {
		return nil, err
	}
	if q == nil || q.materials == nil {
		return nil, queryError(biz.ErrPersistence, scope.RequestID)
	}
	ids := []string{"builtin.mlp", "builtin.sft_lora"}
	response := &modeldevv1.ListImplementationsResponse{Total: uint64(len(ids))}
	// Loaded facts enforce one global T/S/M configuration. An implementation
	// with no installed tenant facts remains unavailable; it must not hide the
	// other implementation or invent another timing configuration.
	var timing cpup01.TimingConfiguration
	foundTiming := false
	for _, id := range ids {
		value, timingErr := q.materials.TimingFor(ctx, scope.TenantID, id)
		if timingErr == nil {
			timing = value
			foundTiming = true
			break
		}
		if !errors.Is(timingErr, biz.ErrAdmissionEnvironmentNotReady) {
			return nil, materialsError(timingErr, scope.RequestID)
		}
	}
	if !foundTiming {
		return nil, materialsError(biz.ErrAdmissionEnvironmentNotReady, scope.RequestID)
	}
	offset, limit := modelDevPageWindow(page, size)
	if offset >= len(ids) {
		return response, nil
	}
	ids = ids[offset:min(offset+limit, len(ids))]
	for _, id := range ids {
		definition := cpup01.Implementations()[id]
		if _, err := timing.Resolve(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), nil); err != nil {
			return nil, queryError(err, scope.RequestID)
		}
		rules, err := contractpb.EncodeImplementationParameterRules(id)
		if err != nil {
			return nil, queryError(err, scope.RequestID)
		}
		view := &modeldevv1.ImplementationView{ImplementationId: id, Version: definition.Version, ParameterContractVersion: definition.ParameterContractVersion, ParameterRules: rules, ExecutionTimeoutDefaultSeconds: timing.ExecutionTimeoutDefaultSeconds, ExecutionTimeoutMaxSeconds: timing.ExecutionTimeoutMaxSeconds, StepTimeoutSeconds: timing.StepTimeoutSeconds}
		if definition.Kind == "GENERAL_TRAINING" {
			view.Name = "MLP training"
			view.Kind = trainingv1.ExecutionKind_EXECUTION_KIND_GENERAL_TRAINING
		} else {
			view.Name = "LoRA supervised finetuning"
			view.Kind = trainingv1.ExecutionKind_EXECUTION_KIND_FINETUNING
		}
		for _, mode := range []string{"cpu", "gpu"} {
			if id == "builtin.sft_lora" && q.models == nil {
				continue
			}
			material, _, err := q.materials.Implementation(ctx, scope.TenantID, id, mode)
			if err == nil {
				view.SupportedDeviceModes = append(view.SupportedDeviceModes, mode)
				if material.Finetuning != nil {
					for _, rule := range view.ParameterRules {
						if rule.Name == "cutoff_len" {
							rule.ValidationDescription += "; installed " + mode + " implementation upper bound=" + strconv.FormatInt(material.Finetuning.MaxSequenceLength, 10)
						}
					}
				}
			} else if !errors.Is(err, biz.ErrImplementationUnavailable) && !errors.Is(err, biz.ErrAdmissionEnvironmentNotReady) {
				return nil, materialsError(err, scope.RequestID)
			}
		}
		view.Available = len(view.SupportedDeviceModes) > 0
		response.Implementations = append(response.Implementations, view)
	}
	return response, nil
}

package service

import (
	"context"
	"errors"
	"sort"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
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
	case errors.Is(err, biz.ErrNoCompatibleRelease):
		return status.Error(codes.FailedPrecondition, "Release unavailable or incompatible")
	default:
		return queryError(err, requestID)
	}
}
func validMaterialMessage(message proto.Message) bool {
	return message != nil && validRuntimeMessage(message.ProtoReflect(), 0)
}
func (m *Management) ValidateRelease(ctx context.Context, r *modeldevv1.ValidateReleaseRequest) (*modeldevv1.ValidateReleaseResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevManagementService_ValidateRelease_FullMethodName)
	if err != nil {
		return nil, err
	}
	if r == nil || !validMaterialMessage(r) {
		return nil, status.Error(codes.InvalidArgument, "invalid Release selection")
	}
	release, err := m.materials.Release(ctx, scope.TenantID, r.PresetId, r.ReleaseId, r.ReleaseDigest)
	if err != nil {
		return nil, materialsError(err, scope.RequestID)
	}
	return &modeldevv1.ValidateReleaseResponse{PresetId: release.PresetID, ReleaseId: release.ReleaseID, ReleaseDigest: r.ReleaseDigest}, nil
}
func (m *Management) ImportRelease(ctx context.Context, r *modeldevv1.ImportReleaseRequest) (*modeldevv1.ImportReleaseResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevManagementService_ImportRelease_FullMethodName)
	if err != nil {
		return nil, err
	}
	if r == nil || !validMaterialMessage(r) {
		return nil, status.Error(codes.InvalidArgument, "invalid Release import")
	}
	release, created, err := m.materials.ImportRelease(ctx, scope.TenantID, r.PresetId, r.ReleaseId, r.ReleaseDigest, r.CanonicalRelease)
	if err != nil {
		return nil, materialsError(err, scope.RequestID)
	}
	return &modeldevv1.ImportReleaseResponse{PresetId: release.PresetID, ReleaseId: release.ReleaseID, ReleaseDigest: r.ReleaseDigest, Created: created}, nil
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
	value, err := m.materials.ImportCSV(ctx, biz.InputImport{TenantID: scope.TenantID, RequestID: scope.RequestID, Actor: scope.Actor, InputVersionID: r.InputVersionId, RequestedAt: r.RequestedAt.AsTime(), Object: object}, r.ReleaseId, r.ReleaseDigest)
	if err != nil {
		return nil, materialsError(err, scope.RequestID)
	}
	return &modeldevv1.ImportCSVResponse{InputVersion: inputVersionView(value)}, nil
}
func inputVersionView(v biz.InputVersion) *modeldevv1.InputVersionView {
	out := &modeldevv1.InputVersionView{InputVersionId: v.Import.InputVersionID, Format: "CSV", SizeBytes: v.Import.Object.SizeBytes, Sha256: v.Import.Object.SHA256, CreatedAt: timestamppb.New(v.Import.RequestedAt)}
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
	if v.Failure != nil {
		out.Failure = &modeldevv1.ErrorDetail{Reason: modeldevv1.ErrorReason_ERROR_REASON_INPUT_NOT_READY, SafeMessage: string(v.Failure.Code)}
	}
	return out
}

// presetDisplayName derives a stable, distinguishing label for one preset.
// The immutable Release contract has no display name, so reuse its fixed
// identity: a human base plus a short prefix of the preset UUID.
func presetDisplayName(presetID string) string {
	const base = "General CPU MLP"
	if len(presetID) < 8 {
		return base
	}
	return base + " · " + presetID[:8]
}
func materialPage(p *modeldevv1.PageRequest) (int, string, error) {
	limit, after := 20, ""
	if p != nil {
		if !validMaterialMessage(p) || p.Limit > 100 || len(p.Cursor) > 2048 {
			return 0, "", status.Error(codes.InvalidArgument, "invalid material page")
		}
		if p.Limit > 0 {
			limit = int(p.Limit)
		}
		after = p.Cursor
	}
	return limit, after, nil
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
	limit, cursorToken, err := materialPage(r.Page)
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
	binding := biz.CursorBinding{Kind: biz.CursorKindInputVersions, TenantID: scope.TenantID,
		FilterKey: biz.CursorFilterKey(string(state))}
	after, err := q.listCursor(binding, cursorToken)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid input page")
	}
	values, next, err := q.materials.Inputs(ctx, scope.TenantID, state, after, limit)
	if err != nil {
		return nil, materialsError(err, scope.RequestID)
	}
	total, err := q.materials.InputsCount(ctx, scope.TenantID, state)
	if err != nil {
		return nil, materialsError(err, scope.RequestID)
	}
	response := &modeldevv1.ListInputVersionsResponse{Total: total}
	response.NextCursor, err = q.nextListCursor(binding, next)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid input page")
	}
	for _, value := range values {
		response.InputVersions = append(response.InputVersions, inputVersionView(value))
	}
	return response, nil
}
func (q *Query) ListPresets(ctx context.Context, r *modeldevv1.ListPresetsRequest) (*modeldevv1.ListPresetsResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevQueryService_ListPresets_FullMethodName)
	if err != nil {
		return nil, err
	}
	if r == nil || !validMaterialMessage(r) {
		return nil, status.Error(codes.InvalidArgument, "invalid preset query")
	}
	limit, cursorToken, err := materialPage(r.Page)
	if err != nil {
		return nil, err
	}
	binding := biz.CursorBinding{Kind: biz.CursorKindPresets, TenantID: scope.TenantID,
		FilterKey: biz.CursorFilterKey("")}
	after, err := q.listCursor(binding, cursorToken)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid preset page")
	}
	releases, err := q.materials.Releases(ctx, scope.TenantID)
	if err != nil {
		return nil, materialsError(err, scope.RequestID)
	}
	presets := map[string]*modeldevv1.PresetView{}
	for _, release := range releases {
		p := presets[release.PresetID]
		if p == nil {
			p = &modeldevv1.PresetView{PresetId: release.PresetID, Name: presetDisplayName(release.PresetID), Kind: trainingv1.ExecutionKind_EXECUTION_KIND_GENERAL_TRAINING}
			presets[release.PresetID] = p
			for _, v := range release.Program.DefaultParameters {
				rule := &modeldevv1.ParameterRule{Name: v.Name, DefaultValue: parameterView(v)}
				switch v.Type {
				case "INTEGER":
					rule.Type = modeldevv1.ParameterType_PARAMETER_TYPE_INTEGER
					rule.AllowedValues = []*trainingv1.Parameter{parameterView(v)}
				case "DECIMAL":
					rule.Type = modeldevv1.ParameterType_PARAMETER_TYPE_DECIMAL
					rule.Maximum = parameterView(cpup01.Parameter{Name: v.Name, Type: "DECIMAL", Value: "0.1"})
				}
				p.ParameterRules = append(p.ParameterRules, rule)
			}
		}
		found := false
		for _, id := range p.ImageVersionIds {
			found = found || id == release.Program.ImageVersionID
		}
		if !found {
			p.ImageVersionIds = append(p.ImageVersionIds, release.Program.ImageVersionID)
		}
	}
	ids := make([]string, 0, len(presets))
	for id := range presets {
		if id > after {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	// total is every distinct preset for this tenant, independent of the window;
	// presets are aggregated from the approved catalogue rather than a table.
	response := &modeldevv1.ListPresetsResponse{Total: uint64(len(presets))}
	if len(ids) > limit {
		ids = ids[:limit]
		response.NextCursor, err = q.nextListCursor(binding, ids[len(ids)-1])
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid preset page")
		}
	}
	for _, id := range ids {
		p := presets[id]
		sort.Strings(p.ImageVersionIds)
		response.Presets = append(response.Presets, p)
	}
	return response, nil
}
func parameterView(p cpup01.Parameter) *trainingv1.Parameter {
	value := &trainingv1.Parameter{Name: p.Name}
	// The registered grammar has only integer and decimal parameters.
	if p.Type == "INTEGER" {
		if p.Name == "epochs" {
			value.Value = &trainingv1.Parameter_IntegerValue{IntegerValue: 3}
		} else {
			value.Value = &trainingv1.Parameter_IntegerValue{IntegerValue: 64}
		}
	} else {
		value.Value = &trainingv1.Parameter_DecimalValue{DecimalValue: p.Value}
	}
	return value
}

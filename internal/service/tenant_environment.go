package service

import (
	"context"
	"errors"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type TenantEnvironment struct {
	modeldevv1.UnimplementedModelDevTenantEnvironmentServiceServer
	usecase *biz.TenantEnvironment
}

func NewTenantEnvironment(usecase *biz.TenantEnvironment) *TenantEnvironment {
	return &TenantEnvironment{usecase: usecase}
}
func environmentServiceError(err error, requestID string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	if errors.Is(err, biz.ErrTenantEnvironmentInvalid) {
		return commandError(codes.InvalidArgument, modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT, "invalid environment request", requestID)
	}
	if errors.Is(err, biz.ErrTenantEnvironmentNotRegistered) {
		return commandError(codes.NotFound, modeldevv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_REGISTERED, "environment not registered", requestID)
	}
	return commandError(codes.Unavailable, modeldevv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE, "environment unavailable", requestID)
}
func (s *TenantEnvironment) EnsureTenantExecutionEnvironment(ctx context.Context, request *modeldevv1.EnsureTenantExecutionEnvironmentRequest) (*modeldevv1.EnsureTenantExecutionEnvironmentResponse, error) {
	access, err := queryScope(ctx, modeldevv1.ModelDevTenantEnvironmentService_EnsureTenantExecutionEnvironment_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || len(request.ProtoReflect().GetUnknown()) != 0 || !queryID(request.OperationId) || !queryID(access.TenantID) || access.Actor == "" {
		return nil, environmentServiceError(biz.ErrTenantEnvironmentInvalid, access.RequestID)
	}
	if s == nil || s.usecase == nil {
		return nil, environmentServiceError(biz.ErrTenantEnvironmentPersistence, access.RequestID)
	}
	row, err := s.usecase.Ensure(ctx, access.TenantID, access.Actor, request.OperationId)
	if err != nil {
		return nil, environmentServiceError(err, access.RequestID)
	}
	view, err := environmentView(row)
	if err != nil {
		return nil, environmentServiceError(err, access.RequestID)
	}
	return &modeldevv1.EnsureTenantExecutionEnvironmentResponse{Environment: view}, nil
}
func (s *TenantEnvironment) GetTenantExecutionEnvironment(ctx context.Context, request *modeldevv1.GetTenantExecutionEnvironmentRequest) (*modeldevv1.GetTenantExecutionEnvironmentResponse, error) {
	access, err := queryScope(ctx, modeldevv1.ModelDevTenantEnvironmentService_GetTenantExecutionEnvironment_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || len(request.ProtoReflect().GetUnknown()) != 0 || !queryID(access.TenantID) {
		return nil, environmentServiceError(biz.ErrTenantEnvironmentInvalid, access.RequestID)
	}
	if s == nil || s.usecase == nil {
		return nil, environmentServiceError(biz.ErrTenantEnvironmentPersistence, access.RequestID)
	}
	row, err := s.usecase.Get(ctx, access.TenantID)
	if err != nil {
		return nil, environmentServiceError(err, access.RequestID)
	}
	view, err := environmentView(row)
	if err != nil {
		return nil, environmentServiceError(err, access.RequestID)
	}
	return &modeldevv1.GetTenantExecutionEnvironmentResponse{Environment: view}, nil
}
func environmentView(row biz.TenantExecutionEnvironment) (*modeldevv1.TenantExecutionEnvironmentView, error) {
	states := map[biz.EnvironmentState]modeldevv1.TenantExecutionEnvironmentState{biz.EnvironmentProvisioning: 1, biz.EnvironmentReady: 2, biz.EnvironmentBlocked: 3, biz.EnvironmentNeedsReview: 4}
	phases := map[biz.EnvironmentPhase]modeldevv1.TenantExecutionEnvironmentPhase{biz.EnvironmentNamespace: 1, biz.EnvironmentResources: 2, biz.EnvironmentExperiment: 3, biz.EnvironmentVerify: 4, biz.EnvironmentPublish: 5, biz.EnvironmentComplete: 6}
	if !queryID(row.ID) || !queryID(row.ClusterID) || row.NamespaceName != "tenant-"+row.TenantID || states[row.State] == 0 || phases[row.Phase] == 0 || row.UpdatedAt.IsZero() {
		return nil, biz.ErrTenantEnvironmentPersistence
	}
	if row.ReasonCode != "" && !biz.ValidEnvironmentReason(row.ReasonCode) {
		return nil, biz.ErrTenantEnvironmentPersistence
	}
	v := &modeldevv1.TenantExecutionEnvironmentView{EnvironmentId: row.ID, ClusterId: row.ClusterID, NamespaceName: row.NamespaceName, State: states[row.State], Phase: phases[row.Phase], ReasonCode: row.ReasonCode, UpdatedAt: timestamppb.New(row.UpdatedAt.UTC())}
	if row.NamespaceUID != "" {
		if !queryID(row.NamespaceUID) {
			return nil, biz.ErrTenantEnvironmentPersistence
		}
		v.NamespaceUid = &row.NamespaceUID
	}
	if row.ExperimentID != "" {
		if !queryID(row.ExperimentID) {
			return nil, biz.ErrTenantEnvironmentPersistence
		}
		v.ExperimentId = &row.ExperimentID
	}
	if row.BindingDigest != "" {
		if len(row.BindingDigest) != 64 {
			return nil, biz.ErrTenantEnvironmentPersistence
		}
		v.BindingDigest = &row.BindingDigest
	}
	if !row.LastVerifiedAt.IsZero() {
		v.LastVerifiedAt = timestamppb.New(row.LastVerifiedAt.UTC())
	}
	if row.State == biz.EnvironmentProvisioning {
		if row.Phase == biz.EnvironmentComplete {
			return nil, biz.ErrTenantEnvironmentPersistence
		}
		v.Retryable = true
		v.RetryAfterSeconds = 2
	}
	if row.State == biz.EnvironmentBlocked && row.ReasonCode == "ENVIRONMENT_DEPENDENCY_UNAVAILABLE" {
		v.Retryable = true
		v.RetryAfterSeconds = 2
	}
	if row.State == biz.EnvironmentReady && (row.Phase != biz.EnvironmentComplete || v.NamespaceUid == nil || v.ExperimentId == nil || v.BindingDigest == nil || v.LastVerifiedAt == nil || row.ReasonCode != "") {
		return nil, biz.ErrTenantEnvironmentPersistence
	}
	if row.State == biz.EnvironmentNeedsReview && row.ReasonCode == "" {
		return nil, biz.ErrTenantEnvironmentPersistence
	}
	if v.UpdatedAt.CheckValid() != nil || v.LastVerifiedAt != nil && v.LastVerifiedAt.CheckValid() != nil || row.UpdatedAt.After(time.Now().Add(time.Minute)) {
		return nil, biz.ErrTenantEnvironmentPersistence
	}
	return v, nil
}

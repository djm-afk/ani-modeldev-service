package service

import (
	"context"
	"errors"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Admission struct {
	modeldevv1.UnimplementedModelDevAdmissionServiceServer
	resolver *biz.AdmissionResolver
}

func NewAdmission(resolver *biz.AdmissionResolver) *Admission { return &Admission{resolver: resolver} }

func (a *Admission) PrepareAdmission(ctx context.Context, r *modeldevv1.PrepareAdmissionRequest) (*modeldevv1.PrepareAdmissionResponse, error) {
	delivery, ok := ctx.Value(governanceDeliveryKey{}).(GovernanceDelivery)
	if !ok {
		return nil, commandError(codes.Unauthenticated, modeldevv1.ErrorReason_ERROR_REASON_UNAUTHENTICATED, "authenticated Governance delivery required", "")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	bad := func() (*modeldevv1.PrepareAdmissionResponse, error) {
		return nil, commandError(codes.InvalidArgument, modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT, "invalid admission preparation", delivery.RequestID)
	}
	if r == nil || r.IdentityRef == nil || r.AcceptedAt == nil || len(r.ProtoReflect().GetUnknown()) != 0 || len(r.IdentityRef.ProtoReflect().GetUnknown()) != 0 || len(r.AcceptedAt.ProtoReflect().GetUnknown()) != 0 || r.AcceptedAt.CheckValid() != nil {
		return bad()
	}
	identity := r.IdentityRef
	if identity.TenantId != delivery.TenantID || identity.Actor != delivery.Actor {
		return bad()
	}
	intent, err := contractpb.DecodeIntent(r.Intent)
	if err != nil {
		return bad()
	}
	origin, err := contractpb.DecodeComputeSelection(r.ComputeSelectionCanonical, r.SelectionDigest)
	if err != nil {
		return bad()
	}
	candidate, err := a.resolver.Prepare(ctx, biz.PrepareAdmissionInput{IdentityRef: cpup01.IdentityRef{TenantID: identity.TenantId, Actor: identity.Actor, ExecutionID: identity.ExecutionId, OperationID: identity.OperationId, IntentHash: identity.IntentHash}, Intent: intent, ComputeOrigin: origin, AcceptedAt: r.AcceptedAt.AsTime()})
	if err != nil {
		return nil, admissionError(err, delivery.RequestID)
	}
	canonical, err := candidate.Canonical()
	if err != nil {
		return nil, admissionError(err, delivery.RequestID)
	}
	hash, err := candidate.Digest()
	if err != nil {
		return nil, admissionError(err, delivery.RequestID)
	}
	return &modeldevv1.PrepareAdmissionResponse{CandidateCanonical: canonical, CandidateHash: hash}, nil
}

func (a *Admission) FinalizeAdmission(ctx context.Context, r *modeldevv1.FinalizeAdmissionRequest) (*modeldevv1.FinalizeAdmissionResponse, error) {
	delivery, ok := ctx.Value(governanceDeliveryKey{}).(GovernanceDelivery)
	if !ok {
		return nil, commandError(codes.Unauthenticated, modeldevv1.ErrorReason_ERROR_REASON_UNAUTHENTICATED, "authenticated Governance delivery required", "")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	bad := func() (*modeldevv1.FinalizeAdmissionResponse, error) {
		return nil, commandError(codes.InvalidArgument, modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT, "invalid admission finalization", delivery.RequestID)
	}
	if r == nil || len(r.ProtoReflect().GetUnknown()) != 0 {
		return bad()
	}
	candidate, err := cpup01.DecodePreparedCandidate(r.CandidateCanonical, r.CandidateHash)
	if err != nil {
		return bad()
	}
	if candidate.Snapshot.IdentityRef.TenantID != delivery.TenantID || candidate.Snapshot.IdentityRef.Actor != delivery.Actor {
		return bad()
	}
	var gpu *cpup01.GPUPlan
	if candidate.Snapshot.ComputeOrigin.DeviceMode == "cpu" {
		if r.GpuExecutionCanonical != nil || r.GpuDisplayCanonical != nil {
			return bad()
		}
	} else {
		gpu, err = contractpb.DecodeGPUPlanCanonical(r.GpuExecutionCanonical, r.GpuDisplayCanonical)
		if err != nil {
			return bad()
		}
	}
	resolved, err := a.resolver.Finalize(ctx, candidate, gpu)
	if err != nil {
		return nil, admissionError(err, delivery.RequestID)
	}
	snapshot, err := contractpb.EncodeSnapshot(resolved.Snapshot)
	if err != nil {
		return nil, admissionError(err, delivery.RequestID)
	}
	return &modeldevv1.FinalizeAdmissionResponse{Snapshot: snapshot, ExecutionSpecHash: resolved.SpecHash}, nil
}
func admissionError(err error, requestID string) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	case errors.Is(err, biz.ErrInvalidAdmission), errors.Is(err, biz.ErrInvalidInput), errors.Is(err, cpup01.ErrInvalidArgument):
		return commandError(codes.InvalidArgument, modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT, "invalid admission plan", requestID)
	case errors.Is(err, biz.ErrInputNotFound):
		return commandError(codes.NotFound, modeldevv1.ErrorReason_ERROR_REASON_RESOURCE_NOT_FOUND, "input version unavailable", requestID)
	case errors.Is(err, biz.ErrAdmissionInputNotReady):
		return commandError(codes.FailedPrecondition, modeldevv1.ErrorReason_ERROR_REASON_INPUT_NOT_READY, "input version is not ready", requestID)
	case errors.Is(err, biz.ErrAdmissionEnvironmentNotReady):
		return commandError(codes.Unavailable, modeldevv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_READY, "managed environment is not ready", requestID)
	case errors.Is(err, biz.ErrImplementationUnavailable):
		return commandError(codes.FailedPrecondition, modeldevv1.ErrorReason_ERROR_REASON_IMPLEMENTATION_UNAVAILABLE, "implementation software materials unavailable", requestID)
	case errors.Is(err, biz.ErrUnsupportedDevice):
		return commandError(codes.FailedPrecondition, modeldevv1.ErrorReason_ERROR_REASON_UNSUPPORTED_DEVICE, "implementation does not support this device", requestID)
	case errors.Is(err, biz.ErrAdmissionPlanMismatch):
		return commandError(codes.InvalidArgument, modeldevv1.ErrorReason_ERROR_REASON_ADMISSION_PLAN_MISMATCH, "admission plan mismatch", requestID)
	default:
		return commandError(codes.Unavailable, modeldevv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE, "admission dependency unavailable", requestID)
	}
}

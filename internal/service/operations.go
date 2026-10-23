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

const InspectExecutionOperation = "/ani.modeldev.v1.ModelDevOperationsService/InspectExecution"

type Operations struct {
	modeldevv1.UnimplementedModelDevOperationsServiceServer
	executions           *biz.ExecutionOperations
	registrationRecovery biz.ModelRegistrationRecovery
	registrationQueries  biz.ArtifactQueryRepository
}

func NewOperations(executions *biz.ExecutionOperations) *Operations {
	return &Operations{executions: executions}
}

func (operations *Operations) WithRegistrationRecovery(recovery biz.ModelRegistrationRecovery, queries biz.ArtifactQueryRepository) *Operations {
	operations.registrationRecovery, operations.registrationQueries = recovery, queries
	return operations
}

func (operations *Operations) ResumeModelRegistration(ctx context.Context, request *modeldevv1.ResumeModelRegistrationRequest) (*modeldevv1.GetExecutionResponse, error) {
	access, err := queryScope(ctx, modeldevv1.ModelDevOperationsService_ResumeModelRegistration_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || !queryID(request.ExecutionId) || len(request.ProtoReflect().GetUnknown()) != 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid registration recovery request")
	}
	if operations == nil || operations.registrationRecovery == nil || operations.registrationQueries == nil {
		return nil, status.Error(codes.Unavailable, "registration recovery unavailable")
	}
	resumed, err := operations.registrationRecovery.ResumeModelRegistration(ctx, access.TenantID, request.ExecutionId)
	if err != nil {
		return nil, operationsError(err, access.RequestID)
	}
	if resumed.TenantID != access.TenantID || resumed.ExecutionID != request.ExecutionId {
		return nil, queryError(biz.ErrPersistence, access.RequestID)
	}
	record, err := operations.registrationQueries.GetQueryRecord(ctx, access.TenantID, request.ExecutionId)
	if err != nil {
		return nil, queryError(err, access.RequestID)
	}
	current := record.Execution
	if current.TenantID != access.TenantID || current.ExecutionID != request.ExecutionId || current.OperationID != resumed.OperationID || current.SpecHash != resumed.SpecHash {
		return nil, queryError(biz.ErrPersistence, access.RequestID)
	}
	view, err := executionView(record)
	if err != nil {
		return nil, queryError(err, access.RequestID)
	}
	return &modeldevv1.GetExecutionResponse{Execution: view}, nil
}

func (operations *Operations) Inspect(ctx context.Context, execution string) (biz.ExecutionInspect, error) {
	access, err := queryScope(ctx, InspectExecutionOperation)
	if err != nil {
		return biz.ExecutionInspect{}, err
	}
	if !queryID(execution) {
		return biz.ExecutionInspect{}, status.Error(codes.InvalidArgument, "invalid execution")
	}
	if operations == nil || operations.executions == nil {
		return biz.ExecutionInspect{}, status.Error(codes.Unavailable, "operations unavailable")
	}
	result, err := operations.executions.Inspect(ctx, access.TenantID, execution)
	if err != nil {
		return biz.ExecutionInspect{}, operationsError(err, access.RequestID)
	}
	return result, nil
}

func (operations *Operations) InspectExecution(ctx context.Context, request *modeldevv1.InspectExecutionRequest) (*modeldevv1.InspectExecutionResponse, error) {
	if request == nil || len(request.ProtoReflect().GetUnknown()) != 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid inspection request")
	}
	view, err := operations.Inspect(ctx, request.ExecutionId)
	if err != nil {
		return nil, err
	}
	return &modeldevv1.InspectExecutionResponse{Inspection: inspection(view)}, nil
}

func (operations *Operations) ReconcileExecution(ctx context.Context, request *modeldevv1.ReconcileExecutionRequest) (*modeldevv1.ReconcileExecutionResponse, error) {
	access, err := queryScope(ctx, modeldevv1.ModelDevOperationsService_ReconcileExecution_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || !queryID(request.ExecutionId) || len(request.ProtoReflect().GetUnknown()) != 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid reconciliation request")
	}
	if operations == nil || operations.executions == nil {
		return nil, status.Error(codes.Unavailable, "operations unavailable")
	}
	view, err := operations.executions.ReconcileWithActor(ctx, access.TenantID, request.ExecutionId, access.Actor)
	if err != nil {
		return nil, operationsError(err, access.RequestID)
	}
	return &modeldevv1.ReconcileExecutionResponse{Inspection: inspection(view)}, nil
}

func (operations *Operations) PlanExecutionCleanup(ctx context.Context, request *modeldevv1.PlanExecutionCleanupRequest) (*modeldevv1.PlanExecutionCleanupResponse, error) {
	access, err := queryScope(ctx, modeldevv1.ModelDevOperationsService_PlanExecutionCleanup_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || !queryID(request.ExecutionId) || len(request.ProtoReflect().GetUnknown()) != 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid cleanup plan request")
	}
	if operations == nil || operations.executions == nil {
		return nil, status.Error(codes.Unavailable, "operations unavailable")
	}
	plan, err := operations.executions.PlanCleanup(ctx, access.TenantID, request.ExecutionId)
	if err != nil {
		return nil, operationsError(err, access.RequestID)
	}
	return &modeldevv1.PlanExecutionCleanupResponse{ExecutionId: plan.ExecutionID, OperationId: plan.OperationID, ExecutionSpecHash: plan.SpecHash, OwnerRevision: plan.OwnerRevision, CloseGeneration: plan.CloseGeneration, NamespaceUid: plan.NamespaceUID, PublicationId: plan.PublicationID, Targets: operationResources(plan.Targets), PlanSha256: plan.PlanHash, Retained: plan.Retained, ObservedAt: operationTimestamp(plan.ObservedAt)}, nil
}

func (operations *Operations) ApplyExecutionCleanup(ctx context.Context, request *modeldevv1.ApplyExecutionCleanupRequest) (*modeldevv1.ApplyExecutionCleanupResponse, error) {
	access, err := queryScope(ctx, modeldevv1.ModelDevOperationsService_ApplyExecutionCleanup_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || !queryID(request.ExecutionId) || len(request.ProtoReflect().GetUnknown()) != 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid cleanup apply request")
	}
	if operations == nil || operations.executions == nil {
		return nil, status.Error(codes.Unavailable, "operations unavailable")
	}
	receipt, err := operations.executions.ApplyCleanup(ctx, access.TenantID, request.ExecutionId, request.PlanSha256, access.Actor)
	if err != nil {
		return nil, operationsError(err, access.RequestID)
	}
	return &modeldevv1.ApplyExecutionCleanupResponse{ExecutionId: receipt.ExecutionID, PlanSha256: receipt.PlanHash, Phase: receipt.Phase, Requested: operationResources(receipt.Requested), ConfirmedAbsent: operationResources(receipt.ConfirmedAbsent), Actor: receipt.Actor, StartedAt: operationTimestamp(receipt.StartedAt), CompletedAt: operationTimestamp(receipt.CompletedAt)}, nil
}

func operationsError(err error, requestID string) error {
	if errors.Is(err, biz.ErrModelRegistrationNotApplicable) {
		return status.Error(codes.FailedPrecondition, "execution has no Model registration target")
	}
	if errors.Is(err, biz.ErrCleanupBlocked) || errors.Is(err, biz.ErrCleanupConflict) || errors.Is(err, biz.ErrCleanupUncertain) {
		return status.Error(codes.FailedPrecondition, "cleanup requires current closed identities and retained output evidence")
	}
	if errors.Is(err, biz.ErrInvalidAdmission) {
		return status.Error(codes.InvalidArgument, "invalid operation request")
	}
	return queryError(err, requestID)
}

func inspection(view biz.ExecutionInspect) *modeldevv1.ExecutionInspection {
	result := &modeldevv1.ExecutionInspection{ExecutionId: view.ExecutionID, OperationId: view.OperationID, ExecutionSpecHash: view.SpecHash, OwnerRevision: view.OwnerRevision, Namespace: view.NamespaceName, NamespaceUid: view.NamespaceUID, RunId: view.RunID, WorkflowUid: view.WorkflowUID, PvcUid: view.PVCUID, Resources: operationResources(view.Resources), ComputeState: string(view.States.Compute), DeliveryState: string(view.States.Delivery), CloseState: string(view.States.Close), DispatchState: string(view.DispatchState), RunCreateInFlight: view.RunCreateInFlight, TrainingCreateInFlight: view.TrainingCreateInFlight, CloseGeneration: view.CloseGeneration, CloseReason: view.CloseReason, CloseReviewReason: view.CloseReviewReason, PublicationId: view.PublicationID, ObservedAt: operationTimestamp(view.ObservedAt), RetrievedAt: operationTimestamp(view.RetrievedAt), ObservationStale: view.ObservationStale, ComputeOutcome: view.ComputeOutcome, CleanupPhase: view.CleanupPhase, CleanupPlanSha256: view.CleanupPlanHash}
	if view.ClosedAt != nil {
		result.ClosedAt = operationTimestamp(*view.ClosedAt)
	}
	return result
}

func operationTimestamp(at time.Time) *timestamppb.Timestamp {
	if at.IsZero() {
		return nil
	}
	return timestamppb.New(at)
}

func operationResources(resources []biz.RuntimeResource) []*modeldevv1.OperationResource {
	result := make([]*modeldevv1.OperationResource, 0, len(resources))
	for _, resource := range resources {
		result = append(result, &modeldevv1.OperationResource{ApiVersion: resource.APIVersion, Kind: resource.Kind, Namespace: resource.Namespace, Name: resource.Name, Uid: resource.UID, OwnerUid: resource.OwnerUID, ApiObjectPresent: resource.APIObjectPresent, CreationDisabled: resource.CreationDisabled, Terminal: resource.Terminal, ExitCode: resource.ExitCode})
	}
	return result
}

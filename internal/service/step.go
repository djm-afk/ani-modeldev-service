package service

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Step adapts managed workload requests to the configured business capabilities.
type Step struct {
	modeldevv1.UnimplementedModelDevStepServiceServer
	steps   *biz.ManagedSteps
	runtime *biz.ManagedRuntime
	storage biz.StorageCredentialIssuer
}

func NewStep(steps *biz.ManagedSteps) *Step {
	return &Step{steps: steps}
}

func NewRuntimeStep(steps *biz.ManagedSteps, runtime *biz.ManagedRuntime, storage ...biz.StorageCredentialIssuer) *Step {
	step := &Step{steps: steps, runtime: runtime}
	if len(storage) == 1 {
		step.storage = storage[0]
	}
	return step
}

func (step *Step) BeginExecution(ctx context.Context, request *modeldevv1.BeginExecutionRequest) (*modeldevv1.BeginExecutionResponse, error) {
	tenant, token, authenticated := managedStepCredentials(ctx)
	if !authenticated {
		return nil, commandError(codes.Unauthenticated, modeldevv1.ErrorReason_ERROR_REASON_UNAUTHENTICATED, "managed workload credentials over TLS required", "")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	invalid := func() (*modeldevv1.BeginExecutionResponse, error) {
		return nil, commandError(codes.InvalidArgument, modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT, "invalid managed Begin request", "")
	}
	if request == nil || request.Context == nil || request.Context.Identity == nil || request.Context.Association == nil ||
		len(request.ProtoReflect().GetUnknown()) != 0 || len(request.Context.ProtoReflect().GetUnknown()) != 0 ||
		len(request.Context.Identity.ProtoReflect().GetUnknown()) != 0 || len(request.Context.Association.ProtoReflect().GetUnknown()) != 0 ||
		request.Context.Step != modeldevv1.PipelineStep_PIPELINE_STEP_PREPARE {
		return invalid()
	}
	identity, association := request.Context.Identity, request.Context.Association
	for _, value := range []string{identity.ExecutionId, identity.ExecutionSpecHash,
		association.KfpRunId, association.NamespaceName, association.NamespaceUid,
		association.WorkflowName, association.WorkflowUid, association.PodName, association.PodUid} {
		if value == "" {
			return invalid()
		}
	}
	unavailable := func() (*modeldevv1.BeginExecutionResponse, error) {
		return nil, commandError(codes.Unavailable, modeldevv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE, "managed Begin unavailable", "")
	}
	if step == nil || step.steps == nil {
		return unavailable()
	}
	// Tenant metadata is only a selector. Begin verifies the current workload and
	// its frozen namespace, ServiceAccount and Run association before any binding.
	result, err := step.steps.Begin(ctx, token, biz.BeginManagedExecutionRequest{
		TenantID: tenant, OperationID: identity.OperationId, ExecutionID: identity.ExecutionId, SpecHash: identity.ExecutionSpecHash,
		Association: biz.ManagedStepAssociation{
			RunID: association.KfpRunId, NamespaceName: association.NamespaceName, NamespaceUID: association.NamespaceUid,
			WorkflowName: association.WorkflowName, WorkflowUID: association.WorkflowUid,
			PodName: association.PodName, PodUID: association.PodUid,
		},
	})
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil, status.FromContextError(err).Err()
		case errors.Is(err, biz.ErrInvalidAdmission):
			return invalid()
		case errors.Is(err, biz.ErrManagedStepUnauthorized):
			return nil, commandError(codes.Unauthenticated, modeldevv1.ErrorReason_ERROR_REASON_UNAUTHENTICATED, "managed workload authentication failed", "")
		case errors.Is(err, biz.ErrRunAuthorityConflict), errors.Is(err, biz.ErrAdmissionConflict), errors.Is(err, biz.ErrExecutionNotFound):
			return nil, commandError(codes.PermissionDenied, modeldevv1.ErrorReason_ERROR_REASON_AUTHORITY_MISMATCH, "managed workload association denied", "")
		case errors.Is(err, biz.ErrPipelineDispatchBlocked):
			return nil, commandError(codes.FailedPrecondition, modeldevv1.ErrorReason_ERROR_REASON_EXECUTION_CLOSING, "execution does not permit a new authority binding", "")
		default:
			return unavailable()
		}
	}
	authority := result.Authority
	states, valid := acceptExecutionStates(result.States)
	boundAt := timestamppb.New(authority.BoundAt)
	if !valid || authority.OwnerRevision == 0 || authority.BoundAt.IsZero() || boundAt.CheckValid() != nil ||
		authority.TenantID != tenant || !runtimeUUID(authority.OperationID) || (identity.OperationId != "" && authority.OperationID != identity.OperationId) || authority.ExecutionID != identity.ExecutionId || authority.SpecHash != identity.ExecutionSpecHash ||
		authority.RunID != association.KfpRunId || authority.NamespaceName != association.NamespaceName || authority.NamespaceUID != association.NamespaceUid ||
		authority.WorkflowName != association.WorkflowName || authority.WorkflowUID != association.WorkflowUid {
		return unavailable()
	}
	if step.runtime == nil {
		return unavailable()
	}
	timing, _, err := step.runtime.BeginManagedStep(ctx, token, biz.BeginManagedExecutionRequest{TenantID: tenant, OperationID: authority.OperationID, ExecutionID: identity.ExecutionId, SpecHash: identity.ExecutionSpecHash,
		Association: biz.ManagedStepAssociation{RunID: association.KfpRunId, NamespaceName: association.NamespaceName, NamespaceUID: association.NamespaceUid, WorkflowName: association.WorkflowName, WorkflowUID: association.WorkflowUid, PodName: association.PodName, PodUID: association.PodUid}}, "prepare")
	if err != nil {
		return nil, runtimeError(err)
	}
	return &modeldevv1.BeginExecutionResponse{
		Authority: &modeldevv1.AuthorityBinding{
			KfpRunId: authority.RunID, NamespaceUid: authority.NamespaceUID, WorkflowUid: authority.WorkflowUID, BoundAt: boundAt,
		},
		States: states, Replayed: authority.Replayed,
		Timing: encodeStepTiming(timing),
	}, nil
}

// This check stays at the adapter boundary even when registered on another
// server. Forwarded metadata can never substitute for the actual TLS channel.
func managedStepCredentials(ctx context.Context) (tenant, token string, ok bool) {
	remote, found := peer.FromContext(ctx)
	if !found {
		return "", "", false
	}
	security, found := remote.AuthInfo.(credentials.TLSInfo)
	if !found || !security.State.HandshakeComplete || security.State.Version < tls.VersionTLS12 {
		return "", "", false
	}
	md, found := metadata.FromIncomingContext(ctx)
	if !found {
		return "", "", false
	}
	for _, key := range []string{"proxy-authorization", "cookie", "x-forwarded-client-cert", "ani-workload-token", "ani-delegation"} {
		if len(md.Get(key)) != 0 {
			return "", "", false
		}
	}
	tenants, credentials := md.Get("x-ani-tenant-id"), md.Get("authorization")
	if len(tenants) != 1 || len(credentials) != 1 || !strings.HasPrefix(credentials[0], "Bearer ") {
		return "", "", false
	}
	id, err := uuid.Parse(tenants[0])
	if err != nil || id == uuid.Nil || id.String() != tenants[0] {
		return "", "", false
	}
	token = strings.TrimPrefix(credentials[0], "Bearer ")
	if token == "" || len(token) > 16384 {
		return "", "", false
	}
	for _, character := range token {
		if character < '!' || character > '~' || character == ',' {
			return "", "", false
		}
	}
	return tenants[0], token, true
}

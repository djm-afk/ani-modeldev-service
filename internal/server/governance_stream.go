package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// verifiedQueryMethods lists the unary reads that carry the BFF's per-request
// current authorization. It is one list so the unary and stream paths cannot
// drift apart.
func verifiedQueryMethods(query modeldevv1.ModelDevQueryServiceServer, extra GovernanceServices) map[string]bool {
	methods := make(map[string]bool, 12)
	if query != nil {
		for _, method := range []string{
			modeldevv1.ModelDevQueryService_GetExecution_FullMethodName,
			modeldevv1.ModelDevQueryService_ListExecutions_FullMethodName,
			modeldevv1.ModelDevQueryService_GetExecutionLogs_FullMethodName,
			modeldevv1.ModelDevQueryService_ListExecutionArtifacts_FullMethodName,
			modeldevv1.ModelDevQueryService_AuthorizeArtifactDownload_FullMethodName,
			modeldevv1.ModelDevQueryService_ListImplementations_FullMethodName,
			modeldevv1.ModelDevQueryService_GetInputVersion_FullMethodName,
			modeldevv1.ModelDevQueryService_ListInputVersions_FullMethodName,
			modeldevv1.ModelDevQueryService_ListExecutionPods_FullMethodName,
			modeldevv1.ModelDevQueryService_ListExecutionEvents_FullMethodName,
		} {
			methods[method] = true
		}
	}
	if extra.Management != nil {
		methods[modeldevv1.ModelDevManagementService_ImportCSV_FullMethodName] = true
		methods[modeldevv1.ModelDevManagementService_ImportFinetuningDataset_FullMethodName] = true
	}
	if extra.Operations != nil {
		methods[modeldevv1.ModelDevOperationsService_InspectExecution_FullMethodName] = true
		methods[modeldevv1.ModelDevOperationsService_ReconcileExecution_FullMethodName] = true
		methods[modeldevv1.ModelDevOperationsService_PlanExecutionCleanup_FullMethodName] = true
		methods[modeldevv1.ModelDevOperationsService_ApplyExecutionCleanup_FullMethodName] = true
		methods[modeldevv1.ModelDevOperationsService_ResumeModelRegistration_FullMethodName] = true
	}
	return methods
}

// verifyGovernancePeer authenticates the calling workload and returns the
// context the handler must run with. Both the unary and the streaming path call
// it, so a method cannot be reachable through one transport only.
func verifyGovernancePeer(ctx context.Context, method string, security CommandTLS, roots *x509.CertPool,
	verifyPeer func(tls.ConnectionState) error, admission modeldevv1.ModelDevAdmissionServiceServer,
	query modeldevv1.ModelDevQueryServiceServer, extra GovernanceServices) (context.Context, error) {
	denied := status.Error(codes.Unauthenticated, "authenticated Governance delivery required")
	remote, ok := peer.FromContext(ctx)
	if !ok {
		return nil, denied
	}
	tlsInfo, ok := remote.AuthInfo.(credentials.TLSInfo)
	if !ok || verifyPeer(tlsInfo.State) != nil {
		return nil, denied
	}
	allowed := method == modeldevv1.ModelDevCommandService_ApplyCloseIntent_FullMethodName ||
		method == modeldevv1.ModelDevCommandService_AcceptExecution_FullMethodName ||
		(admission != nil && (method == modeldevv1.ModelDevAdmissionService_PrepareAdmission_FullMethodName || method == modeldevv1.ModelDevAdmissionService_FinalizeAdmission_FullMethodName))
	isQuery := verifiedQueryMethods(query, extra)[method]
	if !allowed && !isQuery {
		return nil, status.Error(codes.PermissionDenied, "Governance delivery does not permit this method")
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, denied
	}
	// User credentials and forwarded certificate headers cannot accompany these
	// workload requests or substitute for the actual TLS peer.
	for _, key := range []string{"authorization", "proxy-authorization", "cookie", "x-forwarded-client-cert", "ani-workload-token", "ani-delegation"} {
		if len(md.Get(key)) != 0 {
			return nil, denied
		}
	}
	single := func(key string) string {
		values := md.Get(key)
		if len(values) != 1 {
			return ""
		}
		return values[0]
	}
	delivery := service.GovernanceDelivery{TenantID: single("x-ani-tenant-id"), Actor: single("x-ani-actor"), RequestID: single("x-ani-request-id")}
	canonicalID := func(value string) bool {
		id, err := uuid.Parse(value)
		return err == nil && id != uuid.Nil && id.String() == value
	}
	if !canonicalID(delivery.TenantID) || !canonicalID(delivery.RequestID) || !validGovernanceCommandActor(delivery.Actor) {
		return nil, denied
	}
	if isQuery {
		// Only this freshly authenticated BFF workload can assert the current
		// actor's exact method and tenant-wide data permission. Ordinary command
		// delivery, admission history and user-supplied headers grant none.
		if single("x-ani-authorized-method") != method || single("x-ani-data-scope") != "tenant-all" {
			return nil, status.Error(codes.PermissionDenied, "current query authorization required")
		}
		return service.WithVerifiedGovernanceQuery(ctx, delivery, method), nil
	}
	return service.WithVerifiedGovernanceDelivery(ctx, delivery), nil
}

// authenticateStream admits exactly one streaming method: the execution log
// stream. Every other streaming call stays denied rather than inheriting an
// allow-list from the unary path.
func authenticateStream(security CommandTLS, roots *x509.CertPool, verifyPeer func(tls.ConnectionState) error, query modeldevv1.ModelDevQueryServiceServer) grpc.StreamServerInterceptor {
	return func(server any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if query == nil || info.FullMethod != modeldevv1.ModelDevQueryService_GetExecutionLogStream_FullMethodName {
			return status.Error(codes.PermissionDenied, "Governance delivery does not permit this method")
		}
		wrapped, err := streamingContext(stream, info.FullMethod, security, roots, verifyPeer, query)
		if err != nil {
			return err
		}
		return handler(server, wrapped)
	}
}

// streamingContext wraps the server stream with the authenticated context so
// the handler's own context carries the verified delivery, and cancellation
// still propagates from the peer.
func streamingContext(stream grpc.ServerStream, method string, security CommandTLS, roots *x509.CertPool,
	verifyPeer func(tls.ConnectionState) error, query modeldevv1.ModelDevQueryServiceServer) (grpc.ServerStream, error) {
	verified, err := verifyGovernancePeer(stream.Context(), method, security, roots, verifyPeer, nil, query, GovernanceServices{})
	if err != nil {
		return nil, err
	}
	return &verifiedStream{ServerStream: stream, ctx: verified}, nil
}

type verifiedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (stream *verifiedStream) Context() context.Context { return stream.ctx }

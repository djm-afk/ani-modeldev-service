package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/go-kratos/kratos/v3/middleware"
	kratosgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// CommandServerDNSName is the registered server identity for the Governance ->
// ModelDev business listener. Governance verifies this literal DNS SAN, so the
// mounted server certificate must present it exactly.
const CommandServerDNSName = "ani-modeldev-service"

// CommandTLS contains explicit deployment inputs. No environment certificate,
// endpoint or peer name is supplied by a default or by the RPC body.
type CommandTLS struct {
	Certificate       tls.Certificate
	ClientCAs         *x509.CertPool
	GovernanceDNSName string
}

// ValidCommandServerCertificate requires the mounted server certificate to
// present the registered literal DNS SAN with a server-auth usage. A mismatched
// certificate would make every Governance handshake fail, so it is rejected at
// startup instead of at the first RPC. Additional SANs are allowed; a wildcard
// DNS entry is never accepted.
func ValidCommandServerCertificate(certificate tls.Certificate) bool {
	if len(certificate.Certificate) == 0 || certificate.PrivateKey == nil {
		return false
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil || time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
		return false
	}
	matched := false
	for _, name := range leaf.DNSNames {
		if strings.Contains(name, "*") {
			return false
		}
		matched = matched || name == CommandServerDNSName
	}
	if !matched {
		return false
	}
	if len(leaf.ExtKeyUsage) == 0 {
		return true
	}
	for _, usage := range leaf.ExtKeyUsage {
		if usage == x509.ExtKeyUsageAny || usage == x509.ExtKeyUsageServerAuth {
			return true
		}
	}
	return false
}

// NewGovernanceCommandServer registers commands and optional read-only admission
// resolution on a dedicated mTLS server. Every RPC rechecks the peer against the
// configured private CA and current certificate validity; a prior TLS handshake
// is not permanent access.
func NewGovernanceCommandServer(c *conf.Server_GRPC, security CommandTLS, command modeldevv1.ModelDevCommandServiceServer, admission modeldevv1.ModelDevAdmissionServiceServer, middlewares ...middleware.Middleware) (*kratosgrpc.Server, error) {
	return newGovernanceServer(c, security, command, admission, nil, GovernanceServices{}, middlewares...)
}

type GovernanceServices struct {
	Management modeldevv1.ModelDevManagementServiceServer
	Operations modeldevv1.ModelDevOperationsServiceServer
}

func newGovernanceServer(c *conf.Server_GRPC, security CommandTLS, command modeldevv1.ModelDevCommandServiceServer, admission modeldevv1.ModelDevAdmissionServiceServer, query modeldevv1.ModelDevQueryServiceServer, extra GovernanceServices, middlewares ...middleware.Middleware) (*kratosgrpc.Server, error) {
	if c == nil || security.ClientCAs == nil || len(security.Certificate.Certificate) == 0 || security.Certificate.PrivateKey == nil || security.GovernanceDNSName == "" || strings.ContainsAny(security.GovernanceDNSName, "* /\t\r\n") || command == nil {
		return nil, errors.New("explicit command TLS and handler configuration required")
	}
	roots := security.ClientCAs.Clone()
	verifyPeer := func(state tls.ConnectionState) error {
		if !state.HandshakeComplete || state.Version < tls.VersionTLS13 || len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
			return errors.New("verified Governance certificate required")
		}
		leaf := state.PeerCertificates[0]
		if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != security.GovernanceDNSName || len(leaf.IPAddresses) != 0 || len(leaf.URIs) != 0 || len(leaf.EmailAddresses) != 0 {
			return errors.New("exact Governance certificate identity required")
		}
		intermediates := x509.NewCertPool()
		for _, certificate := range state.PeerCertificates[1:] {
			intermediates.AddCert(certificate)
		}
		_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: time.Now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, DNSName: security.GovernanceDNSName})
		return err
	}
	authenticate := func(ctx context.Context, request any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		// Reconcile contains two existing finite two-minute stages. Give only
		// that exact method an outer envelope; never replace caller cancellation.
		timeout := c.Timeout.AsDuration()
		if info.FullMethod == modeldevv1.ModelDevOperationsService_ReconcileExecution_FullMethodName {
			timeout = 5 * time.Minute
		}
		if timeout > 0 {
			bounded, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			ctx = bounded
		}
		denied := status.Error(codes.Unauthenticated, "authenticated Governance delivery required")
		remote, ok := peer.FromContext(ctx)
		if !ok {
			return nil, denied
		}
		tlsInfo, ok := remote.AuthInfo.(credentials.TLSInfo)
		if !ok || verifyPeer(tlsInfo.State) != nil {
			return nil, denied
		}
		allowed := info.FullMethod == modeldevv1.ModelDevCommandService_ApplyCloseIntent_FullMethodName ||
			info.FullMethod == modeldevv1.ModelDevCommandService_AcceptExecution_FullMethodName ||
			(admission != nil && (info.FullMethod == modeldevv1.ModelDevAdmissionService_PrepareAdmission_FullMethodName || info.FullMethod == modeldevv1.ModelDevAdmissionService_FinalizeAdmission_FullMethodName))
		isQuery := query != nil && (info.FullMethod == modeldevv1.ModelDevQueryService_GetExecution_FullMethodName || info.FullMethod == modeldevv1.ModelDevQueryService_ListExecutions_FullMethodName || info.FullMethod == modeldevv1.ModelDevQueryService_GetExecutionLogs_FullMethodName || info.FullMethod == modeldevv1.ModelDevQueryService_ListExecutionArtifacts_FullMethodName || info.FullMethod == modeldevv1.ModelDevQueryService_AuthorizeArtifactDownload_FullMethodName)
		isQuery = isQuery || (query != nil && (info.FullMethod == modeldevv1.ModelDevQueryService_ListImplementations_FullMethodName || info.FullMethod == modeldevv1.ModelDevQueryService_GetInputVersion_FullMethodName || info.FullMethod == modeldevv1.ModelDevQueryService_ListInputVersions_FullMethodName))
		isQuery = isQuery || (extra.Management != nil && (info.FullMethod == modeldevv1.ModelDevManagementService_ImportCSV_FullMethodName || info.FullMethod == modeldevv1.ModelDevManagementService_ImportFinetuningDataset_FullMethodName))
		isQuery = isQuery || (extra.Operations != nil && (info.FullMethod == modeldevv1.ModelDevOperationsService_InspectExecution_FullMethodName || info.FullMethod == modeldevv1.ModelDevOperationsService_ReconcileExecution_FullMethodName || info.FullMethod == modeldevv1.ModelDevOperationsService_PlanExecutionCleanup_FullMethodName || info.FullMethod == modeldevv1.ModelDevOperationsService_ApplyExecutionCleanup_FullMethodName || info.FullMethod == modeldevv1.ModelDevOperationsService_ResumeModelRegistration_FullMethodName))
		allowed = allowed || isQuery
		if !allowed {
			return nil, status.Error(codes.PermissionDenied, "Governance delivery does not permit this method")
		}
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, denied
		}
		// User credentials and forwarded certificate headers cannot accompany
		// these workload requests or substitute for the actual TLS peer.
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
			if single("x-ani-authorized-method") != info.FullMethod || single("x-ani-data-scope") != "tenant-all" {
				return nil, status.Error(codes.PermissionDenied, "current query authorization required")
			}
			return next(service.WithVerifiedGovernanceQuery(ctx, delivery, info.FullMethod), request)
		}
		return next(service.WithVerifiedGovernanceDelivery(ctx, delivery), request)
	}
	transportTimeout := max(c.Timeout.AsDuration(), 5*time.Minute)
	s := kratosgrpc.NewServer(
		kratosgrpc.Network(c.Network), kratosgrpc.Address(c.Addr),
		kratosgrpc.Timeout(transportTimeout),
		kratosgrpc.TLSConfig(&tls.Config{
			MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert,
			ClientCAs: roots, Certificates: []tls.Certificate{security.Certificate},
		}),
		kratosgrpc.UnaryInterceptor(authenticate),
		kratosgrpc.StreamInterceptor(func(any, grpc.ServerStream, *grpc.StreamServerInfo, grpc.StreamHandler) error {
			return status.Error(codes.PermissionDenied, "Governance delivery does not permit streaming")
		}),
		kratosgrpc.Middleware(middlewares...), kratosgrpc.DisableReflection(),
	)
	modeldevv1.RegisterModelDevCommandServiceServer(s, command)
	if admission != nil {
		modeldevv1.RegisterModelDevAdmissionServiceServer(s, admission)
	}
	if query != nil {
		modeldevv1.RegisterModelDevQueryServiceServer(s, query)
	}
	if extra.Management != nil {
		modeldevv1.RegisterModelDevManagementServiceServer(s, extra.Management)
	}
	if extra.Operations != nil {
		modeldevv1.RegisterModelDevOperationsServiceServer(s, extra.Operations)
	}
	return s, nil
}

func validGovernanceCommandActor(actor string) bool {
	for _, prefix := range []string{"governance:user:", "governance:access-key:"} {
		if strings.HasPrefix(actor, prefix) {
			id, err := strconv.ParseUint(strings.TrimPrefix(actor, prefix), 10, 32)
			return err == nil && id != 0 && actor == prefix+strconv.FormatUint(id, 10)
		}
	}
	return false
}

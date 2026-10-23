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
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"google.golang.org/grpc"
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
		verified, err := verifyGovernancePeer(ctx, info.FullMethod, security, roots, verifyPeer, admission, query, extra)
		if err != nil {
			return nil, err
		}
		return next(verified, request)
	}
	s := kratosgrpc.NewServer(
		kratosgrpc.Network(c.Network), kratosgrpc.Address(c.Addr),
		kratosgrpc.Timeout(c.Timeout.AsDuration()),
		kratosgrpc.TLSConfig(&tls.Config{
			MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert,
			ClientCAs: roots, Certificates: []tls.Certificate{security.Certificate},
		}),
		kratosgrpc.UnaryInterceptor(authenticate),
		kratosgrpc.StreamInterceptor(authenticateStream(security, roots, verifyPeer, query)),
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

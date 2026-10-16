// Package modelclient is the ModelDev -> Model business mTLS adapter. It owns
// only transport, caller identity and request metadata; it never decides
// whether a registration write is permitted. That decision belongs to the
// durable dispatch arbitration, and callers must persist it before invoking a
// write method here.
package modelclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Config is deployment-only connection material. No public request selects an
// endpoint, certificate or server identity.
type Config struct {
	// Endpoint is the literal host:port of the Model business mTLS listener.
	Endpoint string
	// CAFile trusts the Model server certificate chain.
	CAFile string
	// ClientDNSName is the single DNS SAN this client certificate must present.
	ClientDNSName   string
	CertificateFile string
	PrivateKeyFile  string
	// ServerName is the exact DNS SAN required on the Model server certificate.
	ServerName string
	// Actor is the stable audit actor asserted on every call. It never grants
	// authorization on its own.
	Actor string
	// Timeout bounds each unary RPC. Callers still inherit their own deadline.
	Timeout time.Duration
}

// Client is safe for concurrent use once constructed.
type Client struct {
	connection *grpc.ClientConn
	client     modelv1.ModelServiceClient
	actor      string
	timeout    time.Duration
}

const maximumTimeout = 30 * time.Second

// New validates every material at startup. A partially materialised or
// mismatched identity fails closed instead of degrading to an unauthenticated
// call, and never falls back to plaintext or an ambient credential.
func New(config Config) (*Client, func(), error) {
	if strings.TrimSpace(config.Endpoint) != config.Endpoint || strings.ContainsAny(config.Endpoint, "/\\ \t\r\n") || config.Endpoint == "" {
		return nil, nil, errors.New("invalid model client endpoint")
	}
	if config.ServerName == "" || strings.ContainsAny(config.ServerName, "*:/\\ \t\r\n") ||
		config.ClientDNSName == "" || strings.ContainsAny(config.ClientDNSName, "*:/\\ \t\r\n") ||
		config.ClientDNSName == config.ServerName {
		return nil, nil, errors.New("model client requires distinct exact client and server DNS identities")
	}
	if config.Actor == "" || len(config.Actor) > 253 || strings.ContainsAny(config.Actor, " \t\r\n") {
		return nil, nil, errors.New("model client requires a bounded audit actor")
	}
	if config.Timeout <= 0 || config.Timeout > maximumTimeout {
		return nil, nil, errors.New("invalid model client timeout")
	}
	certificate, err := tls.LoadX509KeyPair(config.CertificateFile, config.PrivateKeyFile)
	if err != nil || len(certificate.Certificate) == 0 {
		return nil, nil, errors.New("model client certificate unavailable")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil || !exactClientIdentity(leaf, config.ClientDNSName) {
		return nil, nil, errors.New("model client certificate does not present the registered DNS identity")
	}
	caPEM, err := os.ReadFile(config.CAFile)
	if err != nil {
		return nil, nil, errors.New("model client CA unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, nil, errors.New("model client CA contains no certificates")
	}
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      roots,
		Certificates: []tls.Certificate{certificate},
		ServerName:   config.ServerName,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 ||
				!hasExactDNSName(state.PeerCertificates[0], config.ServerName) {
				return errors.New("model server exact DNS SAN mismatch")
			}
			return nil
		},
	}
	connection, err := grpc.NewClient(config.Endpoint,
		grpc.WithDisableServiceConfig(), grpc.WithDisableRetry(),
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	if err != nil {
		return nil, nil, errors.New("invalid model client address")
	}
	return &Client{
		connection: connection,
		client:     modelv1.NewModelServiceClient(connection),
		actor:      config.Actor,
		timeout:    config.Timeout,
	}, func() { _ = connection.Close() }, nil
}

// exactClientIdentity requires exactly one DNS SAN, no URI/IP/email SAN and no
// wildcard, matching the receiving policy's exact-identity lookup.
func exactClientIdentity(cert *x509.Certificate, want string) bool {
	return cert != nil && len(cert.DNSNames) == 1 && cert.DNSNames[0] == want &&
		!strings.Contains(want, "*") &&
		len(cert.URIs) == 0 && len(cert.IPAddresses) == 0 && len(cert.EmailAddresses) == 0
}

func hasExactDNSName(cert *x509.Certificate, name string) bool {
	if cert == nil {
		return false
	}
	matched := false
	for _, dns := range cert.DNSNames {
		if strings.Contains(dns, "*") {
			return false
		}
		matched = matched || dns == name
	}
	return matched
}

// callContext replaces, rather than appends to, any inbound metadata. A caller
// can never forward a forged tenant, actor, request ID or bearer token.
func (c *Client) callContext(ctx context.Context, tenant string) (context.Context, context.CancelFunc, error) {
	if c == nil || c.client == nil || c.timeout <= 0 {
		return nil, nil, status.Error(codes.Unavailable, "MODEL_ADAPTER_UNAVAILABLE")
	}
	parsed, err := uuid.Parse(tenant)
	if err != nil || parsed == uuid.Nil || parsed.String() != tenant {
		return nil, nil, status.Error(codes.InvalidArgument, "MODEL_TENANT_INVALID")
	}
	call, cancel := context.WithTimeout(ctx, c.timeout)
	call = metadata.NewOutgoingContext(call, metadata.Pairs(
		"x-ani-tenant-id", tenant,
		"x-ani-actor", c.actor,
		"x-ani-request-id", uuid.NewString(),
	))
	return call, cancel, nil
}

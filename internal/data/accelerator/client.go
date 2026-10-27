// Package accelerator implements only the ModelDev GPU owner's Governance
// release reporting RPC.
package accelerator

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	quotapb "github.com/zhangzhe-ctrl/ani-governance/api/quota/gen/go/quota/service/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"k8s.io/apimachinery/pkg/util/validation"
)

const ClientDNSName = "ani-modeldev-service"

// The current registered server identity is distinct from the Service endpoint.
// Every connection still requires an explicit configured and verified DNS SAN.
const GovernanceDNSName = "ani-governance"

// Config is explicit deployment material; business input cannot select it.
type Config struct {
	Endpoint, CAFile, CertificateFile, PrivateKeyFile, ServerName string
	Timeout                                                       time.Duration
}

type QuotaClient struct {
	connection *grpc.ClientConn
	client     quotapb.QuotaReleaseServiceClient
	timeout    time.Duration
}

var _ biz.GPUReleaseReporter = (*QuotaClient)(nil)

func exactIdentity(cert *x509.Certificate, want string) bool {
	return cert != nil && len(cert.DNSNames) == 1 && cert.DNSNames[0] == want && !strings.Contains(want, "*") && len(cert.URIs) == 0 && len(cert.IPAddresses) == 0 && len(cert.EmailAddresses) == 0
}

func exactServerDNS(cert *x509.Certificate, want string) bool {
	if cert == nil {
		return false
	}
	matched := false
	for _, name := range cert.DNSNames {
		if strings.Contains(name, "*") {
			return false
		}
		matched = matched || name == want
	}
	return matched
}

func ownerConnection(config Config) (*grpc.ClientConn, error) {
	host, port, err := net.SplitHostPort(config.Endpoint)
	number, numberErr := strconv.ParseUint(port, 10, 16)
	if err != nil || host == "" || strings.ContainsAny(host, "/\\ \t\r\n") || numberErr != nil || number == 0 || len(validation.IsDNS1123Subdomain(config.ServerName)) != 0 || config.ServerName == ClientDNSName || config.Timeout <= 0 || config.Timeout > 30*time.Second {
		return nil, biz.ErrRuntimeNotReady
	}
	certificate, err := tls.LoadX509KeyPair(config.CertificateFile, config.PrivateKeyFile)
	if err != nil || len(certificate.Certificate) == 0 {
		return nil, biz.ErrRuntimeNotReady
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil || !exactIdentity(leaf, ClientDNSName) {
		return nil, biz.ErrRuntimeNotReady
	}
	ca, err := os.ReadFile(config.CAFile)
	if err != nil || len(ca) > 2<<20 {
		return nil, biz.ErrRuntimeNotReady
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, biz.ErrRuntimeNotReady
	}
	server := config.ServerName
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate}, ServerName: server, VerifyConnection: func(state tls.ConnectionState) error {
		if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 || !exactServerDNS(state.PeerCertificates[0], server) {
			return errors.New("GPU owner server identity mismatch")
		}
		return nil
	}}
	return grpc.NewClient(config.Endpoint, grpc.WithDisableServiceConfig(), grpc.WithDisableRetry(), grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
}

func NewQuotaClient(config Config) (*QuotaClient, func(), error) {
	connection, err := ownerConnection(config)
	if err != nil {
		return nil, nil, err
	}
	return &QuotaClient{connection: connection, client: quotapb.NewQuotaReleaseServiceClient(connection), timeout: config.Timeout}, func() { _ = connection.Close() }, nil
}

func canonicalID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func (client *QuotaClient) ReportGPUResourceReleased(ctx context.Context, event biz.GPUResourceReleasedEvent) (biz.GPUReleaseACK, error) {
	if client == nil || client.client == nil || ctx == nil || !canonicalID(event.ReleaseEventID) || !canonicalID(event.CreateOperationID) || !canonicalID(event.ResourceID) {
		return biz.GPUReleaseACK{}, biz.ErrRuntimeConflict
	}
	reason := quotapb.ReleaseReason_RELEASE_REASON_UNSPECIFIED
	switch event.Reason {
	case "ABORTED_CLEANED":
		reason = quotapb.ReleaseReason_ABORTED_CLEANED
	case "RESOURCE_RELEASED":
		reason = quotapb.ReleaseReason_RESOURCE_RELEASED
	default:
		return biz.GPUReleaseACK{}, biz.ErrRuntimeConflict
	}
	request := &quotapb.ReportResourceReleasedRequest{ReleaseEventId: event.ReleaseEventID, CreateOperationId: event.CreateOperationID, ResourceId: event.ResourceID, Reason: reason}
	call, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, metadata.MD{}), client.timeout)
	defer cancel()
	response, err := client.client.ReportResourceReleased(call, request)
	if err != nil {
		return biz.GPUReleaseACK{}, err
	}
	if response == nil || len(response.ProtoReflect().GetUnknown()) != 0 || response.ReleaseEventId != event.ReleaseEventID || response.CreateOperationId != event.CreateOperationID || response.ResourceId != event.ResourceID || (response.Status != quotapb.ResourceReleaseStatus_APPLIED && response.Status != quotapb.ResourceReleaseStatus_ALREADY_APPLIED) {
		return biz.GPUReleaseACK{}, biz.ErrRuntimeConflict
	}
	return biz.GPUReleaseACK{ReleaseEventID: response.ReleaseEventId, CreateOperationID: response.CreateOperationId, ResourceID: response.ResourceId, Status: response.Status.String()}, nil
}

// Package accelerator implements only the ModelDev GPU owner's release RPCs.
package accelerator

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	accpb "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
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
const AcceleratorDNSName = "ani-accelerator-service.ani-accelerator.svc.cluster.local"
const GovernanceDNSName = "ani-governance"

// Config is explicit deployment material; business input cannot select it.
type Config struct {
	Endpoint, CAFile, CertificateFile, PrivateKeyFile, ServerName string
	Timeout                                                       time.Duration
}

type ReleaseClient struct {
	connection *grpc.ClientConn
	client     accpb.AcceleratorUsageServiceClient
	timeout    time.Duration
}
type QuotaClient struct {
	connection *grpc.ClientConn
	client     quotapb.QuotaReleaseServiceClient
	timeout    time.Duration
}

var _ biz.GPUReleaseObserver = (*ReleaseClient)(nil)
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

func NewReleaseClient(config Config) (*ReleaseClient, func(), error) {
	connection, err := ownerConnection(config)
	if err != nil {
		return nil, nil, err
	}
	return &ReleaseClient{connection: connection, client: accpb.NewAcceleratorUsageServiceClient(connection), timeout: config.Timeout}, func() { _ = connection.Close() }, nil
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
func digest(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == value
}
func validRef(ref biz.GPUReleaseRef) bool {
	return ref.OwnerService == ClientDNSName && canonicalID(ref.TenantID) && canonicalID(ref.ResourceID) && canonicalID(ref.CreateOperationID)
}

func (client *ReleaseClient) ObserveGPURelease(ctx context.Context, scope biz.GPUReleaseScope) (biz.GPUReleaseObservation, error) {
	if client == nil || client.client == nil || ctx == nil || !validRef(scope.Ref) || !canonicalID(scope.CloseOperationID) || scope.CloseOperationID == scope.Ref.CreateOperationID || !canonicalID(scope.ClusterID) || scope.SealedAt.IsZero() || len(scope.Pods) > 64 {
		return biz.GPUReleaseObservation{}, biz.ErrRuntimeConflict
	}
	request := &accpb.ObserveReleaseRequest{RequestId: uuid.NewString(), Ref: &accpb.GpuUsageRef{TenantId: scope.Ref.TenantID, OwnerService: scope.Ref.OwnerService, ResourceId: scope.Ref.ResourceID, CreateOperationId: scope.Ref.CreateOperationID}, DeleteOperationId: scope.CloseOperationID, ClusterId: scope.ClusterID, SealedAt: scope.SealedAt.UTC().Format(time.RFC3339Nano)}
	seen := map[string]bool{}
	for _, pod := range scope.Pods {
		if pod.ClusterID != scope.ClusterID || len(validation.IsDNS1123Label(pod.Namespace)) != 0 || len(validation.IsDNS1123Subdomain(pod.Name)) != 0 || pod.UID == "" || len(pod.UID) > 256 || strings.ContainsAny(pod.UID, "\x00\r\n") || pod.ContainerName != "node" || seen[pod.UID] {
			return biz.GPUReleaseObservation{}, biz.ErrRuntimeConflict
		}
		seen[pod.UID] = true
		request.Pods = append(request.Pods, &accpb.PodRef{ClusterId: pod.ClusterID, Namespace: pod.Namespace, Name: pod.Name, Uid: pod.UID, ContainerName: pod.ContainerName})
	}
	// Reconstruct ACC's finite owner context from the frozen original scope.
	// No actor, bearer or incoming metadata is delegated by this close call.
	call, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-action", "ObserveRelease", "x-ani-tenant-id", scope.Ref.TenantID)), client.timeout)
	defer cancel()
	response, err := client.client.ObserveRelease(call, request)
	if err != nil {
		return biz.GPUReleaseObservation{}, err
	}
	if response == nil || response.Ref == nil || response.Observation == nil || len(response.ProtoReflect().GetUnknown()) != 0 || len(response.Ref.ProtoReflect().GetUnknown()) != 0 || len(response.Observation.ProtoReflect().GetUnknown()) != 0 || !canonicalID(response.EvidenceId) || !digest(response.EvidenceDigest) {
		return biz.GPUReleaseObservation{}, biz.ErrRuntimeConflict
	}
	result := biz.GPUReleaseObservation{Ref: biz.GPUReleaseRef{TenantID: response.Ref.TenantId, OwnerService: response.Ref.OwnerService, ResourceID: response.Ref.ResourceId, CreateOperationID: response.Ref.CreateOperationId}, Finding: response.Finding.String(), Complete: response.Observation.Complete, EvidenceID: response.EvidenceId, EvidenceDigest: response.EvidenceDigest, ActivePodUIDs: append([]string{}, response.ActivePodUids...), UncertainNodeUIDs: append([]string{}, response.UncertainNodeUids...), Issues: append([]string{}, response.Observation.Issues...)}
	if result.Ref != scope.Ref || (response.Finding != accpb.ReleaseFinding_ALLOCATION_RELEASED && response.Finding != accpb.ReleaseFinding_ALLOCATION_STILL_PRESENT && response.Finding != accpb.ReleaseFinding_RELEASE_UNKNOWN) {
		return biz.GPUReleaseObservation{}, biz.ErrRuntimeConflict
	}
	if response.Observation.ObservedAt != "" {
		result.ObservedAt, err = time.Parse(time.RFC3339Nano, response.Observation.ObservedAt)
		if err != nil {
			return biz.GPUReleaseObservation{}, biz.ErrRuntimeConflict
		}
	}
	if response.Observation.ReceivedAt != "" {
		result.ReceivedAt, err = time.Parse(time.RFC3339Nano, response.Observation.ReceivedAt)
		if err != nil {
			return biz.GPUReleaseObservation{}, biz.ErrRuntimeConflict
		}
	}
	return result, nil
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

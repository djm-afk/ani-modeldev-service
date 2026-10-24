package accelerator_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	accpb "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	quotapb "github.com/zhangzhe-ctrl/ani-governance/api/quota/gen/go/quota/service/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/accelerator"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type releaseBoundary struct {
	accpb.UnimplementedAcceleratorUsageServiceServer
	call func(*accpb.ObserveReleaseRequest) (*accpb.ReleaseObservation, error)
}

func (s *releaseBoundary) ObserveRelease(_ context.Context, r *accpb.ObserveReleaseRequest) (*accpb.ReleaseObservation, error) {
	return s.call(r)
}

type quotaBoundary struct {
	quotapb.UnimplementedQuotaReleaseServiceServer
	call func(*quotapb.ReportResourceReleasedRequest) (*quotapb.ReportResourceReleasedResponse, error)
}

func (s *quotaBoundary) ReportResourceReleased(_ context.Context, r *quotapb.ReportResourceReleasedRequest) (*quotapb.ReportResourceReleasedResponse, error) {
	return s.call(r)
}

// Only the external ACC/Gov servers are boundary fixtures. The typed client,
// TLS authentication, finite request projection and response decoding are real.
func ownerBoundary(t *testing.T, dns, method string, register func(*grpc.Server), mutate func(*x509.Certificate)) accelerator.Config {
	t.Helper()
	certs := commandtls.NewForServer(t, dns)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certs.Server}, ClientCAs: certs.Roots, ClientAuth: tls.RequireAndVerifyClientCert})), grpc.UnaryInterceptor(func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		remote, ok := peer.FromContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "peer missing")
		}
		identity, ok := remote.AuthInfo.(credentials.TLSInfo)
		if !ok || len(identity.State.VerifiedChains) == 0 || len(identity.State.PeerCertificates) != 1 || !reflect.DeepEqual(identity.State.PeerCertificates[0].DNSNames, []string{accelerator.ClientDNSName}) || info.FullMethod != method {
			return nil, status.Error(codes.PermissionDenied, "owner method denied")
		}
		md, _ := metadata.FromIncomingContext(ctx)
		for _, key := range []string{"authorization", "x-ani-actor", "x-ani-actor-type", "x-ani-actor-id", "x-ani-request-id"} {
			if len(md.Get(key)) != 0 {
				return nil, status.Error(codes.PermissionDenied, "forwarded identity metadata")
			}
		}
		if method == accpb.AcceleratorUsageService_ObserveRelease_FullMethodName {
			original, ok := request.(*accpb.ObserveReleaseRequest)
			if !ok || original.Ref == nil || !reflect.DeepEqual(md.Get("x-ani-action"), []string{"ObserveRelease"}) || !reflect.DeepEqual(md.Get("x-ani-tenant-id"), []string{original.Ref.TenantId}) {
				return nil, status.Error(codes.PermissionDenied, "original owner scope context missing")
			}
		} else if len(md.Get("x-ani-tenant-id")) != 0 || len(md.Get("x-ani-action")) != 0 {
			return nil, status.Error(codes.PermissionDenied, "unexpected Gov release identity metadata")
		}
		return handler(ctx, request)
	}))
	register(server)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	files := certs.WriteServerFiles(t)
	client := certs.ClientCertificate(t, func(c *x509.Certificate) {
		c.DNSNames = []string{accelerator.ClientDNSName}
		if mutate != nil {
			mutate(c)
		}
	})
	private, err := x509.MarshalPKCS8PrivateKey(client.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	pair := filepath.Join(directory, "owner.pem")
	key := filepath.Join(directory, "owner.key")
	if os.WriteFile(pair, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: client.Certificate[0]}), 0600) != nil || os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600) != nil {
		t.Fatal("private synthetic owner TLS material")
	}
	return accelerator.Config{Endpoint: listener.Addr().String(), CAFile: files.CAFile, CertificateFile: pair, PrivateKeyFile: key, ServerName: dns, Timeout: 2 * time.Second}
}

func TestReleaseClientUsesExactOwnerTLSAndStableOriginalScope(t *testing.T) {
	var mu sync.Mutex
	var requests []*accpb.ObserveReleaseRequest
	badRef := false
	config := ownerBoundary(t, accelerator.AcceleratorDNSName, accpb.AcceleratorUsageService_ObserveRelease_FullMethodName, func(server *grpc.Server) {
		accpb.RegisterAcceleratorUsageServiceServer(server, &releaseBoundary{call: func(request *accpb.ObserveReleaseRequest) (*accpb.ReleaseObservation, error) {
			mu.Lock()
			defer mu.Unlock()
			requests = append(requests, proto.Clone(request).(*accpb.ObserveReleaseRequest))
			ref := proto.Clone(request.Ref).(*accpb.GpuUsageRef)
			if badRef {
				ref.TenantId = uuid.NewString()
			}
			return &accpb.ReleaseObservation{EvidenceId: uuid.NewString(), Ref: ref, Finding: accpb.ReleaseFinding_RELEASE_UNKNOWN, EvidenceDigest: strings.Repeat("a", 64), Observation: &accpb.Observation{Issues: []string{"SOURCE_UNKNOWN"}}}, nil
		}})
	}, nil)
	client, closeClient, err := accelerator.NewReleaseClient(config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient()
	scope := biz.GPUReleaseScope{Ref: biz.GPUReleaseRef{TenantID: uuid.NewString(), OwnerService: biz.GPUOwnerService, ResourceID: uuid.NewString(), CreateOperationID: uuid.NewString()}, CloseOperationID: uuid.NewString(), ClusterID: uuid.NewString(), SealedAt: time.Now().UTC().Add(-time.Second), Pods: []biz.GPUReleasePodRef{}}
	scope.Pods = []biz.GPUReleasePodRef{{ClusterID: scope.ClusterID, Namespace: "gpu-owner", Name: "original-trainer", UID: "actual-api-pod-uid", ContainerName: "node"}}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer forged", "x-ani-tenant-id", uuid.NewString(), "x-ani-actor", "forged"))
	for i := 0; i < 2; i++ {
		result, err := client.ObserveGPURelease(ctx, scope)
		if err != nil || result.Ref != scope.Ref || result.Finding != "RELEASE_UNKNOWN" || !result.ObservedAt.IsZero() || result.Complete {
			t.Fatal("unknown source facts or exact mTLS caller lost", err)
		}
	}
	mu.Lock()
	if len(requests) != 2 || requests[0].DeleteOperationId != scope.CloseOperationID || requests[1].DeleteOperationId != scope.CloseOperationID || requests[0].SealedAt != requests[1].SealedAt || requests[0].Pods[0].Uid != scope.Pods[0].UID || requests[0].Pods[0].ContainerName != "node" {
		t.Fatal("release retry changed original scope")
	}
	before := len(requests)
	mu.Unlock()
	bad := scope
	bad.CloseOperationID = scope.Ref.CreateOperationID
	if _, err := client.ObserveGPURelease(ctx, bad); err == nil {
		t.Fatal("create UUID accepted as close")
	}
	mu.Lock()
	if len(requests) != before {
		t.Fatal("invalid scope reached receiver")
	}
	badRef = true
	mu.Unlock()
	if _, err := client.ObserveGPURelease(ctx, scope); err == nil {
		t.Fatal("foreign response ref accepted")
	}
}

func TestQuotaClientSendsOnlyFourStableFieldsAndValidatesACK(t *testing.T) {
	var mu sync.Mutex
	var requests []*quotapb.ReportResourceReleasedRequest
	foreignACK := false
	config := ownerBoundary(t, accelerator.GovernanceDNSName, quotapb.QuotaReleaseService_ReportResourceReleased_FullMethodName, func(server *grpc.Server) {
		quotapb.RegisterQuotaReleaseServiceServer(server, &quotaBoundary{call: func(r *quotapb.ReportResourceReleasedRequest) (*quotapb.ReportResourceReleasedResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			requests = append(requests, proto.Clone(r).(*quotapb.ReportResourceReleasedRequest))
			resource := r.ResourceId
			if foreignACK {
				resource = uuid.NewString()
			}
			return &quotapb.ReportResourceReleasedResponse{ReleaseEventId: r.ReleaseEventId, CreateOperationId: r.CreateOperationId, ResourceId: resource, Status: quotapb.ResourceReleaseStatus_ALREADY_APPLIED}, nil
		}})
	}, nil)
	client, closeClient, err := accelerator.NewQuotaClient(config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient()
	event := biz.GPUResourceReleasedEvent{ReleaseEventID: uuid.NewString(), CreateOperationID: uuid.NewString(), ResourceID: uuid.NewString(), Reason: "ABORTED_CLEANED"}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "forged", "x-ani-tenant-id", uuid.NewString()))
	for i := 0; i < 2; i++ {
		ack, err := client.ReportGPUResourceReleased(ctx, event)
		if err != nil || ack.ReleaseEventID != event.ReleaseEventID || ack.Status != "ALREADY_APPLIED" {
			t.Fatal("four-field release ACK", err)
		}
	}
	mu.Lock()
	if len(requests) != 2 || !proto.Equal(requests[0], requests[1]) || requests[0].ProtoReflect().Descriptor().Fields().Len() != 4 || requests[0].Reason != quotapb.ReleaseReason_ABORTED_CLEANED || requests[0].CreateOperationId != event.CreateOperationID {
		t.Fatal("release retry changed original four-field request")
	}
	foreignACK = true
	mu.Unlock()
	if _, err := client.ReportGPUResourceReleased(ctx, event); err == nil {
		t.Fatal("foreign Gov ACK accepted")
	}
}

func TestOwnerClientRejectsUnregisteredCertificateAndServerAliases(t *testing.T) {
	for name, mutate := range map[string]func(*x509.Certificate){"foreign": func(c *x509.Certificate) { c.DNSNames = []string{"ani-resource-service"} }, "multiple": func(c *x509.Certificate) { c.DNSNames = append(c.DNSNames, "alias") }, "IP": func(c *x509.Certificate) { c.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")} }} {
		t.Run(name, func(t *testing.T) {
			config := ownerBoundary(t, accelerator.AcceleratorDNSName, accpb.AcceleratorUsageService_ObserveRelease_FullMethodName, func(*grpc.Server) {}, mutate)
			if _, closeClient, err := accelerator.NewReleaseClient(config); err == nil {
				closeClient()
				t.Fatal("unregistered owner certificate accepted")
			}
		})
	}
	var called atomic.Bool
	config := ownerBoundary(t, accelerator.AcceleratorDNSName, accpb.AcceleratorUsageService_ObserveRelease_FullMethodName, func(server *grpc.Server) {
		accpb.RegisterAcceleratorUsageServiceServer(server, &releaseBoundary{call: func(r *accpb.ObserveReleaseRequest) (*accpb.ReleaseObservation, error) {
			called.Store(true)
			return &accpb.ReleaseObservation{EvidenceId: uuid.NewString(), Ref: r.Ref, Finding: accpb.ReleaseFinding_RELEASE_UNKNOWN, EvidenceDigest: strings.Repeat("a", 64), Observation: &accpb.Observation{}}, nil
		}})
	}, nil)
	for _, name := range []string{"", "*.cluster.local", accelerator.ClientDNSName} {
		invalid := config
		invalid.ServerName = name
		if _, closeClient, err := accelerator.NewReleaseClient(invalid); err == nil {
			closeClient()
			t.Fatal("missing or wildcard server identity accepted")
		}
	}
	config.ServerName = "accelerator.alias"
	client, closeClient, err := accelerator.NewReleaseClient(config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient()
	scope := biz.GPUReleaseScope{Ref: biz.GPUReleaseRef{TenantID: uuid.NewString(), OwnerService: biz.GPUOwnerService, ResourceID: uuid.NewString(), CreateOperationID: uuid.NewString()}, CloseOperationID: uuid.NewString(), ClusterID: uuid.NewString(), SealedAt: time.Now().UTC()}
	if _, err := client.ObserveGPURelease(context.Background(), scope); err == nil || called.Load() {
		t.Fatal("configured wrong DNS name fell back to another server SAN")
	}
}

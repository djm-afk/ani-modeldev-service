// Package imageruntimefixture runs a real mTLS Resource ImageRuntime listener
// for ModelDev tests. It answers only the two narrow RPCs this service uses and
// never issues credentials: the Secret reference is a synthetic name derived by
// the same contract rule.
package imageruntimefixture

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	imagev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/image/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// ServerDNSName is the synthetic server identity the fixture presents. Callers
// must configure exactly this name, mirroring the registered production SAN.
const ServerDNSName = "ani-resource-image-runtime"

// ClientDNSName is the identity ResourceDev accepts. It matches the contract
// constant so a wrong client SAN fails the fixture handshake, not a mock.
const ClientDNSName = "ani-modeldev-service"

// Resolution is one frozen answer the fixture returns.
type Resolution struct {
	ImageID           string
	Scope             string
	RegistryAuthority string
	Digest            string
	Reference         string
}

// Fixture is a real TLS gRPC server plus the client material a caller needs.
type Fixture struct {
	imagev1.UnimplementedImageRuntimeServiceServer

	CAFile          string
	CertificateFile string
	PrivateKeyFile  string
	ServerName      string
	Endpoint        string

	mu          sync.Mutex
	resolutions map[string]Resolution
	requests    []*imagev1.ResolveImageForWorkloadRequest
	ensured     []*imagev1.EnsureTenantPullSecretRequest
	server      *grpc.Server
}

// DefaultResolution is a valid synthetic answer for one catalogue selection.
func DefaultResolution(imageID, scope, registry string) Resolution {
	digest := "sha256:" + strings.Repeat("e", 64)
	return Resolution{ImageID: imageID, Scope: scope, RegistryAuthority: registry, Digest: digest, Reference: registry + "/cpu/mlp@" + digest}
}

// New starts the listener and returns its client material. The server closes
// with the test.
func New(t testing.TB, resolutions ...Resolution) *Fixture {
	t.Helper()
	certificates := commandtls.NewForServer(t, ServerDNSName)
	files := certificates.WriteServerFiles(t)
	directory := t.TempDir()
	client := certificates.ClientCertificateFor(t, ClientDNSName)
	privateKey, err := x509.MarshalPKCS8PrivateKey(client.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]*pem.Block{
		filepath.Join(directory, "client.pem"): {Type: "CERTIFICATE", Bytes: client.Certificate[0]},
		filepath.Join(directory, "client.key"): {Type: "PRIVATE KEY", Bytes: privateKey},
	}
	for path, block := range values {
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
			t.Fatal("write synthetic image runtime client identity")
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &Fixture{
		CAFile: files.CAFile, CertificateFile: filepath.Join(directory, "client.pem"),
		PrivateKeyFile: filepath.Join(directory, "client.key"), ServerName: ServerDNSName,
		Endpoint: listener.Addr().String(), resolutions: map[string]Resolution{},
	}
	for _, resolution := range resolutions {
		fixture.resolutions[resolution.ImageID+"|"+resolution.Scope] = resolution
	}
	roots := x509.NewCertPool()
	ca, err := os.ReadFile(files.CAFile)
	if err != nil || !roots.AppendCertsFromPEM(ca) {
		t.Fatal("read synthetic image runtime CA")
	}
	fixture.server = grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    roots,
		Certificates: []tls.Certificate{certificates.Server},
	})))
	imagev1.RegisterImageRuntimeServiceServer(fixture.server, fixture)
	go func() { _ = fixture.server.Serve(listener) }()
	t.Cleanup(fixture.server.Stop)
	return fixture
}

// ResolveImageForWorkload answers only an explicitly registered selection and
// echoes the requested platform, so a caller that guesses cannot pass.
func (f *Fixture) ResolveImageForWorkload(_ context.Context, request *imagev1.ResolveImageForWorkloadRequest) (*imagev1.ResolveImageForWorkloadResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	scope := "tenant"
	if request.GetScope() == imagev1.ImageScope_IMAGE_SCOPE_PLATFORM {
		scope = "platform"
	}
	resolution, found := f.resolutions[request.GetImageId()+"|"+scope]
	if !found {
		return nil, os.ErrNotExist
	}
	return &imagev1.ResolveImageForWorkloadResponse{
		TenantId: request.GetTenantId(), ImageId: resolution.ImageID, Scope: request.GetScope(),
		ResolvedReference: resolution.Reference, Digest: resolution.Digest,
		SelectedPlatform: request.GetTargetPlatform(), RegistryAuthority: resolution.RegistryAuthority,
	}, nil
}

// EnsureTenantPullSecret derives the shared name with the contract rule and
// returns no credential material at all.
func (f *Fixture) EnsureTenantPullSecret(_ context.Context, request *imagev1.EnsureTenantPullSecretRequest) (*imagev1.EnsureTenantPullSecretResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, request)
	name, err := cpup01.PullSecretName(request.GetTenantId(), request.GetClusterId(), request.GetNamespace(), request.GetRegistryAuthority())
	if err != nil {
		return nil, os.ErrInvalid
	}
	return &imagev1.EnsureTenantPullSecretResponse{SecretRef: name, Generation: 1, RegistryAuthority: request.GetRegistryAuthority()}, nil
}

// GetTenantPullMaterial must never be reachable: no runtime identity is
// authorized for plaintext credentials.
func (f *Fixture) GetTenantPullMaterial(context.Context, *imagev1.GetTenantPullMaterialRequest) (*imagev1.GetTenantPullMaterialResponse, error) {
	return nil, os.ErrPermission
}

// Requests returns the resolutions this fixture was asked for.
func (f *Fixture) Requests() []*imagev1.ResolveImageForWorkloadRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*imagev1.ResolveImageForWorkloadRequest{}, f.requests...)
}

// Ensured returns every Ensure request, so a test can prove the frozen scope
// and that no second object or credential was requested.
func (f *Fixture) Ensured() []*imagev1.EnsureTenantPullSecretRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*imagev1.EnsureTenantPullSecretRequest{}, f.ensured...)
}

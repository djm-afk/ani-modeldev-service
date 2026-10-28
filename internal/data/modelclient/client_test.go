package modelclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func modelClientTestConfig(t *testing.T, mutate func(*x509.Certificate)) Config {
	c, _ := modelClientTestSetup(t, mutate)
	return c
}

func modelClientTestSetup(t *testing.T, mutate func(*x509.Certificate)) (Config, commandtls.Certificates) {
	t.Helper()
	certs := commandtls.NewForServer(t, "ani-model.ani.internal")
	files := certs.WriteServerFiles(t)
	certificate := certs.ClientCertificate(t, func(c *x509.Certificate) {
		c.DNSNames = []string{ClientDNSName}
		if mutate != nil {
			mutate(c)
		}
	})
	key, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key")
	if os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}), 0600) != nil || os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600) != nil {
		t.Fatal("synthetic certificate write failed")
	}
	return Config{Endpoint: "127.0.0.1:19090", CAFile: files.CAFile, ClientDNSName: ClientDNSName, CertificateFile: certFile, PrivateKeyFile: keyFile, ServerName: "ani-model.ani.internal", Actor: "modeldev:service", Timeout: time.Hour, ResolutionTimeout: 300 * time.Second}, certs
}

type modelPrepareTLSBoundary struct {
	modelv1.UnimplementedModelServiceServer
	calls  atomic.Int64
	tenant string
	t      *testing.T
}

func (s *modelPrepareTLSBoundary) PrepareTrainedArtifact(ctx context.Context, r *modelv1.PrepareTrainedArtifactRequest) (*modelv1.PrepareTrainedArtifactResponse, error) {
	s.calls.Add(1)
	remote, ok := peer.FromContext(ctx)
	if !ok {
		s.t.Error("actual TLS peer absent")
		return nil, status.Error(codes.Unauthenticated, "peer")
	}
	identity, ok := remote.AuthInfo.(credentials.TLSInfo)
	if !ok || identity.State.Version != tls.VersionTLS13 || len(identity.State.PeerCertificates) == 0 || !exactClientIdentity(identity.State.PeerCertificates[0], ClientDNSName) {
		s.t.Error("wrong actual ModelDev identity")
		return nil, status.Error(codes.Unauthenticated, "peer")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if r.TenantId != s.tenant || len(md.Get("x-ani-tenant-id")) != 1 || md.Get("x-ani-tenant-id")[0] != s.tenant || md.Get("x-ani-actor")[0] != "modeldev:service" || len(md.Get("authorization")) != 0 || len(md.Get("cookie")) != 0 {
		s.t.Error("actual wire retained spoofed metadata")
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 5*time.Second {
		s.t.Error("wire renewed original parent deadline")
	}
	return nil, status.Error(codes.Unavailable, "response lost after original handler received request")
}

func TestModelClientRealMTLSKeepsSingleUnknownWriteAndParentBudget(t *testing.T) {
	input, _, _ := artifactFixture()
	config, certs := modelClientTestSetup(t, nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler := &modelPrepareTLSBoundary{tenant: input.TenantID, t: t}
	s := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certs.Server}, ClientCAs: certs.Roots, ClientAuth: tls.RequireAndVerifyClientCert})))
	modelv1.RegisterModelServiceServer(s, handler)
	go func() { _ = s.Serve(listener) }()
	t.Cleanup(func() { s.Stop(); _ = listener.Close() })
	config.Endpoint = listener.Addr().String()
	client, closeClient, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeClient)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", "spoofed", "authorization", "borrowed", "cookie", "session"))
	if _, err := client.PrepareArtifact(ctx, input); status.Code(err) != codes.Unavailable || handler.calls.Load() != 1 {
		t.Fatal("actual TLS call failed or retried unknown write", err)
	}
}

func TestModelClientAllowsFinite3600sAndClosesOnce(t *testing.T) {
	client, closeClient, err := New(modelClientTestConfig(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	closeClient()
	closeClient()
	if client.connection.GetState() != connectivity.Shutdown {
		t.Fatal("shared Model connection was not closed")
	}
}

func TestModelClientRejectsUnregisteredOrBorrowedIdentityAndUnboundedTimeout(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Config)
		cert   func(*x509.Certificate)
	}{
		{"missing resolution budget", func(c *Config) { c.ResolutionTimeout = 0 }, nil},
		{"over24h", func(c *Config) { c.Timeout = 24*time.Hour + time.Second }, nil},
		{"old client alias", func(c *Config) { c.ClientDNSName = "ani-modeldev.ani.internal" }, nil},
		{"borrowed actor", func(c *Config) { c.Actor = "governance:user:42" }, nil},
		{"multiple SANs", nil, func(c *x509.Certificate) { c.DNSNames = append(c.DNSNames, "ani-governance") }},
		{"borrowed certificate", nil, func(c *x509.Certificate) { c.DNSNames = []string{"ani-governance"} }},
		{"server certificate usage", nil, func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth} }},
		{"expired certificate", nil, func(c *x509.Certificate) { c.NotAfter = time.Now().Add(-time.Second) }},
		{"missing numeric port", func(c *Config) { c.Endpoint = "ani-model.ani.internal" }, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := modelClientTestConfig(t, test.cert)
			if test.change != nil {
				test.change(&c)
			}
			client, closeClient, err := New(c)
			if err == nil {
				closeClient()
				t.Fatal("invalid identity/budget accepted")
			}
			if client != nil || closeClient != nil {
				t.Fatal("partially configured dependency escaped")
			}
		})
	}
}

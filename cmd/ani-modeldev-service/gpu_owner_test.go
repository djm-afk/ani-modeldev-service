package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/accelerator"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	"google.golang.org/protobuf/types/known/durationpb"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func gpuOwnerTestConfig(t *testing.T) *conf.ManagedRuntime {
	t.Helper()
	certs := commandtls.NewForServer(t, accelerator.GovernanceDNSName)
	server := certs.WriteServerFiles(t)
	client := certs.ClientCertificate(t, func(c *x509.Certificate) { c.DNSNames = []string{accelerator.ClientDNSName} })
	private, err := x509.MarshalPKCS8PrivateKey(client.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	_, ownerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	write := func(name string, data []byte) string {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	return &conf.ManagedRuntime{GpuOwner: &conf.ManagedGPUOwner{
		OwnerPrivateKeyFile: write("owner.key", []byte(base64.StdEncoding.EncodeToString(ownerPrivate))),
		Governance: &conf.OwnerMTLSConnection{
			Endpoint:        "127.0.0.1:19090",
			CaFile:          server.CAFile,
			CertificateFile: write("owner.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: client.Certificate[0]})),
			PrivateKeyFile:  write("owner-tls.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})),
			ServerName:      accelerator.GovernanceDNSName,
			Timeout:         durationpb.New(2 * time.Second),
		},
	}}
}

// The owner refund chain is assembled from Governance material alone. Binding
// an Accelerator connection is no longer expressible, so absence of that input
// cannot be compensated by any deployment configuration.
func TestGPUOwnerAssemblyDoesNotRequireACCReleaseConnection(t *testing.T) {
	kube, err := dynamic.NewForConfig(&rest.Config{Host: "https://127.0.0.1:6443"})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("governance-only", func(t *testing.T) {
		reconciler, signer, closeOwner, err := assembleGPUOwner(gpuOwnerTestConfig(t), kube, lifecycle.New(nil))
		if err != nil || reconciler == nil || signer == nil || closeOwner == nil {
			t.Fatalf("valid owner refund and independent Pod signer require no ACC release connection: %v", err)
		}
		closeOwner()
		closeOwner()
	})
	t.Run("missing-governance", func(t *testing.T) {
		config := gpuOwnerTestConfig(t)
		config.GpuOwner.Governance = nil
		if _, _, _, err := assembleGPUOwner(config, kube, lifecycle.New(nil)); err == nil {
			t.Fatal("owner refund assembled without its Governance reporter")
		}
	})
	t.Run("absent-gpu-owner", func(t *testing.T) {
		reconciler, signer, closeOwner, err := assembleGPUOwner(&conf.ManagedRuntime{}, kube, lifecycle.New(nil))
		if err != nil || reconciler != nil || signer != nil || closeOwner == nil {
			t.Fatalf("CPU-only runtime must stay independent of GPU owner inputs: %v", err)
		}
		closeOwner()
	})
}

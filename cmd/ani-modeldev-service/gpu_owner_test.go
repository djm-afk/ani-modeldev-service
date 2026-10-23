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

func TestGPUOwnerAssemblyDoesNotRequireACCReleaseConnection(t *testing.T) {
	for _, test := range []struct {
		name string
		acc  *conf.OwnerMTLSConnection
	}{
		{name: "absent"},
		{name: "unavailable", acc: &conf.OwnerMTLSConnection{Endpoint: "unavailable-acc", CaFile: "/missing/acc-ca.pem"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := gpuOwnerTestConfig(t)
			config.GpuOwner.Accelerator = test.acc
			kube, err := dynamic.NewForConfig(&rest.Config{Host: "https://127.0.0.1:6443"})
			if err != nil {
				t.Fatal(err)
			}
			reconciler, signer, closeOwner, err := assembleGPUOwner(config, kube, lifecycle.New(nil))
			if err != nil || reconciler == nil || signer == nil || closeOwner == nil {
				t.Fatalf("valid owner refund and independent Pod signer require no ACC release connection: %v", err)
			}
			closeOwner()
			closeOwner()
		})
	}
}

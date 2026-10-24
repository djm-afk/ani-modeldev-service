package main

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/finetuningmodel"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	"google.golang.org/protobuf/types/known/durationpb"
)

func modelConnectionTestConfig(t *testing.T) *conf.GovernanceCommand {
	t.Helper()
	certs := commandtls.NewForServer(t, "ani-model.ani.internal")
	server := certs.WriteServerFiles(t)
	client := certs.ClientCertificate(t, func(c *x509.Certificate) { c.DNSNames = []string{"ani-modeldev-service"} })
	key, err := x509.MarshalPKCS8PrivateKey(client.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certificatePath, keyPath := filepath.Join(directory, "client.crt"), filepath.Join(directory, "client.key")
	if os.WriteFile(certificatePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: client.Certificate[0]}), 0600) != nil || os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600) != nil {
		t.Fatal("synthetic Model identity write failed")
	}
	return &conf.GovernanceCommand{Model: &conf.ModelConnection{Endpoint: "127.0.0.1:19090", CaFile: server.CAFile, ClientDnsName: "ani-modeldev-service", CertificateFile: certificatePath, PrivateKeyFile: keyPath, ServerName: "ani-model.ani.internal", Actor: "modeldev:service", Timeout: durationpb.New(time.Hour), BaseModelStorageScope: &conf.BaseModelStorageScope{ConnectionId: "model-store-v1", Bucket: "ani-models", ApprovedPrefix: "original/models", CredentialReference: "model-base-read"}}}
}

func TestModelResourceFactorySharesExplicitLiveReaderAndOwnedCleanup(t *testing.T) {
	client, reader, closeResources, err := loadModelResources(modelConnectionTestConfig(t))
	if err != nil || client == nil || reader == nil || closeResources == nil {
		t.Fatal("complete live Model dependency rejected", err)
	}
	if _, ok := reader.(*finetuningmodel.LiveReader); !ok {
		t.Fatal("configured live Model selected a controlled substitute")
	}
	closeResources()
	closeResources()
}

func TestModelResourceFactoryKeepsAbsentDependencyUnavailable(t *testing.T) {
	for _, config := range []*conf.GovernanceCommand{nil, {}} {
		client, reader, closeResources, err := loadModelResources(config)
		if err != nil || client != nil || reader != nil || closeResources == nil {
			t.Fatal("absent Model manufactured a dependency", err)
		}
		closeResources()
		closeResources()
	}
}

func TestModelResourceFactoryRejectsAmbiguousIncompleteAndBorrowedMaterials(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*conf.GovernanceCommand)
	}{
		{"ambiguous controlled facts", func(c *conf.GovernanceCommand) { c.ControlledModelFacts = &conf.ControlledModelFacts{} }},
		{"missing scope", func(c *conf.GovernanceCommand) { c.Model.BaseModelStorageScope = nil }},
		{"wrong scope", func(c *conf.GovernanceCommand) { c.Model.BaseModelStorageScope.ApprovedPrefix = "../model" }},
		{"borrowed identity", func(c *conf.GovernanceCommand) { c.Model.ClientDnsName = "ani-governance" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := modelConnectionTestConfig(t)
			test.change(c)
			client, reader, closeResources, err := loadModelResources(c)
			if err == nil || client != nil || reader != nil {
				t.Fatal("invalid Model resources escaped factory")
			}
			closeResources()
		})
	}
}

func TestIntermediateCommandBuilderCannotOpenUnownedModelConnection(t *testing.T) {
	c := modelConnectionTestConfig(t)
	if _, err := buildCommandServerWithCapabilities(nil, c, nil, nil, nil, nil); err == nil {
		t.Fatal("intermediate builder silently created a client without cleanup")
	}
	if _, err := buildCommandServerWithCapabilitiesAndModelFacts(nil, c, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("live configuration accepted without shared read dependency")
	}
}

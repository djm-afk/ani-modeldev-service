//go:build cpu_mainflow

package submittest_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// The consumer sends a real user request through the existing BFF and starts
// the production Governance delivery worker. This bridge only shares synthetic
// environment references and observes the original committed command identity.
func awaitBusinessAdmission(t *testing.T, ctx context.Context, f *completeFixture, query *mainFlowQuery, repository biz.ExecutionRepository, selection cpup01.ComputeOrigin, intent cpup01.Intent) {
	t.Helper()
	path := os.Getenv("ANI_MODELDEV_MAINFLOW_STARTUP")
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if !filepath.IsAbs(path) || err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("business startup requires a new private directory")
	}
	materials := query.certificates.WriteServerFiles(t)
	privateKey, err := x509.MarshalPKCS8PrivateKey(query.certificates.Governance.PrivateKey)
	if err != nil {
		t.Fatal("business startup synthetic key unavailable")
	}
	clientCert, clientKey := filepath.Join(dir, "startup-client.pem"), filepath.Join(dir, "startup-client.key")
	for name, block := range map[string]*pem.Block{clientCert: {Type: "CERTIFICATE", Bytes: query.certificates.Governance.Certificate[0]}, clientKey: {Type: "PRIVATE KEY", Bytes: privateKey}} {
		file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal("cannot reserve startup TLS material")
		}
		_, writeErr := file.Write(pem.EncodeToMemory(block))
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("cannot write startup TLS material")
		}
	}
	raw, err := json.Marshal(map[string]any{"schema": "ani.modeldev.governance-main-flow-fixture.v3", "address": query.address, "tls": map[string]string{"ca_file": materials.CAFile, "cert_file": clientCert, "key_file": clientKey}, "resource_tenant_id": f.request.Admission.TenantID, "intent": intent, "compute_origin": selection})
	if err != nil {
		t.Fatal("business startup encoding failed")
	}
	// Publish only complete bytes; the independent BFF process may already be waiting.
	staging := path + ".pending"
	file, err := os.OpenFile(staging, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("business startup unavailable")
	}
	_, writeErr := file.Write(raw)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil || os.Rename(staging, path) != nil {
		t.Fatal("business startup write failed")
	}
	t.Log("BFF_CREATE_STARTUP_READY: real mTLS command listener and frozen fixture references")
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, "stop")); err == nil {
			t.Fatal("BFF_ACCEPTANCE_NOT_IMPLEMENTED: BFF consumer stopped before durable delivery")
		}
		encoded, err := os.ReadFile(filepath.Join(dir, "created.json"))
		if err == nil {
			var created struct {
				OperationID string `json:"operation_id"`
				ExecutionID string `json:"execution_id"`
			}
			if json.Unmarshal(encoded, &created) != nil || created.OperationID == "" || created.ExecutionID == "" {
				t.Fatal("BFF identity handoff invalid")
			}
			received, err := repository.Get(ctx, f.request.Admission.TenantID, created.ExecutionID)
			if err == nil {
				if received.OperationID != created.OperationID || received.Intent.Name != intent.Name || received.Snapshot.ComputeOrigin != selection {
					t.Fatal("BFF delivery differed from its accepted intent/selection")
				}
				f.request.Admission = received.Admission
				t.Log("BFF_MAIN_FLOW_ACCEPTED: real HTTP request -> Governance durable acceptance/worker -> ModelDev committed inbox; original identities retained")
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("BFF_ACCEPTANCE_NOT_IMPLEMENTED: no durable command from BFF worker")
		case <-ticker.C:
		}
	}
}

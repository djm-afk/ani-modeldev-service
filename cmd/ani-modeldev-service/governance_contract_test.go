//go:build governance_contract

package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	"google.golang.org/grpc"
)

// TestGovernanceContractProvider is selected explicitly by the cross-repository
// runner. The normal verification build excludes this fixture entirely.
func TestGovernanceContractProvider(t *testing.T) {
	directory, fixture, connection, stop := startGovernanceContractProvider(t)
	intent, err := contractpb.DecodeIntent(fixture.request.Intent)
	if err != nil {
		t.Fatal("GOVERNANCE_CONTRACT_PREFLIGHT: fixed fixture intent invalid")
	}
	handshake := governanceContractHandshake{Schema: "ani.modeldev.governance-admission-fixture.v2.1", Address: fixture.config.Server.Grpc.Addr, Intent: intent, AcceptedAt: fixture.request.AcceptedAt.AsTime()}
	handshake.TLS.CAFile = filepath.Join(directory, "ca.pem")
	handshake.TLS.CertFile = filepath.Join(directory, "governance.pem")
	handshake.TLS.KeyFile = filepath.Join(directory, "governance.key")
	handshake.Scope.ResourceTenantID, handshake.Scope.Actor = fixture.tenantID, "governance:user:42"
	handshake.IdentityRef = fixture.wantSnapshot.IdentityRef
	handshake.ComputeSelectionCanonical = string(fixture.request.ComputeSelectionCanonical)
	handshake.SelectionDigest = fixture.request.SelectionDigest
	raw, err := json.Marshal(handshake)
	if err != nil || len(raw) > 16384 {
		t.Fatal("GOVERNANCE_CONTRACT_PREFLIGHT: bounded handshake encoding failed")
	}
	staging := filepath.Join(directory, "handshake.pending")
	if err := os.WriteFile(staging, raw, 0600); err != nil {
		t.Fatal("GOVERNANCE_CONTRACT_PREFLIGHT: private handshake write failed")
	}
	if err := os.Rename(staging, filepath.Join(directory, "handshake.json")); err != nil {
		t.Fatal("GOVERNANCE_CONTRACT_PREFLIGHT: atomic handshake publication failed")
	}
	t.Log("GOVERNANCE_CONTRACT_PREFLIGHT PASS: real buildApp/mTLS, pinned files and independently recovered READY input; private handshake published")
	waitGovernanceContractStop(t, directory)
	_ = connection.Close()
	stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	observer := fixture.openPool()
	stored, err := input.New(observer).Get(ctx, fixture.tenantID, fixture.ready.Import.InputVersionID)
	if err != nil || !reflect.DeepEqual(stored, fixture.ready) {
		t.Fatal("GOVERNANCE_CONTRACT_BEHAVIOR: Prepare/Finalize changed the durable READY input")
	}
	var identities, executions, closes, submissions int
	err = observer.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM modeldev_execution_identities),
		(SELECT count(*) FROM modeldev_executions),
		(SELECT count(*) FROM modeldev_close_intents),
		(SELECT count(*) FROM modeldev_pipeline_dispatches)`).Scan(&identities, &executions, &closes, &submissions)
	if err != nil || identities != 0 || executions != 0 || closes != 0 || submissions != 0 {
		t.Fatal("GOVERNANCE_CONTRACT_BEHAVIOR: Prepare/Finalize wrote execution/close/submission facts or observer failed")
	}
	requireAdmissionMaterialPoolReleased(t, observer, fixture.applicationName)
	t.Log("GOVERNANCE_CONTRACT_BEHAVIOR PASS: READY unchanged, no execution-side facts, configured app stopped")
}

// Both explicitly selected contract providers use the same real app and
// private transport materials. Each invocation still owns a fresh PG schema,
// directory and stop signal; neither test relaxes the other's fact assertions.
func startGovernanceContractProvider(t *testing.T) (string, configuredAdmissionFixture, *grpc.ClientConn, func()) {
	t.Helper()
	parent := os.Getenv("CPU_P01_GOVERNANCE_CONTRACT_DIR")
	if !filepath.IsAbs(parent) {
		t.Fatal("GOVERNANCE_CONTRACT_PREFLIGHT: explicit private runner directory required")
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("GOVERNANCE_CONTRACT_PREFLIGHT: runner directory must be private and regular")
	}
	// The runner owns parent; this process creates and cleans only its fresh
	// child. A stale child fails rather than taking over old test materials.
	directory := filepath.Join(parent, "provider")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal("GOVERNANCE_CONTRACT_PREFLIGHT: fresh provider directory unavailable")
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error("GOVERNANCE_CONTRACT_CLEANUP: exact private fixture cleanup failed")
		}
	})
	fixture := prepareConfiguredAdmission(t)
	// Use the fixed production service identity the Governance client verifies.
	// Only this synthetic CA signs it.
	fixture.certificates.Server = fixture.certificates.ClientCertificate(t, func(certificate *x509.Certificate) {
		certificate.DNSNames = []string{commandtls.ServerDNSName}
		certificate.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	})
	serverFiles := fixture.certificates.WriteServerFiles(t)
	fixture.config.Command.ClientCaFile = serverFiles.CAFile
	fixture.config.Command.CertificateFile = serverFiles.CertificateFile
	fixture.config.Command.PrivateKeyFile = serverFiles.PrivateKeyFile
	connection, stop := startConfiguredAdmissionApp(t, fixture)
	assertProductionNotReadyWithoutBusinessAdapters(t, fixture.config.Server.Admin.Addr)
	ca, err := os.ReadFile(serverFiles.CAFile)
	if err != nil {
		t.Fatal("GOVERNANCE_CONTRACT_PREFLIGHT: synthetic CA unavailable")
	}
	key, err := x509.MarshalPKCS8PrivateKey(fixture.certificates.Governance.PrivateKey)
	if err != nil {
		t.Fatal("GOVERNANCE_CONTRACT_PREFLIGHT: synthetic client key unavailable")
	}
	for name, raw := range map[string][]byte{
		"ca.pem":         ca,
		"governance.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.certificates.Governance.Certificate[0]}),
		"governance.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}),
	} {
		if err := os.WriteFile(filepath.Join(directory, name), raw, 0600); err != nil {
			t.Fatal("GOVERNANCE_CONTRACT_PREFLIGHT: private TLS fixture write failed")
		}
	}
	return directory, fixture, connection, stop
}

type governanceContractHandshake struct {
	Schema  string `json:"schema"`
	Address string `json:"address"`
	TLS     struct {
		CAFile   string `json:"ca_file"`
		CertFile string `json:"cert_file"`
		KeyFile  string `json:"key_file"`
	} `json:"tls"`
	Scope struct {
		ResourceTenantID string `json:"resource_tenant_id"`
		Actor            string `json:"actor"`
	} `json:"scope"`
	Intent                    cpup01.Intent      `json:"intent"`
	IdentityRef               cpup01.IdentityRef `json:"identity_ref"`
	ComputeSelectionCanonical string             `json:"compute_selection_canonical"`
	SelectionDigest           string             `json:"selection_digest"`
	AcceptedAt                time.Time          `json:"accepted_at"`
}

func waitGovernanceContractStop(t *testing.T, directory string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("GOVERNANCE_CONTRACT_CLEANUP: consumer did not stop provider within five minutes")
		case <-ticker.C:
			info, err := os.Lstat(filepath.Join(directory, "stop"))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil || !info.Mode().IsRegular() || info.Size() != 0 || info.Mode().Perm() != 0600 {
				t.Fatal("GOVERNANCE_CONTRACT_CLEANUP: invalid private stop signal")
			}
			return
		}
	}
}

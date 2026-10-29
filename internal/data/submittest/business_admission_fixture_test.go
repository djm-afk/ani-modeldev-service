//go:build cpu_mainflow

package submittest_test

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/admissionfacts"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/catalogue"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
)

func prepareMainFlowAdmission(t *testing.T, ctx context.Context, f *completeFixture, pool *pgxpool.Pool, store *s3.Client) (*service.Admission, cpup01.ComputeOrigin, cpup01.Intent) {
	t.Helper()
	snapshot := f.request.Admission.Snapshot
	// These are the external fixture's trusted tenant/storage bindings, not an
	// ENV approval or a client-supplied scope. READY still requires real reads,
	// the production CSV verifier and the production input transaction path.
	inputScope := cpup01.StorageScope{StorageConnectionID: snapshot.Input.Object.StorageConnectionID, Bucket: snapshot.Input.Object.Bucket, ApprovedPrefix: path.Dir(snapshot.Input.Object.Key)}
	request := biz.InputImport{Format: "CSV",
		TenantID: f.request.Admission.TenantID, RequestID: uuid.NewString(), InputVersionID: snapshot.Input.InputVersionID,
		Actor: f.request.Admission.Actor, RequestedAt: time.Now().UTC().Truncate(time.Microsecond),
		Scope: inputScope, Object: snapshot.Input.Object,
	}
	inputs := input.New(pool)
	importer := biz.NewInputImporter(inputs, objectstore.NewVerifier(store, inputScope.StorageConnectionID), cpup01.ContentLimits{MaxContentBytes: 17179869184}, 1800*time.Second)
	if _, err := importer.ImportCSV(ctx, request); err != nil {
		t.Fatalf("BFF_ADMISSION_PREFLIGHT: actual fixed CSV import failed: %v", err)
	}
	ready, err := inputs.Get(ctx, request.TenantID, request.InputVersionID)
	if err != nil || ready.State != biz.InputStateReady || ready.Verification == nil || ready.Failure != nil || ready.Verification.ValidateFor(request) != nil {
		t.Fatalf("BFF_ADMISSION_PREFLIGHT: verified CSV was not persisted READY: %v", err)
	}

	material := conformance.MaterialV21()
	material.Implementation, material.Workspace, material.OutputContract = snapshot.Implementation, snapshot.Workspace, snapshot.OutputContract
	material.Program.ImageID, material.Program.ImageVersion, material.Program.ImageScope = snapshot.Program.ImageID, snapshot.Program.ImageVersion, snapshot.Program.ImageScope
	material.Program.ImageDigest, material.Program.Command = snapshot.Program.ImageDigest, snapshot.Program.Command
	rawMaterial, err := material.Canonical()
	if err != nil {
		t.Fatal("current fixture material invalid", err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, material.Implementation.ImplementationID+".cpu.json"), rawMaterial, 0600); err != nil {
		t.Fatal(err)
	}
	selection := snapshot.ComputeOrigin

	type evidence struct {
		Reference string `json:"reference"`
		SHA256    string `json:"sha256"`
	}
	environmentBytes, err := json.Marshal(snapshot.Environment)
	if err != nil {
		t.Fatalf("BFF_ADMISSION_PREFLIGHT: fixture environment encoding failed: %v", err)
	}
	// The two references explicitly describe boundary-fixture material, never
	// LIVE capability evidence. The facts reader consumes a pinned actual file.
	document := struct {
		SchemaVersion       string                            `json:"schema_version"`
		ResourceTenantID    string                            `json:"resource_tenant_id"`
		ImplementationID    string                            `json:"implementation_id"`
		Environment         cpup01.EnvironmentBindingSnapshot `json:"environment"`
		InputScope          cpup01.StorageScope               `json:"input_scope"`
		PublicationScope    cpup01.StorageScope               `json:"publication_scope"`
		Timing              cpup01.TimingConfiguration        `json:"timing"`
		Workspace           cpup01.WorkspaceContract          `json:"workspace"`
		InputFilePath       string                            `json:"input_file_path"`
		OutputDirectoryPath string                            `json:"output_directory_path"`
		EnvironmentEvidence evidence                          `json:"environment_evidence"`
		ApplicationEvidence evidence                          `json:"application_evidence"`
	}{
		SchemaVersion: "ani.modeldev.managed-admission-facts.v3", ResourceTenantID: request.TenantID,
		ImplementationID: snapshot.Implementation.ImplementationID,
		Environment:      snapshot.Environment, InputScope: inputScope, PublicationScope: snapshot.PublicationScope,
		Timing: cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: 3600, ExecutionTimeoutMaxSeconds: 7200, StepTimeoutSeconds: 900}, Workspace: snapshot.Workspace, InputFilePath: "/inputs/data.csv", OutputDirectoryPath: "/outputs",
		EnvironmentEvidence: evidence{Reference: "boundary-fixture:environment", SHA256: completeHash(environmentBytes)},
		ApplicationEvidence: evidence{Reference: "boundary-fixture:cpu-mlp-material", SHA256: completeHash(rawMaterial)},
	}
	rawFacts, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("BFF_ADMISSION_PREFLIGHT: facts encoding failed: %v", err)
	}
	factsPath := filepath.Join(t.TempDir(), "managed-admission-facts.json")
	if err := os.WriteFile(factsPath, rawFacts, 0600); err != nil {
		t.Fatalf("BFF_ADMISSION_PREFLIGHT: facts file write failed: %v", err)
	}
	facts, err := admissionfacts.Load(ctx, []admissionfacts.FileSource{{Path: factsPath, SHA256: completeHash(rawFacts)}})
	if err != nil {
		t.Fatalf("BFF_ADMISSION_PREFLIGHT: pinned facts file load failed: %v", err)
	}
	resolver := biz.NewAdmissionResolver(catalogue.NewReader(directory), inputs, facts, snapshot.Limits)
	t.Log("BFF_ADMISSION_READY: actual fixed CSV verified and persisted READY; canonical catalogue and pinned facts file loaded")
	return service.NewAdmission(resolver), selection, f.request.Admission.Intent
}

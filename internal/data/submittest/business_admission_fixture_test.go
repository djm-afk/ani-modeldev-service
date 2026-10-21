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

func prepareMainFlowAdmission(t *testing.T, ctx context.Context, f *completeFixture, pool *pgxpool.Pool, store *s3.Client) (*service.Admission, biz.AdmissionReleaseSelection, cpup01.Intent) {
	t.Helper()
	snapshot := f.request.Admission.Snapshot
	// These are the external fixture's trusted tenant/storage bindings, not an
	// ENV approval or a client-supplied scope. READY still requires real reads,
	// the production CSV verifier and the production input transaction path.
	inputScope := cpup01.StorageScope{StorageConnectionID: snapshot.Input.Object.StorageConnectionID, Bucket: snapshot.Input.Object.Bucket, ApprovedPrefix: path.Dir(snapshot.Input.Object.Key)}
	request := biz.InputImport{
		TenantID: f.request.Admission.TenantID, RequestID: uuid.NewString(), InputVersionID: snapshot.Input.InputVersionID,
		Actor: f.request.Admission.Actor, RequestedAt: time.Now().UTC().Truncate(time.Microsecond),
		Scope: inputScope, Object: snapshot.Input.Object,
	}
	inputs := input.New(pool)
	importer := biz.NewInputImporter(inputs, objectstore.NewVerifier(store, inputScope.StorageConnectionID, 32<<20))
	if _, err := importer.ImportCSV(ctx, request); err != nil {
		t.Fatalf("BFF_ADMISSION_PREFLIGHT: actual fixed CSV import failed: %v", err)
	}
	ready, err := inputs.Get(ctx, request.TenantID, request.InputVersionID)
	if err != nil || ready.State != biz.InputStateReady || ready.Verification == nil || ready.Failure != nil || ready.Verification.ValidateFor(request) != nil {
		t.Fatalf("BFF_ADMISSION_PREFLIGHT: verified CSV was not persisted READY: %v", err)
	}

	// Reuse the registered MLP argument template/defaults, but bind the file to
	// the actual image and Runtime used by this complete-flow fixture. The real
	// BFF resolver will produce the snapshot; this helper does not manufacture it.
	release := conformance.ReleaseV1()
	release.ReleaseID, release.PresetID = snapshot.Release.ReleaseID, snapshot.Release.PresetID
	release.Kind, release.DeliveryMode = snapshot.Kind, snapshot.DeliveryMode
	release.PipelineID, release.PipelineVersionID = snapshot.Implementation.PipelineID, snapshot.Implementation.PipelineVersionID
	release.PipelineIRSHA256, release.Runtime = snapshot.Implementation.PipelineIRSHA256, snapshot.RuntimePlan.Reference()
	release.Program.ImageVersionID, release.Program.ImageDigest = snapshot.Program.ImageVersionID, f.image
	release.Program.Command = snapshot.Program.Command
	// Only this registered test-fixture recipe is selectable here. The user
	// Intent cannot provide a command, an argv template or a recipe token.
	if args := snapshot.Program.ResolvedArgs; len(args) >= 2 && args[len(args)-2] == "--recipe" && args[len(args)-1] == "slow-stop" {
		release.Program.ArgsTemplate = append(release.Program.ArgsTemplate, cpup01.ReleaseArgument{Literal: "--recipe"}, cpup01.ReleaseArgument{Literal: "slow-stop"})
	}
	release.Resources, release.Workspace, release.OutputContract = snapshot.Resources, snapshot.Workspace, snapshot.OutputContract
	release.ExecutionTimeoutSeconds = uint32(time.Hour / time.Second)
	rawRelease, err := json.Marshal(release)
	if err != nil {
		t.Fatalf("BFF_ADMISSION_PREFLIGHT: release encoding failed: %v", err)
	}
	_, digest, err := cpup01.ParseRelease(rawRelease)
	if err != nil {
		t.Fatalf("BFF_ADMISSION_PREFLIGHT: release is not canonical: %v", err)
	}
	directory := t.TempDir()
	imported, err := catalogue.ImportRelease(ctx, directory, rawRelease, digest)
	if err != nil {
		t.Fatalf("BFF_ADMISSION_PREFLIGHT: immutable catalogue import failed: %v", err)
	}
	selection := biz.AdmissionReleaseSelection{ReleaseID: imported.ReleaseID, ReleaseDigest: imported.Digest, BindingGeneration: 1}

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
		ReleaseID           string                            `json:"release_id"`
		ReleaseDigest       string                            `json:"release_digest"`
		Environment         cpup01.EnvironmentBindingSnapshot `json:"environment"`
		InputScope          cpup01.StorageScope               `json:"input_scope"`
		PublicationScope    cpup01.StorageScope               `json:"publication_scope"`
		Runtime             cpup01.RuntimeRef                 `json:"runtime"`
		Workspace           cpup01.WorkspaceContract          `json:"workspace"`
		InputFilePath       string                            `json:"input_file_path"`
		OutputDirectoryPath string                            `json:"output_directory_path"`
		EnvironmentEvidence evidence                          `json:"environment_evidence"`
		ApplicationEvidence evidence                          `json:"application_evidence"`
	}{
		SchemaVersion: "ani.modeldev.managed-admission-facts.v1", ResourceTenantID: request.TenantID,
		ReleaseID: selection.ReleaseID, ReleaseDigest: selection.ReleaseDigest,
		Environment: snapshot.Environment, InputScope: inputScope, PublicationScope: snapshot.PublicationScope,
		Runtime: release.Runtime, Workspace: release.Workspace, InputFilePath: "/inputs/data.csv", OutputDirectoryPath: "/outputs",
		EnvironmentEvidence: evidence{Reference: "boundary-fixture:environment", SHA256: completeHash(environmentBytes)},
		ApplicationEvidence: evidence{Reference: "boundary-fixture:cpu-mlp-release", SHA256: imported.Digest},
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
	resolver := biz.NewManagedAdmissionResolver(catalogue.NewReader(directory), inputs, facts)
	t.Log("BFF_ADMISSION_READY: actual fixed CSV verified and persisted READY; canonical catalogue and pinned facts file loaded")
	return service.NewAdmission(resolver), selection, f.request.Admission.Intent
}

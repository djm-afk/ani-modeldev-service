package resolutiontest

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/admissionfacts"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/catalogue"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type resolutionFixture struct {
	ctx        context.Context
	openPool   func() *pgxpool.Pool
	directory  string
	material   cpup01.ImplementationMaterial
	imported   biz.InputImport
	ready      biz.InputVersion
	request    biz.PrepareAdmissionInput
	facts      biz.TenantAdmissionFacts
	resolver   *biz.AdmissionResolver
	materials  *countedMaterials
	inputs     *countedInputs
	factReader *countedFacts
}
type countedMaterials struct {
	reader biz.AdmissionMaterialReader
	reads  int
	err    error
}

func (r *countedMaterials) ReadImplementationMaterial(ctx context.Context, id, mode string) (cpup01.ImplementationMaterial, error) {
	r.reads++
	if r.err != nil {
		return cpup01.ImplementationMaterial{}, r.err
	}
	return r.reader.ReadImplementationMaterial(ctx, id, mode)
}

type countedInputs struct {
	reader biz.AdmissionInputReader
	reads  int
}

func (r *countedInputs) Get(ctx context.Context, tenant, id string) (biz.InputVersion, error) {
	r.reads++
	return r.reader.Get(ctx, tenant, id)
}

type countedFacts struct {
	reader   biz.AdmissionFactsReader
	reads    int
	override *biz.TenantAdmissionFacts
	err      error
}

func (r *countedFacts) ReadAdmissionFacts(ctx context.Context, tenant, id string) (biz.TenantAdmissionFacts, error) {
	r.reads++
	if r.err != nil {
		return biz.TenantAdmissionFacts{}, r.err
	}
	if r.override != nil {
		return *r.override, nil
	}
	return r.reader.ReadAdmissionFacts(ctx, tenant, id)
}

func prepareResolution(t *testing.T) resolutionFixture {
	t.Helper()
	openPool := postgres.Prepare(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	snapshot := conformance.SnapshotV1()
	intent := conformance.IntentV1()
	lr := "0.0200"
	epochs, batch := int64(5), int64(37)
	seed := uint32(7)
	intent.MLP = &cpup01.MLPParameters{LearningRate: &lr, Epochs: &epochs, BatchSize: &batch, Seed: &seed}
	_, hash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	identity := snapshot.IdentityRef
	identity.Actor = "governance:user:42"
	identity.IntentHash = hash
	directory := t.TempDir()
	material := conformance.MaterialV21()
	writeMaterial(t, directory, material, "cpu")
	imported := biz.InputImport{Format: "CSV", TenantID: identity.TenantID, RequestID: "12345678-1234-4234-8234-123456789abc", InputVersionID: intent.DatasetVersionID, Actor: identity.Actor, RequestedAt: snapshot.Timing.AcceptedAt.Add(-time.Minute), Scope: cpup01.StorageScope{StorageConnectionID: snapshot.Input.Object.StorageConnectionID, Bucket: snapshot.Input.Object.Bucket, ApprovedPrefix: "fixed/input"}, Object: snapshot.Input.Object}
	repository := input.New(openPool())
	if _, err := repository.FreezeImport(ctx, imported); err != nil {
		t.Fatal("READY preflight freeze failed", err)
	}
	ready, err := repository.RecordVerifiedCSV(ctx, imported, biz.VerifiedCSV{VerifiedObject: biz.VerifiedObject{Object: imported.Object, VerifiedAt: imported.RequestedAt.Add(time.Second)}, SchemaVersion: "ani.cpu.csv.v1", RowCount: 1027, FeatureCount: 16, PreparedDataset: cpup01.PreparedDatasetFacts{Format: "CPU_CSV", TransformID: "csv-identity-v3", SizeBytes: imported.Object.SizeBytes, SHA256: imported.Object.SHA256, RowCount: 1027}})
	if err != nil {
		t.Fatal("READY preflight proof failed", err)
	}
	facts := biz.TenantAdmissionFacts{TenantID: identity.TenantID, Environment: snapshot.Environment, PublicationScope: snapshot.PublicationScope, InputScope: imported.Scope, Workspace: material.Workspace, InputFilePath: "/inputs/data.csv", OutputDirectoryPath: "/outputs", Timing: cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: 3600, ExecutionTimeoutMaxSeconds: 7200, StepTimeoutSeconds: 900}}
	f := resolutionFixture{ctx: ctx, openPool: openPool, directory: directory, material: material, imported: imported, ready: ready, facts: facts, request: biz.PrepareAdmissionInput{IdentityRef: identity, Intent: intent, ComputeOrigin: snapshot.ComputeOrigin, AcceptedAt: time.Now().UTC().Truncate(time.Microsecond)}}
	reader, err := admissionfacts.Load(ctx, []admissionfacts.FileSource{writeManagedFactsFixture(t, f)})
	if err != nil {
		t.Fatal("facts preflight", err)
	}
	f.materials = &countedMaterials{reader: catalogue.NewReader(directory)}
	f.inputs = &countedInputs{reader: input.New(openPool())}
	f.factReader = &countedFacts{reader: reader}
	f.resolver = biz.NewAdmissionResolver(f.materials, f.inputs, f.factReader, cpup01.ContentLimits{MaxContentBytes: 17179869184})
	recovered, err := f.inputs.reader.Get(ctx, imported.TenantID, imported.InputVersionID)
	if err != nil || !reflect.DeepEqual(ready, recovered) {
		t.Fatal("READY unavailable across new PG connection", err)
	}
	return f
}
func writeMaterial(t *testing.T, directory string, m cpup01.ImplementationMaterial, mode string) {
	t.Helper()
	raw, err := m.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, m.Implementation.ImplementationID+"."+mode+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func requireNoResolvedExecution(t *testing.T, f resolutionFixture) {
	t.Helper()
	var executions, identities, closes, dispatches int
	err := f.openPool().QueryRow(f.ctx, `SELECT (SELECT count(*) FROM modeldev_executions),(SELECT count(*) FROM modeldev_execution_identities),(SELECT count(*) FROM modeldev_close_intents),(SELECT count(*) FROM modeldev_pipeline_dispatches)`).Scan(&executions, &identities, &closes, &dispatches)
	if err != nil || executions != 0 || identities != 0 || closes != 0 || dispatches != 0 {
		t.Fatalf("read-only compile wrote durable execution facts: %d/%d/%d/%d %v", executions, identities, closes, dispatches, err)
	}
	ready, err := input.New(f.openPool()).Get(f.ctx, f.imported.TenantID, f.imported.InputVersionID)
	if err != nil || !reflect.DeepEqual(ready, f.ready) {
		t.Fatal("compile changed READY input", err)
	}
}
func cloneCandidate(t *testing.T, c cpup01.PreparedCandidate) cpup01.PreparedCandidate {
	t.Helper()
	raw, e := c.Canonical()
	if e != nil {
		t.Fatal(e)
	}
	hash, _ := c.Digest()
	v, e := cpup01.DecodePreparedCandidate(raw, hash)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func rehashRequest(r *biz.PrepareAdmissionInput) {
	_, r.IdentityRef.IntentHash, _ = cpup01.CanonicalIntent(r.Intent)
}
func TestPrepareFinalizeFreezeCompleteCandidateWithoutWritesOrSecondReads(t *testing.T) {
	f := prepareResolution(t)
	before, originalHash, _ := cpup01.CanonicalIntent(f.request.Intent)
	candidate, err := f.resolver.Prepare(f.ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	if f.materials.reads != 1 || f.inputs.reads != 1 || f.factReader.reads != 1 || candidate.Snapshot.RuntimePlan.Name != "" {
		t.Fatal("Prepare did not make exactly one observation")
	}
	// All provider/configuration changes after Prepare must be irrelevant.
	f.materials.err = biz.ErrPersistence
	f.factReader.err = biz.ErrPersistence
	resolved, err := f.resolver.Finalize(f.ctx, candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.materials.reads != 1 || f.inputs.reads != 1 || f.factReader.reads != 1 {
		t.Fatal("Finalize read new defaults/facts")
	}
	s := resolved.Snapshot
	if s.RuntimePlan.Name != cpup01.RuntimeName(s.Environment.NamespaceUID, s.IdentityRef.ExecutionID) || s.Timing.StepTimeoutSeconds != 900 || !s.DeadlineAt.Equal(f.request.AcceptedAt.Add(time.Hour)) || len(s.Program.ResolvedParameters) != 4 || s.Input.RowCount != 1027 {
		t.Fatal("incomplete plan")
	}
	manifest, err := cpup01.BuildDatasetManifest(cpup01.InputRef{InputVersionID: f.imported.InputVersionID, Format: f.imported.Format, Object: f.imported.Object, RowCount: f.ready.Verification.RowCount, PreparedDataset: f.ready.Verification.PreparedDataset})
	if err != nil {
		t.Fatal(err)
	}
	manifestSHA, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"--dataset-manifest", "/inputs/dataset-manifest.json", "--expected-dataset-manifest-sha256", manifestSHA, "--work-root", "/workspace/work/train", "--output", "/outputs", "--learning-rate", "0.02", "--epochs", "5", "--batch-size", "37", "--seed", "7"}
	if !reflect.DeepEqual(s.Program.ResolvedArgs, expected) {
		t.Fatalf("typed parameters did not enter argv: %v", s.Program.ResolvedArgs)
	}
	spec, _ := s.Digest()
	if spec != resolved.SpecHash {
		t.Fatal("snapshot hash mismatch")
	}
	after, hash, _ := cpup01.CanonicalIntent(f.request.Intent)
	if hash != originalHash || !bytes.Equal(before, after) || *f.request.Intent.MLP.LearningRate != "0.0200" {
		t.Fatal("caller intent mutated")
	}
	s.Program.Command[0] = "consumer-change"
	*s.Input.Object.VersionID = "consumer-change"
	s.OutputContract.RequiredFiles[0].RelativePath = "consumer-change"
	raw, _ := candidate.Canonical()
	if bytes.Contains(raw, []byte("consumer-change")) {
		t.Fatal("Finalize aliased caller candidate")
	}
	again, err := f.resolver.Finalize(f.ctx, candidate, nil)
	if err != nil || again.SpecHash != resolved.SpecHash {
		t.Fatal("fixed retry changed plan", err)
	}
	requireNoResolvedExecution(t, f)
	var runtime map[string]any
	if json.Unmarshal(again.Snapshot.RuntimePlan.ExpectedSpec, &runtime) != nil || runtime["template"] == nil {
		t.Fatal("Runtime plan omitted full template")
	}
}

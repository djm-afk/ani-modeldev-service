package admissionfacts_test

import (
	"context"
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	projection "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/admissionfacts"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/catalogue"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
	envfixture "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/tenantenvironment"
	"strings"
	"testing"
	"time"
)

type boundary struct {
	binding         projection.Binding
	err             error
	verifyErr       error
	reads, verifies int
	materials       biz.AdmissionMaterialReader
}

func (b *boundary) ReadReadyBinding(_ context.Context, tenant, cluster string) (projection.Binding, error) {
	b.reads++
	if tenant != b.binding.TenantID || cluster != b.binding.Environment.ClusterID {
		return projection.Binding{}, biz.ErrAdmissionEnvironmentNotReady
	}
	return b.binding, b.err
}
func (b *boundary) VerifyBinding(_ context.Context, tenant string, e cpup01.EnvironmentBindingSnapshot) error {
	b.verifies++
	if tenant != b.binding.TenantID || e.NamespaceUID != b.binding.Environment.NamespaceUID {
		return biz.ErrAdmissionEnvironmentNotReady
	}
	return b.verifyErr
}
func (b *boundary) ReadImplementationMaterial(_ context.Context, id, mode string) (cpup01.ImplementationMaterial, error) {
	return b.materials.ReadImplementationMaterial(context.Background(), id, mode)
}
func dynamicFixture(t *testing.T) *boundary {
	t.Helper()
	s := conformance.SnapshotV1()
	now := time.Now().UTC().Truncate(time.Microsecond)
	s.DeadlineAt = now.Add(time.Hour)
	a := admissionfixture.Bind(t, biz.Admission{TenantID: s.IdentityRef.TenantID, Actor: s.IdentityRef.Actor, OperationID: s.IdentityRef.OperationID, ExecutionID: s.IdentityRef.ExecutionID, Intent: conformance.IntentV1(), Snapshot: s, AcceptedAt: now})
	pool := postgres.Prepare(t)()
	f := envfixture.NewFixture(t, a)
	a = f.Ensure(t, pool, a)
	binding, err := tenantenvironment.NewRepository(pool).ReadReadyBinding(context.Background(), a.TenantID, a.Snapshot.Environment.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	return &boundary{binding: binding, materials: catalogue.NewReader(f.MaterialsDirectory)}
}

func TestDynamicFactsReadsPublishedEnvironmentAndReverifiesOriginalIdentity(t *testing.T) {
	b := dynamicFixture(t)
	r, err := admissionfacts.New(b, b, b, b.binding.Environment.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		facts, err := r.ReadAdmissionFacts(context.Background(), b.binding.TenantID, "builtin.mlp")
		if err != nil || facts.Environment.NamespaceUID != b.binding.Environment.NamespaceUID || facts.PublicationScope.ApprovedPrefix != "executions/"+b.binding.TenantID {
			t.Fatalf("facts: %+v %v", facts, err)
		}
	}
	if b.reads != 2 || b.verifies != 2 {
		t.Fatal("admission reused stale environment identity")
	}
	b.verifyErr = errors.New("namespace recreated")
	if _, err = r.ReadAdmissionFacts(context.Background(), b.binding.TenantID, "builtin.mlp"); err == nil {
		t.Fatal("replaced namespace accepted")
	}
}
func TestDynamicFactsDoesNotProvisionMissingOrMismatchedMaterials(t *testing.T) {
	b := dynamicFixture(t)
	b.err = biz.ErrAdmissionEnvironmentNotReady
	r, _ := admissionfacts.New(b, b, b, b.binding.Environment.ClusterID)
	if _, err := r.ReadAdmissionFacts(context.Background(), b.binding.TenantID, "builtin.mlp"); !errors.Is(err, biz.ErrAdmissionEnvironmentNotReady) || b.verifies != 0 {
		t.Fatal("missing environment passed")
	}
	b.err = nil
	b.binding.Profiles[0].MaterialSHA256 = strings.Repeat("b", 64)
	if _, err := r.ReadAdmissionFacts(context.Background(), b.binding.TenantID, "builtin.mlp"); err == nil || b.verifies != 0 {
		t.Fatal("unpublished material accepted")
	}
}

func (b *boundary) Get(_ context.Context, tenant, id string) (biz.InputVersion, error) {
	s := conformance.SnapshotV1()
	now := time.Now().UTC().Truncate(time.Microsecond)
	request := biz.InputImport{Format: "CSV", TenantID: tenant, RequestID: "88888888-8888-4888-8888-888888888888", InputVersionID: id, Actor: s.IdentityRef.Actor, RequestedAt: now.Add(-time.Minute), Scope: b.binding.InputScope, Object: s.Input.Object}
	return biz.InputVersion{Import: request, State: biz.InputStateReady, Verification: &biz.VerifiedCSV{VerifiedObject: biz.VerifiedObject{Object: s.Input.Object, VerifiedAt: now}, SchemaVersion: s.Input.SchemaVersion, RowCount: s.Input.RowCount, FeatureCount: s.Input.FeatureCount, PreparedDataset: s.Input.PreparedDataset}}, nil
}
func (b *boundary) ResolveTrainingImage(_ context.Context, tenant, image, scope string, platform cpup01.TrainingPlatform) (biz.TrainingImageResolution, error) {
	s := conformance.SnapshotV1()
	return biz.TrainingImageResolution{ImageID: image, Scope: scope, ImageVersion: s.Program.ImageVersion, Reference: s.Program.ImageDigest, Digest: "sha256:" + strings.Repeat("e", 64), RegistryAuthority: s.TrainingImage.RegistryAuthority, Platform: platform}, nil
}
func TestPrepareFinalizeConsumesDynamicEnvironmentAndFrozenOwner(t *testing.T) {
	b := dynamicFixture(t)
	facts, err := admissionfacts.New(b, b, b, b.binding.Environment.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	s := conformance.SnapshotV1()
	resolver := biz.NewAdmissionResolver(b, b, facts, b, s.Limits)
	candidate, err := resolver.Prepare(context.Background(), biz.PrepareAdmissionInput{IdentityRef: s.IdentityRef, Intent: conformance.IntentV1(), ComputeOrigin: s.ComputeOrigin, AcceptedAt: time.Now().UTC().Truncate(time.Microsecond)})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Snapshot.DispatchOwner.PipelineRoot != b.binding.DispatchOwner.PipelineRoot+"/"+s.IdentityRef.ExecutionID || candidate.Snapshot.Environment.NamespaceUID != b.binding.Environment.NamespaceUID {
		t.Fatal("execution owner not frozen from published environment")
	}
	final, err := resolver.Finalize(context.Background(), candidate, nil)
	if err != nil || final.SpecHash == "" {
		t.Fatalf("dynamic environment vertical admission: %v", err)
	}
	b.verifyErr = biz.EnvironmentError("TENANT_NAMESPACE_UID_CHANGED")
	if _, err = resolver.Finalize(context.Background(), candidate, nil); err == nil {
		t.Fatal("Finalize reused stale namespace identity")
	}
}

func TestSameReadyEnvironmentResolvesBothFixedImplementationPaths(t *testing.T) {
	b := dynamicFixture(t)
	r, err := admissionfacts.New(b, b, b, b.binding.Environment.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	mlp, err := r.ReadAdmissionFacts(context.Background(), b.binding.TenantID, "builtin.mlp")
	if err != nil {
		t.Fatal(err)
	}
	fine, err := r.ReadAdmissionFacts(context.Background(), b.binding.TenantID, "builtin.sft_lora")
	if err != nil {
		t.Fatal(err)
	}
	if mlp.Environment != fine.Environment || mlp.InputFilePath != "/inputs/data.csv" || mlp.OutputDirectoryPath != "/outputs" || fine.InputFilePath != "/workspace/inputs/data/finetune_data.jsonl" || fine.OutputDirectoryPath != "/workspace/training" {
		t.Fatal("one published environment must support both fixed implementations")
	}
}

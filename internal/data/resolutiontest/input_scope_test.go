package resolutiontest

import (
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/admissionfacts"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/catalogue"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	envrepo "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	envfixture "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/tenantenvironment"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPrepareScopesSameInputUUIDByAuthenticatedTenant(t *testing.T) {
	f := prepareResolution(t)
	r := f.request
	r.IdentityRef.TenantID = "99999999-9999-4999-8999-999999999999"
	snapshot := conformance.SnapshotV1()
	admission := admissionfixture.Bind(t, biz.Admission{TenantID: r.IdentityRef.TenantID, Actor: r.IdentityRef.Actor, OperationID: r.IdentityRef.OperationID, ExecutionID: r.IdentityRef.ExecutionID, Intent: r.Intent, AcceptedAt: snapshot.Timing.AcceptedAt, Snapshot: snapshot})
	tenantB := envfixture.NewFixture(t, admission)
	tenantB.Ensure(t, f.openPool(), admission)
	materialB := catalogue.NewReader(tenantB.MaterialsDirectory)
	readerB, err := admissionfacts.New(envrepo.NewRepository(f.openPool()), tenantB.Platform, materialB, tenantB.Config.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := readerB.ReadAdmissionFacts(f.ctx, r.IdentityRef.TenantID, r.Intent.ImplementationID)
	if err != nil {
		t.Fatal(err)
	}
	originalReader, originalMaterials := f.factReader.reader, f.materials.reader
	f.factReader.reader, f.materials.reader = readerB, materialB
	if _, e := f.resolver.Prepare(f.ctx, r); !errors.Is(e, biz.ErrInputNotFound) {
		t.Fatal("foreign input existence disclosed", e)
	}
	other := f.imported
	other.TenantID = r.IdentityRef.TenantID
	other.RequestID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	other.Scope = facts.InputScope
	other.Object.Key = facts.InputScope.ApprovedPrefix + "/data.csv"
	other.Object.SHA256 = strings.Repeat("e", 64)
	version := "other-fixed-version"
	other.Object.VersionID = &version
	repository := input.New(f.openPool())
	if _, e := repository.FreezeImport(f.ctx, other); e != nil {
		t.Fatal(e)
	}
	if _, e := repository.RecordVerifiedCSV(f.ctx, other, biz.VerifiedCSV{VerifiedObject: biz.VerifiedObject{Object: other.Object, VerifiedAt: other.RequestedAt.Add(time.Second)}, SchemaVersion: "ani.cpu.csv.v1", RowCount: 19, FeatureCount: 16, PreparedDataset: cpup01.PreparedDatasetFacts{Format: "CPU_CSV", TransformID: "csv-identity-v3", SizeBytes: other.Object.SizeBytes, SHA256: other.Object.SHA256, RowCount: 19}}); e != nil {
		t.Fatal(e)
	}
	candidate, e := f.resolver.Prepare(f.ctx, r)
	if e != nil || !reflect.DeepEqual(candidate.Snapshot.Input.Object, other.Object) || candidate.Snapshot.Input.RowCount != 19 {
		t.Fatal("tenant B did not compile its original bytes", e)
	}
	f.factReader.reader, f.materials.reader = originalReader, originalMaterials
	original, e := f.resolver.Prepare(f.ctx, f.request)
	if e != nil || !reflect.DeepEqual(original.Snapshot.Input.Object, f.imported.Object) {
		t.Fatal("tenant A mixed in tenant B facts", e)
	}
	requireNoResolvedExecution(t, f)
}
func TestPrepareRejectsPersistedNonReadyProofStates(t *testing.T) {
	f := prepareResolution(t)
	for n, failure := range []biz.InputFailureCode{"", biz.InputFailureSourceUnavailable, biz.InputFailureContentRejected} {
		r := f.imported
		r.InputVersionID = []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "cccccccc-cccc-4ccc-8ccc-cccccccccccc"}[n]
		r.RequestID = r.InputVersionID
		r.Object.Key = f.facts.InputScope.ApprovedPrefix + "/nonready-" + r.InputVersionID + ".csv"
		repository := input.New(f.openPool())
		v, e := repository.FreezeImport(f.ctx, r)
		if e != nil {
			t.Fatal(e)
		}
		if failure != "" {
			v, e = repository.RecordValidationFailure(f.ctx, r, biz.InputValidationFailure{Code: failure, ObservedAt: r.RequestedAt.Add(time.Second)})
			if e != nil {
				t.Fatal(e)
			}
		}
		request := f.request
		request.Intent.DatasetVersionID = r.InputVersionID
		rehashRequest(&request)
		if _, e := f.resolver.Prepare(f.ctx, request); !errors.Is(e, biz.ErrAdmissionInputNotReady) {
			t.Fatal("nonREADY generated candidate", e)
		}
		stored, e := input.New(f.openPool()).Get(f.ctx, r.TenantID, r.InputVersionID)
		if e != nil || !reflect.DeepEqual(stored, v) {
			t.Fatal("rejected prepare mutated input")
		}
	}
	requireNoResolvedExecution(t, f)
}

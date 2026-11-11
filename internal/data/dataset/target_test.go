package dataset_test

import (
	"context"
	"errors"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/dataset"
)

const (
	targetTenant  = "11111111-1111-4111-8111-111111111111"
	targetCluster = "22222222-2222-4222-8222-222222222222"
	targetUID     = "33333333-3333-4333-8333-333333333333"
)

type fakeNamespaces struct {
	identity dataset.NamespaceIdentity
	err      error
	calls    int
}

func (f *fakeNamespaces) ReadNamespaceIdentity(ctx context.Context, tenantID, originalUID string) (dataset.NamespaceIdentity, error) {
	f.calls++
	return f.identity, f.err
}

type fakeObserver struct {
	observation dataset.EnvironmentObservation
	err         error
}

func (f *fakeObserver) ObserveEnvironment(ctx context.Context, tenantID, clusterID string) (dataset.EnvironmentObservation, error) {
	return f.observation, f.err
}

func fixturePlatform() cpup01.StorageScope {
	// The platform scope supplies connection/bucket/credential; the prefix is
	// always overwritten by TenantStorageScope and must not be taken from here.
	return cpup01.StorageScope{
		StorageConnectionID: "input-store-v1", Bucket: "inputs",
		ApprovedPrefix: "inputs/platform-placeholder", CredentialReference: "input-read",
	}
}

func liveNamespace() dataset.NamespaceIdentity {
	return dataset.NamespaceIdentity{
		ClusterID: targetCluster, Name: "tenant-" + targetTenant, UID: targetUID,
	}
}

func newTargetReader(t *testing.T, namespaces dataset.NamespaceReader, observer dataset.EnvironmentObserver) *dataset.TargetReader {
	t.Helper()
	reader, err := dataset.NewTargetReader(namespaces, observer, fixturePlatform(), targetCluster)
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	return reader
}

func TestTargetUsesStandardNamespaceAndInputScope(t *testing.T) {
	reader := newTargetReader(t, &fakeNamespaces{identity: liveNamespace()}, nil)
	target, err := reader.ReadDatasetImportTarget(context.Background(), targetTenant)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if target.NamespaceName != "tenant-"+targetTenant || target.NamespaceUID != targetUID || target.ClusterID != targetCluster {
		t.Fatalf("namespace identity = %+v", target)
	}
	// The prefix is derived, never taken from a caller.
	if target.InputScope.ApprovedPrefix != "inputs/"+targetTenant {
		t.Fatalf("prefix = %q", target.InputScope.ApprovedPrefix)
	}
	if target.SnapshotSHA256 == "" || target.InputScopeSHA256 == "" {
		t.Fatal("target digests were not computed")
	}
}

// The platform scope is a root, not a tenant scope: TenantStorageScope always
// overwrites its prefix. The deployed platform file therefore carries no
// approved_prefix, and construction must still succeed — requiring one rejected
// the whole service at startup.
func TestTargetAcceptsPlatformScopeWithoutPrefix(t *testing.T) {
	platform := cpup01.StorageScope{
		StorageConnectionID: "input-store-v1", Bucket: "inputs", CredentialReference: "input-read",
	}
	reader, err := dataset.NewTargetReader(&fakeNamespaces{identity: liveNamespace()}, nil, platform, targetCluster)
	if err != nil {
		t.Fatalf("a platform scope without a prefix was refused: %v", err)
	}
	target, err := reader.ReadDatasetImportTarget(context.Background(), targetTenant)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if target.InputScope.ApprovedPrefix != "inputs/"+targetTenant {
		t.Fatalf("prefix = %q", target.InputScope.ApprovedPrefix)
	}
}

// The three fields the platform really contributes are still required.
func TestTargetRejectsIncompletePlatformScope(t *testing.T) {
	complete := cpup01.StorageScope{
		StorageConnectionID: "input-store-v1", Bucket: "inputs", CredentialReference: "input-read",
	}
	for name, mutate := range map[string]func(*cpup01.StorageScope){
		"no connection": func(s *cpup01.StorageScope) { s.StorageConnectionID = "" },
		"no bucket":     func(s *cpup01.StorageScope) { s.Bucket = "" },
		"no credential": func(s *cpup01.StorageScope) { s.CredentialReference = "" },
		"spaced value":  func(s *cpup01.StorageScope) { s.Bucket = " inputs" },
		"control rune":  func(s *cpup01.StorageScope) { s.Bucket = "inputs" + string(rune(1)) },
	} {
		scope := complete
		mutate(&scope)
		if _, err := dataset.NewTargetReader(&fakeNamespaces{identity: liveNamespace()}, nil, scope, targetCluster); !errors.Is(err, biz.ErrImportEnvironmentMissing) {
			t.Errorf("%s was accepted: %v", name, err)
		}
	}
}

func TestTargetGenericTenantCannotSupplyPrefix(t *testing.T) {
	// A non-canonical tenant must be refused before any observation. Note the
	// fixture tenant is all digits, so an upper-case variant of it is still
	// canonical and is covered by the success test instead.
	reader := newTargetReader(t, &fakeNamespaces{identity: liveNamespace()}, nil)
	for _, bad := range []string{"", "not-a-uuid", "00000000-0000-0000-0000-000000000000", "11111111-1111-4111-8111-11111111111A"} {
		if _, err := reader.ReadDatasetImportTarget(context.Background(), bad); !errors.Is(err, biz.ErrImportEnvironmentMissing) {
			t.Fatalf("%q accepted: %v", bad, err)
		}
	}
}

func TestTargetFailsClosedOnNamespaceErrors(t *testing.T) {
	reader := newTargetReader(t, &fakeNamespaces{err: errors.New("not found")}, nil)
	if _, err := reader.ReadDatasetImportTarget(context.Background(), targetTenant); !errors.Is(err, biz.ErrImportEnvironmentMissing) {
		t.Fatalf("namespace error returned %v", err)
	}
}

func TestTargetRejectsAnotherCluster(t *testing.T) {
	identity := liveNamespace()
	identity.ClusterID = "44444444-4444-4444-8444-444444444444"
	reader := newTargetReader(t, &fakeNamespaces{identity: identity}, nil)
	if _, err := reader.ReadDatasetImportTarget(context.Background(), targetTenant); !errors.Is(err, biz.ErrImportEnvironmentMissing) {
		t.Fatalf("foreign cluster accepted: %v", err)
	}
}

func TestTargetMissingRegistryRowIsNormal(t *testing.T) {
	// A tenant with no training registration can still import.
	occupier := &fakeNamespaces{identity: liveNamespace()}
	reader := newTargetReader(t, occupier, &fakeObserver{observation: dataset.EnvironmentObservation{Present: false}})
	if _, err := reader.ReadDatasetImportTarget(context.Background(), targetTenant); err != nil {
		t.Fatalf("missing registry row blocked the import: %v", err)
	}
}

func TestTargetBlocksNeedsReviewAndUIDChange(t *testing.T) {
	for name, observation := range map[string]dataset.EnvironmentObservation{
		"needs review": {Present: true, State: "NEEDS_REVIEW", NamespaceUID: targetUID, Blocked: true, BlockReason: "TENANT_NAMESPACE_UID_CHANGED"},
		"uid changed":  {Present: true, State: "READY", NamespaceUID: "55555555-5555-4555-8555-555555555555"},
	} {
		t.Run(name, func(t *testing.T) {
			reader := newTargetReader(t, &fakeNamespaces{identity: liveNamespace()}, &fakeObserver{observation: observation})
			if _, err := reader.ReadDatasetImportTarget(context.Background(), targetTenant); !errors.Is(err, biz.ErrImportEnvironmentMissing) {
				t.Fatalf("observation did not block: %v", err)
			}
		})
	}
}

func TestTargetRegistryReadFailureIsNotMissingRow(t *testing.T) {
	reader := newTargetReader(t, &fakeNamespaces{identity: liveNamespace()}, &fakeObserver{err: errors.New("boom")})
	_, err := reader.ReadDatasetImportTarget(context.Background(), targetTenant)
	if !errors.Is(err, dataset.ErrRegistryObservationFailed) {
		t.Fatalf("read failure returned %v", err)
	}
	if errors.Is(err, biz.ErrImportEnvironmentMissing) {
		t.Fatal("a registry read failure was reported as a missing environment")
	}
}

func TestTargetDigestIsStableAcrossCalls(t *testing.T) {
	reader := newTargetReader(t, &fakeNamespaces{identity: liveNamespace()}, nil)
	first, err := reader.ReadDatasetImportTarget(context.Background(), targetTenant)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reader.ReadDatasetImportTarget(context.Background(), targetTenant)
	if err != nil {
		t.Fatal(err)
	}
	if first.SnapshotSHA256 != second.SnapshotSHA256 || first.InputScopeSHA256 != second.InputScopeSHA256 {
		t.Fatal("target digest is not stable")
	}
}

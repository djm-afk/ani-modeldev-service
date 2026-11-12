package dataset_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// publishVersion writes the input version and the dataset version metadata that
// make an input version belong to a dataset. Both rows are required: the
// availability query joins them, so a version without metadata is a legacy
// input rather than a dataset version.
func (fixture *schedFixture) publishVersion(t *testing.T, inputVersion string, versionNumber int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO modeldev_input_versions (
    tenant_id, input_version_id, request_id, actor, requested_at,
    storage_connection_id, bucket, approved_prefix, object_key, object_version_id,
    size_bytes, sha256, format)
VALUES ($1,$2,$3,$4,clock_timestamp(),$5,$6,$7,$8,'v1',116,$9,'CSV')`,
		schedTenant, inputVersion, uuidFor("eeeeeeee", fixture.nextSeq()), schedActor,
		schedStore, schedBucket, schedPrefix, "inputs/"+inputVersion+".csv", hex64('a')); err != nil {
		t.Fatalf("insert input version: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO modeldev_dataset_version_metadata (
    tenant_id, input_version_id, dataset_id, import_task_id, version_number, preview, published_at)
VALUES ($1,$2,$3,$4,$5,'{}'::jsonb,clock_timestamp())`,
		schedTenant, inputVersion, schedDataset, fixture.taskID, versionNumber); err != nil {
		t.Fatalf("insert version metadata: %v", err)
	}
}

// Prepare must refuse a new admission from an archived collection, and must
// allow it again after a restore. The version itself stays READY throughout:
// only the owning dataset's lifecycle changes.
func TestDatasetAvailabilityFollowsTheLifecycle(t *testing.T) {
	fixture := newSchedFixture(t)
	now := time.Now().UTC()
	taskID := "aaaaaaaa-0000-4000-8000-000000000201"
	fixture.addTask(t, taskID, "QUEUED", now.Add(-time.Minute))
	fixture.publishVersion(t, fixture.lastInputVersion, fixture.lastVersionNumber)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := fixture.repo.CheckDatasetAvailable(ctx, schedTenant, fixture.lastInputVersion); err != nil {
		t.Fatalf("an ACTIVE dataset was reported unavailable: %v", err)
	}

	archived, err := fixture.repo.ArchiveDataset(ctx, biz.DatasetStateChange{
		TenantID: schedTenant, DatasetID: schedDataset, ActorID: schedActor, ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := fixture.repo.CheckDatasetAvailable(ctx, schedTenant, fixture.lastInputVersion); !errors.Is(err, biz.ErrDatasetArchived) {
		t.Fatalf("an archived dataset returned %v, want DATASET_ARCHIVED", err)
	}

	if _, err := fixture.repo.RestoreDataset(ctx, biz.DatasetStateChange{
		TenantID: schedTenant, DatasetID: schedDataset, ActorID: schedActor, ExpectedRevision: archived.Revision,
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if err := fixture.repo.CheckDatasetAvailable(ctx, schedTenant, fixture.lastInputVersion); err != nil {
		t.Fatalf("a restored dataset stayed unavailable: %v", err)
	}
}

// A version that belongs to no dataset is a legacy CSV input. The lifecycle
// does not govern it, so the check must not turn it into an archive rejection.
func TestDatasetAvailabilityAllowsLegacyInputs(t *testing.T) {
	fixture := newSchedFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := fixture.repo.CheckDatasetAvailable(ctx, schedTenant, uuidFor("dddddddd", 900)); err != nil {
		t.Fatalf("a legacy input was reported unavailable: %v", err)
	}
}

// The check is tenant-scoped: another tenant's dataset must not make this
// tenant's version look archived, and must not leak its existence.
func TestDatasetAvailabilityIsTenantScoped(t *testing.T) {
	fixture := newSchedFixture(t)
	now := time.Now().UTC()
	taskID := "aaaaaaaa-0000-4000-8000-000000000202"
	fixture.addTask(t, taskID, "QUEUED", now.Add(-time.Minute))
	fixture.publishVersion(t, fixture.lastInputVersion, fixture.lastVersionNumber)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := fixture.repo.ArchiveDataset(ctx, biz.DatasetStateChange{
		TenantID: schedTenant, DatasetID: schedDataset, ActorID: schedActor, ExpectedRevision: 1,
	}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	otherTenant := "22222222-2222-4222-8222-222222222222"
	if err := fixture.repo.CheckDatasetAvailable(ctx, otherTenant, fixture.lastInputVersion); err != nil {
		t.Fatalf("another tenant observed this dataset's archive: %v", err)
	}
}

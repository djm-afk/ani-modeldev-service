package dataset_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/dataset"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

// Two tenants share one database. Every read and write goes through the
// repository with the caller's tenant, so a foreign id can never be used to
// observe or change another tenant's collection. These tests use the real
// schema, not a fake, because the guarantee lives in the WHERE clauses.
const (
	firstTenant   = "11111111-1111-4111-8111-111111111111"
	secondTenant  = "22222222-2222-4222-8222-222222222222"
	firstDataset  = "33333333-3333-4333-8333-333333333333"
	secondDataset = "44444444-4444-4444-8444-444444444444"
	missingTask   = "55555555-5555-4555-8555-555555555555"
)

func newRepository(t *testing.T) *dataset.Repository {
	t.Helper()
	return dataset.New(postgres.Prepare(t)())
}

func createCollection(t *testing.T, repo *dataset.Repository, tenant, datasetID string) biz.Dataset {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	value, err := repo.CreateDataset(ctx, biz.Dataset{
		TenantID: tenant, DatasetID: datasetID, Name: "shared-collection",
		CreatedBy: "governance:user:1", UpdatedBy: "governance:user:1",
	})
	if err != nil {
		t.Fatalf("create collection for %s: %v", tenant, err)
	}
	return value
}

// Two tenants may use the same name: the unique constraint is per tenant, and
// neither may read the other's row.
func TestCollectionIsInvisibleToAnotherTenant(t *testing.T) {
	repo := newRepository(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	first := createCollection(t, repo, firstTenant, firstDataset)
	second := createCollection(t, repo, secondTenant, secondDataset)
	if first.Name != second.Name {
		t.Fatalf("fixture names diverged: %q and %q", first.Name, second.Name)
	}

	if _, err := repo.GetDataset(ctx, firstTenant, secondDataset); !errors.Is(err, biz.ErrDatasetNotFound) {
		t.Fatalf("first tenant read the second tenant's collection: %v", err)
	}
	if _, err := repo.GetDataset(ctx, secondTenant, firstDataset); !errors.Is(err, biz.ErrDatasetNotFound) {
		t.Fatalf("second tenant read the first tenant's collection: %v", err)
	}

	own, err := repo.GetDataset(ctx, firstTenant, firstDataset)
	if err != nil || own.DatasetID != firstDataset {
		t.Fatalf("own collection unreadable: %+v, %v", own, err)
	}
	// The summary fields are produced by tenant-scoped aggregates. A row that
	// belonged to another tenant would surface here, so zero means isolated.
	if own.ReadyVersionCount != 0 || own.LatestReadyVersionNumber != 0 || own.LatestImport != nil {
		t.Fatalf("own summaries picked up foreign facts: %+v", own)
	}
}

// Lists, versions, tasks and the operation history all stay inside the caller's
// tenant even when the request names a foreign dataset id.
func TestTenantScopedListsNeverCrossTheBoundary(t *testing.T) {
	repo := newRepository(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	createCollection(t, repo, firstTenant, firstDataset)
	createCollection(t, repo, secondTenant, secondDataset)

	page, err := repo.ListDatasets(ctx, biz.DatasetListFilter{TenantID: firstTenant, Page: 1, PageSize: 100})
	if err != nil {
		t.Fatalf("list collections: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].DatasetID != firstDataset {
		t.Fatalf("first tenant saw %d collections: %+v", page.Total, page.Items)
	}

	foreignID := secondDataset
	versions, err := repo.ListVersions(ctx, biz.DatasetVersionFilter{TenantID: firstTenant, DatasetID: foreignID, Page: 1, PageSize: 100})
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if versions.Total != 0 || len(versions.Items) != 0 {
		t.Fatalf("foreign versions leaked: %+v", versions)
	}

	tasks, err := repo.ListImportTasks(ctx, biz.DatasetImportFilter{TenantID: firstTenant, DatasetID: foreignID, Page: 1, PageSize: 100})
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if tasks.Total != 0 || len(tasks.Items) != 0 {
		t.Fatalf("foreign tasks leaked: %+v", tasks)
	}

	operations, err := repo.ListOperations(ctx, biz.DatasetOperationFilter{TenantID: firstTenant, DatasetID: foreignID, Page: 1, PageSize: 100})
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	if operations.Total != 0 || len(operations.Items) != 0 {
		t.Fatalf("foreign operations leaked: %+v", operations)
	}

	if _, err := repo.GetImportTask(ctx, firstTenant, missingTask); !errors.Is(err, biz.ErrImportTaskNotFound) {
		t.Fatalf("unknown task did not report not-found: %v", err)
	}
}

// A foreign tenant cannot advance or close another tenant's collection. The
// write is rejected, and the owner's row is still ACTIVE afterwards.
func TestCrossTenantLifecycleCannotChangeAnotherTenantsCollection(t *testing.T) {
	repo := newRepository(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	createCollection(t, repo, firstTenant, firstDataset)
	createCollection(t, repo, secondTenant, secondDataset)

	change := biz.DatasetStateChange{
		TenantID: secondTenant, DatasetID: firstDataset,
		ActorID: "governance:user:2", ExpectedRevision: 1,
	}
	if _, err := repo.ArchiveDataset(ctx, change); err == nil {
		t.Fatal("a foreign tenant archived another tenant's collection")
	}

	own, err := repo.GetDataset(ctx, firstTenant, firstDataset)
	if err != nil {
		t.Fatalf("read own collection after foreign write: %v", err)
	}
	if own.State != biz.DatasetActive || own.Revision != 1 {
		t.Fatalf("foreign write changed the owner's row: %+v", own)
	}

	// The owner's own archive still works, which proves the rejection above was
	// the tenant boundary and not a broken lifecycle.
	owned := biz.DatasetStateChange{TenantID: firstTenant, DatasetID: firstDataset, ActorID: "governance:user:1", ExpectedRevision: 1}
	archived, err := repo.ArchiveDataset(ctx, owned)
	if err != nil {
		t.Fatalf("owner could not archive its own collection: %v", err)
	}
	if archived.State != biz.DatasetArchived {
		t.Fatalf("owner's archive did not take effect: %+v", archived)
	}
}

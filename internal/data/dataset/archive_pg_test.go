package dataset_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// Archive must close every task that could still produce a version, in the same
// transaction that closes the collection. A task left running would publish a
// READY version after the archive, and a later restore would make it look
// legitimate.
func TestArchiveClosesLiveTasks(t *testing.T) {
	fixture := newSchedFixture(t)
	now := time.Now().UTC()
	queued := "aaaaaaaa-0000-4000-8000-000000000101"
	running := "aaaaaaaa-0000-4000-8000-000000000102"
	failed := "aaaaaaaa-0000-4000-8000-000000000103"
	fixture.addTask(t, queued, "QUEUED", now.Add(-time.Minute))
	fixture.addTask(t, running, "RUNNING", now.Add(-time.Minute))
	fixture.addTask(t, failed, "FAILED", now.Add(-time.Minute))
	fixture.addAttempt(t, running, "bbbbbbbb-0000-4000-8000-000000000102", 1)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// The running task owns an attempt; the failed one is retryable.
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE modeldev_dataset_import_tasks SET current_attempt_id=$1 WHERE tenant_id=$2 AND task_id=$3`,
		"bbbbbbbb-0000-4000-8000-000000000102", schedTenant, running); err != nil {
		t.Fatalf("bind attempt: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE modeldev_dataset_import_tasks SET retryable=true, failure_code='IMPORT_DEPENDENCY_UNAVAILABLE'
		 WHERE tenant_id=$1 AND task_id=$2`, schedTenant, failed); err != nil {
		t.Fatalf("mark retryable: %v", err)
	}

	if _, err := fixture.repo.ArchiveDataset(ctx, biz.DatasetStateChange{
		TenantID: schedTenant, DatasetID: schedDataset, ActorID: schedActor, ExpectedRevision: 1,
	}); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// A QUEUED task with no attempt has nothing to stop: it is finished here.
	if state, _, _ := fixture.taskState(t, queued); state != "CANCELLED" {
		t.Fatalf("queued task state = %s, want CANCELLED", state)
	}
	// A running task gets a cancel intent; the worker converges it.
	if state, _, _ := fixture.taskState(t, running); state != "CANCELING" {
		t.Fatalf("running task state = %s, want CANCELING", state)
	}
	// A retryable failure must not stay retryable after the archive.
	var retryable bool
	if err := fixture.pool.QueryRow(ctx,
		`SELECT retryable FROM modeldev_dataset_import_tasks WHERE tenant_id=$1 AND task_id=$2`,
		schedTenant, failed).Scan(&retryable); err != nil {
		t.Fatalf("read retryable: %v", err)
	}
	if retryable {
		t.Fatal("a retryable task survived the archive")
	}
}

// Restore reopens the collection but must not revive cancelled work.
func TestRestoreDoesNotReviveCancelledTasks(t *testing.T) {
	fixture := newSchedFixture(t)
	now := time.Now().UTC()
	queued := "aaaaaaaa-0000-4000-8000-000000000104"
	fixture.addTask(t, queued, "QUEUED", now.Add(-time.Minute))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	archived, err := fixture.repo.ArchiveDataset(ctx, biz.DatasetStateChange{
		TenantID: schedTenant, DatasetID: schedDataset, ActorID: schedActor, ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if archived.State != biz.DatasetArchived {
		t.Fatalf("state = %s, want ARCHIVED", archived.State)
	}
	if state, _, _ := fixture.taskState(t, queued); state != "CANCELLED" {
		t.Fatalf("queued task state = %s, want CANCELLED", state)
	}

	restored, err := fixture.repo.RestoreDataset(ctx, biz.DatasetStateChange{
		TenantID: schedTenant, DatasetID: schedDataset, ActorID: schedActor, ExpectedRevision: archived.Revision,
	})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.State != biz.DatasetActive {
		t.Fatalf("state = %s, want ACTIVE", restored.State)
	}
	// The cancelled task stays cancelled: restore never reopens work.
	if state, _, _ := fixture.taskState(t, queued); state != "CANCELLED" {
		t.Fatalf("restore revived a cancelled task: %s", state)
	}
}

// A task archived while its Job is still running must not publish after a
// restore. The cancel intent written by the archive is what blocks it.
func TestArchivedTaskCannotPublishAfterRestore(t *testing.T) {
	fixture := newSchedFixture(t)
	now := time.Now().UTC()
	running := "aaaaaaaa-0000-4000-8000-000000000105"
	fixture.addTask(t, running, "RUNNING", now.Add(-time.Minute))
	fixture.addAttempt(t, running, "bbbbbbbb-0000-4000-8000-000000000105", 1)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE modeldev_dataset_import_tasks SET current_attempt_id=$1 WHERE tenant_id=$2 AND task_id=$3`,
		"bbbbbbbb-0000-4000-8000-000000000105", schedTenant, running); err != nil {
		t.Fatalf("bind attempt: %v", err)
	}
	archived, err := fixture.repo.ArchiveDataset(ctx, biz.DatasetStateChange{
		TenantID: schedTenant, DatasetID: schedDataset, ActorID: schedActor, ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, err := fixture.repo.RestoreDataset(ctx, biz.DatasetStateChange{
		TenantID: schedTenant, DatasetID: schedDataset, ActorID: schedActor, ExpectedRevision: archived.Revision,
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// The publication transaction refuses a task with a cancel intent, so the
	// archived-then-restored task can never produce a version.
	var cancelRequested bool
	if err := fixture.pool.QueryRow(ctx,
		`SELECT cancel_requested_at IS NOT NULL FROM modeldev_dataset_import_tasks
		 WHERE tenant_id=$1 AND task_id=$2`, schedTenant, running).Scan(&cancelRequested); err != nil {
		t.Fatalf("read cancel intent: %v", err)
	}
	if !cancelRequested {
		t.Fatal("the archive left no cancel intent, so the task could still publish")
	}
}

// A stale expected_revision must not archive anything, including tasks.
func TestArchiveWithStaleRevisionClosesNothing(t *testing.T) {
	fixture := newSchedFixture(t)
	now := time.Now().UTC()
	queued := "aaaaaaaa-0000-4000-8000-000000000106"
	fixture.addTask(t, queued, "QUEUED", now.Add(-time.Minute))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := fixture.repo.ArchiveDataset(ctx, biz.DatasetStateChange{
		TenantID: schedTenant, DatasetID: schedDataset, ActorID: schedActor, ExpectedRevision: 99,
	}); !errors.Is(err, biz.ErrRevisionConflict) {
		t.Fatalf("stale revision returned %v", err)
	}
	if state, _, _ := fixture.taskState(t, queued); state != "QUEUED" {
		t.Fatalf("a rejected archive still closed the task: %s", state)
	}
}

// The import intent digest must include the confirmed source selection, so the
// same key with a different file is a conflict rather than a replay.
func TestImportDigestIncludesTheSelection(t *testing.T) {
	base := biz.DatasetCommand{
		TenantID: schedTenant, ActorID: schedActor, Kind: biz.OpImportVersion,
		IdempotencyKey: "import-key-1", DatasetID: schedDataset,
		SelectionFingerprint: "fingerprint-a",
	}
	other := base
	other.SelectionFingerprint = "fingerprint-b"

	if base.Digest() == other.Digest() {
		t.Fatal("two different selections produced the same intent digest")
	}
	// The same selection must still hash the same way, or a legitimate replay
	// would be reported as a conflict.
	replay := base
	if base.Digest() != replay.Digest() {
		t.Fatal("the same selection produced two different digests")
	}
}

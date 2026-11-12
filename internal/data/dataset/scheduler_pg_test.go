package dataset_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/dataset"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

// These tests exercise the scheduler's SQL against the real schema. The
// guarantees under test live in WHERE clauses and column defaults, so a fake
// repository cannot prove them: the scan split, the untouched next_run_at, the
// interval binding, the quota count and the two-step writer stop.

const (
	schedTenant   = "11111111-1111-4111-8111-111111111111"
	schedDataset  = "33333333-3333-4333-8333-333333333333"
	schedCluster  = "66666666-6666-4666-8666-666666666666"
	schedNSName   = "tenant-" + schedTenant
	schedNSUID    = "77777777-7777-4777-8777-777777777777"
	schedPrefix   = "inputs/" + schedTenant
	schedStore    = "store-1"
	schedBucket   = "inputs-bucket"
	schedCredRef  = "cred-1"
	schedActor    = "governance:user:1"
	schedDeadline = 30 * time.Minute
)

func hex64(fill byte) string {
	out := make([]byte, 64)
	for i := range out {
		out[i] = fill
	}
	return string(out)
}

// schedFixture inserts one dataset, one acceptance receipt and one task. The
// rows satisfy every CHECK the migration declares, so a later assertion failure
// is about the behaviour under test rather than a malformed fixture.
type schedFixture struct {
	pool      *pgxpool.Pool
	repo      *dataset.Repository
	datasetID string
	taskID    string
	seq       int
	// lastInputVersion and lastVersionNumber record what the most recent addTask
	// reserved, so a test can publish a version for that exact task.
	lastInputVersion  string
	lastVersionNumber int
}

// nextSeq hands out a distinct small integer per fixture row.
func (fixture *schedFixture) nextSeq() int {
	fixture.seq++
	return fixture.seq
}

func newSchedFixture(t *testing.T) *schedFixture {
	t.Helper()
	pool := postgres.Prepare(t)()
	fixture := &schedFixture{pool: pool, repo: dataset.New(pool)}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if _, err := pool.Exec(ctx, `
INSERT INTO modeldev_datasets (tenant_id, dataset_id, name, created_by, updated_by)
VALUES ($1, $2, 'sched-fixture', $3, $3)`, schedTenant, schedDataset, schedActor); err != nil {
		t.Fatalf("insert dataset: %v", err)
	}
	fixture.datasetID = schedDataset
	return fixture
}

// uuidFor builds a valid v4-shaped UUID from a small integer, so each fixture
// row gets a distinct identity without a random source.
func uuidFor(prefix string, n int) string {
	return fmt.Sprintf("%s-0000-4000-8000-%012d", prefix, n)
}

// addTask inserts one task in the requested state. The receipt is written first
// because the task's foreign key names it.
func (fixture *schedFixture) addTask(t *testing.T, taskID, state string, nextRunAt time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	receiptID := uuidFor("cccccccc", fixture.nextSeq())
	inputVersion := uuidFor("dddddddd", fixture.nextSeq())
	inputRequest := uuidFor("eeeeeeee", fixture.nextSeq())
	// Version numbers are unique per dataset, so each fixture task takes the next.
	versionNumber := fixture.nextSeq()
	// A FAILED row must carry a failure code and a finish time; the schema
	// requires both for a terminal state.
	var failureCode *string
	var finishedAt *time.Time
	if state == "FAILED" {
		code := "IMPORT_DEPENDENCY_UNAVAILABLE"
		failureCode = &code
		at := time.Now().UTC()
		finishedAt = &at
	}

	sourceSnapshot := fmt.Sprintf(`{"schema":"ani.modeldev.dataset.source-snapshot.v1",`+
		`"provider":"MODELSCOPE","repo_type":"dataset","repo_id":"org/ds",`+
		`"requested_revision":"master","file_path":"data.json","lock_kind":"FILE_SHA256",`+
		`"resolved_revision":"master","expected_size_bytes":"116",`+
		`"expected_file_sha256":"%s","resolved_at":"2026-11-11T00:00:00.000000Z"}`, hex64('b'))
	targetSnapshot := fmt.Sprintf(`{"schema":"ani.modeldev.dataset.import-target.v1",`+
		`"tenant_id":"%s","cluster_id":"%s","namespace_name":"%s","namespace_uid":"%s",`+
		`"input_scope":{"storage_connection_id":"%s","bucket":"%s","approved_prefix":"%s",`+
		`"credential_reference":"%s"}}`,
		schedTenant, schedCluster, schedNSName, schedNSUID, schedStore, schedBucket, schedPrefix, schedCredRef)
	authorizationScope := fmt.Sprintf(`{"schema":"ani.modeldev.dataset.import-authorization.v1",`+
		`"tenant_id":"%s","input_scope_sha256":"%s","purpose":"dataset-import",`+
		`"allowed_actions":["read"]}`, schedTenant, hex64('c'))

	tx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
INSERT INTO modeldev_dataset_command_receipts (
    tenant_id, receipt_id, actor_id, operation_kind, idempotency_key,
    request_sha256, canonical_request, dataset_id, task_id, response_status, receipt, accepted_at)
VALUES ($1,$2,$3,'IMPORT_DATASET_VERSION',$4,$5,'{}'::jsonb,$6,$7,202,'{}'::jsonb,clock_timestamp())`,
		schedTenant, receiptID, schedActor, "key-"+taskID, hex64('d'), schedDataset, taskID); err != nil {
		t.Fatalf("insert receipt: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO modeldev_dataset_import_tasks (
    tenant_id, task_id, dataset_id, acceptance_receipt_id, reserved_input_version_id,
    input_request_id, version_number, created_by, accepted_at, source_provider,
    source_snapshot, source_snapshot_sha256, expected_size_bytes, accepted_limit_bytes,
    cluster_id, namespace_name, namespace_uid, target_snapshot, target_snapshot_sha256,
    storage_connection_id, bucket, approved_prefix, input_scope_sha256, credential_reference,
    authorization_scope, authorization_scope_sha256, recovery_policy, state, next_run_at, failure_code, finished_at)
VALUES ($1,$2,$3,$4,$5,$6,$24,$7,clock_timestamp(),'MODELSCOPE',
        $8::jsonb,$9,116,1073741824,
        $10,$11,$12,$13::jsonb,$14,
        $15,$16,$17,$18,$19,
        $20::jsonb,$21,'{}'::jsonb,$22,$23,$25,$26)`,
		schedTenant, taskID, schedDataset, receiptID, inputVersion,
		inputRequest, schedActor,
		sourceSnapshot, hex64('e'),
		schedCluster, schedNSName, schedNSUID, targetSnapshot, hex64('f'),
		schedStore, schedBucket, schedPrefix, hex64('c'), schedCredRef,
		authorizationScope, hex64('a'), state, nextRunAt, versionNumber, failureCode, finishedAt); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	fixture.taskID = taskID
	fixture.lastInputVersion = inputVersion
	fixture.lastVersionNumber = versionNumber
}

// addAttempt inserts one attempt for the given task.
func (fixture *schedFixture) addAttempt(t *testing.T, taskID, attemptID string, attemptNo int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := fixture.pool.Exec(ctx, `
INSERT INTO modeldev_dataset_import_attempts (
    tenant_id, attempt_id, dataset_id, task_id, attempt_no, recovery_generation,
    cluster_id, namespace_name, namespace_uid, job_name, pvc_name, secret_name,
    runtime_plan, runtime_plan_sha256, data_object_key, result_object_key, deadline_at)
VALUES ($1,$2,$3,$4,$5,1,$6,$7,$8,$9,$10,$11,'{}'::jsonb,$12,$13,$14,clock_timestamp() + interval '30 minutes')`,
		schedTenant, attemptID, schedDataset, taskID, attemptNo,
		schedCluster, schedNSName, schedNSUID,
		"job-"+attemptID, "pvc-"+attemptID, "secret-"+attemptID,
		hex64('1'), "data/"+attemptID, "result/"+attemptID); err != nil {
		t.Fatalf("insert attempt: %v", err)
	}
}

func (fixture *schedFixture) taskState(t *testing.T, taskID string) (string, time.Time, int32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var state string
	var nextRunAt time.Time
	var deferCount int32
	if err := fixture.pool.QueryRow(ctx,
		`SELECT state, next_run_at, defer_count FROM modeldev_dataset_import_tasks WHERE tenant_id=$1 AND task_id=$2`,
		schedTenant, taskID).Scan(&state, &nextRunAt, &deferCount); err != nil {
		t.Fatalf("read task: %v", err)
	}
	return state, nextRunAt, deferCount
}

func (fixture *schedFixture) attemptRow(t *testing.T, attemptID string) (closed, stopped bool, cleanup string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var dispatchClosedAt, writersStoppedAt *time.Time
	if err := fixture.pool.QueryRow(ctx,
		`SELECT dispatch_closed_at, writers_stopped_at, cleanup_state
		   FROM modeldev_dataset_import_attempts WHERE tenant_id=$1 AND attempt_id=$2`,
		schedTenant, attemptID).Scan(&dispatchClosedAt, &writersStoppedAt, &cleanup); err != nil {
		t.Fatalf("read attempt: %v", err)
	}
	return dispatchClosedAt != nil, writersStoppedAt != nil, cleanup
}

// A full quota must not hide the rows that observe a running Job. Before the
// split, a backlog of unstarted tasks filled every batch and the running ones
// were never observed, so their slots never returned.
func TestListDueTasksSeparatesObservationFromDispatch(t *testing.T) {
	fixture := newSchedFixture(t)
	now := time.Now().UTC()
	queued := "aaaaaaaa-0000-4000-8000-000000000001"
	running := "aaaaaaaa-0000-4000-8000-000000000002"
	fixture.addTask(t, queued, "QUEUED", now.Add(-time.Minute))
	fixture.addTask(t, running, "RUNNING", now.Add(-time.Minute))
	fixture.addAttempt(t, running, "bbbbbbbb-0000-4000-8000-000000000002", 1)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE modeldev_dataset_import_tasks SET current_attempt_id=$1 WHERE tenant_id=$2 AND task_id=$3`,
		"bbbbbbbb-0000-4000-8000-000000000002", schedTenant, running); err != nil {
		t.Fatalf("bind attempt: %v", err)
	}

	observationOnly, err := fixture.repo.ListDueTasks(ctx, now, 20, false)
	if err != nil {
		t.Fatalf("list observation: %v", err)
	}
	if len(observationOnly) != 1 || observationOnly[0].TaskID != running {
		t.Fatalf("observation scan = %v, want only the running task", taskIDs(observationOnly))
	}

	withDispatch, err := fixture.repo.ListDueTasks(ctx, now, 20, true)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(withDispatch) != 2 {
		t.Fatalf("full scan = %v, want both tasks", taskIDs(withDispatch))
	}
	// Observation work sorts first so a bounded batch never spends itself on
	// rows that cannot start.
	if withDispatch[0].TaskID != running {
		t.Fatalf("full scan order = %v, want the running task first", taskIDs(withDispatch))
	}
}

// Binding an attempt must not move the row in the scan order. It used to set
// next_run_at to now, which pushed a running task behind every queued one.
func TestSetCurrentAttemptKeepsNextRunAt(t *testing.T) {
	fixture := newSchedFixture(t)
	original := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
	taskID := "aaaaaaaa-0000-4000-8000-000000000003"
	attemptID := "bbbbbbbb-0000-4000-8000-000000000003"
	fixture.addTask(t, taskID, "RUNNING", original)
	fixture.addAttempt(t, taskID, attemptID, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := fixture.repo.SetCurrentAttempt(ctx, schedTenant, taskID, attemptID); err != nil {
		t.Fatalf("set current attempt: %v", err)
	}
	_, nextRunAt, _ := fixture.taskState(t, taskID)
	if !nextRunAt.Equal(original) {
		t.Fatalf("next_run_at moved from %s to %s", original, nextRunAt)
	}
}

// A deferral must write a real interval. An unbound pgtype.Interval becomes
// NULL, which violates next_run_at NOT NULL and turns a bounded retry into a
// hard persistence failure.
func TestDeferTaskWritesAValidIntervalAndSpendsBudget(t *testing.T) {
	fixture := newSchedFixture(t)
	taskID := "aaaaaaaa-0000-4000-8000-000000000004"
	fixture.addTask(t, taskID, "RUNNING", time.Now().UTC().Add(-time.Minute))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	before := time.Now().UTC()
	if err := fixture.repo.DeferTask(ctx, biz.TaskDeferral{
		TenantID: schedTenant, TaskID: taskID, Delay: 30 * time.Second, FailureCode: "IMPORT_DEPENDENCY_UNAVAILABLE",
	}); err != nil {
		t.Fatalf("defer: %v", err)
	}
	state, nextRunAt, deferCount := fixture.taskState(t, taskID)
	if state != "RUNNING" {
		t.Fatalf("state = %s, want RUNNING", state)
	}
	if nextRunAt.Before(before.Add(20 * time.Second)) {
		t.Fatalf("next_run_at = %s, want roughly 30s ahead", nextRunAt)
	}
	if deferCount != 1 {
		t.Fatalf("defer_count = %d, want 1", deferCount)
	}
}

// The quota counts attempts whose writer is not proven stopped. Counting by the
// dispatch gate instead released a slot while the Job could still be writing.
func TestCountRunningAttemptsUsesWritersStopped(t *testing.T) {
	fixture := newSchedFixture(t)
	taskID := "aaaaaaaa-0000-4000-8000-000000000005"
	attemptID := "bbbbbbbb-0000-4000-8000-000000000005"
	fixture.addTask(t, taskID, "RUNNING", time.Now().UTC().Add(-time.Minute))
	fixture.addAttempt(t, taskID, attemptID, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	running, err := fixture.repo.CountRunningAttempts(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if running != 1 {
		t.Fatalf("running = %d, want 1 before any close", running)
	}

	// Closing the dispatch gate alone must not release the slot.
	if _, err := fixture.repo.CloseAttempt(ctx, schedTenant, attemptID, "FAILED"); err != nil {
		t.Fatalf("close attempt: %v", err)
	}
	running, err = fixture.repo.CountRunningAttempts(ctx)
	if err != nil {
		t.Fatalf("count after close: %v", err)
	}
	if running != 1 {
		t.Fatalf("running = %d, want 1 while the writer is unproven", running)
	}

	// Only the observed stop releases it.
	if _, err := fixture.repo.ConfirmWritersStopped(ctx, schedTenant, attemptID); err != nil {
		t.Fatalf("confirm writers stopped: %v", err)
	}
	running, err = fixture.repo.CountRunningAttempts(ctx)
	if err != nil {
		t.Fatalf("count after stop: %v", err)
	}
	if running != 0 {
		t.Fatalf("running = %d, want 0 after the writer stopped", running)
	}
}

// CloseAttempt must never record that the writer stopped, and
// ConfirmWritersStopped must refuse an attempt whose dispatch gate is open.
func TestWriterStopRequiresAClosedDispatchGate(t *testing.T) {
	fixture := newSchedFixture(t)
	taskID := "aaaaaaaa-0000-4000-8000-000000000006"
	attemptID := "bbbbbbbb-0000-4000-8000-000000000006"
	fixture.addTask(t, taskID, "RUNNING", time.Now().UTC().Add(-time.Minute))
	fixture.addAttempt(t, taskID, attemptID, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := fixture.repo.ConfirmWritersStopped(ctx, schedTenant, attemptID); !errors.Is(err, biz.ErrImportStateConflict) {
		t.Fatalf("an open dispatch gate accepted a writer stop: %v", err)
	}

	if _, err := fixture.repo.CloseAttempt(ctx, schedTenant, attemptID, "FAILED"); err != nil {
		t.Fatalf("close attempt: %v", err)
	}
	closed, stopped, _ := fixture.attemptRow(t, attemptID)
	if !closed {
		t.Fatal("dispatch gate not closed")
	}
	if stopped {
		t.Fatal("CloseAttempt recorded the writer stopped without observing it")
	}

	if _, err := fixture.repo.ConfirmWritersStopped(ctx, schedTenant, attemptID); err != nil {
		t.Fatalf("confirm writers stopped: %v", err)
	}
	_, stopped, _ = fixture.attemptRow(t, attemptID)
	if !stopped {
		t.Fatal("writer stop not recorded")
	}
	// A second confirmation is a state conflict, never a silent success.
	if _, err := fixture.repo.ConfirmWritersStopped(ctx, schedTenant, attemptID); !errors.Is(err, biz.ErrImportStateConflict) {
		t.Fatalf("a repeated writer stop was accepted: %v", err)
	}
}

// The reconcile lease must be releasable so an attempt stays scannable instead
// of waiting out the TTL between two steps that need no exclusivity.
func TestAttemptLeaseIsReleasable(t *testing.T) {
	fixture := newSchedFixture(t)
	taskID := "aaaaaaaa-0000-4000-8000-000000000007"
	attemptID := "bbbbbbbb-0000-4000-8000-000000000007"
	fixture.addTask(t, taskID, "RUNNING", time.Now().UTC().Add(-time.Minute))
	fixture.addAttempt(t, taskID, attemptID, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	leased, err := fixture.repo.AcquireAttemptLease(ctx, schedTenant, attemptID, "modeldev:service:test", time.Minute)
	if err != nil {
		t.Fatalf("acquire attempt lease: %v", err)
	}
	if leased.ReconcileEpoch <= 0 {
		t.Fatalf("reconcile epoch = %d, want > 0", leased.ReconcileEpoch)
	}
	if err := fixture.repo.ReleaseAttemptLease(ctx, schedTenant, attemptID, "modeldev:service:test", leased.ReconcileEpoch); err != nil {
		t.Fatalf("release attempt lease: %v", err)
	}
	// A released attempt is immediately scannable again.
	due, err := fixture.repo.ListDueAttempts(ctx, time.Now().UTC(), 20)
	if err != nil {
		t.Fatalf("list due attempts: %v", err)
	}
	if len(due) != 1 || due[0].AttemptID != attemptID {
		t.Fatalf("due attempts = %v, want the released one", attemptIDs(due))
	}
}

// Cleanup may only be claimed after the writer is proven stopped, and finishing
// it must mark the attempt done.
func TestCleanupClaimRequiresAStoppedWriter(t *testing.T) {
	fixture := newSchedFixture(t)
	taskID := "aaaaaaaa-0000-4000-8000-000000000008"
	attemptID := "bbbbbbbb-0000-4000-8000-000000000008"
	fixture.addTask(t, taskID, "RUNNING", time.Now().UTC().Add(-time.Minute))
	fixture.addAttempt(t, taskID, attemptID, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := fixture.repo.ClaimAttemptCleanup(ctx, schedTenant, attemptID); !errors.Is(err, biz.ErrImportStateConflict) {
		t.Fatalf("cleanup claimed before the writer stopped: %v", err)
	}
	if _, err := fixture.repo.CloseAttempt(ctx, schedTenant, attemptID, "FAILED"); err != nil {
		t.Fatalf("close attempt: %v", err)
	}
	if _, err := fixture.repo.ConfirmWritersStopped(ctx, schedTenant, attemptID); err != nil {
		t.Fatalf("confirm writers stopped: %v", err)
	}
	if _, err := fixture.repo.ClaimAttemptCleanup(ctx, schedTenant, attemptID); err != nil {
		t.Fatalf("claim cleanup: %v", err)
	}
	if _, err := fixture.repo.FinishAttemptCleanup(ctx, schedTenant, attemptID); err != nil {
		t.Fatalf("finish cleanup: %v", err)
	}
	_, _, cleanup := fixture.attemptRow(t, attemptID)
	if cleanup != "DONE" {
		t.Fatalf("cleanup state = %s, want DONE", cleanup)
	}
}

func taskIDs(tasks []biz.DatasetImportTask) []string {
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.TaskID)
	}
	return out
}

func attemptIDs(attempts []biz.DatasetImportAttempt) []string {
	out := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		out = append(out, attempt.AttemptID)
	}
	return out
}

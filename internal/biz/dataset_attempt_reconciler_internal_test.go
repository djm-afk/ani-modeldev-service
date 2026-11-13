package biz

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeAttemptWorker is a minimal in-memory DatasetWorkerRepository for the
// reconciler. Only the methods the reconciler calls are implemented; the rest
// panic so an accidental new dependency is caught by the test rather than
// silently ignored.
type fakeAttemptWorker struct {
	attempts   map[string]DatasetImportAttempt
	leases     map[string]int64
	closed     []string
	stopped    []string
	cleaned    []string
	bound      []string
	deferred   []string
	observe    ImportWorkloadStatus
	observeErr error
	bindErr    error
}

func newFakeAttemptWorker(attempts ...DatasetImportAttempt) *fakeAttemptWorker {
	worker := &fakeAttemptWorker{
		attempts: map[string]DatasetImportAttempt{}, leases: map[string]int64{},
		observe: ImportWorkloadStatus{Present: false},
	}
	for _, attempt := range attempts {
		worker.attempts[attempt.AttemptID] = attempt
	}
	return worker
}

func (worker *fakeAttemptWorker) ListDueTasks(context.Context, time.Time, int32, bool) ([]DatasetImportTask, error) {
	panic("not used by the reconciler")
}

func (worker *fakeAttemptWorker) AcquireTaskLease(context.Context, string, string, string, time.Duration) (DatasetImportTask, error) {
	panic("not used by the reconciler")
}

func (worker *fakeAttemptWorker) ReleaseTaskLease(context.Context, string, string, string, int64) error {
	panic("not used by the reconciler")
}

func (worker *fakeAttemptWorker) CountRunningAttempts(context.Context) (int64, error) {
	panic("not used by the reconciler")
}

func (worker *fakeAttemptWorker) InsertAttempt(context.Context, DatasetImportAttempt) (DatasetImportAttempt, error) {
	panic("not used by the reconciler")
}

func (worker *fakeAttemptWorker) SetCurrentAttempt(_ context.Context, _, _, attemptID string) (DatasetImportTask, error) {
	worker.bound = append(worker.bound, attemptID)
	if worker.bindErr != nil {
		return DatasetImportTask{}, worker.bindErr
	}
	return DatasetImportTask{}, nil
}

func (worker *fakeAttemptWorker) ListDueAttempts(context.Context, time.Time, int32) ([]DatasetImportAttempt, error) {
	out := make([]DatasetImportAttempt, 0, len(worker.attempts))
	for _, attempt := range worker.attempts {
		out = append(out, attempt)
	}
	return out, nil
}

func (worker *fakeAttemptWorker) GetAttempt(_ context.Context, _, attemptID string) (DatasetImportAttempt, error) {
	attempt, ok := worker.attempts[attemptID]
	if !ok {
		return DatasetImportAttempt{}, ErrImportStateConflict
	}
	return attempt, nil
}

func (worker *fakeAttemptWorker) AcquireAttemptLease(_ context.Context, _, attemptID, _ string, _ time.Duration) (DatasetImportAttempt, error) {
	attempt, ok := worker.attempts[attemptID]
	if !ok {
		return DatasetImportAttempt{}, ErrAttemptLeaseLost
	}
	worker.leases[attemptID]++
	attempt.ReconcileEpoch = worker.leases[attemptID]
	return attempt, nil
}

func (worker *fakeAttemptWorker) ReleaseAttemptLease(context.Context, string, string, string, int64) error {
	return nil
}

func (worker *fakeAttemptWorker) ClaimAttemptCleanup(_ context.Context, _, attemptID string) (DatasetImportAttempt, error) {
	attempt := worker.attempts[attemptID]
	if attempt.WritersStoppedAt == nil || attempt.DispatchClosedAt == nil {
		return DatasetImportAttempt{}, ErrImportStateConflict
	}
	attempt.CleanupState = "RUNNING"
	worker.attempts[attemptID] = attempt
	return attempt, nil
}

func (worker *fakeAttemptWorker) FinishAttemptCleanup(_ context.Context, _, attemptID string) (DatasetImportAttempt, error) {
	attempt := worker.attempts[attemptID]
	attempt.CleanupState = "DONE"
	worker.attempts[attemptID] = attempt
	worker.cleaned = append(worker.cleaned, attemptID)
	return attempt, nil
}

func (worker *fakeAttemptWorker) DeferAttemptReconcile(_ context.Context, _, attemptID string, _ time.Duration) error {
	worker.deferred = append(worker.deferred, attemptID)
	return nil
}

func (worker *fakeAttemptWorker) FreezeCandidate(context.Context, string, string, string, string) (DatasetImportAttempt, error) {
	panic("not used by the reconciler")
}

func (worker *fakeAttemptWorker) CloseAttempt(_ context.Context, _, attemptID, _ string) (DatasetImportAttempt, error) {
	attempt := worker.attempts[attemptID]
	if attempt.DispatchClosedAt != nil {
		return DatasetImportAttempt{}, ErrImportStateConflict
	}
	at := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	attempt.DispatchClosedAt = &at
	worker.attempts[attemptID] = attempt
	worker.closed = append(worker.closed, attemptID)
	return attempt, nil
}

func (worker *fakeAttemptWorker) ConfirmWritersStopped(_ context.Context, _, attemptID string) (DatasetImportAttempt, error) {
	attempt := worker.attempts[attemptID]
	if attempt.DispatchClosedAt == nil || attempt.WritersStoppedAt != nil {
		return DatasetImportAttempt{}, ErrImportStateConflict
	}
	at := time.Date(2026, 11, 11, 0, 1, 0, 0, time.UTC)
	attempt.WritersStoppedAt = &at
	worker.attempts[attemptID] = attempt
	worker.stopped = append(worker.stopped, attemptID)
	return attempt, nil
}

// fakeAttemptTasks serves the one task lookup the reconciler performs. get, when
// set, answers instead of the map so a test can vary the answer between reads.
type fakeAttemptTasks struct {
	tasks map[string]DatasetImportTask
	get   func() DatasetImportTask
}

func (tasks *fakeAttemptTasks) GetImportTask(_ context.Context, _, taskID string) (DatasetImportTask, error) {
	if tasks.get != nil {
		return tasks.get(), nil
	}
	task, ok := tasks.tasks[taskID]
	if !ok {
		return DatasetImportTask{}, ErrImportTaskNotFound
	}
	return task, nil
}

func (tasks *fakeAttemptTasks) ListImportTasks(context.Context, DatasetImportFilter) (DatasetImportTaskPage, error) {
	panic("not used by the reconciler")
}

// fakeAttemptWorkloads reports one fixed observation and records deletions.
type fakeAttemptWorkloads struct {
	status  ImportWorkloadStatus
	err     error
	deleted []string
}

func (workloads *fakeAttemptWorkloads) EnsureImporter(context.Context, DatasetImportAttempt, AttemptPlan, DatasetImportTask, TemporaryStorageCredentials) (ImportWorkload, error) {
	panic("not used by the reconciler")
}

func (workloads *fakeAttemptWorkloads) ResumeImporter(context.Context, DatasetImportAttempt, string) error {
	panic("not used by the reconciler")
}

func (workloads *fakeAttemptWorkloads) ObserveImporter(_ context.Context, attempt DatasetImportAttempt) (ImportWorkloadStatus, error) {
	if workloads.err != nil {
		return ImportWorkloadStatus{}, workloads.err
	}
	return workloads.status, nil
}

func (workloads *fakeAttemptWorkloads) DeleteImporter(_ context.Context, attempt DatasetImportAttempt) error {
	workloads.deleted = append(workloads.deleted, attempt.AttemptID)
	return nil
}

func newTestReconciler(t *testing.T, worker *fakeAttemptWorker, tasks *fakeAttemptTasks, workloads *fakeAttemptWorkloads) *AttemptReconciler {
	t.Helper()
	reconciler, err := NewAttemptReconciler(AttemptReconcilerDeps{
		Worker: worker, Tasks: tasks, Workloads: workloads,
		Owner: "modeldev:service:test", Interval: time.Second,
		Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("new reconciler: %v", err)
	}
	return reconciler
}

func reconcilerFixture() (DatasetImportAttempt, DatasetImportTask) {
	attempt := DatasetImportAttempt{
		TenantID: schedulerTenant, AttemptID: "88888888-8888-4888-8888-888888888888",
		DatasetID:     "33333333-3333-4333-8333-333333333333",
		TaskID:        "22222222-2222-4222-8222-222222222222",
		NamespaceName: "tenant-" + schedulerTenant, JobName: "md-import-job",
		CleanupState: "PENDING",
	}
	task := schedulerTask()
	task.State = TaskRunning
	task.CurrentAttemptID = attempt.AttemptID
	return attempt, task
}

// An attempt allocated but never bound must be bound, not closed: the task is
// still running and this attempt is the one it should drive.
func TestReconcilerBindsAnUnboundAttempt(t *testing.T) {
	attempt, task := reconcilerFixture()
	task.CurrentAttemptID = ""
	worker := newFakeAttemptWorker(attempt)
	tasks := &fakeAttemptTasks{tasks: map[string]DatasetImportTask{task.TaskID: task}}
	reconciler := newTestReconciler(t, worker, tasks, &fakeAttemptWorkloads{})

	result, err := reconciler.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Bound != 1 || len(worker.bound) != 1 || worker.bound[0] != attempt.AttemptID {
		t.Fatalf("bound = %v, result = %+v", worker.bound, result)
	}
	if len(worker.closed) != 0 {
		t.Fatalf("a bindable attempt was closed: %v", worker.closed)
	}
}

// A bind conflict is not a failure when the task now names this attempt: the
// other party bound the same row, so there is nothing to close. Treating the
// conflict as failure is what left a slot taken with no Job behind it.
func TestReconcilerTreatsAConcurrentBindOfTheSameAttemptAsSuccess(t *testing.T) {
	attempt, task := reconcilerFixture()
	task.CurrentAttemptID = ""
	worker := newFakeAttemptWorker(attempt)
	worker.bindErr = ErrImportStateConflict
	// The re-read shows the attempt bound by the other party.
	tasks := &fakeAttemptTasks{tasks: map[string]DatasetImportTask{task.TaskID: task}}
	tasks.tasks[task.TaskID] = task
	reconciler := newTestReconciler(t, worker, tasks, &fakeAttemptWorkloads{})

	// The first read sees no binding; the second (after the conflict) sees this
	// attempt, which is what a concurrent bind leaves behind.
	reads := 0
	tasks.get = func() DatasetImportTask {
		reads++
		if reads == 1 {
			return task
		}
		bound := task
		bound.CurrentAttemptID = attempt.AttemptID
		return bound
	}

	result, err := reconciler.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Bound != 1 {
		t.Fatalf("a concurrent bind of the same attempt was not accepted: %+v", result)
	}
	if len(worker.closed) != 0 {
		t.Fatalf("the attempt was closed after a successful bind: %v", worker.closed)
	}
}

// A skipped attempt must leave the current batch. Without this a handful of rows
// that can never progress fill every batch and starve the ones that could.
func TestReconcilerDefersEverySkippedAttempt(t *testing.T) {
	attempt, task := reconcilerFixture()
	// The scheduler owns this attempt's dispatch, so the reconciler skips it.
	worker := newFakeAttemptWorker(attempt)
	tasks := &fakeAttemptTasks{tasks: map[string]DatasetImportTask{task.TaskID: task}}
	reconciler := newTestReconciler(t, worker, tasks, &fakeAttemptWorkloads{})

	result, err := reconciler.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Skipped != 1 {
		t.Fatalf("result = %+v", result)
	}
	if len(worker.deferred) != 1 || worker.deferred[0] != attempt.AttemptID {
		t.Fatalf("a skipped attempt stayed in the batch: %v", worker.deferred)
	}
}

// A worker that died mid-delete leaves cleanup_state RUNNING. The next lease
// holder must be able to finish it; accepting only PENDING/WAITING left the
// attempt RUNNING forever and its objects were never collected.
func TestReconcilerResumesAnInterruptedCleanup(t *testing.T) {
	attempt, task := reconcilerFixture()
	closed := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	stopped := time.Date(2026, 11, 11, 0, 1, 0, 0, time.UTC)
	attempt.DispatchClosedAt = &closed
	attempt.WritersStoppedAt = &stopped
	attempt.CleanupState = "RUNNING"
	worker := newFakeAttemptWorker(attempt)
	tasks := &fakeAttemptTasks{tasks: map[string]DatasetImportTask{task.TaskID: task}}
	workloads := &fakeAttemptWorkloads{status: ImportWorkloadStatus{Present: false}}
	reconciler := newTestReconciler(t, worker, tasks, workloads)

	result, err := reconciler.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Cleaned != 1 || len(worker.cleaned) != 1 {
		t.Fatalf("an interrupted cleanup was not resumed: %+v cleaned=%v", result, worker.cleaned)
	}
	if len(workloads.deleted) != 1 {
		t.Fatalf("the temporary objects were not deleted: %v", workloads.deleted)
	}
}

// A closed attempt whose Job is still running must not be marked stopped. The
// slot stays taken until the Job is observed finished or gone.
func TestReconcilerWaitsForALiveWriter(t *testing.T) {
	attempt, task := reconcilerFixture()
	closed := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	attempt.DispatchClosedAt = &closed
	worker := newFakeAttemptWorker(attempt)
	tasks := &fakeAttemptTasks{tasks: map[string]DatasetImportTask{task.TaskID: task}}
	workloads := &fakeAttemptWorkloads{status: ImportWorkloadStatus{Present: true, Finished: false}}
	reconciler := newTestReconciler(t, worker, tasks, workloads)

	result, err := reconciler.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(worker.stopped) != 0 {
		t.Fatalf("a live writer was marked stopped: %v", worker.stopped)
	}
	if result.Stopped != 0 || result.Skipped != 1 {
		t.Fatalf("result = %+v", result)
	}
	if len(workloads.deleted) != 1 {
		t.Fatalf("a live writer was not asked to stop: %v", workloads.deleted)
	}
}

// A closed attempt whose Job is gone proves no writer survives, so the slot is
// released and the temporary objects are removed.
func TestReconcilerReleasesTheSlotAndCleansUp(t *testing.T) {
	attempt, task := reconcilerFixture()
	closed := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	attempt.DispatchClosedAt = &closed
	worker := newFakeAttemptWorker(attempt)
	tasks := &fakeAttemptTasks{tasks: map[string]DatasetImportTask{task.TaskID: task}}
	workloads := &fakeAttemptWorkloads{status: ImportWorkloadStatus{Present: false}}
	reconciler := newTestReconciler(t, worker, tasks, workloads)

	result, err := reconciler.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(worker.stopped) != 1 {
		t.Fatalf("writer stop not recorded: %v", worker.stopped)
	}
	if len(worker.cleaned) != 1 {
		t.Fatalf("temporary objects not cleaned: %v", worker.cleaned)
	}
	if result.Stopped != 1 || result.Cleaned != 1 {
		t.Fatalf("result = %+v", result)
	}
}

// A succeeded task's READY version and frozen candidate are durable assets in
// object storage. Its Job, PVC and Secret are temporary and must still be
// collected, or every successful import leaks a workload.
func TestReconcilerCollectsTemporaryObjectsOfASucceededTask(t *testing.T) {
	attempt, task := reconcilerFixture()
	closed := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	stopped := time.Date(2026, 11, 11, 0, 1, 0, 0, time.UTC)
	attempt.DispatchClosedAt = &closed
	attempt.WritersStoppedAt = &stopped
	task.State = TaskSucceeded
	task.CandidateAttemptID = attempt.AttemptID
	worker := newFakeAttemptWorker(attempt)
	tasks := &fakeAttemptTasks{tasks: map[string]DatasetImportTask{task.TaskID: task}}
	workloads := &fakeAttemptWorkloads{status: ImportWorkloadStatus{Present: false}}
	reconciler := newTestReconciler(t, worker, tasks, workloads)

	result, err := reconciler.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(worker.cleaned) != 1 || len(workloads.deleted) != 1 {
		t.Fatalf("a succeeded task leaked its temporary objects: cleaned=%v deleted=%v", worker.cleaned, workloads.deleted)
	}
	if result.Cleaned != 1 {
		t.Fatalf("result = %+v", result)
	}
}

// An observation failure must defer, never assume the writer stopped.
func TestReconcilerDefersOnObservationFailure(t *testing.T) {
	attempt, task := reconcilerFixture()
	closed := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	attempt.DispatchClosedAt = &closed
	worker := newFakeAttemptWorker(attempt)
	tasks := &fakeAttemptTasks{tasks: map[string]DatasetImportTask{task.TaskID: task}}
	workloads := &fakeAttemptWorkloads{err: ErrImportDispatchUnavailable}
	reconciler := newTestReconciler(t, worker, tasks, workloads)

	result, err := reconciler.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(worker.stopped) != 0 {
		t.Fatalf("an unobserved writer was marked stopped: %v", worker.stopped)
	}
	if result.Skipped != 1 {
		t.Fatalf("result = %+v", result)
	}
}

// A missing task row must not lead to deleting objects nobody can attribute.
func TestReconcilerSkipsAnAttemptWithoutItsTask(t *testing.T) {
	attempt, _ := reconcilerFixture()
	worker := newFakeAttemptWorker(attempt)
	tasks := &fakeAttemptTasks{tasks: map[string]DatasetImportTask{}}
	workloads := &fakeAttemptWorkloads{status: ImportWorkloadStatus{Present: false}}
	reconciler := newTestReconciler(t, worker, tasks, workloads)

	result, err := reconciler.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(worker.closed) != 0 || len(worker.cleaned) != 0 || len(workloads.deleted) != 0 {
		t.Fatalf("an unattributable attempt was acted on: closed=%v cleaned=%v deleted=%v",
			worker.closed, worker.cleaned, workloads.deleted)
	}
	if result.Skipped != 1 {
		t.Fatalf("result = %+v", result)
	}
}

// The reconciler must require every port: a missing one is a configuration
// error, never a silently degraded loop.
func TestReconcilerRequiresEveryPort(t *testing.T) {
	if _, err := NewAttemptReconciler(AttemptReconcilerDeps{}); !errors.Is(err, ErrImportWorkerConfiguration) {
		t.Fatalf("empty deps accepted: %v", err)
	}
}

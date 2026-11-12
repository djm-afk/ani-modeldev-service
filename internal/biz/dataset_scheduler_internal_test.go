package biz

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

const schedulerTenant = "11111111-1111-4111-8111-111111111111"

func schedulerTask() DatasetImportTask {
	return DatasetImportTask{
		TenantID: schedulerTenant, TaskID: "22222222-2222-4222-8222-222222222222",
		DatasetID:              "33333333-3333-4333-8333-333333333333",
		ReservedInputVersionID: "44444444-4444-4444-8444-444444444444",
		InputRequestID:         "55555555-5555-4555-8555-555555555555",
		VersionNumber:          1, AcceptedAt: time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC),
		SourceProvider: "MODELSCOPE", SourceSnapshotSHA256: fillerHex('a', 64),
		SourceSnapshot:    []byte(`{"schema":"ani.modeldev.dataset.source-snapshot.v1","provider":"MODELSCOPE","repo_type":"dataset","repo_id":"org/ds","requested_revision":"master","file_path":"data.json","lock_kind":"FILE_SHA256","resolved_revision":"master","expected_size_bytes":"116","expected_file_sha256":"` + fillerHex('b', 64) + `","resolved_at":"2026-11-04T09:00:00.000000Z"}`),
		ExpectedSizeBytes: 116, AcceptedLimitBytes: 1 << 30,
		StorageConnectionID: "store-1", Bucket: "inputs-bucket",
		ApprovedPrefix: "inputs/" + schedulerTenant, InputScopeSHA256: fillerHex('c', 64),
		CredentialReference: "cred-1", ClusterID: "66666666-6666-4666-8666-666666666666",
		NamespaceName: "tenant-" + schedulerTenant, NamespaceUID: "77777777-7777-4777-8777-777777777777",
		State: TaskQueued, Phase: PhaseWaiting, RecoveryGeneration: 1,
	}
}

func fillerHex(r byte, n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = r
	}
	return string(out)
}

// The plan must be decodable by the importer's strict decoder shape: fixed
// schema, nested data/result objects and a microsecond deadline.
func TestImportPlanEncodingIsStableAndNested(t *testing.T) {
	planner, err := NewImportPlanPlanner(fillerHex('d', 64), "/var/run/credentials/credentials.json", 30*time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	task := schedulerTask()
	first, err := planner.PlanAttempt(context.Background(), task, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewImportPlanPlanner(fillerHex('d', 64), "/var/run/credentials/credentials.json", 30*time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := second.PlanAttempt(context.Background(), task, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.RuntimePlan) != string(again.RuntimePlan) || first.RuntimePlanSHA256 != again.RuntimePlanSHA256 {
		t.Fatal("plan is not deterministic for the same frozen task")
	}
	var decoded map[string]any
	if err := json.Unmarshal(first.RuntimePlan, &decoded); err != nil {
		t.Fatalf("plan is not valid JSON: %v", err)
	}
	for _, key := range []string{"schema", "tenant_id", "dataset_id", "task_id", "input_version_id", "source_snapshot_sha256", "runtime_plan_sha256", "importer_image_digest", "provider", "repo_id", "requested_revision", "file_path", "lock_kind", "expected_size_bytes", "credential_file", "deadline", "data", "result"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("plan is missing %q", key)
		}
	}
	// input_scope-style dotted keys were a real bug once; the same mistake here
	// would silently produce a plan the importer cannot decode.
	for key := range decoded {
		if len(key) > 7 && key[:8] == "data.key" {
			t.Fatalf("dotted key leaked into the plan: %q", key)
		}
	}
	data, ok := decoded["data"].(map[string]any)
	if !ok || data["key"] == "" || data["bucket"] == "" {
		t.Fatalf("data object is not nested: %#v", decoded["data"])
	}
	// The importer rejects a plan without an attempt id, so the encoder must
	// carry the allocated attempt rather than an empty placeholder.
	if decoded["attempt_id"] != "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" {
		t.Fatalf("plan attempt_id is %v", decoded["attempt_id"])
	}
	if first.DataObjectKey != data["key"] {
		t.Fatalf("frozen key differs from the encoded plan: %q vs %v", first.DataObjectKey, data["key"])
	}
	// The digest covers the plan without itself.
	if decoded["runtime_plan_sha256"] != first.RuntimePlanSHA256 {
		t.Fatal("runtime_plan_sha256 does not match the recorded digest")
	}
}

// The object layout is per attempt, so a retry never overwrites the previous
// attempt's bytes.
func TestImportObjectKeysArePerAttempt(t *testing.T) {
	task := schedulerTask()
	one, err := ImporterDataObjectKey(task, 1)
	if err != nil {
		t.Fatal(err)
	}
	two, err := ImporterDataObjectKey(task, 2)
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Fatal("attempts share a data key")
	}
	result, err := ImporterResultObjectKey(task, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result == one || result == two {
		t.Fatal("result key collides with a data key")
	}
	// A prefix that is not this tenant's is not a legal layout.
	foreign := task
	foreign.ApprovedPrefix = "inputs/99999999-9999-4999-8999-999999999999"
	if _, err := ImporterDataObjectKey(foreign, 1); err == nil {
		t.Fatal("foreign tenant prefix accepted into the object layout")
	}
}

// A content verdict must never be treated as retryable: re-downloading the same
// bytes cannot fix an invalid file.
func TestSchedulerClassifiesRetryability(t *testing.T) {
	scheduler := &ImportScheduler{interval: time.Second}
	for _, cause := range []error{ErrImportDispatchUnavailable, ErrImportQuotaExhausted, ErrSourceUnavailable, ErrTaskLeaseLost} {
		if !scheduler.retryable(cause) {
			t.Fatalf("%v should be retryable", cause)
		}
	}
	for _, cause := range []error{ErrInputVerification, ErrSourceChanged, ErrSourceNotFound, ErrDatasetInvalidMetadata} {
		if scheduler.retryable(cause) {
			t.Fatalf("%v must not be retryable", cause)
		}
	}
	if failureCodeFor(ErrSourceChanged) != string(FailSourceChanged) {
		t.Fatal("a changed source must report SOURCE_CHANGED")
	}
}

// A publish that loses a lifecycle race must name the race. Reporting it as
// IMPORT_PERSISTENCE_UNAVAILABLE told operators the store was broken when the
// collection had simply been archived or the task had already moved on.
func TestFailureCodeNamesLifecycleRaces(t *testing.T) {
	if got := failureCodeFor(ErrDatasetArchived); got != string(FailDatasetArchived) {
		t.Fatalf("archived dataset reported %s, want DATASET_ARCHIVED", got)
	}
	if got := failureCodeFor(ErrImportStateConflict); got != string(FailImportStateConflict) {
		t.Fatalf("state conflict reported %s, want IMPORT_STATE_CONFLICT", got)
	}
}

// A report's own FAILED verdict must survive classification: the job is the only
// party that observed what went wrong, so its code is the one the page branches
// on. Rewriting it to a generic persistence error erases the reason.
func TestFailureCodeKeepsTheReportedVerdict(t *testing.T) {
	for _, code := range []ImportFailureCode{FailSourceChanged, FailSourceNotFound, FailSourceAuthRequired,
		FailDependencyUnavailable, FailResourceUnavailable, FailTimeout, FailStorageVersioningRequired} {
		if got := failureCodeFor(errors.New(string(code))); got != string(code) {
			t.Fatalf("reported %s became %s", code, got)
		}
	}
	if got := failureCodeFor(ErrInputVerification); got != string(FailDatasetInvalid) {
		t.Fatalf("a content rejection became %s, want %s", got, FailDatasetInvalid)
	}
	if got := failureCodeFor(ErrImportResultInvalid); got != string(FailImportResultInvalid) {
		t.Fatalf("an unreadable report became %s, want %s", got, FailImportResultInvalid)
	}
	if got := failureCodeFor(errors.New("something else")); got != string(FailPersistence) {
		t.Fatalf("an unknown cause became %s, want %s", got, FailPersistence)
	}
}

// The scheduler must refuse an assembly missing any port rather than run a
// half-wired loop.
func TestSchedulerRequiresEveryPort(t *testing.T) {
	if _, err := NewImportScheduler(ImportSchedulerDeps{}); !errors.Is(err, ErrImportWorkerConfiguration) {
		t.Fatalf("empty assembly accepted: %v", err)
	}
}

// A task with no bound attempt, no frozen candidate and no pending cancel can
// only be dispatched. While the shared quota is full such a row must be skipped
// so the batch is spent on the rows that observe a running Job and return a
// slot.
func TestSweepSkipsDispatchOnlyRowsWhenQuotaIsFull(t *testing.T) {
	queued := schedulerTask()
	queued.TaskID = "99999999-9999-4999-8999-999999999999"
	// A pending cancel is observation work: it must be processed even while the
	// quota is full, because converging it is what returns a slot.
	canceling := schedulerTask()
	canceling.TaskID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	canceling.State = TaskCanceling
	canceling.Phase = PhaseStopping
	canceledAt := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	canceling.CancelRequestedAt = &canceledAt

	worker := &fakeSweepWorker{tasks: []DatasetImportTask{queued, canceling}, running: 1}
	scheduler := newTestScheduler(t, worker, 1)

	result, err := scheduler.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if worker.includeDispatch {
		t.Fatal("a full quota still asked for dispatch rows")
	}
	if result.Scanned != 1 {
		t.Fatalf("scanned = %d, want only the observation row", result.Scanned)
	}
	if len(worker.advanced) != 1 || worker.advanced[0] != canceling.TaskID {
		t.Fatalf("advanced = %v, want the canceling task", worker.advanced)
	}
}

// With a free slot the same sweep must consider dispatch rows too.
func TestSweepIncludesDispatchRowsWhenQuotaHasRoom(t *testing.T) {
	queued := schedulerTask()
	worker := &fakeSweepWorker{tasks: []DatasetImportTask{queued}, running: 0}
	scheduler := newTestScheduler(t, worker, 3)

	if _, err := scheduler.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !worker.includeDispatch {
		t.Fatal("a free quota did not ask for dispatch rows")
	}
	if len(worker.advanced) != 1 {
		t.Fatalf("advanced = %v", worker.advanced)
	}
}

// A quota read failure must degrade to observation only: starting work on an
// unknown budget would overrun the shared limit.
func TestSweepDegradesToObservationWhenQuotaIsUnknown(t *testing.T) {
	queued := schedulerTask()
	worker := &fakeSweepWorker{tasks: []DatasetImportTask{queued}, countErr: ErrPersistence}
	scheduler := newTestScheduler(t, worker, 3)

	if _, err := scheduler.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if worker.includeDispatch {
		t.Fatal("an unknown quota still asked for dispatch rows")
	}
}

// The automatic retry budget is DeferCount, not RecoveryGeneration: the latter
// only advances on an explicit user retry.
func TestAutomaticRetryBudgetUsesDeferCount(t *testing.T) {
	task := schedulerTask()
	task.State = TaskRunning
	task.RecoveryGeneration = 1
	task.DeferCount = maxAutomaticRetries
	worker := &fakeSweepWorker{}
	scheduler := newTestScheduler(t, worker, 1)

	if _, err := scheduler.recordError(context.Background(), task, ErrSourceUnavailable, taskOutcome{}); err == nil {
		t.Fatal("a spent budget reported no cause")
	}
	if worker.deferred != 0 {
		t.Fatalf("a spent budget deferred again: %d", worker.deferred)
	}
	if worker.failed != 1 {
		t.Fatalf("a spent budget did not fail the task: %d", worker.failed)
	}

	task.DeferCount = 0
	if _, err := scheduler.recordError(context.Background(), task, ErrSourceUnavailable, taskOutcome{}); err == nil {
		t.Fatal("a fresh budget reported no cause")
	}
	if worker.deferred != 1 {
		t.Fatalf("a fresh budget did not defer: %d", worker.deferred)
	}
}

// A closed attempt whose writer has not been observed as stopped must not be
// replaced. Starting a replacement while the previous Pod may still be uploading
// puts two writers on the same task.
func TestPrepareAttemptWaitsForThePreviousWriter(t *testing.T) {
	closed := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	attempt := DatasetImportAttempt{
		TenantID: "11111111-1111-4111-8111-111111111111", AttemptID: "22222222-2222-4222-8222-222222222222",
		TaskID: "33333333-3333-4333-8333-333333333333", AttemptNo: 1,
		DispatchClosedAt: &closed,
		DeadlineAt:       closed.Add(time.Hour),
	}
	task := schedulerTask()
	task.State = TaskRunning
	task.CurrentAttemptID = attempt.AttemptID
	worker := &fakeSweepWorker{attempt: attempt}
	scheduler := newTestScheduler(t, worker, 3)

	if _, _, err := scheduler.prepareAttempt(context.Background(), task); !errors.Is(err, errAttemptWriterRunning) {
		t.Fatalf("a replacement started before the writer stopped: %v", err)
	}
	if worker.cleared != 0 {
		t.Fatalf("the attempt was detached before its writer stopped: %d", worker.cleared)
	}

	// Once the stop is observed, the replacement may proceed. Allocation itself is
	// out of scope here, so a quota refusal is the expected next step.
	stopped := closed.Add(time.Minute)
	worker.attempt.WritersStoppedAt = &stopped
	if _, _, err := scheduler.prepareAttempt(context.Background(), task); errors.Is(err, errAttemptWriterRunning) {
		t.Fatal("a stopped writer still blocked the replacement")
	}
	if worker.cleared != 1 {
		t.Fatalf("the stopped attempt was not detached: %d", worker.cleared)
	}
}

// An attempt that already recorded a Job must be observed, not dispatched again.
// Re-running the dispatch steps issued a second STS session and re-created
// objects for a workload that was already running.
func TestDispatchObservesAnAttemptThatAlreadyHasAJob(t *testing.T) {
	attempt := DatasetImportAttempt{
		TenantID: "11111111-1111-4111-8111-111111111111", AttemptID: "22222222-2222-4222-8222-222222222222",
		TaskID: "33333333-3333-4333-8333-333333333333", AttemptNo: 1,
		JobUID:     "job-uid-1",
		DeadlineAt: time.Date(2026, 11, 11, 1, 0, 0, 0, time.UTC),
	}
	task := schedulerTask()
	task.State = TaskRunning
	task.CurrentAttemptID = attempt.AttemptID
	worker := &fakeSweepWorker{attempt: attempt}
	scheduler := newTestScheduler(t, worker, 3)

	outcome, err := scheduler.dispatch(context.Background(), task, taskOutcome{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if outcome.skipped != 1 || outcome.dispatched != 0 {
		t.Fatalf("a running attempt was dispatched again: %+v", outcome)
	}
}

// A report carries a code, not an error value. Rebuilding it with errors.New
// lost the identity retryable() matches on, so a transient download failure
// became permanent on the first attempt.
func TestReportedFailureCodeKeepsItsRetryability(t *testing.T) {
	scheduler := &ImportScheduler{interval: time.Second}
	// A throttled or failing hub is transient: the same request may succeed.
	for _, code := range []ImportFailureCode{FailDependencyUnavailable, FailResourceUnavailable} {
		cause := causeForFailureCode(code)
		if !scheduler.retryable(cause) {
			t.Fatalf("%s was not retryable after translation", code)
		}
		if got := failureCodeFor(cause); got != string(code) {
			t.Fatalf("%s became %s", code, got)
		}
	}
	// A timeout and every content verdict stay terminal: repeating them cannot
	// change the outcome.
	for _, code := range []ImportFailureCode{FailTimeout, FailSourceNotFound, FailSourceAuthRequired, FailSourceChanged, FailDatasetInvalid} {
		cause := causeForFailureCode(code)
		if scheduler.retryable(cause) {
			t.Fatalf("%s became retryable", code)
		}
		if got := failureCodeFor(cause); got != string(code) {
			t.Fatalf("%s became %s", code, got)
		}
	}
}

// fakeSweepWorker serves the scheduler's scan and records what it was asked for.
type fakeSweepWorker struct {
	tasks           []DatasetImportTask
	attempt         DatasetImportAttempt
	running         int64
	countErr        error
	includeDispatch bool
	advanced        []string
	deferred        int
	failed          int
	cleared         int
}

func (worker *fakeSweepWorker) ListDueTasks(_ context.Context, _ time.Time, _ int32, includeDispatch bool) ([]DatasetImportTask, error) {
	worker.includeDispatch = includeDispatch
	out := make([]DatasetImportTask, 0, len(worker.tasks))
	for _, task := range worker.tasks {
		if !includeDispatch && task.needsDispatchOnly() {
			continue
		}
		out = append(out, task)
	}
	return out, nil
}

func (worker *fakeSweepWorker) CountRunningAttempts(context.Context) (int64, error) {
	if worker.countErr != nil {
		return 0, worker.countErr
	}
	return worker.running, nil
}

func (worker *fakeSweepWorker) AcquireTaskLease(_ context.Context, _, taskID, _ string, _ time.Duration) (DatasetImportTask, error) {
	worker.advanced = append(worker.advanced, taskID)
	for _, task := range worker.tasks {
		if task.TaskID == taskID {
			return task, nil
		}
	}
	return DatasetImportTask{}, ErrTaskLeaseLost
}

func (worker *fakeSweepWorker) ReleaseTaskLease(context.Context, string, string, string, int64) error {
	return nil
}

func (worker *fakeSweepWorker) DeferTask(context.Context, TaskDeferral) error {
	worker.deferred++
	return nil
}

func (worker *fakeSweepWorker) FailTask(context.Context, TaskFailure) (DatasetImportTask, error) {
	worker.failed++
	return DatasetImportTask{}, nil
}

func (worker *fakeSweepWorker) ListDueAttempts(context.Context, time.Time, int32) ([]DatasetImportAttempt, error) {
	panic("not used by the scheduler")
}

func (worker *fakeSweepWorker) GetAttempt(_ context.Context, _, attemptID string) (DatasetImportAttempt, error) {
	if worker.attempt.AttemptID == attemptID {
		return worker.attempt, nil
	}
	// A canceling task with no attempt converges without one.
	return DatasetImportAttempt{}, ErrImportStateConflict
}

func (worker *fakeSweepWorker) AcquireAttemptLease(context.Context, string, string, string, time.Duration) (DatasetImportAttempt, error) {
	panic("not used by the scheduler")
}

func (worker *fakeSweepWorker) ReleaseAttemptLease(context.Context, string, string, string, int64) error {
	panic("not used by the scheduler")
}

func (worker *fakeSweepWorker) ClaimAttemptCleanup(context.Context, string, string) (DatasetImportAttempt, error) {
	panic("not used by the scheduler")
}

func (worker *fakeSweepWorker) FinishAttemptCleanup(context.Context, string, string) (DatasetImportAttempt, error) {
	panic("not used by the scheduler")
}

func (worker *fakeSweepWorker) DeferAttemptReconcile(context.Context, string, string, time.Duration) error {
	panic("not used by the scheduler")
}

func (worker *fakeSweepWorker) FreezeCandidate(context.Context, string, string, string, string) (DatasetImportAttempt, error) {
	panic("not used by the scheduler")
}

func (worker *fakeSweepWorker) CloseAttempt(context.Context, string, string, string) (DatasetImportAttempt, error) {
	panic("not used by the scheduler")
}

func (worker *fakeSweepWorker) ConfirmWritersStopped(context.Context, string, string) (DatasetImportAttempt, error) {
	panic("not used by the scheduler")
}

func (worker *fakeSweepWorker) InsertAttempt(context.Context, DatasetImportAttempt) (DatasetImportAttempt, error) {
	panic("not used by the scheduler")
}

func (worker *fakeSweepWorker) SetCurrentAttempt(context.Context, string, string, string) (DatasetImportTask, error) {
	panic("not used by the scheduler")
}

// newTestScheduler builds a scheduler whose only real dependency is the worker.
// The remaining ports are never reached by the paths these tests exercise.
func newTestScheduler(t *testing.T, worker *fakeSweepWorker, capacity int) *ImportScheduler {
	t.Helper()
	scheduler, err := NewImportScheduler(ImportSchedulerDeps{
		Repository: &fakeSweepRepository{worker: worker}, Worker: worker,
		Planner: &fakeSweepPlanner{}, Workloads: &fakeSweepWorkloads{},
		Objects: &fakeSweepObjects{}, Issuer: &fakeSweepIssuer{},
		Publication: &fakeSweepPublication{}, Operations: &fakeSweepOperations{},
		Environments: &fakeSweepEnvironments{},
		Owner:        "modeldev:service:test", Interval: time.Second, Capacity: capacity,
		ImportTimeout: time.Minute,
		Now:           func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	return scheduler
}

type fakeSweepRepository struct{ worker *fakeSweepWorker }

func (repo *fakeSweepRepository) AllocateAttempt(context.Context, AttemptAllocationRequest) (DatasetImportAttempt, error) {
	// These tests assert the scan split and the writer gate, not allocation.
	return DatasetImportAttempt{}, ErrImportQuotaExhausted
}

func (repo *fakeSweepRepository) StartTask(context.Context, string, string, string, int64) (DatasetImportTask, error) {
	// These tests assert the scan split, not the dispatch chain. A state
	// conflict ends the row through the normal error path.
	return DatasetImportTask{}, ErrImportStateConflict
}

func (repo *fakeSweepRepository) SetCurrentAttempt(context.Context, string, string, string) (DatasetImportTask, error) {
	panic("not used by these tests")
}

func (repo *fakeSweepRepository) FreezeTaskCandidate(context.Context, CandidateFreeze) (DatasetImportTask, error) {
	panic("not used by these tests")
}

func (repo *fakeSweepRepository) FailTask(ctx context.Context, failure TaskFailure) (DatasetImportTask, error) {
	return repo.worker.FailTask(ctx, failure)
}

func (repo *fakeSweepRepository) FinishCancelledTask(context.Context, TaskCancellation) (DatasetImportTask, error) {
	return DatasetImportTask{}, nil
}

func (repo *fakeSweepRepository) DeferTask(ctx context.Context, deferral TaskDeferral) error {
	return repo.worker.DeferTask(ctx, deferral)
}

func (repo *fakeSweepRepository) CloseAttempt(context.Context, string, string, string) (DatasetImportAttempt, error) {
	panic("not used by these tests")
}

func (repo *fakeSweepRepository) ConfirmWritersStopped(context.Context, string, string) (DatasetImportAttempt, error) {
	panic("not used by these tests")
}

func (repo *fakeSweepRepository) ClearCurrentAttempt(_ context.Context, _, _, _ string) (DatasetImportTask, error) {
	repo.worker.cleared++
	task := schedulerTask()
	task.State = TaskRunning
	task.CurrentAttemptID = ""
	return task, nil
}

func (repo *fakeSweepRepository) SetTaskEnvironment(context.Context, string, string, string) (DatasetImportTask, error) {
	panic("not used by these tests")
}

func (repo *fakeSweepRepository) RecordAttemptJob(context.Context, string, string, string, string) (DatasetImportAttempt, error) {
	panic("not used by these tests")
}

func (repo *fakeSweepRepository) RecordAttemptObservation(context.Context, string, string, []byte) (DatasetImportAttempt, error) {
	panic("not used by these tests")
}

type fakeSweepPlanner struct{}

func (fakeSweepPlanner) PlanAttempt(context.Context, DatasetImportTask, string, int64) (AttemptPlan, error) {
	return AttemptPlan{}, nil
}

type fakeSweepWorkloads struct{}

func (fakeSweepWorkloads) EnsureImporter(context.Context, DatasetImportAttempt, AttemptPlan, DatasetImportTask, TemporaryStorageCredentials) (ImportWorkload, error) {
	panic("not used by these tests")
}

func (fakeSweepWorkloads) ResumeImporter(context.Context, DatasetImportAttempt, string) error {
	panic("not used by these tests")
}

func (fakeSweepWorkloads) ObserveImporter(context.Context, DatasetImportAttempt) (ImportWorkloadStatus, error) {
	// The Job is still running, so observation is a no-op for these tests.
	return ImportWorkloadStatus{Present: true, Finished: false}, nil
}

func (fakeSweepWorkloads) DeleteImporter(context.Context, DatasetImportAttempt) error {
	panic("not used by these tests")
}

type fakeSweepObjects struct{}

func (fakeSweepObjects) ReadResult(context.Context, DatasetImportTask, DatasetImportAttempt) (ImportReport, error) {
	panic("not used by these tests")
}

func (fakeSweepObjects) VerifyCandidate(context.Context, DatasetImportTask, ImportResult) (VerifiedAlpaca, []byte, error) {
	panic("not used by these tests")
}

type fakeSweepIssuer struct{}

func (fakeSweepIssuer) IssueImportSession(context.Context, ImportSessionRequest, ImportSessionGrant) (ImportSession, error) {
	panic("not used by these tests")
}

type fakeSweepPublication struct{}

func (fakeSweepPublication) ReserveImport(context.Context, InputImport) (InputVersion, error) {
	panic("not used by these tests")
}

func (fakeSweepPublication) PublishVersion(context.Context, PublicationRequest) (PublicationResult, error) {
	panic("not used by these tests")
}

type fakeSweepOperations struct{}

func (fakeSweepOperations) InsertOperation(context.Context, DatasetOperation) (DatasetOperation, error) {
	panic("not used by these tests")
}

func (fakeSweepOperations) ListOperations(context.Context, DatasetOperationFilter) (DatasetOperationPage, error) {
	panic("not used by these tests")
}

type fakeSweepEnvironments struct{}

func (fakeSweepEnvironments) EnvironmentID(context.Context, string, string) (string, error) {
	panic("not used by these tests")
}

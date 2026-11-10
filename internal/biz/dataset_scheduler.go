package biz

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// ImportScheduler drives accepted tasks to a published version. It owns only
// ordering and legality: persistence sits behind ImportSchedulerRepository and
// every external action goes through a port. Design §8.3 and §9.4 fix the
// recovery rules this loop implements.
type ImportScheduler struct {
	repository   ImportSchedulerRepository
	worker       DatasetWorkerRepository
	planner      AttemptPlanner
	workloads    DatasetImportWorkloadControl
	objects      ImportObjectReader
	issuer       ImportSessionIssuer
	publication  DatasetPublicationRepository
	operations   DatasetOperationRepository
	environments EnvironmentLookup
	now          func() time.Time
	newID        func() (string, error)

	owner         string
	interval      time.Duration
	leaseTTL      time.Duration
	capacity      int
	importTimeout time.Duration
	logf          func(message string, args ...any)
}

// ImportSchedulerDeps keeps the constructor readable. Every port is required.
type ImportSchedulerDeps struct {
	Repository   ImportSchedulerRepository
	Worker       DatasetWorkerRepository
	Planner      AttemptPlanner
	Workloads    DatasetImportWorkloadControl
	Objects      ImportObjectReader
	Issuer       ImportSessionIssuer
	Publication  DatasetPublicationRepository
	Operations   DatasetOperationRepository
	Environments EnvironmentLookup

	Owner         string
	Interval      time.Duration
	LeaseTTL      time.Duration
	Capacity      int
	ImportTimeout time.Duration
	Now           func() time.Time
	NewID         func() (string, error)
	Logf          func(message string, args ...any)
}

// NewImportScheduler validates the assembly. A missing port is a configuration
// error, never a silently degraded loop.
func NewImportScheduler(deps ImportSchedulerDeps) (*ImportScheduler, error) {
	if deps.Repository == nil || deps.Worker == nil || deps.Planner == nil || deps.Workloads == nil ||
		deps.Objects == nil || deps.Issuer == nil || deps.Publication == nil || deps.Operations == nil ||
		deps.Environments == nil ||
		deps.Owner == "" || deps.Interval <= 0 || deps.Capacity <= 0 || deps.ImportTimeout <= 0 {
		return nil, ErrImportWorkerConfiguration
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	newID := deps.NewID
	if newID == nil {
		newID = defaultUUID
	}
	ttl := deps.LeaseTTL
	if ttl <= 0 {
		ttl = 2 * deps.Interval
	}
	return &ImportScheduler{
		repository: deps.Repository, worker: deps.Worker, planner: deps.Planner,
		workloads: deps.Workloads, objects: deps.Objects, issuer: deps.Issuer,
		publication: deps.Publication, operations: deps.Operations, environments: deps.Environments,
		now: now, newID: newID, owner: deps.Owner, interval: deps.Interval,
		leaseTTL: ttl, capacity: deps.Capacity, importTimeout: deps.ImportTimeout, logf: deps.Logf,
	}, nil
}

// BatchResult is one sweep's bounded progress, reported for logs and tests.
type BatchResult struct {
	Scanned, Admitted, Dispatched, Published, Failed, Deferred, Skipped int
	LastError                                                           error
}

// Run sweeps until the context is cancelled. A failing sweep backs off but never
// stops the loop: one bad task must not freeze every other tenant.
func (scheduler *ImportScheduler) Run(ctx context.Context) error {
	if ctx == nil || scheduler == nil {
		return ErrImportWorkerConfiguration
	}
	backoff := time.Second
	for {
		_, err := scheduler.Sweep(ctx)
		if ctx.Err() != nil {
			return nil
		}
		delay := scheduler.interval
		if err != nil {
			delay = backoff
			backoff = min(backoff*2, 30*time.Second)
		} else {
			backoff = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// Sweep runs one pass. Dispatch is bounded by the shared capacity, so a sweep may
// legitimately do nothing while tasks are due.
func (scheduler *ImportScheduler) Sweep(ctx context.Context) (BatchResult, error) {
	result := BatchResult{}
	if ctx == nil || scheduler == nil {
		return result, ErrImportWorkerConfiguration
	}
	now := scheduler.now().UTC()
	tasks, err := scheduler.worker.ListDueTasks(ctx, now, 20)
	if err != nil {
		scheduler.report("list due tasks failed", "error", err)
		return result, err
	}
	scheduler.report("import sweep", "due", len(tasks))
	result.Scanned = len(tasks)
	for _, due := range tasks {
		if ctx.Err() != nil {
			return result, nil
		}
		outcome, taskErr := scheduler.advance(ctx, due, now)
		result.Admitted += outcome.admitted
		result.Dispatched += outcome.dispatched
		result.Published += outcome.published
		result.Failed += outcome.failed
		result.Deferred += outcome.deferred
		result.Skipped += outcome.skipped
		if taskErr != nil {
			result.LastError = taskErr
		}
	}
	return result, nil
}

// report emits one bounded diagnostic line. The loop has no logger of its own,
// so a silent failure here would look exactly like "nothing to do".
func (scheduler *ImportScheduler) report(message string, args ...any) {
	if scheduler.logf != nil {
		scheduler.logf(message, args...)
	}
}

type taskOutcome struct {
	admitted, dispatched, published, failed, deferred, skipped int
}

// advance handles one due task under its lease, so a second replica cannot act
// on the same task concurrently.
func (scheduler *ImportScheduler) advance(ctx context.Context, due DatasetImportTask, now time.Time) (taskOutcome, error) {
	outcome := taskOutcome{}
	leased, err := scheduler.worker.AcquireTaskLease(ctx, due.TenantID, due.TaskID, scheduler.owner, scheduler.leaseTTL)
	if err != nil {
		if errors.Is(err, ErrTaskLeaseLost) || errors.Is(err, ErrImportStateConflict) || errors.Is(err, ErrImportTaskNotFound) {
			scheduler.report("import task skipped", "task", due.TaskID, "reason", err)
			outcome.skipped++
			return outcome, nil
		}
		scheduler.report("import task lease failed", "task", due.TaskID, "error", err)
		return outcome, err
	}
	// The lease is a fence for this step, not a lock held across sweeps. Releasing
	// it on every exit keeps the task scannable: a held lease is skipped by
	// ListDueTasks until it expires, which would stall a task for a whole TTL
	// between two administrative steps that need no exclusivity.
	defer func() {
		if releaseErr := scheduler.worker.ReleaseTaskLease(ctx, leased.TenantID, leased.TaskID, leased.LeaseOwner, leased.LeaseEpoch); releaseErr != nil {
			scheduler.report("import task lease release failed", "task", leased.TaskID, "error", releaseErr)
		}
	}()

	if leased.CancelRequestedAt != nil || leased.State == TaskCanceling {
		return scheduler.cancel(ctx, leased, outcome)
	}

	// A frozen candidate only needs verification and publication. A task without
	// one needs a physical attempt first.
	if leased.Phase == PhaseVerifying || leased.CandidateAttemptID != "" {
		published, pubErr := scheduler.publish(ctx, leased, now)
		if pubErr != nil {
			return scheduler.recordError(ctx, leased, pubErr, outcome)
		}
		if published {
			outcome.published++
		} else {
			outcome.skipped++
		}
		return outcome, nil
	}
	if leased.State == TaskQueued {
		started, startErr := scheduler.repository.StartTask(ctx, leased.TenantID, leased.TaskID, leased.LeaseOwner, leased.LeaseEpoch)
		if startErr != nil {
			return scheduler.recordError(ctx, leased, startErr, outcome)
		}
		leased = started
		outcome.admitted++
	}
	return scheduler.dispatch(ctx, leased, outcome)
}

// dispatch drives the task's current attempt. An attempt is created only when
// the task has none: SetCurrentAttempt is a one-way binding, so allocating on
// every sweep fails the task and leaks a capacity slot each round.
//
// Any failure after allocation closes that attempt, which releases the shared
// slot. Without that, one failure permanently consumes budget and later tasks
// can never start.
func (scheduler *ImportScheduler) dispatch(ctx context.Context, task DatasetImportTask, outcome taskOutcome) (taskOutcome, error) {
	attempt, planned, prepareErr := scheduler.prepareAttempt(ctx, task)
	if prepareErr != nil {
		if errors.Is(prepareErr, ErrImportQuotaExhausted) {
			// The shared budget is full right now; the next sweep retries.
			outcome.skipped++
			return outcome, nil
		}
		return scheduler.recordError(ctx, task, prepareErr, outcome)
	}

	fail := func(cause error) (taskOutcome, error) {
		// Nothing of this attempt is usable after a failed dispatch: close it so
		// its slot returns and the task can be retried cleanly.
		if _, closeErr := scheduler.repository.CloseAttempt(ctx, task.TenantID, attempt.AttemptID, "FAILED"); closeErr != nil {
			scheduler.report("import attempt close failed", "task", task.TaskID, "attempt", attempt.AttemptID, "error", closeErr)
		}
		return scheduler.recordError(ctx, task, cause, outcome)
	}

	credentials, credentialErr := scheduler.credentials(ctx, task, attempt, planned)
	if credentialErr != nil {
		return fail(credentialErr)
	}
	// The native admission policy requires the owning workload to carry a real
	// modeldev.ani.io/environment-id; it can only come from the registry row.
	environmentID, envErr := scheduler.environments.EnvironmentID(ctx, task.TenantID, task.ClusterID)
	if envErr != nil {
		return fail(envErr)
	}
	if environmentID != "" {
		if _, setErr := scheduler.repository.SetTaskEnvironment(ctx, task.TenantID, task.TaskID, environmentID); setErr != nil {
			return fail(setErr)
		}
		task.EnvironmentID = environmentID
	}
	workload, ensureErr := scheduler.workloads.EnsureImporter(ctx, attempt, planned, task, credentials)
	if ensureErr != nil {
		return fail(ensureErr)
	}
	if _, recordErr := scheduler.repository.RecordAttemptJob(ctx, task.TenantID, attempt.AttemptID, workload.JobUID, workload.ResourceVersion); recordErr != nil {
		return fail(recordErr)
	}
	attempt.JobUID = workload.JobUID
	if resumeErr := scheduler.workloads.ResumeImporter(ctx, attempt, workload.JobUID); resumeErr != nil {
		return fail(resumeErr)
	}
	outcome.dispatched++

	// The workload now runs. A later sweep observes it; the same sweep may also
	// find it already finished for a very short import.
	if observeErr := scheduler.observe(ctx, task, attempt); observeErr != nil {
		scheduler.report("import observation deferred", "task", task.TaskID, "attempt", attempt.AttemptID, "error", observeErr)
	}
	return outcome, nil
}

// observe reads the Job state and, once it finished, freezes the candidate or
// records the failure. Reading the report is the control plane's own trusted
// step: the report is decoded and re-checked against the frozen attempt.
func (scheduler *ImportScheduler) observe(ctx context.Context, task DatasetImportTask, attempt DatasetImportAttempt) error {
	status, observeErr := scheduler.workloads.ObserveImporter(ctx, attempt)
	if observeErr != nil {
		return observeErr
	}
	if !status.Present {
		// The Job vanished without a result: nothing verifiable remains.
		if _, closeErr := scheduler.repository.CloseAttempt(ctx, task.TenantID, attempt.AttemptID, "FAILED"); closeErr != nil {
			scheduler.report("import attempt close failed", "task", task.TaskID, "attempt", attempt.AttemptID, "error", closeErr)
		}
		return scheduler.failTask(ctx, task, ErrImportDispatchUnavailable, outcomeNoop())
	}
	if !status.Finished {
		return nil
	}
	report, resultErr := scheduler.objects.ReadResult(ctx, task, attempt)
	if resultErr != nil {
		// A finished Job whose report cannot be read is not publishable. The
		// attempt is closed and the task failed with a content-level reason.
		if _, closeErr := scheduler.repository.CloseAttempt(ctx, task.TenantID, attempt.AttemptID, "FAILED"); closeErr != nil {
			scheduler.report("import attempt close failed", "task", task.TaskID, "attempt", attempt.AttemptID, "error", closeErr)
		}
		return scheduler.failTask(ctx, task, resultErr, outcomeNoop())
	}
	result := report.Result
	if result.Status != ImportResultSucceeded || result.Object == nil {
		code := errors.New(string(result.FailureCode))
		if _, closeErr := scheduler.repository.CloseAttempt(ctx, task.TenantID, attempt.AttemptID, "FAILED"); closeErr != nil {
			scheduler.report("import attempt close failed", "task", task.TaskID, "attempt", attempt.AttemptID, "error", closeErr)
		}
		return scheduler.failTask(ctx, task, code, outcomeNoop())
	}
	object := result.Object
	// Closing the attempt first records that no writer remains and releases its
	// capacity slot; the task may only name an attempt whose row already holds
	// the same frozen object identity.
	if _, freezeErr := scheduler.worker.FreezeCandidate(ctx, task.TenantID, attempt.AttemptID, object.VersionID, object.SHA256); freezeErr != nil {
		if errors.Is(freezeErr, ErrImportStateConflict) {
			// A concurrent cancel already closed this attempt; the next sweep
			// converges the task instead of freezing a candidate nobody owns.
			return nil
		}
		return freezeErr
	}
	if _, freezeErr := scheduler.repository.FreezeTaskCandidate(ctx, CandidateFreeze{
		TenantID: task.TenantID, TaskID: task.TaskID, AttemptID: attempt.AttemptID,
		ObjectKey: object.Key, VersionID: object.VersionID, SizeBytes: object.SizeBytes, SHA256: object.SHA256,
		ReportVersionID: report.VersionID, ReportSHA256: report.SHA256,
	}); freezeErr != nil {
		if errors.Is(freezeErr, ErrImportStateConflict) {
			return nil
		}
		return freezeErr
	}
	return nil
}

// cancel converges a cancel-requested task without publishing anything. It
// never invents writer absence: the attempt is closed only after its workload
// is observed as finished or gone, and the importer objects are removed with
// the same idempotent boundary the normal path uses.
func (scheduler *ImportScheduler) cancel(ctx context.Context, task DatasetImportTask, outcome taskOutcome) (taskOutcome, error) {
	attemptID := task.CandidateAttemptID
	if attemptID == "" {
		attemptID = task.CurrentAttemptID
	}
	if attemptID != "" {
		attempt, err := scheduler.worker.GetAttempt(ctx, task.TenantID, attemptID)
		if err != nil {
			return outcome, err
		}
		if attempt.DispatchClosedAt == nil {
			status, observeErr := scheduler.workloads.ObserveImporter(ctx, attempt)
			if observeErr != nil {
				scheduler.report("import cancel observation deferred", "task", task.TaskID, "attempt", attempt.AttemptID, "error", observeErr)
				outcome.skipped++
				return outcome, nil
			}
			if status.Present && !status.Finished {
				// Ask the workload to stop, then wait for the observed exit before
				// claiming the writers are gone.
				if deleteErr := scheduler.workloads.DeleteImporter(ctx, attempt); deleteErr != nil {
					scheduler.report("import cancel stop failed", "task", task.TaskID, "attempt", attempt.AttemptID, "error", deleteErr)
				}
				outcome.skipped++
				return outcome, nil
			}
			if _, closeErr := scheduler.repository.CloseAttempt(ctx, task.TenantID, attempt.AttemptID, "CANCELLED"); closeErr != nil && !errors.Is(closeErr, ErrImportStateConflict) {
				return outcome, closeErr
			}
		}
		if deleteErr := scheduler.workloads.DeleteImporter(ctx, attempt); deleteErr != nil {
			scheduler.report("import cancel cleanup failed", "task", task.TaskID, "attempt", attempt.AttemptID, "error", deleteErr)
		}
	}
	if _, err := scheduler.repository.FinishCancelledTask(ctx, TaskCancellation{
		TenantID: task.TenantID, TaskID: task.TaskID,
		OperationID: scheduler.newOperationID(), Actor: scheduler.owner,
	}); err != nil {
		if errors.Is(err, ErrImportStateConflict) {
			// A concurrent sweep already finished this cancel.
			outcome.skipped++
			return outcome, nil
		}
		return outcome, err
	}
	outcome.skipped++
	return outcome, nil
}

// failTask ends the task with the given cause and reports it.
func (scheduler *ImportScheduler) failTask(ctx context.Context, task DatasetImportTask, cause error, outcome taskOutcome) error {
	_, err := scheduler.recordError(ctx, task, cause, outcome)
	return err
}

func outcomeNoop() taskOutcome { return taskOutcome{} }

// newOperationID mints the history entry identity for a terminal transition. A
// failure to mint one must not silently drop the event, so the caller sees an
// empty id and the repository refuses the write.
func (scheduler *ImportScheduler) newOperationID() string {
	id, err := scheduler.newID()
	if err != nil {
		scheduler.report("import operation id unavailable", "error", err)
		return ""
	}
	return id
}

// prepareAttempt returns the attempt the task is already working on, or
// allocates exactly one, together with the material plan it froze.
func (scheduler *ImportScheduler) prepareAttempt(ctx context.Context, task DatasetImportTask) (DatasetImportAttempt, AttemptPlan, error) {
	if task.CurrentAttemptID != "" {
		attempt, err := scheduler.worker.GetAttempt(ctx, task.TenantID, task.CurrentAttemptID)
		if err != nil {
			return DatasetImportAttempt{}, AttemptPlan{}, err
		}
		// A closed attempt stays the task's current one until it is detached.
		// Reusing it would resume a workload that dispatch already abandoned, and
		// SetCurrentAttempt could never bind a replacement, so the task would loop
		// forever. Detach and fall through to allocation.
		if attempt.DispatchClosedAt != nil {
			detached, detachErr := scheduler.repository.ClearCurrentAttempt(ctx, task.TenantID, task.TaskID, attempt.AttemptID)
			if detachErr != nil {
				return DatasetImportAttempt{}, AttemptPlan{}, detachErr
			}
			task = detached
		} else if !scheduler.now().Before(attempt.DeadlineAt) {
			// The attempt's frozen deadline has passed. Starting a workload now
			// would be rejected by the API server and could never succeed, so it
			// is closed and the task reported as timed out.
			if _, closeErr := scheduler.repository.CloseAttempt(ctx, task.TenantID, attempt.AttemptID, "DEADLINE"); closeErr != nil {
				scheduler.report("import attempt close failed", "task", task.TaskID, "attempt", attempt.AttemptID, "error", closeErr)
			}
			return DatasetImportAttempt{}, AttemptPlan{}, ErrImportTimeout
		} else {
			plan, planErr := scheduler.planner.PlanAttempt(ctx, task, attempt.AttemptID, attempt.AttemptNo)
			if planErr != nil {
				return DatasetImportAttempt{}, AttemptPlan{}, planErr
			}
			if plan.DeadlineAt.IsZero() {
				plan.DeadlineAt = attempt.DeadlineAt
			}
			return attempt, plan, nil
		}
	}
	attemptID, idErr := scheduler.newID()
	if idErr != nil {
		return DatasetImportAttempt{}, AttemptPlan{}, idErr
	}
	var planned AttemptPlan
	attempt, allocErr := scheduler.repository.AllocateAttempt(ctx, AttemptAllocationRequest{
		Task: task, AttemptID: attemptID,
		Plan: func(ctx context.Context, attemptNo int64) (AttemptPlan, error) {
			plan, err := scheduler.planner.PlanAttempt(ctx, task, attemptID, attemptNo)
			if err != nil {
				return AttemptPlan{}, err
			}
			if plan.DeadlineAt.IsZero() {
				plan.DeadlineAt = scheduler.now().UTC().Add(scheduler.importTimeout)
			}
			planned = plan
			return plan, nil
		},
	})
	if allocErr != nil {
		return DatasetImportAttempt{}, AttemptPlan{}, allocErr
	}
	if _, bindErr := scheduler.repository.SetCurrentAttempt(ctx, task.TenantID, task.TaskID, attempt.AttemptID); bindErr != nil {
		return DatasetImportAttempt{}, AttemptPlan{}, bindErr
	}
	return attempt, planned, nil
}

// credentials issues the attempt-scoped session. Missing STS is a deployment
// failure, not a reason to run the importer with the parent credentials.
func (scheduler *ImportScheduler) credentials(ctx context.Context, task DatasetImportTask, attempt DatasetImportAttempt, plan AttemptPlan) (TemporaryStorageCredentials, error) {
	now := scheduler.now().UTC()
	issuanceID, idErr := scheduler.newID()
	if idErr != nil {
		return TemporaryStorageCredentials{}, ErrImportDispatchUnavailable
	}
	session, err := scheduler.issuer.IssueImportSession(ctx, ImportSessionRequest{
		TenantID: task.TenantID, DatasetID: task.DatasetID, TaskID: task.TaskID, AttemptID: attempt.AttemptID,
		IssuanceID:     issuanceID,
		TaskLeaseEpoch: task.LeaseEpoch, ReconcileEpoch: attempt.ReconcileEpoch,
		Policy: plan.RuntimePlan, PolicySHA256: plan.RuntimePlanSHA256,
		IntendedAt: now, RequestDeadline: now.Add(scheduler.leaseTTL), AuthDeadline: plan.DeadlineAt,
	}, ImportSessionGrant{
		StorageConnectionID: task.StorageConnectionID, Bucket: task.Bucket,
		DataKey: attempt.DataObjectKey, ResultKey: attempt.ResultObjectKey,
	})
	if err != nil {
		return TemporaryStorageCredentials{}, fmt.Errorf("%w: %v", ErrImportDispatchUnavailable, err)
	}
	if !session.Issued {
		return TemporaryStorageCredentials{}, fmt.Errorf("%w: STS reported NOT_ISSUED", ErrImportDispatchUnavailable)
	}
	return session.Credentials, nil
}

// recordError classifies a step failure: a retryable transient failure keeps the
// task alive with a bounded delay; everything else ends it.
func (scheduler *ImportScheduler) recordError(ctx context.Context, task DatasetImportTask, cause error, outcome taskOutcome) (taskOutcome, error) {
	scheduler.report("import task step failed", "task", task.TaskID, "state", string(task.State), "error", cause)
	if scheduler.retryable(cause) && task.RecoveryGeneration <= maxRecoveryGenerations {
		if derr := scheduler.repository.DeferTask(ctx, TaskDeferral{
			TenantID: task.TenantID, TaskID: task.TaskID,
			Delay:       BackoffDelay(int(task.RecoveryGeneration), scheduler.interval, 5*time.Minute),
			FailureCode: failureCodeFor(cause),
		}); derr != nil {
			return outcome, derr
		}
		outcome.deferred++
		return outcome, cause
	}
	if _, ferr := scheduler.repository.FailTask(ctx, TaskFailure{
		TenantID: task.TenantID, TaskID: task.TaskID,
		Code: failureCodeFor(cause), Retryable: scheduler.retryable(cause),
		OperationID: scheduler.newOperationID(), Actor: scheduler.owner,
	}); ferr != nil {
		return outcome, ferr
	}
	outcome.failed++
	return outcome, cause
}

// maxRecoveryGenerations bounds automatic advances of one recovery cycle. Retry
// policy is operational, so this is a small fixed constant, never a content limit.
const maxRecoveryGenerations = 3

// retryable lists transient causes worth another attempt. A content verdict is
// never in it: re-downloading the same bytes cannot fix an invalid file.
func (scheduler *ImportScheduler) retryable(cause error) bool {
	switch {
	case errors.Is(cause, ErrImportDispatchUnavailable),
		errors.Is(cause, ErrImportQuotaExhausted),
		errors.Is(cause, ErrSourceUnavailable),
		errors.Is(cause, ErrTaskLeaseLost),
		errors.Is(cause, ErrAttemptLeaseLost):
		return true
	}
	return false
}

// failureCodeFor maps a cause onto the public failure vocabulary. A cause that
// is already one of the importer's own codes keeps it: the report is the only
// place that knows what the job observed, and rewriting it to a generic
// persistence error would erase the reason the page branches on.
func failureCodeFor(cause error) string {
	// A record that exceeds the parser guard is a content verdict with its own
	// public code, not a generic invalid dataset. It is checked first because the
	// verifier may report it alongside the generic verification failure.
	var violation *cpup01.LimitViolation
	if errors.As(cause, &violation) && violation.Reason == string(FailDatasetRecordTooLarge) {
		return string(FailDatasetRecordTooLarge)
	}
	switch {
	case errors.Is(cause, ErrImportDispatchUnavailable):
		return string(FailDependencyUnavailable)
	case errors.Is(cause, ErrImportQuotaExhausted):
		return string(FailResourceUnavailable)
	case errors.Is(cause, ErrImportTimeout):
		return string(FailTimeout)
	case errors.Is(cause, ErrSourceUnavailable):
		return string(FailDependencyUnavailable)
	case errors.Is(cause, ErrSourceChanged):
		return string(FailSourceChanged)
	case errors.Is(cause, ErrSourceNotFound):
		return string(FailSourceNotFound)
	case errors.Is(cause, ErrSourceAuthRequired):
		return string(FailSourceAuthRequired)
	case errors.Is(cause, ErrImportResultInvalid):
		return string(FailImportResultInvalid)
	case errors.Is(cause, ErrInputSourceUnavailable), errors.Is(cause, ErrObjectSourceUnavailable):
		return string(FailDependencyUnavailable)
	case errors.Is(cause, ErrInputVerification), errors.Is(cause, ErrObjectVerification):
		return string(FailDatasetInvalid)
	}
	// A report's own FAILED verdict arrives as a bare error carrying its code.
	if code := ImportFailureCode(cause.Error()); validImportFailureCode(code) {
		return string(code)
	}
	return string(FailPersistence)
}

// publish re-verifies the frozen candidate and commits the READY version. The
// report is read again from the attempt's own key, so a missing or tampered
// report never publishes.
func (scheduler *ImportScheduler) publish(ctx context.Context, task DatasetImportTask, now time.Time) (bool, error) {
	attempt, attemptErr := scheduler.currentAttempt(ctx, task)
	if attemptErr != nil {
		return false, attemptErr
	}
	report, resultErr := scheduler.objects.ReadResult(ctx, task, attempt)
	if resultErr != nil {
		return false, resultErr
	}
	result := report.Result
	if result.Status != ImportResultSucceeded {
		return false, errors.New(string(result.FailureCode))
	}
	if result.Object.Key != task.CandidateObjectKey || result.Object.VersionID != task.CandidateVersionID ||
		result.Object.SHA256 != task.CandidateSHA256 || result.Object.SizeBytes != task.CandidateSizeBytes {
		// A report that does not describe the frozen candidate is a conflict, never
		// a silent re-point at whatever the object currently holds.
		return false, ErrImportStateConflict
	}
	verified, preview, verifyErr := scheduler.objects.VerifyCandidate(ctx, task, result)
	if verifyErr != nil {
		return false, verifyErr
	}
	// The publication transaction locks the reserved input row and fails with
	// INPUT_VERSION_NOT_FOUND when it is absent. The same InputImport value is
	// used here and below: the frozen fields must match or sameImport reports a
	// conflict instead of republishing the original row.
	importRequest := InputImport{
		Format: "JSON", TenantID: task.TenantID, RequestID: task.InputRequestID,
		InputVersionID: task.ReservedInputVersionID, Actor: scheduler.owner,
		RequestedAt: task.AcceptedAt, Scope: targetStorageScope(task),
		Object: candidateObjectRef(task),
	}
	if _, reserveErr := scheduler.publication.ReserveImport(ctx, importRequest); reserveErr != nil {
		return false, reserveErr
	}
	version, publishErr := scheduler.publication.PublishVersion(ctx, PublicationRequest{
		TenantID: task.TenantID,
		Import:   importRequest,
		Verified: verified, DatasetID: task.DatasetID, TaskID: task.TaskID,
		InputVersionID: task.ReservedInputVersionID, VersionNumber: task.VersionNumber,
		Preview: preview, PublishedAt: now, Actor: scheduler.owner,
		RecoveryGeneration: task.RecoveryGeneration,
		OperationID:        scheduler.newOperationID(),
		Target:             taskTarget(task),
	})
	if publishErr != nil {
		return false, publishErr
	}
	return !version.Replayed || version.Task.State == TaskSucceeded, nil
}

// currentAttempt finds the attempt the task froze. Recovery must read it back
// rather than trust this process's memory.
func (scheduler *ImportScheduler) currentAttempt(ctx context.Context, task DatasetImportTask) (DatasetImportAttempt, error) {
	if task.CandidateAttemptID == "" {
		return DatasetImportAttempt{}, ErrImportStateConflict
	}
	return scheduler.worker.GetAttempt(ctx, task.TenantID, task.CandidateAttemptID)
}

func targetStorageScope(task DatasetImportTask) cpup01.StorageScope {
	return cpup01.StorageScope{
		StorageConnectionID: task.StorageConnectionID, Bucket: task.Bucket, ApprovedPrefix: task.ApprovedPrefix,
		CredentialReference: task.CredentialReference,
	}
}

func candidateObjectRef(task DatasetImportTask) cpup01.FixedObjectRef {
	versionID := task.CandidateVersionID
	return cpup01.FixedObjectRef{
		StorageConnectionID: task.StorageConnectionID, Bucket: task.Bucket, Key: task.CandidateObjectKey,
		VersionID: &versionID, SizeBytes: task.CandidateSizeBytes, SHA256: task.CandidateSHA256,
	}
}

func taskTarget(task DatasetImportTask) DatasetImportTarget {
	return DatasetImportTarget{
		Schema: DatasetImportTargetSchema, ClusterID: task.ClusterID, NamespaceName: task.NamespaceName,
		NamespaceUID: task.NamespaceUID,
		InputScope: ImportTargetScope{
			StorageConnectionID: task.StorageConnectionID, Bucket: task.Bucket,
			ApprovedPrefix: task.ApprovedPrefix, CredentialReference: task.CredentialReference,
		},
		InputScopeSHA256: task.InputScopeSHA256,
	}
}

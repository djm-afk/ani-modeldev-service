package biz

import (
	"context"
	"errors"
	"time"
)

// AttemptReconciler drives the facts the task scheduler cannot: binding an
// attempt that was allocated but never attached, proving a closed attempt's
// writer stopped, and deleting the temporary objects of a finished attempt.
//
// Design §8.3 requires this scan to be independent of the task state: a task
// that already reached SUCCEEDED, FAILED or CANCELLED still owns attempts whose
// Job, PVC and Secret must be removed, and a slot is only released once the
// writer is proven stopped. Without this loop an attempt allocated just before a
// restart held its capacity slot forever.
type AttemptReconciler struct {
	worker    DatasetWorkerRepository
	tasks     DatasetImportTaskRepository
	workloads DatasetImportWorkloadControl

	owner     string
	interval  time.Duration
	leaseTTL  time.Duration
	batchSize int32
	now       func() time.Time
	logf      func(message string, args ...any)
}

// AttemptReconcilerDeps keeps the constructor readable. Every port is required.
type AttemptReconcilerDeps struct {
	Worker    DatasetWorkerRepository
	Tasks     DatasetImportTaskRepository
	Workloads DatasetImportWorkloadControl

	Owner     string
	Interval  time.Duration
	LeaseTTL  time.Duration
	BatchSize int32
	Now       func() time.Time
	Logf      func(message string, args ...any)
}

// NewAttemptReconciler validates the assembly. A missing port is a configuration
// error, never a silently degraded loop.
func NewAttemptReconciler(deps AttemptReconcilerDeps) (*AttemptReconciler, error) {
	if deps.Worker == nil || deps.Tasks == nil || deps.Workloads == nil ||
		deps.Owner == "" || deps.Interval <= 0 {
		return nil, ErrImportWorkerConfiguration
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	ttl := deps.LeaseTTL
	if ttl <= 0 {
		ttl = 2 * deps.Interval
	}
	batchSize := deps.BatchSize
	if batchSize <= 0 {
		batchSize = defaultSweepBatchSize
	}
	return &AttemptReconciler{
		worker: deps.Worker, tasks: deps.Tasks, workloads: deps.Workloads,
		owner: deps.Owner, interval: deps.Interval, leaseTTL: ttl,
		batchSize: batchSize, now: now, logf: deps.Logf,
	}, nil
}

// AttemptBatchResult is one reconcile pass, reported for logs and tests.
type AttemptBatchResult struct {
	Scanned, Bound, Stopped, Cleaned, Skipped int
	LastError                                 error
}

// Run sweeps until the context is cancelled. A failing sweep backs off but never
// stops the loop: one bad attempt must not freeze every other tenant.
func (reconciler *AttemptReconciler) Run(ctx context.Context) error {
	if ctx == nil || reconciler == nil {
		return ErrImportWorkerConfiguration
	}
	backoff := time.Second
	for {
		_, err := reconciler.Sweep(ctx)
		if ctx.Err() != nil {
			return nil
		}
		delay := reconciler.interval
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

// Sweep runs one pass over the attempts whose reconcile time has come.
func (reconciler *AttemptReconciler) Sweep(ctx context.Context) (AttemptBatchResult, error) {
	result := AttemptBatchResult{}
	if ctx == nil || reconciler == nil {
		return result, ErrImportWorkerConfiguration
	}
	now := reconciler.now().UTC()
	attempts, err := reconciler.worker.ListDueAttempts(ctx, now, reconciler.batchSize)
	if err != nil {
		reconciler.report("list due attempts failed", "error", err)
		return result, err
	}
	result.Scanned = len(attempts)
	for _, due := range attempts {
		if ctx.Err() != nil {
			return result, nil
		}
		outcome, attemptErr := reconciler.reconcile(ctx, due)
		result.Bound += outcome.bound
		result.Stopped += outcome.stopped
		result.Cleaned += outcome.cleaned
		result.Skipped += outcome.skipped
		if attemptErr != nil {
			result.LastError = attemptErr
		}
	}
	return result, nil
}

type attemptOutcome struct {
	bound, stopped, cleaned, skipped int
}

// reconcile handles one attempt under its own reconcile lease. The lease is a
// fence for this step, not a lock held across sweeps.
func (reconciler *AttemptReconciler) reconcile(ctx context.Context, due DatasetImportAttempt) (attemptOutcome, error) {
	outcome := attemptOutcome{}
	leased, err := reconciler.worker.AcquireAttemptLease(ctx, due.TenantID, due.AttemptID, reconciler.owner, reconciler.leaseTTL)
	if err != nil {
		if errors.Is(err, ErrAttemptLeaseLost) || errors.Is(err, ErrImportStateConflict) {
			outcome.skipped++
			return outcome, nil
		}
		reconciler.report("attempt lease failed", "attempt", due.AttemptID, "error", err)
		return outcome, err
	}
	defer func() {
		if releaseErr := reconciler.worker.ReleaseAttemptLease(ctx, leased.TenantID, leased.AttemptID, reconciler.owner, leased.ReconcileEpoch); releaseErr != nil {
			reconciler.report("attempt lease release failed", "attempt", leased.AttemptID, "error", releaseErr)
		}
	}()

	task, err := reconciler.tasks.GetImportTask(ctx, leased.TenantID, leased.TaskID)
	if err != nil {
		if errors.Is(err, ErrImportTaskNotFound) {
			// The task row is gone; the attempt cannot be reasoned about. Leave it
			// for an operator rather than deleting objects nobody can attribute.
			outcome.skipped++
			return outcome, nil
		}
		return outcome, err
	}

	// 1. An attempt that is not the task's current one and never closed is an
	// orphan: the task moved on (or never bound it). Close it so its slot can be
	// released, but only after proving no writer exists.
	if leased.DispatchClosedAt == nil {
		if task.CurrentAttemptID == leased.AttemptID {
			// The scheduler owns this attempt's dispatch and observation.
			outcome.skipped++
			return outcome, nil
		}
		if task.CurrentAttemptID == "" && task.State == TaskRunning && task.CancelRequestedAt == nil {
			// Allocated but never bound: bind it so the scheduler can drive it.
			if _, bindErr := reconciler.worker.SetCurrentAttempt(ctx, task.TenantID, task.TaskID, leased.AttemptID); bindErr == nil {
				outcome.bound++
				return outcome, nil
			}
			// A bind failure means the task moved on; fall through to closing.
		}
		if _, closeErr := reconciler.worker.CloseAttempt(ctx, leased.TenantID, leased.AttemptID, "FAILED"); closeErr != nil && !errors.Is(closeErr, ErrImportStateConflict) {
			return outcome, closeErr
		}
		leased.DispatchClosedAt = closedNow(reconciler.now())
	}

	// 2. A closed attempt whose writer is not yet proven stopped. Only an
	// observation of the Job can prove it: a closed dispatch gate stops new work
	// but says nothing about the Pod already running.
	if leased.WritersStoppedAt == nil {
		status, observeErr := reconciler.workloads.ObserveImporter(ctx, leased)
		if observeErr != nil {
			reconciler.report("attempt observation deferred", "attempt", leased.AttemptID, "error", observeErr)
			outcome.skipped++
			return outcome, nil
		}
		if status.Present && !status.Finished {
			// Still writing. Ask it to stop and wait for the observed exit.
			if deleteErr := reconciler.workloads.DeleteImporter(ctx, leased); deleteErr != nil {
				reconciler.report("attempt stop failed", "attempt", leased.AttemptID, "error", deleteErr)
			}
			outcome.skipped++
			return outcome, nil
		}
		if _, stopErr := reconciler.worker.ConfirmWritersStopped(ctx, leased.TenantID, leased.AttemptID); stopErr != nil {
			if errors.Is(stopErr, ErrImportStateConflict) {
				outcome.skipped++
				return outcome, nil
			}
			return outcome, stopErr
		}
		outcome.stopped++
		leased.WritersStoppedAt = closedNow(reconciler.now())
	}

	// 3. Temporary objects of an attempt whose writer is gone. The gate is the
	// design's own rule: a READY version, a still-recoverable candidate or an
	// unfinished multipart upload always wins over deletion.
	if leased.CleanupState == "DONE" || leased.CleanupState == "RETAINED" {
		outcome.skipped++
		return outcome, nil
	}
	// CurrentAttempt is false by construction here: this branch is only reached
	// after writers_stopped_at, which requires a closed dispatch gate. A closed
	// attempt can no longer produce a candidate, so it is not a live current one
	// even while the task row still points at it.
	gate := CleanupGate{
		TaskState:       task.State,
		CurrentAttempt:  false,
		CancelRequested: task.CancelRequestedAt != nil,
	}
	references := LiveReferences{
		ReadyVersion: task.State == TaskSucceeded,
		Candidate:    task.CandidateAttemptID == leased.AttemptID,
	}
	if gateErr := gate.AllowsGCObjects(references); gateErr != nil {
		outcome.skipped++
		return outcome, nil
	}
	if _, claimErr := reconciler.worker.ClaimAttemptCleanup(ctx, leased.TenantID, leased.AttemptID); claimErr != nil {
		if errors.Is(claimErr, ErrImportStateConflict) {
			outcome.skipped++
			return outcome, nil
		}
		return outcome, claimErr
	}
	if deleteErr := reconciler.workloads.DeleteImporter(ctx, leased); deleteErr != nil {
		reconciler.report("attempt cleanup failed", "attempt", leased.AttemptID, "error", deleteErr)
		return outcome, deleteErr
	}
	if _, finishErr := reconciler.worker.FinishAttemptCleanup(ctx, leased.TenantID, leased.AttemptID); finishErr != nil {
		if errors.Is(finishErr, ErrImportStateConflict) {
			outcome.skipped++
			return outcome, nil
		}
		return outcome, finishErr
	}
	outcome.cleaned++
	return outcome, nil
}

func (reconciler *AttemptReconciler) report(message string, args ...any) {
	if reconciler.logf != nil {
		reconciler.logf(message, args...)
	}
}

// closedNow returns the instant to use for a locally advanced fact. It is only
// used to keep the in-memory copy consistent within this step; the database
// records its own clock_timestamp().
func closedNow(now time.Time) *time.Time {
	at := now.UTC()
	return &at
}

package biz

import (
	"errors"
	"time"
)

var (
	ErrTaskLeaseLost        = errors.New("IMPORT_TASK_LEASE_LOST")
	ErrAttemptLeaseLost     = errors.New("IMPORT_ATTEMPT_LEASE_LOST")
	ErrImportQuotaExhausted = errors.New("IMPORT_QUOTA_EXHAUSTED")
	ErrImportDispatchClosed = errors.New("IMPORT_DISPATCH_CLOSED")
)

// TaskLease is the business-execution fence. It expires and is re-acquired; the
// epoch ensures a stale owner cannot act on a newer lease.
type TaskLease struct {
	Owner     string
	Epoch     int64
	ExpiresAt time.Time
}

// AttemptLease is the independent reconcile fence. It survives task
// termination because cleanup and GC still need to run.
type AttemptLease struct {
	Owner     string
	Epoch     int64
	ExpiresAt time.Time
}

// LeaseFence is the pair a worker must hold to start or stop external work.
type LeaseFence struct {
	TaskOwner    string
	TaskEpoch    int64
	AttemptOwner string
	AttemptEpoch int64
	Now          time.Time
}

// Validate checks that both leases are live and belong to this worker. Both are
// required: a valid task lease alone cannot authorize Kubernetes writes, and an
// attempt lease alone cannot authorize a new attempt.
func (fence LeaseFence) Validate(task TaskLease, attempt AttemptLease) error {
	if task.Owner == "" || task.Owner != fence.TaskOwner || task.Epoch != fence.TaskEpoch || !task.ExpiresAt.After(fence.Now) {
		return ErrTaskLeaseLost
	}
	if attempt.Owner == "" || attempt.Owner != fence.AttemptOwner || attempt.Epoch != fence.AttemptEpoch || !attempt.ExpiresAt.After(fence.Now) {
		return ErrAttemptLeaseLost
	}
	return nil
}

// AllowsNewAttempt is the narrower gate for creating a new attempt: the task
// lease must be live, the previous attempt must be closed, and no candidate may
// already be fixed.
type NewAttemptGate struct {
	TaskState       ImportTaskState
	DatasetState    DatasetState
	TargetInvalid   bool
	CancelRequested bool
	PreviousClosed  bool
	WritersStopped  bool
	CandidateFixed  bool
	CurrentAttempt  string
	Now             time.Time
	Deadline        time.Time
}

// Allows reports whether a new attempt may start. Every condition is a persisted
// fact the caller read inside the same transaction.
func (gate NewAttemptGate) Allows() error {
	if gate.TaskState != TaskQueued && gate.TaskState != TaskRunning {
		return ErrImportStateConflict
	}
	if gate.DatasetState != DatasetActive {
		return ErrDatasetArchived
	}
	if gate.TargetInvalid {
		return ErrImportEnvironmentMissing
	}
	if gate.CancelRequested {
		return ErrImportStateConflict
	}
	if gate.CandidateFixed {
		// A frozen candidate is already publishable; a second attempt would
		// create a second version.
		return ErrImportStateConflict
	}
	if gate.CurrentAttempt != "" && !(gate.PreviousClosed && gate.WritersStopped) {
		// No new physical attempt until the old one stopped writing.
		return ErrImportStateConflict
	}
	if !gate.Now.Before(gate.Deadline) {
		return ErrImportTimeout
	}
	return nil
}

// ErrImportTimeout is the absolute deadline failure.
var ErrImportTimeout = errors.New("IMPORT_TIMEOUT")

// AllowsDispatch is the gate for a new external write (Create/unsuspend/issue).
// A closed dispatch gate is one-way: nothing new may start after it.
type DispatchGate struct {
	DispatchClosedAt *time.Time
	TaskState        ImportTaskState
	DatasetState     DatasetState
	TargetInvalid    bool
	CancelRequested  bool
	IsCurrentAttempt bool
	Now              time.Time
	Deadline         time.Time
}

func (gate DispatchGate) Allows() error {
	if gate.DispatchClosedAt != nil {
		return ErrImportDispatchClosed
	}
	if gate.TaskState != TaskRunning || gate.DatasetState != DatasetActive {
		return ErrImportStateConflict
	}
	if gate.TargetInvalid || gate.CancelRequested {
		return ErrImportStateConflict
	}
	if !gate.IsCurrentAttempt {
		return ErrImportStateConflict
	}
	if !gate.Now.Before(gate.Deadline) {
		return ErrImportTimeout
	}
	return nil
}

// AllowsObservation and cleanup need only the attempt fence: a terminated task
// must still be able to observe and delete its own resources.
type CleanupGate struct {
	TaskState       ImportTaskState
	CurrentAttempt  bool
	CancelRequested bool
}

// AllowsGCObjects answers whether an attempt's temporary Kubernetes objects may
// be deleted. It governs the Job, PVC and Secret only: the READY version and the
// S3 objects are never deletion targets, so a succeeded task must still have its
// temporary resources collected.
func (gate CleanupGate) AllowsGCObjects(references LiveReferences) error {
	if references.UnfinishedMultipart {
		return ErrImportStateConflict
	}
	if gate.TaskState == TaskRunning && gate.CurrentAttempt && !gate.CancelRequested {
		// The live attempt may still produce a candidate.
		return ErrImportStateConflict
	}
	return nil
}

// LiveReferences are the durable references an object may still have.
type LiveReferences struct {
	ReadyVersion        bool
	Candidate           bool
	UnfinishedMultipart bool
}

// QuotaSlot is the global running-attempt budget. It is claimed under a single
// capacity lock so two workers cannot both take the last slot.
type QuotaSlot struct {
	Limit   int
	Running int
}

// Available reports how many attempts may still start.
func (quota QuotaSlot) Available() int {
	if quota.Limit <= 0 {
		return 0
	}
	remaining := quota.Limit - quota.Running
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Claim decides whether this worker may take one slot.
func (quota QuotaSlot) Claim() error {
	if quota.Available() <= 0 {
		return ErrImportQuotaExhausted
	}
	return nil
}

// DueTask is the scan projection the scheduler reads. The task scan covers live
// states only; cleanup is driven by the separate attempt scan.
type DueTask struct {
	TenantID  string
	TaskID    string
	State     ImportTaskState
	NextRunAt time.Time
}

// EligibleForTaskScan reports whether a task kind belongs to the task scan.
// Terminal tasks are excluded: their remaining work is attempt reconcile.
func (task DueTask) EligibleForTaskScan(now time.Time) bool {
	switch task.State {
	case TaskQueued, TaskRunning, TaskCanceling:
	default:
		return false
	}
	return !task.NextRunAt.After(now)
}

// EligibleForAttemptScan reports whether an attempt still needs reconciliation.
// It deliberately ignores the parent task state.
func EligibleForAttemptScan(nextAt *time.Time, now time.Time) bool {
	if nextAt == nil {
		return false
	}
	return !nextAt.After(now)
}

// BackoffDelay is the bounded retry delay for ordinary API failures. It never
// grows unbounded and never zeroes out, so a broken dependency cannot spin.
func BackoffDelay(attempt int, base, maximum time.Duration) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	if maximum <= 0 || maximum < base {
		maximum = base
	}
	if attempt <= 1 {
		return base
	}
	delay := base
	for index := 1; index < attempt; index++ {
		delay *= 2
		if delay >= maximum {
			return maximum
		}
	}
	return delay
}

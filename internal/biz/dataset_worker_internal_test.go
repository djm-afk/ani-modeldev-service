package biz

import (
	"errors"
	"testing"
	"time"
)

func liveTaskLease(now time.Time, owner string, epoch int64) TaskLease {
	return TaskLease{Owner: owner, Epoch: epoch, ExpiresAt: now.Add(time.Minute)}
}

func liveAttemptLease(now time.Time, owner string, epoch int64) AttemptLease {
	return AttemptLease{Owner: owner, Epoch: epoch, ExpiresAt: now.Add(time.Minute)}
}

func TestLeaseFenceRequiresBothLive(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	fence := LeaseFence{TaskOwner: "w1", TaskEpoch: 3, AttemptOwner: "w1", AttemptEpoch: 2, Now: now}
	if err := fence.Validate(liveTaskLease(now, "w1", 3), liveAttemptLease(now, "w1", 2)); err != nil {
		t.Fatalf("live fence rejected: %v", err)
	}
	// A stale epoch is a different owner now, even with the same name.
	if err := fence.Validate(liveTaskLease(now, "w1", 2), liveAttemptLease(now, "w1", 2)); !errors.Is(err, ErrTaskLeaseLost) {
		t.Fatalf("stale task epoch accepted: %v", err)
	}
	// Expired leases are gone regardless of owner match.
	expired := TaskLease{Owner: "w1", Epoch: 3, ExpiresAt: now.Add(-time.Second)}
	if err := fence.Validate(expired, liveAttemptLease(now, "w1", 2)); !errors.Is(err, ErrTaskLeaseLost) {
		t.Fatalf("expired task lease accepted: %v", err)
	}
	// An attempt lease alone is not enough.
	if err := fence.Validate(liveTaskLease(now, "w1", 3), liveAttemptLease(now, "w2", 2)); !errors.Is(err, ErrAttemptLeaseLost) {
		t.Fatalf("foreign attempt owner accepted: %v", err)
	}
}

func TestNewAttemptGateRejectsSecondVersionRisk(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	base := NewAttemptGate{
		TaskState: TaskRunning, DatasetState: DatasetActive, PreviousClosed: true,
		WritersStopped: true, Now: now, Deadline: now.Add(time.Hour),
	}
	if err := base.Allows(); err != nil {
		t.Fatalf("legal attempt rejected: %v", err)
	}
	for name, mutate := range map[string]func(*NewAttemptGate){
		"archived dataset":    func(g *NewAttemptGate) { g.DatasetState = DatasetArchived },
		"invalidated target":  func(g *NewAttemptGate) { g.TargetInvalid = true },
		"cancel requested":    func(g *NewAttemptGate) { g.CancelRequested = true },
		"candidate already":   func(g *NewAttemptGate) { g.CandidateFixed = true },
		"old attempt writing": func(g *NewAttemptGate) { g.CurrentAttempt = "a1"; g.WritersStopped = false },
		"deadline passed":     func(g *NewAttemptGate) { g.Now = g.Deadline },
		"failed task":         func(g *NewAttemptGate) { g.TaskState = TaskFailed },
	} {
		gate := base
		mutate(&gate)
		if err := gate.Allows(); err == nil {
			t.Fatalf("%s allowed a new attempt", name)
		}
	}
}

func TestDispatchGateIsOneWay(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	closed := now.Add(-time.Minute)
	gate := DispatchGate{TaskState: TaskRunning, DatasetState: DatasetActive, IsCurrentAttempt: true, Now: now, Deadline: now.Add(time.Hour)}
	if err := gate.Allows(); err != nil {
		t.Fatalf("live dispatch rejected: %v", err)
	}
	gate.DispatchClosedAt = &closed
	if err := gate.Allows(); !errors.Is(err, ErrImportDispatchClosed) {
		t.Fatalf("closed dispatch allowed new work: %v", err)
	}
}

// The gate governs the attempt's temporary Kubernetes objects only. A READY
// version and a frozen candidate are durable assets that live in object storage,
// so they must not block collecting the Job, PVC and Secret.
func TestCleanupGateProtectsLiveReferences(t *testing.T) {
	gate := CleanupGate{TaskState: TaskFailed}
	if err := gate.AllowsGCObjects(LiveReferences{}); err != nil {
		t.Fatalf("orphan objects were not collectable: %v", err)
	}
	// A READY version and a frozen candidate are not deletion targets, so they do
	// not stop the temporary objects from being collected.
	for name, references := range map[string]LiveReferences{
		"ready":     {ReadyVersion: true},
		"candidate": {Candidate: true},
	} {
		if err := gate.AllowsGCObjects(references); err != nil {
			t.Fatalf("%s reference blocked temporary cleanup: %v", name, err)
		}
	}
	// An unfinished multipart upload still owns bytes in the store.
	if err := gate.AllowsGCObjects(LiveReferences{UnfinishedMultipart: true}); !errors.Is(err, ErrImportStateConflict) {
		t.Fatalf("unfinished multipart was collected: %v", err)
	}
	// A live current attempt may still produce its candidate.
	live := CleanupGate{TaskState: TaskRunning, CurrentAttempt: true}
	if err := live.AllowsGCObjects(LiveReferences{}); !errors.Is(err, ErrImportStateConflict) {
		t.Fatal("live attempt result was collected")
	}
	// A succeeded task still owns a Job, PVC and Secret that must be removed.
	if err := (CleanupGate{TaskState: TaskSucceeded}).AllowsGCObjects(LiveReferences{ReadyVersion: true, Candidate: true}); err != nil {
		t.Fatalf("succeeded task temporary objects were kept: %v", err)
	}
}

func TestQuotaClaim(t *testing.T) {
	if err := (QuotaSlot{Limit: 4, Running: 3}).Claim(); err != nil {
		t.Fatalf("free slot rejected: %v", err)
	}
	if err := (QuotaSlot{Limit: 4, Running: 4}).Claim(); !errors.Is(err, ErrImportQuotaExhausted) {
		t.Fatalf("exhausted quota accepted: %v", err)
	}
	if err := (QuotaSlot{Limit: 0, Running: 0}).Claim(); !errors.Is(err, ErrImportQuotaExhausted) {
		t.Fatalf("zero quota accepted: %v", err)
	}
	if (QuotaSlot{Limit: 2, Running: 5}).Available() != 0 {
		t.Fatal("over-subscribed quota reported availability")
	}
}

func TestScanEligibility(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	due := now.Add(-time.Second)
	if !(DueTask{State: TaskRunning, NextRunAt: due}).EligibleForTaskScan(now) {
		t.Fatal("due running task skipped")
	}
	if (DueTask{State: TaskSucceeded, NextRunAt: due}).EligibleForTaskScan(now) {
		t.Fatal("terminal task entered the task scan")
	}
	if (DueTask{State: TaskRunning, NextRunAt: now.Add(time.Minute)}).EligibleForTaskScan(now) {
		t.Fatal("future task ran early")
	}
	// Attempt reconcile continues after the task is terminal.
	if !EligibleForAttemptScan(&due, now) {
		t.Fatal("due attempt skipped")
	}
	if EligibleForAttemptScan(nil, now) {
		t.Fatal("attempt without next_at was scanned")
	}
}

func TestBackoffIsBoundedAndNeverZero(t *testing.T) {
	base := time.Second
	maximum := 30 * time.Second
	if got := BackoffDelay(1, base, maximum); got != base {
		t.Fatalf("first attempt = %v", got)
	}
	previous := time.Duration(0)
	for attempt := 1; attempt <= 20; attempt++ {
		delay := BackoffDelay(attempt, base, maximum)
		if delay <= 0 || delay > maximum {
			t.Fatalf("attempt %d delay %v out of bounds", attempt, delay)
		}
		if delay < previous {
			t.Fatalf("attempt %d delay shrank", attempt)
		}
		previous = delay
	}
	if BackoffDelay(20, base, maximum) != maximum {
		t.Fatal("backoff did not reach the maximum")
	}
}

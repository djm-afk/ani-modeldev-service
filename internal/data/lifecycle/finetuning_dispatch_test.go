package lifecycle_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
)

// These verify the D09.3 arbitration boundary against real PostgreSQL and the
// shared execution row lock. No Model provider is involved: every outcome is
// recorded by the caller, so no stub can masquerade as a verified READY.

// registrationReadyFixture drives the synthetic execution through publication so
// the original registration intent can be persisted. It returns the pool opener
// so a test can take additional connections for the Stop race.
func registrationReadyFixture(t *testing.T) (func() *pgxpool.Pool, biz.Admission, biz.RunAuthorityCandidate) {
	t.Helper()
	open, admission, authority, binding := finetuningRuntimeFixture(t, time.Hour)
	ctx := context.Background()
	repository := lifecycle.New(open())
	for _, role := range []string{"export", "verify"} {
		binding.Role = role
		binding.TaskID = role + "-task"
		binding.PodName = role + "-pod"
		binding.PodUID = role + "-pod-uid"
		start, err := repository.ReserveFinetuningStep(ctx, authority, binding)
		if err != nil || !start.SendPermit {
			t.Fatal("step reservation", err)
		}
		var now time.Time
		if err := open().QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
			t.Fatal(err)
		}
		manifest := "reports/export/merged_manifest.json"
		if role == "verify" {
			manifest = "reports/verify/verify.json"
		}
		result := biz.FinetuningStepResult{StartedAt: start.Start.AdmittedAt, FinishedAt: now.UTC(), ExitCode: 0, ManifestRelativePath: manifest, ManifestBytes: 128, ManifestSHA256: strings.Repeat("a", 64)}
		if _, _, err := repository.RecordFinetuningStepResult(ctx, authority, binding, result); err != nil {
			t.Fatal("step result", err)
		}
	}
	if _, _, err := repository.RecordPublication(ctx, authority, publicationFixture(t, admission, authority)); err != nil {
		t.Fatal("publication", err)
	}
	if _, replayed, err := repository.RecordRegistrationIntent(ctx, authority); err != nil || replayed {
		t.Fatal("registration intent", err)
	}
	return open, admission, authority
}

func confirmedOutcome(t *testing.T, authority biz.RunAuthorityCandidate, permitted biz.FinetuningRegistrationDispatch) biz.FinetuningRegistrationDispatch {
	t.Helper()
	outcome := permitted
	outcome.State = biz.DispatchConfirmed
	outcome.ReceiptID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(authority.ExecutionID+permitted.RequestID)).String()
	return outcome
}

func TestRegistrationIntentIsFixedBeforeFirstWrite(t *testing.T) {
	open, admission, authority := registrationReadyFixture(t)
	ctx := context.Background()
	state, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || state.FinetuningRegistration == nil {
		t.Fatal(err)
	}
	intent := *state.FinetuningRegistration
	if intent.State != biz.RegistrationStatePending || intent.FailureCode != "" || len(intent.Dispatches) != 0 || intent.ContentSHA256 == "" {
		t.Fatalf("intent is not a bare original fact: %+v", intent)
	}
	// The whole registration budget is fixed once, at intent time, and is never
	// re-claimed by a later dispatch.
	want := intent.RecordedAt.Add(time.Duration(admission.Snapshot.Finetuning.Budgets.RPCTimeoutSeconds) * time.Second)
	if !intent.DeadlineAt.Equal(want) {
		t.Fatalf("registration budget was not frozen at intent time: %v != %v", intent.DeadlineAt, want)
	}
	// A second intent write is a replay, never a new budget or content.
	replayedState, replayed, err := lifecycle.New(open()).RecordRegistrationIntent(ctx, authority)
	if err != nil || !replayed || replayedState.FinetuningRegistration == nil || replayedState.FinetuningRegistration.DeadlineAt != intent.DeadlineAt {
		t.Fatalf("intent replay renewed content or budget: %v", err)
	}
}

func TestRegistrationDispatchReplayNeverResends(t *testing.T) {
	open, _, authority := registrationReadyFixture(t)
	ctx := context.Background()
	repository := lifecycle.New(open())
	first, err := repository.PermitRegistrationDispatch(ctx, authority, biz.OpCreateModel, "trace-first")
	if err != nil || !first.SendPermit || first.Dispatch.State != biz.DispatchInFlight || first.Dispatch.RequestID != "trace-first" || first.Dispatch.ContentSHA256 == "" {
		t.Fatalf("first permit: %v %+v", err, first)
	}
	// A different trace id must not become a second container write.
	second, err := repository.PermitRegistrationDispatch(ctx, authority, biz.OpCreateModel, "trace-second")
	if err != nil || second.SendPermit || second.Dispatch.RequestID != "trace-first" || !second.Dispatch.PermittedAt.Equal(first.Dispatch.PermittedAt) {
		t.Fatalf("replay re-sent the container write: %v %+v", err, second.Dispatch)
	}
	// Every permit shares the single registration deadline; no fresh window.
	intent, err := repository.GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || !second.Dispatch.DeadlineAt.Equal(intent.FinetuningRegistration.DeadlineAt) {
		t.Fatalf("permit re-claimed a budget: %v", err)
	}
}

func TestRegistrationStopBeforeDispatchProducesNoWrite(t *testing.T) {
	open, admission, authority := registrationReadyFixture(t)
	ctx := context.Background()
	if err := applyFinetuningStop(ctx, open(), admission); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.New(open()).PermitRegistrationDispatch(ctx, authority, biz.OpCreateModel, "trace-after-stop"); !errors.Is(err, biz.ErrPipelineDispatchBlocked) {
		t.Fatalf("Stop-first still granted an external write: %v", err)
	}
	state, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || len(state.FinetuningRegistration.Dispatches) != 0 {
		t.Fatalf("rejected dispatch was persisted: %v %+v", err, state.FinetuningRegistration)
	}
}

func TestRegistrationDispatchBeforeStopKeepsReceiptAndBlocksVersionWrite(t *testing.T) {
	open, admission, authority := registrationReadyFixture(t)
	ctx := context.Background()
	repository := lifecycle.New(open())
	permit, err := repository.PermitRegistrationDispatch(ctx, authority, biz.OpCreateModel, "trace-container")
	if err != nil || !permit.SendPermit {
		t.Fatal("container permit", err)
	}
	if _, err := repository.RecordRegistrationOutcome(ctx, authority, biz.OpCreateModel, confirmedOutcome(t, authority, permit.Dispatch)); err != nil {
		t.Fatal("container receipt", err)
	}
	if err := applyFinetuningStop(ctx, open(), admission); err != nil {
		t.Fatal(err)
	}
	// Stop after the container receipt must not permit the version write, and
	// must not erase the container receipt or the original target.
	if _, err := repository.PermitRegistrationDispatch(ctx, authority, biz.OpCreateModelVersion, "trace-version"); !errors.Is(err, biz.ErrPipelineDispatchBlocked) {
		t.Fatalf("Stop-first still granted the version write: %v", err)
	}
	state, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || state.FinetuningRegistration == nil {
		t.Fatal(err)
	}
	container := biz.RegistrationDispatchFor(state.FinetuningRegistration, biz.OpCreateModel)
	if container == nil || container.State != biz.DispatchConfirmed || container.ReceiptID == "" ||
		biz.RegistrationDispatchFor(state.FinetuningRegistration, biz.OpCreateModelVersion) != nil {
		t.Fatalf("container receipt was lost or a version write was invented: %+v", state.FinetuningRegistration)
	}
	if state.FinetuningRegistration.State != biz.RegistrationStatePending {
		t.Fatalf("container-only write claimed READY: %+v", state.FinetuningRegistration)
	}
}

func TestRegistrationVersionRequiresVerifiedContainer(t *testing.T) {
	open, _, authority := registrationReadyFixture(t)
	ctx := context.Background()
	repository := lifecycle.New(open())
	// The new branch writes the version only under a resolved target; a container
	// permit that is still in flight is not a container receipt.
	if _, err := repository.PermitRegistrationDispatch(ctx, authority, biz.OpCreateModelVersion, "trace-version"); !errors.Is(err, biz.ErrRuntimeNotReady) {
		t.Fatalf("version write preceded the container receipt: %v", err)
	}
	permit, err := repository.PermitRegistrationDispatch(ctx, authority, biz.OpCreateModel, "trace-container")
	if err != nil || !permit.SendPermit {
		t.Fatal("container permit", err)
	}
	if _, err := repository.PermitRegistrationDispatch(ctx, authority, biz.OpCreateModelVersion, "trace-version"); !errors.Is(err, biz.ErrRuntimeNotReady) {
		t.Fatalf("in-flight container authorized a version write: %v", err)
	}
	if _, err := repository.RecordRegistrationOutcome(ctx, authority, biz.OpCreateModel, confirmedOutcome(t, authority, permit.Dispatch)); err != nil {
		t.Fatal("container receipt", err)
	}
	version, err := repository.PermitRegistrationDispatch(ctx, authority, biz.OpCreateModelVersion, "trace-version")
	if err != nil || !version.SendPermit {
		t.Fatalf("verified container did not permit the version write: %v", err)
	}
	// A lost version response is UNKNOWN, never a silent retry or READY.
	unknown := version.Dispatch
	unknown.State = biz.DispatchUnknown
	state, err := repository.RecordRegistrationOutcome(ctx, authority, biz.OpCreateModelVersion, unknown)
	if err != nil {
		t.Fatal(err)
	}
	if state.FinetuningRegistration.State != biz.RegistrationStatePending {
		t.Fatalf("UNKNOWN version result claimed READY: %+v", state.FinetuningRegistration)
	}
	// Verifying the original result settles it; only a confirmed version is READY.
	state, err = repository.RecordRegistrationOutcome(ctx, authority, biz.OpCreateModelVersion, confirmedOutcome(t, authority, version.Dispatch))
	if err != nil || state.FinetuningRegistration.State != biz.RegistrationStateReady {
		t.Fatalf("confirmed version did not become READY: %v %+v", err, state.FinetuningRegistration)
	}
	// A READY registration replays its original result; it never starts a new
	// external write. A previously permitted operation returns its stored fact.
	if replay, err := lifecycle.New(open()).PermitRegistrationDispatch(ctx, authority, biz.OpCreateModelVersion, "trace-again"); err != nil || replay.SendPermit {
		t.Fatalf("READY registration re-sent the version write: %v %+v", err, replay)
	}
	// An operation that was never permitted cannot begin after READY.
	if _, err := lifecycle.New(open()).PermitRegistrationDispatch(ctx, authority, biz.OpControlledCopy, "trace-new"); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatalf("READY registration accepted a new external write: %v", err)
	}
}

func TestRegistrationOutcomeMustMatchOriginalPermit(t *testing.T) {
	open, _, authority := registrationReadyFixture(t)
	ctx := context.Background()
	repository := lifecycle.New(open())
	permit, err := repository.PermitRegistrationDispatch(ctx, authority, biz.OpCreateModel, "trace-container")
	if err != nil || !permit.SendPermit {
		t.Fatal("container permit", err)
	}
	// A receipt cannot be pinned to a different trace than the stored permit.
	swapped := permit.Dispatch
	swapped.RequestID = "trace-other"
	swapped.State = biz.DispatchConfirmed
	swapped.ReceiptID = uuid.New().String()
	if _, err := repository.RecordRegistrationOutcome(ctx, authority, biz.OpCreateModel, swapped); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatalf("mismatched receipt was accepted: %v", err)
	}
	// A confirmed outcome without a receipt is not a verified result.
	empty := permit.Dispatch
	empty.State = biz.DispatchConfirmed
	if _, err := repository.RecordRegistrationOutcome(ctx, authority, biz.OpCreateModel, empty); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatalf("receipt-less confirmation was accepted: %v", err)
	}
	if _, err := repository.RecordRegistrationOutcome(ctx, authority, biz.OpCreateModelVersion, confirmedOutcome(t, authority, permit.Dispatch)); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatalf("outcome was recorded for an unpermitted operation: %v", err)
	}
}

func TestRegistrationUnavailableIsTerminalForTheIntent(t *testing.T) {
	open, _, authority := registrationReadyFixture(t)
	ctx := context.Background()
	repository := lifecycle.New(open())
	permit, err := repository.PermitRegistrationDispatch(ctx, authority, biz.OpCreateModel, "trace-container")
	if err != nil || !permit.SendPermit {
		t.Fatal("container permit", err)
	}
	// Unavailability cannot overwrite an intent that already holds a permit.
	if _, _, err := repository.RecordRegistrationUnavailable(ctx, authority); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatalf("unavailable record replaced an in-flight intent: %v", err)
	}
}

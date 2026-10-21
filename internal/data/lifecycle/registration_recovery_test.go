package lifecycle_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
)

// Only the existing Model/transfer boundary helper is substituted. All clocks,
// original-content checks, permits, close/Stop fences and outcomes use the real
// restricted PostgreSQL and the production Repository/Orchestrator.
type recoveryModelBoundary struct {
	*registrationModelBoundary
	reads          int
	readDeadlines  []time.Time
	grantDeadlines []time.Time
	writeDeadlines []time.Time
	beforeComplete func()
}

func (b *recoveryModelBoundary) deadline(ctx context.Context) {
	b.t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok {
		b.t.Fatal("recovery write lost its finite business deadline")
	}
	b.writeDeadlines = append(b.writeDeadlines, deadline)
}

func (b *recoveryModelBoundary) PrepareArtifact(ctx context.Context, request biz.TrainedArtifactRequest) (biz.TrainedArtifactSession, error) {
	b.deadline(ctx)
	return b.registrationModelBoundary.PrepareArtifact(ctx, request)
}

func (b *recoveryModelBoundary) ReadArtifact(ctx context.Context, request biz.TrainedArtifactRequest) (biz.TrainedArtifactObservation, error) {
	b.reads++
	deadline, ok := ctx.Deadline()
	if !ok {
		b.t.Fatal("original-key recovery read lost finite business deadline")
	}
	b.readDeadlines = append(b.readDeadlines, deadline)
	return b.registrationModelBoundary.ReadArtifact(ctx, request)
}

func (b *recoveryModelBoundary) ArtifactUploadGrant(ctx context.Context, request biz.TrainedArtifactRequest, session biz.TrainedArtifactSession) (biz.TrainedArtifactUploadGrant, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		b.t.Fatal("original upload-ticket read lost finite business deadline")
	}
	b.grantDeadlines = append(b.grantDeadlines, deadline)
	return b.registrationModelBoundary.ArtifactUploadGrant(ctx, request, session)
}

func (b *recoveryModelBoundary) CopyTrainedArtifact(ctx context.Context, admitted biz.Execution, object cpup01.FixedObjectRef, grant biz.TrainedArtifactUploadGrant) error {
	b.deadline(ctx)
	return b.registrationModelBoundary.CopyTrainedArtifact(ctx, admitted, object, grant)
}

func (b *recoveryModelBoundary) CompleteArtifact(ctx context.Context, request biz.TrainedArtifactRequest, session biz.TrainedArtifactSession) (biz.FinetuningModelReceipt, error) {
	b.deadline(ctx)
	if b.beforeComplete != nil {
		b.beforeComplete()
	}
	return b.registrationModelBoundary.CompleteArtifact(ctx, request, session)
}

func recoveryWriter(t *testing.T, repository *lifecycle.Repository, boundary *recoveryModelBoundary) *biz.FinetuningRegistrationOrchestrator {
	t.Helper()
	writer, err := biz.NewFinetuningRegistrationOrchestrator(repository, boundary, boundary)
	if err != nil {
		t.Fatal(err)
	}
	return writer
}

func closeRegistrationFailure(t *testing.T, repository *lifecycle.Repository, authority biz.RunAuthorityCandidate) biz.ExecutionRuntime {
	t.Helper()
	ctx := context.Background()
	state, err := repository.GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || state.Observation == nil {
		t.Fatal("published training facts missing", err)
	}
	closing, _, err := repository.RequestRuntimeClose(ctx, authority, "STEP_FAILED")
	if err != nil {
		t.Fatal("original failed close", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	evidence := biz.ManagedCloseEvidence{
		RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, ObservedAt: now,
		OwnerTermination: &biz.ManagedOwnerTermination{
			RunState: "FAILED", RunFinishedAt: now, WorkflowPhase: "Failed", WorkflowFinishedAt: now,
			WorkflowResourceVersion: "original-failed-workflow-rv", WorkflowNodesSHA256: strings.Repeat("a", 64),
		},
	}
	zero := int32(0)
	for _, step := range state.FinetuningSteps {
		evidence.Resources = append(evidence.Resources, biz.RuntimeResource{APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: step.Binding.PodName, UID: step.Binding.PodUID, OwnerUID: authority.WorkflowUID, APIObjectPresent: true, Terminal: true, ExitCode: &zero})
	}
	closed, err := repository.ConfirmRuntimeClosed(ctx, authority, closing.CloseGeneration, *state.Observation, evidence)
	if err != nil || closed.ClosedAt == nil || closed.CloseEvidence == nil {
		t.Fatal("failed close lacked durable owner proof", err)
	}
	return closed
}

func assertRecoveryOriginals(t *testing.T, before, after biz.ExecutionRuntime) {
	t.Helper()
	a, b := before.FinetuningRegistration, after.FinetuningRegistration
	if a == nil || b == nil || a.ContentSHA256 != b.ContentSHA256 || a.VersionKey != b.VersionKey || a.VersionTag != b.VersionTag || a.BaseModelVersionID != b.BaseModelVersionID || a.VerifyManifestSHA256 != b.VerifyManifestSHA256 || a.PublicationID != b.PublicationID || !reflect.DeepEqual(a.TargetModelID, b.TargetModelID) || !reflect.DeepEqual(a.NewModelName, b.NewModelName) || !reflect.DeepEqual(a.Bundle, b.Bundle) || !a.RecordedAt.Equal(b.RecordedAt) || !a.DeadlineAt.Equal(b.DeadlineAt) {
		t.Fatal("recovery replaced the original target, object, content or normal REGISTER clock")
	}
	for _, original := range a.Dispatches {
		current := biz.RegistrationDispatchFor(b, original.Operation)
		if current == nil || current.RequestID != original.RequestID || !current.PermittedAt.Equal(original.PermittedAt) || !current.DeadlineAt.Equal(original.DeadlineAt) || current.ContentSHA256 != original.ContentSHA256 {
			t.Fatal("recovery replaced an already-dispatched original attempt or deadline")
		}
	}
	if !reflect.DeepEqual(before.StepTimings, after.StepTimings) || !reflect.DeepEqual(before.TrainingWindow, after.TrainingWindow) || !reflect.DeepEqual(before.Training, after.Training) || !reflect.DeepEqual(before.FinetuningSteps, after.FinetuningSteps) || !reflect.DeepEqual(before.Publication, after.Publication) || !reflect.DeepEqual(before.ClosedAt, after.ClosedAt) || !reflect.DeepEqual(before.CloseEvidence, after.CloseEvidence) || before.CloseGeneration != after.CloseGeneration || before.CloseOperationID != after.CloseOperationID || before.CloseReason != after.CloseReason {
		t.Fatal("recovery rewrote original execution clocks, computation, publication or failed close history")
	}
}

func awaitRegistrationDatabaseTime(t *testing.T, pool *pgxpool.Pool, until time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		var now time.Time
		if err := pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
			t.Fatal(err)
		}
		if !now.Before(until) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestClosedRegistrationRecoveryHasOneIndependentFiniteDatabaseWindow(t *testing.T) {
	open, admission, authority := registrationReadyFixture(t, func(a *biz.Admission) {
		a.Snapshot.Timing.StepTimeoutSeconds = 2
		a.Snapshot.DeadlineAt = a.AcceptedAt.Add(2 * time.Second)
	})
	ctx := context.Background()
	repository := lifecycle.New(open())
	closed := closeRegistrationFailure(t, repository, authority)
	awaitRegistrationDatabaseTime(t, open(), admission.Snapshot.DeadlineAt)
	started, err := repository.BeginRegistrationRecovery(ctx, authority)
	if err != nil || started.FinetuningRegistration.Recovery == nil {
		t.Fatal("expired original execution prevented explicit recovery", err)
	}
	window := *started.FinetuningRegistration.Recovery
	if window.AdmittedAt.Before(*closed.ClosedAt) || window.AdmittedAt.Before(admission.Snapshot.DeadlineAt) || !window.DeadlineAt.Equal(window.AdmittedAt.Add(2*time.Second)) || window.CommandFence != closed.CloseGeneration {
		t.Fatal("recovery did not receive its own single frozen S window")
	}
	assertRecoveryOriginals(t, closed, started)
	reconnected := lifecycle.New(open())
	replayed, err := reconnected.BeginRegistrationRecovery(ctx, authority)
	if err != nil || !reflect.DeepEqual(replayed.FinetuningRegistration.Recovery, &window) || replayed.OwnerRevision != started.OwnerRevision {
		t.Fatal("reconnect renewed or rewrote recovery window", err)
	}
	awaitRegistrationDatabaseTime(t, open(), window.DeadlineAt)
	if _, err := lifecycle.New(open()).BeginRegistrationRecovery(ctx, authority); !errors.Is(err, biz.ErrPipelineDispatchBlocked) {
		t.Fatal("expired recovery acquired another budget", err)
	}
	permit, err := lifecycle.New(open()).PermitRegistrationRecoveryDispatch(ctx, authority, biz.OpPrepareTrainedArtifact, "expired-recovery-never-sent")
	if !errors.Is(err, biz.ErrPipelineDispatchBlocked) || permit.SendPermit {
		t.Fatal("expired recovery granted an external write", err)
	}
	stored, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || !reflect.DeepEqual(stored.FinetuningRegistration.Recovery, &window) || len(stored.FinetuningRegistration.Dispatches) != 0 {
		t.Fatal("expired replay mutated original recovery", err)
	}
	assertRecoveryOriginals(t, closed, stored)
	admitted, err := execution.New(open()).Get(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || !reflect.DeepEqual(admitted.Snapshot.Timing, admission.Snapshot.Timing) || !admitted.Snapshot.DeadlineAt.Equal(admission.Snapshot.DeadlineAt) {
		t.Fatal("explicit recovery renewed immutable execution Timing", err)
	}
}

func TestClosedRegistrationRecoveryRunsOnlyOriginalNewOrAppendAndKeepsFailureHistory(t *testing.T) {
	for _, appendTarget := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "append"}[appendTarget], func(t *testing.T) {
			open, admission, authority := registrationReadyFixture(t, func(a *biz.Admission) {
				if appendTarget {
					target := "77777777-7777-4777-8777-777777777777"
					a.Intent.TargetModelID, a.Intent.NewModelName = &target, nil
					a.Snapshot.Finetuning.TargetModelID, a.Snapshot.Finetuning.NewModelName = &target, nil
				}
			})
			ctx := context.Background()
			repository := lifecycle.New(open())
			closed := closeRegistrationFailure(t, repository, authority)
			originalExecution, err := execution.New(open()).Get(ctx, authority.TenantID, authority.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			started, err := repository.BeginRegistrationRecovery(ctx, authority)
			if err != nil {
				t.Fatal(err)
			}
			boundary := &recoveryModelBoundary{registrationModelBoundary: newRegistrationBoundary(t, started, authority)}
			ready, err := recoveryWriter(t, repository, boundary).Ensure(ctx, authority, biz.Execution{Admission: admission}, started, true)
			if err != nil || ready.FinetuningRegistration.State != biz.RegistrationStateReady || boundary.prepareWrites != 1 || boundary.copyWrites != 1 || boundary.completeWrites != 1 {
				t.Fatal("original closed registration did not finish exactly once", err)
			}
			for _, deadline := range boundary.writeDeadlines {
				if !deadline.Equal(started.FinetuningRegistration.Recovery.DeadlineAt) {
					t.Fatal("recovery write used renewed or normal execution deadline")
				}
			}
			for _, deadline := range boundary.grantDeadlines {
				if !deadline.Equal(started.FinetuningRegistration.Recovery.DeadlineAt) {
					t.Fatal("recovery upload-ticket read used renewed or normal execution deadline")
				}
			}
			assertRecoveryOriginals(t, closed, ready)
			reopened := lifecycle.New(open())
			stored, err := reopened.BeginRegistrationRecovery(ctx, authority)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := recoveryWriter(t, reopened, boundary).Ensure(ctx, authority, biz.Execution{Admission: admission}, stored, true); err != nil || boundary.prepareWrites != 1 || boundary.copyWrites != 1 || boundary.completeWrites != 1 {
				t.Fatal("READY recovery repeated external writes", err)
			}
			queryCtx := service.WithVerifiedGovernanceQuery(ctx, service.GovernanceDelivery{TenantID: admission.TenantID, Actor: admission.Actor, RequestID: "closed-registration-history"}, modeldevv1.ModelDevQueryService_GetExecution_FullMethodName)
			response, err := service.NewQuery(execution.New(open()), nil).GetExecution(queryCtx, &modeldevv1.GetExecutionRequest{ExecutionId: authority.ExecutionID})
			if err != nil || response.GetExecution().GetModelRegistration().GetState() != biz.RegistrationStateReady || response.GetExecution().GetHistory().GetRunState() != modeldevv1.KfpRunTerminalState_KFP_RUN_TERMINAL_STATE_FAILED || response.GetExecution().GetHistory().GetWorkflowPhase() != modeldevv1.WorkflowTerminalPhase_WORKFLOW_TERMINAL_PHASE_FAILED || response.GetExecution().GetStates().GetComputeState().String() != "COMPUTE_STATE_"+string(originalExecution.States.Compute) || response.GetExecution().GetStates().GetCloseState() != modeldevv1.CloseState_CLOSE_STATE_CLOSED {
				t.Fatal("READY recovery erased original compute or failed KFP/Workflow/close projection", err)
			}
		})
	}
}

func TestClosedRegistrationRecoveryShorterParentDeadlineBoundsEveryModelCall(t *testing.T) {
	for _, appendTarget := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "append"}[appendTarget], func(t *testing.T) {
			open, admission, authority := registrationReadyFixture(t, func(a *biz.Admission) {
				if appendTarget {
					target := "77777777-7777-4777-8777-777777777777"
					a.Intent.TargetModelID, a.Intent.NewModelName = &target, nil
					a.Snapshot.Finetuning.TargetModelID, a.Snapshot.Finetuning.NewModelName = &target, nil
				}
			})
			repository := lifecycle.New(open())
			closed := closeRegistrationFailure(t, repository, authority)
			started, err := repository.BeginRegistrationRecovery(context.Background(), authority)
			if err != nil {
				t.Fatal(err)
			}
			deadline := started.FinetuningRegistration.Recovery.AdmittedAt.Add(10 * time.Second)
			if !deadline.Before(started.FinetuningRegistration.Recovery.DeadlineAt) {
				t.Fatal("fixture parent must be shorter than the frozen recovery window")
			}
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			defer cancel()
			boundary := &recoveryModelBoundary{registrationModelBoundary: newRegistrationBoundary(t, started, authority)}
			ready, err := recoveryWriter(t, repository, boundary).Ensure(ctx, authority, biz.Execution{Admission: admission}, started, true)
			if err != nil || ready.FinetuningRegistration.State != biz.RegistrationStateReady || len(boundary.grantDeadlines) != 1 {
				t.Fatal("original recovery did not finish under the shorter caller deadline", err)
			}
			for _, deadlines := range [][]time.Time{boundary.grantDeadlines, boundary.readDeadlines, boundary.writeDeadlines} {
				for _, actual := range deadlines {
					if !actual.Equal(deadline) {
						t.Fatal("Model call extended the shorter parent deadline")
					}
				}
			}
			assertRecoveryOriginals(t, closed, ready)
		})
	}
}

func TestClosedRegistrationRecoveryObservesUnknownOriginalWritesWithoutResending(t *testing.T) {
	for _, unknown := range []string{"prepare", "copy", "complete", "missing-prepare"} {
		t.Run(unknown, func(t *testing.T) {
			open, admission, authority := registrationReadyFixture(t)
			ctx := context.Background()
			repository := lifecycle.New(open())
			state, err := repository.GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			boundary := &recoveryModelBoundary{registrationModelBoundary: newRegistrationBoundary(t, state, authority)}
			switch unknown {
			case "prepare", "missing-prepare":
				boundary.prepareLost, boundary.readUnavailable = true, true
				boundary.missingPrepare = unknown == "missing-prepare"
			case "copy":
				boundary.beforeCopy = func() { boundary.readUnavailable = true }
			case "complete":
				boundary.completeLost = true
			}
			if _, err := recoveryWriter(t, repository, boundary).Ensure(ctx, authority, biz.Execution{Admission: admission}, state, false); !errors.Is(err, biz.ErrModelDependencyUnavailable) {
				t.Fatal("unknown normal write was accepted", err)
			}
			closed := closeRegistrationFailure(t, repository, authority)
			started, err := lifecycle.New(open()).BeginRegistrationRecovery(ctx, authority)
			if err != nil {
				t.Fatal(err)
			}
			originalPrepare, originalCopy, originalComplete := boundary.prepareWrites, boundary.copyWrites, boundary.completeWrites
			beforeReads := boundary.reads
			beforeGrants := len(boundary.grantDeadlines)
			boundary.readUnavailable = false
			boundary.beforeCopy = nil
			ready, err := recoveryWriter(t, lifecycle.New(open()), boundary).Ensure(ctx, authority, biz.Execution{Admission: admission}, started, true)
			if unknown == "missing-prepare" {
				if !errors.Is(err, biz.ErrModelDependencyUnavailable) || boundary.copyWrites != 0 || boundary.completeWrites != 0 {
					t.Fatal("missing original session became not-sent permission", err)
				}
			} else if err != nil || ready.FinetuningRegistration.State != biz.RegistrationStateReady {
				t.Fatal("late original proof was not recovered", err)
			}
			if boundary.reads <= beforeReads || originalPrepare > 0 && boundary.prepareWrites != originalPrepare || originalCopy > 0 && boundary.copyWrites != originalCopy || originalComplete > 0 && boundary.completeWrites != originalComplete {
				t.Fatal("unknown external write was resent rather than read")
			}
			for _, deadline := range boundary.readDeadlines[beforeReads:] {
				if !deadline.Equal(started.FinetuningRegistration.Recovery.DeadlineAt) {
					t.Fatal("unknown original read reused an expired normal window or renewed recovery budget")
				}
			}
			for _, deadline := range boundary.grantDeadlines[beforeGrants:] {
				if !deadline.Equal(started.FinetuningRegistration.Recovery.DeadlineAt) {
					t.Fatal("unknown original upload-ticket read renewed recovery budget")
				}
			}
			stored, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			assertRecoveryOriginals(t, closed, stored)
		})
	}
}

func TestClosedRegistrationRecoveryStopFencesNewWritesAndPreservesLateOriginalFacts(t *testing.T) {
	for _, point := range []string{"before-prepare", "after-prepare-dispatch", "after-copy-dispatch", "after-complete-dispatch", "unknown-prepare-readback"} {
		t.Run(point, func(t *testing.T) {
			open, admission, authority := registrationReadyFixture(t)
			ctx := context.Background()
			repository := lifecycle.New(open())
			state, err := repository.GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			boundary := &recoveryModelBoundary{registrationModelBoundary: newRegistrationBoundary(t, state, authority)}
			if point == "unknown-prepare-readback" {
				boundary.prepareLost, boundary.readUnavailable = true, true
				if _, err := recoveryWriter(t, repository, boundary).Ensure(ctx, authority, biz.Execution{Admission: admission}, state, false); !errors.Is(err, biz.ErrModelDependencyUnavailable) {
					t.Fatal(err)
				}
				boundary.readUnavailable = false
			}
			closed := closeRegistrationFailure(t, repository, authority)
			started, err := repository.BeginRegistrationRecovery(ctx, authority)
			if err != nil {
				t.Fatal(err)
			}
			window := *started.FinetuningRegistration.Recovery
			stop := func() {
				receipt, err := execution.New(open()).ApplyCloseIntent(ctx, closeIntentForRequest(closedStopRequest(admission, 1)))
				if err != nil || receipt.Generation <= window.CommandFence || receipt.ClosedGeneration != closed.CloseGeneration {
					t.Fatal("new Stop did not advance command fence while retaining original close", err)
				}
			}
			switch point {
			case "before-prepare", "unknown-prepare-readback":
				stop()
			case "after-prepare-dispatch":
				boundary.beforePrepare = stop
			case "after-copy-dispatch":
				boundary.beforeCopy = stop
			case "after-complete-dispatch":
				boundary.beforeComplete = stop
			}
			result, err := recoveryWriter(t, lifecycle.New(open()), boundary).Ensure(ctx, authority, biz.Execution{Admission: admission}, started, true)
			if point == "after-complete-dispatch" {
				if err != nil || result.FinetuningRegistration.State != biz.RegistrationStateReady || result.FinetuningRegistration.Receipt == nil || boundary.completeWrites != 1 {
					t.Fatal("Stop erased a verified late original Complete fact", err)
				}
			} else if !errors.Is(err, biz.ErrPipelineDispatchBlocked) || boundary.completeWrites != 0 {
				t.Fatal("new Stop allowed a successor write", err)
			}
			if point == "before-prepare" && (boundary.prepareWrites != 0 || boundary.copyWrites != 0) || (point == "after-prepare-dispatch" || point == "unknown-prepare-readback") && (boundary.prepareWrites != 1 || boundary.copyWrites != 0) || (point == "after-copy-dispatch" || point == "after-complete-dispatch") && (boundary.prepareWrites != 1 || boundary.copyWrites != 1) {
				t.Fatal("Stop-first operation reached external write boundary")
			}
			stored, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
			if err != nil || !reflect.DeepEqual(stored.FinetuningRegistration.Recovery, &window) {
				t.Fatal("Stop renewed/replaced recovery window", err)
			}
			assertRecoveryOriginals(t, closed, stored)
			if point != "before-prepare" {
				prepared := biz.RegistrationDispatchFor(stored.FinetuningRegistration, biz.OpPrepareTrainedArtifact)
				if prepared == nil || prepared.State != biz.DispatchConfirmed || stored.FinetuningRegistration.Session == nil {
					t.Fatal("Stop lost verified late original Prepare association")
				}
			}
			if _, err := lifecycle.New(open()).BeginRegistrationRecovery(ctx, authority); point != "after-complete-dispatch" && !errors.Is(err, biz.ErrPipelineDispatchBlocked) {
				t.Fatal("same recovery reauthorized writes across newer Stop", err)
			}
		})
	}
}

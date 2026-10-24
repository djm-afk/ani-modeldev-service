package lifecycle_test

import (
	"context"
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"reflect"
	"testing"
	"time"
)

// Only Model and object transfer are external boundary substitutes. Every
// permit, Stop, original target/content, outcome, READY and reconnect below
// uses the production lifecycle repository and a real restricted PostgreSQL.
type registrationModelBoundary struct {
	t                                                *testing.T
	authority                                        biz.RunAuthorityCandidate
	original                                         biz.FinetuningRegistration
	observation                                      biz.TrainedArtifactObservation
	prepared, copied, completed                      bool
	missingPrepare, readUnavailable, pendingComplete bool
	prepareLost, completeLost                        bool
	prepareWrites, copyWrites, completeWrites        int
	beforePrepare, beforeGrant, beforeCopy           func()
}

func (b *registrationModelBoundary) check(r biz.TrainedArtifactRequest) {
	b.t.Helper()
	if r.TenantID != b.authority.TenantID || r.ExecutionID != b.authority.ExecutionID || r.Registration.ContentSHA256 != b.original.ContentSHA256 || r.Registration.VersionKey != b.original.VersionKey || r.Registration.VersionTag != b.original.VersionTag || !reflect.DeepEqual(r.Registration.TargetModelID, b.original.TargetModelID) || !reflect.DeepEqual(r.Registration.NewModelName, b.original.NewModelName) || !reflect.DeepEqual(r.Registration.Bundle, b.original.Bundle) {
		b.t.Fatal("changed original Model boundary request")
	}
}
func (b *registrationModelBoundary) PrepareArtifact(ctx context.Context, r biz.TrainedArtifactRequest) (biz.TrainedArtifactSession, error) {
	b.check(r)
	b.prepareWrites++
	if _, ok := ctx.Deadline(); !ok {
		b.t.Fatal("write lost finite original deadline")
	}
	if b.beforePrepare != nil {
		b.beforePrepare()
	}
	b.prepared = !b.missingPrepare
	if b.prepareLost || b.missingPrepare {
		return biz.TrainedArtifactSession{}, biz.ErrModelDependencyUnavailable
	}
	return b.observation.Session, nil
}
func (b *registrationModelBoundary) ReadArtifact(_ context.Context, r biz.TrainedArtifactRequest) (biz.TrainedArtifactObservation, error) {
	b.check(r)
	if !b.prepared || b.readUnavailable {
		return biz.TrainedArtifactObservation{}, biz.ErrModelDependencyUnavailable
	}
	o := b.observation
	o.State = "pending"
	o.ObjectVerified = b.copied
	o.Receipt = nil
	if b.completed {
		o.State = "ready"
		o.Receipt = b.observation.Receipt
	}
	return o, nil
}
func (b *registrationModelBoundary) ArtifactUploadGrant(_ context.Context, r biz.TrainedArtifactRequest, s biz.TrainedArtifactSession) (biz.TrainedArtifactUploadGrant, error) {
	b.check(r)
	if s != b.observation.Session {
		b.t.Fatal("ticket selected another original session")
	}
	if b.beforeGrant != nil {
		b.beforeGrant()
	}
	return biz.TrainedArtifactUploadGrant{Session: s, URL: "https://model.example.test/ani-models/" + s.StoragePath + "?original=ephemeral", ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (b *registrationModelBoundary) CopyTrainedArtifact(ctx context.Context, execution biz.Execution, object cpup01.FixedObjectRef, grant biz.TrainedArtifactUploadGrant) error {
	if execution.ExecutionID != b.authority.ExecutionID || !reflect.DeepEqual(object, b.original.Bundle) || grant.Session != b.observation.Session {
		b.t.Fatal("copy changed original source/session")
	}
	if _, ok := ctx.Deadline(); !ok {
		b.t.Fatal("copy lost original deadline")
	}
	b.copyWrites++
	if b.beforeCopy != nil {
		b.beforeCopy()
	}
	b.copied = true
	return nil
}
func (b *registrationModelBoundary) CompleteArtifact(ctx context.Context, r biz.TrainedArtifactRequest, s biz.TrainedArtifactSession) (biz.FinetuningModelReceipt, error) {
	b.check(r)
	if _, ok := ctx.Deadline(); !ok {
		b.t.Fatal("completion lost original deadline")
	}
	if !b.prepared || !b.copied || s != b.observation.Session {
		b.t.Fatal("completion preceded original copy proof")
	}
	b.completeWrites++
	b.completed = !b.pendingComplete
	if b.completeLost {
		b.readUnavailable = true
		return biz.FinetuningModelReceipt{}, biz.ErrModelDependencyUnavailable
	}
	if b.pendingComplete {
		return biz.FinetuningModelReceipt{}, biz.ErrModelDependencyUnavailable
	}
	return *b.observation.Receipt, nil
}
func newRegistrationBoundary(t *testing.T, state biz.ExecutionRuntime, a biz.RunAuthorityCandidate) *registrationModelBoundary {
	return &registrationModelBoundary{t: t, authority: a, original: *state.FinetuningRegistration, observation: registrationObservation(t, state, a)}
}
func registrationWriter(t *testing.T, repository *lifecycle.Repository, b *registrationModelBoundary) *biz.FinetuningRegistrationOrchestrator {
	t.Helper()
	w, err := biz.NewFinetuningRegistrationOrchestrator(repository, b, b)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestOriginalRegistrationOrchestratorNewAndAppendUseThreeActualPermits(t *testing.T) {
	for _, appendTarget := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "append"}[appendTarget], func(t *testing.T) {
			edit := func(a *biz.Admission) {
				if appendTarget {
					target := "77777777-7777-4777-8777-777777777777"
					a.Intent.TargetModelID = &target
					a.Intent.NewModelName = nil
					a.Snapshot.Finetuning.TargetModelID = &target
					a.Snapshot.Finetuning.NewModelName = nil
				}
			}
			open, admission, authority := registrationReadyFixture(t, edit)
			ctx := context.Background()
			repository := lifecycle.New(open())
			state, err := repository.GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			boundary := newRegistrationBoundary(t, state, authority)
			execution := biz.Execution{Admission: admission}
			ready, err := registrationWriter(t, repository, boundary).Ensure(ctx, authority, execution, state, false)
			if err != nil || ready.FinetuningRegistration.State != biz.RegistrationStateReady || !biz.ValidFinetuningModelReceipt(authority.TenantID, *ready.FinetuningRegistration, ready.FinetuningRegistration.Receipt) {
				t.Fatal("three-write registration", err)
			}
			reopened := lifecycle.New(open())
			stored, err := reopened.GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := registrationWriter(t, reopened, boundary).Ensure(ctx, authority, execution, stored, false); err != nil {
				t.Fatal(err)
			}
			if boundary.prepareWrites != 1 || boundary.copyWrites != 1 || boundary.completeWrites != 1 || len(stored.FinetuningRegistration.Dispatches) != 3 {
				t.Fatal("reconnect repeated an original external write", boundary.prepareWrites, boundary.copyWrites, boundary.completeWrites)
			}
		})
	}
}

func TestOriginalRegistrationUnknownPrepareIsObservedWithoutResending(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "lost-response-with-original-session", true: "missing-is-not-not-sent"}[missing], func(t *testing.T) {
			open, admission, a := registrationReadyFixture(t)
			ctx := context.Background()
			repository := lifecycle.New(open())
			state, err := repository.GetRuntime(ctx, a.TenantID, a.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			b := newRegistrationBoundary(t, state, a)
			b.prepareLost = true
			b.readUnavailable = true
			b.missingPrepare = missing
			if _, err := registrationWriter(t, repository, b).Ensure(ctx, a, biz.Execution{Admission: admission}, state, false); !errors.Is(err, biz.ErrModelDependencyUnavailable) {
				t.Fatal("unknown Prepare", err)
			}
			repository = lifecycle.New(open())
			state, err = repository.GetRuntime(ctx, a.TenantID, a.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			if biz.RegistrationDispatchFor(state.FinetuningRegistration, biz.OpPrepareTrainedArtifact).State != biz.DispatchUnknown || state.FinetuningRegistration.Receipt != nil {
				t.Fatal("unknown write claimed success")
			}
			b.readUnavailable = false
			final, err := registrationWriter(t, repository, b).Ensure(ctx, a, biz.Execution{Admission: admission}, state, false)
			if missing {
				if !errors.Is(err, biz.ErrModelDependencyUnavailable) || b.copyWrites != 0 || b.completeWrites != 0 {
					t.Fatal("absent original caused a new write", err)
				}
			} else if err != nil || final.FinetuningRegistration.State != biz.RegistrationStateReady {
				t.Fatal("original late Prepare could not be verified", err)
			}
			if b.prepareWrites != 1 {
				t.Fatal("unknown original Prepare was resent")
			}
		})
	}
}

func TestOriginalRegistrationStopArbitratesEachWriteWithRealPostgres(t *testing.T) {
	for _, point := range []string{"before-prepare", "after-prepare-dispatch", "before-copy-permit", "after-copy-dispatch"} {
		t.Run(point, func(t *testing.T) {
			open, admission, a := registrationReadyFixture(t)
			ctx := context.Background()
			repository := lifecycle.New(open())
			state, err := repository.GetRuntime(ctx, a.TenantID, a.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			b := newRegistrationBoundary(t, state, a)
			stop := func() {
				if err := applyFinetuningStop(ctx, open(), admission); err != nil {
					t.Fatal(err)
				}
			}
			switch point {
			case "before-prepare":
				stop()
			case "after-prepare-dispatch":
				b.beforePrepare = stop
			case "before-copy-permit":
				b.beforeGrant = stop
			case "after-copy-dispatch":
				b.beforeCopy = stop
			}
			_, err = registrationWriter(t, repository, b).Ensure(ctx, a, biz.Execution{Admission: admission}, state, false)
			if !errors.Is(err, biz.ErrPipelineDispatchBlocked) {
				t.Fatal("Stop did not block subsequent write", err)
			}
			if b.completeWrites != 0 || point == "before-prepare" && b.prepareWrites != 0 || point != "after-copy-dispatch" && b.copyWrites != 0 {
				t.Fatal("Stop-first operation reached provider", b.prepareWrites, b.copyWrites, b.completeWrites)
			}
			stored, err := lifecycle.New(open()).GetRuntime(ctx, a.TenantID, a.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.FinetuningRegistration.State == biz.RegistrationStateReady || stored.FinetuningRegistration.Receipt != nil {
				t.Fatal("late prepare/copy erased Stop or manufactured READY")
			}
		})
	}
}

func TestOriginalRegistrationCompleteNeedsExactReadyReadback(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "lost-complete-response", true: "pending-is-not-ready"}[pending], func(t *testing.T) {
			open, admission, a := registrationReadyFixture(t)
			ctx := context.Background()
			repository := lifecycle.New(open())
			state, err := repository.GetRuntime(ctx, a.TenantID, a.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			b := newRegistrationBoundary(t, state, a)
			b.pendingComplete = pending
			b.completeLost = !pending
			if _, err := registrationWriter(t, repository, b).Ensure(ctx, a, biz.Execution{Admission: admission}, state, false); !errors.Is(err, biz.ErrModelDependencyUnavailable) {
				t.Fatal("unverified completion became READY", err)
			}
			repository = lifecycle.New(open())
			state, err = repository.GetRuntime(ctx, a.TenantID, a.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			if state.FinetuningRegistration.Receipt != nil || state.FinetuningRegistration.State == biz.RegistrationStateReady {
				t.Fatal("pending/unread original became READY")
			}
			b.readUnavailable = false
			final, err := registrationWriter(t, repository, b).Ensure(ctx, a, biz.Execution{Admission: admission}, state, false)
			if pending {
				if !errors.Is(err, biz.ErrModelDependencyUnavailable) {
					t.Fatal("pending completion accepted", err)
				}
			} else if err != nil || final.FinetuningRegistration.State != biz.RegistrationStateReady {
				t.Fatal("original complete receipt not recovered", err)
			}
			if b.prepareWrites != 1 || b.copyWrites != 1 || b.completeWrites != 1 {
				t.Fatal("unknown Complete was resent")
			}
		})
	}
}

func TestOriginalRegistrationRejectsChangedFrozenExecutionBeforeIO(t *testing.T) {
	open, admission, a := registrationReadyFixture(t)
	ctx := context.Background()
	repository := lifecycle.New(open())
	state, err := repository.GetRuntime(ctx, a.TenantID, a.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	b := newRegistrationBoundary(t, state, a)
	admission.Snapshot.PublicationScope.ApprovedPrefix = "other/scope"
	if _, err := registrationWriter(t, repository, b).Ensure(ctx, a, biz.Execution{Admission: admission}, state, false); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("changed frozen source accepted", err)
	}
	if b.prepareWrites != 0 || b.copyWrites != 0 || b.completeWrites != 0 {
		t.Fatal("changed source reached Model")
	}
}

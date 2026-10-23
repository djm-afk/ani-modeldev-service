package runtimeproof_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/cleanup"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/stepidentity"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
)

func retryOperations(t *testing.T, f *computeCleanupFixture) *biz.ExecutionOperations {
	t.Helper()
	pool := f.step.open()
	identity, err := stepidentity.New(f.kube, "managed-test-audience")
	if err != nil {
		t.Fatal(err)
	}
	steps, err := biz.NewManagedSteps(submission.New(pool), execution.New(pool), identity, f.step.proof)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := biz.NewManagedRuntime(steps, lifecycle.New(pool), trainer.New(f.kube), f.step.proof, f.step.proof)
	if err != nil {
		t.Fatal(err)
	}
	closer, err := biz.NewExecutionCloser(runtime, f.step.f.runs, f.step.proof, f.step.proof)
	if err != nil {
		t.Fatal(err)
	}
	closer, err = closer.WithComputeCleanup(f.step.proof)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := biz.NewManagedExecutionOperations(execution.New(pool), closer, cleanup.New(f.kube, f.step.proof))
	if err != nil {
		t.Fatal(err)
	}
	return operations
}

func explicitRetry(t *testing.T, operations *biz.ExecutionOperations, f *computeCleanupFixture) error {
	t.Helper()
	// The old owner can only hit the exhausted cap: no behavior is mocked.
	method := reflect.ValueOf(operations).MethodByName("ReconcileWithActor")
	if !method.IsValid() {
		_, err := operations.Reconcile(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
		return err
	}
	values := method.Call([]reflect.Value{reflect.ValueOf(context.Background()), reflect.ValueOf(f.step.f.execution.TenantID), reflect.ValueOf(f.step.f.execution.ExecutionID), reflect.ValueOf("governance:operator-3")})
	if values[1].IsNil() {
		return nil
	}
	return values[1].Interface().(error)
}

func exhaustedCleanup(t *testing.T) (*computeCleanupFixture, *configuredCleanupObserver, *biz.ExecutionOperations) {
	t.Helper()
	f := newComputeCleanupFixture(t, false)
	observer := &configuredCleanupObserver{TrainingRuntime: trainer.New(f.kube), err: biz.ErrRuntimeNotReady}
	f.step.proof.WithTrainingRuntime(observer)
	for i := 0; i < 3; i++ {
		result, err := f.worker(t).ReconcileOnce(context.Background())
		if err != nil || result.Unresolved != 1 {
			t.Fatal("original failed cleanup", result, err)
		}
	}
	operations := retryOperations(t, f)
	_, _ = operations.Reconcile(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
	state, err := lifecycle.New(f.step.open()).GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
	if err != nil || state.ComputeCleanup == nil || state.ComputeCleanup.Attempts != 3 || state.ComputeCleanup.LastError != "ATTEMPTS_EXHAUSTED" || state.ComputeCleanup.LogsReady || len(f.deleted) != 0 {
		t.Fatal("original pre-DELETE exhaustion", state.ComputeCleanup, err)
	}
	observer.err = nil
	return f, observer, operations
}

func TestExplicitComputeCleanupRetryPreservesFailedAttemptsAndOriginalScope(t *testing.T) {
	f, _, operations := exhaustedCleanup(t)
	before, _ := lifecycle.New(f.step.open()).GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
	if err := explicitRetry(t, operations, f); err != nil {
		t.Fatalf("EXHAUSTED_PREDELETE_OPERATOR_RECOVERY_MISSING: %v", err)
	}
	after, err := lifecycle.New(f.step.open()).GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
	if err != nil || after.ComputeCleanup.Attempts != 4 || after.ComputeCleanup.Phase != "APPLIED" || len(f.deleted) != 7 || f.logReads != 1 {
		t.Fatal("one extra original cleanup did not finish", after.ComputeCleanup, err)
	}
	if before.CloseOperationID != after.CloseOperationID || before.CloseGeneration != after.CloseGeneration || !reflect.DeepEqual(before.ComputeCleanup.Targets, after.ComputeCleanup.Targets) || !reflect.DeepEqual(before.Observation, after.Observation) {
		t.Fatal("operator retry changed original close or process identities")
	}
	field := reflect.ValueOf(after.ComputeCleanup).Elem().FieldByName("OperatorRetry")
	if !field.IsValid() || field.IsNil() {
		t.Fatal("operator admission audit missing")
	}
	admission := field.Elem()
	if admission.FieldByName("Actor").String() != "governance:operator-3" || admission.FieldByName("OriginalAttempts").Uint() != 3 || admission.FieldByName("OriginalError").String() != "ATTEMPTS_EXHAUSTED" {
		t.Fatal("original failure/actor audit lost")
	}
	at := admission.FieldByName("AdmittedAt").Interface().(time.Time)
	deadline := admission.FieldByName("DeadlineAt").Interface().(time.Time)
	if deadline.Sub(at) != 2*time.Minute {
		t.Fatal("unbounded recovery deadline")
	}
	if err := explicitRetry(t, operations, f); err != nil {
		t.Fatal("completed exact replay", err)
	}
	final, _ := lifecycle.New(f.step.open()).GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
	if !reflect.DeepEqual(after.ComputeCleanup, final.ComputeCleanup) || len(f.deleted) != 7 || f.logReads != 1 {
		t.Fatal("repeat operator call renewed the one-shot recovery")
	}
}

func TestExplicitComputeCleanupRetryDoesNotAuthorizeReplacementUID(t *testing.T) {
	f, _, operations := exhaustedCleanup(t)
	for _, object := range f.objects {
		if object.GetKind() == "Pod" {
			object.SetUID("replacement-pod")
		}
	}
	if err := explicitRetry(t, operations, f); err == nil {
		t.Fatal("replacement UID granted a retry")
	}
	state, err := lifecycle.New(f.step.open()).GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
	if err != nil || state.ComputeCleanup.Attempts != 3 || len(f.deleted) != 0 || f.logReads != 0 {
		t.Fatal("failed fresh preflight changed original fence", state.ComputeCleanup, err)
	}
}

func TestExplicitComputeCleanupRetryRequiresClosedUntouchedOriginalReceipt(t *testing.T) {
	f, _, _ := exhaustedCleanup(t)
	repository := lifecycle.New(f.step.open())
	state, err := repository.GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"requested", "confirmed absent", "not closed", "changed original UID"} {
		t.Run(mode, func(t *testing.T) {
			raw, _ := json.Marshal(state)
			var changed biz.ExecutionRuntime
			if err := json.Unmarshal(raw, &changed); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "requested":
				changed.ComputeCleanup.Requested = append(changed.ComputeCleanup.Requested, changed.ComputeCleanup.Targets[0])
			case "confirmed absent":
				changed.ComputeCleanup.ConfirmedAbsent = append(changed.ComputeCleanup.ConfirmedAbsent, changed.ComputeCleanup.Targets[0])
			case "not closed":
				changed.ClosedAt = nil
			case "changed original UID":
				changed.ComputeCleanup.Targets[0].UID = "replacement-uid"
			}
			if biz.ComputeCleanupRetryEligible(f.step.f.execution, f.step.f.authority(), changed) {
				t.Fatal("non-original pre-DELETE receipt admitted")
			}
		})
	}
	logs, err := f.step.proof.CaptureComputeCleanupLogs(context.Background(), f.step.f.execution, f.step.f.authority(), state)
	if err != nil {
		t.Fatal(err)
	}
	logged, err := repository.RecordComputeCleanupLogs(context.Background(), f.step.f.authority(), state.CloseGeneration, logs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AdmitComputeCleanupRetry(context.Background(), f.step.f.authority(), state.CloseGeneration, "governance:operator-3", logs); err == nil {
		t.Fatal("existing requested intent was reset by operator recovery")
	}
	after, err := repository.GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
	if err != nil || !reflect.DeepEqual(logged.ComputeCleanup, after.ComputeCleanup) || len(f.deleted) != 0 {
		t.Fatal("rejected admission changed original audit", err)
	}
}

func TestExplicitComputeCleanupRetryDeadlineBlocksFurtherDeletes(t *testing.T) {
	f, _, _ := exhaustedCleanup(t)
	repository := lifecycle.New(f.step.open())
	state, err := repository.GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	logs, err := f.step.proof.CaptureComputeCleanupLogs(context.Background(), f.step.f.execution, f.step.f.authority(), state)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := repository.AdmitComputeCleanupRetry(context.Background(), f.step.f.authority(), state.CloseGeneration, "governance:operator-3", logs)
	if err != nil {
		t.Fatal(err)
	}
	active, err := repository.StartComputeCleanupAttempt(context.Background(), f.step.f.authority(), state.CloseGeneration)
	if err != nil || active.ComputeCleanup.Attempts != 4 {
		t.Fatal("one fourth attempt", err)
	}
	// An independently read boundary must fail closed after the DB-issued
	// deadline. The durable admission is never rewritten to renew it.
	active.ComputeCleanup.OperatorRetry.DeadlineAt = time.Now().Add(-time.Second)
	if _, err := f.step.proof.ReconcileComputeCleanup(context.Background(), f.step.f.execution, f.step.f.authority(), active); err == nil || len(f.deleted) != 0 {
		t.Fatal("expired retry performed a DELETE", err)
	}
	replayed, err := repository.AdmitComputeCleanupRetry(context.Background(), f.step.f.authority(), state.CloseGeneration, "governance:another-operator", logs)
	if err != nil || !reflect.DeepEqual(admitted.ComputeCleanup.OperatorRetry, replayed.ComputeCleanup.OperatorRetry) {
		t.Fatal("replay renewed operator deadline or actor", err)
	}
	if _, err := repository.StartComputeCleanupAttempt(context.Background(), f.step.f.authority(), state.CloseGeneration); err == nil {
		t.Fatal("operator retry allowed a fifth attempt")
	}
}

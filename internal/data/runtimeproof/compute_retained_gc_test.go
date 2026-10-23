package runtimeproof_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
)

func TestOriginalStoppedPodGCAfterClosePreservesCleanupAndOperatorRecovery(t *testing.T) {
	for _, failedAttempts := range []int{1, 3} {
		name := map[int]string{1: "ordinary-owner-attempt", 3: "exhausted-original-operator-retry"}[failedAttempts]
		t.Run(name, func(t *testing.T) {
			f := newComputeCleanupClosingFixture(t, true, "USER_STOP")
			observer := &configuredCleanupObserver{TrainingRuntime: trainer.New(f.kube), err: biz.ErrRuntimeNotReady}
			f.step.proof.WithTrainingRuntime(observer)
			for i := 0; i < failedAttempts; i++ {
				result, err := f.worker(t).ReconcileOnce(context.Background())
				if err != nil || result.Unresolved != 1 {
					t.Fatal("original pre-DELETE capture failure", result, err)
				}
			}
			if failedAttempts == 3 {
				// The normal owner records the exhausted cap without renewing it.
				_, _ = retryOperations(t, f).Reconcile(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
			}
			pool := f.step.open()
			defer pool.Close()
			repository := lifecycle.New(pool)
			before, err := repository.GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
			if err != nil || before.ClosedAt == nil || before.StepExitCleanup.CompletedAt == nil || before.ComputeCleanup.Attempts != uint32(failedAttempts) || before.ComputeCleanup.LogsReady || len(before.ComputeCleanup.Targets) != 7 {
				t.Fatal("original CLOSED scope was not frozen before GC", before.ComputeCleanup, err)
			}
			if failedAttempts == 3 && before.ComputeCleanup.LastError != "ATTEMPTS_EXHAUSTED" {
				t.Fatal("normal owner did not retain the exhausted attempt cap")
			}
			var retained *biz.RecordedTrainingLog
			for _, fact := range before.Observation.Resources {
				if fact.Kind == "Pod" {
					if !fact.APIObjectPresent || !fact.Terminal || fact.ExitCode == nil || *fact.ExitCode != 143 || fact.TrainingLog == nil {
						t.Fatal("fixture lost the actual stopped worker exit/log", fact)
					}
					retained = fact.TrainingLog
				}
			}
			if retained == nil {
				t.Fatal("original worker log missing")
			}
			// Only the external Kubernetes boundary changes: normal GC removes
			// the original Pod after its real finalizer-release and CLOSED commit.
			f.mu.Lock()
			for path, object := range f.objects {
				if object.GetKind() == "Pod" {
					if len(object.GetFinalizers()) != 0 {
						f.mu.Unlock()
						t.Fatal("GC preceded normal retention release")
					}
					delete(f.objects, path)
					f.step.mu.Lock()
					delete(f.step.pods, object.GetName())
					f.step.mu.Unlock()
				}
			}
			f.mu.Unlock()
			observer.err = nil
			reads := f.logReads
			if failedAttempts == 3 {
				if err := explicitRetry(t, retryOperations(t, f), f); err != nil {
					t.Fatalf("normal original-scope operator retry rejected retained exit/log after GC: %v", err)
				}
			} else {
				result, err := f.worker(t).ReconcileOnce(context.Background())
				if err != nil || result.Unresolved != 0 {
					t.Fatalf("normal owner capture rejected retained exit/log after GC: %+v %v", result, err)
				}
			}
			after, err := repository.GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
			if err != nil || after.ComputeCleanup.Phase != "APPLIED" || !after.ComputeCleanup.LogsReady || len(after.ComputeCleanup.ConfirmedAbsent) != 7 || len(after.ComputeCleanup.Logs) != 1 || f.logReads != reads {
				t.Fatal("original cleanup did not reuse its durable worker log", after.ComputeCleanup, err)
			}
			if after.ComputeCleanup.Attempts != uint32(failedAttempts+1) || after.ComputeCleanup.PlanHash != before.ComputeCleanup.PlanHash || !reflect.DeepEqual(after.ComputeCleanup.Targets, before.ComputeCleanup.Targets) || !reflect.DeepEqual(after.Observation, before.Observation) || !reflect.DeepEqual(after.ComputeCleanup.Logs[0], *retained) {
				t.Fatal("GC recovery rewrote original scope, observation, log or attempt budget")
			}
			if failedAttempts == 3 && (after.ComputeCleanup.OperatorRetry == nil || after.ComputeCleanup.OperatorRetry.OriginalAttempts != 3) {
				t.Fatal("normal retry lost the original exhausted-attempt audit")
			}
		})
	}
}

func TestPreDeletePodGCCaptureRejectsMissingOriginalEvidence(t *testing.T) {
	for _, attack := range []string{"missing-log", "missing-exit", "missing-controller", "another-original-pod-log"} {
		t.Run(attack, func(t *testing.T) {
			f, _, _ := exhaustedCleanup(t)
			pool := f.step.open()
			defer pool.Close()
			state, err := lifecycle.New(pool).GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			// A missing original Pod is not sufficient when any original exit,
			// retained log or still-required controller proof is absent.
			f.mu.Lock()
			for path, object := range f.objects {
				if object.GetKind() == "Pod" || attack == "missing-controller" && object.GetKind() == "JobSet" {
					delete(f.objects, path)
					if object.GetKind() == "Pod" {
						f.step.mu.Lock()
						delete(f.step.pods, object.GetName())
						f.step.mu.Unlock()
					}
				}
			}
			f.mu.Unlock()
			for i := range state.Observation.Resources {
				pod := &state.Observation.Resources[i]
				if pod.Kind == "Pod" {
					if attack == "missing-log" {
						pod.TrainingLog = nil
					} else if attack == "missing-exit" {
						pod.ExitCode = nil
					} else if attack == "another-original-pod-log" {
						prior := *pod
						prior.UID, prior.Name, prior.APIObjectPresent = "prior-training-pod", "prior-training", false
						prior.TrainingLog = nil
						wrong := *pod.TrainingLog
						state.Observation.Resources = append(state.Observation.Resources, prior)
						logID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(f.step.f.execution.ExecutionID+":"+prior.UID+":node")).String()
						record := biz.QueryRecord{Execution: f.step.f.execution, Runtime: state}
						wrong.Source, err = biz.TrainingLogSourceFor(record, logID)
						if err != nil || biz.ValidateRecordedTrainingLogs(record, []biz.RecordedTrainingLog{wrong}) != nil {
							t.Fatal("counterexample must be a valid log for a different original Pod", err)
						}
						state.Observation.Resources[i].TrainingLog = &wrong
					}
				}
			}
			if attack == "missing-log" || attack == "another-original-pod-log" {
				// Model a legacy close whose original frozen scope has no saved
				// tail; the live Pod would be needed for its first valid capture.
				targets, hash, err := biz.ComputeCleanupTargets(f.step.f.execution, f.step.f.authority(), state)
				if err != nil {
					t.Fatal(err)
				}
				state.ComputeCleanup.Targets, state.ComputeCleanup.PlanHash = targets, hash
			}
			logs, err := f.step.proof.CaptureComputeCleanupLogs(context.Background(), f.step.f.execution, f.step.f.authority(), state)
			if err == nil || len(logs) != 0 || len(f.deleted) != 0 {
				t.Fatalf("unproven GC authorized capture or deletion: logs=%v err=%v deletes=%v", logs, err, f.deleted)
			}
		})
	}
}

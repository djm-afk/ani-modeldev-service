package runtimeproof_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/cleanup"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestAutomaticComputeCleanupCompletedBeforeCloseWithoutSetSuspendedReport(t *testing.T) {
	for _, attack := range []string{"original", "completed-after-close", "missing-completion-time", "future-completion", "duplicate-completion", "set-generation3", "set-not-suspended", "train-no-suspend-report", "set-replacement", "set-foreign-owner", "unknown-running-writer", "original-writer-running", "missing-log"} {
		t.Run(attack, func(t *testing.T) {
			f := newComputeCleanupClosingFixture(t, false, "USER_STOP", completedSetSuspendBoundary)
			state, err := lifecycle.New(f.step.open()).GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			train, set := completedBeforeCloseControllers(t, f, state.CloseRequestedAt)
			condition := func(at time.Time) []any {
				return []any{map[string]any{"type": "Completed", "status": "True", "lastTransitionTime": at.Format(time.RFC3339Nano)}}
			}
			switch attack {
			case "completed-after-close":
				_ = unstructured.SetNestedSlice(set.Object, condition(state.CloseRequestedAt.Add(time.Nanosecond)), "status", "conditions")
			case "missing-completion-time":
				_ = unstructured.SetNestedSlice(set.Object, []any{map[string]any{"type": "Completed", "status": "True"}}, "status", "conditions")
			case "future-completion":
				_ = unstructured.SetNestedSlice(set.Object, condition(time.Now().Add(time.Hour)), "status", "conditions")
			case "duplicate-completion":
				rows := condition(state.CloseRequestedAt.Add(-time.Second))
				rows = append(rows, rows[0])
				_ = unstructured.SetNestedSlice(set.Object, rows, "status", "conditions")
			case "set-generation3":
				set.SetGeneration(3)
			case "set-not-suspended":
				_ = unstructured.SetNestedField(set.Object, false, "spec", "suspend")
			case "train-no-suspend-report":
				_ = unstructured.SetNestedSlice(train.Object, []any{}, "status", "conditions")
			case "set-replacement":
				set.SetUID("replacement-set")
			case "set-foreign-owner":
				owners := set.GetOwnerReferences()
				owners[0].UID = "foreign-train"
				set.SetOwnerReferences(owners)
			case "unknown-running-writer", "original-writer-running":
				for _, object := range f.objects {
					if object.GetKind() != "Pod" {
						continue
					}
					pod := object
					if attack == "unknown-running-writer" {
						pod = object.DeepCopy()
						pod.SetName("unknown-running-writer")
						pod.SetUID("unknown-running-uid")
						pod.SetOwnerReferences(nil)
						f.step.pods[pod.GetName()] = pod
					}
					_ = unstructured.SetNestedField(pod.Object, "Running", "status", "phase")
					_ = unstructured.SetNestedSlice(pod.Object, []any{map[string]any{"name": "node", "state": map[string]any{"running": map[string]any{"startedAt": time.Now().Add(-time.Minute).Format(time.RFC3339Nano)}}}}, "status", "containerStatuses")
				}
			case "missing-log":
				f.step.serveExtra = func(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
					if strings.HasSuffix(r.URL.Path, "/pods/actual-training/log") {
						w.WriteHeader(http.StatusForbidden)
						return true
					}
					return f.serve(t, w, r)
				}
			}
			result, err := f.worker(t).ReconcileOnce(context.Background())
			if attack != "original" {
				if err != nil || result.Unresolved != 1 || len(f.deleted) != 0 {
					t.Fatal("incomplete completion/identity/writer/log facts allowed a DELETE", result, err, f.deleted)
				}
				return
			}
			state, readErr := lifecycle.New(f.step.open()).GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
			if err != nil || readErr != nil || result.Unresolved != 0 || state.ComputeCleanup == nil || state.ComputeCleanup.Phase != "APPLIED" || !state.ComputeCleanup.LogsReady || len(state.ComputeCleanup.ConfirmedAbsent) != 5 || len(f.deleted) != 5 || f.logReads != 1 || state.Workspace == nil {
				t.Fatal("completed original set could not finish bounded automatic cleanup", result, err, readErr, "logs", f.logReads, "deletes", len(f.deleted))
			}
			for _, resource := range state.Observation.Resources {
				if resource.Kind == "Pod" && (resource.ExitCode == nil || *resource.ExitCode != 0) {
					t.Fatal("natural pre-close training exit was changed", resource)
				}
			}
			if again, err := f.worker(t).ReconcileOnce(context.Background()); err != nil || again.Examined != 0 {
				t.Fatal("restart reissued original completed cleanup", again, err)
			}
		})
	}
}

// The exact condition shape came from Stop374's original raw: generation2,
// suspend=true, Completed=True before the close fence, without Suspended or an
// invented observedGeneration. Relative fixture clocks keep that same order.
func completedBeforeCloseControllers(t *testing.T, f *computeCleanupFixture, fence time.Time) (*unstructured.Unstructured, *unstructured.Unstructured) {
	t.Helper()
	var train, set *unstructured.Unstructured
	for _, object := range f.objects {
		if object.GetKind() != "TrainJob" && object.GetKind() != "JobSet" {
			continue
		}
		object.SetGeneration(2)
		_ = unstructured.SetNestedField(object.Object, true, "spec", "suspend")
		if object.GetKind() == "TrainJob" {
			train = object
			_ = unstructured.SetNestedSlice(object.Object, []any{map[string]any{"type": "Complete", "status": "True", "lastTransitionTime": fence.Add(-time.Second).Format(time.RFC3339Nano)}, map[string]any{"type": "Suspended", "status": "True", "lastTransitionTime": time.Now().UTC().Format(time.RFC3339Nano)}}, "status", "conditions")
		} else {
			set = object
			_ = unstructured.SetNestedSlice(object.Object, []any{map[string]any{"type": "Completed", "status": "True", "lastTransitionTime": fence.Add(-time.Second).Format(time.RFC3339Nano)}}, "status", "conditions")
		}
	}
	if train == nil || set == nil {
		t.Fatal("original compute controllers absent from fixture")
	}
	return train, set
}

func TestCompletedSetFallbackStillRequiresDurableExitAndLogs(t *testing.T) {
	for _, missing := range []string{"exit", "log"} {
		t.Run(missing, func(t *testing.T) {
			f := newComputeCleanupClosingFixture(t, false, "USER_STOP", completedSetSuspendBoundary)
			state, err := lifecycle.New(f.step.open()).GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			_, set := completedBeforeCloseControllers(t, f, state.CloseRequestedAt)
			_ = unstructured.SetNestedSlice(set.Object, []any{map[string]any{"type": "Completed", "status": "True"}}, "status", "conditions")
			if result, err := f.worker(t).ReconcileOnce(context.Background()); err != nil || result.Unresolved != 1 || len(f.deleted) != 0 {
				t.Fatal("fixture must commit logs/intent but retain original controllers", result, err)
			}
			record, err := execution.New(f.step.open()).GetOperationRecord(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			if record.Runtime.ComputeCleanup == nil || !record.Runtime.ComputeCleanup.LogsReady {
				t.Fatal("negative test requires already durable original log/target intent")
			}
			completedBeforeCloseControllers(t, f, record.Runtime.CloseRequestedAt)
			if missing == "log" {
				record.Runtime.ComputeCleanup.LogsReady = false
			} else {
				for i := range record.Runtime.Observation.Resources {
					if record.Runtime.Observation.Resources[i].Kind == "Pod" {
						record.Runtime.Observation.Resources[i].Terminal = false
						record.Runtime.Observation.Resources[i].ExitCode = nil
					}
				}
			}
			var target biz.RuntimeResource
			for _, candidate := range record.Runtime.ComputeCleanup.Targets {
				if candidate.Kind == "TrainJob" {
					target = candidate
				}
			}
			if err := cleanup.New(f.kube, f.step.proof).DeleteRecordedComputeController(context.Background(), record, target); !errors.Is(err, biz.ErrCleanupBlocked) || len(f.deleted) != 0 {
				t.Fatal("missing persisted exit/log permitted controller deletion", err)
			}
		})
	}
}

// Replace only the external controller reaction to the real adapter's exact
// original UID/RV suspend PATCH. Existing process exit0 is never rewritten to
// a synthetic killed exit, matching the original completed-before-Stop case.
func completedSetSuspendBoundary(f *computeCleanupFixture) {
	previous := f.step.serveExtra
	f.step.serveExtra = func(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodPatch || !strings.Contains(r.URL.Path, "/trainjobs/") {
			return previous(t, w, r)
		}
		var patch []map[string]any
		if json.NewDecoder(r.Body).Decode(&patch) != nil || len(patch) != 3 || patch[2]["path"] != "/spec/suspend" || patch[2]["value"] != true {
			t.Error("unexpected late Stop PATCH")
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		state, err := lifecycle.New(f.step.open()).GetRuntime(r.Context(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
		if err != nil || state.CloseReason != "USER_STOP" || state.CloseRequestedAt.IsZero() {
			t.Error("controller reaction preceded durable Stop")
			w.WriteHeader(http.StatusConflict)
			return true
		}
		f.mu.Lock()
		train := f.objects[r.URL.Path]
		if train == nil || patch[0]["value"] != string(train.GetUID()) || patch[1]["value"] != train.GetResourceVersion() {
			f.mu.Unlock()
			w.WriteHeader(http.StatusConflict)
			return true
		}
		completedBeforeCloseControllers(t, f, state.CloseRequestedAt)
		f.stopPatches++
		train.SetResourceVersion("late-stop-2")
		body := train.DeepCopy()
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
		return true
	}
}

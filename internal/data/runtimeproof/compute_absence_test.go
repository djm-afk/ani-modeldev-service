package runtimeproof_test

import (
	"context"
	"encoding/json"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
)

// Every receipt is committed through the real PG repository. The external
// Kubernetes fixture then represents independently completed exact-UID removal.
func failedFourthCleanup(t *testing.T) (*computeCleanupFixture, *biz.ExecutionOperations, biz.ExecutionRuntime) {
	t.Helper()
	f, _, operations := exhaustedCleanup(t)
	repository := lifecycle.New(f.step.open())
	state, err := repository.GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	logs, err := f.step.proof.CaptureComputeCleanupLogs(context.Background(), f.step.f.execution, f.step.f.authority(), state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repository.AdmitComputeCleanupRetry(context.Background(), f.step.f.authority(), state.CloseGeneration, "governance:operator-3", logs); err != nil {
		t.Fatal(err)
	}
	state, err = repository.StartComputeCleanupAttempt(context.Background(), f.step.f.authority(), state.CloseGeneration)
	if err != nil || state.ComputeCleanup.Attempts != 4 {
		t.Fatal("original fourth attempt", err)
	}
	state, err = repository.RecordComputeCleanupResult(context.Background(), f.step.f.authority(), state.CloseGeneration, 4, nil, "BOUNDARY_UNCERTAIN")
	if err != nil || state.ComputeCleanup.Phase != "NEEDS_REVIEW" {
		t.Fatal("original fourth failure", err)
	}
	return f, operations, state
}

func TestExhaustedComputeCleanupObservesOriginalAbsenceWithoutNewAttempt(t *testing.T) {
	f, operations, before := failedFourthCleanup(t)
	f.objects = nil
	f.workflowDeleted, f.runtimeDeleted = true, true
	result, err := operations.ReconcileWithActor(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID, "governance:operator-9")
	if err != nil {
		t.Fatalf("EXHAUSTED_ORIGINAL_ABSENCE_NOT_RECORDED: %v", err)
	}
	after, err := lifecycle.New(f.step.open()).GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
	if err != nil || after.ComputeCleanup.Phase != "APPLIED" || after.ComputeCleanup.Attempts != 4 || len(after.ComputeCleanup.ConfirmedAbsent) != 7 || len(f.deleted) != 0 || f.logReads != 1 {
		t.Fatal("pure original-scope observation did not complete the same attempt", result, after.ComputeCleanup, err, f.deleted, f.logReads)
	}
	if !reflect.DeepEqual(before.ComputeCleanup.OperatorRetry, after.ComputeCleanup.OperatorRetry) || !reflect.DeepEqual(before.ComputeCleanup.Logs, after.ComputeCleanup.Logs) || !reflect.DeepEqual(before.ComputeCleanup.Requested, after.ComputeCleanup.Requested) || before.CloseOperationID != after.CloseOperationID || before.CloseGeneration != after.CloseGeneration || after.ComputeCleanup.PlanHash != before.ComputeCleanup.PlanHash {
		t.Fatal("observation changed original deadline/actor/logs/scope")
	}
	if _, err := operations.ReconcileWithActor(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID, "governance:operator-9"); err != nil {
		t.Fatal("completed replay", err)
	}
	final, _ := lifecycle.New(f.step.open()).GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
	if !reflect.DeepEqual(after.ComputeCleanup, final.ComputeCleanup) || len(f.deleted) != 0 {
		t.Fatal("replay changed completed original receipt")
	}
}

func TestExhaustedComputeCleanupObservationRejectsUnprovenAbsence(t *testing.T) {
	for _, mode := range []string{"original still present", "replacement UID", "unknown read", "replacement PVC", "replacement namespace"} {
		t.Run(mode, func(t *testing.T) {
			f, operations, before := failedFourthCleanup(t)
			f.objects = nil
			f.workflowDeleted, f.runtimeDeleted = true, true
			original := f.step.serveExtra
			f.step.serveExtra = func(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
				if r.Method != http.MethodGet {
					t.Errorf("absence observation mutated Kubernetes: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(400)
					return true
				}
				var object *unstructured.Unstructured
				if strings.HasSuffix(r.URL.Path, "/workflows/"+f.step.f.workflow.GetName()) {
					switch mode {
					case "original still present":
						object = f.step.f.workflow.DeepCopy()
					case "replacement UID":
						object = f.step.f.workflow.DeepCopy()
						object.SetUID("same-name-replacement")
					case "unknown read":
						w.WriteHeader(503)
						_ = json.NewEncoder(w).Encode(metav1.Status{Status: "Failure", Reason: metav1.StatusReasonServiceUnavailable, Code: 503})
						return true
					}
				}
				if mode == "replacement PVC" && strings.HasSuffix(r.URL.Path, "/persistentvolumeclaims/"+f.step.workspace.PVCName) {
					object = &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": f.step.workspace.PVCName, "namespace": f.step.workspace.NamespaceName, "uid": "replacement-pvc"}}}
				}
				if mode == "replacement namespace" && r.URL.Path == "/api/v1/namespaces/"+f.step.workspace.NamespaceName {
					object = &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": f.step.workspace.NamespaceName, "uid": "replacement-namespace"}}}
				}
				if object != nil {
					_ = json.NewEncoder(w).Encode(object)
					return true
				}
				return original(t, w, r)
			}
			_, err := operations.ReconcileWithActor(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID, "governance:operator-9")
			after, readErr := lifecycle.New(f.step.open()).GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
			if err == nil || readErr != nil || !reflect.DeepEqual(before.ComputeCleanup, after.ComputeCleanup) || len(f.deleted) != 0 || f.logReads != 1 {
				t.Fatal("unproven absence changed the original receipt or consumed another attempt", mode, err, readErr, after.ComputeCleanup)
			}
		})
	}
}

func TestComputeCleanupAbsenceRequiresCompleteOriginalScopeAndLogs(t *testing.T) {
	f, _, before := failedFourthCleanup(t)
	f.objects = nil
	f.workflowDeleted, f.runtimeDeleted = true, true
	for _, mode := range []string{"expired original attempt", "missing logs", "changed hash", "changed requested UID"} {
		t.Run(mode, func(t *testing.T) {
			raw, _ := json.Marshal(before)
			var state biz.ExecutionRuntime
			if err := json.Unmarshal(raw, &state); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "expired original attempt":
				state.ComputeCleanup.OperatorRetry.DeadlineAt = time.Now().Add(-time.Minute)
			case "missing logs":
				state.ComputeCleanup.Logs = nil
			case "changed hash":
				state.ComputeCleanup.PlanHash = strings.Repeat("0", 64)
			case "changed requested UID":
				state.ComputeCleanup.Requested[0].UID = "replacement-requested-uid"
			}
			absent, err := f.step.proof.ObserveComputeCleanupAbsence(context.Background(), f.step.f.execution, f.step.f.authority(), state)
			if mode == "expired original attempt" {
				if err != nil || len(absent) != 7 {
					t.Fatal("expired attempt could not observe without renewal", err, absent)
				}
			} else if err == nil || len(absent) != 0 {
				t.Fatal("incomplete/drifted receipt accepted", mode, err)
			}
			after, _ := lifecycle.New(f.step.open()).GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
			if !reflect.DeepEqual(before.ComputeCleanup, after.ComputeCleanup) || len(f.deleted) != 0 {
				t.Fatal("boundary observation changed durable receipt or deleted")
			}
		})
	}
}

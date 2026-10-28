package runtimeproof_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The external Kubernetes boundary models a controller's pending deletion:
// suspend terminates the worker, but GC cannot erase its logs until ModelDev
// removes the original Pod's exit-evidence finalizer.
func collectStoppedPodAfterRetentionRelease(f *computeCleanupFixture) {
	f.refreshTrainingBeforeClose = true
	prior := f.step.serveExtra
	podPath := "/api/v1/namespaces/" + f.step.workspace.NamespaceName + "/pods/actual-training"
	f.step.serveExtra = func(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && r.URL.Path == podPath+"/log" {
			f.mu.Lock()
			present := f.objects[podPath] != nil
			f.mu.Unlock()
			if !present {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound})
				return true
			}
		}
		handled := prior(t, w, r)
		if !handled || r.Method != http.MethodPatch {
			return handled
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		pod := f.objects[podPath]
		if pod == nil {
			return handled
		}
		if r.URL.Path != podPath {
			at := metav1.Now()
			pod.SetDeletionTimestamp(&at)
		} else if pod.GetDeletionTimestamp() != nil && len(pod.GetFinalizers()) == 0 {
			delete(f.objects, podPath)
			f.step.mu.Lock()
			delete(f.step.pods, pod.GetName())
			f.step.mu.Unlock()
		}
		return handled
	}
}

func TestStoppedTrainingLogsSurviveOriginalPodGarbageCollection(t *testing.T) {
	f := newComputeCleanupClosingFixture(t, false, "USER_STOP", collectStoppedPodAfterRetentionRelease)
	result, err := f.worker(t).ReconcileOnce(context.Background())
	if err != nil || result.Unresolved != 0 {
		t.Fatalf("original Stop did not close and preserve its worker evidence: %+v %v", result, err)
	}
	pool := f.step.open()
	defer pool.Close()
	query := service.NewQuery(execution.New(pool), nil)
	ctx := service.WithVerifiedGovernanceQuery(context.Background(), service.GovernanceDelivery{TenantID: f.step.f.execution.TenantID, RequestID: "stopped-original-pod-log-read"}, modeldevv1.ModelDevQueryService_GetExecutionLogs_FullMethodName)
	state, err := lifecycle.New(pool).GetRuntime(ctx, f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
	if err != nil {
		t.Fatal("reconnected original worker facts", err)
	}
	source, err := biz.TrainingLogSourceFor(biz.QueryRecord{Execution: f.step.f.execution, Runtime: state}, "")
	if err != nil {
		t.Fatal("original persisted worker identity", err)
	}
	logs, err := query.GetExecutionLogs(ctx, &modeldevv1.GetExecutionLogsRequest{ExecutionId: f.step.f.execution.ExecutionID, PodUid: &source.PodUID, LogId: &source.LogID})
	if err != nil || len(logs.GetLines()) != 1 || logs.GetLines()[0].GetText() != "actual retained training output" || logs.GetSource().GetResourceUid() != "training-pod-original" {
		t.Fatalf("STOP_LOG_RETENTION_LOST: original worker logs must remain queryable after normal Stop/GC: %v %v", logs, err)
	}
}

func TestRetainedTrainingLogRejectsMutationBeforeCleanup(t *testing.T) {
	f := newComputeCleanupClosingFixture(t, false, "USER_STOP")
	pool := f.step.open()
	defer pool.Close()
	repository := lifecycle.New(pool)
	state, err := repository.GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(state.Observation)
	if err != nil {
		t.Fatal(err)
	}
	for _, attack := range []string{"erase", "foreign-source", "changed-output", "new-pod-claims-original-tail"} {
		t.Run(attack, func(t *testing.T) {
			var observation biz.TrainingRuntimeObservation
			if err := json.Unmarshal(encoded, &observation); err != nil {
				t.Fatal(err)
			}
			observation.ObservedAt = time.Now().UTC()
			found := false
			for i := range observation.Resources {
				pod := &observation.Resources[i]
				if pod.Kind != "Pod" || pod.TrainingLog == nil {
					continue
				}
				found = true
				switch attack {
				case "erase":
					pod.TrainingLog = nil
				case "foreign-source":
					pod.TrainingLog.Source.PodUID = "foreign-training-pod"
				case "changed-output":
					pod.TrainingLog.Result.Lines[0].Text = "replacement output"
				case "new-pod-claims-original-tail":
					copy := *pod
					copy.UID, copy.Name = "new-training-pod", "new-training-pod"
					observation.Resources = append(observation.Resources, copy)
				}
				break
			}
			if !found {
				t.Fatal("fixture has no committed original worker log")
			}
			if _, err := repository.RecordTrainingObservation(context.Background(), f.step.f.authority(), observation); err == nil {
				t.Fatal("replacement or foreign worker log was accepted as original evidence")
			}
		})
	}
}

package runtimeproof_test

import (
	"context"
	"net/http"
	"testing"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
)

func TestRunningTrainingTailPersistsThroughStopKubeletLossAndGC(t *testing.T) {
	f := newComputeCleanupClosingFixture(t, false, "USER_STOP", func(f *computeCleanupFixture) {
		f.captureRunningBeforeClose = true
		collectStoppedPodAfterRetentionRelease(f)
		prior := f.step.serveExtra
		podPath := "/api/v1/namespaces/" + f.step.workspace.NamespaceName + "/pods/actual-training"
		f.step.serveExtra = func(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
			if r.Method == http.MethodGet && r.URL.Path == podPath+"/log" {
				f.mu.Lock()
				pod := f.objects[podPath]
				lost := pod != nil && pod.GetDeletionTimestamp() != nil
				f.mu.Unlock()
				if lost {
					_, _ = w.Write([]byte("unable to retrieve container logs for containerd://original-worker"))
					return true
				}
			}
			return prior(t, w, r)
		}
	})
	result, err := f.worker(t).ReconcileOnce(context.Background())
	if err != nil || result.Unresolved != 0 {
		t.Fatalf("actual running log/exit/cleanup did not close: %+v %v", result, err)
	}
	pool := f.step.open()
	defer pool.Close()
	state, err := lifecycle.New(pool).GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
	if err != nil || state.ClosedAt == nil || state.ComputeCleanup == nil || !state.ComputeCleanup.LogsReady || len(state.ComputeCleanup.Logs) != 1 {
		t.Fatalf("original close/archive was not durable: %+v %v", state, err)
	}
	if !state.ComputeCleanup.Logs[0].Result.Truncated {
		t.Fatal("unavailable final refresh was silently presented as complete logs")
	}
	query := service.NewQuery(execution.New(pool), nil)
	ctx := service.WithVerifiedGovernanceQuery(context.Background(), service.GovernanceDelivery{TenantID: f.step.f.execution.TenantID, RequestID: "running-original-tail-after-stop"}, modeldevv1.ModelDevQueryService_GetExecutionLogs_FullMethodName)
	logs, err := query.GetExecutionLogs(ctx, &modeldevv1.GetExecutionLogsRequest{ExecutionId: f.step.f.execution.ExecutionID})
	if err != nil || len(logs.GetLines()) != 1 || logs.GetLines()[0].GetText() != "actual retained training output" || logs.GetSource().GetResourceUid() != "training-pod-original" || !logs.GetTruncated() {
		t.Fatalf("original running tail missing after real PG/Stop/GC: %+v %v", logs, err)
	}
}

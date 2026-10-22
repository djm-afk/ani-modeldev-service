package cleanup_test

import (
	"context"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/cleanup"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// These adapter-only compatibility cases consume the same typed durable
// receipt whose transaction and restart behavior are tested with real PG.
func TestCleanupResolvedAfterStepReclaimKeepsTrainingLogsAndChecksCurrentWriters(t *testing.T) {
	for _, attack := range []string{"none", "new-pvc-writer", "replaced-step-pod", "missing-training-log-pod"} {
		t.Run(attack, func(t *testing.T) {
			f := newCleanupResolutionFixture(t)
			proof, err := f.proof.VerifyOwnerWritersAbsent(context.Background(), f.record.Execution, *f.record.Authority, f.record.Runtime.Workspace)
			if err != nil {
				t.Fatal(err)
			}
			completed := time.Now().UTC()
			removed := append([]biz.RuntimeResource{}, proof.Resources...)
			resource := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
			for i := range removed {
				removed[i].APIObjectPresent = false
				if err := f.kube.Tracker().Delete(resource, f.namespace(), removed[i].Name); err != nil {
					t.Fatal(err)
				}
			}
			f.record.Runtime.StepExitCleanup = &biz.StepExitCleanupReceipt{CloseGeneration: f.record.Runtime.CloseGeneration, Evidence: proof, StartedAt: completed.Add(-time.Second), CompletedAt: &completed, RemovedResources: removed}
			switch attack {
			case "new-pvc-writer":
				pod := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "new-writer", "namespace": f.namespace(), "uid": "new-writer-uid"}, "spec": map[string]any{"volumes": []any{map[string]any{"name": "workspace", "persistentVolumeClaim": map[string]any{"claimName": f.record.Runtime.Workspace.PVCName}}}}, "status": map[string]any{"phase": "Running"}}}
				if err := f.kube.Tracker().Add(pod); err != nil {
					t.Fatal(err)
				}
			case "replaced-step-pod":
				pod := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": proof.Resources[0].Name, "namespace": f.namespace(), "uid": "replacement-step-uid"}, "status": map[string]any{"phase": "Succeeded"}}}
				if err := f.kube.Tracker().Add(pod); err != nil {
					t.Fatal(err)
				}
			case "missing-training-log-pod":
				if err := f.kube.Tracker().Delete(resource, f.namespace(), "original-training-pod"); err != nil {
					t.Fatal(err)
				}
			}
			confirmed, err := cleanup.New(f.kube, f.proof).VerifyCleanupResolved(context.Background(), f.record)
			if attack == "none" {
				if err != nil || len(confirmed) != 3 {
					t.Fatal("safe step reclamation invalidated original audited controller cleanup", err)
				}
				if _, err := f.kube.Tracker().Get(resource, f.namespace(), "original-training-pod"); err != nil {
					t.Fatal("training logs Pod disappeared", err)
				}
			} else if err == nil || len(confirmed) != 0 {
				t.Fatal("durable receipt bypassed current safety check", attack, err)
			}
			f.assertReadOnly(t)
		})
	}
}

package runtimeproof_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The cleaner, original writer proof and all durable receipts are real. Only
// the Kubernetes/KFP transports supply the independent VM's live objects.
func TestAutomaticComputeCleanupPreservesIndependentVMAndPVCInSameNamespace(t *testing.T) {
	f := newComputeCleanupFixture(t, false)
	ns := f.step.workspace.NamespaceName
	vm := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kubevirt.io/v1", "kind": "VirtualMachine",
		"metadata": map[string]any{"name": "independent-vm", "namespace": ns, "uid": "18181818-1111-4111-8111-181818181818", "resourceVersion": "23"},
		"spec":     map[string]any{"running": true},
		"status":   map[string]any{"ready": true, "printableStatus": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
	}}
	pvc := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim",
		"metadata": map[string]any{"name": "independent-vm-disk", "namespace": ns, "uid": "29292929-2222-4222-8222-292929292929", "resourceVersion": "31"},
		"spec":     map[string]any{"accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": "1Gi"}}},
		"status":   map[string]any{"phase": "Bound"},
	}}
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "independent-vm-launcher", "namespace": ns, "uid": "3a3a3a3a-3333-4333-8333-3a3a3a3a3a3a", "resourceVersion": "42", "ownerReferences": []any{map[string]any{"apiVersion": "kubevirt.io/v1", "kind": "VirtualMachine", "name": vm.GetName(), "uid": string(vm.GetUID()), "controller": true}}},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "compute"}}, "volumes": []any{map[string]any{"name": "disk", "persistentVolumeClaim": map[string]any{"claimName": pvc.GetName()}}}},
		"status":   map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
	}}
	f.step.pods[pod.GetName()] = pod
	type preservedObject struct {
		resource schema.GroupVersionResource
		object   *unstructured.Unstructured
	}
	preserved := []preservedObject{
		{schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}, vm},
		{schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}, pvc},
		{schema.GroupVersionResource{Version: "v1", Resource: "pods"}, pod},
	}
	paths := map[string]*unstructured.Unstructured{
		"/apis/kubevirt.io/v1/namespaces/" + ns + "/virtualmachines/" + vm.GetName(): vm,
		"/api/v1/namespaces/" + ns + "/persistentvolumeclaims/" + pvc.GetName():      pvc,
		"/api/v1/namespaces/" + ns + "/pods/" + pod.GetName():                        pod,
	}
	var foreignMutations atomic.Int32
	originalBoundary := f.step.serveExtra
	f.step.serveExtra = func(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
		if object := paths[r.URL.Path]; object != nil {
			if r.Method != http.MethodGet {
				foreignMutations.Add(1)
				t.Errorf("cleaner attempted to mutate independent %s UID %s", object.GetKind(), object.GetUID())
				w.WriteHeader(http.StatusForbidden)
				return true
			}
			_ = json.NewEncoder(w).Encode(object)
			return true
		}
		return originalBoundary(t, w, r)
	}
	ctx := context.Background()
	before := make([]*unstructured.Unstructured, len(preserved))
	for i, item := range preserved {
		var err error
		before[i], err = f.kube.Resource(item.resource).Namespace(ns).Get(ctx, item.object.GetName(), metav1.GetOptions{})
		if err != nil {
			t.Fatal("independent object before cleanup", err)
		}
	}
	result, err := f.worker(t).ReconcileOnce(ctx)
	if err != nil || result.Closed != 1 || result.Unresolved != 0 {
		t.Fatal("real original cleanup failed", result, err)
	}
	state, err := lifecycle.New(f.step.open()).GetRuntime(ctx, f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
	if err != nil || state.ComputeCleanup == nil || state.ComputeCleanup.Phase != "APPLIED" || len(state.ComputeCleanup.ConfirmedAbsent) != 7 {
		t.Fatal("original deletions were not durably confirmed", err)
	}
	f.mu.Lock()
	deleted, remaining := append([]string{}, f.deleted...), len(f.objects)
	f.mu.Unlock()
	if remaining != 0 || !f.workflowDeleted || !f.runtimeDeleted || !reflect.DeepEqual(deleted, []string{"TrainJob", "JobSet", "Job", "Pod", "PodGroup", "Workflow", "TrainingRuntime"}) {
		t.Fatal("cleanup did not remove exactly the original ModelDev graph", deleted, remaining)
	}
	for i, item := range preserved {
		after, err := f.kube.Resource(item.resource).Namespace(ns).Get(ctx, item.object.GetName(), metav1.GetOptions{})
		if err != nil || !reflect.DeepEqual(before[i].Object, after.Object) {
			t.Fatal("independent object UID, health or content changed", item.object.GetKind(), err)
		}
		t.Logf("CL03 preserved kind=%s name=%s namespace=%s uid=%s health=%v", after.GetKind(), after.GetName(), after.GetNamespace(), after.GetUID(), after.Object["status"])
	}
	if foreignMutations.Load() != 0 {
		t.Fatal("independent object mutation attempted")
	}
	t.Log("CL03 real PostgreSQL APPLIED: exactly 7 original compute targets removed; independent running VM/launcher and Bound PVC original UIDs/content unchanged")
}

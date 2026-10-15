//go:build finetuning_core_flow

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestCoreFlowTrainingTriggerRequiresOriginalRunningNode(t *testing.T) {
	now := time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name              string
		change            func(*unstructured.Unstructured)
		running, conflict bool
		startsDuringRead  bool
	}{
		{name: "pending-container-creating", change: func(p *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(p.Object, "Pending", "status", "phase")
			_ = unstructured.SetNestedSlice(p.Object, []any{map[string]any{"name": "node", "state": map[string]any{"waiting": map[string]any{"reason": "ContainerCreating"}}}}, "status", "containerStatuses")
		}},
		{name: "original-running-container", running: true},
		{name: "node-starts-and-deadline-passes-during-pod-get", running: true, startsDuringRead: true},
		{name: "same-name-new-pod-uid", change: func(p *unstructured.Unstructured) { p.SetUID(types.UID("replacement-pod")) }, conflict: true},
		{name: "different-controller-uid", change: func(p *unstructured.Unstructured) {
			owners := p.GetOwnerReferences()
			owners[0].UID = types.UID("replacement-job")
			p.SetOwnerReferences(owners)
		}, conflict: true},
		{name: "original-pod-not-readable-yet", change: func(p *unstructured.Unstructured) { p.SetName("not-the-original-pod-name") }},
		{name: "running-without-container-id", change: func(p *unstructured.Unstructured) {
			rows, _, _ := unstructured.NestedSlice(p.Object, "status", "containerStatuses")
			delete(rows[0].(map[string]any), "containerID")
			_ = unstructured.SetNestedSlice(p.Object, rows, "status", "containerStatuses")
		}},
		{name: "running-without-started-at", change: func(p *unstructured.Unstructured) {
			rows, _, _ := unstructured.NestedSlice(p.Object, "status", "containerStatuses")
			rows[0].(map[string]any)["state"] = map[string]any{"running": map[string]any{}}
			_ = unstructured.SetNestedSlice(p.Object, rows, "status", "containerStatuses")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record, objects := coreFlowWriterFixture(t, now)
			if tc.change != nil {
				tc.change(objects[len(objects)-1].(*unstructured.Unstructured))
			}
			observedAt, startedAt := now, now.Add(-time.Second)
			if tc.startsDuringRead {
				observedAt, startedAt = now.Add(3*time.Second), now.Add(2*time.Second)
				pod := objects[len(objects)-1].(*unstructured.Unstructured)
				rows, _, _ := unstructured.NestedSlice(pod.Object, "status", "containerStatuses")
				rows[0].(map[string]any)["state"] = map[string]any{"running": map[string]any{"startedAt": startedAt.Format(time.RFC3339Nano)}}
				_ = unstructured.SetNestedSlice(pod.Object, rows, "status", "containerStatuses")
			}
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
			podRead := false
			client.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) { podRead = true; return false, nil, nil })
			proof, running, err := coreFlowRunningTrainingWriter(context.Background(), client, record, func() time.Time {
				if !podRead {
					t.Fatal("writer timestamp taken before the actual Pod GET")
				}
				return observedAt
			})
			if running != tc.running || errors.Is(err, biz.ErrRuntimeConflict) != tc.conflict || (!tc.conflict && err != nil) {
				t.Fatalf("wrong training trigger: running=%v err=%v", running, err)
			}
			if running && (proof == nil || proof.Namespace != "original-namespace" || proof.NamespaceUID != "original-namespace-uid" || proof.TrainJobUID != "original-train" || proof.JobSetUID != "original-set" || proof.JobUID != "original-job" || proof.PodUID != "original-pod" || proof.ContainerName != "node" || proof.ContainerID != "containerd://original-node" || !proof.StartedAt.Equal(startedAt) || !proof.ObservedAt.Equal(observedAt)) {
				t.Fatal("trigger lacks original process identity and actual start")
			}
			if tc.startsDuringRead && proof.ObservedAt.Before(now.Add(time.Second)) {
				t.Fatal("deadline crossed during GET was backdated")
			}
			for _, action := range client.Actions() {
				if action.GetVerb() != "get" {
					t.Fatal("writer check performed a non-GET action", action.GetVerb())
				}
			}
		})
	}
}

func coreFlowWriterFixture(t *testing.T, now time.Time) (biz.QueryRecord, []runtime.Object) {
	t.Helper()
	const ns = "original-namespace"
	controller := true
	owner := func(api, kind, name, uid string) []metav1.OwnerReference {
		return []metav1.OwnerReference{{APIVersion: api, Kind: kind, Name: name, UID: types.UID(uid), Controller: &controller}}
	}
	train := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "trainer.kubeflow.org/v1alpha1", "kind": "TrainJob", "metadata": map[string]any{"name": "md-original", "namespace": ns, "uid": "original-train", "annotations": map[string]any{"modeldev.ani.io/tenant-id": "tenant", "modeldev.ani.io/execution-id": "execution", "modeldev.ani.io/execution-spec-sha256": "spec"}}}}
	set := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "jobset.x-k8s.io/v1alpha2", "kind": "JobSet", "metadata": map[string]any{"name": "md-original", "namespace": ns, "uid": "original-set"}}}
	set.SetOwnerReferences(owner(train.GetAPIVersion(), train.GetKind(), train.GetName(), string(train.GetUID())))
	job := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": "md-original-node-0", "namespace": ns, "uid": "original-job"}}}
	job.SetOwnerReferences(owner(set.GetAPIVersion(), set.GetKind(), set.GetName(), string(set.GetUID())))
	pod := &corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: "md-original-node-0-abc", Namespace: ns, UID: types.UID("original-pod"), OwnerReferences: owner(job.GetAPIVersion(), job.GetKind(), job.GetName(), string(job.GetUID()))}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "node"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "node", ContainerID: "containerd://original-node", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-time.Second))}}}}}}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
	if err != nil {
		t.Fatal(err)
	}
	upod := &unstructured.Unstructured{Object: raw}
	var record biz.QueryRecord
	record.Execution.TenantID, record.Execution.ExecutionID = "tenant", "execution"
	record.Execution.Snapshot.Environment.NamespaceName, record.Execution.Snapshot.Environment.NamespaceUID = ns, "original-namespace-uid"
	handle := biz.TrainingHandle{NamespaceUID: "original-namespace-uid", TrainJobUID: "original-train", PVCUID: "original-pvc"}
	record.Runtime.Training = &biz.TrainingPlan{TenantID: "tenant", ExecutionID: "execution", SpecHash: "spec", Name: train.GetName()}
	record.Runtime.TrainingHandle = &handle
	record.Runtime.Observation = &biz.TrainingRuntimeObservation{Handle: handle, Outcome: "RUNNING", ObservedAt: now}
	previous := ""
	for _, obj := range []*unstructured.Unstructured{train, set, job, upod} {
		record.Runtime.Observation.Resources = append(record.Runtime.Observation.Resources, biz.RuntimeResource{APIVersion: obj.GetAPIVersion(), Kind: obj.GetKind(), Namespace: ns, Name: obj.GetName(), UID: string(obj.GetUID()), OwnerUID: previous, APIObjectPresent: true})
		previous = string(obj.GetUID())
	}
	namespace := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": ns, "uid": "original-namespace-uid"}}}
	return record, []runtime.Object{namespace, train, set, job, upod}
}

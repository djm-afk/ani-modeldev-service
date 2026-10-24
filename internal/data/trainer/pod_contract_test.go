package trainer_test

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestTrainingObservationRetainsResourceDriftForOriginalUIDStop(t *testing.T) {
	for _, mode := range []string{"pod-limit", "pod-sidecar", "pod-image", "pod-security", "group-vector", "group-queue"} {
		t.Run(mode, func(t *testing.T) {
			f := newTrainingFixture(t)
			adapter := f.adapter(t)
			handle, err := adapter.CreateTraining(context.Background(), f.plan)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "pod-limit":
				containers, _, _ := unstructured.NestedSlice(f.pod, "spec", "containers")
				_ = unstructured.SetNestedField(containers[0].(map[string]any), "3Gi", "resources", "limits", "memory")
				_ = unstructured.SetNestedSlice(f.pod, containers, "spec", "containers")
			case "pod-sidecar":
				containers, _, _ := unstructured.NestedSlice(f.pod, "spec", "containers")
				_ = unstructured.SetNestedSlice(f.pod, append(containers, map[string]any{"name": "unknown"}), "spec", "containers")
			case "pod-image":
				containers, _, _ := unstructured.NestedSlice(f.pod, "spec", "containers")
				containers[0].(map[string]any)["image"] = "foreign/image:latest"
				_ = unstructured.SetNestedSlice(f.pod, containers, "spec", "containers")
			case "pod-security":
				_ = unstructured.SetNestedField(f.pod, true, "spec", "hostPID")
			case "group-vector":
				_ = unstructured.SetNestedField(f.podGroup, "2", "spec", "minResources", "cpu")
			case "group-queue":
				_ = unstructured.SetNestedField(f.podGroup, "foreign", "spec", "queue")
			}
			observed, err := adapter.ObserveTraining(context.Background(), f.plan, handle, nil)
			if err != nil || !observed.SpecificationMismatch || observed.WritersAbsent {
				t.Fatal("effective drift lost original graph", observed, err)
			}
			if err := adapter.StopTraining(context.Background(), f.plan, handle); err != nil || f.patches != 1 {
				t.Fatal("drift prevented original UID suspend", err)
			}
		})
	}
}

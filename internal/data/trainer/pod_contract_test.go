package trainer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestTrainingObservationRetainsResourceDriftForOriginalUIDStop(t *testing.T) {
	for _, mode := range []string{"pod-limit", "pod-sidecar", "pod-image", "pod-security", "pod-cache-env", "pod-extra-env", "group-vector", "group-queue"} {
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
			case "pod-cache-env", "pod-extra-env":
				containers, _, _ := unstructured.NestedSlice(f.pod, "spec", "containers")
				node := containers[0].(map[string]any)
				environment, _, _ := unstructured.NestedSlice(node, "env")
				if mode == "pod-cache-env" {
					environment[0].(map[string]any)["value"] = "/tmp"
				} else {
					environment = append(environment, map[string]any{"name": "PYTHONPATH", "value": "/foreign"})
				}
				_ = unstructured.SetNestedSlice(node, environment, "env")
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

// Current executions always freeze Volcano policy. A missing group after a
// current freeze remains drift; a retired policy is rejected at decode.
func TestPodGroupFollowsTheFrozenRuntimePlanRatherThanTheDeployment(t *testing.T) {
	t.Run("current plan without its PodGroup is a mismatch", func(t *testing.T) {
		f := newTrainingFixture(t)
		f.podGroup = nil
		adapter := f.adapter(t)
		handle, err := adapter.CreateTraining(context.Background(), f.plan)
		if err != nil {
			t.Fatal(err)
		}
		observed, err := adapter.ObserveTraining(context.Background(), f.plan, handle, nil)
		if err != nil || !observed.SpecificationMismatch {
			t.Fatalf("a plan that schedules a PodGroup must notice its absence: %+v / %v", observed, err)
		}
	})
	t.Run("missing current policy is rejected", func(t *testing.T) {
		f := newTrainingFixture(t)
		plan := f.plan.Snapshot.RuntimePlan
		var spec map[string]any
		if err := json.Unmarshal(plan.ExpectedSpec, &spec); err != nil {
			t.Fatal(err)
		}
		delete(spec, "podGroupPolicy")
		raw, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		plan.ExpectedSpec = raw
		digest := sha256.Sum256(raw)
		plan.ExpectedSpecSHA256 = hex.EncodeToString(digest[:])
		if plan.Validate() == nil {
			t.Fatal("retired plan passed current contract decoder")
		}

	})
}

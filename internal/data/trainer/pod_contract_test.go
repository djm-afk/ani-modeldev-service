package trainer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
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

// An execution frozen with the current contract carries podGroupPolicy.volcano,
// so a missing PodGroup stays a specification mismatch. An execution frozen by
// an older deployment has no such policy; its absent PodGroup is not queried
// and is never reported as a mismatch.
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
	t.Run("legacy plan without a PodGroup is complete", func(t *testing.T) {
		f := newTrainingFixture(t)
		f.podGroup = nil
		adapter := f.adapter(t)
		handle, err := adapter.CreateTraining(context.Background(), f.plan)
		if err != nil {
			t.Fatal(err)
		}
		// Re-freeze the same original request as an older deployment did: no
		// podGroupPolicy, and therefore no PodGroup annotation binding.
		f.plan = withoutPodGroupPolicy(t, f.plan)
		_ = unstructured.SetNestedField(f.train, f.plan.SpecHash, "metadata", "annotations", "modeldev.ani.io/execution-spec-sha256")
		_ = unstructured.SetNestedField(f.train, f.plan.RequestSHA256, "metadata", "annotations", "modeldev.ani.io/training-request-sha256")
		f.setTerminal(t, 0)
		observed, err := adapter.ObserveTraining(context.Background(), f.plan, handle, nil)
		if err != nil || observed.SpecificationMismatch {
			t.Fatalf("a legacy plan without PodGroup must not require one: %+v / %v", observed, err)
		}
		if observed.Outcome != "SUCCEEDED" {
			t.Fatalf("legacy plan observation lost its outcome: %+v", observed)
		}
		if f.podGroupGets.Load() != 0 {
			t.Fatalf("legacy plan queried %d PodGroups", f.podGroupGets.Load())
		}
	})
}

// withoutPodGroupPolicy rebuilds the frozen plan as an older deployment froze
// it: the same executable spec without podGroupPolicy. Snapshot digest and the
// request digest are re-derived so the plan stays internally consistent.
func withoutPodGroupPolicy(t *testing.T, plan biz.TrainingPlan) biz.TrainingPlan {
	t.Helper()
	var spec map[string]any
	if err := json.Unmarshal(plan.Snapshot.RuntimePlan.ExpectedSpec, &spec); err != nil {
		t.Fatal(err)
	}
	delete(spec, "podGroupPolicy")
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	plan.Snapshot.RuntimePlan.ExpectedSpec = raw
	digest := sha256.Sum256(raw)
	plan.Snapshot.RuntimePlan.ExpectedSpecSHA256 = hex.EncodeToString(digest[:])
	plan.RuntimeBinding.ObservedSpecSHA256 = plan.Snapshot.RuntimePlan.ExpectedSpecSHA256
	if plan.SpecHash, err = plan.Snapshot.Digest(); err != nil {
		t.Fatal(err)
	}
	plan.RequestSHA256 = ""
	request, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(request)
	plan.RequestSHA256 = hex.EncodeToString(sum[:])
	return plan
}

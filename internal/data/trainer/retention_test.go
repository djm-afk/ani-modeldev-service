package trainer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const exitEvidenceFinalizer = "modeldev.ani.io/training-exit-evidence"

func TestTrainingRequiresExitRetentionRuntimeBeforeNewCreate(t *testing.T) {
	f := newTrainingFixture(t)
	delete(runtimePodTemplate(f), "metadata")
	freezeRuntime(f)
	_, err := f.adapter(t).CreateTraining(context.Background(), f.plan)
	if !errors.Is(err, biz.ErrRuntimeConflict) || f.posts != 0 {
		t.Fatalf("new creation without Runtime exit retention reached the controller: posts=%d err=%v", f.posts, err)
	}
}

func runtimePodTemplate(f *trainingFixture) map[string]any {
	jobs := f.runtime["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["replicatedJobs"].([]any)
	return jobs[0].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["template"].(map[string]any)
}

func freezeRuntime(f *trainingFixture) {
	encoded, _ := json.Marshal(f.runtime["spec"])
	digest := sha256.Sum256(encoded)
	f.plan.Snapshot.RuntimePlan.ExpectedSpecSHA256 = hex.EncodeToString(digest[:])
}

func TestTrainingRetainsStoppedPodUntilExitEvidenceIsDurable(t *testing.T) {
	f := newTrainingFixture(t)
	metadata := f.pod["metadata"].(map[string]any)
	metadata["resourceVersion"] = "1"
	metadata["finalizers"] = []any{"batch.kubernetes.io/job-tracking", exitEvidenceFinalizer}
	a, ctx := f.adapter(t), context.Background()
	handle, err := a.CreateTraining(ctx, f.plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.StopTraining(ctx, f.plan, handle); err != nil {
		t.Fatal(err)
	}
	f.setTerminal(t, 143)
	for _, object := range []map[string]any{f.train, f.jobset, f.job} {
		object["status"] = map[string]any{"conditions": []any{}}
		_ = unstructured.SetNestedField(object, true, "spec", "suspend")
	}
	observed, err := a.ObserveTraining(ctx, f.plan, handle, nil)
	if err != nil {
		t.Fatal(err)
	}
	if observed.WritersAbsent || f.podPatches != 0 {
		t.Fatalf("exit must be retained for persistence before closure: absent=%v patches=%d", observed.WritersAbsent, f.podPatches)
	}
	var exitFact biz.RuntimeResource
	for _, fact := range observed.Resources {
		if fact.UID == childPodUID {
			exitFact = fact
		}
	}
	if !exitFact.Terminal || exitFact.ExitCode == nil || *exitFact.ExitCode != 143 || !exitFact.APIObjectPresent || exitFact.OwnerUID != childJobUID {
		t.Fatalf("real owned Pod exit was not offered for durable persistence: %+v", exitFact)
	}
	confirmed, err := a.ObserveTraining(ctx, f.plan, handle, observed.Resources)
	if err != nil || !confirmed.WritersAbsent || f.podPatches != 1 {
		t.Fatalf("durably recorded exit did not release the stopped writer: %+v patches=%d err=%v", confirmed, f.podPatches, err)
	}
	finalizers, _, _ := unstructured.NestedStringSlice(f.pod, "metadata", "finalizers")
	if !reflect.DeepEqual(finalizers, []string{"batch.kubernetes.io/job-tracking"}) || f.deletes != 0 {
		t.Fatalf("release changed a controller finalizer or deleted the Pod: %v deletes=%d", finalizers, f.deletes)
	}
	f.pod = nil
	collected, err := a.ObserveTraining(ctx, f.plan, handle, confirmed.Resources)
	if err != nil || !collected.WritersAbsent {
		t.Fatalf("garbage collection erased the exact durably retained exit: %+v err=%v", collected, err)
	}
}

func TestTrainingReleasesDurableNaturalExitBeforeControllerCompletion(t *testing.T) {
	f := newTrainingFixture(t)
	f.pod["metadata"].(map[string]any)["resourceVersion"] = "1"
	f.pod["metadata"].(map[string]any)["finalizers"] = []any{exitEvidenceFinalizer}
	a, ctx := f.adapter(t), context.Background()
	handle, err := a.CreateTraining(ctx, f.plan)
	if err != nil {
		t.Fatal(err)
	}
	f.setTerminal(t, 0)
	for _, object := range []map[string]any{f.train, f.jobset, f.job} {
		object["status"] = map[string]any{"conditions": []any{}}
	}
	observed, err := a.ObserveTraining(ctx, f.plan, handle, nil)
	if err != nil || observed.WritersAbsent || f.podPatches != 0 {
		t.Fatalf("natural exit escaped retention before durable observation: %+v err=%v", observed, err)
	}
	confirmed, err := a.ObserveTraining(ctx, f.plan, handle, observed.Resources)
	if err != nil || confirmed.WritersAbsent || f.podPatches != 1 {
		t.Fatalf("parent completion must not hold a durably proved natural Pod exit: %+v patches=%d err=%v", confirmed, f.podPatches, err)
	}
	if finalizers, _, _ := unstructured.NestedStringSlice(f.pod, "metadata", "finalizers"); len(finalizers) != 0 {
		t.Fatalf("own finalizer leaked on natural completion: %v", finalizers)
	}
	f.setTerminal(t, 0)
	completed, err := a.ObserveTraining(ctx, f.plan, handle, confirmed.Resources)
	if err != nil || !completed.WritersAbsent || completed.Outcome != "SUCCEEDED" {
		t.Fatalf("actual parent completion did not confirm natural success: %+v err=%v", completed, err)
	}
}

func TestTrainingRuntimeDriftBlocksLookupButOriginalStopAndObservationStillWork(t *testing.T) {
	f := newTrainingFixture(t)
	a, ctx := f.adapter(t), context.Background()
	handle, err := a.CreateTraining(ctx, f.plan)
	if err != nil {
		t.Fatal(err)
	}
	// A current template edit cannot transfer or hide the original computation.
	delete(runtimePodTemplate(f), "metadata")
	if _, err := a.FindTraining(ctx, f.plan); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("drifted Runtime allowed normal lookup", err)
	}
	if err := a.StopTraining(ctx, f.plan, handle); err != nil {
		t.Fatalf("upgrade blocked normal Stop of a legacy Run: %v", err)
	}
	f.setTerminal(t, 0)
	observed, err := a.ObserveTraining(ctx, f.plan, handle, nil)
	if err != nil || observed.Outcome != "SUCCEEDED" || !observed.WritersAbsent || f.podPatches != 0 || f.posts != 1 {
		t.Fatalf("legacy exit observation lost its frozen contract: %+v err=%v", observed, err)
	}
	_ = unstructured.SetNestedField(f.runtime, "changed", "spec", "unexpected")
	if _, err := a.FindTraining(ctx, f.plan); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatalf("legacy compatibility bypassed Runtime digest validation: %v", err)
	}
}

func TestTrainingDoesNotReleaseExitWhenConditionalPodPatchIsRejected(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*trainingFixture)
	}{
		{"permission denied", func(f *trainingFixture) { f.podPatchStatusCode = http.StatusForbidden }},
		{"recreated Pod UID", func(f *trainingFixture) {
			f.beforePodPatch = func() { f.pod["metadata"].(map[string]any)["uid"] = "12345678-1234-4234-8234-123456789012" }
		}},
		{"resource version changed", func(f *trainingFixture) {
			f.beforePodPatch = func() { f.pod["metadata"].(map[string]any)["resourceVersion"] = "99" }
		}},
		{"controller changed", func(f *trainingFixture) {
			f.beforePodPatch = func() {
				owners := f.pod["metadata"].(map[string]any)["ownerReferences"].([]any)
				owners[0].(map[string]any)["uid"] = "12345678-1234-4234-8234-123456789012"
			}
		}},
		{"another finalizer added", func(f *trainingFixture) {
			f.beforePodPatch = func() {
				f.pod["metadata"].(map[string]any)["finalizers"] = []any{exitEvidenceFinalizer, "other.example.test/retain"}
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newTrainingFixture(t)
			f.pod["metadata"].(map[string]any)["resourceVersion"] = "1"
			f.pod["metadata"].(map[string]any)["finalizers"] = []any{exitEvidenceFinalizer}
			a, ctx := f.adapter(t), context.Background()
			handle, err := a.CreateTraining(ctx, f.plan)
			if err != nil {
				t.Fatal(err)
			}
			f.setTerminal(t, 0)
			observed, err := a.ObserveTraining(ctx, f.plan, handle, nil)
			if err != nil {
				t.Fatal(err)
			}
			test.change(f)
			failed, err := a.ObserveTraining(ctx, f.plan, handle, observed.Resources)
			if !errors.Is(err, biz.ErrTrainingUnavailable) || failed.WritersAbsent || f.podPatches != 1 || f.deletes != 0 {
				t.Fatalf("rejected conditional release produced closure proof: %+v patches=%d err=%v", failed, f.podPatches, err)
			}
			finalizers, _, _ := unstructured.NestedStringSlice(f.pod, "metadata", "finalizers")
			if len(finalizers) == 0 || finalizers[0] != exitEvidenceFinalizer {
				t.Fatalf("rejected mutation lost retained exit evidence: %v", finalizers)
			}
		})
	}
}

func TestTrainingRetainsPodUntilEveryContainerActuallyExits(t *testing.T) {
	for _, kind := range []string{"containers", "initContainers", "ephemeralContainers"} {
		t.Run(kind, func(t *testing.T) {
			f := newTrainingFixture(t)
			f.pod["metadata"].(map[string]any)["resourceVersion"] = "1"
			f.pod["metadata"].(map[string]any)["finalizers"] = []any{exitEvidenceFinalizer}
			a, ctx := f.adapter(t), context.Background()
			handle, err := a.CreateTraining(ctx, f.plan)
			if err != nil {
				t.Fatal(err)
			}
			f.setTerminal(t, 0)
			observed, err := a.ObserveTraining(ctx, f.plan, handle, nil)
			if err != nil {
				t.Fatal(err)
			}
			status, name := "containerStatuses", "node"
			if kind != "containers" {
				name = "other"
				status = "initContainerStatuses"
				if kind == "ephemeralContainers" {
					status = "ephemeralContainerStatuses"
				}
				_ = unstructured.SetNestedSlice(f.pod, []any{map[string]any{"name": name}}, "spec", kind)
			}
			_ = unstructured.SetNestedSlice(f.pod, []any{map[string]any{"name": name, "state": map[string]any{"running": map[string]any{"startedAt": "2026-10-03T00:00:00Z"}}}}, "status", status)
			stillRunning, err := a.ObserveTraining(ctx, f.plan, handle, observed.Resources)
			if err != nil || stillRunning.WritersAbsent || f.podPatches != 0 {
				t.Fatalf("a previous node exit bypassed current all-container checks: %+v patches=%d err=%v", stillRunning, f.podPatches, err)
			}
		})
	}
}

func TestTrainingDoesNotReleaseHistoricalPodWithoutItsExactParentChain(t *testing.T) {
	f := newTrainingFixture(t)
	a, ctx := f.adapter(t), context.Background()
	handle, err := a.CreateTraining(ctx, f.plan)
	if err != nil {
		t.Fatal(err)
	}
	f.setTerminal(t, 0)
	observed, err := a.ObserveTraining(ctx, f.plan, handle, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(f.pod)
	f.oldPod = trainingObject(t, string(encoded))
	metadata := f.oldPod["metadata"].(map[string]any)
	metadata["name"], metadata["uid"], metadata["resourceVersion"] = "prior-training-pod", "12345678-1234-4234-8234-123456789012", "1"
	metadata["finalizers"] = []any{exitEvidenceFinalizer}
	owners := metadata["ownerReferences"].([]any)
	owners[0].(map[string]any)["uid"] = "23456789-2345-4345-8345-234567890123"
	code := int32(0)
	history := append(observed.Resources, biz.RuntimeResource{APIVersion: "v1", Kind: "Pod", Namespace: f.plan.Workspace.NamespaceName, Name: "prior-training-pod", UID: "12345678-1234-4234-8234-123456789012", OwnerUID: "23456789-2345-4345-8345-234567890123", Terminal: true, ExitCode: &code, APIObjectPresent: true})
	failed, err := a.ObserveTraining(ctx, f.plan, handle, history)
	if !errors.Is(err, biz.ErrRuntimeConflict) || failed.WritersAbsent || f.podPatches != 0 {
		t.Fatalf("unproved historical parent chain released a retained exit: %+v err=%v", failed, err)
	}
}

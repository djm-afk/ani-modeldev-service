package trainer_test

import (
	"context"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func stoppedWithoutWorker(t *testing.T) (*trainingFixture, biz.TrainingHandle) {
	t.Helper()
	f := newTrainingFixture(t)
	handle, err := f.adapter(t).CreateTraining(context.Background(), f.plan)
	if err != nil {
		t.Fatal(err)
	}
	f.pod = nil
	f.podListResourceVersion = "102"
	_ = unstructured.SetNestedMap(f.job, runtimePodTemplate(f), "spec", "template")
	for index, object := range []map[string]any{f.train, f.jobset, f.job} {
		_ = unstructured.SetNestedField(object, true, "spec", "suspend")
		_ = unstructured.SetNestedField(object, int64(2), "metadata", "generation")
		_ = unstructured.SetNestedField(object, "101", "metadata", "resourceVersion")
		_ = unstructured.SetNestedField(object, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), "metadata", "creationTimestamp")
		reason := []string{"Suspended", "SuspendedJobs", "JobSuspended"}[index]
		object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Suspended", "status": "True", "reason": reason, "lastTransitionTime": time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)}}}
	}
	// Job startTime is set before the first Pod attempt and survives suspension;
	// it is not evidence that a worker container ever existed or ran.
	_ = unstructured.SetNestedField(f.job, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), "status", "startTime")
	return f, handle
}

func TestStoppedTrainingWithoutCreatedWorkerHasBoundedExitProof(t *testing.T) {
	f, handle := stoppedWithoutWorker(t)
	observed, err := f.adapter(t).ObserveTraining(context.Background(), f.plan, handle, nil)
	if err != nil || !observed.WritersAbsent || observed.Outcome == "SUCCEEDED" {
		t.Fatalf("all original controllers acknowledged suspension and complete retained-worker list is empty: absent=%v outcome=%s err=%v", observed.WritersAbsent, observed.Outcome, err)
	}
	for _, fact := range observed.Resources {
		if fact.Kind == "Pod" {
			t.Fatal("invented a worker exit")
		}
	}
	if f.deletes != 0 || f.posts != 1 || f.podPatches != 0 {
		t.Fatal("zero-worker close changed compute or finalizers")
	}
}

func TestNoWorkerCloseRejectsIncompleteOrUnacknowledgedEvidence(t *testing.T) {
	for _, scenario := range []string{"train_ack", "set_ack", "job_ack", "list_watermark", "active", "succeeded", "failed", "retention", "stale_generation", "duplicate_ack", "deleting_controller"} {
		t.Run(scenario, func(t *testing.T) {
			f, handle := stoppedWithoutWorker(t)
			switch scenario {
			case "train_ack":
				f.train["status"] = map[string]any{}
			case "set_ack":
				f.jobset["status"] = map[string]any{}
			case "job_ack":
				f.job["status"] = map[string]any{}
			case "list_watermark":
				f.podListResourceVersion = ""
			case "deleting_controller":
				_ = unstructured.SetNestedField(f.job, time.Now().UTC().Format(time.RFC3339Nano), "metadata", "deletionTimestamp")
			case "active", "succeeded", "failed":
				_ = unstructured.SetNestedField(f.job, int64(1), "status", scenario)
			case "retention":
				unstructured.RemoveNestedField(f.job, "spec", "template", "metadata", "finalizers")
			case "stale_generation":
				conditions, _, _ := unstructured.NestedSlice(f.job, "status", "conditions")
				conditions[0].(map[string]any)["observedGeneration"] = int64(1)
				_ = unstructured.SetNestedSlice(f.job, conditions, "status", "conditions")
			case "duplicate_ack":
				conditions, _, _ := unstructured.NestedSlice(f.job, "status", "conditions")
				_ = unstructured.SetNestedSlice(f.job, append(conditions, conditions[0]), "status", "conditions")
			}
			observed, err := f.adapter(t).ObserveTraining(context.Background(), f.plan, handle, nil)
			if err == nil && observed.WritersAbsent {
				t.Fatalf("%s erased unresolved creation/writer facts", scenario)
			}
		})
	}
}

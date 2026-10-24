package trainer_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The accepted GPU plan and original controller graph are real domain values;
// only the external Kubernetes API and kubelet logs use existing fixtures.
func stoppedGPUFixture(t *testing.T) (*trainingFixture, biz.TrainingHandle, *trainer.OwnerAssertionSigner) {
	t.Helper()
	plan, handle, pod, client, keyFile, _ := ownerSigningFixture(t)
	f := newTrainingFixture(t)
	f.plan, f.pod = plan, pod.Object
	installFixtureRuntime(t, f)
	var runtimeSpec map[string]any
	if err := json.Unmarshal(plan.Snapshot.RuntimePlan.ExpectedSpec, &runtimeSpec); err != nil {
		t.Fatal(err)
	}
	jobs, _, _ := unstructured.NestedSlice(runtimeSpec, "template", "spec", "replicatedJobs")
	annotations, _, _ := unstructured.NestedStringMap(jobs[0].(map[string]any), "template", "spec", "template", "metadata", "annotations")
	pod.SetAnnotations(annotations)
	f.train = map[string]any{
		"apiVersion": "trainer.kubeflow.org/v1alpha1", "kind": "TrainJob",
		"metadata": map[string]any{"name": plan.Name, "namespace": plan.Workspace.NamespaceName,
			"uid": handle.TrainJobUID, "resourceVersion": "1", "generation": float64(1),
			"annotations": map[string]any{"modeldev.ani.io/tenant-id": plan.TenantID,
				"modeldev.ani.io/execution-id": plan.ExecutionID, "modeldev.ani.io/execution-spec-sha256": plan.SpecHash,
				"modeldev.ani.io/training-request-sha256": plan.RequestSHA256, "modeldev.ani.io/workspace-uid": handle.PVCUID}},
		"spec": map[string]any{"suspend": true},
	}
	f.pod["metadata"].(map[string]any)["finalizers"] = []any{exitEvidenceFinalizer}
	// Real suspension leaves all original controllers present and stops their
	// creation; it does not manufacture Complete/Failed controller conditions.
	for _, object := range []map[string]any{f.train, f.jobset, f.job} {
		_ = unstructured.SetNestedField(object, true, "spec", "suspend")
		object["status"] = map[string]any{"conditions": []any{}}
	}
	deleting := metav1.NewTime(time.Now().UTC())
	pod.SetDeletionTimestamp(&deleting)
	_, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(pod.GetNamespace()).Update(context.Background(), pod, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	signer, err := trainer.NewOwnerAssertionSigner(client, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	return f, handle, signer
}

func TestGPUStoppedPodObservationDoesNotRequireOwnerResigning(t *testing.T) {
	for _, withSigner := range []bool{true, false} {
		t.Run(map[bool]string{true: "original-deleting-pod", false: "unsigned-terminal-no-signer"}[withSigner], func(t *testing.T) {
			f, handle, signer := stoppedGPUFixture(t)
			a := f.adapter(t)
			if withSigner {
				a.WithOwnerSigner(signer)
			}
			// Preserve the actual Stop shape: deleting original Pod, exit 143,
			// suspended controllers and the original exit-evidence finalizer.
			f.setTerminal(t, 143)
			if !withSigner {
				delete(f.pod["metadata"].(map[string]any), "deletionTimestamp")
			}
			for _, object := range []map[string]any{f.train, f.jobset, f.job} {
				object["status"] = map[string]any{"conditions": []any{}}
			}
			observed, err := a.ObserveTraining(context.Background(), f.plan, handle, nil)
			if err != nil || observed.SpecificationMismatch || observed.WritersAbsent {
				t.Fatalf("original stopped GPU exit must be offered for persistence: %+v err=%v", observed, err)
			}
			log := originalWorkerLog(observed)
			for _, fact := range observed.Resources {
				if fact.UID == childPodUID && (!fact.Terminal || fact.ExitCode == nil || *fact.ExitCode != 143) {
					t.Fatalf("original worker exit code was not preserved: %+v", fact)
				}
			}
			if log == nil || len(log.Result.Lines) == 0 {
				t.Fatal("terminal observation lost original worker logs")
			}
			confirmed, err := a.ObserveTraining(context.Background(), f.plan, handle, observed.Resources)
			if err != nil || !confirmed.WritersAbsent {
				t.Fatalf("persisted original exit did not close suspended writers: %+v err=%v", confirmed, err)
			}
		})
	}
}

func TestGPUDeletingWorkerRemainsUnresolvedUntilActualExit(t *testing.T) {
	f, handle, _ := stoppedGPUFixture(t)
	observed, err := f.adapter(t).ObserveTraining(context.Background(), f.plan, handle, nil)
	if err != nil || observed.SpecificationMismatch || observed.WritersAbsent {
		t.Fatalf("deletion request cannot substitute for real GPU worker exit: %+v err=%v", observed, err)
	}
}

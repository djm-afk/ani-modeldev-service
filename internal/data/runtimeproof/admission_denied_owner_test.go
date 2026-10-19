package runtimeproof_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// These fields reproduce ft-cn99c-1611097450 from the original 2026-10-15
// admission denial. Only the fixture namespace/name and completion time change.
// Argo startedAt records node initialization, not a running container.
func addServerDeniedDriver(t *testing.T, f *ownerCloseFixture) string {
	t.Helper()
	id := f.workflow.GetName() + "-1611097450"
	name := f.workflow.GetName() + ".root.exit-handler-1.train-wait-driver"
	finished, _, _ := unstructured.NestedString(f.workflow.Object, "status", "finishedAt")
	node := map[string]any{"id": id, "name": name, "displayName": "train-wait-driver", "type": "Pod", "phase": "Error", "templateName": "system-container-driver", "templateScope": "local/" + f.workflow.GetName(), "startedAt": finished, "finishedAt": finished, "progress": "0/1", "taskResultSynced": true, "message": "task '" + name + "' errored: admission webhook \"" + f.workflow.GetNamespace() + ".modeldev.ani.io\" denied the request: ModelDev Pod admission refused: dependency_unavailable"}
	nodes := f.workflow.Object["status"].(map[string]any)["nodes"].(map[string]any)
	nodes[id] = node
	f.workflow.Object["status"].(map[string]any)["phase"] = "Error"
	return id
}

func TestOwnerCloseProvesFormalAdmissionDeniedDriverWithoutInventingExit(t *testing.T) {
	f := newOwnerCloseFixture(t)
	f.terminateExternalRecords(t)
	id := addServerDeniedDriver(t, f)
	evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil)
	if err != nil || evidence.OwnerTermination == nil || len(evidence.Resources) != len(f.pods) {
		t.Fatalf("explicit server-denied create is not an absent historical Pod: resources=%d actual=%d err=%v", len(evidence.Resources), len(f.pods), err)
	}
	for _, r := range evidence.Resources {
		if r.Name == id || r.UID == id {
			t.Fatal("non-created driver acquired invented exit", r)
		}
	}
}

func TestOwnerCloseAdmissionDenialDoesNotHideUnknownOrActualWriters(t *testing.T) {
	for _, attack := range []string{"other-hook", "http-timeout", "ordinary-error", "wrong-node-message", "wrong-template", "output", "host", "container", "resources", "progress", "phase", "same-node-pod", "foreign-same-node-pod", "historical-pod"} {
		t.Run(attack, func(t *testing.T) {
			f := newOwnerCloseFixture(t)
			f.terminateExternalRecords(t)
			id := addServerDeniedDriver(t, f)
			node := f.workflow.Object["status"].(map[string]any)["nodes"].(map[string]any)[id].(map[string]any)
			switch attack {
			case "other-hook":
				node["message"] = "task '" + node["name"].(string) + "' errored: admission webhook \"other.modeldev.ani.io\" denied the request: ModelDev Pod admission refused: dependency_unavailable"
			case "http-timeout":
				node["message"] = "failed calling webhook: context deadline exceeded"
			case "ordinary-error":
				node["message"] = "pod deleted"
			case "wrong-node-message":
				node["message"] = "task 'other-node' errored: admission webhook \"" + f.workflow.GetNamespace() + ".modeldev.ani.io\" denied the request: ModelDev Pod admission refused: dependency_unavailable"
			case "wrong-template":
				node["templateName"] = "metadata-0-2-retry-system-container-impl"
			case "output":
				node["outputs"] = map[string]any{"result": "ran"}
			case "host":
				node["hostNodeName"] = "actual-worker"
			case "container":
				node["containerID"] = "actual-container"
			case "resources":
				node["resourcesDuration"] = map[string]any{"cpu": int64(1)}
			case "progress":
				node["progress"] = "1/1"
			case "phase":
				node["phase"] = "Failed"
			case "same-node-pod", "foreign-same-node-pod":
				p := f.pods[0].DeepCopy()
				p.SetName("unexpected-denied-driver")
				p.SetUID("actual-denied-driver-uid")
				if attack == "foreign-same-node-pod" {
					p.SetOwnerReferences(nil)
				}
				p.SetAnnotations(map[string]string{"workflows.argoproj.io/node-id": id, "workflows.argoproj.io/node-name": node["name"].(string)})
				f.pods = append(f.pods, *p)
			case "historical-pod":
				f.pods = f.pods[1:]
			}
			evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil)
			if err == nil || evidence.OwnerTermination != nil {
				t.Fatalf("%s fabricated complete owner proof: %+v %v", attack, evidence, err)
			}
		})
	}
}

func TestAdmissionDeniedDriverCleanupPersistsRealExitsAndOnlyWorkflowDeletion(t *testing.T) {
	var compute *computeCleanupFixture
	var runtimeObject *unstructured.Unstructured
	step := newConfiguredStepRetentionFixture(t, false, nil, func(owner *ownerCloseFixture) { runtimeObject = computeRuntime(t, owner) }, func(step *stepRetentionFixture, _ *lifecycle.Repository, kube dynamic.Interface) {
		addServerDeniedDriver(t, step.f)
		compute = &computeCleanupFixture{step: step, kube: kube, runtime: runtimeObject, objects: map[string]*unstructured.Unstructured{}, pendingReads: map[string]int{}}
		step.serveExtra = compute.serve
	})
	if compute == nil || compute.step != step {
		t.Fatal("existing real-PG/HTTPS fixture unavailable")
	}
	ctx := context.Background()
	pool := step.open()
	before, err := lifecycle.New(pool).GetRuntime(ctx, step.f.execution.TenantID, step.f.execution.ExecutionID)
	pool.Close()
	if err != nil || before.Training != nil || before.TrainingHandle != nil || before.StepExitCleanup == nil || len(before.StepExitCleanup.Evidence.Resources) != len(step.f.pods) {
		t.Fatal("noncreation invented training or a Pod exit", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		closer, _ := closedRecoveryCloser(t, compute)
		result, err := closer.Reconcile(ctx, step.f.execution.TenantID, step.f.execution.ExecutionID)
		if err != nil || result.Runtime.StepExitCleanup == nil || result.Runtime.StepExitCleanup.CompletedAt == nil || result.Runtime.ComputeCleanup == nil || result.Runtime.ComputeCleanup.Phase != "APPLIED" || len(step.pods) != 0 {
			t.Fatalf("post-close original exit cleanup failed: %+v %v", result.Runtime.ComputeCleanup, err)
		}
		if !reflect.DeepEqual(result.Runtime.CloseEvidence, before.CloseEvidence) || !result.Runtime.ClosedAt.Equal(*before.ClosedAt) || result.Runtime.TrainingHandle != nil || result.Runtime.Training != nil {
			t.Fatal("cleanup changed original close or invented training")
		}
	}
	if !reflect.DeepEqual(compute.deleted, []string{"Workflow"}) || step.deletes != len(step.f.pods) || step.patches != len(step.f.pods) {
		t.Fatal("restart repeated mutations or deleted a non-created Pod", compute.deleted, step.deletes, step.patches)
	}
	pool = step.open()
	state, err := lifecycle.New(pool).GetRuntime(ctx, step.f.execution.TenantID, step.f.execution.ExecutionID)
	pool.Close()
	if err != nil || state.ComputeCleanup.Attempts != 1 || !state.ComputeCleanup.LogsReady || len(state.ComputeCleanup.Targets) != 1 {
		t.Fatal("non-created training added cleanup targets", err)
	}
}

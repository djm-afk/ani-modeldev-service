package runtimeproof_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestOwnerCloseProvesCapturedPodInitializationAbortWithoutInventingMainExit(t *testing.T) {
	f := newOwnerCloseFixture(t)
	f.terminateExternalRecords(t)
	pod := capturedInitializationAbort(t, &f.pods[0])
	evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil)
	if err != nil || evidence.OwnerTermination == nil {
		t.Fatalf("INITIALIZATION_ABORT_NOT_IMPLEMENTED: retained captured never-started Pod must close only with independent completed owner proof: %+v %v", evidence, err)
	}
	var resource biz.RuntimeResource
	for _, item := range evidence.Resources {
		if item.UID == string(pod.GetUID()) {
			resource = item
		}
	}
	if resource.ExitCode != nil || !resource.APIObjectPresent || !resource.Terminal {
		t.Fatalf("initialization abort invented a main exit or disappeared its Pod: %+v", resource)
	}
	raw, err := json.Marshal(resource)
	if err != nil {
		t.Fatal(err)
	}
	var facts map[string]any
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatal(err)
	}
	proof, ok := facts["InitializationAbort"].(map[string]any)
	if !ok || proof["PodResourceVersion"] != "1170948" || proof["NodeName"] != "ani-01" || proof["Phase"] != "Failed" {
		t.Fatalf("actual initialization facts were not retained: %s", raw)
	}
	association := biz.ManagedStepAssociation{RunID: f.runID, NamespaceName: f.authority().NamespaceName, NamespaceUID: f.authority().NamespaceUID, WorkflowName: f.workflow.GetName(), WorkflowUID: string(f.workflow.GetUID()), PodName: pod.GetName(), PodUID: string(pod.GetUID())}
	if _, err := f.verifier().ResolveManagedTaskID(context.Background(), f.dispatch.Plan, association, "workspace-name"); err == nil {
		t.Fatal("initialization abort authorized a current managed caller")
	}
	if _, err := f.verifier().VerifyWritersAbsent(context.Background(), f.execution, association, nil); err == nil {
		t.Fatal("owner-only abort leaked into ordinary managed close proof")
	}
}

func TestOwnerInitializationAbortAllowsSandboxStopAfterDeletionButRejectsFutureStop(t *testing.T) {
	for _, future := range []bool{false, true} {
		name := "after-deletion"
		if future {
			name = "future-stop"
		}
		t.Run(name, func(t *testing.T) {
			f := newOwnerCloseFixture(t)
			f.terminateExternalRecords(t)
			pod := capturedInitializationAbort(t, &f.pods[0])
			// Keep the captured init/status structure; vary only the sandbox
			// timestamp at the external boundary to test both deletion orders.
			stopped := "2026-10-05T18:10:49Z"
			if future {
				stopped = time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
			}
			pod.Object["status"].(map[string]any)["conditions"].([]any)[1].(map[string]any)["lastTransitionTime"] = stopped
			evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil)
			if future {
				if !errors.Is(err, biz.ErrRuntimeNotReady) || evidence.OwnerTermination != nil {
					t.Fatalf("future sandbox stop became close proof: %+v %v", evidence, err)
				}
				return
			}
			if err != nil || evidence.OwnerTermination == nil {
				t.Fatalf("sandbox stopping after irreversible deletion must retain valid owner proof: %+v %v", evidence, err)
			}
			for _, resource := range evidence.Resources {
				if resource.UID == string(pod.GetUID()) {
					abort := resource.InitializationAbort
					if abort == nil || !abort.SandboxStoppedAt.After(abort.DeletedAt) || resource.ExitCode != nil {
						t.Fatalf("actual deletion/stop ordering was rewritten: %+v", resource)
					}
					return
				}
			}
			t.Fatal("aborted Pod missing from owner evidence")
		})
	}
}

func TestOwnerInitializationAbortFailsClosedOnIncompleteOrStartedContainers(t *testing.T) {
	for _, attack := range []string{"running", "missing-status", "extra-status", "history", "restart", "started", "container-id", "image-id", "ready", "sidecar-init", "ephemeral", "initialized", "sandbox-live", "sandbox-before-exit", "no-deletion", "no-finalizer", "phase-only", "restart-policy", "disruption", "controller-incomplete", "wrong-owner"} {
		t.Run(attack, func(t *testing.T) {
			f := newOwnerCloseFixture(t)
			f.terminateExternalRecords(t)
			pod := capturedInitializationAbort(t, &f.pods[0])
			spec, status := pod.Object["spec"].(map[string]any), pod.Object["status"].(map[string]any)
			main := status["containerStatuses"].([]any)[0].(map[string]any)
			conditions := status["conditions"].([]any)
			switch attack {
			case "running":
				main["state"] = map[string]any{"running": map[string]any{"startedAt": "2026-10-05T18:10:44Z"}}
			case "missing-status":
				status["containerStatuses"] = status["containerStatuses"].([]any)[1:]
			case "extra-status":
				status["containerStatuses"] = append(status["containerStatuses"].([]any), main)
			case "history":
				main["lastState"] = map[string]any{"terminated": map[string]any{"exitCode": int64(0)}}
			case "restart":
				main["restartCount"] = int64(1)
			case "started":
				main["started"] = true
			case "container-id":
				main["containerID"] = "containerd://previously-started"
			case "image-id":
				main["imageID"] = "sha256:previous-image"
			case "ready":
				main["ready"] = true
			case "sidecar-init":
				spec["initContainers"].([]any)[0].(map[string]any)["restartPolicy"] = "Always"
			case "ephemeral":
				spec["ephemeralContainers"] = []any{map[string]any{"name": "debug"}}
			case "initialized":
				conditions[0].(map[string]any)["status"] = "True"
			case "sandbox-live":
				conditions[1].(map[string]any)["status"] = "True"
			case "sandbox-before-exit":
				conditions[1].(map[string]any)["lastTransitionTime"] = "2026-10-05T18:10:43Z"
			case "no-deletion":
				pod.SetDeletionTimestamp(nil)
			case "no-finalizer":
				pod.SetFinalizers(nil)
			case "phase-only":
				delete(status, "conditions")
			case "restart-policy":
				spec["restartPolicy"] = "Always"
			case "disruption":
				status["conditions"] = append(conditions, map[string]any{"type": "DisruptionTarget", "status": "True"})
			case "controller-incomplete":
				f.workflow.SetLabels(nil)
			case "wrong-owner":
				refs := pod.GetOwnerReferences()
				refs[0].UID = "other-owner"
				pod.SetOwnerReferences(refs)
			}
			if evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil); err == nil || evidence.OwnerTermination != nil || (!errors.Is(err, biz.ErrRuntimeNotReady) && !errors.Is(err, biz.ErrRuntimeConflict)) {
				t.Fatalf("%s converted incomplete writer facts to CLOSED: %+v %v", attack, evidence, err)
			}
		})
	}
}

// The container/spec/condition structure and timestamps are the actual
// deadline02 Pod capture (SHA a7c64330...). Only its owner envelope is adapted
// to the existing frozen conformance fixture; no waiting state becomes an exit.
func capturedInitializationAbort(t *testing.T, pod *unstructured.Unstructured) *unstructured.Unstructured {
	t.Helper()
	raw, err := os.ReadFile("testdata/kubelet-1.35.8-pod-initialization-abort.json")
	if err != nil {
		t.Fatal(err)
	}
	var object unstructured.Unstructured
	if err := object.UnmarshalJSON(raw); err != nil {
		t.Fatal(err)
	}
	account, _, _ := unstructured.NestedString(pod.Object, "spec", "serviceAccountName")
	pod.Object["spec"], pod.Object["status"] = object.Object["spec"], object.Object["status"]
	if err := unstructured.SetNestedField(pod.Object, account, "spec", "serviceAccountName"); err != nil {
		t.Fatal(err)
	}
	pod.SetResourceVersion("1170948")
	pod.SetGeneration(2)
	var deletion metav1.Time
	if err := deletion.UnmarshalJSON([]byte(`"2026-10-05T18:10:48Z"`)); err != nil {
		t.Fatal(err)
	}
	pod.SetDeletionTimestamp(&deletion)
	pod.SetFinalizers([]string{"modeldev.ani.io/step-exit-evidence"})
	return pod
}

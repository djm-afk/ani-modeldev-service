package runtimeproof_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// The shape is the original general-cpu-l9wdw Terminate observation:
// Completed=True, all actual Pod nodes finished, with three onExit DAG/Retry
// nodes left Running by Argo 3.7.3. Only its envelope is adapted to the frozen
// existing owner fixture; none of the original node phases is rewritten.
func capturedTerminatedStructuralNodes(t *testing.T, f *ownerCloseFixture) {
	t.Helper()
	f.terminateExternalRecords(t)
	if err := unstructured.SetNestedField(f.workflow.Object, "Terminate", "spec", "shutdown"); err != nil {
		t.Fatal(err)
	}
	nodes, _, err := unstructured.NestedMap(f.workflow.Object, "status", "nodes")
	if err != nil {
		t.Fatal(err)
	}
	// Retain the existing captured node IDs, boundaries and KFP child refs.
	// Its corresponding executor/DAG envelope supplies the same three native
	// structural states without inventing a different task/Pod association.
	selected := map[string]string{"workspace-name": "DAG", "executor": "Retry", "workspace-name.onExit": "DAG"}
	matched := 0
	for _, raw := range nodes {
		node := raw.(map[string]any)
		name, _ := node["displayName"].(string)
		if kind, ok := selected[name]; ok && node["type"] == kind {
			node["phase"] = "Running"
			delete(node, "finishedAt")
			matched++
		}
	}
	if matched != 3 {
		t.Fatalf("captured structural envelope changed: %d", matched)
	}
	for _, raw := range f.run["run_details"].(map[string]any)["task_details"].([]any) {
		task := raw.(map[string]any)
		name, _ := task["display_name"].(string)
		if _, selected := selected[name]; selected {
			task["state"] = "RUNNING"
		}
	}
	if err := unstructured.SetNestedMap(f.workflow.Object, nodes, "status", "nodes"); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerCloseAcceptsCapturedNativeTerminateStructuralResidue(t *testing.T) {
	f := newOwnerCloseFixture(t)
	capturedTerminatedStructuralNodes(t, f)
	evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil)
	if err != nil || evidence.OwnerTermination == nil || len(evidence.Resources) != len(f.pods) {
		t.Fatalf("NATIVE_TERMINATE_NOT_IMPLEMENTED: completed original Workflow must retain full structural Running residue while independently proving every actual Pod: %+v %v", evidence, err)
	}
}

func TestOwnerCloseCombinesNativeTerminateAndUncreatedInitWithoutRewritingFacts(t *testing.T) {
	f := newOwnerCloseFixture(t)
	capturedTerminatedStructuralNodes(t, f)
	capturedNeverCreatedFirstInit(t, &f.pods[0])
	originalWorkflow, _ := json.Marshal(f.workflow.Object)
	originalRun, _ := json.Marshal(f.run)
	verifier := f.verifier()
	evidence, err := verifier.VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil)
	if err != nil || evidence.OwnerTermination == nil {
		t.Fatalf("combined original native termination shape remained unresolved: %+v %v", evidence, err)
	}
	if err := verifier.VerifyRecordedOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil, evidence); err != nil {
		t.Fatalf("independent retained abort proof could not be reread: %v", err)
	}
	afterWorkflow, _ := json.Marshal(f.workflow.Object)
	afterRun, _ := json.Marshal(f.run)
	if !bytes.Equal(originalWorkflow, afterWorkflow) || !bytes.Equal(originalRun, afterRun) {
		t.Fatal("owner close rewrote a captured structural task or Workflow phase")
	}
}

func TestOwnerCloseRejectsUnprovedNativeTerminate(t *testing.T) {
	for _, attack := range []string{"shutdown-absent", "shutdown-stop", "completion-label", "completion-condition", "workflow-running", "running-pod-node", "unknown-running-kind", "pending-structural", "running-container", "missing-historical-pod", "task-child", "task-podname", "task-name", "task-state", "duplicate-structural-match"} {
		t.Run(attack, func(t *testing.T) {
			f := newOwnerCloseFixture(t)
			capturedTerminatedStructuralNodes(t, f)
			status := f.workflow.Object["status"].(map[string]any)
			nodes := status["nodes"].(map[string]any)
			var structural map[string]any
			for _, raw := range nodes {
				node := raw.(map[string]any)
				if node["type"] == "DAG" && node["phase"] == "Running" {
					structural = node
					break
				}
			}
			switch attack {
			case "shutdown-absent":
				unstructured.RemoveNestedField(f.workflow.Object, "spec", "shutdown")
			case "shutdown-stop":
				_ = unstructured.SetNestedField(f.workflow.Object, "Stop", "spec", "shutdown")
			case "completion-label":
				f.workflow.SetLabels(nil)
			case "completion-condition":
				status["conditions"] = []any{map[string]any{"type": "Completed", "status": "False"}}
			case "workflow-running":
				status["phase"] = "Running"
			case "running-pod-node":
				for _, raw := range nodes {
					if node := raw.(map[string]any); node["type"] == "Pod" {
						node["phase"] = "Running"
						break
					}
				}
			case "unknown-running-kind":
				structural["type"] = "Steps"
			case "pending-structural":
				structural["phase"] = "Pending"
			case "running-container":
				f.pods[0].Object["status"] = map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"name": "main", "state": map[string]any{"running": map[string]any{}}}}}
			case "missing-historical-pod":
				f.pods = f.pods[1:]
			case "task-child", "task-podname", "task-name", "task-state":
				for _, raw := range f.run["run_details"].(map[string]any)["task_details"].([]any) {
					task := raw.(map[string]any)
					if task["display_name"] != "executor" {
						continue
					}
					switch attack {
					case "task-child":
						task["child_tasks"] = []any{map[string]any{"pod_name": "another-controller-node"}}
					case "task-podname":
						task["pod_name"] = f.pods[0].GetName()
					case "task-name":
						task["display_name"] = "unrelated-running-task"
					case "task-state":
						task["state"] = "PENDING"
					}
				}
			case "duplicate-structural-match":
				duplicate := map[string]any{}
				for key, value := range structural {
					duplicate[key] = value
				}
				id := f.workflow.GetName() + "-duplicate"
				duplicate["id"] = id
				nodes[id] = duplicate
			}
			if evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil); err == nil || evidence.OwnerTermination != nil {
				t.Fatalf("%s bypassed original writer/termination proof: %+v %v", attack, evidence, err)
			}
		})
	}
}

// The original 617 execution never formed a sandbox. Kubelet 1.35.8 emitted
// Unknown/137 for its never-created first init, without a process identity or
// any start/finish timestamp. This is not a real exit or a successful init.
func capturedNeverCreatedFirstInit(t *testing.T, pod *unstructured.Unstructured) *unstructured.Unstructured {
	t.Helper()
	pod = capturedInitializationAbort(t, pod)
	pod.SetResourceVersion("51806037")
	var deleted metav1.Time
	if err := deleted.UnmarshalJSON([]byte(`"2026-10-21T20:06:30Z"`)); err != nil {
		t.Fatal(err)
	}
	pod.SetDeletionTimestamp(&deleted)
	status := pod.Object["status"].(map[string]any)
	for _, raw := range status["conditions"].([]any) {
		raw.(map[string]any)["lastTransitionTime"] = "2026-10-21T19:57:22Z"
	}
	first := status["initContainerStatuses"].([]any)[0].(map[string]any)
	first["state"] = map[string]any{"terminated": map[string]any{"exitCode": int64(137), "reason": "ContainerStatusUnknown", "message": "The container could not be located when the pod was terminated", "startedAt": nil, "finishedAt": nil}}
	first["ready"], first["imageID"] = false, ""
	delete(first, "containerID")
	return pod
}

func TestOwnerCloseProvesCapturedNeverCreatedFirstInitWithoutProcessExit(t *testing.T) {
	f := newOwnerCloseFixture(t)
	f.terminateExternalRecords(t)
	pod := capturedNeverCreatedFirstInit(t, &f.pods[0])
	evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil)
	if err != nil || evidence.OwnerTermination == nil {
		t.Fatalf("NEVER_CREATED_INIT_NOT_IMPLEMENTED: original terminal owner plus stopped never-created sandbox must retain a distinct abort marker: %+v %v", evidence, err)
	}
	for _, resource := range evidence.Resources {
		if resource.UID != string(pod.GetUID()) {
			continue
		}
		if resource.ExitCode != nil || resource.InitializationAbort == nil || len(resource.InitializationAbort.InitContainerExits) != 0 || !resource.InitializationAbort.ValidAt(evidence.ObservedAt) {
			t.Fatalf("unknown init status became a real process exit: %+v", resource)
		}
		raw, _ := json.Marshal(resource.InitializationAbort)
		var proof map[string]any
		_ = json.Unmarshal(raw, &proof)
		marker, ok := proof["UncreatedInitContainer"].(map[string]any)
		if !ok || marker["Name"] != "init" || marker["Reason"] != "ContainerStatusUnknown" || marker["ReportedExitCode"] != float64(137) {
			t.Fatalf("missing independent uncreated-init marker: %s", raw)
		}
		return
	}
	t.Fatal("original never-created Pod missing from evidence")
}

func TestOwnerCloseRejectsUnprovedNeverCreatedFirstInit(t *testing.T) {
	for _, attack := range []string{"reason", "exit-code", "message", "container-id", "status-container-id", "image-id", "started-at", "finished-at", "started", "restart", "last-state", "ready", "generation", "sandbox-live", "initialized", "no-retention"} {
		t.Run(attack, func(t *testing.T) {
			f := newOwnerCloseFixture(t)
			f.terminateExternalRecords(t)
			pod := capturedNeverCreatedFirstInit(t, &f.pods[0])
			status := pod.Object["status"].(map[string]any)
			first := status["initContainerStatuses"].([]any)[0].(map[string]any)
			termination := first["state"].(map[string]any)["terminated"].(map[string]any)
			switch attack {
			case "reason":
				termination["reason"] = "Error"
			case "exit-code":
				termination["exitCode"] = int64(1)
			case "message":
				termination["message"] = "unknown"
			case "container-id":
				termination["containerID"] = "containerd://real-process"
			case "status-container-id":
				first["containerID"] = "containerd://real-process"
			case "image-id":
				first["imageID"] = "sha256:actual-image"
			case "started-at":
				termination["startedAt"] = "2026-10-21T19:57:21Z"
			case "finished-at":
				termination["finishedAt"] = "2026-10-21T19:57:22Z"
			case "started":
				first["started"] = true
			case "restart":
				first["restartCount"] = int64(1)
			case "last-state":
				first["lastState"] = map[string]any{"terminated": map[string]any{"exitCode": int64(0)}}
			case "ready":
				first["ready"] = true
			case "generation":
				status["conditions"].([]any)[0].(map[string]any)["observedGeneration"] = int64(1)
			case "sandbox-live":
				status["conditions"].([]any)[1].(map[string]any)["status"] = "True"
			case "initialized":
				status["conditions"].([]any)[0].(map[string]any)["status"] = "True"
			case "no-retention":
				pod.SetFinalizers(nil)
			}
			if evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil); err == nil || evidence.OwnerTermination != nil {
				t.Fatalf("%s falsely proved a never-created init: %+v %v", attack, evidence, err)
			}
		})
	}
}

func TestOwnerCloseRoundTripsNeverCreatedMarkerAndRejectsPersistedForgery(t *testing.T) {
	f := newOwnerCloseFixture(t)
	f.terminateExternalRecords(t)
	pod := capturedNeverCreatedFirstInit(t, &f.pods[0])
	evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var original *biz.PodInitializationAbort
	for _, resource := range evidence.Resources {
		if resource.UID == string(pod.GetUID()) {
			original = resource.InitializationAbort
		}
	}
	if original == nil {
		t.Fatal("missing actual abort")
	}
	raw, _ := json.Marshal(original)
	for _, attack := range []string{"none", "missing-marker", "reason", "code", "container-id", "image-id", "start-time", "finish-time", "started", "restart", "last-state", "ready", "init-order", "real-exit"} {
		t.Run(attack, func(t *testing.T) {
			var proof biz.PodInitializationAbort
			if err := json.Unmarshal(raw, &proof); err != nil {
				t.Fatal(err)
			}
			marker := proof.UncreatedInitContainer
			at := evidence.ObservedAt.Add(-time.Minute)
			switch attack {
			case "missing-marker":
				proof.UncreatedInitContainer = nil
			case "reason":
				marker.Reason = "Error"
			case "code":
				marker.ReportedExitCode = 1
			case "container-id":
				marker.ContainerID = "actual-container"
			case "image-id":
				marker.ImageID = "actual-image"
			case "start-time":
				marker.StartedAt = &at
			case "finish-time":
				marker.FinishedAt = &at
			case "started":
				marker.Started = true
			case "restart":
				marker.RestartCount = 1
			case "last-state":
				marker.LastStateEmpty = false
			case "ready":
				marker.Ready = true
			case "init-order":
				proof.DeclaredInitContainers[0] = "another-init"
			case "real-exit":
				proof.InitContainerExits = []biz.InitializationContainerExit{{Name: "init", ContainerID: "actual-container", ImageID: "actual-image", StartedAt: at, FinishedAt: at}}
			}
			if proof.ValidAt(evidence.ObservedAt) != (attack == "none") {
				t.Fatalf("%s changed persisted abort validity incorrectly", attack)
			}
		})
	}
}

func TestOwnerNativeTerminateUncreatedInitPersistsThroughIndependentDatabaseAndCleanup(t *testing.T) {
	fixture := newConfiguredStepRetentionFixture(t, false, nil, nil, func(step *stepRetentionFixture, _ *lifecycle.Repository, _ dynamic.Interface) {
		capturedTerminatedStructuralNodes(t, step.f)
		capturedNeverCreatedFirstInit(t, &step.f.pods[0])
		for i := range step.f.pods {
			step.pods[step.f.pods[i].GetName()] = step.f.pods[i].DeepCopy()
		}
	})
	ctx := context.Background()
	state, err := lifecycle.New(fixture.open()).GetRuntime(ctx, fixture.f.execution.TenantID, fixture.f.execution.ExecutionID)
	if err != nil || state.ClosedAt == nil || state.CloseEvidence == nil {
		t.Fatalf("independent PostgreSQL reconnect lost original native close: %+v %v", state, err)
	}
	var original *biz.InitializationContainerNotCreated
	for _, resource := range state.CloseEvidence.Resources {
		if abort := resource.InitializationAbort; abort != nil {
			if resource.ExitCode != nil || len(abort.InitContainerExits) != 0 || abort.UncreatedInitContainer == nil || !abort.ValidAt(state.CloseEvidence.ObservedAt) {
				t.Fatalf("database converted missing init into a process exit: %+v", resource)
			}
			original = abort.UncreatedInitContainer
		}
	}
	if original == nil || original.ReportedExitCode != 137 {
		t.Fatal("database omitted distinct uncreated-init marker")
	}
	removed, err := fixture.reconcile(t)
	if err != nil {
		t.Fatal("strict retained-UID cleanup rejected the persisted uncreated-init proof", err)
	}
	state, err = lifecycle.New(fixture.open()).CompleteStepExitCleanup(ctx, fixture.f.authority(), fixture.receipt.CloseGeneration, removed)
	if err != nil || state.StepExitCleanup.CompletedAt == nil {
		t.Fatal("original owner cleanup did not commit after independent close", err)
	}
	for _, resource := range state.StepExitCleanup.RemovedResources {
		if abort := resource.InitializationAbort; abort != nil {
			if resource.ExitCode != nil || abort.UncreatedInitContainer == nil || *abort.UncreatedInitContainer != *original {
				t.Fatal("cleanup rewrote the retained missing-container marker")
			}
		}
	}
}

package runtimeproof_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// Keep the original 5953 graph, Run tasks, Pod identities and all kubelet status
// bytes. Only the frozen admission's external connection/namespace envelope is
// adapted, as in the existing owner-close conformance fixture.
func capturedEvictedOwner(t *testing.T) *ownerCloseFixture {
	t.Helper()
	f := newOwnerCloseFixture(t)
	raw, err := os.ReadFile("testdata/kubelet-1.35.8-initialized-evicted-owner.json")
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Workflow map[string]any
		Pods     []map[string]any
		Run      map[string]any
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	f.workflow = &unstructured.Unstructured{}
	workflowJSON, _ := json.Marshal(record.Workflow)
	if err := f.workflow.UnmarshalJSON(workflowJSON); err != nil {
		t.Fatal(err)
	}
	f.workflow.SetNamespace(f.execution.Snapshot.Environment.NamespaceName)
	f.run, f.runID = record.Run, record.Run["run_id"].(string)
	f.dispatch.ConfirmedRuns[0].RunID = f.runID
	plan := f.dispatch.Plan
	f.run["experiment_id"], f.run["display_name"], f.run["service_account"] = plan.Environment.ExperimentID, plan.DisplayName, plan.Environment.Identities.KFPStepServiceAccount
	f.run["pipeline_version_reference"] = map[string]any{"pipeline_id": plan.PipelineID, "pipeline_version_id": plan.PipelineVersionID}
	f.run["runtime_config"] = map[string]any{"parameters": map[string]any{"execution_id": plan.ExecutionID, "spec_hash": plan.SpecHash}, "pipeline_root": plan.Owner.PipelineRoot}
	f.pods = nil
	for _, raw := range record.Pods {
		var pod unstructured.Unstructured
		podJSON, _ := json.Marshal(raw)
		if err := pod.UnmarshalJSON(podJSON); err != nil {
			t.Fatal(err)
		}
		pod.SetNamespace(f.execution.Snapshot.Environment.NamespaceName)
		if err := unstructured.SetNestedField(pod.Object, plan.Environment.Identities.KFPStepServiceAccount, "spec", "serviceAccountName"); err != nil {
			t.Fatal(err)
		}
		f.pods = append(f.pods, pod)
	}
	return f
}

func TestOwnerCloseProvesCapturedInitializedEvictedSandboxWithoutInventingExit(t *testing.T) {
	f := capturedEvictedOwner(t)
	v := f.verifier()
	evidence, err := v.VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil)
	if err != nil || evidence.OwnerTermination == nil || len(evidence.Resources) != 6 {
		t.Fatalf("EVICTED_SANDBOX_CLOSE_NOT_IMPLEMENTED: completed original owner and post-termination missing-container markers cannot close: %+v %v", evidence, err)
	}
	missing := 0
	for _, resource := range evidence.Resources {
		if resource.UID != "759d2e06-4622-40ed-a8bb-0bd3a1923b90" && resource.UID != "b64f6994-1dcf-4853-b2a4-abbd89b6ec64" {
			continue
		}
		missing++
		raw, _ := json.Marshal(resource)
		var saved map[string]any
		_ = json.Unmarshal(raw, &saved)
		if resource.ExitCode != nil || resource.InitializationAbort != nil || saved["SandboxTermination"] == nil || !resource.Terminal || !resource.CreationDisabled {
			t.Fatalf("kubelet's missing-state marker became a real exit or lost its original proof: %s", raw)
		}
		association := biz.ManagedStepAssociation{RunID: f.runID, NamespaceName: f.authority().NamespaceName, NamespaceUID: f.authority().NamespaceUID, WorkflowName: f.workflow.GetName(), WorkflowUID: string(f.workflow.GetUID()), PodName: resource.Name, PodUID: resource.UID}
		role := "workspace-name"
		if resource.UID == "b64f6994-1dcf-4853-b2a4-abbd89b6ec64" {
			role = "close-finalizer"
		}
		if _, err := v.ResolveManagedTaskID(context.Background(), f.dispatch.Plan, association, role); err == nil {
			t.Fatal("post-termination proof authorized an ordinary managed caller")
		}
		if _, err := v.VerifyWritersAbsent(context.Background(), f.execution, association, nil); err == nil {
			t.Fatal("sandbox-only owner proof leaked into ordinary managed close")
		}
	}
	if missing != 2 {
		t.Fatalf("original two evicted Pod identities lost: %d", missing)
	}
	if err := v.VerifyRecordedOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil, evidence); err != nil {
		t.Fatalf("persisted sandbox proof cannot be independently reread: %v", err)
	}
}

func TestOwnerSandboxTerminationRejectsIncompleteNativeFacts(t *testing.T) {
	for _, attack := range []string{"missing-marker", "marker-message", "marker-code", "marker-time", "sandbox-missing", "sandbox-ready", "condition-generation", "status-generation", "future-condition", "not-initialized", "not-evicted", "running", "missing-status", "extra-status", "extra-declared", "restartable-init", "ephemeral", "init-not-exited", "init-exit-after-stop", "main-history", "main-started", "main-identity", "wait-history", "no-retention", "no-node", "wrong-role", "wrong-owner", "missing-historical-pod", "creation-unconfirmed", "workflow-running", "unrecognized-template", "duplicate-membership"} {
		t.Run(attack, func(t *testing.T) {
			f := capturedEvictedOwner(t)
			var pod *unstructured.Unstructured
			for i := range f.pods {
				if string(f.pods[i].GetUID()) == "759d2e06-4622-40ed-a8bb-0bd3a1923b90" {
					pod = &f.pods[i]
				}
			}
			status, spec := pod.Object["status"].(map[string]any), pod.Object["spec"].(map[string]any)
			conditions := status["conditions"].([]any)
			var sandbox map[string]any
			for _, raw := range conditions {
				if c := raw.(map[string]any); c["type"] == "PodReadyToStartContainers" {
					sandbox = c
				}
			}
			containers := status["containerStatuses"].([]any)
			main := containers[0].(map[string]any)
			wait := containers[1].(map[string]any)
			marker := main["state"].(map[string]any)["terminated"].(map[string]any)
			switch attack {
			case "missing-marker":
				main["state"] = map[string]any{"waiting": map[string]any{"reason": "ImagePullBackOff"}}
			case "marker-message":
				marker["message"] = "unknown, but not kubelet post-termination"
			case "marker-code":
				marker["exitCode"] = int64(0)
			case "marker-time":
				marker["startedAt"] = "2026-10-28T23:14:58Z"
			case "sandbox-missing":
				sandbox["type"] = "UnrelatedCondition"
			case "sandbox-ready":
				sandbox["status"] = "True"
			case "condition-generation":
				sandbox["observedGeneration"] = int64(2)
			case "status-generation":
				status["observedGeneration"] = int64(2)
			case "future-condition":
				sandbox["lastTransitionTime"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
			case "not-initialized":
				for _, raw := range conditions {
					if c := raw.(map[string]any); c["type"] == "Initialized" {
						c["status"] = "False"
					}
				}
			case "not-evicted":
				status["reason"] = "NodeLost"
			case "running":
				main["state"] = map[string]any{"running": map[string]any{"startedAt": "2026-10-28T23:14:58Z"}}
			case "missing-status":
				status["containerStatuses"] = containers[:1]
			case "extra-status":
				status["containerStatuses"] = append(containers, main)
			case "extra-declared":
				spec["containers"] = append(spec["containers"].([]any), map[string]any{"name": "unknown-writer"})
			case "restartable-init":
				spec["initContainers"].([]any)[0].(map[string]any)["restartPolicy"] = "Always"
			case "ephemeral":
				spec["ephemeralContainers"] = []any{map[string]any{"name": "debug"}}
			case "init-not-exited":
				status["initContainerStatuses"].([]any)[0].(map[string]any)["state"] = map[string]any{"running": map[string]any{}}
			case "init-exit-after-stop":
				status["initContainerStatuses"].([]any)[1].(map[string]any)["state"].(map[string]any)["terminated"].(map[string]any)["finishedAt"] = "2026-10-28T23:16:00Z"
			case "main-history":
				main["lastState"] = wait["lastState"]
			case "main-started":
				main["started"] = true
			case "main-identity":
				main["containerID"] = "containerd://actual-main"
			case "wait-history":
				wait["lastState"].(map[string]any)["terminated"].(map[string]any)["message"] = "unproved historical process"
			case "no-retention":
				pod.SetFinalizers(nil)
			case "no-node":
				delete(spec, "nodeName")
			case "wrong-role":
				labels := pod.GetLabels()
				labels["ani.modeldev/step-role"] = "prepare"
				pod.SetLabels(labels)
			case "wrong-owner":
				refs := pod.GetOwnerReferences()
				refs[0].UID = "foreign-workflow"
				pod.SetOwnerReferences(refs)
			case "missing-historical-pod":
				for i := range f.pods {
					if f.pods[i].GetUID() == pod.GetUID() {
						f.pods = append(f.pods[:i], f.pods[i+1:]...)
						break
					}
				}
			case "creation-unconfirmed":
				f.dispatch.State = biz.PipelineDispatchUncertain
			case "workflow-running":
				f.workflow.Object["status"].(map[string]any)["phase"] = "Running"
			case "unrecognized-template":
				nodes := f.workflow.Object["status"].(map[string]any)["nodes"].(map[string]any)
				nodes["md-9tbrj-1827742978"].(map[string]any)["templateName"] = "unreviewed-retry-system-container-impl"
			case "duplicate-membership":
				tasks := f.run["run_details"].(map[string]any)["task_details"].([]any)
				for _, raw := range tasks {
					if task := raw.(map[string]any); task["display_name"] == "executor" {
						task["child_tasks"] = append(task["child_tasks"].([]any), map[string]any{"pod_name": "md-9tbrj-1827742978"})
						break
					}
				}
			}
			if evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil); err == nil || evidence.OwnerTermination != nil {
				t.Fatalf("%s converted incomplete native facts into owner close: %+v %v", attack, evidence, err)
			}
		})
	}
}

func TestRecordedSandboxTerminationSurvivesOwnFinalizerReleaseButRejectsChangedFacts(t *testing.T) {
	f := capturedEvictedOwner(t)
	evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range f.pods {
		f.pods[i].SetFinalizers(nil)
		f.pods[i].SetResourceVersion("after-own-finalizer-release")
	}
	if err := f.verifier().VerifyRecordedOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil, evidence); err != nil {
		t.Fatalf("own-finalizer release erased independently persisted sandbox facts: %v", err)
	}
	for i := range f.pods {
		if string(f.pods[i].GetUID()) == "759d2e06-4622-40ed-a8bb-0bd3a1923b90" {
			f.pods[i].Object["status"].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)["started"] = true
		}
	}
	if err := f.verifier().VerifyRecordedOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil, evidence); err == nil {
		t.Fatal("changed process status borrowed a previously proved sandbox marker")
	}
}

func TestCapturedEvictedSandboxPersistsCloseStepCleanupAndComputeGCAcrossReopen(t *testing.T) {
	f, closed := newClosedRecoveryCleanupFixture(t, true)
	if closed.ClosedAt == nil || closed.StepExitCleanup == nil || closed.StepExitCleanup.CompletedAt == nil || closed.Workspace != nil || closed.Training != nil {
		t.Fatal("original pre-workspace owner did not persist independent close and step cleanup")
	}
	missing := 0
	for _, resource := range closed.CloseEvidence.Resources {
		if resource.SandboxTermination != nil {
			missing++
			if resource.ExitCode != nil || !resource.SandboxTermination.ValidAt(closed.CloseEvidence.ObservedAt) {
				t.Fatal("persisted missing marker became a process exit")
			}
		}
	}
	if missing != 2 {
		t.Fatalf("original sandbox marker count changed after PostgreSQL reopen: %d", missing)
	}
	for attempt := 0; attempt < 2; attempt++ {
		closer, _ := closedRecoveryCloser(t, f)
		result, err := closer.Reconcile(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
		if err != nil || result.Runtime.ComputeCleanup == nil || result.Runtime.ComputeCleanup.Phase != "APPLIED" || !result.Runtime.ComputeCleanup.LogsReady || len(result.Runtime.ComputeCleanup.Logs) != 0 || !f.workflowDeleted || len(f.step.pods) != 0 {
			t.Fatalf("post-termination close did not reach original Workflow/Pod GC: %+v %v", result.Runtime.ComputeCleanup, err)
		}
	}
	if f.step.f.run["state"] != "FAILED" {
		t.Fatal("close recovery changed original failed Run into training success")
	}
}

func capturedSandboxTerminationProof(t *testing.T) *biz.PodSandboxTermination {
	t.Helper()
	f := capturedEvictedOwner(t)
	evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range evidence.Resources {
		if resource.SandboxTermination != nil {
			return resource.SandboxTermination
		}
	}
	t.Fatal("missing original post-termination marker")
	return nil
}

func TestSandboxTerminationRejectsExistingWorkspaceAtCloseWrite(t *testing.T) {
	proof := capturedSandboxTerminationProof(t)
	newConfiguredStepRetentionFixture(t, false, nil, nil, func(f *stepRetentionFixture, repository *lifecycle.Repository, _ dynamic.Interface) {
		ctx, authority := context.Background(), f.f.authority()
		closing, _, err := repository.RequestRuntimeClose(ctx, authority, "STEP_FAILED")
		if err != nil || closing.Workspace == nil {
			t.Fatal("existing prepared workspace precondition", err)
		}
		evidence, err := f.proof.VerifyOwnerWritersAbsent(ctx, f.f.execution, authority, &f.workspace)
		if err != nil {
			t.Fatal(err)
		}
		// The storage port must independently reject an otherwise well-formed
		// owner claim outside its supported pre-workspace scope.
		evidence.Resources[0].ExitCode = nil
		evidence.Resources[0].CreationDisabled = true
		evidence.Resources[0].SandboxTermination = proof
		if _, err := repository.ConfirmRuntimeClosed(ctx, authority, closing.CloseGeneration, biz.TrainingRuntimeObservation{}, evidence); !errors.Is(err, biz.ErrRuntimeNotReady) {
			t.Fatalf("EXISTING_WORKSPACE_SANDBOX_WRITE_ACCEPTED: persisted workspace accepted owner-only pre-workspace marker: %v", err)
		}
	})
}

func TestSandboxTerminationRejectsExistingWorkspaceOnCommittedReopen(t *testing.T) {
	f := newStepRetentionFixture(t, false)
	ctx := context.Background()
	pool := f.open()
	repository := lifecycle.New(pool)
	state, err := repository.GetRuntime(ctx, f.f.execution.TenantID, f.f.execution.ExecutionID)
	if err != nil || state.Workspace == nil || state.ClosedAt == nil {
		t.Fatal("existing workspace and committed close precondition", err)
	}
	proof := capturedSandboxTerminationProof(t)
	uid := state.CloseEvidence.Resources[0].UID
	for _, resources := range [][]biz.RuntimeResource{state.CloseEvidence.Resources, state.StepExitCleanup.Evidence.Resources} {
		for i := range resources {
			if resources[i].UID == uid {
				resources[i].ExitCode = nil
				resources[i].CreationDisabled = true
				resources[i].SandboxTermination = proof
			}
		}
	}
	raw, _ := json.Marshal(state)
	// Fault injection is restricted to this test's freshly isolated schema.
	// Close and step receipts agree, so a different equality check cannot hide
	// the missing persisted workspace-scope validation.
	if _, err := pool.Exec(ctx, "UPDATE modeldev_execution_runtimes SET facts=$3::jsonb WHERE tenant_id=$1::uuid AND execution_id=$2::uuid", f.f.execution.TenantID, f.f.execution.ExecutionID, raw); err != nil {
		t.Fatal("isolated corrupt-facts fixture", err)
	}
	pool.Close()
	if _, err := lifecycle.New(f.open()).GetRuntime(ctx, f.f.execution.TenantID, f.f.execution.ExecutionID); !errors.Is(err, biz.ErrPersistence) {
		t.Fatalf("EXISTING_WORKSPACE_SANDBOX_REOPEN_ACCEPTED: committed marker outside pre-workspace scope survived reopen: %v", err)
	}
}

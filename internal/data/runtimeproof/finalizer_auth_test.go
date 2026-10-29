package runtimeproof_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The independent KFP GetRun and complete Workflow/Pod capture are from the
// failed 5f0b866e execution. Only the conformance admission's external envelope
// is adapted, as in the existing captured owner fixtures; task IDs, children,
// node graph, Pod UIDs and original exits remain the actual captured values.
func capturedFinalizerAuth(t *testing.T) (*ownerCloseFixture, biz.ManagedStepAssociation) {
	t.Helper()
	f := newOwnerCloseFixture(t)
	raw, err := os.ReadFile("testdata/kfp-2.16-finalizer-after-rejected-prepare.json")
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
	f.workflow = &unstructured.Unstructured{Object: record.Workflow}
	f.workflow.SetNamespace(f.execution.Snapshot.Environment.NamespaceName)
	f.run, f.runID = record.Run, record.Run["run_id"].(string)
	f.dispatch.ConfirmedRuns[0].RunID = f.runID
	plan := f.dispatch.Plan
	f.run["experiment_id"], f.run["display_name"], f.run["service_account"] = plan.Environment.ExperimentID, plan.DisplayName, plan.Environment.Identities.KFPStepServiceAccount
	f.run["pipeline_version_reference"] = map[string]any{"pipeline_id": plan.PipelineID, "pipeline_version_id": plan.PipelineVersionID}
	f.run["runtime_config"] = map[string]any{"parameters": map[string]any{"execution_id": plan.ExecutionID, "spec_hash": plan.SpecHash}, "pipeline_root": plan.Owner.PipelineRoot}
	f.pods = nil
	for _, value := range record.Pods {
		pod := unstructured.Unstructured{Object: value}
		pod.SetNamespace(plan.Environment.NamespaceName)
		if err := unstructured.SetNestedField(pod.Object, plan.Environment.Identities.KFPStepServiceAccount, "spec", "serviceAccountName"); err != nil {
			t.Fatal(err)
		}
		f.pods = append(f.pods, pod)
	}
	a := f.authority()
	return f, biz.ManagedStepAssociation{RunID: a.RunID, NamespaceName: a.NamespaceName, NamespaceUID: a.NamespaceUID,
		WorkflowName: a.WorkflowName, WorkflowUID: a.WorkflowUID,
		PodName: "md-58kt8-4222919810", PodUID: "769e67c0-9cb2-477f-b6c5-6f027a8eb14a"}
}

func TestManagedFinalizerAuthenticatesAfterSiblingPrepareAdmissionRejected(t *testing.T) {
	f, association := capturedFinalizerAuth(t)
	id, err := f.verifier().ResolveManagedTaskID(context.Background(), f.dispatch.Plan, association, "close")
	if err != nil || id != "607c8d15-1181-4f53-af92-0d7e3d506316" {
		t.Fatalf("FINALIZER_AUTH_BLOCKED_BY_REJECTED_SIBLING: independent original Run/Workflow/Pod must resolve CLOSE task identity: id=%q err=%v", id, err)
	}
}

func TestManagedFinalizerRetainsIndependentIdentityFences(t *testing.T) {
	for _, attack := range []string{"pod-uid", "workflow-uid", "namespace-uid", "run-id", "wrong-role", "unknown-role", "service-account", "duplicate-role", "missing-independent-executor-reference"} {
		t.Run(attack, func(t *testing.T) {
			f, a := capturedFinalizerAuth(t)
			role := "close"
			switch attack {
			case "pod-uid":
				a.PodUID = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
			case "workflow-uid":
				a.WorkflowUID = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
			case "namespace-uid":
				a.NamespaceUID = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
			case "run-id":
				f.run["run_id"] = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
			case "wrong-role":
				role = "prepare"
			case "unknown-role":
				role = "unknown"
			case "service-account":
				for i := range f.pods {
					if f.pods[i].GetName() == a.PodName {
						_ = unstructured.SetNestedField(f.pods[i].Object, "other-step", "spec", "serviceAccountName")
					}
				}
			case "duplicate-role":
				tasks := f.run["run_details"].(map[string]any)["task_details"].([]any)
				for _, value := range tasks {
					if task := value.(map[string]any); task["display_name"] == "finalize-close" {
						copy := make(map[string]any)
						for key, value := range task {
							copy[key] = value
						}
						copy["task_id"] = "bbbbbbbb-1111-4111-8111-aaaaaaaaaaaa"
						f.run["run_details"].(map[string]any)["task_details"] = append(tasks, copy)
						break
					}
				}
			case "missing-independent-executor-reference":
				for _, value := range f.run["run_details"].(map[string]any)["task_details"].([]any) {
					task := value.(map[string]any)
					children, _ := task["child_tasks"].([]any)
					for _, ref := range children {
						if child := ref.(map[string]any); child["pod_name"] == "md-58kt8-4222919810" {
							child["pod_name"] = "unrelated-native-node"
						}
					}
				}
			}
			if id, err := f.verifier().ResolveManagedTaskID(context.Background(), f.dispatch.Plan, a, role); err == nil || id != "" {
				t.Fatalf("unverified current close identity accepted: id=%q err=%v", id, err)
			}
		})
	}
}

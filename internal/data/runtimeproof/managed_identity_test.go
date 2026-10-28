package runtimeproof_test

import (
	"context"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// This is a CODE vector for an ordinary running task with the current frozen
// metadata wrapper. It does not claim that the failed live 5953 Pod ran.
func TestManagedIdentityAcceptsCurrentMetadataRunningPodWithOriginalFences(t *testing.T) {
	f := newOwnerCloseFixture(t)
	a := f.authority()
	podName, podUID := "general-cpu-rnggt-retry-system-container-impl-1467617326", "cbee9dc7-116f-432d-b639-334fcadf0e7e"
	nodes := f.workflow.Object["status"].(map[string]any)["nodes"].(map[string]any)
	for _, raw := range nodes {
		node := raw.(map[string]any)
		if node["templateName"] == "retry-system-container-impl" {
			node["templateName"] = "metadata-1-2-retry-system-container-impl"
		}
		if node["displayName"] == "workspace-name" || node["displayName"] == "executor" || node["type"] == "Pod" && node["phase"] == "Succeeded" {
			node["phase"] = "Running"
		}
	}
	for _, raw := range f.run["run_details"].(map[string]any)["task_details"].([]any) {
		task := raw.(map[string]any)
		if task["display_name"] == "workspace-name" || task["display_name"] == "executor" {
			task["state"] = "RUNNING"
		}
	}
	f.workflow.Object["status"].(map[string]any)["phase"] = "Running"
	f.run["state"] = "RUNNING"
	for i := range f.pods {
		if f.pods[i].GetName() == podName {
			f.pods[i].Object["status"] = map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"name": "main", "state": map[string]any{"running": map[string]any{"startedAt": time.Now().UTC().Format(time.RFC3339Nano)}}}}}
		}
	}
	association := biz.ManagedStepAssociation{RunID: a.RunID, NamespaceName: a.NamespaceName, NamespaceUID: a.NamespaceUID, WorkflowName: a.WorkflowName, WorkflowUID: a.WorkflowUID, PodName: podName, PodUID: podUID}
	id, err := f.verifier().ResolveManagedTaskID(context.Background(), f.dispatch.Plan, association, "workspace-name")
	if err != nil || id != "35e10c3b-d56f-4260-bdb0-1069ea06f76d" {
		t.Fatalf("current running metadata executor did not resolve: %q %v", id, err)
	}
	association.PodUID = "another-uid"
	if id, err := f.verifier().ResolveManagedTaskID(context.Background(), f.dispatch.Plan, association, "workspace-name"); err == nil || id != "" {
		t.Fatal("current metadata wrapper bypassed original Pod UID fence")
	}
}

func TestManagedIdentityReturnsKFPTaskIDOnlyForExactCurrentRolePod(t *testing.T) {
	f := newOwnerCloseFixture(t)
	a := f.authority()
	association := biz.ManagedStepAssociation{RunID: a.RunID, NamespaceName: a.NamespaceName, NamespaceUID: a.NamespaceUID, WorkflowName: a.WorkflowName, WorkflowUID: a.WorkflowUID, PodName: "general-cpu-rnggt-retry-system-container-impl-1467617326", PodUID: "cbee9dc7-116f-432d-b639-334fcadf0e7e"}
	id, err := f.verifier().ResolveManagedTaskID(context.Background(), f.dispatch.Plan, association, "workspace-name")
	if err != nil || id != "35e10c3b-d56f-4260-bdb0-1069ea06f76d" {
		t.Fatalf("real KFP identity unresolved: %q %v", id, err)
	}
	for _, change := range []string{"uid", "role", "workflow", "namespace"} {
		t.Run(change, func(t *testing.T) {
			claim, role := association, "workspace-name"
			if change == "uid" {
				claim.PodUID = "different-pod-uid"
			}
			if change == "role" {
				role = "publish"
			}
			if change == "workflow" {
				claim.WorkflowUID = "different-controller-uid"
			}
			if change == "namespace" {
				claim.NamespaceUID = "different-namespace-uid"
			}
			if id, err := f.verifier().ResolveManagedTaskID(context.Background(), f.dispatch.Plan, claim, role); err == nil || id != "" {
				t.Fatal("unproven task identifier disclosed")
			}
		})
	}
}

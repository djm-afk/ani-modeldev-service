package runtimeproof_test

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"testing"
)

// Only the external KFP/Argo boundary is a fixture. Production resolves its
// native graph and actual Pod UID through the same authenticated code path.
func nativeTaskFixture(t *testing.T, workflow, pod *unstructured.Unstructured, runID, taskID, role, state string) map[string]any {
	t.Helper()
	nodes, _, err := unstructured.NestedMap(workflow.Object, "status", "nodes")
	if err != nil {
		t.Fatal(err)
	}
	if nodes == nil {
		nodes = map[string]any{}
	}
	controller, executor := pod.GetName()+"-controller", pod.GetName()+"-native-node"
	phase := map[string]string{"SUCCEEDED": "Succeeded", "RUNNING": "Running", "FAILED": "Failed", "SKIPPED": "Omitted"}[state]
	nodes[controller] = map[string]any{"id": controller, "name": workflow.GetName() + "." + role, "displayName": role, "type": "DAG", "phase": phase, "children": []any{executor}}
	nodeName := workflow.GetName() + "." + role + ".executor"
	nodes[executor] = map[string]any{"id": executor, "name": nodeName, "displayName": "executor", "type": "Pod", "phase": phase, "boundaryID": controller, "templateName": "metadata-0-2-retry-system-container-impl"}
	if err := unstructured.SetNestedMap(workflow.Object, nodes, "status", "nodes"); err != nil {
		t.Fatal(err)
	}
	annotations := pod.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["workflows.argoproj.io/node-id"] = executor
	annotations["workflows.argoproj.io/node-name"] = nodeName
	pod.SetAnnotations(annotations)
	return map[string]any{"run_id": runID, "task_id": taskID, "display_name": role, "state": state, "child_tasks": []any{map[string]any{"pod_name": executor}}}
}

package kfp_test

import (
	"context"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

const (
	pipelineNamespace   = "tenant-training"
	pipelineNamespaceID = "namespace-uid"
	pipelineExecution   = "608aa6bf-4dac-4989-bd22-9b0ef6f1ce68"
	pipelineRunID       = "run-1"
	pipelineWorkflow    = "run-1"
	pipelineWorkflowID  = "workflow-uid"
)

func pipelineScope() biz.ExecutionWorkloadScope {
	return biz.ExecutionWorkloadScope{
		TenantID: "9086ce3b-bd19-47c3-8467-190607f633db", ExecutionID: pipelineExecution,
		NamespaceName: pipelineNamespace, NamespaceUID: pipelineNamespaceID,
		RunID: pipelineRunID, WorkflowName: pipelineWorkflow, WorkflowUID: pipelineWorkflowID,
		Availability: biz.PodAvailabilityLive,
	}
}

func pipelineObject(apiVersion, kind, name, uid string, owner map[string]any, labels map[string]any) *unstructured.Unstructured {
	metadata := map[string]any{"name": name, "uid": uid}
	if kind != "Namespace" {
		metadata["namespace"] = pipelineNamespace
	}
	if len(labels) != 0 {
		metadata["labels"] = labels
	}
	if owner != nil {
		metadata["ownerReferences"] = []any{owner}
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": metadata}}
}

func workflowOwner(uid string) map[string]any {
	return map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "name": pipelineWorkflow, "uid": uid, "controller": true}
}

func stepPod(name, uid, workflowUID, nodeName string) *unstructured.Unstructured {
	return pipelineObject("v1", "Pod", name, uid, workflowOwner(workflowUID),
		map[string]any{"workflows.argoproj.io/workflow": pipelineWorkflow, "workflows.argoproj.io/node-name": nodeName, "workflows.argoproj.io/completed": "false"})
}

// Step Pods carry the Argo node name as step_name, never the node ID as a Pod
// name, and each gets an owner-issued log ID.
func TestListStepPodsProjectsNodeNameAndLogID(t *testing.T) {
	workflow := pipelineObject("argoproj.io/v1alpha1", "Workflow", pipelineWorkflow, pipelineWorkflowID, nil, nil)
	objects := []runtime.Object{workflow,
		pipelineObject("v1", "Namespace", pipelineNamespace, pipelineNamespaceID, nil, nil),
		stepPod("run-1-prepare-12345", "step-uid", pipelineWorkflowID, "prepare"),
	}
	reader := kfp.NewPodReader(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...))
	pods, err := reader.ListStepPods(context.Background(), pipelineScope())
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != 1 || pods[0].StepName != "prepare" || pods[0].Layer != biz.PodLayerPipeline || pods[0].Availability != biz.PodAvailabilityLive {
		t.Fatalf("step pod projection: %+v", pods)
	}
	if pods[0].LogID == "" || !pods[0].LogAvailable {
		t.Fatalf("step log credential: %+v", pods[0])
	}
}

// A residual step Pod whose owner Workflow still matches the frozen authority UID
// is shown read-only as ORPHANED; one whose owner UID differs never appears.
func TestListStepPodsMarksOrphansAndSkipsForeignWorkflows(t *testing.T) {
	objects := []runtime.Object{
		pipelineObject("v1", "Namespace", pipelineNamespace, pipelineNamespaceID, nil, nil),
		stepPod("run-1-verify-aaaaa", "orphan-uid", pipelineWorkflowID, "verify"),
		stepPod("run-1-foreign-bbbbb", "foreign-uid", "another-workflow-uid", "export"),
	}
	reader := kfp.NewPodReader(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...))
	pods, err := reader.ListStepPods(context.Background(), pipelineScope())
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != 1 || pods[0].UID != "orphan-uid" || pods[0].Availability != biz.PodAvailabilityOrphaned {
		t.Fatalf("orphan selection: %+v", pods)
	}
}

// A Workflow replaced under the same name breaks the frozen UID and is refused
// instead of being reported as this execution's run.
func TestListStepPodsRejectsReplacedWorkflow(t *testing.T) {
	replaced := pipelineObject("argoproj.io/v1alpha1", "Workflow", pipelineWorkflow, "replacement-uid", nil, nil)
	objects := []runtime.Object{replaced,
		pipelineObject("v1", "Namespace", pipelineNamespace, pipelineNamespaceID, nil, nil),
		stepPod("run-1-prepare-12345", "step-uid", "replacement-uid", "prepare"),
	}
	reader := kfp.NewPodReader(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...))
	if _, err := reader.ListStepPods(context.Background(), pipelineScope()); err == nil {
		t.Fatal("replaced Workflow UID accepted")
	}
}

// A log read re-applies the list verification, so a Pod the list would hide
// cannot be addressed directly.
func TestStepPodReusesListVerification(t *testing.T) {
	objects := []runtime.Object{
		pipelineObject("v1", "Namespace", pipelineNamespace, pipelineNamespaceID, nil, nil),
		stepPod("run-1-verify-aaaaa", "orphan-uid", pipelineWorkflowID, "verify"),
	}
	reader := kfp.NewPodReader(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...))
	if _, err := reader.StepPod(context.Background(), pipelineScope(), "orphan-uid"); err != nil {
		t.Fatalf("authorized step Pod rejected: %v", err)
	}
	if _, err := reader.StepPod(context.Background(), pipelineScope(), "unknown-uid"); err == nil {
		t.Fatal("unknown Pod UID returned a Pod")
	}
}

package runtimeproof

import (
	"context"
	"encoding/json"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// A root driver can be rejected before any Pod exists. Discover the original
// Workflow independently; the native graph and frozen inputs below, rather
// than this discovery label, establish its close-only association.
func (verifier *Verifier) rootAdmissionWorkflow(ctx context.Context, namespace, runID string) (*unstructured.Unstructured, error) {
	list, err := verifier.kube.Resource(schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil || list == nil || list.GetContinue() != "" {
		return nil, biz.ErrRuntimeNotReady
	}
	var selected *unstructured.Unstructured
	for i := range list.Items {
		workflow := &list.Items[i]
		linked := workflow.GetLabels()["pipeline/runid"] == runID
		if template := namedWorkflowTemplate(workflow, "system-dag-driver"); template != nil {
			args, _, _ := unstructured.NestedSlice(template, "container", "args")
			for i := 0; i+1 < len(args); i++ {
				linked = linked || args[i] == "--run_id" && args[i+1] == runID
			}
		}
		if !linked {
			continue
		}
		if selected != nil {
			return nil, biz.ErrRunAuthorityConflict
		}
		selected = workflow
	}
	if selected == nil {
		return nil, biz.ErrRuntimeNotReady
	}
	return selected, nil
}

// Match the current KFP 2.16 three-node failure before its first Pod: the
// entrypoint and root driver failed, and the dependent root was omitted.
// Nothing here grants a normal managed-step identity or invents a Pod exit.
func rootAdmissionRejected(workflow *unstructured.Unstructured, plan biz.PipelineDispatchPlan, runID string, run kfp.ManagedRun, pods []unstructured.Unstructured) bool {
	if run.State != "FAILED" || run.FinishedAt.IsZero() || run.FinishedAt.After(time.Now()) || len(run.Tasks) != 3 ||
		workflow.GetNamespace() != plan.Environment.NamespaceName || workflow.GetUID() == "" || workflow.GetDeletionTimestamp() != nil ||
		workflow.GetLabels()["pipeline/runid"] != runID {
		return false
	}
	account, _ := textAt(workflow, "spec", "serviceAccountName")
	entrypoint, _ := textAt(workflow, "spec", "entrypoint")
	if account != plan.Environment.Identities.KFPStepServiceAccount || entrypoint != "entrypoint" {
		return false
	}
	proof, actualPodNodes, err := ownerWorkflowCompletion(workflow, time.Now().UTC())
	if err != nil || len(actualPodNodes) != 0 || proof.WorkflowPhase != "Error" || !proof.WorkflowFinishedAt.Equal(run.FinishedAt) {
		return false
	}
	nodes, _, _ := unstructured.NestedMap(workflow.Object, "status", "nodes")
	if len(nodes) != 3 {
		return false
	}
	byName := make(map[string]map[string]any, 3)
	for _, task := range run.Tasks {
		if task.RunID != runID || task.PodName != "" || byName[task.Name] != nil {
			return false
		}
		_, node, err := taskControllerNode(task, nodes)
		if err != nil || task.State != "" && task.State != controllerTaskState(node["phase"].(string)) {
			return false
		}
		byName[task.Name] = node
	}
	entry, driver, root := byName[workflow.GetName()], byName["root-driver"], byName["root"]
	if entry == nil || driver == nil || root == nil || entry["id"] != workflow.GetName() || entry["name"] != workflow.GetName() || entry["type"] != "DAG" || entry["phase"] != "Error" || entry["templateName"] != "entrypoint" || entry["templateScope"] != "local/"+workflow.GetName() ||
		driver["name"] != workflow.GetName()+".root-driver" || driver["templateName"] != "system-dag-driver" || driver["boundaryID"] != entry["id"] ||
		root["name"] != workflow.GetName()+".root" || root["type"] != "Skipped" || root["phase"] != "Omitted" || root["templateName"] != "root" || root["templateScope"] != "local/"+workflow.GetName() || root["boundaryID"] != entry["id"] ||
		!admissionRejectionWithoutProcess(entry) || !admissionRejectionWithoutProcess(root) || !serverDeniedAdmissionNode(workflow, driver) {
		return false
	}
	outbound, ok := entry["outboundNodes"].([]any)
	if !ok || len(outbound) != 1 || outbound[0] != root["id"] {
		return false
	}
	for i := range pods {
		pod := &pods[i]
		if pod.GetLabels()["workflows.argoproj.io/workflow"] == workflow.GetName() || podMatchesServerDeniedNode(workflow, pod) {
			return false
		}
		for _, owner := range pod.GetOwnerReferences() {
			if owner.UID == workflow.GetUID() {
				return false
			}
		}
	}
	config, ok := rootNamedParameter(driver, "inputs", "runtime-config")
	driverType, typeOK := rootNamedParameter(driver, "inputs", "driver-type")
	if !ok || !typeOK || driverType != "ROOT_DAG" || !rootRuntimeConfigMatches(config, plan) {
		return false
	}
	native := namedWorkflowTemplate(workflow, "system-dag-driver")
	if native == nil {
		return false
	}
	command, _, _ := unstructured.NestedSlice(native, "container", "command")
	args, _, _ := unstructured.NestedSlice(native, "container", "args")
	if len(command) != 1 || command[0] != "driver" || !rootDriverFlag(args, "--run_id", runID) || !rootDriverFlag(args, "--run_display_name", plan.DisplayName) {
		return false
	}
	entryTemplate := namedWorkflowTemplate(workflow, "entrypoint")
	if entryTemplate == nil {
		return false
	}
	if _, retry := entryTemplate["retryStrategy"]; retry {
		return false
	}
	tasks, found, err := unstructured.NestedSlice(entryTemplate, "dag", "tasks")
	if err != nil || !found || len(tasks) != 2 {
		return false
	}
	seenDriver, seenRoot := false, false
	for _, raw := range tasks {
		task, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		if _, retry := task["retryStrategy"]; retry {
			return false
		}
		switch task["name"] {
		case "root-driver":
			value, ok := rootNamedParameter(task, "arguments", "runtime-config")
			typeValue, typeOK := rootNamedParameter(task, "arguments", "driver-type")
			if seenDriver || task["template"] != "system-dag-driver" || !ok || value != config || !typeOK || typeValue != "ROOT_DAG" {
				return false
			}
			seenDriver = true
		case "root":
			if seenRoot || task["template"] != "root" || task["depends"] != "root-driver.Succeeded" {
				return false
			}
			seenRoot = true
		default:
			return false
		}
	}
	return seenDriver && seenRoot
}

func namedWorkflowTemplate(workflow *unstructured.Unstructured, name string) map[string]any {
	templates, _, err := unstructured.NestedSlice(workflow.Object, "spec", "templates")
	if err != nil {
		return nil
	}
	var selected map[string]any
	for _, raw := range templates {
		template, ok := raw.(map[string]any)
		if !ok {
			return nil
		}
		if template["name"] == name {
			if selected != nil {
				return nil
			}
			selected = template
		}
	}
	return selected
}

func rootNamedParameter(value map[string]any, field, name string) (string, bool) {
	parameters, _, err := unstructured.NestedSlice(value, field, "parameters")
	if err != nil {
		return "", false
	}
	matches, result := 0, ""
	for _, raw := range parameters {
		parameter, ok := raw.(map[string]any)
		if !ok {
			return "", false
		}
		if parameter["name"] == name {
			matches++
			result, ok = parameter["value"].(string)
			if !ok {
				return "", false
			}
		}
	}
	return result, matches == 1
}

func rootDriverFlag(args []any, flag, expected string) bool {
	matches := 0
	for i, arg := range args {
		if arg == flag {
			matches++
			if i+1 == len(args) || args[i+1] != expected {
				return false
			}
		}
	}
	return matches == 1
}

func rootRuntimeConfigMatches(raw string, plan biz.PipelineDispatchPlan) bool {
	var config map[string]any
	if json.Unmarshal([]byte(raw), &config) != nil || len(config) != 2 || config["gcsOutputDirectory"] != plan.Owner.PipelineRoot {
		return false
	}
	parameters, ok := config["parameterValues"].(map[string]any)
	return ok && len(parameters) == 2 && parameters["execution_id"] == plan.ExecutionID && parameters["spec_hash"] == plan.SpecHash
}

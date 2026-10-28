package runtimeproof

import (
	"strings"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func resolvedManagedTasks(workflow *unstructured.Unstructured, tasks []kfp.ManagedTask, pods []unstructured.Unstructured) ([]kfp.ManagedTask, error) {
	return resolvedManagedTasksForKind(workflow, tasks, pods, "GENERAL_TRAINING")
}

func resolvedManagedTasksForKind(workflow *unstructured.Unstructured, tasks []kfp.ManagedTask, pods []unstructured.Unstructured, kind string) ([]kfp.ManagedTask, error) {
	if workflow == nil || workflow.GetUID() == "" {
		return nil, biz.ErrRuntimeNotReady
	}
	nodes, found, err := unstructured.NestedMap(workflow.Object, "status", "nodes")
	if err != nil || !found || len(nodes) == 0 {
		return nil, biz.ErrRuntimeNotReady
	}
	for id, raw := range nodes {
		node, ok := raw.(map[string]any)
		if !ok || node["id"] != id || id == "" || node["name"] == nil || node["displayName"] == nil {
			return nil, biz.ErrRuntimeNotReady
		}
	}
	var result []kfp.ManagedTask
	for _, task := range tasks {
		name := task.Name
		if name == "finalize-close" {
			name = "close-finalizer"
		}
		switch name {
		case "workspace-name", "prepare", "train-wait", "collect", "publish", "close", "close-finalizer":
		case "export", "verify", "register":
			if kind != "FINETUNING" {
				continue
			}
		default:
			continue
		}
		id, node, err := taskControllerNode(task, nodes)
		if err != nil {
			return nil, err
		}
		phase, _ := node["phase"].(string)
		state := controllerTaskState(phase)
		if state == "" || (task.State != "" && task.State != state) {
			return nil, biz.ErrRuntimeNotReady
		}
		resolved := kfp.ManagedTask{RunID: task.RunID, ID: task.ID, Name: name, State: state}
		mainNodes := 0
		for podID, raw := range nodes {
			podNode := raw.(map[string]any)
			if podNode["boundaryID"] != id || podNode["type"] != "Pod" || !managedExecutorTemplate(podNode["templateName"]) {
				continue
			}
			mainNodes++
			// Prove this executor node is also present in the independently read
			// frozen Run. Traversing children would follow downstream tasks.
			members := 0
			for _, rawTask := range tasks {
				if rawTask.RunID != task.RunID {
					return nil, biz.ErrRuntimeConflict
				}
				for _, ref := range rawTask.ChildTasks {
					if ref.TaskID == "" && ref.PodName == podID {
						members++
					}
				}
			}
			if members != 1 {
				return nil, biz.ErrRuntimeNotReady
			}
			matches := 0
			association := biz.ManagedStepAssociation{WorkflowName: workflow.GetName(), WorkflowUID: string(workflow.GetUID())}
			for i := range pods {
				pod := &pods[i]
				if !workflowOwner(pod, association) {
					continue
				}
				annotations := pod.GetAnnotations()
				if annotations["workflows.argoproj.io/node-id"] != podID {
					continue
				}
				if annotations["workflows.argoproj.io/node-name"] != podNode["name"] || pod.GetUID() == "" || pod.GetNamespace() != workflow.GetNamespace() || pod.GetAPIVersion() != "v1" || pod.GetKind() != "Pod" {
					return nil, biz.ErrRuntimeConflict
				}
				matches++
				resolved.PodName = pod.GetName()
			}
			if matches != 1 {
				return nil, biz.ErrRuntimeNotReady
			}
		}
		if mainNodes > 1 || (state == "SKIPPED" && mainNodes != 0) || (terminalTask(state) && mainNodes != 1) {
			return nil, biz.ErrRuntimeNotReady
		}
		result = append(result, resolved)
	}
	if len(result) == 0 {
		return nil, biz.ErrRuntimeNotReady
	}
	return result, nil
}

func managedExecutorTemplate(name any) bool {
	// KFP wraps the executor template when the frozen platform spec supplies
	// the two admission labels. Match the observed templates exactly; the
	// independent Run node membership and current Workflow/Pod checks below
	// still establish authority, never a template prefix or a Pod name.
	return name == "retry-system-container-impl" || name == "metadata-0-2-retry-system-container-impl" || name == "metadata-1-2-retry-system-container-impl"
}

// KFP 2.16 derives task details from Argo displayName and node children. Those
// references are DAG successors as well as executor nodes, not direct Pod names.
func taskControllerNode(task kfp.ManagedTask, nodes map[string]any) (string, map[string]any, error) {
	var id string
	var selected map[string]any
	for nodeID, raw := range nodes {
		node := raw.(map[string]any)
		if node["displayName"] != task.Name {
			continue
		}
		children, ok := node["children"].([]any)
		if node["children"] != nil && !ok {
			return "", nil, biz.ErrRuntimeNotReady
		}
		if len(children) != len(task.ChildTasks) {
			continue
		}
		matched := true
		seen := make(map[string]bool)
		for _, child := range children {
			name, ok := child.(string)
			if !ok || name == "" || seen[name] || nodes[name] == nil {
				matched = false
				break
			}
			seen[name] = true
		}
		for _, ref := range task.ChildTasks {
			if ref.TaskID != "" || ref.PodName == "" || !seen[ref.PodName] {
				matched = false
				break
			}
			delete(seen, ref.PodName)
		}
		if !matched || len(seen) != 0 {
			continue
		}
		if id != "" {
			return "", nil, biz.ErrRuntimeConflict
		}
		id, selected = nodeID, node
	}
	if id == "" {
		return "", nil, biz.ErrRuntimeNotReady
	}
	return id, selected, nil
}

func controllerTaskState(phase string) string {
	switch phase {
	case "Succeeded":
		return "SUCCEEDED"
	case "Failed", "Error":
		return "FAILED"
	case "Skipped", "Omitted":
		return "SKIPPED"
	case "Pending":
		return "PENDING"
	case "Running":
		return "RUNNING"
	}
	return ""
}

func closeTaskForPod(tasks []kfp.ManagedTask, pod string) (kfp.ManagedTask, error) {
	var result kfp.ManagedTask
	count := 0
	for _, task := range tasks {
		if task.PodName == pod && (task.Name == "close" || task.Name == "close-finalizer") {
			result = task
			count++
		}
	}
	if count != 1 || result.ID == "" || strings.TrimSpace(result.State) == "" {
		return kfp.ManagedTask{}, biz.ErrRuntimeConflict
	}
	return result, nil
}

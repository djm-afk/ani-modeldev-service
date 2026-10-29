package runtimeproof

import (
	"context"
	"strings"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (v *Verifier) currentTasks(ctx context.Context, execution biz.Execution, association biz.ManagedStepAssociation) ([]kfp.ManagedTask, error) {
	if v == nil || v.kube == nil || v.runs == nil || v.plans == nil || ctx == nil || ctx.Err() != nil {
		return nil, biz.ErrRuntimeNotReady
	}
	if _, _, err := execution.CanonicalPayloads(); err != nil {
		return nil, biz.ErrRuntimeConflict
	}
	plan, err := v.plans.Get(ctx, execution.TenantID, execution.ExecutionID)
	if err != nil {
		return nil, err
	}
	if plan.Plan.TenantID != execution.TenantID || plan.Plan.ExecutionID != execution.ExecutionID || plan.Plan.OperationID != execution.OperationID || plan.Plan.SpecHash != execution.SpecHash {
		return nil, biz.ErrRuntimeConflict
	}
	return v.tasksForPlanKind(ctx, plan.Plan, association, execution.Snapshot.Kind)
}

// VerifyManagedRun combines authenticated KFP membership with current controller
// node and Pod ownership. KFP child references alone are not Pod identities.
func (v *Verifier) VerifyManagedRun(ctx context.Context, plan biz.PipelineDispatchPlan, association biz.ManagedStepAssociation, taskName string) error {
	_, err := v.ResolveManagedTaskID(ctx, plan, association, taskName)
	return err
}

func (v *Verifier) ResolveManagedTaskID(ctx context.Context, plan biz.PipelineDispatchPlan, association biz.ManagedStepAssociation, taskName string) (string, error) {
	kind := "GENERAL_TRAINING"
	if taskName == "export" || taskName == "verify" || taskName == "register" {
		kind = "FINETUNING"
	}
	tasks, err := v.tasksForPlanRole(ctx, plan, association, kind, taskName)
	if err != nil {
		return "", err
	}
	var task kfp.ManagedTask
	if taskName == "close" {
		task, err = closeTaskForPod(tasks, association.PodName)
	} else {
		task, err = oneTask(tasks, taskName)
	}
	if err != nil || task.RunID != association.RunID || task.PodName != association.PodName {
		return "", biz.ErrRuntimeConflict
	}
	pod, err := v.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(association.NamespaceName).Get(ctx, association.PodName, metav1.GetOptions{})
	if err != nil || !sameObject(pod, "v1", "Pod", association.NamespaceName, association.PodName, association.PodUID) || pod.GetDeletionTimestamp() != nil || !workflowOwner(pod, association) {
		return "", biz.ErrRuntimeNotReady
	}
	phase, _ := textAt(pod, "status", "phase")
	reason, _ := textAt(pod, "status", "reason")
	if phase == "Failed" && reason == "Evicted" {
		// The owner can retain post-termination absence evidence. An evicted
		// Pod cannot use that evidence as a current managed caller identity.
		return "", biz.ErrRuntimeNotReady
	}
	account, _ := textAt(pod, "spec", "serviceAccountName")
	if account != plan.Environment.Identities.KFPStepServiceAccount {
		return "", biz.ErrRuntimeConflict
	}
	return task.ID, nil
}

func (v *Verifier) tasksForPlan(ctx context.Context, plan biz.PipelineDispatchPlan, association biz.ManagedStepAssociation) ([]kfp.ManagedTask, error) {
	return v.tasksForPlanKind(ctx, plan, association, "GENERAL_TRAINING")
}

func (v *Verifier) tasksForPlanKind(ctx context.Context, plan biz.PipelineDispatchPlan, association biz.ManagedStepAssociation, kind string) ([]kfp.ManagedTask, error) {
	return v.tasksForPlanRole(ctx, plan, association, kind, "")
}

func (v *Verifier) tasksForPlanRole(ctx context.Context, plan biz.PipelineDispatchPlan, association biz.ManagedStepAssociation, kind, role string) ([]kfp.ManagedTask, error) {
	if v == nil || v.kube == nil || v.runs == nil || ctx == nil || ctx.Err() != nil {
		return nil, biz.ErrRuntimeNotReady
	}
	environment := plan.Environment
	if association.NamespaceName != environment.NamespaceName || association.NamespaceUID != environment.NamespaceUID || association.WorkflowName == "" || association.WorkflowUID == "" {
		return nil, biz.ErrRuntimeConflict
	}
	namespace, err := v.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, environment.NamespaceName, metav1.GetOptions{})
	if err != nil || !sameObject(namespace, "v1", "Namespace", "", environment.NamespaceName, environment.NamespaceUID) || namespace.GetDeletionTimestamp() != nil {
		return nil, biz.ErrRuntimeNotReady
	}
	workflow, err := v.kube.Resource(schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}).Namespace(environment.NamespaceName).Get(ctx, association.WorkflowName, metav1.GetOptions{})
	if err != nil || !sameObject(workflow, "argoproj.io/v1alpha1", "Workflow", environment.NamespaceName, association.WorkflowName, association.WorkflowUID) || workflow.GetDeletionTimestamp() != nil {
		return nil, biz.ErrRuntimeNotReady
	}
	tasks, err := v.runs.GetManagedTasks(ctx, plan, association.RunID)
	if err != nil {
		return nil, err
	}
	pods, err := v.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(environment.NamespaceName).List(ctx, metav1.ListOptions{})
	if err != nil || pods == nil || pods.GetContinue() != "" {
		return nil, biz.ErrRuntimeNotReady
	}
	return resolvedManagedTasksForRole(workflow, tasks, pods.Items, kind, role)
}

func oneTask(tasks []kfp.ManagedTask, name string) (kfp.ManagedTask, error) {
	var result kfp.ManagedTask
	count := 0
	for _, task := range tasks {
		if task.Name == name {
			count++
			result = task
		}
	}
	if count != 1 || result.ID == "" || (result.PodName == "" && result.State != "SKIPPED") || result.State == "" {
		return kfp.ManagedTask{}, biz.ErrRuntimeNotReady
	}
	for _, task := range tasks {
		if task.Name != name && (task.ID == result.ID || (result.PodName != "" && task.PodName == result.PodName)) {
			return kfp.ManagedTask{}, biz.ErrRuntimeConflict
		}
	}
	return result, nil
}

func (v *Verifier) currentTaskPod(ctx context.Context, execution biz.Execution, association biz.ManagedStepAssociation, task kfp.ManagedTask, expectedUID string) (*unstructured.Unstructured, error) {
	pod, err := v.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(association.NamespaceName).Get(ctx, task.PodName, metav1.GetOptions{})
	if err != nil {
		return nil, biz.ErrRuntimeNotReady
	}
	account, _ := textAt(pod, "spec", "serviceAccountName")
	if pod.GetAPIVersion() != "v1" || pod.GetKind() != "Pod" || pod.GetNamespace() != association.NamespaceName || pod.GetName() != task.PodName || pod.GetUID() == "" || (expectedUID != "" && string(pod.GetUID()) != expectedUID) || account != execution.Snapshot.Environment.Identities.KFPStepServiceAccount || !workflowOwner(pod, association) {
		return nil, biz.ErrRuntimeConflict
	}
	return pod, nil
}

func sameObject(object *unstructured.Unstructured, apiVersion, kind, namespace, name, uid string) bool {
	return object != nil && object.GetAPIVersion() == apiVersion && object.GetKind() == kind && object.GetNamespace() == namespace && object.GetName() == name && string(object.GetUID()) == uid
}

func workflowOwner(pod *unstructured.Unstructured, association biz.ManagedStepAssociation) bool {
	count := 0
	for _, owner := range pod.GetOwnerReferences() {
		if owner.Controller == nil || !*owner.Controller {
			continue
		}
		count++
		if owner.APIVersion != "argoproj.io/v1alpha1" || owner.Kind != "Workflow" || owner.Name != association.WorkflowName || string(owner.UID) != association.WorkflowUID {
			return false
		}
	}
	return count == 1
}

func preparedMount(pod *unstructured.Unstructured, pvc string) bool {
	volumes, _, err := unstructured.NestedSlice(pod.Object, "spec", "volumes")
	if err != nil {
		return false
	}
	volume := ""
	for _, item := range volumes {
		object, ok := item.(map[string]any)
		if !ok {
			return false
		}
		name, _, _ := unstructured.NestedString(object, "persistentVolumeClaim", "claimName")
		if name == pvc {
			if volume != "" {
				return false
			}
			volume, _ = object["name"].(string)
		}
		if name != "" && name != pvc {
			return false
		}
		if _, hasHost, _ := unstructured.NestedFieldNoCopy(object, "hostPath"); hasHost {
			return false
		}
	}
	containers, _, err := unstructured.NestedSlice(pod.Object, "spec", "containers")
	if err != nil || volume == "" {
		return false
	}
	mainCount := 0
	for _, item := range containers {
		container, ok := item.(map[string]any)
		if !ok || container["name"] != "main" {
			continue
		}
		mainCount++
		mounts, _, err := unstructured.NestedSlice(container, "volumeMounts")
		if err != nil {
			return false
		}
		expected := map[string]string{"/workspace": "", "/workspace/work/prepare/tmp": "work/prepare/tmp"}
		seen := map[string]bool{}
		for _, item := range mounts {
			mount, ok := item.(map[string]any)
			if !ok {
				return false
			}
			path, _ := mount["mountPath"].(string)
			if mount["name"] != volume {
				if path == "/workspace" || strings.HasPrefix(path, "/workspace/") {
					return false
				}
				continue
			}
			subpath, allowed := expected[path]
			if !allowed || seen[path] || mount["readOnly"] == true ||
				mount["subPathExpr"] != nil && mount["subPathExpr"] != "" ||
				mount["subPath"] != subpath && !(subpath == "" && mount["subPath"] == nil) {
				return false
			}
			seen[path] = true
		}
		if len(seen) != len(expected) {
			return false
		}
	}
	return mainCount == 1
}

func controlOnly(pod *unstructured.Unstructured) bool {
	if pod == nil {
		return false
	}
	volumes, _, err := unstructured.NestedSlice(pod.Object, "spec", "volumes")
	if err != nil {
		return false
	}
	for _, item := range volumes {
		volume, ok := item.(map[string]any)
		if !ok {
			return false
		}
		for source := range volume {
			switch source {
			case "name", "emptyDir", "projected", "secret", "configMap", "downwardAPI":
			default:
				return false
			}
		}
	}
	return true
}

func terminatedPod(pod *unstructured.Unstructured) (bool, bool, time.Time, *int32) {
	phase, _ := textAt(pod, "status", "phase")
	if phase != "Succeeded" && phase != "Failed" {
		return false, false, time.Time{}, nil
	}
	containers, found, err := unstructured.NestedSlice(pod.Object, "spec", "containers")
	if err != nil || !found || len(containers) == 0 {
		return false, false, time.Time{}, nil
	}
	allZero := true
	var completed time.Time
	var mainCode *int32
	for _, kind := range []struct{ spec, status string }{{"containers", "containerStatuses"}, {"initContainers", "initContainerStatuses"}, {"ephemeralContainers", "ephemeralContainerStatuses"}} {
		containers, _, err := unstructured.NestedSlice(pod.Object, "spec", kind.spec)
		if err != nil {
			return false, false, time.Time{}, nil
		}
		statuses, _, err := unstructured.NestedSlice(pod.Object, "status", kind.status)
		if err != nil || len(statuses) != len(containers) {
			return false, false, time.Time{}, nil
		}
		seen := make(map[string]bool)
		for _, item := range statuses {
			status, ok := item.(map[string]any)
			if !ok {
				return false, false, time.Time{}, nil
			}
			name, ok := status["name"].(string)
			if !ok || seen[name] {
				return false, false, time.Time{}, nil
			}
			seen[name] = true
			known := false
			for _, item := range containers {
				container, ok := item.(map[string]any)
				if ok && container["name"] == name {
					known = true
				}
			}
			if !known {
				return false, false, time.Time{}, nil
			}
			state, _, err := unstructured.NestedMap(status, "state")
			if err != nil || len(state) != 1 {
				return false, false, time.Time{}, nil
			}
			code, found, err := unstructured.NestedInt64(state, "terminated", "exitCode")
			if err != nil || !found || code < -2147483648 || code > 2147483647 {
				return false, false, time.Time{}, nil
			}
			finished, _, _ := unstructured.NestedString(state, "terminated", "finishedAt")
			at, err := time.Parse(time.RFC3339Nano, finished)
			if err != nil || at.IsZero() {
				return false, false, time.Time{}, nil
			}
			allZero = allZero && code == 0
			if kind.spec == "containers" && name == "main" {
				value := int32(code)
				mainCode = &value
				completed = at
			}
		}
	}
	return true, allZero, completed, mainCode
}

func podFact(pod *unstructured.Unstructured, workflowUID string, exit int32) biz.RuntimeResource {
	return biz.RuntimeResource{APIVersion: "v1", Kind: "Pod", Namespace: pod.GetNamespace(), Name: pod.GetName(), UID: string(pod.GetUID()), OwnerUID: workflowUID, APIObjectPresent: true, Terminal: true, ExitCode: &exit}
}

func terminalTask(state string) bool {
	return state == "SUCCEEDED" || state == "FAILED" || state == "CANCELED"
}

func textAt(object *unstructured.Unstructured, fields ...string) (string, bool) {
	if object == nil {
		return "", false
	}
	text, found, err := unstructured.NestedString(object.Object, fields...)
	return text, found && err == nil
}

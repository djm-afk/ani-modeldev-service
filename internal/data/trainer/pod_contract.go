package trainer

import (
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ValidatePodResources checks the complete effective request for the current
// single-worker capability. An additional container, Pod-level request or
// RuntimeClass overhead requires an explicit future compiler/defaulting rule.
func ValidatePodResources(expected map[string]string, pod *unstructured.Unstructured) error {
	if pod == nil || pod.GetKind() != "Pod" || pod.GetAPIVersion() != "v1" {
		return biz.ErrRuntimeConflict
	}
	for _, field := range []string{"initContainers", "ephemeralContainers", "resourceClaims"} {
		values, _, err := unstructured.NestedSlice(pod.Object, "spec", field)
		if err != nil || len(values) != 0 {
			return biz.ErrRuntimeConflict
		}
	}
	for _, field := range []string{"overhead", "resources"} {
		values, _, err := unstructured.NestedMap(pod.Object, "spec", field)
		if err != nil || len(values) != 0 {
			return biz.ErrRuntimeConflict
		}
	}
	containers, _, err := unstructured.NestedSlice(pod.Object, "spec", "containers")
	if err != nil || len(containers) != 1 {
		return biz.ErrRuntimeConflict
	}
	container, ok := containers[0].(map[string]any)
	if !ok || container["name"] != "node" {
		return biz.ErrRuntimeConflict
	}
	resources, ok := container["resources"].(map[string]any)
	if !ok || len(resources) != 2 {
		return biz.ErrRuntimeConflict
	}
	if values, _, err := unstructured.NestedSlice(container, "envFrom"); err != nil || len(values) != 0 {
		return biz.ErrRuntimeConflict
	}
	environment, _, err := unstructured.NestedSlice(container, "env")
	if err != nil || len(environment) > 1 {
		return biz.ErrRuntimeConflict
	}
	if len(environment) == 1 {
		entry, ok := environment[0].(map[string]any)
		if !ok || entry["name"] != "JOB_COMPLETION_INDEX" || len(entry) != 2 {
			return biz.ErrRuntimeConflict
		}
		field, found, err := unstructured.NestedMap(entry, "valueFrom", "fieldRef")
		if err != nil || !found || field["fieldPath"] != "metadata.labels['batch.kubernetes.io/job-completion-index']" || field["apiVersion"] != "v1" || len(field) != 2 {
			return biz.ErrRuntimeConflict
		}
	}
	for _, field := range []string{"requests", "limits"} {
		actual, ok := resources[field].(map[string]any)
		if !ok || !sameResourceVector(expected, actual) {
			return biz.ErrRuntimeConflict
		}
	}
	return nil
}

func sameResourceVector(expected map[string]string, actual map[string]any) bool {
	if len(expected) < 2 || len(actual) != len(expected) || expected["cpu"] == "" || expected["memory"] == "" {
		return false
	}
	for key, value := range expected {
		wanted, err := resource.ParseQuantity(value)
		text, ok := actual[key].(string)
		if err != nil || wanted.Sign() <= 0 || !ok {
			return false
		}
		got, err := resource.ParseQuantity(text)
		if err != nil || wanted.Cmp(got) != 0 {
			return false
		}
	}
	return true
}

func validatePodGroup(plan biz.TrainingPlan, handle biz.TrainingHandle, group *unstructured.Unstructured) error {
	if group == nil || group.GetAPIVersion() != "scheduling.volcano.sh/v1beta1" || group.GetKind() != "PodGroup" || group.GetNamespace() != plan.Workspace.NamespaceName || group.GetName() != plan.Name || group.GetUID() == "" || !controllerIs(group, "trainer.kubeflow.org/v1alpha1", "TrainJob", plan.Name, handle.TrainJobUID) {
		return biz.ErrRuntimeConflict
	}
	minimum, _, err := unstructured.NestedInt64(group.Object, "spec", "minMember")
	if err != nil || minimum != 1 {
		return biz.ErrRuntimeConflict
	}
	actual, _, err := unstructured.NestedMap(group.Object, "spec", "minResources")
	expected, vectorErr := biz.TrainingResourceVector(plan.Snapshot)
	queue, _, queueErr := unstructured.NestedString(group.Object, "spec", "queue")
	wantedQueue := "default"
	if plan.Snapshot.GPU != nil {
		wantedQueue = plan.Snapshot.GPU.Execution.QueueName
	}
	if err != nil || vectorErr != nil || queueErr != nil || queue != wantedQueue || !sameResourceVector(expected, actual) {
		return biz.ErrRuntimeConflict
	}
	return nil
}

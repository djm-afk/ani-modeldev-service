package trainer

import (
	"strings"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	kjson "k8s.io/apimachinery/pkg/util/json"
	"reflect"
)

// ValidatePodTemplate checks the effective executable, workspace and isolation
// after all admission mutations. Kubernetes coordination fields are handled
// separately by ValidatePodResources; they cannot replace the frozen program.
func ValidatePodTemplate(expected corev1.PodSpec, actual corev1.PodSpec) error {
	if len(expected.Containers) != 1 || len(actual.Containers) != 1 {
		return biz.ErrRuntimeConflict
	}
	wanted, worker := expected.Containers[0], actual.Containers[0]
	if worker.Name != wanted.Name || worker.Image != wanted.Image || !reflect.DeepEqual(worker.Command, wanted.Command) ||
		!reflect.DeepEqual(worker.Args, wanted.Args) || worker.WorkingDir != wanted.WorkingDir ||
		!sameTrainingEnvironment(wanted.Env, worker.Env) || !reflect.DeepEqual(worker.EnvFrom, wanted.EnvFrom) ||
		!reflect.DeepEqual(worker.VolumeMounts, wanted.VolumeMounts) || !reflect.DeepEqual(worker.SecurityContext, wanted.SecurityContext) ||
		!reflect.DeepEqual(worker.Lifecycle, wanted.Lifecycle) || !reflect.DeepEqual(worker.LivenessProbe, wanted.LivenessProbe) ||
		!reflect.DeepEqual(worker.ReadinessProbe, wanted.ReadinessProbe) || !reflect.DeepEqual(worker.StartupProbe, wanted.StartupProbe) ||
		worker.Stdin != wanted.Stdin || worker.StdinOnce != wanted.StdinOnce || worker.TTY != wanted.TTY ||
		!reflect.DeepEqual(actual.Volumes, expected.Volumes) || !reflect.DeepEqual(actual.SecurityContext, expected.SecurityContext) ||
		!trainingPullSecretsCovered(expected.ImagePullSecrets, actual.ImagePullSecrets) ||
		actual.ServiceAccountName != expected.ServiceAccountName || actual.SchedulerName != expected.SchedulerName ||
		!reflect.DeepEqual(actual.RuntimeClassName, expected.RuntimeClassName) || !reflect.DeepEqual(actual.NodeSelector, expected.NodeSelector) ||
		actual.RestartPolicy != corev1.RestartPolicyNever || actual.AutomountServiceAccountToken == nil || *actual.AutomountServiceAccountToken ||
		actual.HostNetwork || actual.HostPID || actual.HostIPC {
		return biz.ErrRuntimeConflict
	}
	return nil
}

// trainingPullSecretsCovered accepts the frozen references plus whatever the API
// server copied from the Pod's ServiceAccount. A Pod that declares no
// imagePullSecrets of its own inherits the ServiceAccount's registry credential
// at admission, so an empty expectation must still accept that single
// credential; the finetuning template deliberately carries none. Any reference
// outside the expected set that is not a ServiceAccount inheritance is still a
// real substitution and stays rejected. The adapter holds no ServiceAccount read
// permission, so inheritance is recognised by its platform-prefixed name rather
// than by reading the account.
func trainingPullSecretsCovered(expected, actual []corev1.LocalObjectReference) bool {
	if len(expected) == 0 {
		for _, reference := range actual {
			if !serviceAccountPullSecret(reference.Name) {
				return false
			}
		}
		return true
	}
	if len(actual) < len(expected) {
		return false
	}
	for _, want := range expected {
		matched := false
		for _, got := range actual {
			if got.Name == want.Name {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// serviceAccountPullSecret recognises the tenant registry credential a namespace
// ServiceAccount carries for every managed workload. The shared tenant Secret the
// plan freezes uses the ani-pull- prefix and is never mistaken for it.
func serviceAccountPullSecret(name string) bool {
	return name != "" && strings.HasPrefix(name, "ani-registry-pull")
}

func validateEffectiveTrainingPod(plan biz.TrainingPlan, pod *unstructured.Unstructured) error {
	var spec map[string]any
	if kjson.Unmarshal(plan.Snapshot.RuntimePlan.ExpectedSpec, &spec) != nil {
		return biz.ErrRuntimeConflict
	}
	jobs, _, err := unstructured.NestedSlice(spec, "template", "spec", "replicatedJobs")
	if err != nil || len(jobs) != 1 {
		return biz.ErrRuntimeConflict
	}
	job, ok := jobs[0].(map[string]any)
	if !ok {
		return biz.ErrRuntimeConflict
	}
	wanted, found, err := unstructured.NestedMap(job, "template", "spec", "template", "spec")
	if err != nil || !found {
		return biz.ErrRuntimeConflict
	}
	var expected corev1.PodSpec
	var actual corev1.Pod
	if runtime.DefaultUnstructuredConverter.FromUnstructured(wanted, &expected) != nil ||
		runtime.DefaultUnstructuredConverter.FromUnstructured(pod.Object, &actual) != nil || ValidatePodTemplate(expected, actual.Spec) != nil {
		return biz.ErrRuntimeConflict
	}
	annotations, _, err := unstructured.NestedStringMap(job, "template", "spec", "template", "metadata", "annotations")
	if err != nil {
		return biz.ErrRuntimeConflict
	}
	for key, wanted := range annotations {
		if pod.GetAnnotations()[key] != wanted {
			return biz.ErrRuntimeConflict
		}
	}
	return nil
}

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
	if err != nil {
		return biz.ErrRuntimeConflict
	}
	var actualEnv []corev1.EnvVar
	for _, value := range environment {
		entry, ok := value.(map[string]any)
		if !ok || len(entry) != 2 {
			return biz.ErrRuntimeConflict
		}
		if entry["name"] == "JOB_COMPLETION_INDEX" {
			source, ok := entry["valueFrom"].(map[string]any)
			if !ok || len(source) != 1 {
				return biz.ErrRuntimeConflict
			}
			field, ok := source["fieldRef"].(map[string]any)
			if !ok || len(field) != 2 {
				return biz.ErrRuntimeConflict
			}
		} else {
			if _, ok := entry["value"].(string); !ok {
				return biz.ErrRuntimeConflict
			}
		}
		var variable corev1.EnvVar
		if runtime.DefaultUnstructuredConverter.FromUnstructured(entry, &variable) != nil {
			return biz.ErrRuntimeConflict
		}
		actualEnv = append(actualEnv, variable)
	}
	// Resource validation also rejects environment injection; the executable
	// comparison independently checks the frozen literal values.
	allowed := map[string]string{"TMPDIR": "/workspace/work/train/tmp", "HF_HOME": "/workspace/work/train/hf", "HF_DATASETS_CACHE": "/workspace/work/train/hf/datasets", "MPLCONFIGDIR": "/workspace/work/train/matplotlib"}
	seen := map[string]bool{}
	for _, entry := range actualEnv {
		if seen[entry.Name] {
			return biz.ErrRuntimeConflict
		}
		seen[entry.Name] = true
		if entry.Name == "JOB_COMPLETION_INDEX" {
			if !completionIndexEnvironment(entry) {
				return biz.ErrRuntimeConflict
			}
			continue
		}
		wanted, ok := allowed[entry.Name]
		if !ok || entry.Value != wanted || entry.ValueFrom != nil {
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

// Job controller may append only its standard downward-API coordination
// variable. All application environment must equal the immutable Runtime plan.
func completionIndexEnvironment(value corev1.EnvVar) bool {
	return value.Name == "JOB_COMPLETION_INDEX" && value.Value == "" && value.ValueFrom != nil && value.ValueFrom.FieldRef != nil &&
		value.ValueFrom.FieldRef.APIVersion == "v1" && value.ValueFrom.FieldRef.FieldPath == "metadata.labels['batch.kubernetes.io/job-completion-index']" &&
		value.ValueFrom.ResourceFieldRef == nil && value.ValueFrom.ConfigMapKeyRef == nil && value.ValueFrom.SecretKeyRef == nil
}

func sameTrainingEnvironment(expected, actual []corev1.EnvVar) bool {
	filtered := make([]corev1.EnvVar, 0, len(actual))
	coordination := false
	for _, variable := range actual {
		if variable.Name == "JOB_COMPLETION_INDEX" {
			if coordination || !completionIndexEnvironment(variable) {
				return false
			}
			coordination = true
		} else {
			filtered = append(filtered, variable)
		}
	}
	if len(expected) != len(filtered) {
		return false
	}
	for i := range expected {
		if !reflect.DeepEqual(expected[i], filtered[i]) {
			return false
		}
	}
	return true
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

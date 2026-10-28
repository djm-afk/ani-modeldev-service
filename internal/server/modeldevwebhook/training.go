package modeldevwebhook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

type trainingReader interface {
	TrainingObject(context.Context, string, string, string) (*unstructured.Unstructured, error)
}

func (handler *Handler) admitTraining(ctx context.Context, request *admissionv1.AdmissionRequest, pod *corev1.Pod, raw map[string]any, binding NamespaceBinding, failure *admissionFailure) (*admissionv1.AdmissionResponse, string) {
	reader, ok := handler.reader.(trainingReader)
	if !ok {
		return unavailable(request)
	}
	if request.UserInfo.Username != binding.Training.Creator || request.Kind.Kind != "Pod" || request.Kind.Group != "" || request.Kind.Version != "v1" || pod.Kind != "Pod" || pod.APIVersion != "v1" || pod.Namespace != binding.Namespace || len(pod.OwnerReferences) != 1 {
		return denied(request, "training_identity")
	}
	dependency, cancel := context.WithTimeout(ctx, handler.dependencyTimeout)
	defer cancel()
	started := time.Now()
	namespace, err := handler.reader.Namespace(dependency, binding.Namespace)
	if err != nil || namespace == nil {
		failure.record("namespace", started, dependency, err)
		return unavailable(request)
	}
	if namespace.Name != binding.Namespace || string(namespace.UID) != binding.NamespaceUID || namespace.DeletionTimestamp != nil || namespace.Labels[binding.OwnerLabel.Key] != binding.OwnerLabel.Value {
		return denied(request, "namespace_identity")
	}
	owner := pod.OwnerReferences[0]
	var train *unstructured.Unstructured
	for _, entry := range []struct{ api, kind string }{{"batch/v1", "Job"}, {"jobset.x-k8s.io/v1alpha2", "JobSet"}, {"trainer.kubeflow.org/v1alpha1", "TrainJob"}} {
		if owner.APIVersion != entry.api || owner.Kind != entry.kind || owner.Controller == nil || !*owner.Controller || owner.Name == "" || owner.UID == "" {
			return denied(request, "training_owner")
		}
		started = time.Now()
		object, readErr := reader.TrainingObject(dependency, binding.Namespace, entry.kind, owner.Name)
		if readErr != nil || object == nil {
			failure.record("training_owner", started, dependency, readErr)
			return unavailable(request)
		}
		if object.GetAPIVersion() != entry.api || object.GetKind() != entry.kind || object.GetNamespace() != binding.Namespace || object.GetName() != owner.Name || object.GetUID() != owner.UID || object.GetDeletionTimestamp() != nil {
			return denied(request, "training_owner")
		}
		if entry.kind == "TrainJob" {
			train = object
			break
		}
		refs := object.GetOwnerReferences()
		if len(refs) != 1 {
			return denied(request, "training_owner")
		}
		owner = refs[0]
	}
	annotations := train.GetAnnotations()
	execution := annotations["modeldev.ani.io/execution-id"]
	if !canonicalUUID(execution) || !canonicalUUID(annotations["modeldev.ani.io/tenant-id"]) || !sha256Pattern.MatchString(annotations["modeldev.ani.io/execution-spec-sha256"]) || train.GetName() != "md-"+execution {
		return denied(request, "training_plan")
	}
	// Worker and step Pods must report the same tenant. The TrainJob annotation is
	// the execution's own claim; the namespace label is the platform's. They can
	// only agree for the tenant that owns this namespace.
	if namespace.Labels[NamespaceTenantLabel] != annotations["modeldev.ani.io/tenant-id"] {
		return denied(request, "namespace_identity")
	}
	name, _, _ := unstructured.NestedString(train.Object, "spec", "runtimeRef", "name")
	kind, _, _ := unstructured.NestedString(train.Object, "spec", "runtimeRef", "kind")
	api, _, _ := unstructured.NestedString(train.Object, "spec", "runtimeRef", "apiGroup")
	if kind != "TrainingRuntime" || api != "trainer.kubeflow.org" || name != cpup01.RuntimeName(binding.NamespaceUID, execution) {
		return denied(request, "training_plan")
	}
	started = time.Now()
	template, err := reader.TrainingObject(dependency, binding.Namespace, "TrainingRuntime", name)
	if err != nil || template == nil {
		failure.record("training_runtime", started, dependency, err)
		return unavailable(request)
	}
	if template.GetAPIVersion() != "trainer.kubeflow.org/v1alpha1" || template.GetKind() != "TrainingRuntime" || template.GetNamespace() != binding.Namespace || template.GetName() != name || string(template.GetUID()) != annotations["modeldev.ani.io/runtime-uid"] || template.GetDeletionTimestamp() != nil || len(template.GetOwnerReferences()) != 0 {
		return denied(request, "training_plan")
	}
	for _, key := range []string{"modeldev.ani.io/tenant-id", "modeldev.ani.io/execution-id", "modeldev.ani.io/create-operation-id", "modeldev.ani.io/execution-spec-sha256", "modeldev.ani.io/runtime-spec-sha256", "modeldev.ani.io/workspace-uid"} {
		if annotations[key] == "" || template.GetAnnotations()[key] != annotations[key] {
			return denied(request, "training_plan")
		}
	}
	spec, found, err := unstructured.NestedMap(template.Object, "spec")
	if err != nil || !found {
		return denied(request, "training_plan")
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(spec) != nil {
		return denied(request, "training_plan")
	}
	digest := sha256.Sum256(bytes.TrimSuffix(buffer.Bytes(), []byte("\n")))
	if hex.EncodeToString(digest[:]) != annotations["modeldev.ani.io/runtime-spec-sha256"] {
		return denied(request, "training_plan")
	}
	jobs, _, _ := unstructured.NestedSlice(spec, "template", "spec", "replicatedJobs")
	if len(jobs) != 1 {
		return denied(request, "training_plan")
	}
	job, ok := jobs[0].(map[string]any)
	if !ok {
		return denied(request, "training_plan")
	}
	expected, found, err := unstructured.NestedMap(job, "template", "spec", "template", "spec")
	if err != nil || !found {
		return denied(request, "training_plan")
	}
	var expectedPod corev1.PodSpec
	if runtime.DefaultUnstructuredConverter.FromUnstructured(expected, &expectedPod) != nil || len(expectedPod.Containers) != 1 || expectedPod.ServiceAccountName != binding.Training.ServiceAccount {
		return denied(request, "training_plan")
	}
	vector := map[string]string{}
	for key, value := range expectedPod.Containers[0].Resources.Requests {
		vector[string(key)] = value.String()
	}
	actual := &unstructured.Unstructured{Object: raw}
	if trainer.ValidatePodResources(vector, actual) != nil {
		return denied(request, "training_resources")
	}
	if trainer.ValidatePodTemplate(expectedPod, pod.Spec) != nil {
		return denied(request, "training_spec")
	}
	expectedAnnotations, _, _ := unstructured.NestedStringMap(job, "template", "spec", "template", "metadata", "annotations")
	for key, value := range expectedAnnotations {
		if pod.Annotations[key] != value {
			return denied(request, "training_spec")
		}
	}
	expectedFinalizers, _, _ := unstructured.NestedStringSlice(job, "template", "spec", "template", "metadata", "finalizers")
	if len(expectedFinalizers) != 1 || expectedFinalizers[0] != "modeldev.ani.io/training-exit-evidence" || !slices.Contains(pod.Finalizers, expectedFinalizers[0]) {
		return denied(request, "training_retention")
	}
	// Metric labels come from the verified TrainJob annotations, never from the
	// request. A requested label with a different value is replaced, so it cannot
	// attribute this Pod to another execution or tenant.
	builder := patchBuilder{object: raw}
	for key, value := range map[string]string{TenantLabel: annotations["modeldev.ani.io/tenant-id"], InstanceLabel: execution, WorkloadKindLabel: WorkloadKindTrainer} {
		builder.set(value, "metadata", "labels", key)
	}
	response := &admissionv1.AdmissionResponse{UID: request.UID, Allowed: true}
	if len(builder.operations) != 0 {
		patch, err := json.Marshal(builder.operations)
		if err != nil {
			return denied(request, "patch_unavailable")
		}
		kind := admissionv1.PatchTypeJSONPatch
		response.Patch, response.PatchType = patch, &kind
		return response, "mutated"
	}
	return response, "training_verified"
}

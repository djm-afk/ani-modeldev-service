package modeldevwebhook

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
)

// IdentityReader reads only live namespace and original Workflow identities.
// An adapter must preserve context cancellation and may not substitute a newer
// Workflow with the same name. The webhook never reads ModelDev business state.
type IdentityReader interface {
	Namespace(context.Context, string) (*corev1.Namespace, error)
	Workflow(context.Context, string, string) (*unstructured.Unstructured, error)
}

type mountRule struct {
	subPath  string
	readOnly bool
}

func roleMounts(kind string) map[string]map[string]mountRule {
	roles := map[string]map[string]mountRule{
		"workspace-name": {}, "train-wait": {}, "finalize-close": {},
		"prepare": {"/workspace": {"", false}, "/workspace/work/prepare/tmp": {"work/prepare/tmp", false}},
		"close":   {"/workspace": {"", true}},
	}
	for _, role := range []string{"collect", "publish"} {
		roles[role] = map[string]mountRule{"/workspace": {"", true}, "/workspace/publication": {"publication", false}, "/workspace/work/" + role: {"work/" + role, false}}
	}
	if kind == "FINETUNING" {
		roles["register"] = map[string]mountRule{"/workspace": {"", true}}
		roles["export"] = map[string]mountRule{
			"/workspace/inputs": {"inputs", true}, "/workspace/training": {"training", true}, "/workspace/reports/train": {"reports/train", true}, "/workspace/export": {"export", false}, "/workspace/reports/export": {"reports/export", false}, "/workspace/work/export": {"work/export", false},
		}
		roles["verify"] = map[string]mountRule{
			"/workspace/inputs": {"inputs", true}, "/workspace/training": {"training", true}, "/workspace/reports/train": {"reports/train", true}, "/workspace/export": {"export", true}, "/workspace/reports/export": {"reports/export", true}, "/workspace/reports/verify": {"reports/verify", false}, "/workspace/work/verify": {"work/verify", false},
		}
	}
	return roles
}

func componentResourcesMatch(actual corev1.ResourceRequirements, expected cpup01.ComponentResources) bool {
	if len(actual.Claims) != 0 {
		return false
	}
	for _, pair := range []struct {
		actual               corev1.ResourceList
		cpu, memory, storage string
	}{{actual.Requests, expected.CPURequest, expected.MemoryRequest, expected.EphemeralStorageRequest}, {actual.Limits, expected.CPULimit, expected.MemoryLimit, expected.EphemeralStorageLimit}} {
		for key := range pair.actual {
			if key != corev1.ResourceCPU && key != corev1.ResourceMemory && key != corev1.ResourceEphemeralStorage {
				return false
			}
		}
		for key, text := range map[corev1.ResourceName]string{corev1.ResourceCPU: pair.cpu, corev1.ResourceMemory: pair.memory} {
			want, err := resource.ParseQuantity(text)
			got, ok := pair.actual[key]
			if err != nil || !ok || got.Cmp(want) != 0 {
				return false
			}
		}
		if got, ok := pair.actual[corev1.ResourceEphemeralStorage]; ok {
			want, err := resource.ParseQuantity(pair.storage)
			if err != nil || got.Cmp(want) != 0 {
				return false
			}
		}
	}
	return true
}

func componentWorkEnvironment(container corev1.Container, role string) bool {
	if role != "prepare" && role != "export" && role != "verify" && role != "collect" && role != "publish" {
		return true
	}
	work := "/workspace/work/" + role
	expected := map[string]string{"TMPDIR": work + "/tmp", "HF_HOME": work + "/hf", "HF_DATASETS_CACHE": work + "/hf/datasets", "MPLCONFIGDIR": work + "/matplotlib"}
	seen := map[string]bool{}
	if len(container.EnvFrom) > 1 {
		return false
	}
	for _, source := range container.EnvFrom {
		// The current KFP executor imports its optional metadata connection.
		// All work paths must still be explicit env entries, which take
		// precedence over envFrom; this source cannot supply a missing path.
		ref := source.ConfigMapRef
		if source.Prefix != "" || source.SecretRef != nil || ref == nil || ref.Name != "metadata-grpc-configmap" || ref.Optional == nil || !*ref.Optional {
			return false
		}
	}
	for _, entry := range container.Env {
		wanted, managed := expected[entry.Name]
		if !managed {
			continue
		}
		if seen[entry.Name] || entry.ValueFrom != nil || entry.Value != wanted {
			return false
		}
		seen[entry.Name] = true
	}
	return len(seen) == len(expected)
}

type patchOperation struct {
	Operation string `json:"op"`
	Path      string `json:"path"`
	Value     any    `json:"value,omitempty"`
}

// patchBuilder changes individual leaves in the original JSON. Unknown fields
// and other controllers' metadata, security settings and finalizers survive.
type patchBuilder struct {
	object     map[string]any
	operations []patchOperation
}

func escape(segment string) string {
	return strings.ReplaceAll(strings.ReplaceAll(segment, "~", "~0"), "/", "~1")
}

func (builder *patchBuilder) set(value any, segments ...string) {
	var current any = builder.object
	path := ""
	for i, segment := range segments {
		path += "/" + escape(segment)
		last := i == len(segments)-1
		switch node := current.(type) {
		case map[string]any:
			if last {
				normalized, _ := json.Marshal(value)
				old, _ := json.Marshal(node[segment])
				if string(normalized) != string(old) {
					builder.operations = append(builder.operations, patchOperation{"add", path, value})
					node[segment] = value
				}
				return
			}
			if node[segment] == nil {
				node[segment] = map[string]any{}
				builder.operations = append(builder.operations, patchOperation{"add", path, map[string]any{}})
			}
			current = node[segment]
		case []any:
			var index int
			_, _ = fmt.Sscan(segment, &index)
			current = node[index]
		}
	}
}

func (builder *patchBuilder) remove(segments ...string) {
	var current any = builder.object
	path := ""
	for i, segment := range segments {
		path += "/" + escape(segment)
		switch node := current.(type) {
		case map[string]any:
			child, found := node[segment]
			if !found {
				return
			}
			if i == len(segments)-1 {
				builder.operations = append(builder.operations, patchOperation{Operation: "remove", Path: path})
				delete(node, segment)
				return
			}
			current = child
		case []any:
			var index int
			_, _ = fmt.Sscan(segment, &index)
			current = node[index]
		default:
			return
		}
	}
}

func denied(request *admissionv1.AdmissionRequest, reason string) (*admissionv1.AdmissionResponse, string) {
	return &admissionv1.AdmissionResponse{UID: request.UID, Allowed: false, Result: &metav1.Status{Code: 403, Reason: metav1.StatusReasonForbidden, Message: "ModelDev Pod admission refused: " + reason}}, reason
}

// Keep dependency failures transient inside the successful AdmissionReview
// transport response. The API server preserves this typed rejection status;
// security and policy refusals continue to use Forbidden.
func unavailable(request *admissionv1.AdmissionRequest) (*admissionv1.AdmissionResponse, string) {
	return &admissionv1.AdmissionResponse{UID: request.UID, Allowed: false, Result: &metav1.Status{Code: 503, Reason: metav1.StatusReasonServiceUnavailable, Message: "ModelDev Pod admission refused: dependency_unavailable"}}, "dependency_unavailable"
}

func (handler *Handler) admit(ctx context.Context, request *admissionv1.AdmissionRequest, failure *admissionFailure) (*admissionv1.AdmissionResponse, string) {
	allowed := &admissionv1.AdmissionResponse{UID: request.UID, Allowed: true}
	binding, found := handler.bindings[request.Namespace]
	if !found || request.Operation != admissionv1.Create || request.Resource != (metav1.GroupVersionResource{Version: "v1", Resource: "pods"}) || request.SubResource != "" {
		return allowed, "outside_scope"
	}
	var pod corev1.Pod
	var raw map[string]any
	if json.Unmarshal(request.Object.Raw, &pod) != nil || json.Unmarshal(request.Object.Raw, &raw) != nil || raw["metadata"] == nil || raw["spec"] == nil {
		return denied(request, "invalid_pod")
	}
	labels := pod.Labels
	if binding.Training != nil && pod.Spec.ServiceAccountName == binding.Training.ServiceAccount {
		return handler.admitTraining(ctx, request, &pod, raw, binding, failure)
	}
	if labels["pipelines.kubeflow.org/v2_component"] != "true" {
		if labels[ProfileLabel] != "" || labels[RoleLabel] != "" {
			return denied(request, "invalid_target")
		}
		return allowed, "outside_scope"
	}
	if request.UserInfo.Username != binding.ArgoCreator || request.Kind != (metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}) || pod.APIVersion != "v1" || pod.Kind != "Pod" || pod.Namespace != binding.Namespace || pod.Spec.ServiceAccountName != binding.StepServiceAccount || len(pod.OwnerReferences) != 1 {
		return denied(request, "workload_identity")
	}
	owner := pod.OwnerReferences[0]
	if owner.APIVersion != "argoproj.io/v1alpha1" || owner.Kind != "Workflow" || owner.Controller == nil || !*owner.Controller || len(validation.IsDNS1123Subdomain(owner.Name)) != 0 || !canonicalUUID(string(owner.UID)) || labels["workflows.argoproj.io/workflow"] != owner.Name || !canonicalUUID(labels["pipeline/runid"]) {
		return denied(request, "workflow_identity")
	}
	identityCtx, cancel := context.WithTimeout(ctx, handler.dependencyTimeout)
	defer cancel()
	started := time.Now()
	namespace, err := handler.reader.Namespace(identityCtx, binding.Namespace)
	if err != nil || namespace == nil {
		failure.record("namespace", started, identityCtx, err)
		return unavailable(request)
	}
	if string(namespace.UID) != binding.NamespaceUID || namespace.Name != binding.Namespace || namespace.DeletionTimestamp != nil || namespace.Labels[binding.OwnerLabel.Key] != binding.OwnerLabel.Value {
		return denied(request, "namespace_identity")
	}
	// The tenant label becomes part of the observability scope, so a missing or
	// non-canonical value must fail closed instead of producing an unattributable
	// metric series.
	if !canonicalUUID(namespace.Labels[NamespaceTenantLabel]) {
		return denied(request, "namespace_identity")
	}
	started = time.Now()
	workflow, err := handler.reader.Workflow(identityCtx, binding.Namespace, owner.Name)
	if err != nil || workflow == nil {
		failure.record("workflow", started, identityCtx, err)
		return unavailable(request)
	}
	if workflow.GetAPIVersion() != owner.APIVersion || workflow.GetKind() != owner.Kind || workflow.GetName() != owner.Name || workflow.GetNamespace() != binding.Namespace || workflow.GetUID() != owner.UID || workflow.GetDeletionTimestamp() != nil || workflow.GetLabels()["pipeline/runid"] != labels["pipeline/runid"] {
		return denied(request, "workflow_identity")
	}
	// KFP 2.16's workflow-level v2 label covers backend driver Pods as well.
	// Recognize only the separately frozen backend executable after live owner
	// verification; never infer a workload profile from missing metadata.
	if binding.Driver != nil {
		for _, container := range pod.Spec.Containers {
			if container.Name == "main" && container.Image == binding.Driver.Image {
				return handler.retainDriver(request, &pod, raw, binding.Driver)
			}
		}
	}
	profile, ok := handler.profiles[labels[ProfileLabel]]
	if !ok || !slices.Contains(binding.Profiles, profile.ID) {
		return denied(request, "unknown_profile")
	}
	rules, ok := roleMounts(profile.Kind)[labels[RoleLabel]]
	if !ok {
		return denied(request, "unknown_role")
	}
	componentResources := profile.material.ComponentResources[labels[RoleLabel]]
	digest, err := componentResources.Digest()
	if err != nil || pod.Annotations["ani.modeldev/component-resources-sha256"] != digest {
		return denied(request, "component_resources")
	}
	if profile.PipelineVersionLabel != "" && workflow.GetLabels()[profile.PipelineVersionLabel] != profile.material.Implementation.PipelineVersionID || profile.PipelineVersionAnnotation != "" && workflow.GetAnnotations()[profile.PipelineVersionAnnotation] != profile.material.Implementation.PipelineVersionID {
		return denied(request, "pipeline_version")
	}
	if pod.Spec.HostNetwork || pod.Spec.HostPID || pod.Spec.HostIPC || len(pod.Spec.EphemeralContainers) != 0 || len(pod.Spec.Containers) != 2 || len(pod.Spec.InitContainers) > 4 {
		return denied(request, "container_contract")
	}
	pvcNames := map[string]bool{}
	readOnlyVolumes := map[string]bool{}
	volumeNames := map[string]bool{}
	for _, volume := range pod.Spec.Volumes {
		if volumeNames[volume.Name] || volume.Name == "" || volume.HostPath != nil {
			return denied(request, "volume_contract")
		}
		if volume.PersistentVolumeClaim == nil && volume.EmptyDir == nil && volume.ConfigMap == nil && volume.Secret == nil && volume.Projected == nil {
			return denied(request, "volume_contract")
		}
		volumeNames[volume.Name] = true
		if volume.PersistentVolumeClaim != nil {
			claim := volume.PersistentVolumeClaim.ClaimName
			if !strings.HasPrefix(claim, "ani-kfp-workspace-") || len(validation.IsDNS1123Subdomain(claim)) != 0 || volume.PersistentVolumeClaim.ReadOnly {
				return denied(request, "workspace_contract")
			}
			pvcNames[volume.Name] = true
		}
		readOnlyVolumes[volume.Name] = volume.ConfigMap != nil || volume.Secret != nil || volume.Projected != nil
	}
	expectedPVCs := 0
	if len(rules) != 0 {
		expectedPVCs = 1
	}
	if len(pvcNames) != expectedPVCs {
		return denied(request, "workspace_contract")
	}
	seenContainers := map[string]bool{}
	mainFound, waitFound := false, false
	categories := []struct {
		name       string
		containers []corev1.Container
	}{{"containers", pod.Spec.Containers}, {"initContainers", pod.Spec.InitContainers}}
	for _, category := range categories {
		for _, container := range category.containers {
			if seenContainers[container.Name] || container.Name == "" || len(container.VolumeDevices) != 0 {
				return denied(request, "container_contract")
			}
			seenContainers[container.Name] = true
			main := category.name == "containers" && container.Name == "main"
			if category.name == "containers" && !main && container.Name != "wait" {
				return denied(request, "container_contract")
			}
			if main {
				mainFound = true
				if !componentWorkEnvironment(container, labels[RoleLabel]) {
					return denied(request, "work_environment")
				}
				if container.Image != profile.ComponentImage || !componentResourcesMatch(container.Resources, componentResources) {
					return denied(request, "image_contract")
				}
			} else if !slices.Contains(profile.AllowedSupportImages, container.Image) {
				return denied(request, "image_contract")
			}
			if category.name == "containers" && container.Name == "wait" {
				waitFound = true
			}
			if container.SecurityContext != nil && (container.SecurityContext.Privileged != nil && *container.SecurityContext.Privileged || container.SecurityContext.Capabilities != nil && len(container.SecurityContext.Capabilities.Add) != 0) {
				return denied(request, "privileged_container")
			}
			if main {
				control := false
				for _, mount := range container.VolumeMounts {
					if mount.MountPath != "/tmp" {
						continue
					}
					for _, volume := range pod.Spec.Volumes {
						if volume.Name != mount.Name {
							continue
						}
						want, err := resource.ParseQuantity(componentResources.EphemeralStorageLimit)
						if control || err != nil || volume.EmptyDir == nil || volume.EmptyDir.Medium != "" || volume.EmptyDir.SizeLimit == nil || volume.EmptyDir.SizeLimit.Cmp(want) != 0 || mount.SubPath != "" || mount.SubPathExpr != "" || mount.ReadOnly {
							return denied(request, "control_io")
						}
						control = true
					}
				}
				if !control {
					return denied(request, "control_io")
				}
			}
			seenMounts := map[string]bool{}
			for _, mount := range container.VolumeMounts {
				if !volumeNames[mount.Name] || seenMounts[mount.MountPath] {
					return denied(request, "mount_contract")
				}
				seenMounts[mount.MountPath] = true
				if !pvcNames[mount.Name] {
					continue
				}
				if mount.SubPathExpr != "" {
					return denied(request, "mount_contract")
				}
				rule, ok := rules[mount.MountPath]
				if !ok || mount.SubPath != rule.subPath {
					return denied(request, "mount_contract")
				}
			}
			if main {
				count := 0
				for _, mount := range container.VolumeMounts {
					if pvcNames[mount.Name] {
						count++
					}
				}
				if count != len(rules) {
					return denied(request, "mount_contract")
				}
			}
		}
	}
	if !mainFound || !waitFound {
		return denied(request, "container_contract")
	}
	seenFinalizers := map[string]bool{}
	for _, finalizer := range pod.Finalizers {
		if finalizer == "" || seenFinalizers[finalizer] {
			return denied(request, "finalizer_contract")
		}
		seenFinalizers[finalizer] = true
	}
	builder := patchBuilder{object: raw}
	// Metric labels use already-verified sources: the Workflow run ID for the
	// instance, and the tenant label of the namespace whose UID and owner label
	// were checked above. Backend driver Pods return earlier and stay unlabelled.
	builder.set(labels["pipeline/runid"], "metadata", "labels", InstanceLabel)
	builder.set(namespace.Labels[NamespaceTenantLabel], "metadata", "labels", TenantLabel)
	builder.set(WorkloadKindTrainer, "metadata", "labels", WorkloadKindLabel)
	builder.set(profile.FSGroup, "spec", "securityContext", "fsGroup")
	builder.set("OnRootMismatch", "spec", "securityContext", "fsGroupChangePolicy")
	builder.set("RuntimeDefault", "spec", "securityContext", "seccompProfile", "type")
	builder.remove("spec", "securityContext", "seccompProfile", "localhostProfile")
	for _, category := range categories {
		for i, container := range category.containers {
			prefix := []string{"spec", category.name, fmt.Sprint(i), "securityContext"}
			for key, value := range map[string]any{"runAsUser": profile.RunAsUser, "runAsGroup": profile.RunAsGroup, "runAsNonRoot": true, "readOnlyRootFilesystem": true, "allowPrivilegeEscalation": false, "privileged": false} {
				builder.set(value, append(append([]string{}, prefix...), key)...)
			}
			builder.set([]string{"ALL"}, append(append([]string{}, prefix...), "capabilities", "drop")...)
			builder.remove(append(append([]string{}, prefix...), "capabilities", "add")...)
			builder.set("RuntimeDefault", append(append([]string{}, prefix...), "seccompProfile", "type")...)
			builder.remove(append(append([]string{}, prefix...), "seccompProfile", "localhostProfile")...)
			main := category.name == "containers" && container.Name == "main"
			if main {
				builder.set(componentResources.EphemeralStorageRequest, "spec", category.name, fmt.Sprint(i), "resources", "requests", "ephemeral-storage")
				builder.set(componentResources.EphemeralStorageLimit, "spec", category.name, fmt.Sprint(i), "resources", "limits", "ephemeral-storage")
			}
			for j, mount := range container.VolumeMounts {
				if pvcNames[mount.Name] {
					readOnly := true
					if main {
						readOnly = rules[mount.MountPath].readOnly
					}
					builder.set(readOnly, "spec", category.name, fmt.Sprint(i), "volumeMounts", fmt.Sprint(j), "readOnly")
				} else if readOnlyVolumes[mount.Name] {
					builder.set(true, "spec", category.name, fmt.Sprint(i), "volumeMounts", fmt.Sprint(j), "readOnly")
				}
			}
		}
	}
	if !seenFinalizers[ExitEvidenceFinalizer] {
		if _, present := raw["metadata"].(map[string]any)["finalizers"]; !present || raw["metadata"].(map[string]any)["finalizers"] == nil {
			builder.set([]string{ExitEvidenceFinalizer}, "metadata", "finalizers")
		} else {
			builder.operations = append(builder.operations, patchOperation{"add", "/metadata/finalizers/-", ExitEvidenceFinalizer})
		}
	}
	if len(builder.operations) != 0 {
		patch, err := json.Marshal(builder.operations)
		if err != nil {
			return denied(request, "patch_unavailable")
		}
		patchType := admissionv1.PatchTypeJSONPatch
		allowed.PatchType, allowed.Patch = &patchType, patch
		return allowed, "mutated"
	}
	return allowed, "already_conforming"
}

func (handler *Handler) retainDriver(request *admissionv1.AdmissionRequest, pod *corev1.Pod, raw map[string]any, driver *DriverBinding) (*admissionv1.AdmissionResponse, string) {
	if pod.Labels[ProfileLabel] != "" || pod.Labels[RoleLabel] != "" && pod.Labels[RoleLabel] != "driver" || pod.Spec.HostNetwork || pod.Spec.HostPID || pod.Spec.HostIPC || len(pod.Spec.EphemeralContainers) != 0 || len(pod.Spec.Containers) != 2 || len(pod.Spec.InitContainers) > 4 {
		return denied(request, "driver_contract")
	}
	mainFound, waitFound := false, false
	seen := map[string]bool{}
	for _, category := range []struct {
		primary    bool
		containers []corev1.Container
	}{{true, pod.Spec.Containers}, {false, pod.Spec.InitContainers}} {
		for _, container := range category.containers {
			if container.SecurityContext != nil && (container.SecurityContext.Privileged != nil && *container.SecurityContext.Privileged || container.SecurityContext.Capabilities != nil && len(container.SecurityContext.Capabilities.Add) != 0) {
				return denied(request, "privileged_container")
			}
			if seen[container.Name] || container.Name == "" || len(container.VolumeDevices) != 0 {
				return denied(request, "driver_contract")
			}
			seen[container.Name] = true
			if category.primary && container.Name == "main" {
				mainFound = true
				if container.Image != driver.Image || !matchesDriverCommand(container.Command, driver.Command) {
					return denied(request, "driver_contract")
				}
				kind, kindOK := driverArgument(container.Args, "--type")
				run, runOK := driverArgument(container.Args, "--run_id")
				if !kindOK || !runOK || run != pod.Labels["pipeline/runid"] || !slices.Contains([]string{"ROOT_DAG", "DAG", "CONTAINER"}, kind) {
					return denied(request, "driver_contract")
				}
			} else {
				if category.primary && container.Name != "wait" || !slices.Contains(driver.AllowedSupportImages, container.Image) {
					return denied(request, "driver_contract")
				}
				if category.primary {
					waitFound = true
				}
			}
		}
	}
	if !mainFound || !waitFound {
		return denied(request, "driver_contract")
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil || volume.HostPath != nil || volume.EmptyDir == nil && volume.ConfigMap == nil && volume.Secret == nil && volume.Projected == nil {
			return denied(request, "driver_contract")
		}
	}
	seenFinalizers := map[string]bool{}
	for _, finalizer := range pod.Finalizers {
		if finalizer == "" || seenFinalizers[finalizer] {
			return denied(request, "finalizer_contract")
		}
		seenFinalizers[finalizer] = true
	}
	builder := patchBuilder{object: raw}
	builder.set("driver", "metadata", "labels", RoleLabel)
	if !seenFinalizers[ExitEvidenceFinalizer] {
		if raw["metadata"].(map[string]any)["finalizers"] == nil {
			builder.set([]string{ExitEvidenceFinalizer}, "metadata", "finalizers")
		} else {
			builder.operations = append(builder.operations, patchOperation{"add", "/metadata/finalizers/-", ExitEvidenceFinalizer})
		}
	}
	response := &admissionv1.AdmissionResponse{UID: request.UID, Allowed: true}
	if len(builder.operations) != 0 {
		patch, err := json.Marshal(builder.operations)
		if err != nil {
			return denied(request, "patch_unavailable")
		}
		kind := admissionv1.PatchTypeJSONPatch
		response.Patch, response.PatchType = patch, &kind
		return response, "driver_retained"
	}
	return response, "already_conforming"
}

// Argo v3.7.3 workflow/controller/workflowpod.go prepends this exact emissary
// grammar to the original command. Only its three logging options may vary;
// the executable after the delimiter must still equal the frozen command.
func matchesDriverCommand(command, frozen []string) bool {
	if slices.Equal(command, frozen) {
		return true
	}
	if len(command) != 9+len(frozen) || command[0] != "/var/run/argo/argoexec" || command[1] != "emissary" || command[2] != "--loglevel" || command[4] != "--log-format" || command[6] != "--gloglevel" || command[8] != "--" {
		return false
	}
	if !slices.Contains([]string{"panic", "fatal", "error", "warning", "info", "debug", "trace"}, command[3]) || !slices.Contains([]string{"text", "json"}, command[5]) {
		return false
	}
	level, err := strconv.Atoi(command[7])
	return err == nil && strconv.Itoa(level) == command[7] && slices.Equal(command[9:], frozen)
}

func driverArgument(arguments []string, name string) (string, bool) {
	value, count := "", 0
	for i, argument := range arguments {
		if argument == name {
			if i+1 == len(arguments) || strings.HasPrefix(arguments[i+1], "--") {
				return "", false
			}
			value, count = arguments[i+1], count+1
		}
		if strings.HasPrefix(argument, name+"=") {
			return "", false
		}
	}
	return value, count == 1
}

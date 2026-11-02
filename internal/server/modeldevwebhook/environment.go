package modeldevwebhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"time"

	projection "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/tenantenvironment"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var errEnvironmentUnavailable = errors.New("environment dependency unavailable")

func reservedAccount(name string) bool {
	return name == "modeldev-step" || name == "trainer-workload" || name == "verifier"
}
func reservedMetadata(labels, annotations map[string]string, finalizers []string) bool {
	for _, m := range []map[string]string{labels, annotations} {
		for key := range m {
			if strings.HasPrefix(key, "ani.modeldev/") || strings.HasPrefix(key, "modeldev.ani.io/") {
				return true
			}
		}
	}
	for _, f := range finalizers {
		if strings.HasPrefix(f, "modeldev.ani.io/") {
			return true
		}
	}
	return false
}
func protectedPod(pod *corev1.Pod) bool {
	if reservedAccount(pod.Spec.ServiceAccountName) || reservedMetadata(pod.Labels, pod.Annotations, pod.Finalizers) {
		return true
	}
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim != nil && strings.HasPrefix(v.PersistentVolumeClaim.ClaimName, "ani-kfp-workspace-") {
			return true
		}
		if v.Secret != nil && v.Secret.SecretName == "modeldev-kfp-artifacts" {
			return true
		}
		if v.Projected != nil {
			for _, source := range v.Projected.Sources {
				if source.Secret != nil && source.Secret.Name == "modeldev-kfp-artifacts" {
					return true
				}
			}
		}
	}
	containers := append(append([]corev1.Container{}, pod.Spec.Containers...), pod.Spec.InitContainers...)
	for _, c := range pod.Spec.EphemeralContainers {
		containers = append(containers, corev1.Container{Env: c.Env, EnvFrom: c.EnvFrom})
	}
	for _, c := range containers {
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == "modeldev-kfp-artifacts" {
				return true
			}
		}
		for _, e := range c.EnvFrom {
			if e.SecretRef != nil && e.SecretRef.Name == "modeldev-kfp-artifacts" {
				return true
			}
		}
	}
	return false
}
func (h *Handler) environment(ctx context.Context, namespace string, pod *corev1.Pod, failure *admissionFailure) (NamespaceBinding, error) {
	var result NamespaceBinding
	if !strings.HasPrefix(namespace, "tenant-") || !canonicalUUID(strings.TrimPrefix(namespace, "tenant-")) || pod.Namespace != namespace || !reservedAccount(pod.Spec.ServiceAccountName) || pod.Spec.ServiceAccountName == "verifier" {
		return result, ErrConfiguration
	}
	dependency, cancel := context.WithTimeout(ctx, h.dependencyTimeout)
	defer cancel()
	started := time.Now()
	live, err := h.reader.Namespace(dependency, namespace)
	if err != nil || live == nil {
		failure.record("namespace", started, dependency, err)
		return result, errEnvironmentUnavailable
	}
	tenant := strings.TrimPrefix(namespace, "tenant-")
	if live.Name != namespace || !canonicalUUID(string(live.UID)) || live.Labels["network.ani.io/tenant-id"] != tenant || live.Labels["network.ani.io/managed-by"] != "ani-network-service" || live.Status.Phase != corev1.NamespaceActive || live.DeletionTimestamp != nil {
		return result, ErrConfiguration
	}
	started = time.Now()
	cm, err := h.reader.Environment(dependency, namespace)
	if err != nil || cm == nil {
		failure.record("environment", started, dependency, err)
		return result, errEnvironmentUnavailable
	}
	if cm.Namespace != namespace || cm.Name != "modeldev-execution-environment" || cm.Immutable == nil || !*cm.Immutable || cm.DeletionTimestamp != nil || len(cm.Data) != 2 || len(cm.BinaryData) != 0 || len(cm.Data["binding.json"])+len(cm.Data["binding.sha256"]) > 64<<10 || projection.Digest([]byte(cm.Data["binding.json"])) != cm.Data["binding.sha256"] {
		return result, ErrConfiguration
	}
	b, err := projection.Decode([]byte(cm.Data["binding.json"]))
	if err != nil || b.TenantID != tenant || b.Environment.ClusterID != h.config.ClusterID || b.Environment.NamespaceName != namespace || b.Environment.NamespaceUID != string(live.UID) || b.MaterialBundleDigest != h.config.MaterialBundleDigest || !slices.Equal(b.ControllerPrincipals, h.config.ControllerPrincipals) || cm.Labels["ani.io/managed-by"] != "ani-modeldev-service" || cm.Labels["modeldev.ani.io/environment-id"] != b.Environment.BindingID || cm.Labels["modeldev.ani.io/tenant-id"] != tenant || cm.Annotations["modeldev.ani.io/namespace-uid"] != string(live.UID) {
		return result, ErrConfiguration
	}
	ids := []string{}
	for _, p := range b.Profiles {
		frozen, ok := h.profiles[p.ID]
		if !ok || frozen.MaterialSHA256 != p.MaterialSHA256 || frozen.material.Implementation.PipelineID != p.PipelineID || frozen.material.Implementation.PipelineVersionID != p.VersionID || frozen.material.Implementation.PipelineIRSHA256 != p.IRSHA256 {
			return result, ErrConfiguration
		}
		ids = append(ids, p.ID)
	}
	if len(ids) != len(h.profiles) {
		return result, ErrConfiguration
	}
	identities := b.Environment.Identities
	uid := identities.KFPStepServiceAccountUID
	if pod.Spec.ServiceAccountName == "trainer-workload" {
		uid = identities.TrainerServiceAccountUID
	}
	started = time.Now()
	sa, err := h.reader.ServiceAccount(dependency, namespace, pod.Spec.ServiceAccountName)
	if err != nil || sa == nil {
		failure.record("service_account", started, dependency, err)
		return result, errEnvironmentUnavailable
	}
	if sa.Name != pod.Spec.ServiceAccountName || sa.Namespace != namespace || string(sa.UID) != uid || sa.DeletionTimestamp != nil || sa.Labels["ani.io/managed-by"] != "ani-modeldev-service" || sa.Labels["modeldev.ani.io/environment-id"] != b.Environment.BindingID || sa.Labels["modeldev.ani.io/tenant-id"] != tenant || sa.Annotations["modeldev.ani.io/namespace-uid"] != string(live.UID) {
		return result, ErrConfiguration
	}
	refs := []corev1.LocalObjectReference{}
	for _, ref := range b.ManagedImageAccess.PullBindings {
		refs = append(refs, corev1.LocalObjectReference{Name: ref.SecretName})
	}
	if !reflect.DeepEqual(pod.Spec.ImagePullSecrets, refs) {
		return result, ErrConfiguration
	}
	if pod.Spec.ServiceAccountName == "trainer-workload" {
		if len(sa.ImagePullSecrets) != 0 || sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken {
			return result, ErrConfiguration
		}
	} else if !reflect.DeepEqual(sa.ImagePullSecrets, refs) {
		return result, ErrConfiguration
	}
	return NamespaceBinding{Namespace: namespace, NamespaceUID: string(live.UID), OwnerLabel: LabelBinding{Key: "network.ani.io/managed-by", Value: "ani-network-service"}, ArgoCreator: h.config.ArgoCreator, StepServiceAccount: "modeldev-step", Profiles: ids, Driver: h.config.Driver, Training: h.config.Training, EnvironmentID: b.Environment.BindingID, TenantID: tenant, PipelineRoot: b.DispatchOwner.PipelineRoot}, nil
}

func oneNamed(items []any, name string) (map[string]any, bool) {
	var result map[string]any
	seen := map[string]bool{}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, false
		}
		key, ok := item["name"].(string)
		if !ok || key == "" || seen[key] {
			return nil, false
		}
		seen[key] = true
		if key == name {
			result = item
		}
	}
	return result, result != nil
}
func workflowEnvironment(workflow *unstructured.Unstructured, binding NamespaceBinding) bool {
	entry, ok, _ := unstructured.NestedString(workflow.Object, "spec", "entrypoint")
	templates, found, _ := unstructured.NestedSlice(workflow.Object, "spec", "templates")
	if !ok || !found {
		return false
	}
	template, ok := oneNamed(templates, entry)
	if !ok {
		return false
	}
	tasks, found, _ := unstructured.NestedSlice(template, "dag", "tasks")
	if !found {
		return false
	}
	root, ok := oneNamed(tasks, "root-driver")
	if !ok {
		return false
	}
	parameters, found, _ := unstructured.NestedSlice(root, "arguments", "parameters")
	if !found {
		return false
	}
	parameter, ok := oneNamed(parameters, "runtime-config")
	if !ok {
		return false
	}
	value, ok := parameter["value"].(string)
	if !ok || len(value) > 64<<10 || !uniqueJSONKeys([]byte(value)) {
		return false
	}
	var config struct {
		ParameterValues map[string]string `json:"parameterValues"`
		Root            string            `json:"gcsOutputDirectory"`
	}
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF || len(config.ParameterValues) != 4 {
		return false
	}
	p := config.ParameterValues
	return canonicalUUID(p["execution_id"]) && sha256Pattern.MatchString(p["spec_hash"]) && p["environment_binding_id"] == binding.EnvironmentID && p["namespace_uid"] == binding.NamespaceUID && config.Root == binding.PipelineRoot+"/"+p["execution_id"]
}

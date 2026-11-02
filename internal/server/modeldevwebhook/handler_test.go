package modeldevwebhook_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	projection "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server/modeldevwebhook"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sort"
)

const namespaceUID = "8f79e7cc-769b-4b2d-87d3-a69c53c946f3"
const namespaceTenant = "9a1f3c62-4b7d-4e05-8c31-2d6f5b0e7a48"
const workflowUID = "2d9c812d-c6ec-4fa4-8e6b-e14b5a0a0727"
const runID = "5dc1e8a2-b890-4f41-bba0-c3f9b1370177"
const requestUID = "549b5d38-739c-4bb7-8eb6-e262b945b1ed"
const supportImage = "example.invalid/support@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type identities struct {
	namespace   *corev1.Namespace
	environment *corev1.ConfigMap
	accounts    map[string]*corev1.ServiceAccount
	workflow    *unstructured.Unstructured
	err         error
	entered     chan struct{}
	block       chan struct{}
	once        sync.Once
}

func (reader *identities) Namespace(ctx context.Context, _ string) (*corev1.Namespace, error) {
	if reader.entered != nil {
		reader.once.Do(func() { close(reader.entered) })
	}
	if reader.block != nil {
		select {
		case <-reader.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if reader.err != nil {
		return nil, reader.err
	}
	return reader.namespace.DeepCopy(), nil
}
func (reader *identities) Workflow(context.Context, string, string) (*unstructured.Unstructured, error) {
	if reader.err != nil {
		return nil, reader.err
	}
	return reader.workflow.DeepCopy(), nil
}
func refreshProjection(t *testing.T, config *modeldevwebhook.Config, reader *identities) {
	t.Helper()
	binding, err := projection.Decode([]byte(reader.environment.Data["binding.json"]))
	if err != nil {
		t.Fatal(err)
	}
	for i := range binding.Profiles {
		for _, p := range config.Profiles {
			if p.ID == binding.Profiles[i].ID {
				raw, _ := os.ReadFile(p.MaterialFile)
				material, _, err := cpup01.ParseMaterial(raw)
				if err != nil {
					t.Fatal(err)
				}
				binding.Profiles[i].MaterialSHA256 = p.MaterialSHA256
				binding.Profiles[i].IRSHA256 = material.Implementation.PipelineIRSHA256
				binding.Profiles[i].PipelineID = material.Implementation.PipelineID
				binding.Profiles[i].VersionID = material.Implementation.PipelineVersionID
			}
		}
	}
	raw, err := binding.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	reader.environment.Data["binding.json"] = string(raw)
	reader.environment.Data["binding.sha256"] = projection.Digest(raw)
}
func (reader *identities) Environment(context.Context, string) (*corev1.ConfigMap, error) {
	if reader.err != nil {
		return nil, reader.err
	}
	return reader.environment.DeepCopy(), nil
}
func (reader *identities) ServiceAccount(_ context.Context, _ string, name string) (*corev1.ServiceAccount, error) {
	if reader.err != nil {
		return nil, reader.err
	}
	sa := reader.accounts[name]
	if sa == nil {
		return nil, errors.New("not found")
	}
	return sa.DeepCopy(), nil
}

func fixtureConfig(t *testing.T) (modeldevwebhook.Config, *identities) {
	t.Helper()
	directory := t.TempDir()
	profiles := []modeldevwebhook.Profile{}
	for _, kind := range []string{"GENERAL_TRAINING", "FINETUNING"} {
		release := conformance.MaterialV21()
		id := "modeldev-mlp-cpu"
		if kind == "FINETUNING" {
			release = conformance.FinetuningMaterialV21()
			id = "modeldev-sft-lora-cpu"
		}
		for role := range release.ComponentResources {
			release.ComponentResources[role] = cpup01.ComponentResources{CPURequest: "2", CPULimit: "4", MemoryRequest: "6Gi", MemoryLimit: "8Gi", EphemeralStorageRequest: "10Gi", EphemeralStorageLimit: "12Gi"}
		}
		raw, err := release.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, id+".json")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(raw)
		// General training no longer pins an image in its material, so the step
		// component image is synthetic here. Finetuning keeps its material-bound
		// export/verify identity, which the configuration cross-checks.
		componentImage := "registry.example.test/modeldev-step@sha256:" + strings.Repeat("e", 64)
		if release.Finetuning != nil {
			componentImage = release.Program.ImageDigest
		}
		profiles = append(profiles, modeldevwebhook.Profile{ID: id, Kind: kind, MaterialFile: path, MaterialSHA256: hex.EncodeToString(digest[:]), ComponentImage: componentImage,
			AllowedSupportImages: []string{supportImage}, RunAsUser: 10001, RunAsGroup: 10001, FSGroup: 10001})
	}
	profiles = append(profiles, profiles[0], profiles[1])
	profiles[2].ID = "modeldev-mlp-gpu"
	profiles[3].ID = "modeldev-sft-lora-gpu"
	bundle := []byte(`{"schema":"test-immutable-bundle"}`)
	bundleFile := filepath.Join(directory, "manifest.json")
	if err := os.WriteFile(bundleFile, bundle, 0600); err != nil {
		t.Fatal(err)
	}
	bundleHash := sha256.Sum256(bundle)
	config := modeldevwebhook.Config{SchemaVersion: modeldevwebhook.ConfigSchemaVersion, Profiles: profiles, ClusterID: "22222222-2222-4222-8222-222222222222", MaterialBundleFile: bundleFile, MaterialBundleDigest: hex.EncodeToString(bundleHash[:]), ArgoCreator: "system:serviceaccount:kubeflow:argo", Training: &modeldevwebhook.TrainingBinding{ServiceAccount: "trainer-workload", Creator: "system:serviceaccount:kube-system:job-controller"}, Driver: &modeldevwebhook.DriverBinding{Image: "example.invalid/driver@sha256:" + strings.Repeat("b", 64), Command: []string{"driver"}, AllowedSupportImages: []string{supportImage}}, ControllerPrincipals: []string{"system:serviceaccount:kube-system:job-controller", "system:serviceaccount:kubeflow:argo"}}
	namespace := "tenant-" + namespaceTenant
	reader := &identities{namespace: &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: types.UID(namespaceUID), Labels: map[string]string{"network.ani.io/managed-by": "ani-network-service", modeldevwebhook.NamespaceTenantLabel: namespaceTenant}}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}}, workflow: &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "metadata": map[string]any{"name": "main-flow", "namespace": namespace, "uid": workflowUID, "labels": map[string]any{"pipeline/runid": runID}}}}}
	snapshot := conformance.SnapshotV1()
	input, _ := cpup01.TenantStorageScope(snapshot.PublicationScope, namespaceTenant, "input")
	publish, _ := cpup01.TenantStorageScope(snapshot.PublicationScope, namespaceTenant, "publish")
	model, _ := cpup01.TenantStorageScope(snapshot.PublicationScope, namespaceTenant, "base-model")
	ids := snapshot.Environment.Identities
	ids.KFPStepServiceAccount = "modeldev-step"
	ids.TrainerServiceAccount = "trainer-workload"
	ids.VerifierServiceAccount = "verifier"
	b := projection.Binding{SchemaVersion: projection.Schema, TenantID: namespaceTenant, Environment: projection.Environment{BindingID: "11111111-1111-4111-8111-111111111111", ClusterID: config.ClusterID, NamespaceName: namespace, NamespaceUID: namespaceUID, KFPConnectionRef: "managed-kfp", ExperimentID: "44444444-4444-4444-8444-444444444444", Identities: ids}, InputScope: input, PublicationScope: publish, ModelScope: model, Workspace: snapshot.Workspace, Timing: cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: 1800, ExecutionTimeoutMaxSeconds: 3600, StepTimeoutSeconds: 300}, InputFilePath: "/inputs/dataset-manifest.json", OutputDirectoryPath: "/workspace/training", TargetPlatform: cpup01.TrainingPlatform{OS: "linux", Architecture: "amd64"}, DispatchOwner: cpup01.DispatchOwnerSnapshot{Reference: "managed-owner", RevisionSHA256: strings.Repeat("a", 64), PipelineRoot: "s3://test-artifacts/artifacts/" + namespaceTenant}, ManagedImageAccess: cpup01.ManagedImageAccessSnapshot{PullBindings: []cpup01.ManagedImagePullBinding{{RegistryAuthority: "registry.example.test", SecretName: "ani-pull-" + strings.Repeat("a", 32)}}}, MaterialBundleDigest: config.MaterialBundleDigest, ControllerPrincipals: config.ControllerPrincipals, Objects: []projection.ObjectRef{}}
	for _, profile := range profiles {
		raw, _ := os.ReadFile(profile.MaterialFile)
		m, _, _ := cpup01.ParseMaterial(raw)
		b.Profiles = append(b.Profiles, projection.Profile{ID: profile.ID, PipelineID: m.Implementation.PipelineID, VersionID: m.Implementation.PipelineVersionID, IRSHA256: m.Implementation.PipelineIRSHA256, MaterialSHA256: profile.MaterialSHA256})
	}
	sort.Slice(b.Profiles, func(i, j int) bool { return b.Profiles[i].ID < b.Profiles[j].ID })
	for kind, names := range map[string][]string{"ServiceAccount": {"modeldev-step", "trainer-workload", "verifier"}, "RoleBinding": {"modeldev-runtime", "modeldev-step", "modeldev-webhook-read", "modeldev-kfp-api", "modeldev-kfp-access"}, "ConfigMap": {"modeldev-step-owner", "ani-kfp-ca", "kfp-launcher", "metadata-grpc-configmap"}} {
		for _, name := range names {
			uid := uuid.NewString()
			if kind == "ServiceAccount" {
				uid = map[string]string{"modeldev-step": ids.KFPStepServiceAccountUID, "trainer-workload": ids.TrainerServiceAccountUID, "verifier": ids.VerifierServiceAccountUID}[name]
			}
			b.Objects = append(b.Objects, projection.ObjectRef{Kind: kind, Name: name, UID: uid, SpecDigest: strings.Repeat("a", 64)})
		}
	}
	sort.Slice(b.Objects, func(i, j int) bool {
		return b.Objects[i].Kind+"/"+b.Objects[i].Name < b.Objects[j].Kind+"/"+b.Objects[j].Name
	})
	bindingRaw, err := b.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	immutable := true
	owner := metav1.ObjectMeta{Namespace: namespace, Name: "modeldev-execution-environment", UID: "88888888-8888-4888-8888-888888888888", Labels: map[string]string{"ani.io/managed-by": "ani-modeldev-service", "modeldev.ani.io/environment-id": b.Environment.BindingID, "modeldev.ani.io/tenant-id": namespaceTenant}, Annotations: map[string]string{"modeldev.ani.io/namespace-uid": namespaceUID}}
	reader.environment = &corev1.ConfigMap{ObjectMeta: owner, Immutable: &immutable, Data: map[string]string{"binding.json": string(bindingRaw), "binding.sha256": projection.Digest(bindingRaw)}}
	reader.accounts = map[string]*corev1.ServiceAccount{}
	for _, entry := range []struct{ name, uid string }{{"modeldev-step", ids.KFPStepServiceAccountUID}, {"trainer-workload", ids.TrainerServiceAccountUID}, {"verifier", ids.VerifierServiceAccountUID}} {
		metadata := *owner.DeepCopy()
		metadata.Name = entry.name
		metadata.UID = types.UID(entry.uid)
		sa := &corev1.ServiceAccount{ObjectMeta: metadata, ImagePullSecrets: []corev1.LocalObjectReference{{Name: "ani-pull-" + strings.Repeat("a", 32)}}}
		if entry.name == "trainer-workload" {
			sa.ImagePullSecrets = nil
			off := false
			sa.AutomountServiceAccountToken = &off
		}
		reader.accounts[entry.name] = sa
	}
	runtimeConfig, _ := json.Marshal(map[string]any{"gcsOutputDirectory": b.DispatchOwner.PipelineRoot + "/22222222-2222-4222-8222-222222222222", "parameterValues": map[string]string{"execution_id": "22222222-2222-4222-8222-222222222222", "spec_hash": strings.Repeat("a", 64), "environment_binding_id": b.Environment.BindingID, "namespace_uid": namespaceUID}})
	reader.workflow.Object["spec"] = map[string]any{"entrypoint": "entrypoint", "templates": []any{map[string]any{"name": "entrypoint", "dag": map[string]any{"tasks": []any{map[string]any{"name": "root-driver", "arguments": map[string]any{"parameters": []any{map[string]any{"name": "runtime-config", "value": string(runtimeConfig)}}}}}}}}}

	return config, reader
}

func request(t *testing.T, config modeldevwebhook.Config, profile, role string) admissionv1.AdmissionReview {
	t.Helper()
	image := config.Profiles[0].ComponentImage
	resourceDigest := ""
	ephemeralLimit := ""
	for _, candidate := range config.Profiles {
		if candidate.ID != profile {
			continue
		}
		raw, err := os.ReadFile(candidate.MaterialFile)
		if err != nil {
			t.Fatal(err)
		}
		material, _, err := cpup01.ParseMaterial(raw)
		if err != nil {
			t.Fatal(err)
		}
		if value, ok := material.ComponentResources[role]; ok {
			ephemeralLimit = value.EphemeralStorageLimit
			resourceDigest, err = value.Digest()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if profile == "modeldev-sft-lora-cpu" {
		image = config.Profiles[1].ComponentImage
	}
	mounts := map[string]string{}
	switch role {
	case "prepare", "collect", "publish", "close", "register":
		mounts["/workspace"] = ""
	case "export":
		mounts = map[string]string{"/workspace/inputs": "inputs", "/workspace/training": "training", "/workspace/reports/train": "reports/train", "/workspace/export": "export", "/workspace/reports/export": "reports/export"}
	case "verify":
		mounts = map[string]string{"/workspace/inputs": "inputs", "/workspace/training": "training", "/workspace/reports/train": "reports/train", "/workspace/export": "export", "/workspace/reports/export": "reports/export", "/workspace/reports/verify": "reports/verify"}
	}
	if role == "collect" || role == "publish" {
		mounts["/workspace/publication"] = "publication"
		mounts["/workspace/work/"+role] = "work/" + role
	}
	if role == "export" || role == "verify" {
		mounts["/workspace/work/"+role] = "work/" + role
	}
	if role == "prepare" {
		mounts["/workspace/work/prepare/tmp"] = "work/prepare/tmp"
	}
	volumeMounts := []map[string]any{{"name": "component-tmp", "mountPath": "/tmp"}}
	for path, subpath := range mounts {
		volumeMounts = append(volumeMounts, map[string]any{"name": "workspace", "mountPath": path, "subPath": subpath})
	}
	volumes := []map[string]any{{"name": "component-tmp", "emptyDir": map[string]any{"sizeLimit": ephemeralLimit}}}
	if len(mounts) != 0 {
		volumes = append(volumes, map[string]any{"name": "workspace", "persistentVolumeClaim": map[string]any{"claimName": "ani-kfp-workspace-test"}})
	}
	pod := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"namespace": "tenant-9a1f3c62-4b7d-4e05-8c31-2d6f5b0e7a48", "name": "step-test", "labels": map[string]string{"pipelines.kubeflow.org/v2_component": "true", modeldevwebhook.ProfileLabel: profile, modeldevwebhook.RoleLabel: role, "workflows.argoproj.io/workflow": "main-flow", "pipeline/runid": runID}, "ownerReferences": []map[string]any{{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "name": "main-flow", "uid": workflowUID, "controller": true}}, "finalizers": []string{"other.example.test/retain"}, "annotations": map[string]string{"other.example.test/config": "unchanged", "ani.modeldev/component-resources-sha256": resourceDigest}},
		"spec": map[string]any{"serviceAccountName": "modeldev-step", "containers": []map[string]any{{"name": "main", "image": image, "volumeMounts": volumeMounts, "resources": map[string]any{"requests": map[string]string{"cpu": "2", "memory": "6Gi"}, "limits": map[string]string{"cpu": "4", "memory": "8Gi"}}}, {"name": "wait", "image": supportImage}}, "initContainers": []map[string]any{{"name": "init", "image": supportImage}}, "volumes": volumes, "unknownFutureField": map[string]string{"marker": "preserved"}}}
	if role == "prepare" || role == "export" || role == "verify" || role == "collect" || role == "publish" {
		environment := []map[string]string{}
		for name, suffix := range map[string]string{"TMPDIR": "/tmp", "HF_HOME": "/hf", "HF_DATASETS_CACHE": "/hf/datasets", "MPLCONFIGDIR": "/matplotlib"} {
			environment = append(environment, map[string]string{"name": name, "value": "/workspace/work/" + role + suffix})
		}
		pod["spec"].(map[string]any)["containers"].([]map[string]any)[0]["env"] = environment
	}
	pod["spec"].(map[string]any)["imagePullSecrets"] = []map[string]string{{"name": "ani-pull-" + strings.Repeat("a", 32)}}
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	return admissionv1.AdmissionReview{TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"}, Request: &admissionv1.AdmissionRequest{UID: types.UID(requestUID), Kind: metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}, Resource: metav1.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespace: "tenant-9a1f3c62-4b7d-4e05-8c31-2d6f5b0e7a48", Operation: admissionv1.Create, UserInfo: authenticationv1.UserInfo{Username: "system:serviceaccount:kubeflow:argo"}, Object: runtime.RawExtension{Raw: raw}}}
}

func call(t *testing.T, server *httptest.Server, body admissionv1.AdmissionReview) admissionv1.AdmissionResponse {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Post(server.URL+"/mutate", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("HTTP admission protocol status %d", response.StatusCode)
	}
	var result admissionv1.AdmissionReview
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Request != nil || result.Kind != "AdmissionReview" || result.APIVersion != "admission.k8s.io/v1" || result.Response == nil || result.Response.UID != body.Request.UID {
		t.Fatalf("invalid v1 response envelope: %+v", result)
	}
	return *result.Response
}

func changePod(t *testing.T, body *admissionv1.AdmissionReview, mutate func(*corev1.Pod)) {
	t.Helper()
	var pod corev1.Pod
	if err := json.Unmarshal(body.Request.Object.Raw, &pod); err != nil {
		t.Fatal(err)
	}
	mutate(&pod)
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	body.Request.Object.Raw = raw
}

func TestHTTPSProfilesMountsPrecisePatchAndIdempotence(t *testing.T) {
	config, reader := fixtureConfig(t)
	handler, err := modeldevwebhook.New(config, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	for _, profile := range []string{"modeldev-mlp-cpu", "modeldev-sft-lora-cpu"} {
		roles := []string{"workspace-name", "prepare", "train-wait", "collect", "publish", "close", "finalize-close"}
		if profile == "modeldev-sft-lora-cpu" {
			roles = append(roles, "export", "verify", "register")
		}
		for _, role := range roles {
			t.Run(profile+"/"+role, func(t *testing.T) {
				body := request(t, config, profile, role)
				result := call(t, server, body)
				if !result.Allowed || result.PatchType == nil || *result.PatchType != admissionv1.PatchTypeJSONPatch {
					t.Fatalf("expected mutation: %+v", result)
				}
				var operations []map[string]any
				if err := json.Unmarshal(result.Patch, &operations); err != nil {
					t.Fatal(err)
				}
				for _, operation := range operations {
					if operation["path"] == "/spec" || operation["path"] == "/metadata" {
						t.Fatal("whole object replacement loses independent fields")
					}
				}
				patch, err := jsonpatch.DecodePatch(result.Patch)
				if err != nil {
					t.Fatal(err)
				}
				mutated, err := patch.Apply(body.Request.Object.Raw)
				if err != nil {
					t.Fatal(err)
				}
				var pod corev1.Pod
				if err := json.Unmarshal(mutated, &pod); err != nil {
					t.Fatal(err)
				}
				if len(pod.Finalizers) != 2 || pod.Finalizers[0] != "other.example.test/retain" || pod.Finalizers[1] != modeldevwebhook.ExitEvidenceFinalizer || pod.Annotations["other.example.test/config"] != "unchanged" {
					t.Fatal("foreign metadata lost")
				}
				var raw map[string]any
				_ = json.Unmarshal(mutated, &raw)
				if raw["spec"].(map[string]any)["unknownFutureField"].(map[string]any)["marker"] != "preserved" {
					t.Fatal("unknown spec field lost")
				}
				if *pod.Spec.SecurityContext.FSGroup != 10001 {
					t.Fatal("fsGroup missing")
				}
				for _, container := range append(pod.Spec.Containers, pod.Spec.InitContainers...) {
					if *container.SecurityContext.RunAsUser != 10001 || *container.SecurityContext.RunAsGroup != 10001 || !*container.SecurityContext.ReadOnlyRootFilesystem || *container.SecurityContext.AllowPrivilegeEscalation {
						t.Fatal("container security incomplete")
					}
				}
				main := pod.Spec.Containers[0]
				if main.Resources.Requests.Cpu().String() != "2" || main.Resources.Limits.Memory().String() != "8Gi" || main.Resources.Requests.StorageEphemeral().String() != "10Gi" {
					t.Fatal("independent CPU/memory lost or storage missing")
				}
				for _, mount := range main.VolumeMounts {
					readOnly := mount.MountPath == "/workspace" && role != "prepare" || role == "export" && (mount.SubPath == "inputs" || mount.SubPath == "training" || mount.SubPath == "reports/train") || role == "verify" && strings.HasPrefix(mount.MountPath, "/workspace/") && mount.SubPath != "reports/verify" && !strings.HasPrefix(mount.SubPath, "work/")
					if mount.ReadOnly != readOnly {
						t.Fatalf("wrong role mount: %+v", mount)
					}
				}
				body.Request.Object.Raw = mutated
				replay := call(t, server, body)
				if !replay.Allowed || len(replay.Patch) != 0 {
					t.Fatalf("conforming replay mutated: %+v", replay)
				}
			})
		}
	}
}

func TestFineVerifyOriginalAdapterMountsRemainReadOnlyAcrossAdmissionReplay(t *testing.T) {
	config, reader := fixtureConfig(t)
	handler, err := modeldevwebhook.New(config, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	body := request(t, config, "modeldev-sft-lora-cpu", "verify")
	result := call(t, server, body)
	if !result.Allowed {
		t.Fatalf("six original verify subpaths were refused: %+v", result)
	}
	apply := func(raw []byte, response admissionv1.AdmissionResponse) []byte {
		t.Helper()
		patch, err := jsonpatch.DecodePatch(response.Patch)
		if err != nil {
			t.Fatal(err)
		}
		updated, err := patch.Apply(raw)
		if err != nil {
			t.Fatal(err)
		}
		return updated
	}
	mutated := apply(body.Request.Object.Raw, result)
	want := map[string]bool{"inputs": true, "training": true, "reports/train": true, "export": true, "reports/export": true, "reports/verify": false, "work/verify": false}
	assertMounts := func(raw []byte) {
		t.Helper()
		var pod corev1.Pod
		if err := json.Unmarshal(raw, &pod); err != nil {
			t.Fatal(err)
		}
		if len(pod.Spec.Containers[0].VolumeMounts) != len(want)+1 {
			t.Fatal("verify lost its precise workspace and control I/O scopes")
		}
		for _, mount := range pod.Spec.Containers[0].VolumeMounts {
			if mount.MountPath == "/tmp" {
				if mount.ReadOnly {
					t.Fatal("control I/O unwritable")
				}
				continue
			}
			readOnly, ok := want[mount.SubPath]
			if !ok || mount.MountPath != "/workspace/"+mount.SubPath || mount.ReadOnly != readOnly {
				t.Fatalf("verify could write original adapter/report or lost its report output: %+v", mount)
			}
		}
	}
	assertMounts(mutated)
	for path, readOnly := range want {
		if !readOnly {
			continue
		}
		t.Run("repair writable "+path, func(t *testing.T) {
			replay := body
			replay.Request = body.Request.DeepCopy()
			replay.Request.Object.Raw = append([]byte(nil), mutated...)
			changePod(t, &replay, func(pod *corev1.Pod) {
				for i := range pod.Spec.Containers[0].VolumeMounts {
					if pod.Spec.Containers[0].VolumeMounts[i].SubPath == path {
						pod.Spec.Containers[0].VolumeMounts[i].ReadOnly = false
					}
				}
			})
			result := call(t, server, replay)
			if !result.Allowed || len(result.Patch) == 0 {
				t.Fatal("admission replay left an original input writable")
			}
			assertMounts(apply(replay.Request.Object.Raw, result))
		})
	}
	for _, path := range []string{"training", "reports/train"} {
		for _, mode := range []string{"missing", "wrong subpath"} {
			t.Run(mode+" "+path, func(t *testing.T) {
				rejected := request(t, config, "modeldev-sft-lora-cpu", "verify")
				changePod(t, &rejected, func(pod *corev1.Pod) {
					mounts := pod.Spec.Containers[0].VolumeMounts
					for i := range mounts {
						if mounts[i].SubPath != path {
							continue
						}
						if mode == "missing" {
							pod.Spec.Containers[0].VolumeMounts = append(mounts[:i], mounts[i+1:]...)
						} else {
							pod.Spec.Containers[0].VolumeMounts[i].SubPath = "another-execution"
						}
						break
					}
				})
				if result := call(t, server, rejected); result.Allowed || result.Result == nil || result.Result.Message != "ModelDev Pod admission refused: mount_contract" {
					t.Fatalf("verify accepted a missing or foreign original subpath: %+v", result)
				}
			})
		}
	}
	t.Run("extra writable subtree", func(t *testing.T) {
		rejected := request(t, config, "modeldev-sft-lora-cpu", "verify")
		changePod(t, &rejected, func(pod *corev1.Pod) {
			pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "workspace", MountPath: "/workspace/another", SubPath: "another"})
		})
		if result := call(t, server, rejected); result.Allowed || result.Result == nil || result.Result.Message != "ModelDev Pod admission refused: mount_contract" {
			t.Fatalf("verify accepted an additional writable workspace subtree: %+v", result)
		}
	})
}

func TestRefuseUntrustedScopeOwnerProfileImagesAndMounts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*admissionv1.AdmissionReview, *identities)
	}{
		{"namespace UID", func(_ *admissionv1.AdmissionReview, r *identities) { r.namespace.UID = types.UID(workflowUID) }},
		{"namespace owner", func(_ *admissionv1.AdmissionReview, r *identities) {
			r.namespace.Labels["network.ani.io/managed-by"] = "other-owner"
		}},
		{"workflow UID", func(_ *admissionv1.AdmissionReview, r *identities) { r.workflow.SetUID(types.UID(namespaceUID)) }},
		{"workflow run", func(_ *admissionv1.AdmissionReview, r *identities) {
			r.workflow.SetLabels(map[string]string{"pipeline/runid": namespaceUID})
		}},
		{"creator", func(b *admissionv1.AdmissionReview, _ *identities) { b.Request.UserInfo.Username = "other" }},
		{"profile missing", func(b *admissionv1.AdmissionReview, _ *identities) {
			changePod(t, b, func(p *corev1.Pod) { delete(p.Labels, modeldevwebhook.ProfileLabel) })
		}},
		{"profile unknown", func(b *admissionv1.AdmissionReview, _ *identities) {
			changePod(t, b, func(p *corev1.Pod) { p.Labels[modeldevwebhook.ProfileLabel] = "request-overridden" })
		}},
		{"role unknown", func(b *admissionv1.AdmissionReview, _ *identities) {
			changePod(t, b, func(p *corev1.Pod) { p.Labels[modeldevwebhook.RoleLabel] = "shell" })
		}},
		{"step SA", func(b *admissionv1.AdmissionReview, _ *identities) {
			changePod(t, b, func(p *corev1.Pod) { p.Spec.ServiceAccountName = "other" })
		}},
		{"owner missing", func(b *admissionv1.AdmissionReview, _ *identities) {
			changePod(t, b, func(p *corev1.Pod) { p.OwnerReferences = nil })
		}},
		{"main image", func(b *admissionv1.AdmissionReview, _ *identities) {
			changePod(t, b, func(p *corev1.Pod) { p.Spec.Containers[0].Image = supportImage })
		}},
		{"mutable support", func(b *admissionv1.AdmissionReview, _ *identities) {
			changePod(t, b, func(p *corev1.Pod) { p.Spec.InitContainers[0].Image = "mutable:tag" })
		}},
		{"mount subpath", func(b *admissionv1.AdmissionReview, _ *identities) {
			changePod(t, b, func(p *corev1.Pod) { p.Spec.Containers[0].VolumeMounts[0].SubPath = "publication" })
		}},
		{"mount expr", func(b *admissionv1.AdmissionReview, _ *identities) {
			changePod(t, b, func(p *corev1.Pod) { p.Spec.Containers[0].VolumeMounts[0].SubPathExpr = "$(UNTRUSTED)" })
		}},
		{"extra writer mount", func(b *admissionv1.AdmissionReview, _ *identities) {
			changePod(t, b, func(p *corev1.Pod) {
				p.Spec.Containers[0].VolumeMounts = append(p.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "workspace", MountPath: "/other"})
			})
		}},
		{"host namespace", func(b *admissionv1.AdmissionReview, _ *identities) {
			changePod(t, b, func(p *corev1.Pod) { p.Spec.HostPID = true })
		}},
		{"untrusted volume source", func(b *admissionv1.AdmissionReview, _ *identities) {
			changePod(t, b, func(p *corev1.Pod) {
				p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "untrusted", VolumeSource: corev1.VolumeSource{NFS: &corev1.NFSVolumeSource{Server: "untrusted.invalid", Path: "/"}}})
			})
		}},
		{"privileged", func(b *admissionv1.AdmissionReview, _ *identities) {
			changePod(t, b, func(p *corev1.Pod) {
				yes := true
				p.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: &yes}
			})
		}},
		{"API unavailable", func(_ *admissionv1.AdmissionReview, r *identities) {
			r.err = errors.New("sensitive token response must never be logged")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, reader := fixtureConfig(t)
			var logs bytes.Buffer
			handler, err := modeldevwebhook.New(config, reader, slog.New(slog.NewJSONHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewTLSServer(handler)
			defer server.Close()
			body := request(t, config, "modeldev-sft-lora-cpu", "export")
			test.mutate(&body, reader)
			result := call(t, server, body)
			wantCode, wantReason := int32(403), metav1.StatusReasonForbidden
			if test.name == "API unavailable" {
				wantCode, wantReason = 503, metav1.StatusReasonServiceUnavailable
			}
			if result.Allowed || result.Result == nil || result.Result.Code != wantCode || result.Result.Reason != wantReason || len(result.Patch) != 0 {
				t.Fatalf("untrusted target accepted: %+v", result)
			}
			if strings.Contains(logs.String(), runID) || strings.Contains(logs.String(), "sensitive token") || strings.Contains(logs.String(), namespaceUID) {
				t.Fatal("private identity/error logged")
			}
		})
	}
}

func TestOutsideScopeDoesNotReadDependenciesOrMutate(t *testing.T) {
	config, reader := fixtureConfig(t)
	reader.err = errors.New("must not be consulted")
	handler, err := modeldevwebhook.New(config, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	for _, change := range []func(*admissionv1.AdmissionReview){

		func(b *admissionv1.AdmissionReview) { b.Request.Operation = admissionv1.Update },
		func(b *admissionv1.AdmissionReview) { b.Request.Resource.Resource = "secrets" },
		func(b *admissionv1.AdmissionReview) {
			changePod(t, b, func(p *corev1.Pod) {
				p.Spec.ServiceAccountName = "default"
				p.Labels = nil
				p.Annotations = nil
				p.Finalizers = nil
				p.Spec.Volumes = nil
			})
		},
	} {
		body := request(t, config, "modeldev-mlp-cpu", "prepare")
		change(&body)
		result := call(t, server, body)
		if !result.Allowed || len(result.Patch) != 0 {
			t.Fatal("outside scope changed")
		}
	}
	if result := call(t, server, request(t, config, "modeldev-mlp-cpu", "export")); result.Allowed {
		t.Fatal("CPU profile accepted FINETUNING role")
	}
}

func TestBoundedConcurrencyBodyDeadlineAndReadiness(t *testing.T) {
	config, reader := fixtureConfig(t)
	config.MaxConcurrentRequests = 1
	reader.entered, reader.block = make(chan struct{}), make(chan struct{})
	handler, err := modeldevwebhook.New(config, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	body := request(t, config, "modeldev-mlp-cpu", "prepare")
	raw, _ := json.Marshal(body)
	finished := make(chan error, 1)
	go func() {
		response, err := server.Client().Post(server.URL+"/mutate", "application/json", bytes.NewReader(raw))
		if err == nil {
			_, err = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		finished <- err
	}()
	select {
	case <-reader.entered:
	case <-time.After(time.Second):
		t.Fatal("identity synchronization point absent")
	}
	response, err := server.Client().Post(server.URL+"/mutate", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatal("concurrency not bounded")
	}
	close(reader.block)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	reader.block = nil
	for _, body := range []string{"not-json", strings.Repeat("x", (1<<20)+1), string(raw) + " {}"} {
		response, err := server.Client().Post(server.URL+"/mutate", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatal("invalid or oversized body accepted")
		}
	}
	reader.err = errors.New("not authorized")
	response, err = server.Client().Get(server.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("global readiness depends on a tenant")
	}
	config.RequestTimeoutMilliseconds, config.DependencyTimeoutMilliseconds = 200, 50
	reader.block = make(chan struct{})
	reader.err = nil
	reader.entered = nil
	handler, err = modeldevwebhook.New(config, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	second := httptest.NewTLSServer(handler)
	defer second.Close()
	started := time.Now()
	result := call(t, second, request(t, config, "modeldev-mlp-cpu", "prepare"))
	if result.Allowed || time.Since(started) > time.Second {
		t.Fatal("dependency deadline not enforced")
	}
}

func TestConfigFreezesReleaseAndOptionalLivePipelineVersion(t *testing.T) {
	config, reader := fixtureConfig(t)
	config.Profiles[0].PipelineVersionLabel = "pipelines.example.test/version"
	handler, err := modeldevwebhook.New(config, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	if call(t, server, request(t, config, "modeldev-mlp-cpu", "prepare")).Allowed {
		t.Fatal("missing frozen version metadata accepted")
	}
	labels := reader.workflow.GetLabels()
	labels[config.Profiles[0].PipelineVersionLabel] = conformance.MaterialV21().Implementation.PipelineVersionID
	reader.workflow.SetLabels(labels)
	if !call(t, server, request(t, config, "modeldev-mlp-cpu", "prepare")).Allowed {
		t.Fatal("original frozen version refused")
	}
	for _, change := range []func(*modeldevwebhook.Config){
		func(c *modeldevwebhook.Config) { c.Profiles[0].MaterialSHA256 = strings.Repeat("0", 64) },
		func(c *modeldevwebhook.Config) { c.Profiles[0].ComponentImage = "mutable:tag" },
		func(c *modeldevwebhook.Config) { c.Profiles[0].RunAsUser = 0 },
		func(c *modeldevwebhook.Config) { c.ClusterID = "invalid" },
		func(c *modeldevwebhook.Config) { c.Profiles[1].MaterialFile = "" },
	} {
		fresh, _ := fixtureConfig(t)
		change(&fresh)
		if fresh.Validate() == nil {
			t.Fatal("unfrozen/unsafe configuration accepted")
		}
	}
	raw, _ := json.Marshal(config)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := modeldevwebhook.LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(string(raw), `"schema_version":`, `"schema_version":"ani.modeldev.webhook.v1","schema_version":`, 1), strings.Replace(string(raw), `"cluster_id":`, `"cluster_id":"invalid","cluster_id":`, 1), strings.TrimSuffix(string(raw), "}") + `,"untrusted_policy":true}`} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := modeldevwebhook.LoadConfig(path); err == nil {
			t.Fatal("duplicate or unknown configuration key accepted")
		}
	}
}

func TestPinnedBackendDriversRetainOnlyOriginalExitEvidence(t *testing.T) {
	config, reader := fixtureConfig(t)
	driverImage := "example.invalid/kfp-driver@sha256:" + strings.Repeat("b", 64)
	config.Driver = &modeldevwebhook.DriverBinding{Image: driverImage, Command: []string{"driver"}, AllowedSupportImages: []string{supportImage}}
	handler, err := modeldevwebhook.New(config, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	for _, kind := range []string{"ROOT_DAG", "DAG", "CONTAINER"} {
		body := request(t, config, "modeldev-mlp-cpu", "workspace-name")
		changePod(t, &body, func(p *corev1.Pod) {
			delete(p.Labels, modeldevwebhook.ProfileLabel)
			delete(p.Labels, modeldevwebhook.RoleLabel)
			p.Spec.Containers[0].Image = driverImage
			p.Spec.Containers[0].Command = []string{"driver"}
			p.Spec.Containers[0].Args = []string{"--type", kind, "--run_id", runID}
		})
		result := call(t, server, body)
		if !result.Allowed || len(result.Patch) == 0 {
			t.Fatal("real backend driver refused or not retained")
		}
		var operations []map[string]any
		_ = json.Unmarshal(result.Patch, &operations)
		for _, operation := range operations {
			if strings.HasPrefix(operation["path"].(string), "/spec") {
				t.Fatal("backend driver compute policy changed")
			}
		}
		patch, err := jsonpatch.DecodePatch(result.Patch)
		if err != nil {
			t.Fatal(err)
		}
		mutated, err := patch.Apply(body.Request.Object.Raw)
		if err != nil {
			t.Fatal(err)
		}
		var pod corev1.Pod
		_ = json.Unmarshal(mutated, &pod)
		if pod.Labels[modeldevwebhook.RoleLabel] != "driver" || len(pod.Finalizers) != 2 || pod.Spec.Containers[0].SecurityContext != nil {
			t.Fatal("driver evidence or unchanged backend security lost")
		}
		body.Request.Object.Raw = mutated
		if replay := call(t, server, body); !replay.Allowed || len(replay.Patch) != 0 {
			t.Fatal("driver replay not idempotent")
		}
		for _, mutate := range []func(*corev1.Pod){
			func(p *corev1.Pod) {
				yes := true
				p.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: &yes}
			},
			func(p *corev1.Pod) {
				p.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"SYS_ADMIN"}}}
			},
			func(p *corev1.Pod) { p.Spec.Containers[0].Command = []string{"sh"} },
			func(p *corev1.Pod) {
				p.Spec.Containers[0].Args = append(p.Spec.Containers[0].Args, "--run_id", namespaceUID)
			},
			func(p *corev1.Pod) { p.Spec.Containers[0].Args = []string{"--type", "OTHER", "--run_id", runID} },
			func(p *corev1.Pod) { p.Labels[modeldevwebhook.ProfileLabel] = "modeldev-mlp-cpu" },
			func(p *corev1.Pod) {
				p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "ani-kfp-workspace-test"}}})
			},
		} {
			bad := body
			bad.Request = body.Request.DeepCopy()
			changePod(t, &bad, mutate)
			if call(t, server, bad).Allowed {
				t.Fatal("untrusted backend driver accepted")
			}
		}
	}
}

func TestPinnedBackendDriverArgoEmissaryCommand(t *testing.T) {
	config, reader := fixtureConfig(t)
	driverImage := "example.invalid/kfp-driver@sha256:" + strings.Repeat("b", 64)
	config.Driver = &modeldevwebhook.DriverBinding{Image: driverImage, Command: []string{"driver"}, AllowedSupportImages: []string{supportImage}}
	handler, err := modeldevwebhook.New(config, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	// Argo v3.7.3 workflowpod.go wraps the original command after all
	// substitutions. The installed controller uses these default log options.
	command := []string{"/var/run/argo/argoexec", "emissary", "--loglevel", "info", "--log-format", "text", "--gloglevel", "0", "--", "driver"}
	for _, kind := range []string{"ROOT_DAG", "DAG", "CONTAINER"} {
		t.Run(kind, func(t *testing.T) {
			body := request(t, config, "modeldev-mlp-cpu", "workspace-name")
			changePod(t, &body, func(p *corev1.Pod) {
				delete(p.Labels, modeldevwebhook.ProfileLabel)
				delete(p.Labels, modeldevwebhook.RoleLabel)
				p.Spec.Containers[0].Image = driverImage
				p.Spec.Containers[0].Command = append([]string(nil), command...)
				p.Spec.Containers[0].Args = []string{"--type", kind, "--run_id", runID}
			})
			result := call(t, server, body)
			if !result.Allowed || len(result.Patch) == 0 {
				t.Fatalf("canonical Argo emissary driver refused: %v", result.Result)
			}
			var operations []map[string]any
			if err := json.Unmarshal(result.Patch, &operations); err != nil {
				t.Fatal(err)
			}
			for _, operation := range operations {
				if strings.HasPrefix(operation["path"].(string), "/spec") {
					t.Fatal("driver wrapping or backend compute policy changed")
				}
			}
			patch, err := jsonpatch.DecodePatch(result.Patch)
			if err != nil {
				t.Fatal(err)
			}
			mutated, err := patch.Apply(body.Request.Object.Raw)
			if err != nil {
				t.Fatal(err)
			}
			body.Request.Object.Raw = mutated
			if replay := call(t, server, body); !replay.Allowed || len(replay.Patch) != 0 {
				t.Fatal("wrapped driver replay not idempotent")
			}
			logging := body
			logging.Request = body.Request.DeepCopy()
			changePod(t, &logging, func(p *corev1.Pod) {
				p.Spec.Containers[0].Command[3] = "warning"
				p.Spec.Containers[0].Command[5] = "json"
				p.Spec.Containers[0].Command[7] = "2"
			})
			if result := call(t, server, logging); !result.Allowed || len(result.Patch) != 0 {
				t.Fatal("canonical controller log options refused or compute policy changed")
			}
			for _, test := range []struct {
				name   string
				mutate func(*corev1.Pod)
			}{
				{"other-wrapper", func(p *corev1.Pod) { p.Spec.Containers[0].Command[0] = "/usr/bin/argoexec" }},
				{"other-subcommand", func(p *corev1.Pod) { p.Spec.Containers[0].Command[1] = "wait" }},
				{"unknown-option", func(p *corev1.Pod) { p.Spec.Containers[0].Command[2] = "--config" }},
				{"option-order", func(p *corev1.Pod) {
					p.Spec.Containers[0].Command[2], p.Spec.Containers[0].Command[4] = "--log-format", "--loglevel"
				}},
				{"invalid-log-level", func(p *corev1.Pod) { p.Spec.Containers[0].Command[3] = "info;driver" }},
				{"invalid-log-format", func(p *corev1.Pod) { p.Spec.Containers[0].Command[5] = "yaml" }},
				{"noncanonical-glog-level", func(p *corev1.Pod) { p.Spec.Containers[0].Command[7] = "00" }},
				{"missing-delimiter", func(p *corev1.Pod) { p.Spec.Containers[0].Command[8] = "driver" }},
				{"other-executable", func(p *corev1.Pod) { p.Spec.Containers[0].Command[9] = "sh" }},
				{"extra-command", func(p *corev1.Pod) { p.Spec.Containers[0].Command = append(p.Spec.Containers[0].Command, "other") }},
				{"extra-prefix", func(p *corev1.Pod) {
					p.Spec.Containers[0].Command = append([]string{"sh", "-c"}, p.Spec.Containers[0].Command...)
				}},
				{"other-image", func(p *corev1.Pod) { p.Spec.Containers[0].Image = supportImage }},
				{"other-run", func(p *corev1.Pod) { p.Spec.Containers[0].Args[3] = namespaceUID }},
				{"other-owner", func(p *corev1.Pod) { p.OwnerReferences[0].UID = types.UID(namespaceUID) }},
			} {
				t.Run(test.name, func(t *testing.T) {
					bad := body
					bad.Request = body.Request.DeepCopy()
					changePod(t, &bad, test.mutate)
					if call(t, server, bad).Allowed {
						t.Fatal("untrusted wrapped driver accepted")
					}
				})
			}
		})
	}
}

func TestNewFreezesCallerOwnedProfileAndDriverSlices(t *testing.T) {
	config, reader := fixtureConfig(t)
	driverImage := "example.invalid/kfp-driver@sha256:" + strings.Repeat("b", 64)
	config.Driver = &modeldevwebhook.DriverBinding{Image: driverImage, Command: []string{"driver"}, AllowedSupportImages: []string{supportImage}}
	body := request(t, config, "modeldev-mlp-cpu", "prepare")
	driverBody := request(t, config, "modeldev-mlp-cpu", "workspace-name")
	changePod(t, &driverBody, func(p *corev1.Pod) {
		delete(p.Labels, modeldevwebhook.ProfileLabel)
		delete(p.Labels, modeldevwebhook.RoleLabel)
		p.Spec.Containers[0].Image = driverImage
		p.Spec.Containers[0].Command = []string{"driver"}
		p.Spec.Containers[0].Args = []string{"--type", "ROOT_DAG", "--run_id", runID}
	})
	handler, err := modeldevwebhook.New(config, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	config.ControllerPrincipals[0] = "other"
	config.Driver.Command[0] = "sh"
	config.Driver.AllowedSupportImages[0] = "other:tag"
	config.Driver.Image = "other:tag"
	config.Profiles[0].AllowedSupportImages[0] = "other:tag"
	config.Profiles[0].ComponentImage = "other:tag"
	if !call(t, server, body).Allowed || !call(t, server, driverBody).Allowed {
		t.Fatal("caller changed frozen handler authority")
	}
}

func TestContainerSeccompCannotOverrideFrozenDefault(t *testing.T) {
	config, reader := fixtureConfig(t)
	handler, err := modeldevwebhook.New(config, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	for _, profile := range []string{"modeldev-mlp-cpu", "modeldev-sft-lora-cpu"} {
		body := request(t, config, profile, "prepare")
		changePod(t, &body, func(p *corev1.Pod) {
			p.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined}}
			name := "untrusted.json"
			p.Spec.InitContainers[0].SecurityContext = &corev1.SecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeLocalhost, LocalhostProfile: &name}}
		})
		result := call(t, server, body)
		if !result.Allowed {
			t.Fatal("safe seccomp correction refused")
		}
		patch, err := jsonpatch.DecodePatch(result.Patch)
		if err != nil {
			t.Fatal(err)
		}
		mutated, err := patch.Apply(body.Request.Object.Raw)
		if err != nil {
			t.Fatal(err)
		}
		var pod corev1.Pod
		if err := json.Unmarshal(mutated, &pod); err != nil {
			t.Fatal(err)
		}
		for _, container := range append(pod.Spec.Containers, pod.Spec.InitContainers...) {
			if container.SecurityContext.SeccompProfile == nil || container.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault || container.SecurityContext.SeccompProfile.LocalhostProfile != nil {
				t.Fatal("container overrides RuntimeDefault")
			}
		}
	}
}

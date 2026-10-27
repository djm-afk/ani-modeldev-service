package modeldevwebhook_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server/modeldevwebhook"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kjson "k8s.io/apimachinery/pkg/util/json"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
)

type trainingIdentities struct {
	*identities
	objects map[string]*unstructured.Unstructured
}

func (reader *trainingIdentities) TrainingObject(_ context.Context, namespace, kind, name string) (*unstructured.Unstructured, error) {
	object := reader.objects[kind+"/"+name]
	if object == nil || object.GetNamespace() != namespace {
		return nil, errors.New("external object absent")
	}
	return object.DeepCopy(), nil
}

func trainingAdmissionFixture(t *testing.T) (modeldevwebhook.Config, *trainingIdentities, admissionv1.AdmissionReview) {
	t.Helper()
	config, identity := fixtureConfig(t)
	config.Bindings[0].Training = &modeldevwebhook.TrainingBinding{ServiceAccount: "training-worker", Creator: "system:serviceaccount:kube-system:job-controller"}
	snapshot := conformance.SnapshotV1()
	snapshot.Environment.NamespaceName = "test-tenant"
	snapshot.Environment.NamespaceUID = namespaceUID
	snapshot.Environment.Identities.TrainerServiceAccount = "training-worker"
	accepted := time.Now().UTC().Truncate(time.Microsecond)
	snapshot.DeadlineAt = accepted.Add(time.Hour)
	a := admissionfixture.Bind(t, biz.Admission{TenantID: "11111111-1111-4111-8111-111111111111", OperationID: "33333333-3333-4333-8333-333333333333", ExecutionID: "22222222-2222-4222-8222-222222222222", Actor: "governance:user:42", Intent: conformance.IntentV1(), Snapshot: snapshot, AcceptedAt: accepted})
	var spec map[string]any
	if kjson.Unmarshal(a.Snapshot.RuntimePlan.ExpectedSpec, &spec) != nil {
		t.Fatal("invalid runtime fixture")
	}
	annotations := map[string]any{"modeldev.ani.io/tenant-id": a.TenantID, "modeldev.ani.io/execution-id": a.ExecutionID, "modeldev.ani.io/create-operation-id": a.OperationID, "modeldev.ani.io/execution-spec-sha256": a.SpecHash, "modeldev.ani.io/runtime-spec-sha256": a.Snapshot.RuntimePlan.ExpectedSpecSHA256, "modeldev.ani.io/workspace-uid": "original-pvc", "modeldev.ani.io/runtime-uid": "original-runtime"}
	ref := func(api, kind, name, uid string) []any {
		return []any{map[string]any{"apiVersion": api, "kind": kind, "name": name, "uid": uid, "controller": true}}
	}
	object := func(api, kind, name, uid string, refs []any) *unstructured.Unstructured {
		metadata := map[string]any{"namespace": "test-tenant", "name": name, "uid": uid}
		if refs != nil {
			metadata["ownerReferences"] = refs
		}
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": api, "kind": kind, "metadata": metadata}}
	}
	name := "md-" + a.ExecutionID
	train := object("trainer.kubeflow.org/v1alpha1", "TrainJob", name, "original-train", nil)
	train.Object["metadata"].(map[string]any)["annotations"] = annotations
	train.Object["spec"] = map[string]any{"runtimeRef": map[string]any{"apiGroup": "trainer.kubeflow.org", "kind": "TrainingRuntime", "name": a.Snapshot.RuntimePlan.Name}}
	template := object("trainer.kubeflow.org/v1alpha1", "TrainingRuntime", a.Snapshot.RuntimePlan.Name, "original-runtime", nil)
	template.Object["metadata"].(map[string]any)["annotations"] = annotations
	template.Object["spec"] = spec
	set := object("jobset.x-k8s.io/v1alpha2", "JobSet", name, "original-set", ref(train.GetAPIVersion(), train.GetKind(), name, string(train.GetUID())))
	job := object("batch/v1", "Job", name+"-node-0", "original-job", ref(set.GetAPIVersion(), set.GetKind(), name, string(set.GetUID())))
	jobs, _, _ := unstructured.NestedSlice(spec, "template", "spec", "replicatedJobs")
	podTemplate, _, _ := unstructured.NestedMap(jobs[0].(map[string]any), "template", "spec", "template")
	pod := object("v1", "Pod", "training-node", "", ref(job.GetAPIVersion(), job.GetKind(), job.GetName(), string(job.GetUID())))
	pod.Object["spec"] = podTemplate["spec"]
	metadata := podTemplate["metadata"].(map[string]any)
	for key, value := range metadata {
		pod.Object["metadata"].(map[string]any)[key] = value
	}
	body := request(t, config, "cpu-v1", "prepare")
	body.Request.UserInfo.Username = config.Bindings[0].Training.Creator
	body.Request.Object.Raw, _ = json.Marshal(pod.Object)
	reader := &trainingIdentities{identities: identity, objects: map[string]*unstructured.Unstructured{}}
	// Worker admission requires the namespace tenant label to agree with the
	// TrainJob annotation.
	identity.namespace.Labels[modeldevwebhook.NamespaceTenantLabel] = a.TenantID
	for _, value := range []*unstructured.Unstructured{train, set, job, template} {
		reader.objects[value.GetKind()+"/"+value.GetName()] = value
	}
	return config, reader, body
}

func TestActualTrainingGraphAdmissionRejectsEffectiveResourceAndIdentityDrift(t *testing.T) {
	for _, mode := range []string{"original", "sidecar", "init", "ephemeral", "overhead", "pod-resources", "gpu-injection", "memory-limit", "env-secret", "creator", "runtime-uid", "runtime-spec", "job-owner", "image", "privileged", "workspace", "retention"} {
		t.Run(mode, func(t *testing.T) {
			config, reader, body := trainingAdmissionFixture(t)
			changePod(t, &body, func(pod *corev1.Pod) {
				switch mode {
				case "sidecar":
					pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: "unknown"})
				case "init":
					pod.Spec.InitContainers = []corev1.Container{{Name: "unknown"}}
				case "ephemeral":
					pod.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "unknown"}}}
				case "overhead":
					pod.Spec.Overhead = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1m")}
				case "gpu-injection":
					pod.Spec.Containers[0].Resources.Requests["volcano.sh/vgpu-number"] = resource.MustParse("1")
				case "memory-limit":
					pod.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("3Gi")
				case "env-secret":
					pod.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "PYTHONPATH", Value: "/untrusted"}}
				case "image":
					pod.Spec.Containers[0].Image = "arbitrary:latest"
				case "privileged":
					yes := true
					pod.Spec.Containers[0].SecurityContext.Privileged = &yes
				case "workspace":
					pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = "another-workspace"
				case "retention":
					pod.Finalizers = nil
				}
			})
			if mode == "pod-resources" {
				var raw map[string]any
				_ = json.Unmarshal(body.Request.Object.Raw, &raw)
				raw["spec"].(map[string]any)["resources"] = map[string]any{"requests": map[string]any{"cpu": "2"}}
				body.Request.Object.Raw, _ = json.Marshal(raw)
			}
			if mode == "creator" {
				body.Request.UserInfo.Username = "system:serviceaccount:test-tenant:training-worker"
			}
			for _, value := range reader.objects {
				if mode == "runtime-uid" && value.GetKind() == "TrainingRuntime" {
					value.SetUID("replacement-runtime")
				}
				if mode == "runtime-spec" && value.GetKind() == "TrainingRuntime" {
					_ = unstructured.SetNestedField(value.Object, int64(2), "spec", "mlPolicy", "numNodes")
				}
				if mode == "job-owner" && value.GetKind() == "Job" {
					refs := value.GetOwnerReferences()
					refs[0].UID = "replacement-set"
					value.SetOwnerReferences(refs)
				}
			}
			handler, err := modeldevwebhook.New(config, reader, nil)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewTLSServer(handler)
			defer server.Close()
			response := call(t, server, body)
			if response.Allowed != (mode == "original") {
				t.Fatalf("training %s allowed=%v status=%v", mode, response.Allowed, response.Result)
			}
			// The accepted path patches the three metric labels; every rejection
			// must stay patch-free.
			if (mode == "original") != (len(response.Patch) != 0) {
				t.Fatalf("training %s patch=%d", mode, len(response.Patch))
			}
		})
	}
}

func TestTrainingAdmissionPatchesMetricLabelsFromVerifiedSources(t *testing.T) {
	config, reader, body := trainingAdmissionFixture(t)
	handler, err := modeldevwebhook.New(config, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	var original map[string]any
	if json.Unmarshal(body.Request.Object.Raw, &original) != nil {
		t.Fatal("invalid request")
	}
	response := call(t, server, body)
	if !response.Allowed || response.PatchType == nil || *response.PatchType != admissionv1.PatchTypeJSONPatch {
		t.Fatalf("worker admission allowed=%v patchType=%v", response.Allowed, response.PatchType)
	}
	patch, err := jsonpatch.DecodePatch(response.Patch)
	if err != nil {
		t.Fatal(err)
	}
	patched, err := patch.Apply(body.Request.Object.Raw)
	if err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if json.Unmarshal(patched, &pod) != nil {
		t.Fatal("invalid patched pod")
	}
	for key, want := range map[string]string{modeldevwebhook.TenantLabel: "11111111-1111-4111-8111-111111111111", modeldevwebhook.InstanceLabel: "22222222-2222-4222-8222-222222222222", modeldevwebhook.WorkloadKindLabel: modeldevwebhook.WorkloadKindTrainer} {
		if pod.Labels[key] != want {
			t.Fatalf("label %s = %q, want %q", key, pod.Labels[key], want)
		}
	}
	// The patch must not disturb the frozen Runtime identity or other metadata.
	if string(original["metadata"].(map[string]any)["namespace"].(string)) != "test-tenant" {
		t.Fatal("namespace rewritten")
	}
	if len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Name != "node" {
		t.Fatal("container contract changed")
	}
}

func TestTrainingAdmissionRejectsRequestedMetricLabelSpoofing(t *testing.T) {
	config, reader, body := trainingAdmissionFixture(t)
	var raw map[string]any
	if json.Unmarshal(body.Request.Object.Raw, &raw) != nil {
		t.Fatal("invalid request")
	}
	metadata := raw["metadata"].(map[string]any)
	labels, _ := metadata["labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
	}
	// A request may not attribute the Pod to another tenant or execution.
	labels[modeldevwebhook.TenantLabel] = "00000000-0000-4000-8000-000000000000"
	labels[modeldevwebhook.InstanceLabel] = "11111111-1111-4111-8111-111111111111"
	metadata["labels"] = labels
	body.Request.Object.Raw, _ = json.Marshal(raw)
	handler, err := modeldevwebhook.New(config, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	response := call(t, server, body)
	if !response.Allowed {
		t.Fatalf("worker admission denied: %v", response.Result)
	}
	patch, err := jsonpatch.DecodePatch(response.Patch)
	if err != nil {
		t.Fatal(err)
	}
	patched, err := patch.Apply(body.Request.Object.Raw)
	if err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if json.Unmarshal(patched, &pod) != nil {
		t.Fatal("invalid patched pod")
	}
	if pod.Labels[modeldevwebhook.TenantLabel] != "11111111-1111-4111-8111-111111111111" || pod.Labels[modeldevwebhook.InstanceLabel] != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("requested labels survived: %v", pod.Labels)
	}
}

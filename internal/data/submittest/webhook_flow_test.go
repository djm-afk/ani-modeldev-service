//go:build cpu_mainflow

package submittest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server/modeldevwebhook"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// The external API boundary applies the production webhook's actual HTTPS
// AdmissionReview patch. It does not replace ModelDev business logic or storage.
type flowIdentityReader struct{ client dynamic.Interface }

func (reader flowIdentityReader) Namespace(ctx context.Context, name string) (*corev1.Namespace, error) {
	object, err := reader.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	var namespace corev1.Namespace
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &namespace)
	return &namespace, err
}
func (reader flowIdentityReader) Workflow(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	return reader.client.Resource(schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

func (f *completeFixture) admitModelDevSteps(t *testing.T) {
	t.Helper()
	ns := f.workspace.NamespaceName
	f.objects["/api/v1/namespaces/"+ns]["metadata"].(map[string]any)["labels"] = map[string]any{"ani.io/managed-by": "modeldev-flow-fixture"}
	f.objects["/apis/argoproj.io/v1alpha1/namespaces/"+ns+"/workflows/main-flow"]["metadata"].(map[string]any)["labels"] = map[string]any{"pipeline/runid": completeRunID}
	oldPVC := f.workspace.PVCName
	f.workspace.PVCName = "ani-kfp-workspace-" + f.request.Admission.ExecutionID
	pvcBase := "/api/v1/namespaces/" + ns + "/persistentvolumeclaims/"
	pvc := f.objects[pvcBase+oldPVC]
	pvc["metadata"].(map[string]any)["name"] = f.workspace.PVCName
	f.objects[pvcBase+f.workspace.PVCName] = pvc
	delete(f.objects, pvcBase+oldPVC)
	snapshot := f.request.Admission.Snapshot
	release := conformance.ReleaseV1()
	release.ReleaseID, release.PresetID = snapshot.Release.ReleaseID, snapshot.Release.PresetID
	release.PipelineID, release.PipelineVersionID, release.PipelineIRSHA256 = snapshot.Release.PipelineID, snapshot.Release.PipelineVersionID, snapshot.Release.PipelineIRSHA256
	release.Runtime, release.Resources, release.Workspace, release.OutputContract = snapshot.Release.Runtime, snapshot.Resources, snapshot.Workspace, snapshot.OutputContract
	release.Program.ImageVersionID, release.Program.ImageDigest, release.Program.Command = snapshot.Program.ImageVersionID, f.image, snapshot.Program.Command
	raw, err := release.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	releaseFile := filepath.Join(t.TempDir(), "release.json")
	if err := os.WriteFile(releaseFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	componentImage := "registry.example.test/modeldev-step@sha256:" + strings.Repeat("a", 64)
	supportImage := "registry.example.test/argoexec@sha256:" + strings.Repeat("b", 64)
	driverImage := "registry.example.test/kfp-driver@sha256:" + strings.Repeat("c", 64)
	config := modeldevwebhook.Config{SchemaVersion: modeldevwebhook.ConfigSchemaVersion,
		Bindings: []modeldevwebhook.NamespaceBinding{{Namespace: ns, NamespaceUID: f.workspace.NamespaceUID, OwnerLabel: modeldevwebhook.LabelBinding{Key: "ani.io/managed-by", Value: "modeldev-flow-fixture"}, ArgoCreator: "system:serviceaccount:kubeflow:argo", StepServiceAccount: snapshot.Environment.Identities.KFPStepServiceAccount, Profiles: []string{"cpu-v1"}, Driver: &modeldevwebhook.DriverBinding{Image: driverImage, Command: []string{"driver"}, AllowedSupportImages: []string{supportImage}}}},
		Profiles: []modeldevwebhook.Profile{{ID: "cpu-v1", Kind: "GENERAL_TRAINING", ReleaseFile: releaseFile, ReleaseSHA256: completeHash(raw), ComponentImage: componentImage, AllowedSupportImages: []string{supportImage}, RunAsUser: 10001, RunAsGroup: 10001, FSGroup: 10001, EphemeralStorageRequest: "1Gi", EphemeralStorageLimit: "2Gi"}}}
	client, err := dynamic.NewForConfig(&rest.Config{Host: f.kube.URL, BearerToken: "synthetic-kube-owner", Timeout: 5 * time.Second, TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.kube.Certificate().Raw})}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := modeldevwebhook.New(config, flowIdentityReader{client}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	endpoint := httptest.NewTLSServer(handler)
	defer endpoint.Close()
	f.objects["/api/v1/namespaces/"+ns+"/pods/main-driver"] = map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "main-driver", "namespace": ns, "uid": completeStepUID("driver"), "resourceVersion": "1", "ownerReferences": []any{map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "name": "main-flow", "uid": completeWorkflowUID, "controller": true}}}, "spec": map[string]any{"serviceAccountName": snapshot.Environment.Identities.KFPStepServiceAccount, "containers": []any{map[string]any{"name": "main", "image": driverImage, "command": []string{"driver"}, "args": []string{"--type", "ROOT_DAG", "--run_id", completeRunID}}}}, "status": map[string]any{"phase": "Running"}}
	for _, role := range []string{"prepare", "train-wait", "collect", "publish", "close", "driver"} {
		path := "/api/v1/namespaces/" + ns + "/pods/main-" + role
		pod := f.objects[path]
		meta, spec := pod["metadata"].(map[string]any), pod["spec"].(map[string]any)
		meta["labels"] = map[string]any{"pipelines.kubeflow.org/v2_component": "true", "pipeline/runid": completeRunID, "workflows.argoproj.io/workflow": "main-flow", modeldevwebhook.ProfileLabel: "cpu-v1", modeldevwebhook.RoleLabel: role}
		containers := spec["containers"].([]any)
		main := containers[0].(map[string]any)
		main["image"] = componentImage
		if role == "driver" {
			main["image"] = driverImage
			delete(meta["labels"].(map[string]any), modeldevwebhook.ProfileLabel)
			delete(meta["labels"].(map[string]any), modeldevwebhook.RoleLabel)
		}
		if role == "train-wait" {
			delete(spec, "volumes")
			delete(main, "volumeMounts")
		}
		if volumes, ok := spec["volumes"].([]any); ok {
			for _, item := range volumes {
				item.(map[string]any)["persistentVolumeClaim"].(map[string]any)["claimName"] = f.workspace.PVCName
			}
		}
		spec["containers"] = append(containers, map[string]any{"name": "wait", "image": supportImage})
		podRaw, _ := json.Marshal(pod)
		review := admissionv1.AdmissionReview{TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"}, Request: &admissionv1.AdmissionRequest{UID: types.UID(uuid.NewString()), Kind: metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}, Resource: metav1.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespace: ns, Operation: admissionv1.Create, UserInfo: authenticationv1.UserInfo{Username: "system:serviceaccount:kubeflow:argo"}, Object: runtime.RawExtension{Raw: podRaw}}}
		body, _ := json.Marshal(review)
		response, err := endpoint.Client().Post(endpoint.URL+"/mutate", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		var result admissionv1.AdmissionReview
		err = json.NewDecoder(response.Body).Decode(&result)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || result.Response == nil || !result.Response.Allowed || result.Response.UID != review.Request.UID {
			t.Fatalf("production webhook refused %s: %+v %v", role, result.Response, err)
		}
		patch, err := jsonpatch.DecodePatch(result.Response.Patch)
		if err != nil {
			t.Fatal(err)
		}
		mutated, err := patch.Apply(podRaw)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(mutated, &pod); err != nil {
			t.Fatal(err)
		}
		f.objects[path] = pod
	}
	t.Log("WEBHOOK_FLOW: production HTTPS AdmissionReview mutated all CPU steps; actual patch applied at Kubernetes boundary")
}

func (f *completeFixture) handleRetainedPodMutation(w http.ResponseWriter, request *http.Request) bool {
	base := "/api/v1/namespaces/" + f.workspace.NamespaceName + "/pods/"
	if !strings.HasPrefix(request.URL.Path, base) || (request.Method != "PATCH" && request.Method != "DELETE") {
		return false
	}
	pod := f.objects[request.URL.Path]
	if pod == nil {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": "NotFound", "code": 404})
		return true
	}
	meta := pod["metadata"].(map[string]any)
	if request.Method == "PATCH" {
		raw, err := io.ReadAll(request.Body)
		patch, patchErr := jsonpatch.DecodePatch(raw)
		before, _ := json.Marshal(pod)
		if err != nil || patchErr != nil {
			w.WriteHeader(400)
			return true
		}
		after, err := patch.Apply(before)
		if err != nil {
			w.WriteHeader(409)
			return true
		}
		if json.Unmarshal(after, &pod) != nil {
			w.WriteHeader(400)
			return true
		}
		pod["metadata"].(map[string]any)["resourceVersion"] = "2"
		f.objects[request.URL.Path] = pod
		_ = json.NewEncoder(w).Encode(pod)
		return true
	}
	var options metav1.DeleteOptions
	if json.NewDecoder(request.Body).Decode(&options) != nil || options.Preconditions == nil || options.Preconditions.UID == nil || string(*options.Preconditions.UID) != meta["uid"] || options.Preconditions.ResourceVersion == nil || *options.Preconditions.ResourceVersion != meta["resourceVersion"] {
		w.WriteHeader(409)
		return true
	}
	if finalizers, ok := meta["finalizers"].([]any); ok && len(finalizers) != 0 {
		w.WriteHeader(409)
		return true
	}
	phase, _ := pod["status"].(map[string]any)["phase"].(string)
	if phase != "Succeeded" && phase != "Failed" {
		w.WriteHeader(409)
		return true
	}
	if f.archivedStepStates == nil {
		f.archivedStepStates = map[string]string{}
	}
	state := "SUCCEEDED"
	if phase == "Failed" {
		state = "FAILED"
	}
	f.archivedStepStates[strings.TrimPrefix(meta["name"].(string), "main-")] = state
	delete(f.objects, request.URL.Path)
	f.stepDeletes++
	_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Success"})
	return true
}

// Argo's wait container belongs to the external-system boundary. Its exit is
// acknowledged only after the real component returned and KFP made the Pod
// terminal, using that same completed-at timestamp.
func (f *completeFixture) completeWaitExitLocked(pod map[string]any) {
	if os.Getenv("ANI_MODELDEV_WEBHOOK_MAINFLOW") == "" {
		return
	}
	status, ok := pod["status"].(map[string]any)
	if !ok || (status["phase"] != "Succeeded" && status["phase"] != "Failed") {
		return
	}
	spec, ok := pod["spec"].(map[string]any)
	if !ok {
		return
	}
	containers, _ := spec["containers"].([]any)
	wait := false
	for _, container := range containers {
		wait = wait || container.(map[string]any)["name"] == "wait"
	}
	if !wait {
		return
	}
	statuses, _ := status["containerStatuses"].([]any)
	if len(statuses) != 1 {
		return
	}
	raw, _ := json.Marshal(statuses[0])
	var completed map[string]any
	_ = json.Unmarshal(raw, &completed)
	completed["name"] = "wait"
	status["containerStatuses"] = append(statuses, completed)
}

func (f *completeFixture) assertStepExitCleanup(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	worker, _ := assembleRecoveryOwner(t, f, pool)
	if _, err := worker.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := lifecycle.New(pool).GetRuntime(ctx, f.request.Admission.TenantID, f.request.Admission.ExecutionID)
	if err != nil || stored.StepExitCleanup == nil || stored.StepExitCleanup.CompletedAt == nil || stored.StepExitCleanup.CompletedAt.IsZero() {
		t.Fatalf("durable step exit cleanup missing after restart: %+v %v", stored.StepExitCleanup, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stepDeletes != 6 {
		t.Fatalf("expected exact original 6 component/driver Pods cleaned, got %d", f.stepDeletes)
	}
	for _, role := range []string{"prepare", "train-wait", "collect", "publish", "close", "driver"} {
		if f.objects["/api/v1/namespaces/"+f.workspace.NamespaceName+"/pods/main-"+role] != nil {
			t.Fatal("retained step Pod leaked", role)
		}
	}
	if f.objects["/api/v1/namespaces/"+f.workspace.NamespaceName+"/persistentvolumeclaims/"+f.workspace.PVCName] == nil || f.objects["/api/v1/namespaces/"+f.workspace.NamespaceName+"/pods/actual-training-pod"] == nil {
		t.Fatal("cleanup deleted retained volume or training log Pod")
	}
	t.Log("WEBHOOK_FLOW: real PostgreSQL CLOSED/exit cleanup receipt survived reconnect; 6 original component/driver Pods removed; training log Pod, PVC and publication retained")
}

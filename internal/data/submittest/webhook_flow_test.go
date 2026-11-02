//go:build cpu_mainflow

package submittest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
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
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/catalogue"
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
func (reader flowIdentityReader) Environment(ctx context.Context, namespace string) (*corev1.ConfigMap, error) {
	object, err := reader.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace(namespace).Get(ctx, "modeldev-execution-environment", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	var result corev1.ConfigMap
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &result)
	return &result, err
}
func (reader flowIdentityReader) ServiceAccount(ctx context.Context, namespace, name string) (*corev1.ServiceAccount, error) {
	object, err := reader.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	var result corev1.ServiceAccount
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &result)
	return &result, err
}

func (reader flowIdentityReader) Workflow(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	return reader.client.Resource(schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

func (f *completeFixture) admitModelDevSteps(t *testing.T) {
	t.Helper()
	ns := f.workspace.NamespaceName
	// Namespace, ServiceAccounts and immutable projection came from real Ensure.
	f.objects["/apis/argoproj.io/v1alpha1/namespaces/"+ns+"/workflows/main-flow"]["metadata"].(map[string]any)["labels"] = map[string]any{"pipeline/runid": completeRunID}
	snapshot := f.request.Admission.Snapshot
	if f.environment == nil {
		t.Fatal("webhook mainflow requires real Ensure before Pod admission")
	}
	material, err := catalogue.ReadImplementationMaterial(context.Background(), f.environment.MaterialsDirectory, "builtin.mlp", "cpu")
	if err != nil {
		t.Fatal(err)
	}
	componentImage := "registry.example.test/modeldev-step@sha256:" + strings.Repeat("a", 64)
	supportImage := "registry.example.test/argoexec@sha256:" + strings.Repeat("b", 64)
	driverImage := "registry.example.test/kfp-driver@sha256:" + strings.Repeat("c", 64)
	config := modeldevwebhook.Config{SchemaVersion: modeldevwebhook.ConfigSchemaVersion, ClusterID: snapshot.Environment.ClusterID, MaterialBundleDigest: f.environment.Config.MaterialBundleDigest, MaterialBundleFile: filepath.Join(f.environment.MaterialsDirectory, "manifest.json"), ArgoCreator: "system:serviceaccount:kubeflow:argo", Training: &modeldevwebhook.TrainingBinding{ServiceAccount: "trainer-workload", Creator: "system:serviceaccount:kube-system:job-controller"}, Driver: &modeldevwebhook.DriverBinding{Image: driverImage, Command: []string{"driver"}, AllowedSupportImages: []string{supportImage}}, ControllerPrincipals: f.environment.Config.ControllerPrincipals}
	for _, profile := range f.environment.Config.Profiles {
		implementation, device, kind := "builtin.mlp", "cpu", "GENERAL_TRAINING"
		if strings.Contains(profile.ID, "sft-lora") {
			implementation, kind = "builtin.sft_lora", "FINETUNING"
		}
		if strings.HasSuffix(profile.ID, "-gpu") {
			device = "gpu"
		}
		image := componentImage
		if kind == "FINETUNING" {
			fine, err := catalogue.ReadImplementationMaterial(context.Background(), f.environment.MaterialsDirectory, implementation, device)
			if err != nil {
				t.Fatal(err)
			}
			image = fine.Finetuning.ExportProgram.ImageDigest
		}
		config.Profiles = append(config.Profiles, modeldevwebhook.Profile{ID: profile.ID, Kind: kind, MaterialFile: filepath.Join(f.environment.MaterialsDirectory, implementation+"."+device+".json"), MaterialSHA256: profile.MaterialSHA256, ComponentImage: image, AllowedSupportImages: []string{supportImage}, RunAsUser: 10001, RunAsGroup: 10001, FSGroup: 10001})
	}
	client, err := dynamic.NewForConfig(&rest.Config{Host: f.kube.URL, BearerToken: "synthetic-kube-owner", RateLimiter: f.kubeRateLimiter, Timeout: 5 * time.Second, TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.kube.Certificate().Raw})}})
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
	f.objects["/api/v1/namespaces/"+ns+"/pods/main-driver"]["metadata"].(map[string]any)["annotations"] = map[string]any{"workflows.argoproj.io/node-id": "main-driver", "workflows.argoproj.io/node-name": "main-flow.driver"}
	// Preserve the external CreatePVC request graph independently of Prepare.
	// Failed input cannot manufacture a durable prepared Workspace authority.
	created := time.Now().UTC().Add(-time.Second).Truncate(time.Second)
	request := map[string]any{"taskInfo": map[string]any{"name": "createpvc"}, "componentRef": map[string]any{"name": "comp-createpvc"}, "dependentTasks": []string{"workspace-name"}, "inputs": map[string]any{"parameters": map[string]any{"pvc_name": map[string]any{"taskOutputParameter": map[string]any{"outputParameterKey": "name", "producerTask": "workspace-name"}}, "size": map[string]any{"runtimeValue": map[string]any{"constant": fmt.Sprint(snapshot.Workspace.CapacityBytes)}}, "storage_class_name": map[string]any{"runtimeValue": map[string]any{"constant": snapshot.Workspace.StorageClass}}, "access_modes": map[string]any{"runtimeValue": map[string]any{"constant": []string{"ReadWriteMany"}}}}}}
	taskJSON, _ := json.Marshal(request)
	componentJSON := `{"executorLabel":"exec-createpvc"}`
	f.createPVCNode = map[string]any{"id": "main-createpvc", "name": "main-flow.createpvc", "displayName": "createpvc", "type": "Pod", "phase": "Succeeded", "templateName": "system-container-driver", "startedAt": created.Add(-time.Second).Format(time.RFC3339Nano), "finishedAt": created.Add(time.Second).Format(time.RFC3339Nano), "inputs": map[string]any{"parameters": []any{map[string]any{"name": "task", "value": string(taskJSON)}, map[string]any{"name": "component", "value": componentJSON}}}}
	pvc := f.objects["/api/v1/namespaces/"+ns+"/persistentvolumeclaims/"+f.workspace.PVCName]
	pvc["metadata"].(map[string]any)["creationTimestamp"] = created.Format(time.RFC3339Nano)
	pvc["spec"].(map[string]any)["accessModes"] = []string{"ReadWriteMany"}
	f.objects["/api/v1/namespaces/"+ns+"/pods/main-createpvc"] = map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "main-createpvc", "namespace": ns, "uid": completeStepUID("createpvc"), "resourceVersion": "1", "annotations": map[string]any{"workflows.argoproj.io/node-id": "main-createpvc", "workflows.argoproj.io/node-name": "main-flow.createpvc"}, "ownerReferences": []any{map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "name": "main-flow", "uid": completeWorkflowUID, "controller": true}}}, "spec": map[string]any{"serviceAccountName": snapshot.Environment.Identities.KFPStepServiceAccount, "containers": []any{map[string]any{"name": "main", "image": driverImage, "command": []string{"driver"}, "args": []string{"--type", "CONTAINER", "--run_id", completeRunID, "--task", string(taskJSON), "--component", componentJSON}}}}, "status": map[string]any{"phase": "Running"}}
	for _, role := range []string{"prepare", "train-wait", "collect", "publish", "close", "driver", "createpvc"} {
		path := "/api/v1/namespaces/" + ns + "/pods/main-" + role
		pod := f.objects[path]
		meta, spec := pod["metadata"].(map[string]any), pod["spec"].(map[string]any)
		meta["labels"] = map[string]any{"pipelines.kubeflow.org/v2_component": "true", "pipeline/runid": completeRunID, "workflows.argoproj.io/workflow": "main-flow", modeldevwebhook.ProfileLabel: "modeldev-mlp-cpu", modeldevwebhook.RoleLabel: role}
		containers := spec["containers"].([]any)
		main := containers[0].(map[string]any)
		main["image"] = componentImage
		if role == "driver" || role == "createpvc" {
			main["image"] = driverImage
			delete(meta["labels"].(map[string]any), modeldevwebhook.ProfileLabel)
			delete(meta["labels"].(map[string]any), modeldevwebhook.RoleLabel)
		} else {
			resources, ok := material.ComponentResources[role]
			if !ok {
				t.Fatalf("current material has no resource profile for %s", role)
			}
			digest, err := resources.Digest()
			if err != nil {
				t.Fatal(err)
			}
			meta["annotations"].(map[string]any)["ani.modeldev/component-resources-sha256"] = digest
			main["resources"] = map[string]any{
				"requests": map[string]string{"cpu": resources.CPURequest, "memory": resources.MemoryRequest, "ephemeral-storage": resources.EphemeralStorageRequest},
				"limits":   map[string]string{"cpu": resources.CPULimit, "memory": resources.MemoryLimit, "ephemeral-storage": resources.EphemeralStorageLimit},
			}
			volumes := []any{map[string]any{"name": "component-tmp", "emptyDir": map[string]any{"sizeLimit": resources.EphemeralStorageLimit}}}
			mounts := []any{map[string]any{"name": "component-tmp", "mountPath": "/tmp"}}
			if role != "train-wait" {
				volumes = append(volumes, map[string]any{"name": "workspace", "persistentVolumeClaim": map[string]any{"claimName": f.workspace.PVCName}})
				mounts = append(mounts, map[string]any{"name": "workspace", "mountPath": "/workspace", "readOnly": role != "prepare"})
			}
			if role == "prepare" {
				mounts = append(mounts, map[string]any{"name": "workspace", "mountPath": "/workspace/work/prepare/tmp", "subPath": "work/prepare/tmp"})
			}
			if role == "collect" || role == "publish" {
				mounts = append(mounts,
					map[string]any{"name": "workspace", "mountPath": "/workspace/publication", "subPath": material.Workspace.PublicationSubpath},
					map[string]any{"name": "workspace", "mountPath": "/workspace/work/" + role, "subPath": material.Workspace.WorkSubpath + "/" + role})
			}
			if role == "prepare" || role == "collect" || role == "publish" {
				work := "/workspace/" + material.Workspace.WorkSubpath + "/" + role
				main["env"] = []any{
					map[string]string{"name": "TMPDIR", "value": work + "/tmp"},
					map[string]string{"name": "HF_HOME", "value": work + "/hf"},
					map[string]string{"name": "HF_DATASETS_CACHE", "value": work + "/hf/datasets"},
					map[string]string{"name": "MPLCONFIGDIR", "value": work + "/matplotlib"},
				}
			}
			spec["volumes"], main["volumeMounts"] = volumes, mounts
		}
		spec["containers"] = append(containers, map[string]any{"name": "wait", "image": supportImage})
		refs := []any{}
		for _, binding := range snapshot.ManagedImageAccess.PullBindings {
			refs = append(refs, map[string]any{"name": binding.SecretName})
		}
		spec["imagePullSecrets"] = refs
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
	f.finishStep("createpvc")
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
	if request.Method == http.MethodDelete && !strings.HasPrefix(meta["name"].(string), "main-") {
		return false // Native training Pods follow the durable compute intent.
	}
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
		f.archivedStepFinishes = map[string]string{}
	}
	state := "SUCCEEDED"
	if phase == "Failed" {
		state = "FAILED"
	}
	if strings.HasPrefix(meta["name"].(string), "main-") {
		role := strings.TrimPrefix(meta["name"].(string), "main-")
		f.archivedStepStates[role] = state
		f.archivedStepFinishes[role] = completePodFinishedAt(pod)
		f.stepDeletes++
	}
	delete(f.objects, request.URL.Path)
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
	if f.stepDeletes != 7 {
		t.Fatalf("expected exact original 7 component/driver Pods cleaned, got %d", f.stepDeletes)
	}
	for _, role := range []string{"prepare", "train-wait", "collect", "publish", "close", "driver", "createpvc"} {
		if f.objects["/api/v1/namespaces/"+f.workspace.NamespaceName+"/pods/main-"+role] != nil {
			t.Fatal("retained step Pod leaked", role)
		}
	}
	if stored.ComputeCleanup == nil || stored.ComputeCleanup.Phase != "APPLIED" || !stored.ComputeCleanup.LogsReady || stored.ComputeCleanup.CompletedAt.IsZero() || len(stored.ComputeCleanup.ConfirmedAbsent) != len(stored.ComputeCleanup.Targets) || len(f.computeDeletes) != len(stored.ComputeCleanup.Targets) {
		t.Fatal("compute cleanup did not durably confirm every original target absent", stored.ComputeCleanup)
	}
	for _, target := range stored.ComputeCleanup.Targets {
		for _, object := range f.objects {
			if object["metadata"].(map[string]any)["uid"] == target.UID {
				t.Fatal("original compute target remained after APPLIED", target.Kind, target.UID)
			}
		}
		matched := 0
		for _, uid := range f.computeDeletes {
			if uid == target.UID {
				matched++
			}
		}
		if matched != 1 {
			t.Fatal("compute target was not deleted exactly once", target.UID)
		}
	}
	if f.objects["/api/v1/namespaces/"+f.workspace.NamespaceName+"/persistentvolumeclaims/"+f.workspace.PVCName] == nil {
		t.Fatal("cleanup deleted the retained work volume")
	}
	t.Log("WEBHOOK_FLOW: real PostgreSQL CLOSED/exit cleanup survived reconnect; 7 original component/driver Pods and all original compute targets removed only after durable logs; PVC and publication retained")
}

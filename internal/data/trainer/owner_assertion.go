package trainer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	accpb "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// OwnerAssertionSigner uses an independent mounted Ed25519 private key. The
// secret file contains one base64-encoded 64-byte private key; ACC receives only
// its separate 32-byte public key. No workload/template contains the secret.
type OwnerAssertionSigner struct {
	client dynamic.Interface
	key    ed25519.PrivateKey
}

func NewOwnerAssertionSigner(client dynamic.Interface, keyFile string) (*OwnerAssertionSigner, error) {
	if client == nil || !filepath.IsAbs(keyFile) {
		return nil, biz.ErrRuntimeNotReady
	}
	raw, err := os.ReadFile(keyFile)
	if err != nil || len(raw) > 4096 {
		return nil, biz.ErrRuntimeNotReady
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(decoded) != ed25519.PrivateKeySize {
		return nil, biz.ErrRuntimeNotReady
	}
	key := ed25519.PrivateKey(decoded)
	if !bytes.Equal(ed25519.NewKeyFromSeed(key.Seed()), key) {
		return nil, biz.ErrRuntimeNotReady
	}
	return &OwnerAssertionSigner{client: client, key: append(ed25519.PrivateKey{}, key...)}, nil
}

// SignPod re-reads the actual API UID and its original controller chain before
// signing. The caller invokes this only for a Pod observed under its accepted
// training plan; a replacement UID or uncertain API result remains unresolved.
func (signer *OwnerAssertionSigner) SignPod(ctx context.Context, plan biz.TrainingPlan, handle biz.TrainingHandle, observed *unstructured.Unstructured) error {
	if signer == nil || signer.client == nil || len(signer.key) != ed25519.PrivateKeySize || ctx == nil || !validTrainingPlan(plan) || plan.Snapshot.ComputeOrigin.DeviceMode != "gpu" || plan.Snapshot.GPU == nil || plan.Snapshot.GPU.ValidateFor(plan.Snapshot.ComputeOrigin) != nil || observed == nil || observed.GetUID() == "" || observed.GetNamespace() != plan.Workspace.NamespaceName || handle.TrainJobUID == "" || handle.NamespaceUID != plan.Workspace.NamespaceUID || handle.PVCUID != plan.Workspace.PVCUID {
		return biz.ErrRuntimeConflict
	}
	pods := signer.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(plan.Workspace.NamespaceName)
	pod, err := pods.Get(ctx, observed.GetName(), metav1.GetOptions{})
	if err != nil {
		return biz.ErrTrainingUnavailable
	}
	if !liveIdentity(pod, "v1", "Pod", plan.Workspace.NamespaceName, observed.GetName(), string(observed.GetUID())) || pod.GetResourceVersion() == "" {
		return biz.ErrRuntimeConflict
	}
	refs := pod.GetOwnerReferences()
	if len(refs) != 1 || refs[0].Controller == nil || !*refs[0].Controller || refs[0].APIVersion != "batch/v1" || refs[0].Kind != "Job" || refs[0].UID == "" {
		return biz.ErrRuntimeConflict
	}
	job, err := signer.client.Resource(schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}).Namespace(plan.Workspace.NamespaceName).Get(ctx, refs[0].Name, metav1.GetOptions{})
	if err != nil {
		return biz.ErrTrainingUnavailable
	}
	if !liveIdentity(job, "batch/v1", "Job", plan.Workspace.NamespaceName, refs[0].Name, string(refs[0].UID)) {
		return biz.ErrRuntimeConflict
	}
	jobRefs := job.GetOwnerReferences()
	if len(jobRefs) != 1 || jobRefs[0].Controller == nil || !*jobRefs[0].Controller || jobRefs[0].APIVersion != "jobset.x-k8s.io/v1alpha2" || jobRefs[0].Kind != "JobSet" || jobRefs[0].Name != plan.Name || jobRefs[0].UID == "" {
		return biz.ErrRuntimeConflict
	}
	set, err := signer.client.Resource(schema.GroupVersionResource{Group: "jobset.x-k8s.io", Version: "v1alpha2", Resource: "jobsets"}).Namespace(plan.Workspace.NamespaceName).Get(ctx, plan.Name, metav1.GetOptions{})
	if err != nil {
		return biz.ErrTrainingUnavailable
	}
	if !liveIdentity(set, "jobset.x-k8s.io/v1alpha2", "JobSet", plan.Workspace.NamespaceName, plan.Name, string(jobRefs[0].UID)) || !controllerIs(set, "trainer.kubeflow.org/v1alpha1", "TrainJob", plan.Name, handle.TrainJobUID) {
		return biz.ErrRuntimeConflict
	}
	vector, err := biz.TrainingResourceVector(plan.Snapshot)
	if err != nil || ValidatePodResources(vector, pod) != nil {
		return biz.ErrRuntimeConflict
	}
	gpu := plan.Snapshot.GPU.Execution
	scheduler, _, _ := unstructured.NestedString(pod.Object, "spec", "schedulerName")
	runtimeClass, _, _ := unstructured.NestedString(pod.Object, "spec", "runtimeClassName")
	account, _, _ := unstructured.NestedString(pod.Object, "spec", "serviceAccountName")
	selectors, _, _ := unstructured.NestedStringMap(pod.Object, "spec", "nodeSelector")
	if scheduler != gpu.SchedulerName || runtimeClass != gpu.RuntimeClassName || account != plan.Snapshot.Environment.Identities.TrainerServiceAccount || !reflect.DeepEqual(selectors, gpu.NodeSelector) {
		return biz.ErrRuntimeConflict
	}
	annotations := pod.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	for key, want := range gpu.PodAnnotations {
		if annotations[key] != want {
			return biz.ErrRuntimeConflict
		}
	}
	labels := pod.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	expectedLabels := map[string]string{"accelerator.ani.io/resource-tenant-id": plan.TenantID, "accelerator.ani.io/owner-service": biz.GPUOwnerService, "accelerator.ani.io/resource-id": plan.ExecutionID, "accelerator.ani.io/create-operation-id": plan.OperationID}
	for key, want := range expectedLabels {
		if have, exists := labels[key]; exists && have != want {
			return biz.ErrRuntimeConflict
		}
		labels[key] = want
	}
	ref := &accpb.GpuUsageRef{TenantId: plan.TenantID, OwnerService: biz.GPUOwnerService, ResourceId: plan.ExecutionID, CreateOperationId: plan.OperationID}
	bytesToSign, err := gpuprotocol.OwnerAssertionSigningBytes(&accpb.PodRef{ClusterId: plan.Snapshot.Environment.ClusterID, Namespace: pod.GetNamespace(), Name: pod.GetName(), Uid: string(pod.GetUID()), ContainerName: "node"}, ref, gpu.ExecutionDigest)
	if err != nil {
		return biz.ErrRuntimeConflict
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(signer.key, bytesToSign))
	if value, exists := annotations["accelerator.ani.io/execution-digest"]; exists && value != gpu.ExecutionDigest {
		return biz.ErrRuntimeConflict
	}
	if value, exists := annotations["accelerator.ani.io/owner-signature"]; exists && value != signature {
		return biz.ErrRuntimeConflict
	}
	// Retry of the exact signed Pod is a read-only success.
	if annotations["accelerator.ani.io/owner-signature"] == signature && annotations["accelerator.ani.io/execution-digest"] == gpu.ExecutionDigest {
		original := pod.GetLabels()
		matched := true
		for key, want := range expectedLabels {
			matched = matched && original[key] == want
		}
		if matched {
			return nil
		}
	}
	annotations["accelerator.ani.io/execution-digest"] = gpu.ExecutionDigest
	annotations["accelerator.ani.io/owner-signature"] = signature
	ownerJSON, _, _ := unstructured.NestedSlice(pod.Object, "metadata", "ownerReferences")
	patch, err := json.Marshal([]map[string]any{{"op": "test", "path": "/metadata/uid", "value": string(pod.GetUID())}, {"op": "test", "path": "/metadata/resourceVersion", "value": pod.GetResourceVersion()}, {"op": "test", "path": "/metadata/ownerReferences", "value": ownerJSON}, {"op": "add", "path": "/metadata/labels", "value": labels}, {"op": "add", "path": "/metadata/annotations", "value": annotations}})
	if err != nil {
		return biz.ErrTrainingUnavailable
	}
	result, err := pods.Patch(ctx, pod.GetName(), types.JSONPatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return biz.ErrTrainingUnavailable
	}
	if !liveIdentity(result, "v1", "Pod", pod.GetNamespace(), pod.GetName(), string(pod.GetUID())) || !reflect.DeepEqual(result.GetOwnerReferences(), pod.GetOwnerReferences()) {
		return biz.ErrRuntimeConflict
	}
	for key, want := range expectedLabels {
		if result.GetLabels()[key] != want {
			return biz.ErrRuntimeConflict
		}
	}
	if result.GetAnnotations()["accelerator.ani.io/execution-digest"] != gpu.ExecutionDigest || result.GetAnnotations()["accelerator.ani.io/owner-signature"] != signature {
		return biz.ErrRuntimeConflict
	}
	return nil
}

package trainer_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	accpb "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	codec "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func ownerSigningFixture(t *testing.T) (biz.TrainingPlan, biz.TrainingHandle, *unstructured.Unstructured, *dynamicfake.FakeDynamicClient, string, ed25519.PublicKey) {
	t.Helper()
	f := newTrainingFixture(t)
	s := f.plan.Snapshot
	selection := &compute.ComputeSelection{SchemaVersion: 1, PresetId: s.ComputeOrigin.ComputePresetID, Code: "gpu-t4", Name: "T4", ComputeType: "gpu", CpuMillis: 1000, MemoryMib: 1024, Gpu: &compute.GpuSelection{ProfileId: "12121212-1212-4212-8212-121212121212", ProfileVersion: 3, SpecDigest: strings.Repeat("a", 64), DevicesPerUnit: 1, ClusterId: s.Environment.ClusterID, PoolId: "13131313-1313-4313-8313-131313131313", Mode: accpb.SupplyMode_SHARED_FIXED, ModelKey: "nvidia-t4", MemoryMibPerDevice: 2048, CoreLimitPercent: 30}}
	var err error
	selection.SelectionDigest, err = compute.SelectionDigest(selection)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := compute.CanonicalSelection(selection)
	if err != nil {
		t.Fatal(err)
	}
	s.ComputeOrigin, err = codec.DecodeComputeSelection(raw, selection.SelectionDigest)
	if err != nil {
		t.Fatal(err)
	}
	e := &accpb.GpuExecution{SchedulerName: "volcano", QueueName: "gpu-task", RuntimeClassName: "nvidia", NodeSelector: map[string]string{"accelerator.ani.io/supply-group": "14141414-1414-4414-8414-141414141414", "accelerator.ani.io/model-key": "nvidia-t4", "accelerator.ani.io/baseline-id": "15151515-1515-4515-8515-151515151515"}, PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"}, Resources: map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": "2048", "volcano.sh/vgpu-cores": "30"}}
	e.ExecutionDigest, err = gpuprotocol.GpuExecutionDigest(e)
	if err != nil {
		t.Fatal(err)
	}
	er, dr, err := codec.EncodeGPUPlanCanonical(e, &accpb.GpuDisplay{ProfileId: selection.Gpu.ProfileId, ProfileVersion: 3, ProfileName: "T4 fixture", ModelKey: "nvidia-t4", Mode: selection.Gpu.Mode})
	if err != nil {
		t.Fatal(err)
	}
	s.GPU, err = codec.DecodeGPUPlanCanonical(er, dr)
	if err != nil {
		t.Fatal(err)
	}
	a := admissionfixture.Bind(t, biz.Admission{TenantID: f.plan.TenantID, OperationID: f.plan.OperationID, ExecutionID: f.plan.ExecutionID, Actor: "governance:user:owner-signing", Intent: conformance.IntentV1(), Snapshot: s, AcceptedAt: s.Timing.AcceptedAt})
	plan, err := biz.FreezeTrainingPlan(biz.Execution{Admission: a}, f.plan.Workspace, f.plan.Timing.AdmittedAt)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = biz.BindTrainingRuntime(plan, biz.RuntimeTemplateBinding{NamespaceUID: plan.Workspace.NamespaceUID, Name: plan.Snapshot.RuntimePlan.Name, UID: "runtime-owner-original", ObservedSpecSHA256: plan.Snapshot.RuntimePlan.ExpectedSpecSHA256})
	if err != nil {
		t.Fatal(err)
	}
	pod := &unstructured.Unstructured{Object: f.pod}
	pod.SetResourceVersion("1")
	var runtimeSpec map[string]any
	if json.Unmarshal(plan.Snapshot.RuntimePlan.ExpectedSpec, &runtimeSpec) != nil {
		t.Fatal("runtime spec")
	}
	jobs, _, _ := unstructured.NestedSlice(runtimeSpec, "template", "spec", "replicatedJobs")
	spec, _, _ := unstructured.NestedMap(jobs[0].(map[string]any), "template", "spec", "template", "spec")
	pod.Object["spec"] = spec
	pod.SetAnnotations(e.PodAnnotations)
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), pod, &unstructured.Unstructured{Object: f.job}, &unstructured.Unstructured{Object: f.jobset})
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "owner.key")
	if os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(private)), 0600) != nil {
		t.Fatal("synthetic owner key")
	}
	return plan, biz.TrainingHandle{NamespaceUID: plan.Workspace.NamespaceUID, PVCUID: plan.Workspace.PVCUID, TrainJobUID: trainingUID}, pod, client, keyFile, public
}

// The Kubernetes API is the external boundary fixture. The accepted plan,
// original controller checks, conditional patch and formal signature are real.
func TestOwnerAssertionSignsOnlyActualOriginalTrainerPod(t *testing.T) {
	plan, handle, pod, client, keyFile, public := ownerSigningFixture(t)
	signer, err := trainer.NewOwnerAssertionSigner(client, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := signer.SignPod(ctx, plan, handle, pod); err != nil {
		t.Fatal("sign actual Trainer UID", err)
	}
	resource := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(pod.GetNamespace())
	actual, err := resource.Get(ctx, pod.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	signature, err := base64.StdEncoding.DecodeString(actual.GetAnnotations()["accelerator.ani.io/owner-signature"])
	if err != nil {
		t.Fatal(err)
	}
	ref := &accpb.GpuUsageRef{TenantId: plan.TenantID, OwnerService: biz.GPUOwnerService, ResourceId: plan.ExecutionID, CreateOperationId: plan.OperationID}
	actualRef := &accpb.PodRef{ClusterId: plan.Snapshot.Environment.ClusterID, Namespace: actual.GetNamespace(), Name: actual.GetName(), Uid: string(actual.GetUID()), ContainerName: "node"}
	raw, err := gpuprotocol.OwnerAssertionSigningBytes(actualRef, ref, plan.Snapshot.GPU.Execution.ExecutionDigest)
	if err != nil || !ed25519.Verify(public, raw, signature) {
		t.Fatal("ACC formal canonical helper cannot verify actual Pod assertion", err)
	}
	actualRef.Uid = "anticipated-template-uid"
	raw, _ = gpuprotocol.OwnerAssertionSigningBytes(actualRef, ref, plan.Snapshot.GPU.Execution.ExecutionDigest)
	if ed25519.Verify(public, raw, signature) {
		t.Fatal("signature did not bind actual API UID")
	}
	patches := 0
	for _, action := range client.Actions() {
		if action.GetVerb() == "patch" {
			patches++
		}
	}
	if err := signer.SignPod(ctx, plan, handle, pod); err != nil {
		t.Fatal("same signed Pod retry", err)
	}
	after := 0
	for _, action := range client.Actions() {
		if action.GetVerb() == "patch" {
			after++
		}
	}
	if patches != 1 || after != patches {
		t.Fatal("retry rewrote owner assertion")
	}
}

func TestOwnerAssertionRejectsReplacementForeignOwnerAndResourceDrift(t *testing.T) {
	for _, attack := range []string{"anticipated-uid", "foreign-controller", "foreign-owner", "resource-drift"} {
		t.Run(attack, func(t *testing.T) {
			plan, handle, pod, client, keyFile, _ := ownerSigningFixture(t)
			signer, err := trainer.NewOwnerAssertionSigner(client, keyFile)
			if err != nil {
				t.Fatal(err)
			}
			observed := pod.DeepCopy()
			actual := pod.DeepCopy()
			switch attack {
			case "anticipated-uid":
				observed.SetUID("never-observed")
			case "foreign-controller":
				refs := actual.GetOwnerReferences()
				refs[0].UID = "foreign-job"
				actual.SetOwnerReferences(refs)
			case "foreign-owner":
				actual.SetLabels(map[string]string{"accelerator.ani.io/owner-service": "ani-resource-service"})
			case "resource-drift":
				containers, _, _ := unstructured.NestedSlice(actual.Object, "spec", "containers")
				container := containers[0].(map[string]any)
				_ = unstructured.SetNestedField(container, "2", "resources", "requests", "volcano.sh/vgpu-number")
				_ = unstructured.SetNestedSlice(actual.Object, containers, "spec", "containers")
			}
			if _, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(actual.GetNamespace()).Update(context.Background(), actual, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := signer.SignPod(context.Background(), plan, handle, observed); err == nil {
				t.Fatal("untrusted actual Pod was signed")
			}
			for _, action := range client.Actions() {
				if action.GetVerb() == "patch" {
					t.Fatal("invalid owner Pod reached signature mutation")
				}
			}
		})
	}
}

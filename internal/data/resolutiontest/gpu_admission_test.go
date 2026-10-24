package resolutiontest

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	accpb "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	"google.golang.org/grpc/codes"
)

// ACC/Res are the external boundaries represented by their actual released
// protocol helpers. Tenant facts, READY input, compilation and mTLS are real.
func TestGPUWholeAndSharedFinalizeOriginalCandidateWithoutAdditionalReads(t *testing.T) {
	for _, mode := range []accpb.SupplyMode{accpb.SupplyMode_WHOLE_EXCLUSIVE, accpb.SupplyMode_SHARED_FIXED} {
		for _, factor10 := range []bool{false, true} {
			t.Run(mode.String()+"/factor10="+strconv.FormatBool(factor10), func(t *testing.T) {
				f := prepareResolution(t)
				cores := uint32(30)
				if mode == accpb.SupplyMode_WHOLE_EXCLUSIVE {
					cores = 100
				}
				selection := &compute.ComputeSelection{SchemaVersion: 1, PresetId: f.request.Intent.ComputePresetID, Code: "gpu-t4", Name: "T4 fixture", ComputeType: "gpu", CpuMillis: 1000, MemoryMib: 1024, Gpu: &compute.GpuSelection{ProfileId: "12121212-1212-4212-8212-121212121212", ProfileVersion: 3, SpecDigest: strings.Repeat("a", 64), DevicesPerUnit: 1, ClusterId: f.facts.Environment.ClusterID, PoolId: "13131313-1313-4313-8313-131313131313", Mode: mode, ModelKey: "nvidia-t4", MemoryMibPerDevice: 2048, CoreLimitPercent: cores}}
				if factor10 {
					selection.Gpu.MemoryMibPerDevice = 3840
				}
				var err error
				selection.SelectionDigest, err = compute.SelectionDigest(selection)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := compute.CanonicalSelection(selection)
				if err != nil {
					t.Fatal(err)
				}
				f.request.ComputeOrigin, err = contractpb.DecodeComputeSelection(raw, selection.SelectionDigest)
				if err != nil {
					t.Fatal(err)
				}
				writeMaterial(t, f.directory, f.material, "gpu")
				client := startAdmissionClient(t, f, f.resolver)
				ctx := admissionRPCContext(f.ctx, f.facts.TenantID)
				prepared, err := client.PrepareAdmission(ctx, admissionRPCRequest(t, f))
				if err != nil {
					t.Fatal(err)
				}
				candidate, err := cpup01.DecodePreparedCandidate(prepared.CandidateCanonical, prepared.CandidateHash)
				if err != nil || candidate.GPURequest == nil || candidate.GPURequest.ContainerName != "node" || candidate.GPURequest.Replicas != 1 || candidate.GPURequest.DevicesPerReplica != 1 {
					t.Fatal("fixed GPU request lost", err)
				}
				exec := &accpb.GpuExecution{SchedulerName: "volcano", QueueName: "gpu-task", RuntimeClassName: "nvidia", NodeSelector: map[string]string{"accelerator.ani.io/supply-group": "14141414-1414-4414-8414-141414141414", "accelerator.ani.io/model-key": "nvidia-t4", "accelerator.ani.io/baseline-id": "15151515-1515-4515-8515-151515151515"}, PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"}, Resources: map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": "2048", "volcano.sh/vgpu-cores": "30"}}
				if factor10 {
					exec.Resources["volcano.sh/vgpu-memory"] = "384"
					exec.PodAnnotations["accelerator.ani.io/memory-block-mib"] = "10"
					exec.NodeSelector["kubernetes.io/hostname"] = "node3"
				}
				if cores == 100 {
					exec.Resources["volcano.sh/vgpu-cores"] = "100"
				}
				exec.ExecutionDigest, err = gpuprotocol.GpuExecutionDigest(exec)
				if err != nil {
					t.Fatal(err)
				}
				display := &accpb.GpuDisplay{ProfileId: selection.Gpu.ProfileId, ProfileVersion: selection.Gpu.ProfileVersion, ProfileName: "T4 fixture", ModelKey: "nvidia-t4", Mode: mode}
				er, dr, err := contractpb.EncodeGPUPlanCanonical(exec, display)
				if err != nil {
					t.Fatal(err)
				}
				before := f.factReader.reads
				request := &modeldevv1.FinalizeAdmissionRequest{CandidateCanonical: prepared.CandidateCanonical, CandidateHash: prepared.CandidateHash, GpuExecutionCanonical: er, GpuDisplayCanonical: dr}
				finalized, err := client.FinalizeAdmission(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
				snapshot, err := contractpb.DecodeSnapshot(finalized.Snapshot)
				if err != nil || snapshot.GPU == nil || snapshot.GPU.ValidateFor(snapshot.ComputeOrigin) != nil {
					t.Fatal("GPU snapshot lost formal facts", err)
				}
				var spec map[string]any
				if json.Unmarshal(snapshot.RuntimePlan.ExpectedSpec, &spec) != nil {
					t.Fatal("runtime plan missing")
				}
				serialized := string(snapshot.RuntimePlan.ExpectedSpec)
				required := []string{"gpu-task", "nvidia", "accelerator.ani.io/baseline-id", `"volcano.sh/vgpu-memory":"2048"`}
				if factor10 {
					required = []string{"gpu-task", "nvidia", "accelerator.ani.io/baseline-id", `"volcano.sh/vgpu-memory":"384"`, `"accelerator.ani.io/memory-block-mib":"10"`, `"kubernetes.io/hostname":"node3"`}
					if candidate.GPURequest.MemoryMiBPerDevice != 3840 || snapshot.GPU.Execution.Resources["volcano.sh/vgpu-memory"] != "384" {
						t.Fatal("candidate logical MiB or frozen wire blocks changed")
					}
				}
				for _, required := range required {
					if !strings.Contains(serialized, required) {
						t.Fatal("GPU formal runtime projection missing", required)
					}
				}
				again, err := client.FinalizeAdmission(ctx, request)
				if err != nil || again.ExecutionSpecHash != finalized.ExecutionSpecHash || f.factReader.reads != before {
					t.Fatal("Finalize changed candidate or reread facts", err)
				}
				display.ProfileVersion++
				request.GpuExecutionCanonical, request.GpuDisplayCanonical, err = contractpb.EncodeGPUPlanCanonical(exec, display)
				if err != nil {
					t.Fatal(err)
				}
				response, err := client.FinalizeAdmission(ctx, request)
				requireRPCError(t, response, err, codes.InvalidArgument)
				requireNoResolvedExecution(t, f)
			})
		}
	}
}

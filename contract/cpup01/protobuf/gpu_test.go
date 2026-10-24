package protobuf_test

import (
	"bytes"
	accpb "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	codec "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	"google.golang.org/protobuf/proto"
	"strings"
	"testing"
)

func formalGPUFixture(t *testing.T, mode accpb.SupplyMode, factor10 ...bool) (cpup01.Snapshot, *accpb.GpuExecution, *accpb.GpuDisplay) {
	t.Helper()
	s := conformance.SnapshotV1()
	core := uint32(30)
	if mode == accpb.SupplyMode_WHOLE_EXCLUSIVE {
		core = 100
	}
	selection := &compute.ComputeSelection{SchemaVersion: 1, PresetId: s.ComputeOrigin.ComputePresetID, Code: "gpu-t4", Name: "T4", ComputeType: "gpu", CpuMillis: 1000, MemoryMib: 1024, Gpu: &compute.GpuSelection{ProfileId: "12121212-1212-4212-8212-121212121212", ProfileVersion: 3, SpecDigest: strings.Repeat("a", 64), DevicesPerUnit: 1, ClusterId: s.Environment.ClusterID, PoolId: "13131313-1313-4313-8313-131313131313", Mode: mode, ModelKey: "nvidia-t4", MemoryMibPerDevice: 2048, CoreLimitPercent: core}}
	if len(factor10) != 0 && factor10[0] {
		selection.Gpu.MemoryMibPerDevice = 3840
	}
	var err error
	selection.SelectionDigest, err = compute.SelectionDigest(selection)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := compute.CanonicalSelection(selection)
	if err != nil {
		t.Fatal(err)
	}
	s.ComputeOrigin, err = codec.DecodeComputeSelection(canonical, selection.SelectionDigest)
	if err != nil {
		t.Fatal(err)
	}
	e := &accpb.GpuExecution{SchedulerName: "volcano", QueueName: "gpu-task", RuntimeClassName: "nvidia", NodeSelector: map[string]string{"accelerator.ani.io/supply-group": "14141414-1414-4414-8414-141414141414", "accelerator.ani.io/model-key": "nvidia-t4", "accelerator.ani.io/baseline-id": "15151515-1515-4515-8515-151515151515"}, PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"}, Resources: map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": "2048", "volcano.sh/vgpu-cores": "30"}}
	if len(factor10) != 0 && factor10[0] {
		e.Resources["volcano.sh/vgpu-memory"] = "384"
		e.PodAnnotations["accelerator.ani.io/memory-block-mib"] = "10"
		e.NodeSelector["kubernetes.io/hostname"] = "node3"
	}
	if core == 100 {
		e.Resources["volcano.sh/vgpu-cores"] = "100"
	}
	e.ExecutionDigest, err = gpuprotocol.GpuExecutionDigest(e)
	if err != nil {
		t.Fatal(err)
	}
	d := &accpb.GpuDisplay{ProfileId: selection.Gpu.ProfileId, ProfileVersion: selection.Gpu.ProfileVersion, ProfileName: "T4 fixture", ModelKey: selection.Gpu.ModelKey, Mode: mode}
	return s, e, d
}

func TestFactor10HostnameSnapshotPreservesLogicalMemoryAndWireResources(t *testing.T) {
	for _, mode := range []accpb.SupplyMode{accpb.SupplyMode_WHOLE_EXCLUSIVE, accpb.SupplyMode_SHARED_FIXED} {
		t.Run(mode.String(), func(t *testing.T) {
			s, execution, display := formalGPUFixture(t, mode, true)
			er, dr, err := codec.EncodeGPUPlanCanonical(execution, display)
			if err != nil {
				t.Fatalf("official factor10 placement rejected: %v", err)
			}
			s.GPU, err = codec.DecodeGPUPlanCanonical(er, dr)
			if err != nil || s.GPU.ValidateFor(s.ComputeOrigin) != nil {
				t.Fatalf("384 wire blocks did not resolve to the selected 3840 MiB: %v", err)
			}
			message, err := codec.EncodeSnapshot(s)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := codec.DecodeSnapshot(message)
			if err != nil {
				t.Fatal(err)
			}
			request, err := cpup01.GPURequestFor(decoded.ComputeOrigin)
			if err != nil || request.MemoryMiBPerDevice != 3840 || decoded.GPU.Execution.Resources["volcano.sh/vgpu-memory"] != "384" || decoded.GPU.Execution.PodAnnotations["accelerator.ani.io/memory-block-mib"] != "10" || decoded.GPU.Execution.NodeSelector["kubernetes.io/hostname"] != "node3" {
				t.Fatal("snapshot conflated logical MiB, wire blocks or frozen placement", err)
			}
			before, err := s.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			after, err := decoded.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("factor10 snapshot changed across the complete typed wire")
			}
		})
	}
}

func TestFactor10SnapshotRejectsUnsupportedAnnotationsAndLogicalMemoryDrift(t *testing.T) {
	for name, change := range map[string]func(*accpb.GpuExecution){
		"explicit factor1":              func(e *accpb.GpuExecution) { e.PodAnnotations["accelerator.ani.io/memory-block-mib"] = "1" },
		"noncanonical factor":           func(e *accpb.GpuExecution) { e.PodAnnotations["accelerator.ani.io/memory-block-mib"] = "010" },
		"unsupported factor":            func(e *accpb.GpuExecution) { e.PodAnnotations["accelerator.ani.io/memory-block-mib"] = "2" },
		"additional annotation":         func(e *accpb.GpuExecution) { e.PodAnnotations["untrusted.example/factor"] = "10" },
		"lost factor":                   func(e *accpb.GpuExecution) { delete(e.PodAnnotations, "accelerator.ani.io/memory-block-mib") },
		"logical MiB written as blocks": func(e *accpb.GpuExecution) { e.Resources["volcano.sh/vgpu-memory"] = "3840" },
		"logical overflow":              func(e *accpb.GpuExecution) { e.Resources["volcano.sh/vgpu-memory"] = "214748365" },
	} {
		t.Run(name, func(t *testing.T) {
			s, execution, display := formalGPUFixture(t, accpb.SupplyMode_SHARED_FIXED, true)
			originalExecution, originalDisplay, err := codec.EncodeGPUPlanCanonical(execution, display)
			if err != nil {
				t.Fatal(err)
			}
			original, err := codec.DecodeGPUPlanCanonical(originalExecution, originalDisplay)
			if err != nil {
				t.Fatal(err)
			}
			change(execution)
			execution.ExecutionDigest, _ = gpuprotocol.GpuExecutionDigest(execution)
			original.Execution.PodAnnotations = execution.PodAnnotations
			original.Execution.Resources = execution.Resources
			original.Execution.ExecutionDigest = execution.ExecutionDigest
			if original.ValidateFor(s.ComputeOrigin) == nil {
				t.Fatal("snapshot accepted changed encoding or logical memory under a fresh digest")
			}
			er, dr, err := codec.EncodeGPUPlanCanonical(execution, display)
			if err != nil {
				return
			}
			plan, err := codec.DecodeGPUPlanCanonical(er, dr)
			if err == nil && plan.ValidateFor(s.ComputeOrigin) == nil {
				t.Fatal("changed encoding or logical memory accepted after digest recomputation")
			}
		})
	}
}
func TestOfficialGPUWholeAndSharedCanonicalWireFacts(t *testing.T) {
	for _, mode := range []accpb.SupplyMode{accpb.SupplyMode_WHOLE_EXCLUSIVE, accpb.SupplyMode_SHARED_FIXED} {
		s, e, d := formalGPUFixture(t, mode)
		er, dr, err := codec.EncodeGPUPlanCanonical(e, d)
		if err != nil {
			t.Fatal(err)
		}
		s.GPU, err = codec.DecodeGPUPlanCanonical(er, dr)
		if err != nil || s.GPU.ValidateFor(s.ComputeOrigin) != nil {
			t.Fatal("formal cross-provider facts rejected", err)
		}
		request, err := cpup01.GPURequestFor(s.ComputeOrigin)
		if err != nil || request.ContainerName != "node" || request.Replicas != 1 || request.DevicesPerReplica != 1 {
			t.Fatal("request topology changed")
		}
		message, err := codec.EncodeSnapshot(s)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := codec.DecodeSnapshot(message)
		if err != nil {
			t.Fatal(err)
		}
		before, _ := s.Canonical()
		after, _ := decoded.Canonical()
		if !bytes.Equal(before, after) {
			t.Fatal("GPU typed wire changed frozen snapshot")
		}
	}
}
func TestGPUAdapterRejectsUnknownDriftAndCrossCandidateMixing(t *testing.T) {
	s, e, d := formalGPUFixture(t, accpb.SupplyMode_SHARED_FIXED)
	goodE, goodD, err := codec.EncodeGPUPlanCanonical(e, d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codec.DecodeGPUPlanCanonical(append(goodE, ' '), goodD); err == nil {
		t.Fatal("noncanonical GPU bytes accepted")
	}
	for _, change := range []func(*accpb.GpuExecution){func(e *accpb.GpuExecution) { e.ProtoReflect().SetUnknown([]byte{0x40, 1}) }, func(e *accpb.GpuExecution) { e.Resources["nvidia.com/gpu"] = "1" }, func(e *accpb.GpuExecution) { e.NodeSelector["unknown"] = "allowed" }, func(e *accpb.GpuExecution) { e.Resources["volcano.sh/vgpu-number"] = "2" }} {
		bad := proto.Clone(e).(*accpb.GpuExecution)
		change(bad)
		if bad.ProtoReflect().GetUnknown() == nil {
			bad.ExecutionDigest, _ = gpuprotocol.GpuExecutionDigest(bad)
		}
		er, dr, err := codec.EncodeGPUPlanCanonical(bad, d)
		if err == nil {
			p, decodeErr := codec.DecodeGPUPlanCanonical(er, dr)
			if decodeErr == nil && p.ValidateFor(s.ComputeOrigin) == nil {
				t.Fatal("GPU drift accepted")
			}
		}
	}
	wrong := proto.Clone(d).(*accpb.GpuDisplay)
	wrong.ProfileVersion++
	er, dr, err := codec.EncodeGPUPlanCanonical(e, wrong)
	if err != nil {
		t.Fatal(err)
	}
	p, err := codec.DecodeGPUPlanCanonical(er, dr)
	if err != nil {
		t.Fatal(err)
	}
	if p.ValidateFor(s.ComputeOrigin) == nil {
		t.Fatal("another profile version was mixed into candidate")
	}
	cpu := conformance.SnapshotV1()
	cpu.GPU = p
	if cpu.Validate() == nil {
		t.Fatal("CPU accepted accelerator plan")
	}
}

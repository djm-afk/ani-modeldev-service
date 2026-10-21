package cpup01_test

import (
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"strings"
	"testing"
)

func TestFrozenGPUExecutionAcceptsOnlyObservedRuntimeClassHostname(t *testing.T) {
	e := cpup01.GPUExecution{SchedulerName: "volcano", QueueName: "gpu", RuntimeClassName: "nvidia", NodeSelector: map[string]string{"accelerator.ani.io/supply-group": "original-group", "accelerator.ani.io/model-key": "nvidia-t4", "accelerator.ani.io/baseline-id": "original-baseline", "kubernetes.io/hostname": "ani-03"}, PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"}, Resources: map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": "8192", "volcano.sh/vgpu-cores": "25"}}
	freeze := func() {
		t.Helper()
		var err error
		e.ExecutionDigest, err = gpuprotocol.GpuExecutionDigest(&acc.GpuExecution{SchedulerName: e.SchedulerName, QueueName: e.QueueName, RuntimeClassName: e.RuntimeClassName, NodeSelector: e.NodeSelector, PodAnnotations: e.PodAnnotations, Resources: e.Resources})
		if err != nil {
			t.Fatal(err)
		}
	}
	freeze()
	if err := e.Validate(); err != nil {
		t.Fatalf("official frozen hostname rejected: %v", err)
	}
	original := e.ExecutionDigest
	e.NodeSelector["kubernetes.io/hostname"] = "ani-02"
	if e.Validate() == nil {
		t.Fatal("new placement accepted under original digest")
	}
	freeze()
	if e.ExecutionDigest == original {
		t.Fatal("placement is not committed by digest")
	}
	for _, bad := range []string{"", "bad/value", strings.Repeat("a", 64)} {
		e.NodeSelector["kubernetes.io/hostname"] = bad
		freeze()
		if e.Validate() == nil {
			t.Fatalf("invalid hostname label accepted: %q", bad)
		}
	}
	delete(e.NodeSelector, "kubernetes.io/hostname")
	freeze()
	if e.Validate() != nil {
		t.Fatal("original three-selector execution no longer valid")
	}
	e.NodeSelector["unknown.example/selector"] = "any"
	freeze()
	if e.Validate() == nil {
		t.Fatal("unknown fourth selector accepted")
	}
}

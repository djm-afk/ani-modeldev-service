package lifecycle

import (
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// A GPU execution can fail before the runtime exists at all: the first pipeline
// steps are rejected by admission, the catalogue image is missing, or prepare
// itself errors out. The dispatch was real, so close evidence carries
// NoDispatch=false, yet neither cleanup receipt can ever appear. Without this
// branch the release is skipped forever and the quota charge never comes back.
func TestRuntimeNeverMaterialized(t *testing.T) {
	if !runtimeNeverMaterialized(biz.ExecutionRuntime{}) {
		t.Fatal("empty runtime must read as never materialized")
	}
	// Close bookkeeping alone does not imply a runtime was built.
	closed := biz.ExecutionRuntime{CloseGeneration: 1, CloseOperationID: "op", CloseReason: "STEP_FAILED"}
	if !runtimeNeverMaterialized(closed) {
		t.Fatal("close bookkeeping was mistaken for runtime materialization")
	}
	for name, state := range map[string]biz.ExecutionRuntime{
		"workspace":   {Workspace: &biz.WorkspaceBinding{}},
		"training":    {Training: &biz.TrainingPlan{}},
		"handle":      {TrainingHandle: &biz.TrainingHandle{}},
		"observation": {Observation: &biz.TrainingRuntimeObservation{}},
		"cleanup":     {ComputeCleanup: &biz.ComputeCleanupReceipt{}},
		"exit":        {StepExitCleanup: &biz.StepExitCleanupReceipt{}},
	} {
		if runtimeNeverMaterialized(state) {
			t.Fatalf("a runtime carrying %s was treated as never materialized", name)
		}
	}
}

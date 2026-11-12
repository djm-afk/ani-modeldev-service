package biz

import (
	"errors"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// The training Pod writes the prepared dataset, its mmap working files and its
// outputs into one execution PVC. A version that cannot fit must be refused with
// a capacity verdict, not left to fail on a full volume mid-run.
func TestWorkspaceCapacityRejectsAnOversizeRun(t *testing.T) {
	workspace := cpup01.WorkspaceContract{Mode: "EXECUTION_PVC", StorageClass: "task-workspace", CapacityBytes: 1 << 30}
	input := cpup01.InputRef{
		Format: "CSV", RowCount: 1_000_000,
		Object:          cpup01.FixedObjectRef{SizeBytes: 900 << 20},
		PreparedDataset: cpup01.PreparedDatasetFacts{SizeBytes: 900 << 20, RowCount: 1_000_000},
	}
	// 900MiB prepared + 80MB working + 64MiB artifacts + 900MiB packaging is far
	// beyond a 1Gi volume.
	if err := checkWorkspaceCapacity(workspace, input); !errors.Is(err, ErrWorkspaceCapacityInsufficient) {
		t.Fatalf("oversize run returned %v, want WORKSPACE_CAPACITY_INSUFFICIENT", err)
	}
}

// A run that fits must pass: the check is a capacity verdict, not a new ceiling.
func TestWorkspaceCapacityAcceptsAFittingRun(t *testing.T) {
	workspace := cpup01.WorkspaceContract{Mode: "EXECUTION_PVC", StorageClass: "task-workspace", CapacityBytes: 8 << 30}
	input := cpup01.InputRef{
		Format: "CSV", RowCount: 1000,
		Object:          cpup01.FixedObjectRef{SizeBytes: 4 << 20},
		PreparedDataset: cpup01.PreparedDatasetFacts{SizeBytes: 4 << 20, RowCount: 1000},
	}
	if err := checkWorkspaceCapacity(workspace, input); err != nil {
		t.Fatalf("a fitting run was refused: %v", err)
	}
}

// A contract without capacity is not this check's business; snapshot validation
// already rejects it, so the check must not invent a second verdict.
func TestWorkspaceCapacityIgnoresAnAbsentContract(t *testing.T) {
	input := cpup01.InputRef{RowCount: 1 << 40, Object: cpup01.FixedObjectRef{SizeBytes: 1 << 40}}
	if err := checkWorkspaceCapacity(cpup01.WorkspaceContract{}, input); err != nil {
		t.Fatalf("an absent capacity contract was judged: %v", err)
	}
}

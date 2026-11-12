package biz

import (
	"errors"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// The MLP program writes the prepared dataset, three mmap working files and its
// outputs into one execution PVC. A run that cannot fit must be refused with a
// capacity verdict, not left to fail on a full volume mid-run.
func TestWorkspaceCapacityRejectsAnOversizeMLPRun(t *testing.T) {
	workspace := cpup01.WorkspaceContract{Mode: "EXECUTION_PVC", StorageClass: "task-workspace", CapacityBytes: 1 << 30}
	input := cpup01.InputRef{
		Format: "CSV", RowCount: 1_000_000,
		Object:          cpup01.FixedObjectRef{SizeBytes: 900 << 20},
		PreparedDataset: cpup01.PreparedDatasetFacts{SizeBytes: 900 << 20, RowCount: 1_000_000},
	}
	// 900MiB prepared + 80MB working + 64MiB artifacts is beyond a 1Gi volume.
	if err := checkWorkspaceCapacity(workspace, "GENERAL_TRAINING", input); !errors.Is(err, ErrWorkspaceCapacityInsufficient) {
		t.Fatalf("oversize run returned %v, want WORKSPACE_CAPACITY_INSUFFICIENT", err)
	}
}

// A run that fits must pass: the check is a capacity verdict, not a new ceiling.
func TestWorkspaceCapacityAcceptsAFittingMLPRun(t *testing.T) {
	workspace := cpup01.WorkspaceContract{Mode: "EXECUTION_PVC", StorageClass: "task-workspace", CapacityBytes: 8 << 30}
	input := cpup01.InputRef{
		Format: "CSV", RowCount: 1000,
		Object:          cpup01.FixedObjectRef{SizeBytes: 4 << 20},
		PreparedDataset: cpup01.PreparedDatasetFacts{SizeBytes: 4 << 20, RowCount: 1000},
	}
	if err := checkWorkspaceCapacity(workspace, "GENERAL_TRAINING", input); err != nil {
		t.Fatalf("a fitting run was refused: %v", err)
	}
}

// The estimate must not double-count the dataset. The MLP program reads the
// prepared file in place and writes only the model, metrics, config and summary,
// so a run whose real footprint fits must not be refused by an inflated formula.
func TestWorkspaceCapacityDoesNotDoubleCountTheDataset(t *testing.T) {
	// 600MiB prepared + 80MB working + 64MiB artifacts = ~744MiB, which fits a
	// 1Gi volume. A formula that added a second copy of the dataset would refuse.
	workspace := cpup01.WorkspaceContract{Mode: "EXECUTION_PVC", StorageClass: "task-workspace", CapacityBytes: 1 << 30}
	input := cpup01.InputRef{
		Format: "CSV", RowCount: 1_000_000,
		Object:          cpup01.FixedObjectRef{SizeBytes: 600 << 20},
		PreparedDataset: cpup01.PreparedDatasetFacts{SizeBytes: 600 << 20, RowCount: 1_000_000},
	}
	if err := checkWorkspaceCapacity(workspace, "GENERAL_TRAINING", input); err != nil {
		t.Fatalf("a run that fits was refused by an inflated estimate: %v", err)
	}
}

// Finetuning's volume holds the base model, the merged model and a tar bundle
// whose sizes this service does not know. Applying the MLP formula there would
// refuse runs that actually fit, so the check must not run at all.
func TestWorkspaceCapacitySkipsFinetuning(t *testing.T) {
	workspace := cpup01.WorkspaceContract{Mode: "EXECUTION_PVC", StorageClass: "task-workspace", CapacityBytes: 1 << 20}
	input := cpup01.InputRef{
		Format: "JSON", RowCount: 10_000_000,
		Object:          cpup01.FixedObjectRef{SizeBytes: 900 << 20},
		PreparedDataset: cpup01.PreparedDatasetFacts{SizeBytes: 900 << 20, RowCount: 10_000_000},
	}
	if err := checkWorkspaceCapacity(workspace, "FINETUNING", input); err != nil {
		t.Fatalf("finetuning was judged by the MLP formula: %v", err)
	}
}

// A contract without capacity is not this check's business; snapshot validation
// already rejects it, so the check must not invent a second verdict.
func TestWorkspaceCapacityIgnoresAnAbsentContract(t *testing.T) {
	input := cpup01.InputRef{RowCount: 1 << 40, Object: cpup01.FixedObjectRef{SizeBytes: 1 << 40}}
	if err := checkWorkspaceCapacity(cpup01.WorkspaceContract{}, "GENERAL_TRAINING", input); err != nil {
		t.Fatalf("an absent capacity contract was judged: %v", err)
	}
}

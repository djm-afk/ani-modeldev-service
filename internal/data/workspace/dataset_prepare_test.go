package workspace_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/workspace"
)

func TestPrepareCSVUsesOneGETAndFrozenDatasetManifest(t *testing.T) {
	_, execution, _ := preparedWorkspace(t)
	data := pipelineCSVFixture()
	input := &execution.Snapshot.Input
	input.Object.SizeBytes, input.Object.SHA256 = int64(len(data)), pipelineDigest(data)
	input.PreparedDataset = cpup01.PreparedDatasetFacts{Format: "CPU_CSV", TransformID: "csv-identity-v3", SizeBytes: input.Object.SizeBytes, SHA256: input.Object.SHA256, RowCount: input.RowCount}
	dataset, err := cpup01.BuildDatasetManifest(*input)
	if err != nil {
		t.Fatal(err)
	}
	wanted, err := dataset.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	for index, argument := range execution.Snapshot.Program.ResolvedArgs {
		if argument == "--expected-dataset-manifest-sha256" && index+1 < len(execution.Snapshot.Program.ResolvedArgs) {
			execution.Snapshot.Program.ResolvedArgs[index+1] = pipelineDigest(wanted)
		}
	}
	refreshSpec(t, &execution)
	store, client := newPipelineS3(t, execution, data)
	contract := execution.Snapshot.Workspace
	binding := biz.WorkspaceBinding{Mode: contract.Mode, NamespaceName: execution.Snapshot.Environment.NamespaceName, NamespaceUID: execution.Snapshot.Environment.NamespaceUID, PVCName: "execution-workspace", PVCUID: "55555555-5555-4555-8555-555555555555", InputSubpath: contract.InputSubpath, TrainingSubpath: contract.TrainingSubpath, ReportsSubpath: contract.ReportsSubpath, PublicationSubpath: contract.PublicationSubpath, WorkSubpath: contract.WorkSubpath}
	root := t.TempDir()
	if _, err := workspace.PrepareInput(context.Background(), client, execution, binding, root); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"train", "prepare", "export", "verify", "collect", "publish"} {
		for _, sub := range []string{"tmp", "hf/datasets", "matplotlib"} {
			info, err := os.Stat(filepath.Join(root, "work", role, sub))
			if err != nil || !info.IsDir() {
				t.Fatalf("execution-private role work directory missing: %s/%s: %v", role, sub, err)
			}
		}
	}
	if store.gets != 1 {
		t.Fatalf("prepare read the immutable source %d times; validation and copy must share one GET", store.gets)
	}
	actual, err := os.ReadFile(filepath.Join(root, "inputs/data.csv"))
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("prepared CSV changed original verified bytes: %v", err)
	}
	manifest, err := os.ReadFile(filepath.Join(root, "inputs/dataset-manifest.json"))
	if err != nil || !bytes.Equal(manifest, wanted) {
		t.Fatalf("prepare did not publish the independently frozen dataset manifest: %q %v", manifest, err)
	}
}

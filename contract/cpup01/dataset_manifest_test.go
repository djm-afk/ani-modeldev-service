package cpup01_test

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

func TestCurrentDatasetManifestFreezesDerivedFacts(t *testing.T) {
	input := conformance.SnapshotV1().Input
	input.RowCount = math.MaxInt64
	input.PreparedDataset = cpup01.PreparedDatasetFacts{Format: "CPU_CSV", TransformID: "csv-identity-v3", SizeBytes: input.Object.SizeBytes, SHA256: input.Object.SHA256, RowCount: input.RowCount}
	manifest, err := cpup01.BuildDatasetManifest(input)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := manifest.Canonical()
	if err != nil || !bytes.Contains(raw, []byte(`"row_count":"9223372036854775807"`)) || !bytes.Contains(raw, []byte(`"schema_version":"ani.modeldev.dataset-manifest.v3"`)) || manifest.PreparedRelativePath != "inputs/data.csv" {
		t.Fatalf("frozen current manifest: %s, %v", raw, err)
	}
	input.Format = "JSON"
	input.PreparedDataset = cpup01.PreparedDatasetFacts{Format: "ALPACA_JSONL", TransformID: "alpaca-array-compact-jsonl-v3", SizeBytes: input.Object.SizeBytes - 1, SHA256: strings.Repeat("a", 64), RowCount: input.RowCount}
	manifest, err = cpup01.BuildDatasetManifest(input)
	if err != nil || manifest.PreparedRelativePath != "inputs/data/finetune_data.jsonl" {
		t.Fatalf("JSONL transform path: %#v, %v", manifest, err)
	}
	input.PreparedDataset.SizeBytes = input.Object.SizeBytes + 1
	if _, err := cpup01.BuildDatasetManifest(input); err == nil {
		t.Fatal("derived JSONL larger than original source accepted")
	}
}

package cpup01_test

import (
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

func TestCurrentMaterialOnlyUsesFrozenDatasetManifest(t *testing.T) {
	material := conformance.MaterialV21()
	material.Program.ArgsTemplate = []cpup01.MaterialArgument{
		{Literal: "--dataset-manifest"}, {Literal: "/inputs/dataset-manifest.json"},
		{Literal: "--expected-dataset-manifest-sha256"}, {Source: "DATASET_MANIFEST_SHA256"},
		{Literal: "--work-root"}, {Literal: "/workspace/work/train"},
		{Literal: "--output"}, {Source: "OUTPUT_PATH"},
	}
	if _, err := material.Canonical(); err != nil {
		t.Fatalf("current frozen manifest source rejected: %v", err)
	}
	material.Program.ArgsTemplate[3].Source = "INPUT_SHA256"
	if _, err := material.Canonical(); err == nil {
		t.Fatal("obsolete dataset byte identity source was accepted")
	}
}

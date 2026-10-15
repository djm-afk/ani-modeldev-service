package main

import (
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

func TestCandidateUsesActualWorkloadEvidenceAndFormalCodec(t *testing.T) {
	// Metadata identities below are synthetic. The public fixture contains only
	// W0 WORKLOAD observations and proves no live admission or environment.
	// No candidate is written or enabled by this test.
	var fixture struct {
		Class    string           `json:"fixture_class"`
		Workload workloadEvidence `json:"workload"`
	}
	if err := readJSON("testdata/workload-only.json", &fixture, true); err != nil {
		t.Fatal(err)
	}
	if fixture.Class != "WORKLOAD_ONLY/FIXTURE" {
		t.Fatal("test fixture must declare its limited scope")
	}
	evidence := fixture.Workload
	entrySHA, err := hashFile("../../../training/finetuning/finetune_entry.py")
	if err != nil {
		t.Fatal(err)
	}
	mappingSHA, err := hashFile("../../../training/finetuning/configuration.py")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateEvidence(evidence, entrySHA, mappingSHA); err != nil {
		t.Fatal(err)
	}
	uuid := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	input := facts{ReleaseID: uuid, PresetID: uuid, Runtime: cpup01.RuntimeRef{Name: "test-runtime", Kind: "TrainingRuntime", APIGroup: "trainer.kubeflow.org", ContentSHA256: strings.Repeat("a", 64), TargetJobs: []string{"node"}}, WorkspaceStorageClass: "test-only-storage"}
	input.TrainingImage = image{VersionID: uuid, Digest: "registry.example.test/test@sha256:" + strings.Repeat("b", 64)}
	input.ExportImage, input.VerifyImage = input.TrainingImage, input.TrainingImage
	pipeline := pipelineReceipt{Phase: "CONFIRMED", PipelineID: uuid, PipelineVersionID: uuid, PipelineIRSHA256: strings.Repeat("c", 64)}
	document, err := candidate(input, pipeline, storageReceipt{BaseVersionID: uuid}, entrySHA, mappingSHA)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := document.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	parsed, digest, err := cpup01.ParseRelease(raw)
	if err != nil || digest != hash(raw) {
		t.Fatalf("formal ParseRelease: digest=%s err=%v", digest, err)
	}
	if len(parsed.Program.AllowedParameterSets) != 2 || len(parsed.OutputContract.RequiredFiles) != 18 || parsed.Finetuning.Budgets.RPCTimeoutSeconds != 600 || parsed.Finetuning.Budgets.PrepareTimeoutSeconds != 900 || parsed.Finetuning.Budgets.ExportTimeoutSeconds != 1800 || parsed.Workspace.CapacityBytes != 20<<30 {
		t.Fatal("candidate lost real structure or D10 budget")
	}
	if _, err := cpup01.ResolveFinetuningParameters(parsed.Program, cpup01.FinetuningParameters{LearningRate: &evidence.B.LearningRate}); err == nil {
		t.Fatal("unmeasured B-rate/A-steps cross-combination accepted")
	}
	input.TrainingImage.Digest = "sha256:" + strings.Repeat("b", 64)
	if _, err := candidate(input, pipeline, storageReceipt{BaseVersionID: uuid}, entrySHA, mappingSHA); err == nil {
		t.Fatal("image config ID used as manifest reference")
	}
	pipeline.Phase = "UPLOAD_VERSION_PENDING"
	if _, err := candidate(input, pipeline, storageReceipt{BaseVersionID: uuid}, entrySHA, mappingSHA); err == nil {
		t.Fatal("unconfirmed pipeline used")
	}
	evidence.B.OptimizerSteps = 9
	if err := validateEvidence(evidence, entrySHA, mappingSHA); err == nil {
		t.Fatal("requested 10 promoted over actual 9")
	}
}

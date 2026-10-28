package cpup01_test

import (
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"strings"
	"testing"
)

func TestFinetuningAllFieldsConsumeWithoutApprovalSet(t *testing.T) {
	i := conformance.FinetuningIntentV1()
	lr := "0.00015"
	drop := "0.15"
	steps, batch, accum, cutoff, rank, alpha := int64(301), int64(3), int64(4), int64(256), int64(13), int64(19)
	seed := uint32(123)
	i.Finetuning = &cpup01.FinetuningParameters{LearningRate: &lr, MaxSteps: &steps, PerDeviceTrainBatchSize: &batch, GradientAccumulationSteps: &accum, CutoffLen: &cutoff, LoraRank: &rank, LoraAlpha: &alpha, LoraDropout: &drop, Seed: &seed}
	if _, _, e := cpup01.CanonicalIntent(i); e != nil {
		t.Fatal(e)
	}
	p, e := i.ResolveParameters()
	if e != nil || len(p) != 9 {
		t.Fatalf("complete parameters: %v", e)
	}
	if e := cpup01.ValidateResolvedParameters(i.ImplementationID, p); e != nil {
		t.Fatal(e)
	}
	s := conformance.FinetuningSnapshotV1()
	if e := s.Validate(); e != nil {
		t.Fatal(e)
	}
	s.Input.RowCount = 71
	s.Input.PreparedDataset.RowCount = 71
	dataset, _ := cpup01.BuildDatasetManifest(s.Input)
	datasetDigest, _ := dataset.Digest()
	for _, args := range [][]string{s.Program.ResolvedArgs, s.Finetuning.ExportProgram.ResolvedArgs, s.Finetuning.VerifyProgram.ResolvedArgs} {
		for index, arg := range args {
			if arg == "--expected-dataset-manifest-sha256" {
				args[index+1] = datasetDigest
			}
		}
	}
	if e := s.Validate(); e != nil {
		t.Fatal(e)
	}
}
func TestFinetuningRejectsNonRepresentableDropoutAndUnknownFields(t *testing.T) {
	raw := `{"name":"ft","kind":"FINETUNING","implementation_id":"builtin.sft_lora","compute_preset_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","dataset_version_id":"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee","base_model_version_id":"55555555-5555-4555-8555-555555555555","new_model_name":"model","parameters":{}}`
	for _, p := range []string{`{"max_steps":0}`, `{"lora_dropout":"1"}`, `{"lora_dropout":"0.999999999999999999999999999999"}`, `{"epochs":3}`, `{"lora_rank":1,"lora_rank":2}`} {
		if _, e := cpup01.ParseIntent([]byte(strings.Replace(raw, `"parameters":{}`, `"parameters":`+p, 1))); e == nil {
			t.Fatal("invalid field accepted", p)
		}
	}
}

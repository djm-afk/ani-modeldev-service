package workspace_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/workspace"
)

// An opt-in adapter test over the actual Fedora trainer artifacts. It does not
// represent authenticated admission, persistence, KFP, publication or closing.
func TestCollectActualFedoraMLPArtifacts(t *testing.T) {
	root := os.Getenv("ANI_MODELDEV_WORKLOAD_ROOT")
	if root == "" {
		t.Skip("requires task-owned actual Fedora workload artifacts")
	}
	for _, vector := range []struct {
		name, rate          string
		rows, epochs, batch int64
		seed                uint32
	}{
		{"mlp-baseline", "0.01", 1024, 3, 64, 42}, {"mlp-nondefault", "1000", 17, 2, 6, 7},
	} {
		t.Run(vector.name, func(t *testing.T) {
			snapshot := conformance.SnapshotV1()
			intent := conformance.IntentV1()
			intent.MLP = &cpup01.MLPParameters{LearningRate: &vector.rate, Epochs: &vector.epochs, BatchSize: &vector.batch, Seed: &vector.seed}
			snapshot.TrainingProgram.ResolvedParameters, _ = intent.ResolveParameters()
			snapshot.Input.RowCount = uint32(vector.rows)
			raw, err := os.ReadFile(filepath.Join(root, vector.name, "input.csv"))
			if err != nil {
				t.Fatal(err)
			}
			snapshot.Input.Object.SizeBytes, snapshot.Input.Object.SHA256 = int64(len(raw)), pipelineDigest(raw)
			execution := actualWorkloadExecution(t, snapshot, intent)
			result, err := workspace.Collect(context.Background(), filepath.Join(root, vector.name, "result"), execution)
			if err != nil || len(result.Files) != 4 {
				t.Fatalf("actual trainer files failed the production collector: files=%d err=%v", len(result.Files), err)
			}
		})
	}
}

func TestCollectActualFedoraLoRAArtifacts(t *testing.T) {
	root := os.Getenv("ANI_MODELDEV_WORKLOAD_ROOT")
	name := os.Getenv("ANI_MODELDEV_LORA_VECTOR_DIRECTORY")
	if root == "" || name == "" {
		t.Skip("requires task-owned actual Fedora LoRA artifacts")
	}
	snapshot := conformance.FinetuningSnapshotV1()
	intent := conformance.FinetuningIntentV1()
	rate, dropout := "0.0003", "0.125"
	steps, batch, accumulation, cutoff, rank, alpha, seed := int64(7), int64(2), int64(3), int64(96), int64(4), int64(7), uint32(7)
	intent.Finetuning = &cpup01.FinetuningParameters{LearningRate: &rate, MaxSteps: &steps, PerDeviceTrainBatchSize: &batch, GradientAccumulationSteps: &accumulation, CutoffLen: &cutoff, LoraRank: &rank, LoraAlpha: &alpha, LoraDropout: &dropout, Seed: &seed}
	snapshot.Program.ResolvedParameters, _ = intent.ResolveParameters()
	settings := &snapshot.Finetuning.ManagedSettings
	settings.PerDeviceTrainBatchSize, settings.GradientAccumulationSteps, settings.CutoffLen = batch, accumulation, cutoff
	settings.LoraRank, settings.LoraAlpha, settings.LoraDropout = rank, alpha, dropout
	settings.Seed, settings.DataSeed = int64(seed), int64(seed)
	snapshot.Input.RowCount = 17
	directory := filepath.Join(root, name, "workspace")
	raw, err := os.ReadFile(filepath.Join(directory, "inputs/data/finetune_data.json"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Input.Object.SizeBytes, snapshot.Input.Object.SHA256 = int64(len(raw)), pipelineDigest(raw)
	var base struct {
		Files []struct {
			Path   string `json:"path"`
			Bytes  int64  `json:"bytes,string"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}
	raw, err = os.ReadFile(filepath.Join(directory, "inputs/base_manifest.json"))
	if err != nil || json.Unmarshal(raw, &base) != nil {
		t.Fatal("missing actual base member inventory")
	}
	snapshot.Finetuning.BaseModel.Files = nil
	for _, file := range base.Files {
		snapshot.Finetuning.BaseModel.Files = append(snapshot.Finetuning.BaseModel.Files, cpup01.BaseModelFile{Path: file.Path, SizeBytes: file.Bytes, SHA256: file.SHA256})
	}
	baseHash, err := snapshot.Finetuning.BaseModel.BaseManifestSHA256()
	if err != nil || baseHash != pipelineDigest(raw) {
		t.Fatal("workload base manifest must use the same canonical contract as Go")
	}
	arguments := func(role string) []string {
		return []string{role, "--configuration", "/workspace/inputs/finetune_configuration.json", "--workspace", "/workspace", "--expected-input-sha256", snapshot.Input.Object.SHA256, "--expected-input-bytes", strconv.FormatInt(snapshot.Input.Object.SizeBytes, 10), "--expected-base-manifest-sha256", baseHash}
	}
	snapshot.Program.ResolvedArgs = arguments("train")
	snapshot.Finetuning.ExportProgram.ResolvedArgs, snapshot.Finetuning.VerifyProgram.ResolvedArgs = arguments("export"), arguments("verify")
	configuration := cpup01.FinetuningConfiguration{ParameterContractVersion: snapshot.Finetuning.ParameterContractVersion, Parameters: snapshot.Program.ResolvedParameters, ManagedSettings: *settings}
	snapshot.Finetuning.ResolvedConfigurationSHA256, err = configuration.Digest()
	if err != nil {
		t.Fatal(err)
	}
	required := []cpup01.RequiredOutput{}
	for _, file := range snapshot.OutputContract.RequiredFiles {
		if file.Role != "HF_MODEL" {
			required = append(required, file)
		}
	}
	for _, name := range []string{"Modelfile", "added_tokens.json", "config.json", "generation_config.json", "merges.txt", "model.safetensors", "special_tokens_map.json", "tokenizer.json", "tokenizer_config.json", "vocab.json"} {
		inTar := true
		required = append(required, cpup01.RequiredOutput{Role: "HF_MODEL", RelativePath: "export/merged/" + name, MaxSizeBytes: 2 << 30, InTar: &inTar})
	}
	snapshot.OutputContract.RequiredFiles = required
	execution := actualWorkloadExecution(t, snapshot, intent)
	result, err := workspace.Collect(context.Background(), directory, execution)
	if err != nil || len(result.Files) != 18 {
		t.Fatalf("actual LoRA/export/verify files failed the production collector: files=%d err=%v", len(result.Files), err)
	}
}

func actualWorkloadExecution(t *testing.T, snapshot cpup01.Snapshot, intent cpup01.Intent) biz.Execution {
	t.Helper()
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	execution := biz.Execution{Admission: biz.Admission{TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:user:workload-adapter-test", OperationID: "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff", ExecutionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", Intent: intent, IntentHash: intentHash, Snapshot: snapshot, AcceptedAt: snapshot.Timing.AcceptedAt}}
	refreshSpec(t, &execution)
	return execution
}

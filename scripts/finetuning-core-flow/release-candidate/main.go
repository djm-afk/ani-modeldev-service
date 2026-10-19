// Command release-candidate runs on Fedora and writes an immutable candidate.
// It uses confirmed task receipts and the formal Release codec; it neither
// publishes a Release nor changes any current binding or public selection.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

type image struct {
	VersionID string `json:"version_id"`
	Digest    string `json:"digest"`
}

// Environment identities/scopes are deliberately supplied to admission by the
// existing ENV facts reader, rather than added to the immutable Release schema.
type facts struct {
	ReleaseID             string            `json:"release_id"`
	PresetID              string            `json:"preset_id"`
	Runtime               cpup01.RuntimeRef `json:"runtime"`
	TrainingImage         image             `json:"training_image"`
	ExportImage           image             `json:"export_image"`
	VerifyImage           image             `json:"verify_image"`
	WorkspaceStorageClass string            `json:"workspace_storage_class"`
}

type pipelineReceipt struct {
	Phase             string `json:"phase"`
	PipelineID        string `json:"pipeline_id"`
	PipelineVersionID string `json:"pipeline_version_id"`
	PipelineIRSHA256  string `json:"pipeline_ir_sha256"`
}

type storageReceipt struct {
	BaseVersionID string `json:"base_version_id"`
}

type runEvidence struct {
	LearningRate        string         `json:"learning_rate"`
	MaxSteps            int64          `json:"max_steps"`
	OptimizerSteps      int64          `json:"actual_optimizer_steps"`
	Rows                int64          `json:"actual_effective_source_rows"`
	ExitCodes           map[string]int `json:"role_exit_codes"`
	ConfigurationSHA256 string         `json:"resolved_and_effective_configuration_sha256"`
	Dtypes              string         `json:"all_four_dtypes"`
	FiniteLogits        bool           `json:"finite_logits"`
}

type workloadEvidence struct {
	Scope  string `json:"scope"`
	Status string `json:"status"`
	Host   string `json:"host"`
	Image  struct {
		EntrySHA256   string `json:"entry_sha256"`
		MappingSHA256 string `json:"configuration_codec_sha256"`
	} `json:"image"`
	A             runEvidence `json:"A"`
	B             runEvidence `json:"B"`
	MergedMembers []string    `json:"merged_members"`
}

func readJSON(path string, target any, strict bool) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("%s: trailing JSON", path)
	}
	return nil
}

func hash(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func hashFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return hash(raw), nil
}

func settings() cpup01.FinetuningManagedSettings {
	return cpup01.FinetuningManagedSettings{Stage: "sft", FinetuningType: "lora", LoraTarget: "all", LoraRank: 8, LoraAlpha: 16, CutoffLen: 128, PerDeviceTrainBatchSize: 1, GradientAccumulationSteps: 1, Seed: 42, DataSeed: 42, Device: "cpu", FP16: false, BF16: false, FlashAttn: "disabled", ReportTo: "none", SaveStrategy: "no", Template: "qwen", TrainDtype: "float32", ExportDtype: "float32", VerifyDtype: "float32", LRSchedulerType: "linear", WarmupRatio: "0", WeightDecay: "0", LoraDropout: "0", NumTrainEpochs: 3, Optim: "adamw_torch", LoggingSteps: 1}
}

func parameters(rate string, steps int64) []cpup01.Parameter {
	return []cpup01.Parameter{{Name: "learning_rate", Type: "DECIMAL", Value: rate}, {Name: "max_steps", Type: "INTEGER", Value: fmt.Sprint(steps)}}
}

var mergedMembers = []string{"Modelfile", "added_tokens.json", "config.json", "generation_config.json", "merges.txt", "model.safetensors", "special_tokens_map.json", "tokenizer.json", "tokenizer_config.json", "vocab.json"}

func validateEvidence(evidence workloadEvidence, entrySHA, mappingSHA string) error {
	if evidence.Scope != "WORKLOAD" || evidence.Status != "PASS" || evidence.Host != "fedora" || entrySHA != evidence.Image.EntrySHA256 || mappingSHA != evidence.Image.MappingSHA256 || !slices.Equal(evidence.MergedMembers, mergedMembers) {
		return fmt.Errorf("WORKLOAD evidence scope, implementation, mapping or merged members mismatch")
	}
	for _, expected := range []struct {
		run   runEvidence
		rate  string
		steps int64
	}{{evidence.A, "0.0001", 20}, {evidence.B, "0.0002", 10}} {
		run := expected.run
		configuration := cpup01.FinetuningConfiguration{ParameterContractVersion: cpup01.FinetuningParameterContractVersion, Parameters: parameters(expected.rate, expected.steps), ManagedSettings: settings()}
		digest, err := configuration.Digest()
		if err != nil {
			return err
		}
		if run.LearningRate != expected.rate || run.MaxSteps != expected.steps || run.OptimizerSteps != expected.steps || run.Rows != 64 || run.Dtypes != "float32" || !run.FiniteLogits || run.ConfigurationSHA256 != digest || len(run.ExitCodes) != 3 {
			return fmt.Errorf("WORKLOAD complete parameter combination/effective facts mismatch")
		}
		for _, role := range []string{"train", "export", "verify"} {
			if code, ok := run.ExitCodes[role]; !ok || code != 0 {
				return fmt.Errorf("WORKLOAD %s did not exit zero", role)
			}
		}
	}
	return nil
}

func arguments(mode string) []cpup01.ReleaseArgument {
	return []cpup01.ReleaseArgument{{Literal: mode}, {Literal: "--configuration"}, {Literal: "/workspace/inputs/finetune_configuration.json"}, {Literal: "--workspace"}, {Literal: "/workspace"}, {Literal: "--expected-input-sha256"}, {Source: "INPUT_SHA256"}, {Literal: "--expected-input-bytes"}, {Source: "INPUT_BYTES"}, {Literal: "--expected-base-manifest-sha256"}, {Source: "BASE_MANIFEST_SHA256"}}
}

func candidate(input facts, pipeline pipelineReceipt, storage storageReceipt, entrySHA, mappingSHA string) (cpup01.ReleaseDocument, error) {
	if pipeline.Phase != "CONFIRMED" || input.WorkspaceStorageClass == "" {
		return cpup01.ReleaseDocument{}, fmt.Errorf("confirmed PipelineVersion and explicit workspace storage class required")
	}
	command := []string{"/opt/venv/bin/python", "-I", "/opt/finetuning/finetune_entry.py"}
	component := func(mode string, image image) cpup01.ReleaseComponentProgram {
		return cpup01.ReleaseComponentProgram{ImageVersionID: image.VersionID, ImageDigest: image.Digest, Command: append([]string{}, command...), ArgsTemplate: arguments(mode), ImplementationSHA256: entrySHA}
	}
	a, b := parameters("0.0001", 20), parameters("0.0002", 10)
	managed := settings()
	// Managed settings contain only fixed ASCII scalars, so Marshal yields the
	// exact canonical projection used by the actual Python entry's recipe hash.
	recipe, err := json.Marshal(managed)
	if err != nil {
		return cpup01.ReleaseDocument{}, err
	}
	files := make([]cpup01.RequiredOutput, 0, len(mergedMembers)+8)
	add := func(role, path string, inTar bool, max int64) {
		flag := inTar
		files = append(files, cpup01.RequiredOutput{Role: role, RelativePath: path, MaxSizeBytes: max, InTar: &flag})
	}
	for _, name := range mergedMembers {
		max := int64(16 << 20)
		if name == "model.safetensors" {
			max = 2 << 30
		}
		add("HF_MODEL", "export/merged/"+name, true, max)
	}
	add("LORA_ADAPTER", "training/adapter/adapter_config.json", false, 1<<20)
	add("LORA_ADAPTER", "training/adapter/adapter_model.safetensors", false, 1<<28)
	add("METRICS", "reports/train/metrics.jsonl", false, 1<<20)
	add("SUMMARY", "reports/train/summary.json", false, 64<<10)
	add("SOURCE_METADATA", "reports/train/adapter_manifest.json", false, 64<<10)
	add("EXPORT_REPORT", "reports/export/merged_manifest.json", false, 64<<10)
	add("EXPORT_REPORT", "reports/export/export.json", false, 64<<10)
	add("VERIFY_REPORT", "reports/verify/verify.json", false, 64<<10)
	maxTar := int64(3 << 30)
	document := cpup01.ReleaseDocument{SchemaVersion: cpup01.ReleaseSchemaVersion, ReleaseID: input.ReleaseID, PresetID: input.PresetID, Kind: "FINETUNING", DeliveryMode: "SAVE_ARTIFACTS", PipelineID: pipeline.PipelineID, PipelineVersionID: pipeline.PipelineVersionID, PipelineIRSHA256: pipeline.PipelineIRSHA256, Runtime: input.Runtime,
		Program:   cpup01.ReleaseProgram{ImageVersionID: input.TrainingImage.VersionID, ImageDigest: input.TrainingImage.Digest, Command: command, ArgsTemplate: arguments("train"), ParameterContractVersion: cpup01.FinetuningParameterContractVersion, DefaultParameters: a, ParameterRules: []cpup01.ParameterRule{{Name: "learning_rate", Type: "DECIMAL", AllowedValues: []cpup01.Parameter{a[0], b[0]}}, {Name: "max_steps", Type: "INTEGER", AllowedValues: []cpup01.Parameter{a[1], b[1]}}}, AllowedParameterSets: [][]cpup01.Parameter{a, b}},
		Resources: cpup01.CPUResources{Nodes: 1, ProcessesPerNode: 1, RequestMillicpu: 2000, LimitMillicpu: 4000, RequestMemoryBytes: 6 << 30, LimitMemoryBytes: 8 << 30}, Workspace: cpup01.WorkspaceContract{Mode: "EXECUTION_PVC", StorageClass: input.WorkspaceStorageClass, CapacityBytes: 20 << 30, InputSubpath: "inputs", TrainingSubpath: "training", ReportsSubpath: "reports", PublicationSubpath: "publication"}, OutputContract: cpup01.OutputContract{SchemaVersion: "ani.finetune.output.v1", OutputKind: "MERGED_HF_MODEL", DeliveryMode: "SAVE_ARTIFACTS", RequiredFiles: files, MaxFileCount: 64, MaxTotalBytes: 4 << 30, CreateTarBundle: true, MaxTarBytes: &maxTar}, ExecutionTimeoutSeconds: 10800,
		Finetuning: &cpup01.FinetuningRelease{BaseModelVersionID: storage.BaseVersionID, ManagedSettings: managed, ExportProgram: component("export", input.ExportImage), VerifyProgram: component("verify", input.VerifyImage), RecipeSHA256: hash(recipe), MappingSHA256: mappingSHA, Budgets: cpup01.ComponentBudgets{PrepareTimeoutSeconds: 900, TrainingTimeoutSeconds: 3600, ExportTimeoutSeconds: 1800, VerifyTimeoutSeconds: 900, CollectTimeoutSeconds: 600, PublishTimeoutSeconds: 900, RPCTimeoutSeconds: 600, MaxBaseModelBytes: 2 << 30, MaxBaseModelExpandedBytes: 4 << 30, MaxSpoolBytes: 8 << 30}}}
	if _, err := document.Canonical(); err != nil {
		return cpup01.ReleaseDocument{}, err
	}
	return document, nil
}

func run() error {
	factsPath := flag.String("facts", "", "explicit final Release/image/runtime identities JSON")
	pipelinePath := flag.String("pipeline-receipt", "", "actual CONFIRMED pipeline-receipt.json")
	storagePath := flag.String("storage-receipt", "", "actual storage-receipt.json")
	evidencePath := flag.String("workload-evidence", ".scratch/finetuning-cpu/records/WORKLOAD-actual-20261014.json", "actual A+B WORKLOAD index")
	entryPath := flag.String("entry", "training/finetuning/finetune_entry.py", "exact image entry payload source")
	mappingPath := flag.String("mapping", "training/finetuning/configuration.py", "exact image configuration codec source")
	output := flag.String("output", "", "new canonical candidate file; existing files are preserved")
	flag.Parse()
	if flag.NArg() != 0 || *factsPath == "" || *pipelinePath == "" || *storagePath == "" || *output == "" {
		return fmt.Errorf("--facts --pipeline-receipt --storage-receipt --output are required; run only on Fedora")
	}
	var input facts
	var pipeline pipelineReceipt
	var storage storageReceipt
	var evidence workloadEvidence
	for _, read := range []struct {
		path   string
		target any
		strict bool
	}{{*factsPath, &input, true}, {*pipelinePath, &pipeline, false}, {*storagePath, &storage, false}, {*evidencePath, &evidence, false}} {
		if err := readJSON(read.path, read.target, read.strict); err != nil {
			return err
		}
	}
	entrySHA, err := hashFile(*entryPath)
	if err != nil {
		return err
	}
	mappingSHA, err := hashFile(*mappingPath)
	if err != nil {
		return err
	}
	if err := validateEvidence(evidence, entrySHA, mappingSHA); err != nil {
		return err
	}
	document, err := candidate(input, pipeline, storage, entrySHA, mappingSHA)
	if err != nil {
		return err
	}
	raw, err := document.Canonical()
	if err != nil {
		return err
	}
	_, digest, err := cpup01.ParseRelease(raw)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"scope": "IMMUTABLE_CANDIDATE", "release_path": *output, "release_id": document.ReleaseID, "release_digest": digest, "bytes": len(raw), "approved_combinations": 2, "merged_members": len(mergedMembers), "entry_sha256": entrySHA, "mapping_sha256": mappingSHA, "recipe_sha256": document.Finetuning.RecipeSHA256, "enabled": false})
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

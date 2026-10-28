package conformance

import (
	"strconv"
	"strings"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// FinetuningIntentV1 and related fixtures identify no real image, provider,
// workload or environment. They are shared contract vectors for consumer tests.
// The finetuning contract is unchanged: no training image selection is present.
func FinetuningIntentV1() cpup01.Intent {
	base := "55555555-5555-4555-8555-555555555555"
	name := "cpu-ft-contract"
	return cpup01.Intent{Name: "CPU FT", Kind: "FINETUNING", ImplementationID: "builtin.sft_lora", ComputePresetID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", DatasetVersionID: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", BaseModelVersionID: &base, Finetuning: &cpup01.FinetuningParameters{}, NewModelName: &name}
}

func FinetuningManagedSettingsV1() cpup01.FinetuningManagedSettings {
	return cpup01.FinetuningManagedSettings{Stage: "sft", FinetuningType: "lora", LoraTarget: "all", LoraRank: 8, LoraAlpha: 16, CutoffLen: 128, PerDeviceTrainBatchSize: 1, GradientAccumulationSteps: 1, Seed: 42, DataSeed: 42, Device: "cpu", FlashAttn: "disabled", ReportTo: "none", SaveStrategy: "no", Template: "qwen", TrainDtype: "float32", ExportDtype: "float32", VerifyDtype: "float32", LRSchedulerType: "linear", WarmupRatio: "0", WeightDecay: "0", LoraDropout: "0", NumTrainEpochs: 3, Optim: "adamw_torch", LoggingSteps: 1}
}

// FinetuningMaterialV21 keeps the finetuning material shape exactly as it was:
// a pinned image_version_id plus digest, plus the export/verify components. The
// values are the original fixture's, so the finetuning snapshot hash is
// unchanged by the training-image work.
func FinetuningMaterialV21() cpup01.ImplementationMaterial {
	material := cpup01.ImplementationMaterial{
		SchemaVersion:  cpup01.MaterialSchemaVersion,
		Implementation: cpup01.ImplementationSnapshot{ImplementationID: "builtin.sft_lora", Version: "1", ParameterContractVersion: cpup01.FinetuningParameterContractVersion, PipelineID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", PipelineVersionID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", PipelineIRSHA256: strings.Repeat("b", 64)},
		Program:        &cpup01.MaterialProgram{ImageVersionID: "ffffffff-ffff-4fff-8fff-ffffffffffff", ImageDigest: "registry.example.test/cpu/mlp@sha256:" + strings.Repeat("e", 64)},
	}
	material.Program.Command = []string{"/opt/venv/bin/python", "-I", "/opt/finetuning/finetune_entry.py"}

	arguments := func(mode string) []cpup01.MaterialArgument {
		return []cpup01.MaterialArgument{{Literal: mode}, {Literal: "--configuration"}, {Literal: "/workspace/inputs/finetune_configuration.json"}, {Literal: "--workspace"}, {Literal: "/workspace"}, {Literal: "--expected-input-sha256"}, {Source: "INPUT_SHA256"}, {Literal: "--expected-input-bytes"}, {Source: "INPUT_BYTES"}, {Literal: "--expected-base-manifest-sha256"}, {Source: "BASE_MANIFEST_SHA256"}}
	}
	material.Program.ArgsTemplate = arguments("train")
	component := func(mode string) cpup01.ComponentProgram {
		return cpup01.ComponentProgram{ImageVersionID: material.Program.ImageVersionID, ImageDigest: material.Program.ImageDigest, Command: append([]string{}, material.Program.Command...), ArgsTemplate: arguments(mode), ImplementationSHA256: strings.Repeat("a", 64)}
	}
	material.Finetuning = &cpup01.FinetuningMaterial{MaxSequenceLength: 32768, ManagedSettings: FinetuningManagedSettingsV1(), ExportProgram: component("export"), VerifyProgram: component("verify"), RecipeSHA256: strings.Repeat("b", 64), MappingSHA256: strings.Repeat("c", 64), Budgets: cpup01.ComponentBudgets{RPCTimeoutSeconds: 30, MaxBaseModelBytes: 2 << 30, MaxBaseModelExpandedBytes: 4 << 30, MaxSpoolBytes: 8 << 30}}
	maxTar := int64(3 << 30)
	files := []cpup01.RequiredOutput{}
	add := func(role, path string, inTar bool, max int64) {
		value := inTar
		files = append(files, cpup01.RequiredOutput{Role: role, RelativePath: path, MaxSizeBytes: max, InTar: &value})
	}
	for _, name := range []string{"config.json", "model.safetensors", "tokenizer.json", "tokenizer_config.json"} {
		add("HF_MODEL", "export/merged/"+name, true, 2<<30)
	}
	add("LORA_ADAPTER", "training/adapter/adapter_config.json", false, 1<<20)
	add("LORA_ADAPTER", "training/adapter/adapter_model.safetensors", false, 1<<28)
	add("METRICS", "reports/train/metrics.jsonl", false, 1<<20)
	add("SUMMARY", "reports/train/summary.json", false, 1<<20)
	add("SOURCE_METADATA", "reports/train/adapter_manifest.json", false, 1<<20)
	add("EXPORT_REPORT", "reports/export/merged_manifest.json", false, 1<<20)
	add("EXPORT_REPORT", "reports/export/export.json", false, 1<<20)
	add("VERIFY_REPORT", "reports/verify/verify.json", false, 1<<20)
	material.Workspace = cpup01.WorkspaceContract{Mode: "EXECUTION_PVC", StorageClass: "task-workspace", CapacityBytes: 2147483648, InputSubpath: "inputs", TrainingSubpath: "training", ReportsSubpath: "reports", PublicationSubpath: "publication"}
	material.OutputContract = cpup01.OutputContract{SchemaVersion: "ani.finetune.output.v1", OutputKind: "MERGED_HF_MODEL", DeliveryMode: "SAVE_ARTIFACTS", RequiredFiles: files, MaxFileCount: 64, MaxTotalBytes: 4 << 30, CreateTarBundle: true, MaxTarBytes: &maxTar}
	return material
}

func FinetuningSnapshotV1() cpup01.Snapshot {
	snapshot := SnapshotV1()
	material := FinetuningMaterialV21()
	intent := FinetuningIntentV1()
	snapshot.SchemaVersion = cpup01.SnapshotSchemaVersion
	snapshot.Kind = "FINETUNING"
	snapshot.Implementation = material.Implementation
	// The training program is replaced by the finetuning program identity.
	snapshot.TrainingProgram = nil
	snapshot.Program = &cpup01.ProgramRef{ImageVersionID: material.Program.ImageVersionID, ImageDigest: material.Program.ImageDigest}
	_, snapshot.IdentityRef.IntentHash, _ = cpup01.CanonicalIntent(intent)
	snapshot.Input.Format = "JSON"
	snapshot.Input.SchemaVersion = cpup01.FinetuningInputSchemaVersion
	snapshot.Input.RowCount = 64
	snapshot.Input.FeatureCount = 0
	snapshot.Program.Command = append([]string{}, material.Program.Command...)
	snapshot.Program.ResolvedParameters, _ = intent.ResolveParameters()
	snapshot.Program.ResolvedArgs = []string{"train", "--configuration", "/workspace/inputs/finetune_configuration.json", "--workspace", "/workspace", "--expected-input-sha256", snapshot.Input.Object.SHA256, "--expected-input-bytes", strconv.FormatInt(snapshot.Input.Object.SizeBytes, 10), "--expected-base-manifest-sha256", strings.Repeat("d", 64)}
	version := "base-version-1"
	baseObject := cpup01.FixedObjectRef{StorageConnectionID: "model-store-v1", Bucket: "base-models", Key: "approved/qwen/base.tar", VersionID: &version, SizeBytes: 20480, SHA256: strings.Repeat("e", 64)}
	base := cpup01.BaseModelRef{ModelID: "66666666-6666-4666-8666-666666666666", VersionID: *intent.BaseModelVersionID, Family: "Qwen2.5-0.5B-Instruct", Format: "safetensors", Object: baseObject, Scope: cpup01.StorageScope{StorageConnectionID: "model-store-v1", Bucket: "base-models", ApprovedPrefix: "approved/qwen", CredentialReference: "modeldev-base-read-v1"}}
	for _, name := range []string{"config.json", "model.safetensors", "tokenizer.json", "tokenizer_config.json"} {
		base.Files = append(base.Files, cpup01.BaseModelFile{Path: name, SizeBytes: 1024, SHA256: strings.Repeat("f", 64)})
	}
	baseManifestHash, err := base.BaseManifestSHA256()
	if err != nil {
		panic("invalid synthetic base manifest")
	}
	snapshot.Program.ResolvedArgs[len(snapshot.Program.ResolvedArgs)-1] = baseManifestHash
	program := func(mode string) cpup01.ProgramRef {
		arguments := append([]string{}, snapshot.Program.ResolvedArgs...)
		arguments[0] = mode
		return cpup01.ProgramRef{ImageVersionID: material.Program.ImageVersionID, ImageDigest: material.Program.ImageDigest, Command: append([]string{}, material.Program.Command...), ResolvedArgs: arguments, ResolvedParameters: []cpup01.Parameter{}}
	}
	configuration := cpup01.FinetuningConfiguration{ParameterContractVersion: cpup01.FinetuningParameterContractVersion, Parameters: snapshot.Program.ResolvedParameters, ManagedSettings: material.Finetuning.ManagedSettings}
	digest, err := configuration.Digest()
	if err != nil {
		panic("invalid synthetic finetuning configuration")
	}
	snapshot.Finetuning = &cpup01.FinetuningSnapshot{ParameterContractVersion: cpup01.FinetuningParameterContractVersion, BaseModel: base, NewModelName: intent.NewModelName, ManagedSettings: material.Finetuning.ManagedSettings, ResolvedConfigurationSHA256: digest, ExportProgram: program("export"), VerifyProgram: program("verify"), RecipeSHA256: material.Finetuning.RecipeSHA256, MappingSHA256: material.Finetuning.MappingSHA256, Budgets: material.Finetuning.Budgets}
	snapshot.OutputContract = material.OutputContract
	if err := snapshot.Validate(); err != nil {
		panic("invalid synthetic finetuning snapshot: " + err.Error())
	}
	return snapshot
}

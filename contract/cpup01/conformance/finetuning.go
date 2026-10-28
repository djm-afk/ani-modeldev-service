package conformance

import (
	"strings"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// FinetuningIntentV1 and related fixtures identify no real image, provider,
// workload or environment. They are shared contract vectors for consumer tests.
func FinetuningIntentV1() cpup01.Intent {
	base := "55555555-5555-4555-8555-555555555555"
	name := "cpu-ft-contract"
	return cpup01.Intent{Name: "CPU FT", Kind: "FINETUNING", ImplementationID: "builtin.sft_lora", ComputePresetID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", DatasetVersionID: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", BaseModelVersionID: &base, Finetuning: &cpup01.FinetuningParameters{}, NewModelName: &name}
}

func FinetuningManagedSettingsV1() cpup01.FinetuningManagedSettings {
	return cpup01.FinetuningManagedSettings{Stage: "sft", FinetuningType: "lora", LoraTarget: "all", LoraRank: 8, LoraAlpha: 16, CutoffLen: 128, PerDeviceTrainBatchSize: 1, GradientAccumulationSteps: 1, Seed: 42, DataSeed: 42, Device: "cpu", FlashAttn: "disabled", ReportTo: "none", SaveStrategy: "no", TrainDtype: "float32", ExportDtype: "float32", VerifyDtype: "float32", LRSchedulerType: "linear", WarmupRatio: "0", WeightDecay: "0", LoraDropout: "0", NumTrainEpochs: 3, Optim: "adamw_torch", LoggingSteps: 1}
}

func FinetuningMaterialV21() cpup01.ImplementationMaterial {
	material := MaterialV21()
	material.ComponentResources = ComponentResourceFixtures("FINETUNING")
	material.Implementation.ImplementationID = "builtin.sft_lora"
	material.Implementation.ParameterContractVersion = cpup01.FinetuningParameterContractVersion
	material.Program.Command = []string{"/actual/image/python", "-I", "/opt/ani-modeldev/training/finetuning/finetune_entry.py"}

	arguments := func(mode string) []cpup01.MaterialArgument {
		return []cpup01.MaterialArgument{{Literal: mode}, {Literal: "--configuration"}, {Literal: "/workspace/inputs/finetune_configuration.json"}, {Literal: "--workspace"}, {Literal: "/workspace"}, {Literal: "--dataset-manifest"}, {Literal: "/workspace/inputs/dataset-manifest.json"}, {Literal: "--expected-dataset-manifest-sha256"}, {Source: "DATASET_MANIFEST_SHA256"}, {Literal: "--expected-base-manifest-sha256"}, {Source: "BASE_MANIFEST_SHA256"}, {Literal: "--expected-template-plan-sha256"}, {Source: "TEMPLATE_PLAN_SHA256"}}
	}
	material.Program.ArgsTemplate = arguments("train")
	component := func(mode string) cpup01.ComponentProgram {
		return cpup01.ComponentProgram{ImageID: material.Program.ImageID, ImageVersion: material.Program.ImageVersion, ImageScope: material.Program.ImageScope, ImageDigest: material.Program.ImageDigest, Command: append([]string{}, material.Program.Command...), ArgsTemplate: arguments(mode), ImplementationSHA256: strings.Repeat("a", 64)}
	}
	material.Finetuning = &cpup01.FinetuningMaterial{ManagedSettings: FinetuningManagedSettingsV1(), ExportProgram: component("export"), VerifyProgram: component("verify"), RecipeSHA256: strings.Repeat("b", 64), MappingSHA256: strings.Repeat("c", 64), RuntimeCapabilities: FinetuningRuntimeCapabilities(), ModelTemplateAssociations: []cpup01.ModelTemplateAssociation{{ModelVersionID: "55555555-5555-4555-8555-555555555555", TemplateID: "qwen"}}}
	files := []cpup01.RequiredOutput{}
	add := func(role, path string) {
		files = append(files, cpup01.RequiredOutput{Role: role, RelativePath: path, InTar: false})
	}
	add("METRICS", "reports/train/metrics.jsonl")
	add("SUMMARY", "reports/train/summary.json")
	add("SOURCE_METADATA", "reports/train/adapter_manifest.json")
	add("SOURCE_METADATA", "reports/train/effective-template.json")
	add("SOURCE_METADATA", "reports/train/effective-template.jinja")
	add("EXPORT_REPORT", "reports/export/merged_manifest.json")
	add("EXPORT_REPORT", "reports/export/export.json")
	add("VERIFY_REPORT", "reports/verify/verify.json")
	material.OutputContract = cpup01.OutputContract{SchemaVersion: cpup01.OutputContractSchemaVersion, OutputKind: "MERGED_HF_MODEL", DeliveryMode: "SAVE_ARTIFACTS", RequiredFiles: files, CreateTarBundle: true}

	return material
}

func FinetuningSnapshotV1() cpup01.Snapshot {
	snapshot := SnapshotV1()
	material := FinetuningMaterialV21()
	intent := FinetuningIntentV1()
	snapshot.Kind = "FINETUNING"
	snapshot.Implementation = material.Implementation
	_, snapshot.IdentityRef.IntentHash, _ = cpup01.CanonicalIntent(intent)
	snapshot.Input.Format = "JSON"
	snapshot.Input.SchemaVersion = cpup01.FinetuningInputSchemaVersion
	snapshot.Input.RowCount = 64
	snapshot.Input.PreparedDataset = cpup01.PreparedDatasetFacts{Format: "ALPACA_JSONL", TransformID: "alpaca-array-compact-jsonl-v3", SizeBytes: snapshot.Input.Object.SizeBytes - 1, SHA256: strings.Repeat("a", 64), RowCount: 64}
	snapshot.Input.FeatureCount = 0
	snapshot.Program.Command = append([]string{}, material.Program.Command...)
	snapshot.Program.ResolvedParameters, _ = intent.ResolveParameters()
	dataset, _ := cpup01.BuildDatasetManifest(snapshot.Input)
	datasetDigest, _ := dataset.Digest()
	snapshot.Program.ResolvedArgs = []string{"train", "--configuration", "/workspace/inputs/finetune_configuration.json", "--workspace", "/workspace", "--dataset-manifest", "/workspace/inputs/dataset-manifest.json", "--expected-dataset-manifest-sha256", datasetDigest, "--expected-base-manifest-sha256", strings.Repeat("d", 64)}
	version := "base-version-1"
	baseObject := cpup01.FixedObjectRef{StorageConnectionID: "model-store-v1", Bucket: "base-models", Key: "approved/qwen/base.tar", VersionID: &version, SizeBytes: 20480, SHA256: strings.Repeat("e", 64)}
	base := cpup01.BaseModelRef{ModelID: "66666666-6666-4666-8666-666666666666", VersionID: *intent.BaseModelVersionID, Format: "safetensors", Object: baseObject, Scope: cpup01.StorageScope{StorageConnectionID: "model-store-v1", Bucket: "base-models", ApprovedPrefix: "approved/qwen", CredentialReference: "modeldev-base-read-v1"}}
	for _, name := range []string{"config.json", "model.safetensors", "tokenizer.json", "tokenizer_config.json"} {
		base.Files = append(base.Files, cpup01.BaseModelFile{Path: name, SizeBytes: 1024, SHA256: strings.Repeat("f", 64)})
	}
	base.Facts = NormalizedModelFacts()
	baseManifestHash, err := base.BaseManifestSHA256()
	if err != nil {
		panic("invalid synthetic base manifest")
	}
	snapshot.Program.ResolvedArgs[len(snapshot.Program.ResolvedArgs)-1] = baseManifestHash
	planDigest, _ := FinetuningTemplatePlan().Digest()
	snapshot.Program.ResolvedArgs = append(snapshot.Program.ResolvedArgs, "--expected-template-plan-sha256", planDigest)
	program := func(mode string) cpup01.ProgramRef {
		arguments := append([]string{}, snapshot.Program.ResolvedArgs...)
		arguments[0] = mode
		return cpup01.ProgramRef{ImageID: material.Program.ImageID, ImageVersion: material.Program.ImageVersion, ImageScope: material.Program.ImageScope, ImageDigest: material.Program.ImageDigest, Command: append([]string{}, material.Program.Command...), ResolvedArgs: arguments, ResolvedParameters: []cpup01.Parameter{}}
	}
	configuration := cpup01.FinetuningConfiguration{ParameterContractVersion: cpup01.FinetuningParameterContractVersion, Parameters: snapshot.Program.ResolvedParameters, ManagedSettings: material.Finetuning.ManagedSettings}
	digest, err := configuration.Digest()
	if err != nil {
		panic("invalid synthetic finetuning configuration")
	}
	snapshot.Finetuning = &cpup01.FinetuningSnapshot{ParameterContractVersion: cpup01.FinetuningParameterContractVersion, BaseModel: base, NewModelName: intent.NewModelName, ManagedSettings: material.Finetuning.ManagedSettings, ResolvedConfigurationSHA256: digest, ExportProgram: program("export"), VerifyProgram: program("verify"), RecipeSHA256: material.Finetuning.RecipeSHA256, MappingSHA256: material.Finetuning.MappingSHA256, RuntimeCapabilities: material.Finetuning.RuntimeCapabilities, TemplatePlan: FinetuningTemplatePlan()}
	snapshot.OutputContract = material.OutputContract
	if err := snapshot.Validate(); err != nil {
		panic("invalid synthetic finetuning snapshot: " + err.Error())
	}
	return snapshot
}

// These values are synthetic contract fixtures, not image inspection evidence.
func FinetuningRuntimeCapabilities() cpup01.RuntimeCapabilities {
	return cpup01.RuntimeCapabilities{SchemaVersion: cpup01.FinetuningCapabilitiesSchemaVersion, ImageDigest: SnapshotV1().Program.ImageDigest, EntrypointSHA256: strings.Repeat("1", 64), Packages: []cpup01.RuntimePackage{{Name: "fixture-runtime", Version: "1"}}, CausalLMModelTypes: []string{"qwen2", "llama"}, RegisteredTemplates: []cpup01.RegisteredTemplate{{ID: "qwen", SourceSHA256: strings.Repeat("2", 64)}}, ContextLimitFields: []cpup01.ContextLimitField{{Source: "config.json", Field: "max_position_embeddings"}, {Source: "config.json", Field: "n_positions"}, {Source: "tokenizer_config.json", Field: "model_max_length"}}}
}
func FinetuningTemplatePlan() cpup01.TemplatePlan {
	capabilities := FinetuningRuntimeCapabilities()
	digest, _ := capabilities.Digest()
	id := "qwen"
	return cpup01.TemplatePlan{SchemaVersion: cpup01.TemplatePlanSchemaVersion, Mode: "NAMED", SelectionSource: "MODEL_ASSOCIATION", TemplateID: &id, SourceSHA256: strings.Repeat("2", 64), RuntimeCapabilitiesSHA256: digest, ImageDigest: capabilities.ImageDigest}
}
func NormalizedModelFacts() cpup01.NormalizedModelFacts {
	return cpup01.NormalizedModelFacts{ModelType: "qwen2", Architectures: []string{"Qwen2ForCausalLM"}, OriginalDtype: "bfloat16", ContextLimit: 32768, ContextSources: []cpup01.ContextLimitFact{{SourcePath: "config.json", SourceSHA256: strings.Repeat("f", 64), Field: "max_position_embeddings", Value: 32768}}, ConfigFile: cpup01.FileFact{Path: "config.json", SizeBytes: 1024, SHA256: strings.Repeat("f", 64)}, TokenizerConfigFile: cpup01.FileFact{Path: "tokenizer_config.json", SizeBytes: 1024, SHA256: strings.Repeat("f", 64)}, WeightLayout: cpup01.WeightLayout{Kind: "SINGLE", EntryPath: "model.safetensors", ShardPaths: []string{}}, TemplateSources: []cpup01.TemplateSource{}}
}

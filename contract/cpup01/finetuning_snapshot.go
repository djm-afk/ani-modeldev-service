package cpup01

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const FinetuningInputSchemaVersion = "ani.finetune.alpaca.v1"

// FinetuningManagedSettings is the finite, read-only program configuration.
// Decimal settings use exact strings, matching the effective entry projection.
type FinetuningManagedSettings struct {
	Stage                     string `json:"stage"`
	FinetuningType            string `json:"finetuning_type"`
	LoraTarget                string `json:"lora_target"`
	LoraRank                  int64  `json:"lora_rank"`
	LoraAlpha                 int64  `json:"lora_alpha"`
	CutoffLen                 int64  `json:"cutoff_len"`
	PerDeviceTrainBatchSize   int64  `json:"per_device_train_batch_size"`
	GradientAccumulationSteps int64  `json:"gradient_accumulation_steps"`
	Seed                      int64  `json:"seed"`
	DataSeed                  int64  `json:"data_seed"`
	Device                    string `json:"device"`
	FP16                      bool   `json:"fp16"`
	BF16                      bool   `json:"bf16"`
	FlashAttn                 string `json:"flash_attn"`
	ReportTo                  string `json:"report_to"`
	SaveStrategy              string `json:"save_strategy"`
	Template                  string `json:"template"`
	TrainDtype                string `json:"train_dtype"`
	ExportDtype               string `json:"export_dtype"`
	VerifyDtype               string `json:"verify_dtype"`
	LRSchedulerType           string `json:"lr_scheduler_type"`
	WarmupRatio               string `json:"warmup_ratio"`
	WeightDecay               string `json:"weight_decay"`
	LoraDropout               string `json:"lora_dropout"`
	NumTrainEpochs            int64  `json:"num_train_epochs"`
	Optim                     string `json:"optim"`
	LoggingSteps              int64  `json:"logging_steps"`
}

// FinetuningConfiguration is the only resolved/effective digest projection.
// It excludes paths, objects, credentials, executions, clocks and live UIDs.
type FinetuningConfiguration struct {
	ParameterContractVersion string                    `json:"parameter_contract_version"`
	Parameters               []Parameter               `json:"parameters"`
	ManagedSettings          FinetuningManagedSettings `json:"managed_settings"`
}

func (configuration FinetuningConfiguration) Canonical() ([]byte, error) {
	if configuration.ParameterContractVersion != FinetuningParameterContractVersion || validateCompleteFinetuningParameters(configuration.Parameters) != nil || invalidFinetuningManagedSettings(configuration.ManagedSettings) != "" {
		return nil, fmt.Errorf("%w: finetuning configuration", ErrInvalidArgument)
	}
	settings := configuration.ManagedSettings
	expected := map[string]string{"lora_rank": strconv.FormatInt(settings.LoraRank, 10), "lora_alpha": strconv.FormatInt(settings.LoraAlpha, 10), "cutoff_len": strconv.FormatInt(settings.CutoffLen, 10), "per_device_train_batch_size": strconv.FormatInt(settings.PerDeviceTrainBatchSize, 10), "gradient_accumulation_steps": strconv.FormatInt(settings.GradientAccumulationSteps, 10), "seed": strconv.FormatInt(settings.Seed, 10), "lora_dropout": canonicalDecimal(settings.LoraDropout)}
	for _, p := range configuration.Parameters {
		if value, ok := expected[p.Name]; ok && canonicalDecimal(p.Value) != value {
			return nil, fmt.Errorf("%w: managed parameter mismatch", ErrInvalidArgument)
		}
	}
	configuration.Parameters = canonicalParameters(configuration.Parameters)
	return encodeCanonicalJSON(configuration)
}

func (configuration FinetuningConfiguration) Digest() (string, error) {
	raw, err := configuration.Canonical()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// BaseModelRef freezes provider identities and the approved archive's byte
// inventory. Authorization/protected status are verified by the facts reader;
// this document cannot manufacture them with a caller-supplied bool.
type BaseModelRef struct {
	ModelID   string          `json:"model_id"`
	VersionID string          `json:"version_id"`
	Family    string          `json:"family"`
	Format    string          `json:"format"`
	Object    FixedObjectRef  `json:"object"`
	Scope     StorageScope    `json:"scope"`
	Files     []BaseModelFile `json:"files"`
}

type BaseModelFile struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes,string"`
	SHA256    string `json:"sha256"`
}

// BaseManifestBytes is the single byte inventory format shared with prepare
// and the managed entry. It commits source members without exposing storage.
func (model BaseModelRef) BaseManifestBytes() ([]byte, error) {
	type member struct {
		Path   string `json:"path"`
		Bytes  int64  `json:"bytes,string"`
		SHA256 string `json:"sha256"`
	}
	files := make([]member, 0, len(model.Files))
	seen := map[string]bool{}
	if len(model.Files) == 0 || len(model.Files) > 128 {
		return nil, fmt.Errorf("%w: base model files", ErrInvalidArgument)
	}
	for _, file := range model.Files {
		if !validSnapshotPath(file.Path) || seen[file.Path] || file.SizeBytes <= 0 || !snapshotSHA256Pattern.MatchString(file.SHA256) {
			return nil, fmt.Errorf("%w: base model files", ErrInvalidArgument)
		}
		seen[file.Path] = true
		files = append(files, member{Path: file.Path, Bytes: file.SizeBytes, SHA256: file.SHA256})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return encodeCanonicalJSON(struct {
		Schema string   `json:"schema"`
		Files  []member `json:"files"`
	}{Schema: "ani.finetune.base-manifest.v1", Files: files})
}

func (model BaseModelRef) BaseManifestSHA256() (string, error) {
	raw, err := model.BaseManifestBytes()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

type ComponentBudgets struct {
	RPCTimeoutSeconds         uint32 `json:"rpc_timeout_seconds"`
	MaxBaseModelBytes         int64  `json:"max_base_model_bytes,string"`
	MaxBaseModelExpandedBytes int64  `json:"max_base_model_expanded_bytes,string"`
	MaxSpoolBytes             int64  `json:"max_spool_bytes,string"`
}

type FinetuningSnapshot struct {
	ParameterContractVersion    string                    `json:"parameter_contract_version"`
	BaseModel                   BaseModelRef              `json:"base_model"`
	TargetModelID               *string                   `json:"target_model_id,omitempty"`
	NewModelName                *string                   `json:"new_model_name,omitempty"`
	ManagedSettings             FinetuningManagedSettings `json:"managed_settings"`
	ResolvedConfigurationSHA256 string                    `json:"resolved_configuration_sha256"`
	ExportProgram               ProgramRef                `json:"export_program"`
	VerifyProgram               ProgramRef                `json:"verify_program"`
	RecipeSHA256                string                    `json:"recipe_sha256"`
	MappingSHA256               string                    `json:"mapping_sha256"`
	Budgets                     ComponentBudgets          `json:"budgets"`
}

// RequiresModelRegistration keeps target presence as the canonical delivery
// choice. A validated snapshot without either target only publishes artifacts.
func (fine *FinetuningSnapshot) RequiresModelRegistration() bool {
	return fine != nil && (fine.TargetModelID != nil || fine.NewModelName != nil)
}

func invalidFinetuningManagedSettings(settings FinetuningManagedSettings) string {
	if settings.Stage != "sft" || settings.FinetuningType != "lora" || settings.LoraTarget != "all" || settings.LoraRank < 1 || settings.LoraAlpha < 1 || settings.CutoffLen < 1 || settings.PerDeviceTrainBatchSize < 1 || settings.GradientAccumulationSteps < 1 || settings.Seed < 0 || settings.Seed > 4294967295 || settings.DataSeed != settings.Seed || (settings.Device != "cpu" && settings.Device != "cuda") || settings.FP16 || settings.BF16 || settings.FlashAttn != "disabled" || settings.ReportTo != "none" || settings.SaveStrategy != "no" || settings.Template != "qwen" || settings.TrainDtype != "float32" || settings.ExportDtype != "float32" || settings.VerifyDtype != "float32" || settings.LRSchedulerType != "linear" || settings.WarmupRatio != "0" || settings.WeightDecay != "0" || validateFinetuningParameter(Parameter{Name: "lora_dropout", Type: "DECIMAL", Value: settings.LoraDropout}) != nil || settings.NumTrainEpochs != 3 || settings.Optim != "adamw_torch" || settings.LoggingSteps != 1 {
		return "managed settings"
	}
	return ""
}

func invalidComponentBudgets(budgets ComponentBudgets) string {
	if budgets.RPCTimeoutSeconds == 0 || budgets.MaxBaseModelBytes <= 0 || budgets.MaxBaseModelExpandedBytes <= 0 || budgets.MaxSpoolBytes < budgets.MaxBaseModelBytes {
		return "component budgets"
	}
	return ""
}

func invalidFixedObject(object FixedObjectRef) string {
	if !validSnapshotReference(object.StorageConnectionID) || !validSnapshotDNSName(object.Bucket) || !validSnapshotPath(object.Key) || object.SizeBytes <= 0 || !snapshotSHA256Pattern.MatchString(object.SHA256) {
		return "fixed model object"
	}
	if (object.VersionID == nil) == (object.ImmutableCopy == nil) {
		return "model immutability"
	}
	if object.VersionID != nil && (!validSnapshotText(*object.VersionID) || strings.EqualFold(*object.VersionID, "null")) {
		return "model version"
	}
	if object.ImmutableCopy != nil && !*object.ImmutableCopy {
		return "model immutable copy"
	}
	return ""
}

func invalidBaseModel(model BaseModelRef, budgets ComponentBudgets) string {
	if !validUUID(model.ModelID) || !validUUID(model.VersionID) || model.Family != "Qwen2.5-0.5B-Instruct" || model.Format != "safetensors" {
		return "base model identity"
	}
	if field := invalidFixedObject(model.Object); field != "" {
		return field
	}
	if model.Object.SizeBytes > budgets.MaxBaseModelBytes {
		return "base model bounds"
	}
	if model.Scope.Validate() != nil || model.Scope.StorageConnectionID != model.Object.StorageConnectionID || model.Scope.Bucket != model.Object.Bucket || !strings.HasPrefix(model.Object.Key, model.Scope.ApprovedPrefix+"/") {
		return "base model scope"
	}
	if len(model.Files) == 0 || len(model.Files) > 128 {
		return "base model files"
	}
	seen := map[string]bool{}
	remaining := budgets.MaxBaseModelExpandedBytes
	for _, file := range model.Files {
		if !validSnapshotPath(file.Path) || seen[file.Path] || file.SizeBytes <= 0 || file.SizeBytes > remaining || !snapshotSHA256Pattern.MatchString(file.SHA256) {
			return "base model files"
		}
		seen[file.Path] = true
		remaining -= file.SizeBytes
	}
	if !seen["config.json"] || !seen["model.safetensors"] || !seen["tokenizer_config.json"] || !seen["tokenizer.json"] {
		return "base model HF files"
	}
	return ""
}

func invalidFinetuningSnapshot(snapshot Snapshot) string {
	fine := snapshot.Finetuning
	if fine == nil || fine.ParameterContractVersion != FinetuningParameterContractVersion {
		return "finetuning contract"
	}
	if snapshot.Input.Format != "JSON" || snapshot.Input.SchemaVersion != FinetuningInputSchemaVersion || snapshot.Input.RowCount == 0 || snapshot.Input.FeatureCount != 0 {
		return "fixed JSON contract"
	}
	if fine.TargetModelID != nil && fine.NewModelName != nil || fine.TargetModelID != nil && !validUUID(*fine.TargetModelID) || fine.NewModelName != nil && !validModelName(*fine.NewModelName) {
		return "model target"
	}
	if field := invalidComponentBudgets(fine.Budgets); field != "" {
		return field
	}
	if field := invalidBaseModel(fine.BaseModel, fine.Budgets); field != "" {
		return field
	}
	if !snapshotSHA256Pattern.MatchString(fine.RecipeSHA256) || !snapshotSHA256Pattern.MatchString(fine.MappingSHA256) {
		return "program provenance"
	}
	configuration := FinetuningConfiguration{ParameterContractVersion: fine.ParameterContractVersion, Parameters: snapshot.Program.ResolvedParameters, ManagedSettings: fine.ManagedSettings}
	digest, err := configuration.Digest()
	if err != nil || digest != fine.ResolvedConfigurationSHA256 {
		return "resolved configuration"
	}
	if !slices.Equal(snapshot.Program.Command, []string{"/opt/venv/bin/python", "-I", "/opt/finetuning/finetune_entry.py"}) {
		return "finetuning command"
	}
	baseManifestHash, err := fine.BaseModel.BaseManifestSHA256()
	if err != nil {
		return "base model files"
	}
	arguments := func(mode string) []string {
		return []string{mode, "--configuration", "/workspace/inputs/finetune_configuration.json", "--workspace", "/workspace", "--expected-input-sha256", snapshot.Input.Object.SHA256, "--expected-input-bytes", strconv.FormatInt(snapshot.Input.Object.SizeBytes, 10), "--expected-base-manifest-sha256", baseManifestHash}
	}
	if !slices.Equal(snapshot.Program.ResolvedArgs, arguments("train")) {
		return "resolved finetuning args"
	}
	for _, component := range []struct {
		Program ProgramRef
		Mode    string
	}{{fine.ExportProgram, "export"}, {fine.VerifyProgram, "verify"}} {
		program := component.Program
		if !validUUID(program.ImageVersionID) || !validSnapshotImage(program.ImageDigest) || !slices.Equal(program.Command, snapshot.Program.Command) || len(program.ResolvedParameters) != 0 || !slices.Equal(program.ResolvedArgs, arguments(component.Mode)) {
			return "component program"
		}
	}
	return ""
}

func canonicalFinetuningSnapshot(fine FinetuningSnapshot) FinetuningSnapshot {
	fine.BaseModel.ModelID = strings.ToLower(fine.BaseModel.ModelID)
	fine.BaseModel.VersionID = strings.ToLower(fine.BaseModel.VersionID)
	fine.BaseModel.Files = append([]BaseModelFile{}, fine.BaseModel.Files...)
	sort.Slice(fine.BaseModel.Files, func(i, j int) bool { return fine.BaseModel.Files[i].Path < fine.BaseModel.Files[j].Path })
	if fine.TargetModelID != nil {
		target := strings.ToLower(*fine.TargetModelID)
		fine.TargetModelID = &target
	}
	fine.ExportProgram.ImageVersionID = strings.ToLower(fine.ExportProgram.ImageVersionID)
	fine.VerifyProgram.ImageVersionID = strings.ToLower(fine.VerifyProgram.ImageVersionID)
	fine.ExportProgram.ResolvedParameters = canonicalParameters(fine.ExportProgram.ResolvedParameters)
	fine.VerifyProgram.ResolvedParameters = canonicalParameters(fine.VerifyProgram.ResolvedParameters)
	return fine
}

func invalidFinetuningArguments(arguments []MaterialArgument, mode string) string {
	want := []MaterialArgument{{Literal: mode}, {Literal: "--configuration"}, {Literal: "/workspace/inputs/finetune_configuration.json"}, {Literal: "--workspace"}, {Literal: "/workspace"}, {Literal: "--expected-input-sha256"}, {Source: "INPUT_SHA256"}, {Literal: "--expected-input-bytes"}, {Source: "INPUT_BYTES"}, {Literal: "--expected-base-manifest-sha256"}, {Source: "BASE_MANIFEST_SHA256"}}
	if !slices.Equal(arguments, want) {
		return "args template"
	}
	return ""
}

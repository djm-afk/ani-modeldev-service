// Command modeldev-execution-materials renders the finite installed software
// materials from explicit image/Pipeline facts. Rendering is not enablement or
// an environment acceptance result. Run generation only on Fedora.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"unicode/utf8"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

type image struct {
	ImageID          string            `json:"image_id"`
	ImageVersion     int64             `json:"image_version,string"`
	ImageScope       cpup01.ImageScope `json:"image_scope"`
	Digest           string            `json:"digest"`
	EntrySHA256      string            `json:"entry_sha256"`
	PythonExecutable string            `json:"python_executable"`
}
type pipelineReceipt struct {
	Phase             string `json:"phase"`
	PipelineID        string `json:"pipeline_id"`
	PipelineVersionID string `json:"pipeline_version_id"`
	PipelineIRSHA256  string `json:"pipeline_ir_sha256"`
}
type finetuningFacts struct {
	ExportImage               image                             `json:"export_image"`
	VerifyImage               image                             `json:"verify_image"`
	MappingSHA256             string                            `json:"mapping_sha256"`
	RuntimeCapabilities       cpup01.RuntimeCapabilities        `json:"runtime_capabilities"`
	ModelTemplateAssociations []cpup01.ModelTemplateAssociation `json:"model_template_associations"`
}
type facts struct {
	SchemaVersion          string                               `json:"schema_version"`
	ImplementationID       string                               `json:"implementation_id"`
	DeviceMode             string                               `json:"device_mode"`
	TrainingImage          image                                `json:"training_image"`
	Pipeline               pipelineReceipt                      `json:"pipeline"`
	WorkspaceStorageClass  string                               `json:"workspace_storage_class"`
	WorkspaceCapacityBytes int64                                `json:"workspace_capacity_bytes"`
	Finetuning             *finetuningFacts                     `json:"finetuning,omitempty"`
	ComponentResources     map[string]cpup01.ComponentResources `json:"component_resources"`
}

func hash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func fileHash(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return hash(raw), nil
}
func readFacts(path string) (facts, error) {
	file, err := os.Open(path)
	if err != nil {
		return facts{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > cpup01.MaxMaterialBytes {
		return facts{}, errors.New("invalid material input size or type")
	}
	raw, err := io.ReadAll(io.LimitReader(file, cpup01.MaxMaterialBytes+1))
	if err != nil {
		return facts{}, err
	}
	if len(raw) > cpup01.MaxMaterialBytes || !utf8.Valid(raw) {
		return facts{}, errors.New("invalid material input size or encoding")
	}
	scan := json.NewDecoder(bytes.NewReader(raw))
	if strictValue(scan, 0) != nil {
		return facts{}, errors.New("ambiguous material input")
	}
	var f facts
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&f); err != nil {
		return facts{}, errors.New("invalid material input")
	}
	if d.Decode(new(any)) != io.EOF {
		return facts{}, errors.New("trailing material input")
	}
	return f, nil
}
func strictValue(d *json.Decoder, depth int) error {
	if depth > cpup01.MaxJSONDepth {
		return errors.New("depth")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("null")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate")
			}
			seen[name] = true
			if err := strictValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := strictValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("delimiter")
	}
	_, err = d.Token()
	return err
}

func material(f facts, entrySHA, mappingSHA string) (cpup01.ImplementationMaterial, error) {
	bad := func() (cpup01.ImplementationMaterial, error) {
		return cpup01.ImplementationMaterial{}, errors.New("explicit confirmed PipelineVersion, immutable image/source evidence and workspace required")
	}
	definition, ok := cpup01.Implementations()[f.ImplementationID]
	if !ok || f.SchemaVersion != "ani.modeldev.material-input.v3" || (f.DeviceMode != "cpu" && f.DeviceMode != "gpu") || f.Pipeline.Phase != "CONFIRMED" || f.TrainingImage.EntrySHA256 != entrySHA || !filepath.IsAbs(f.TrainingImage.PythonExecutable) || filepath.Clean(f.TrainingImage.PythonExecutable) != f.TrainingImage.PythonExecutable || f.WorkspaceCapacityBytes <= 0 {
		return bad()
	}
	m := cpup01.ImplementationMaterial{SchemaVersion: cpup01.MaterialSchemaVersion, Implementation: cpup01.ImplementationSnapshot{ImplementationID: f.ImplementationID, Version: definition.Version, ParameterContractVersion: definition.ParameterContractVersion, PipelineID: f.Pipeline.PipelineID, PipelineVersionID: f.Pipeline.PipelineVersionID, PipelineIRSHA256: f.Pipeline.PipelineIRSHA256}, Workspace: cpup01.WorkspaceContract{Mode: "EXECUTION_PVC", StorageClass: f.WorkspaceStorageClass, CapacityBytes: f.WorkspaceCapacityBytes, InputSubpath: "inputs", TrainingSubpath: "training", ReportsSubpath: "reports", PublicationSubpath: "publication", WorkSubpath: "work"}}
	m.ComponentResources = f.ComponentResources
	if f.ImplementationID == "builtin.mlp" {
		if f.Finetuning != nil {
			return bad()
		}
		// Training names only the catalogue default. Resource resolves the
		// version and digest at admission, so the material cannot pin a stale
		// image and the same path serves a user selection.
		m.Training = &cpup01.TrainingMaterial{DefaultImage: cpup01.DefaultTrainingImage{ImageID: f.TrainingImage.ImageID, Scope: f.TrainingImage.ImageScope}}
		m.Training.Command = []string{f.TrainingImage.PythonExecutable, "-I", "/opt/ani-modeldev/training/train_mlp.py"}
		device := "cpu"
		if f.DeviceMode == "gpu" {
			device = "cuda"
		}
		m.Training.ArgsTemplate = []cpup01.MaterialArgument{{Literal: "--dataset-manifest"}, {Literal: "/inputs/dataset-manifest.json"}, {Literal: "--expected-dataset-manifest-sha256"}, {Source: "DATASET_MANIFEST_SHA256"}, {Literal: "--work-root"}, {Literal: "/workspace/work/train"}, {Literal: "--output"}, {Source: "OUTPUT_PATH"}, {Literal: "--learning-rate"}, {Source: "LEARNING_RATE"}, {Literal: "--epochs"}, {Source: "EPOCHS"}, {Literal: "--batch-size"}, {Source: "BATCH_SIZE"}, {Literal: "--seed"}, {Source: "SEED"}, {Literal: "--device"}, {Literal: device}}
		m.OutputContract = cpup01.OutputContract{SchemaVersion: cpup01.OutputContractSchemaVersion, OutputKind: "CHECKPOINT", DeliveryMode: "SAVE_ARTIFACTS", RequiredFiles: []cpup01.RequiredOutput{{Role: "METRICS", RelativePath: "metrics.jsonl", InTar: true}, {Role: "CHECKPOINT", RelativePath: "model.pt", InTar: true}, {Role: "MODEL_CONFIG", RelativePath: "model_config.json", InTar: true}, {Role: "SUMMARY", RelativePath: "summary.json", InTar: true}}, CreateTarBundle: false}
	} else {
		if f.Finetuning == nil || f.Finetuning.RuntimeCapabilities.EntrypointSHA256 != entrySHA || f.Finetuning.RuntimeCapabilities.ImageDigest != f.TrainingImage.Digest || f.Finetuning.ExportImage.Digest != f.TrainingImage.Digest || f.Finetuning.VerifyImage.Digest != f.TrainingImage.Digest || f.Finetuning.MappingSHA256 != mappingSHA || f.Finetuning.ExportImage.EntrySHA256 != entrySHA || f.Finetuning.VerifyImage.EntrySHA256 != entrySHA {
			return bad()
		}
		m.Program = &cpup01.MaterialProgram{ImageID: f.TrainingImage.ImageID, ImageVersion: f.TrainingImage.ImageVersion, ImageScope: f.TrainingImage.ImageScope, ImageDigest: f.TrainingImage.Digest}
		m.Program.Command = []string{f.TrainingImage.PythonExecutable, "-I", "/opt/ani-modeldev/training/finetuning/finetune_entry.py"}
		m.Program.ArgsTemplate = arguments("train")
		settings, err := managedSettings(f.DeviceMode)
		if err != nil {
			return bad()
		}
		encoded, err := canonical(settings)
		if err != nil {
			return bad()
		}
		component := func(mode string, i image) cpup01.ComponentProgram {
			return cpup01.ComponentProgram{ImageID: i.ImageID, ImageVersion: i.ImageVersion, ImageScope: i.ImageScope, ImageDigest: i.Digest, Command: append([]string{}, m.Program.Command...), ArgsTemplate: arguments(mode), ImplementationSHA256: entrySHA}
		}
		m.Finetuning = &cpup01.FinetuningMaterial{ManagedSettings: settings, ExportProgram: component("export", f.Finetuning.ExportImage), VerifyProgram: component("verify", f.Finetuning.VerifyImage), RecipeSHA256: hash(encoded), MappingSHA256: mappingSHA, RuntimeCapabilities: f.Finetuning.RuntimeCapabilities, ModelTemplateAssociations: f.Finetuning.ModelTemplateAssociations}
		m.OutputContract = finetuningOutput()
	}
	if _, err := m.Canonical(); err != nil {
		return bad()
	}
	return m, nil
}
func arguments(mode string) []cpup01.MaterialArgument {
	return []cpup01.MaterialArgument{{Literal: mode}, {Literal: "--configuration"}, {Literal: "/workspace/inputs/finetune_configuration.json"}, {Literal: "--workspace"}, {Literal: "/workspace"}, {Literal: "--dataset-manifest"}, {Literal: "/workspace/inputs/dataset-manifest.json"}, {Literal: "--expected-dataset-manifest-sha256"}, {Source: "DATASET_MANIFEST_SHA256"}, {Literal: "--expected-base-manifest-sha256"}, {Source: "BASE_MANIFEST_SHA256"}, {Literal: "--expected-template-plan-sha256"}, {Source: "TEMPLATE_PLAN_SHA256"}}
}
func canonical(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var object any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&object); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	e := json.NewEncoder(&out)
	e.SetEscapeHTML(false)
	if err := e.Encode(object); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}
func managedSettings(device string) (cpup01.FinetuningManagedSettings, error) {
	i := cpup01.Intent{Kind: "FINETUNING", ImplementationID: "builtin.sft_lora", Finetuning: &cpup01.FinetuningParameters{}}
	parameters, err := i.ResolveParameters()
	if err != nil {
		return cpup01.FinetuningManagedSettings{}, err
	}
	values := map[string]string{}
	for _, p := range parameters {
		values[p.Name] = p.Value
	}
	integer := func(name string) int64 { v, _ := strconv.ParseInt(values[name], 10, 64); return v }
	mode := "cpu"
	if device == "gpu" {
		mode = "cuda"
	}
	return cpup01.FinetuningManagedSettings{Stage: "sft", FinetuningType: "lora", LoraTarget: "all", LoraRank: integer("lora_rank"), LoraAlpha: integer("lora_alpha"), CutoffLen: integer("cutoff_len"), PerDeviceTrainBatchSize: integer("per_device_train_batch_size"), GradientAccumulationSteps: integer("gradient_accumulation_steps"), Seed: integer("seed"), DataSeed: integer("seed"), Device: mode, FlashAttn: "disabled", ReportTo: "none", SaveStrategy: "no", TrainDtype: "float32", ExportDtype: "float32", VerifyDtype: "float32", LRSchedulerType: "linear", WarmupRatio: "0", WeightDecay: "0", LoraDropout: values["lora_dropout"], NumTrainEpochs: 3, Optim: "adamw_torch", LoggingSteps: 1}, nil
}
func finetuningOutput() cpup01.OutputContract {
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
	return cpup01.OutputContract{SchemaVersion: cpup01.OutputContractSchemaVersion, OutputKind: "MERGED_HF_MODEL", DeliveryMode: "SAVE_ARTIFACTS", RequiredFiles: files, CreateTarBundle: true}
}

func run() error {
	factsPath := flag.String("facts", "", "explicit material input JSON")
	output := flag.String("output", "", "new canonical material path")
	entry := flag.String("entry", "", "exact training image entry source")
	mapping := flag.String("mapping", "training/finetuning/configuration.py", "exact LoRA codec source")
	flag.Parse()
	if flag.NArg() != 0 || *factsPath == "" || *output == "" || *entry == "" {
		return errors.New("--facts --entry --output are required")
	}
	if _, err := os.Stat("/etc/fedora-release"); err != nil {
		return errors.New("run generation on Fedora")
	}
	f, err := readFacts(*factsPath)
	if err != nil {
		return err
	}
	entrySHA, err := fileHash(*entry)
	if err != nil {
		return err
	}
	mappingSHA := ""
	if f.ImplementationID == "builtin.sft_lora" {
		mappingSHA, err = fileHash(*mapping)
		if err != nil {
			return err
		}
	}
	m, err := material(f, entrySHA, mappingSHA)
	if err != nil {
		return err
	}
	raw, err := m.Canonical()
	if err != nil {
		return err
	}
	if _, _, err := cpup01.ParseMaterial(raw); err != nil {
		return err
	}
	if filepath.Base(*output) != f.ImplementationID+"."+f.DeviceMode+".json" {
		return errors.New("output filename must match implementation and device")
	}
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(raw); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	fmt.Printf("implementation=%s device=%s material_sha256=%s\n", f.ImplementationID, f.DeviceMode, hash(raw))
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

package workspace

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/finetuningmodel"
	"golang.org/x/sys/unix"
)

func openPipelineReadDirectory(root int, relative string) (int, error) {
	if !biz.ValidStorageKey(relative) {
		return -1, biz.ErrInvalidWorkspaceOutput
	}
	current, err := unix.Dup(root)
	if err != nil {
		return -1, biz.ErrInvalidWorkspaceOutput
	}
	unix.CloseOnExec(current)
	for _, component := range strings.Split(relative, "/") {
		next, err := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(current)
		if err != nil {
			return -1, biz.ErrInvalidWorkspaceOutput
		}
		current = next
	}
	return current, nil
}

func openPipelineReadFile(root int, relative string) (int, error) {
	if !biz.ValidStorageKey(relative) {
		return -1, biz.ErrInvalidWorkspaceOutput
	}
	parent := root
	if directory := path.Dir(relative); directory != "." {
		fd, err := openPipelineReadDirectory(root, directory)
		if err != nil {
			return -1, err
		}
		defer unix.Close(fd)
		parent = fd
	}
	fd, err := unix.Openat(parent, path.Base(relative), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, biz.ErrInvalidWorkspaceOutput
	}
	return fd, nil
}

func collectFinetuning(ctx context.Context, root int, execution biz.Execution) (biz.CollectedOutput, error) {
	contract := execution.Snapshot.OutputContract
	dynamic, err := finetuningOutputInventory(ctx, root, contract)
	if err != nil {
		return biz.CollectedOutput{}, err
	}
	files := make([]biz.CollectedFile, 0, len(dynamic))
	remaining := execution.Snapshot.Limits.MaxContentBytes
	for _, required := range dynamic {
		limit := remaining
		if required.Role != "METRICS" && required.Role != "HF_MODEL" && required.Role != "LORA_ADAPTER" && limit > cpup01.MaxManifestBytes {
			limit = cpup01.MaxManifestBytes
		}
		file, err := collectFile(ctx, root, required.RelativePath, required.Role, limit)
		if err != nil {
			return biz.CollectedOutput{}, err
		}
		file.InTar = required.InTar
		files = append(files, file)
		remaining -= file.SizeBytes
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelativePath < files[j].RelativePath })
	if err := verifyProducedModelLayout(ctx, root, execution, files); err != nil {
		return biz.CollectedOutput{}, err
	}
	if err := verifyFinetuningReports(ctx, root, execution, files); err != nil {
		return biz.CollectedOutput{}, err
	}
	manifest, hash, err := cpup01.OutputManifestBytes(cpup01.AdmissionEnvelope(execution.Admission), files)
	if err != nil {
		return biz.CollectedOutput{}, biz.ErrInvalidWorkspaceOutput
	}
	return biz.CollectedOutput{ExecutionID: execution.ExecutionID, ExecutionSpecHash: execution.SpecHash, StorageState: "WORKSPACE_ONLY", Files: files, Manifest: manifest, ManifestSHA256: hash}, nil
}

// Only the two current producer manifests extend the fixed platform reports.
// Neither directory discovery nor a material can invent extra published paths.
func finetuningOutputInventory(ctx context.Context, root int, contract cpup01.OutputContract) ([]cpup01.RequiredOutput, error) {
	required := append([]cpup01.RequiredOutput{}, contract.RequiredFiles...)
	wanted := map[string]bool{}
	for _, file := range required {
		wanted[file.RelativePath] = true
	}
	for _, section := range []struct {
		manifest, prefix, role string
		inTar                  bool
	}{
		{"reports/train/adapter_manifest.json", "training/adapter/", "LORA_ADAPTER", false},
		{"reports/export/merged_manifest.json", "export/merged/", "HF_MODEL", true},
	} {
		raw, err := readCurrentReport(ctx, root, section.manifest)
		if err != nil {
			return nil, err
		}
		var report struct {
			Files []finetuningMember `json:"files"`
		}
		if decodeFinetuningReport(raw, &report) != nil || len(report.Files) == 0 || len(report.Files) > cpup01.MaxModelFiles {
			return nil, biz.ErrInvalidWorkspaceOutput
		}
		for _, file := range report.Files {
			name := section.prefix + file.Path
			if !biz.ValidStorageKey(file.Path) || len(file.Path) > cpup01.MaxRelativePathBytes || !biz.ValidStorageKey(name) || len(name) > cpup01.MaxRelativePathBytes || wanted[name] || file.Bytes < 0 || len(file.SHA256) != 64 {
				return nil, biz.ErrInvalidWorkspaceOutput
			}
			wanted[name] = true
			required = append(required, cpup01.RequiredOutput{Role: section.role, RelativePath: name, InTar: section.inTar})
		}
	}
	if len(required) > cpup01.MaxOutputFiles {
		return nil, biz.ErrInvalidWorkspaceOutput
	}
	actual := map[string]bool{}
	for _, directory := range []string{"training/adapter", "export/merged", "reports/train", "reports/export", "reports/verify"} {
		if err := walkFinetuningOutputs(ctx, root, directory, actual, wanted, cpup01.MaxOutputFiles); err != nil {
			return nil, err
		}
	}
	if len(actual) != len(wanted) {
		return nil, biz.ErrInvalidWorkspaceOutput
	}
	for name := range actual {
		if !wanted[name] {
			return nil, biz.ErrInvalidWorkspaceOutput
		}
	}
	return required, nil
}

func verifyFinetuningInventory(ctx context.Context, root int, contract cpup01.OutputContract) error {
	_, err := finetuningOutputInventory(ctx, root, contract)
	return err
}

func readCurrentReport(ctx context.Context, root int, name string) ([]byte, error) {
	fact, err := collectFile(ctx, root, name, "SOURCE_METADATA", cpup01.MaxManifestBytes)
	if err != nil {
		return nil, err
	}
	capture, err := capturePipelineFile(ctx, root, fact)
	if err != nil {
		return nil, err
	}
	defer capture.Close()
	raw, err := io.ReadAll(io.LimitReader(capture, cpup01.MaxManifestBytes))
	if err != nil || int64(len(raw)) != fact.SizeBytes {
		return nil, biz.ErrInvalidWorkspaceOutput
	}
	return raw, nil
}

func walkFinetuningOutputs(ctx context.Context, root int, relative string, files, wanted map[string]bool, maximum uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.Count(relative, "/") > 16 {
		return biz.ErrInvalidWorkspaceOutput
	}
	fd, err := openPipelineReadDirectory(root, relative)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(fd), relative)
	defer directory.Close()
	entries, err := directory.ReadDir(int(maximum) + 1)
	if err != nil && err != io.EOF || len(entries) > int(maximum) {
		return biz.ErrInvalidWorkspaceOutput
	}
	for _, entry := range entries {
		member := relative + "/" + entry.Name()
		var info unix.Stat_t
		if !biz.ValidStorageKey(member) || unix.Fstatat(fd, entry.Name(), &info, unix.AT_SYMLINK_NOFOLLOW) != nil {
			return biz.ErrInvalidWorkspaceOutput
		}
		switch info.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			allowed := false
			for name := range wanted {
				if strings.HasPrefix(name, member+"/") {
					allowed = true
					break
				}
			}
			if !allowed {
				return biz.ErrInvalidWorkspaceOutput
			}
			if err := walkFinetuningOutputs(ctx, root, member, files, wanted, maximum); err != nil {
				return err
			}
		case unix.S_IFREG:
			if info.Nlink != 1 || files[member] || uint64(len(files)) >= uint64(maximum) {
				return biz.ErrInvalidWorkspaceOutput
			}
			files[member] = true
		default:
			return biz.ErrInvalidWorkspaceOutput
		}
	}
	return nil
}

type finetuningMember struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes,string"`
	SHA256 string `json:"sha256"`
}

func reportMembers(files []cpup01.OutputFile, prefix string) []finetuningMember {
	result := []finetuningMember{}
	for _, file := range files {
		if strings.HasPrefix(file.RelativePath, prefix) {
			result = append(result, finetuningMember{Path: strings.TrimPrefix(file.RelativePath, prefix), Bytes: file.SizeBytes, SHA256: file.SHA256})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}

func readFinetuningReport(ctx context.Context, root int, relative string, files []cpup01.OutputFile) ([]byte, error) {
	for _, file := range files {
		if file.RelativePath == relative {
			if file.SizeBytes > cpup01.MaxManifestBytes {
				return nil, biz.ErrInvalidWorkspaceOutput
			}
			captured, err := capturePipelineFile(ctx, root, file)
			if err != nil {
				return nil, err
			}
			defer captured.Close()
			content, err := io.ReadAll(io.LimitReader(captured, cpup01.MaxManifestBytes))
			if err != nil || int64(len(content)) != file.SizeBytes {
				return nil, biz.ErrInvalidWorkspaceOutput
			}
			return content, nil
		}
	}
	return nil, biz.ErrInvalidWorkspaceOutput
}

func sameFinetuningMembers(left, right []finetuningMember) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func decodeFinetuningReport(raw []byte, value any) error {
	scanner := json.NewDecoder(bytes.NewReader(raw))
	scanner.UseNumber()
	if checkFinetuningReportJSON(scanner, 0) != nil {
		return biz.ErrInvalidWorkspaceOutput
	}
	if _, err := scanner.Token(); err != io.EOF {
		return biz.ErrInvalidWorkspaceOutput
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(value); err != nil {
		return biz.ErrInvalidWorkspaceOutput
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return biz.ErrInvalidWorkspaceOutput
	}
	return nil
}

func checkFinetuningReportJSON(decoder *json.Decoder, depth int) error {
	if depth > cpup01.MaxJSONDepth {
		return biz.ErrInvalidWorkspaceOutput
	}
	token, err := decoder.Token()
	if err != nil {
		return biz.ErrInvalidWorkspaceOutput
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				token, err := decoder.Token()
				key, ok := token.(string)
				if err != nil || !ok || seen[key] {
					return biz.ErrInvalidWorkspaceOutput
				}
				seen[key] = true
				if err := checkFinetuningReportJSON(decoder, depth+1); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return biz.ErrInvalidWorkspaceOutput
			}
		case '[':
			for decoder.More() {
				if err := checkFinetuningReportJSON(decoder, depth+1); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return biz.ErrInvalidWorkspaceOutput
			}
		default:
			return biz.ErrInvalidWorkspaceOutput
		}
	}
	return nil
}

func verifyFinetuningReports(ctx context.Context, root int, execution biz.Execution, files []cpup01.OutputFile) error {
	invalid := biz.ErrInvalidWorkspaceOutput
	fine := execution.Snapshot.Finetuning
	if fine == nil {
		return invalid
	}
	dataset, err := cpup01.BuildDatasetManifest(execution.Snapshot.Input)
	if err != nil {
		return invalid
	}
	datasetSHA, err := dataset.Digest()
	if err != nil {
		return invalid
	}
	baseHash, err := fine.BaseModel.BaseManifestSHA256()
	if err != nil {
		return invalid
	}
	templateRaw, err := readFinetuningReport(ctx, root, "reports/train/effective-template.json", files)
	if err != nil {
		return invalid
	}
	effectiveTemplate, err := cpup01.ParseEffectiveTemplate(templateRaw)
	if err != nil || effectiveTemplate.ValidateFor(fine.TemplatePlan, fine.RuntimeCapabilities) != nil {
		return invalid
	}
	templateSHA := pipelineContentHash(templateRaw)
	matchedBody := false
	for _, file := range files {
		if file.RelativePath == cpup01.EffectiveTemplateRelativePath {
			matchedBody = file.SizeBytes == effectiveTemplate.TemplateFile.SizeBytes && file.SHA256 == effectiveTemplate.TemplateFile.SHA256
		}
	}
	if !matchedBody {
		return invalid
	}
	resolved := cpup01.FinetuningConfiguration{ParameterContractVersion: fine.ParameterContractVersion, Parameters: execution.Snapshot.Program.ResolvedParameters, ManagedSettings: fine.ManagedSettings}
	resolvedBytes, err := resolved.Canonical()
	if err != nil {
		return invalid
	}
	var summary struct {
		EffectiveTemplateSHA256      string             `json:"effective_template_sha256"`
		Schema                       string             `json:"schema"`
		Status                       string             `json:"status"`
		Samples                      int64              `json:"samples"`
		SourceSamples                int64              `json:"source_samples"`
		DroppedSamples               *int64             `json:"dropped_samples"`
		EffectiveTokenCount          int64              `json:"effective_token_count"`
		MaxEffectiveTokens           int64              `json:"max_effective_tokens"`
		SamplesAtCutoff              *int64             `json:"samples_at_cutoff"`
		TruncatedSamples             *int64             `json:"truncated_samples"`
		TruncatedTokens              *int64             `json:"truncated_tokens"`
		Device                       string             `json:"device"`
		TrainDtypes                  []string           `json:"train_dtypes"`
		AdapterDtype                 string             `json:"adapter_dtype"`
		OptimizerSteps               int64              `json:"optimizer_steps"`
		Steps                        int64              `json:"steps"`
		MetricSamples                int64              `json:"metric_samples"`
		InputSHA256                  string             `json:"input_sha256"`
		InputBytes                   int64              `json:"input_bytes,string"`
		DatasetManifestSHA256        string             `json:"dataset_manifest_sha256"`
		PreparedInputSHA256          string             `json:"prepared_input_sha256"`
		PreparedInputBytes           int64              `json:"prepared_input_bytes,string"`
		BaseManifestSHA256           string             `json:"base_manifest_sha256"`
		ParameterContractVersion     string             `json:"parameter_contract_version"`
		ResolvedConfigurationSHA256  string             `json:"resolved_configuration_sha256"`
		EffectiveConfiguration       json.RawMessage    `json:"effective_configuration"`
		EffectiveConfigurationSHA256 string             `json:"effective_configuration_sha256"`
		LearningRate                 string             `json:"learning_rate"`
		MaxSteps                     int64              `json:"max_steps"`
		RecipeSHA256                 string             `json:"recipe_sha256"`
		AdapterFiles                 []finetuningMember `json:"adapter_files"`
	}
	raw, err := readFinetuningReport(ctx, root, "reports/train/summary.json", files)
	if err != nil || decodeFinetuningReport(raw, &summary) != nil {
		return invalid
	}
	if summary.EffectiveTemplateSHA256 != templateSHA || summary.Schema != "ani.finetune.summary.v1" || summary.Status != "SUCCEEDED" || summary.SourceSamples != int64(execution.Snapshot.Input.RowCount) || summary.Samples < 1 || summary.Samples > summary.SourceSamples || summary.DroppedSamples == nil || *summary.DroppedSamples != summary.SourceSamples-summary.Samples || summary.OptimizerSteps <= 0 || summary.Steps != summary.OptimizerSteps || summary.InputSHA256 != execution.Snapshot.Input.Object.SHA256 || summary.InputBytes != execution.Snapshot.Input.Object.SizeBytes || summary.BaseManifestSHA256 != baseHash || summary.ParameterContractVersion != fine.ParameterContractVersion || summary.ResolvedConfigurationSHA256 != fine.ResolvedConfigurationSHA256 || summary.EffectiveConfigurationSHA256 != fine.ResolvedConfigurationSHA256 {
		return invalid
	}
	if summary.DatasetManifestSHA256 != datasetSHA || summary.PreparedInputSHA256 != dataset.PreparedSHA256 || summary.PreparedInputBytes != dataset.PreparedSizeBytes {
		return invalid
	}
	if summary.Device != fine.ManagedSettings.Device || len(summary.TrainDtypes) != 1 || summary.TrainDtypes[0] != fine.ManagedSettings.TrainDtype || summary.AdapterDtype != "float32" ||
		summary.EffectiveTokenCount < summary.Samples || summary.MaxEffectiveTokens < 1 || summary.MaxEffectiveTokens > fine.ManagedSettings.CutoffLen || summary.MaxEffectiveTokens > summary.EffectiveTokenCount || (summary.EffectiveTokenCount-1)/summary.MaxEffectiveTokens+1 > summary.Samples ||
		summary.SamplesAtCutoff == nil || *summary.SamplesAtCutoff < 0 || *summary.SamplesAtCutoff > summary.Samples ||
		summary.TruncatedSamples == nil || *summary.TruncatedSamples < 0 || *summary.TruncatedSamples > summary.Samples ||
		summary.TruncatedTokens == nil || *summary.TruncatedTokens < 0 || (*summary.TruncatedSamples == 0) != (*summary.TruncatedTokens == 0) {
		return invalid
	}
	var effective cpup01.FinetuningConfiguration
	decoder := json.NewDecoder(bytes.NewReader(summary.EffectiveConfiguration))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&effective) != nil {
		return invalid
	}
	effectiveBytes, err := effective.Canonical()
	if err != nil || !bytes.Equal(effectiveBytes, resolvedBytes) || pipelineContentHash(effectiveBytes) != summary.EffectiveConfigurationSHA256 {
		return invalid
	}
	for _, parameter := range resolved.Parameters {
		switch parameter.Name {
		case "learning_rate":
			if parameter.Value != summary.LearningRate {
				return invalid
			}
		case "max_steps":
			value, err := strconv.ParseInt(parameter.Value, 10, 64)
			if err != nil || summary.MaxSteps != value || summary.OptimizerSteps != value {
				return invalid
			}
		}
	}
	var projection map[string]json.RawMessage
	if json.Unmarshal(resolvedBytes, &projection) != nil || pipelineContentHash(projection["managed_settings"]) != summary.RecipeSHA256 {
		return invalid
	}
	adapter := reportMembers(files, "training/adapter/")
	if !sameFinetuningMembers(summary.AdapterFiles, adapter) {
		return invalid
	}
	var association struct {
		EffectiveTemplateSHA256     string             `json:"effective_template_sha256"`
		Schema                      string             `json:"schema"`
		InputSHA256                 string             `json:"input_sha256"`
		DatasetManifestSHA256       string             `json:"dataset_manifest_sha256"`
		PreparedInputSHA256         string             `json:"prepared_input_sha256"`
		PreparedInputBytes          int64              `json:"prepared_input_bytes,string"`
		BaseManifestSHA256          string             `json:"base_manifest_sha256"`
		ResolvedConfigurationSHA256 string             `json:"resolved_configuration_sha256"`
		Files                       []finetuningMember `json:"files"`
	}
	raw, err = readFinetuningReport(ctx, root, "reports/train/adapter_manifest.json", files)
	if err != nil || decodeFinetuningReport(raw, &association) != nil || association.EffectiveTemplateSHA256 != templateSHA || association.Schema != "ani.finetune.adapter-manifest.v3" || association.InputSHA256 != summary.InputSHA256 || association.BaseManifestSHA256 != baseHash || association.ResolvedConfigurationSHA256 != fine.ResolvedConfigurationSHA256 || !sameFinetuningMembers(association.Files, adapter) {
		return invalid
	}
	if association.DatasetManifestSHA256 != datasetSHA || association.PreparedInputSHA256 != dataset.PreparedSHA256 || association.PreparedInputBytes != dataset.PreparedSizeBytes {
		return invalid
	}
	adapterManifestHash := pipelineContentHash(raw)
	var merged struct {
		EffectiveTemplateSHA256     string             `json:"effective_template_sha256"`
		Schema                      string             `json:"schema"`
		ResolvedConfigurationSHA256 string             `json:"resolved_configuration_sha256"`
		Dtype                       string             `json:"dtype"`
		InputSHA256                 string             `json:"input_sha256"`
		DatasetManifestSHA256       string             `json:"dataset_manifest_sha256"`
		PreparedInputSHA256         string             `json:"prepared_input_sha256"`
		PreparedInputBytes          int64              `json:"prepared_input_bytes,string"`
		BaseManifestSHA256          string             `json:"base_manifest_sha256"`
		AdapterManifestSHA256       string             `json:"adapter_manifest_sha256"`
		AdapterFiles                []finetuningMember `json:"adapter_files"`
		Files                       []finetuningMember `json:"files"`
	}
	raw, err = readFinetuningReport(ctx, root, "reports/export/merged_manifest.json", files)
	if err != nil || decodeFinetuningReport(raw, &merged) != nil || merged.EffectiveTemplateSHA256 != templateSHA || merged.Schema != "ani.finetune.merged-manifest.v3" || merged.ResolvedConfigurationSHA256 != fine.ResolvedConfigurationSHA256 || merged.Dtype != fine.ManagedSettings.ExportDtype || merged.InputSHA256 != summary.InputSHA256 || merged.BaseManifestSHA256 != baseHash || merged.AdapterManifestSHA256 != adapterManifestHash || !sameFinetuningMembers(merged.AdapterFiles, adapter) || !sameFinetuningMembers(merged.Files, reportMembers(files, "export/merged/")) {
		return invalid
	}
	if merged.DatasetManifestSHA256 != datasetSHA || merged.PreparedInputSHA256 != dataset.PreparedSHA256 || merged.PreparedInputBytes != dataset.PreparedSizeBytes {
		return invalid
	}
	mergedHash := pipelineContentHash(raw)
	var exported struct {
		Schema string `json:"schema"`
		Status string `json:"status"`
		Dtype  string `json:"dtype"`
	}
	raw, err = readFinetuningReport(ctx, root, "reports/export/export.json", files)
	if err != nil || decodeFinetuningReport(raw, &exported) != nil || exported.Schema != "ani.finetune.export.v1" || exported.Status != "SUCCEEDED" || exported.Dtype != fine.ManagedSettings.ExportDtype {
		return invalid
	}
	var verified struct {
		EffectiveTemplateSHA256     string  `json:"effective_template_sha256"`
		Schema                      string  `json:"schema"`
		Status                      string  `json:"status"`
		Dtype                       string  `json:"dtype"`
		Device                      string  `json:"device"`
		ResolvedConfigurationSHA256 string  `json:"resolved_configuration_sha256"`
		MergedManifestSHA256        string  `json:"merged_manifest_sha256"`
		FiniteLogits                bool    `json:"finite_logits"`
		GeneratedTokenIDs           []int64 `json:"generated_token_ids"`
	}
	raw, err = readFinetuningReport(ctx, root, "reports/verify/verify.json", files)
	if err != nil || decodeFinetuningReport(raw, &verified) != nil || verified.EffectiveTemplateSHA256 != templateSHA || verified.Schema != "ani.finetune.verify.v1" || verified.Status != "PASSED" || verified.Dtype != fine.ManagedSettings.VerifyDtype || verified.Device != "cpu" || verified.ResolvedConfigurationSHA256 != fine.ResolvedConfigurationSHA256 || verified.MergedManifestSHA256 != mergedHash || !verified.FiniteLogits || len(verified.GeneratedTokenIDs) == 0 || len(verified.GeneratedTokenIDs) > 8 {
		return invalid
	}
	return verifyFinetuningMetrics(ctx, root, files, summary.OptimizerSteps, summary.MetricSamples)
}

func verifyFinetuningMetrics(ctx context.Context, root int, files []cpup01.OutputFile, steps, samples int64) error {
	return verifyMetricFile(ctx, root, files, "reports/train/metrics.jsonl", steps, samples)
}

func verifyMetricFile(ctx context.Context, root int, files []cpup01.OutputFile, name string, steps, samples int64) error {
	var expected cpup01.OutputFile
	for _, file := range files {
		if file.RelativePath == name {
			expected = file
		}
	}
	if expected.SizeBytes <= 0 || steps < 1 || samples != steps {
		return biz.ErrInvalidWorkspaceOutput
	}
	captured, err := capturePipelineFile(ctx, root, expected)
	if err != nil {
		return err
	}
	defer captured.Close()
	scanner := bufio.NewScanner(contextReader{ctx: ctx, reader: captured})
	scanner.Buffer(make([]byte, 4096), 16384)
	var count, lastStep int64
	for scanner.Scan() {
		var metric struct {
			Schema    string    `json:"schema"`
			Step      int64     `json:"step"`
			Name      string    `json:"name"`
			Value     float64   `json:"value"`
			Rank      int64     `json:"rank"`
			Timestamp time.Time `json:"timestamp"`
		}
		if decodeFinetuningReport(scanner.Bytes(), &metric) != nil || metric.Schema != "ani.metric.v1" || metric.Name != "train.loss" || metric.Step != lastStep+1 || metric.Step > steps || metric.Rank != 0 || metric.Timestamp.IsZero() || !finiteLoss(metric.Value) {
			return biz.ErrInvalidWorkspaceOutput
		}
		count++
		lastStep = metric.Step
	}
	if scanner.Err() != nil || lastStep != steps || count != samples || count == 0 {
		return pipelineIOError(ctx, biz.ErrInvalidWorkspaceOutput)
	}
	return nil
}

func verifyProducedModelLayout(ctx context.Context, root int, execution biz.Execution, files []cpup01.OutputFile) error {
	inventory := []cpup01.BaseModelFile{}
	for _, file := range files {
		if strings.HasPrefix(file.RelativePath, "export/merged/") {
			inventory = append(inventory, cpup01.BaseModelFile{Path: strings.TrimPrefix(file.RelativePath, "export/merged/"), SizeBytes: file.SizeBytes, SHA256: file.SHA256})
		}
	}
	var index []byte
	for _, file := range inventory {
		if file.Path == "model.safetensors.index.json" {
			var err error
			index, err = readFinetuningReport(ctx, root, "export/merged/"+file.Path, files)
			if err != nil {
				return err
			}
		}
	}
	if _, err := finetuningmodel.NormalizeWeightLayout(inventory, index); err != nil {
		return biz.ErrInvalidWorkspaceOutput
	}
	for _, name := range []string{"config.json", "tokenizer_config.json"} {
		raw, err := readFinetuningReport(ctx, root, "export/merged/"+name, files)
		if err != nil || len(raw) == 0 {
			return biz.ErrInvalidWorkspaceOutput
		}
		var config struct {
			ModelType string `json:"model_type"`
		}
		if decodeFinetuningReport(raw, &config) != nil || name == "config.json" && config.ModelType != execution.Snapshot.Finetuning.BaseModel.Facts.ModelType {
			return biz.ErrInvalidWorkspaceOutput
		}
	}
	return nil
}

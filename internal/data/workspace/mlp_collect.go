package workspace

import (
	"context"
	"encoding/json"
	"math"
	"strconv"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// Verify observations against this execution's frozen implementation inputs.
// The candidate result.json never supplies these facts or publication status.
func verifyMLPReports(ctx context.Context, root int, execution biz.Execution, files []cpup01.OutputFile) error {
	invalid := biz.ErrInvalidWorkspaceOutput
	var summary struct {
		Status       string   `json:"status"`
		Error        *string  `json:"error"`
		Samples      int64    `json:"samples"`
		Epochs       int64    `json:"epochs"`
		BatchSize    int64    `json:"batch_size"`
		LearningRate string   `json:"learning_rate"`
		Seed         *uint32  `json:"seed"`
		Steps        int64    `json:"steps"`
		WorldSize    int64    `json:"world_size"`
		Device       string   `json:"device"`
		TorchVersion string   `json:"torch_version"`
		InputSHA256  string   `json:"input_sha256"`
		InputBytes   int64    `json:"input_bytes,string"`
		InitialLoss  *float64 `json:"initial_loss"`
		FinalLoss    *float64 `json:"final_loss"`
	}
	raw, err := readFinetuningReport(ctx, root, "summary.json", files)
	if err != nil || decodeFinetuningReport(raw, &summary) != nil {
		return invalid
	}
	expected := map[string]string{}
	for _, parameter := range execution.Snapshot.Program.ResolvedParameters {
		expected[parameter.Name] = parameter.Value
	}
	epochs, epochErr := strconv.ParseInt(expected["epochs"], 10, 64)
	batch, batchErr := strconv.ParseInt(expected["batch_size"], 10, 64)
	seed, seedErr := strconv.ParseUint(expected["seed"], 10, 32)
	samples := int64(execution.Snapshot.Input.RowCount)
	if epochErr != nil || batchErr != nil || seedErr != nil || epochs < 1 || batch < 1 || samples < 1 {
		return invalid
	}
	batches := (samples-1)/batch + 1
	if epochs > math.MaxInt64/batches {
		return invalid
	}
	steps := epochs * batches
	device := "cpu"
	if execution.Snapshot.ComputeOrigin.DeviceMode == "gpu" {
		device = "cuda"
	}
	if summary.Status != "SUCCEEDED" || summary.Error != nil || summary.Samples != samples ||
		summary.Epochs != epochs || summary.BatchSize != batch || summary.LearningRate != expected["learning_rate"] ||
		summary.Seed == nil || uint64(*summary.Seed) != seed || summary.Steps != steps || summary.WorldSize != 1 ||
		summary.Device != device || summary.TorchVersion == "" || len(summary.TorchVersion) > 128 ||
		summary.InputSHA256 != execution.Snapshot.Input.Object.SHA256 || summary.InputBytes != execution.Snapshot.Input.Object.SizeBytes ||
		summary.InitialLoss == nil || summary.FinalLoss == nil || !finiteLoss(*summary.InitialLoss) || !finiteLoss(*summary.FinalLoss) {
		return invalid
	}
	var model struct {
		Architecture string `json:"architecture"`
		InputDim     int64  `json:"input_dim"`
		HiddenDim    int64  `json:"hidden_dim"`
		OutputDim    int64  `json:"output_dim"`
		Dtype        string `json:"dtype"`
	}
	raw, err = readFinetuningReport(ctx, root, "model_config.json", files)
	var fields map[string]json.RawMessage
	if err != nil || decodeFinetuningReport(raw, &model) != nil || json.Unmarshal(raw, &fields) != nil || len(fields) != 5 ||
		model.Architecture != "mlp-16-32-2" || model.InputDim != 16 || model.HiddenDim != 32 || model.OutputDim != 2 || model.Dtype != "float32" {
		return invalid
	}
	return verifyMetricFile(ctx, root, files, "metrics.jsonl", steps, steps)
}

func finiteLoss(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

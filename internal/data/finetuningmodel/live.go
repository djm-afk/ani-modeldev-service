package finetuningmodel

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	modelv1 "github.com/zhangzhe-ctrl/ani-model-service/api/model/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

type ModelFactsClient interface {
	ResolveFinetuningFacts(context.Context, string, *modelv1.ResolveFinetuningFactsRequest) (*modelv1.ResolveFinetuningFactsResponse, error)
}

// LiveReader uses the existing Model catalogue, exact object version and
// provider-verified archive inventory. User Model rights are rechecked by
// Governance before the delegated admission; no approval flag is fabricated.
type LiveReader struct {
	client ModelFactsClient
	scope  cpup01.StorageScope
}

var _ biz.FinetuningModelFactsReader = (*LiveReader)(nil)

func NewLive(client ModelFactsClient, scope cpup01.StorageScope) (*LiveReader, error) {
	if client == nil || scope.Validate() != nil {
		return nil, ErrInvalidModelFacts
	}
	return &LiveReader{client: client, scope: scope}, nil
}

func (r *LiveReader) ResolveFinetuningModelFacts(ctx context.Context, tenant string, intent cpup01.Intent, material cpup01.ImplementationMaterial) (cpup01.BaseModelRef, error) {
	if err := ctx.Err(); err != nil {
		return cpup01.BaseModelRef{}, err
	}
	if r == nil || r.client == nil || !canonicalID(tenant) || intent.Kind != "FINETUNING" || intent.BaseModelVersionID == nil || material.Finetuning == nil {
		return cpup01.BaseModelRef{}, biz.ErrFinetuningNotEnabled
	}
	if _, _, err := cpup01.CanonicalIntent(intent); err != nil {
		return cpup01.BaseModelRef{}, biz.ErrFinetuningNotEnabled
	}
	in := &modelv1.ResolveFinetuningFactsRequest{TenantId: tenant, BaseModelVersionId: *intent.BaseModelVersionID}
	if intent.TargetModelID != nil {
		in.Target = &modelv1.ResolveFinetuningFactsRequest_TargetModelId{TargetModelId: *intent.TargetModelID}
	} else if intent.NewModelName != nil {
		in.Target = &modelv1.ResolveFinetuningFactsRequest_NewModelName{NewModelName: *intent.NewModelName}
	}
	out, err := r.client.ResolveFinetuningFacts(ctx, tenant, in)
	if err != nil {
		return cpup01.BaseModelRef{}, err
	}
	if out.ValidateFor(in) != nil || out.Bucket != r.scope.Bucket || !strings.HasPrefix(out.ObjectKey, r.scope.ApprovedPrefix+"/") || !supportedQwenModel(out.ConfigJson, out.TokenizerConfigJson) {
		return cpup01.BaseModelRef{}, biz.ErrModelDependencyUnavailable
	}
	sequenceLimit := originalSequenceLimit(out.ConfigJson, out.TokenizerConfigJson)
	if sequenceLimit == 0 {
		return cpup01.BaseModelRef{}, biz.ErrModelDependencyUnavailable
	}
	parameters, err := intent.ResolveParameters()
	if err != nil {
		return cpup01.BaseModelRef{}, biz.ErrInvalidAdmission
	}
	for _, parameter := range parameters {
		if parameter.Name == "cutoff_len" {
			cutoff, err := strconv.ParseInt(parameter.Value, 10, 64)
			if err != nil || cutoff > sequenceLimit || cutoff > material.Finetuning.MaxSequenceLength {
				return cpup01.BaseModelRef{}, biz.ErrInvalidAdmission
			}
		}
	}
	limits := material.Finetuning.Budgets
	if out.SizeBytes > limits.MaxBaseModelBytes {
		return cpup01.BaseModelRef{}, biz.ErrModelDependencyUnavailable
	}
	version := out.ObjectVersionId
	base := cpup01.BaseModelRef{ModelID: out.ModelId, VersionID: out.ModelVersionId, Family: "Qwen2.5-0.5B-Instruct", Format: out.Format, Scope: r.scope, Object: cpup01.FixedObjectRef{StorageConnectionID: r.scope.StorageConnectionID, Bucket: out.Bucket, Key: out.ObjectKey, VersionID: &version, SizeBytes: out.SizeBytes, SHA256: out.Sha256}}
	remaining := limits.MaxBaseModelExpandedBytes
	for _, member := range out.Files {
		if member.SizeBytes > remaining {
			return cpup01.BaseModelRef{}, biz.ErrModelDependencyUnavailable
		}
		remaining -= member.SizeBytes
		base.Files = append(base.Files, cpup01.BaseModelFile{Path: member.Path, SizeBytes: member.SizeBytes, SHA256: member.Sha256})
	}
	if _, err := base.BaseManifestSHA256(); err != nil {
		return cpup01.BaseModelRef{}, biz.ErrModelDependencyUnavailable
	}
	return base, nil
}

// Match the entry's finite context limits, including the Tokenizer's unbounded
// integer sentinel. These bytes were verified against the original inventory;
// no current model/default is read while finalizing or executing this plan.
func originalSequenceLimit(config, tokenizer []byte) int64 {
	var model struct {
		Limit json.RawMessage `json:"max_position_embeddings"`
	}
	var tokens struct {
		Limit json.RawMessage `json:"model_max_length"`
	}
	if json.Unmarshal(config, &model) != nil || json.Unmarshal(tokenizer, &tokens) != nil {
		return 0
	}
	var limit int64
	for _, raw := range []json.RawMessage{model.Limit, tokens.Limit} {
		value, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil || value <= 0 || value == 1<<63-1 {
			continue
		}
		if limit == 0 || value < limit {
			limit = value
		}
	}
	return limit
}

// This is the structure exercised by the managed Qwen2.5-0.5B LoRA program.
// It is an implementation capability boundary, not a weight-hash allowlist.
func supportedQwenModel(config, tokenizer []byte) bool {
	var model struct {
		ModelType     string   `json:"model_type"`
		Architectures []string `json:"architectures"`
		HiddenSize    int      `json:"hidden_size"`
		Layers        int      `json:"num_hidden_layers"`
		Heads         int      `json:"num_attention_heads"`
		KVHeads       int      `json:"num_key_value_heads"`
		Intermediate  int      `json:"intermediate_size"`
		Vocab         int      `json:"vocab_size"`
		Dtype         string   `json:"torch_dtype"`
	}
	var tokens struct {
		Class string `json:"tokenizer_class"`
	}
	return json.Unmarshal(config, &model) == nil && json.Unmarshal(tokenizer, &tokens) == nil && model.ModelType == "qwen2" && len(model.Architectures) == 1 && model.Architectures[0] == "Qwen2ForCausalLM" && model.HiddenSize == 896 && model.Layers == 24 && model.Heads == 14 && model.KVHeads == 2 && model.Intermediate == 4864 && model.Vocab == 151936 && model.Dtype == "bfloat16" && tokens.Class == "Qwen2Tokenizer"
}

package finetuningmodel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"

	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/protobuf/proto"
)

type liveModelBoundary struct {
	response *modelv1.ResolveFinetuningFactsResponse
	calls    int
	tenant   string
}

func (b *liveModelBoundary) ResolveFinetuningFacts(_ context.Context, tenant string, _ *modelv1.ResolveFinetuningFactsRequest) (*modelv1.ResolveFinetuningFactsResponse, error) {
	b.calls++
	b.tenant = tenant
	return proto.Clone(b.response).(*modelv1.ResolveFinetuningFactsResponse), nil
}

var modelTestLimits = cpup01.ContentLimits{MaxContentBytes: 1 << 20}

func liveModelFixture(t *testing.T) (*liveModelBoundary, cpup01.StorageScope, cpup01.Intent, cpup01.ImplementationMaterial) {
	t.Helper()
	intent := conformance.FinetuningIntentV1()
	tenant := conformance.SnapshotV1().IdentityRef.TenantID
	config := []byte(`{"model_type":"llama","architectures":["LlamaForCausalLM"],"hidden_size":128,"torch_dtype":"float32","max_position_embeddings":8192}`)
	tokenizer := []byte(`{"tokenizer_class":"LlamaTokenizer","model_max_length":8192,"chat_template":"{{ messages }}"}`)
	out := &modelv1.ResolveFinetuningFactsResponse{TenantId: tenant, ModelId: "66666666-6666-4666-8666-666666666666", ModelVersionId: *intent.BaseModelVersionID, Format: "safetensors", Bucket: "ani-models", ObjectKey: tenant + "/models/original/model.tar", ObjectVersionId: "original-s3-version", SizeBytes: 16384, Sha256: strings.Repeat("a", 64), ConfigJson: config, TokenizerConfigJson: tokenizer, Target: &modelv1.ResolveFinetuningFactsResponse_NewModelName{NewModelName: *intent.NewModelName}, VerificationSource: modelv1.FinetuningVerificationSource}
	for name, raw := range map[string][]byte{"config.json": config, "model.safetensors": []byte("original-weight-boundary-bytes"), "tokenizer.model": []byte("native-sentencepiece-boundary"), "tokenizer_config.json": tokenizer} {
		setLiveFile(out, name, raw)
	}
	return &liveModelBoundary{response: out}, cpup01.StorageScope{StorageConnectionID: "model-store-v1", Bucket: "ani-models", ApprovedPrefix: tenant + "/models", CredentialReference: "model-base-read"}, intent, conformance.FinetuningMaterialV21()
}
func setLiveFile(out *modelv1.ResolveFinetuningFactsResponse, name string, raw []byte) {
	hash := sha256.Sum256(raw)
	next := &modelv1.FinetuningArtifactFile{Path: name, SizeBytes: int64(len(raw)), Sha256: hex.EncodeToString(hash[:])}
	for i, f := range out.Files {
		if f.Path == name {
			out.Files[i] = next
			return
		}
	}
	out.Files = append(out.Files, next)
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
}
func changeLiveJSON(t *testing.T, out *modelv1.ResolveFinetuningFactsResponse, name, key string, value any) {
	t.Helper()
	raw := out.ConfigJson
	if name == "tokenizer_config.json" {
		raw = out.TokenizerConfigJson
	}
	var values map[string]any
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	if d.Decode(&values) != nil {
		t.Fatal("fixture JSON")
	}
	if value == nil {
		delete(values, key)
	} else {
		values[key] = value
	}
	next, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if name == "config.json" {
		out.ConfigJson = next
	} else {
		out.TokenizerConfigJson = next
	}
	setLiveFile(out, name, next)
}
func TestLiveModelReaderFreezesGenericOriginalFactsWithoutInventedFiles(t *testing.T) {
	b, scope, intent, material := liveModelFixture(t)
	reader, err := NewLive(b, scope, modelTestLimits)
	if err != nil {
		t.Fatal(err)
	}
	base, err := reader.ResolveFinetuningModelFacts(context.Background(), b.response.TenantId, intent, material)
	if err != nil || b.calls != 1 || base.Facts.ModelType != "llama" || base.Facts.OriginalDtype != "float32" || base.Facts.ContextLimit != 8192 || base.Facts.WeightLayout.Kind != "SINGLE" || len(base.Facts.TemplateSources) != 1 || base.Facts.TemplateSources[0].Kind != "EMBEDDED" || len(base.Files) != 4 || base.Object.VersionID == nil || *base.Object.VersionID != "original-s3-version" || base.Object.SHA256 != b.response.Sha256 || base.Scope != scope {
		t.Fatalf("generic fixed original model missing: %+v %v", base, err)
	}
	if _, err := base.BaseManifestSHA256(); err != nil {
		t.Fatal(err)
	}
	changeLiveJSON(t, b.response, "config.json", "model_type", "qwen3")
	material.Finetuning.RuntimeCapabilities.CausalLMModelTypes = append(material.Finetuning.RuntimeCapabilities.CausalLMModelTypes, "qwen3")
	changeLiveJSON(t, b.response, "config.json", "hidden_size", 999)
	if next, err := reader.ResolveFinetuningModelFacts(context.Background(), b.response.TenantId, intent, material); err != nil || next.Facts.ModelType != "qwen3" {
		t.Fatal("dimensions/family remained an approval gate", err)
	}
}
func TestLiveModelReaderNormalizesRealShardedInventory(t *testing.T) {
	b, scope, intent, material := liveModelFixture(t)
	for i, f := range b.response.Files {
		if f.Path == "model.safetensors" {
			b.response.Files = append(b.response.Files[:i], b.response.Files[i+1:]...)
			break
		}
	}
	index := []byte(`{"weight_map":{"a":"part-1.safetensors","b":"part-1.safetensors","c":"part-2.safetensors"}}`)
	b.response.WeightIndexJson = index
	for name, raw := range map[string][]byte{"model.safetensors.index.json": index, "part-1.safetensors": []byte("first"), "part-2.safetensors": []byte("second")} {
		setLiveFile(b.response, name, raw)
	}
	reader, _ := NewLive(b, scope, modelTestLimits)
	base, err := reader.ResolveFinetuningModelFacts(context.Background(), b.response.TenantId, intent, material)
	if err != nil || base.Facts.WeightLayout.Kind != "SHARDED" || len(base.Facts.WeightLayout.ShardPaths) != 2 {
		t.Fatalf("shards not normalized: %+v %v", base.Facts.WeightLayout, err)
	}
}
func TestLiveModelReaderIncludesProvenNativeContextField(t *testing.T) {
	b, scope, intent, material := liveModelFixture(t)
	changeLiveJSON(t, b.response, "config.json", "seq_length", 32)
	material.Finetuning.RuntimeCapabilities.ContextLimitFields = append(material.Finetuning.RuntimeCapabilities.ContextLimitFields, cpup01.ContextLimitField{Source: "config.json", Field: "seq_length"})
	reader, _ := NewLive(b, scope, modelTestLimits)
	base, err := reader.ResolveFinetuningModelFacts(context.Background(), b.response.TenantId, intent, material)
	if err != nil || base.Facts.ContextLimit != 32 {
		t.Fatal("proven native context field ignored", err)
	}
}
func TestLiveModelReaderRejectsUnfixedForeignOrChangedProviderFacts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*modelv1.ResolveFinetuningFactsResponse)
	}{
		{"null version", func(r *modelv1.ResolveFinetuningFactsResponse) { r.ObjectVersionId = "null" }},
		{"wrong tenant", func(r *modelv1.ResolveFinetuningFactsResponse) { r.TenantId = r.ModelId }},
		{"foreign bucket", func(r *modelv1.ResolveFinetuningFactsResponse) { r.Bucket = "other" }},
		{"foreign prefix", func(r *modelv1.ResolveFinetuningFactsResponse) { r.ObjectKey = r.TenantId + "/other/model.tar" }},
		{"config mismatch", func(r *modelv1.ResolveFinetuningFactsResponse) { r.ConfigJson = append(r.ConfigJson, ' ') }},
		{"injected target", func(r *modelv1.ResolveFinetuningFactsResponse) {
			r.Target = &modelv1.ResolveFinetuningFactsResponse_NewModelName{NewModelName: "different"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, scope, intent, material := liveModelFixture(t)
			tc.change(b.response)
			reader, _ := NewLive(b, scope, modelTestLimits)
			if _, err := reader.ResolveFinetuningModelFacts(context.Background(), b.response.TenantId, intent, material); !errors.Is(err, biz.ErrModelDependencyUnavailable) {
				t.Fatal("unsafe provider observation admitted", err)
			}
		})
	}
}
func TestLiveModelReaderUsesOneExplicitLogicalContentLimit(t *testing.T) {
	b, scope, intent, material := liveModelFixture(t)
	var logical int64
	for _, f := range b.response.Files {
		logical += f.SizeBytes
	}
	for _, limit := range []int64{logical, logical - 1} {
		reader, err := NewLive(b, scope, cpup01.ContentLimits{MaxContentBytes: limit})
		if err != nil {
			t.Fatal(err)
		}
		_, err = reader.ResolveFinetuningModelFacts(context.Background(), b.response.TenantId, intent, material)
		var failure *cpup01.LimitViolation
		if limit == logical && err != nil || limit < logical && (!errors.As(err, &failure) || failure.Actual != logical || failure.Limit != limit || failure.Kind != "base_model") {
			t.Fatalf("logical content boundary: %v", err)
		}
	}
	if _, err := NewLive(b, scope, cpup01.ContentLimits{}); err == nil {
		t.Fatal("omitted deployment L defaulted")
	}
}

func TestModelContextUsesSharedGoPythonVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/context-limit-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name              string `json:"name"`
		Config, Tokenizer json.RawMessage
		Want              int64  `json:"want"`
		Error             string `json:"error"`
	}
	if json.Unmarshal(raw, &vectors) != nil {
		t.Fatal("context vectors")
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			b, scope, intent, material := liveModelFixture(t)
			for name, value := range map[string]json.RawMessage{"config.json": v.Config, "tokenizer_config.json": v.Tokenizer} {
				key := "max_position_embeddings"
				body := b.response.ConfigJson
				if name == "tokenizer_config.json" {
					key = "model_max_length"
					body = b.response.TokenizerConfigJson
				}
				var object map[string]json.RawMessage
				if json.Unmarshal(body, &object) != nil {
					t.Fatal("fixture")
				}
				if len(value) == 0 {
					delete(object, key)
				} else {
					object[key] = value
				}
				next, err := json.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				setLiveFile(b.response, name, next)
				if name == "config.json" {
					b.response.ConfigJson = next
				} else {
					b.response.TokenizerConfigJson = next
				}
			}
			reader, _ := NewLive(b, scope, modelTestLimits)
			base, err := reader.ResolveFinetuningModelFacts(context.Background(), b.response.TenantId, intent, material)
			if v.Error != "" {
				if err == nil || err.Error() != v.Error {
					t.Fatalf("want %s got %v", v.Error, err)
				}
			} else if err != nil || base.Facts.ContextLimit != v.Want {
				t.Fatalf("want %d got %d/%v", v.Want, base.Facts.ContextLimit, err)
			}
		})
	}
}

func TestModelArchitectureAndOriginalDtypeRemainDescriptions(t *testing.T) {
	b, scope, intent, material := liveModelFixture(t)
	changeLiveJSON(t, b.response, "config.json", "architectures", nil)
	changeLiveJSON(t, b.response, "config.json", "torch_dtype", map[string]any{"decoder": "float16"})
	reader, _ := NewLive(b, scope, modelTestLimits)
	base, err := reader.ResolveFinetuningModelFacts(context.Background(), b.response.TenantId, intent, material)
	if err != nil || len(base.Facts.Architectures) != 0 || base.Facts.OriginalDtype != `{"decoder":"float16"}` {
		t.Fatalf("ARCHITECTURE_DTYPE_DESCRIPTION_REQUIRED: %+v %v", base.Facts, err)
	}
}

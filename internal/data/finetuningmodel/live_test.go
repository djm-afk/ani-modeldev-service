package finetuningmodel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	modelv1 "github.com/zhangzhe-ctrl/ani-model-service/api/model/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/protobuf/proto"
)

// This substitutes only the external Model owner response. It cannot write a
// model, approve a parameter or bypass the admission's real domain behavior.
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

func liveModelFixture(t *testing.T) (*liveModelBoundary, cpup01.StorageScope, cpup01.Intent, cpup01.ImplementationMaterial) {
	t.Helper()
	intent := conformance.FinetuningIntentV1()
	tenant := conformance.SnapshotV1().IdentityRef.TenantID
	config := []byte(`{"model_type":"qwen2","architectures":["Qwen2ForCausalLM"],"hidden_size":896,"num_hidden_layers":24,"num_attention_heads":14,"num_key_value_heads":2,"intermediate_size":4864,"vocab_size":151936,"torch_dtype":"bfloat16","max_position_embeddings":32768}`)
	tokenizer := []byte(`{"tokenizer_class":"Qwen2Tokenizer","model_max_length":32768}`)
	out := &modelv1.ResolveFinetuningFactsResponse{TenantId: tenant, ModelId: "66666666-6666-4666-8666-666666666666", ModelVersionId: *intent.BaseModelVersionID, Format: "safetensors", Bucket: "ani-models", ObjectKey: tenant + "/models/original/model.tar", ObjectVersionId: "original-s3-version", SizeBytes: 16384, Sha256: strings.Repeat("a", 64), ConfigJson: config, TokenizerConfigJson: tokenizer, Target: &modelv1.ResolveFinetuningFactsResponse_NewModelName{NewModelName: *intent.NewModelName}, VerificationSource: modelv1.FinetuningVerificationSource}
	for name, raw := range map[string][]byte{"config.json": config, "model.safetensors": []byte("original-weight-boundary-bytes"), "tokenizer.json": []byte("{}"), "tokenizer_config.json": tokenizer} {
		digest := sha256.Sum256(raw)
		out.Files = append(out.Files, &modelv1.FinetuningArtifactFile{Path: name, SizeBytes: int64(len(raw)), Sha256: hex.EncodeToString(digest[:])})
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	return &liveModelBoundary{response: out}, cpup01.StorageScope{StorageConnectionID: "model-store-v1", Bucket: "ani-models", ApprovedPrefix: tenant + "/models", CredentialReference: "model-base-read"}, intent, conformance.FinetuningMaterialV21()
}

func changeLiveConfig(t *testing.T, out *modelv1.ResolveFinetuningFactsResponse, key string, value any) {
	t.Helper()
	var config map[string]any
	if json.Unmarshal(out.ConfigJson, &config) != nil {
		t.Fatal("fixture config")
	}
	config[key] = value
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	out.ConfigJson = raw
	digest := sha256.Sum256(raw)
	for _, file := range out.Files {
		if file.Path == "config.json" {
			file.SizeBytes = int64(len(raw))
			file.Sha256 = hex.EncodeToString(digest[:])
		}
	}
}

func TestLiveModelReaderRejectsCutoffAboveOriginalTokenizerBoundary(t *testing.T) {
	boundary, scope, intent, material := liveModelFixture(t)
	cutoff := int64(96)
	intent.Finetuning.CutoffLen = &cutoff
	boundary.response.TokenizerConfigJson = []byte(`{"tokenizer_class":"Qwen2Tokenizer","model_max_length":64}`)
	digest := sha256.Sum256(boundary.response.TokenizerConfigJson)
	for _, file := range boundary.response.Files {
		if file.Path == "tokenizer_config.json" {
			file.SizeBytes = int64(len(boundary.response.TokenizerConfigJson))
			file.Sha256 = hex.EncodeToString(digest[:])
		}
	}
	reader, err := NewLive(boundary, scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ResolveFinetuningModelFacts(context.Background(), boundary.response.TenantId, intent, material); !errors.Is(err, biz.ErrInvalidAdmission) {
		t.Fatal("cutoff above the verified original tokenizer boundary was admitted", err)
	}
}

func TestLiveModelReaderUsesFiniteOriginalContextAndMaterialLimits(t *testing.T) {
	for _, test := range []struct {
		name      string
		model     any
		tokenizer any
		cutoff    int64
		material  int64
		want      error
	}{
		{"model is smaller", 64, 256, 96, 32768, biz.ErrInvalidAdmission},
		{"tokenizer boundary is inclusive", 256, 64, 64, 32768, nil},
		{"one above tokenizer boundary", 256, 64, 65, 32768, biz.ErrInvalidAdmission},
		{"model boundary is inclusive", 64, 256, 64, 32768, nil},
		{"material is smaller", 256, 256, 96, 64, biz.ErrInvalidAdmission},
		{"omitted cutoff uses implementation default", 64, 64, 0, 32768, biz.ErrInvalidAdmission},
		{"tokenizer sentinel uses model boundary", 32768, json.Number("1000000000000000019884624838656"), 32768, 65536, nil},
		{"sentinel does not enlarge model boundary", 32768, json.Number("1000000000000000019884624838656"), 32769, 65536, biz.ErrInvalidAdmission},
		{"int64 maximum is not a finite limit", nil, json.Number("9223372036854775807"), 96, 32768, biz.ErrModelDependencyUnavailable},
		{"missing model boundary uses finite tokenizer", nil, 128, 96, 32768, nil},
		{"no finite boundary", nil, nil, 96, 32768, biz.ErrModelDependencyUnavailable},
		{"invalid boundaries are unavailable", -1, "128", 96, 32768, biz.ErrModelDependencyUnavailable},
		{"fractional boundaries are not integers", json.Number("128.5"), 0, 96, 32768, biz.ErrModelDependencyUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			boundary, scope, intent, material := liveModelFixture(t)
			changeLiveConfig(t, boundary.response, "max_position_embeddings", test.model)
			tokenizer, err := json.Marshal(map[string]any{"tokenizer_class": "Qwen2Tokenizer", "model_max_length": test.tokenizer})
			if err != nil {
				t.Fatal(err)
			}
			boundary.response.TokenizerConfigJson = tokenizer
			digest := sha256.Sum256(tokenizer)
			for _, file := range boundary.response.Files {
				if file.Path == "tokenizer_config.json" {
					file.SizeBytes = int64(len(tokenizer))
					file.Sha256 = hex.EncodeToString(digest[:])
				}
			}
			if test.cutoff != 0 {
				cutoff := test.cutoff
				intent.Finetuning.CutoffLen = &cutoff
			}
			material.Finetuning.MaxSequenceLength = test.material
			reader, err := NewLive(boundary, scope)
			if err != nil {
				t.Fatal(err)
			}
			base, err := reader.ResolveFinetuningModelFacts(context.Background(), boundary.response.TenantId, intent, material)
			if !errors.Is(err, test.want) || test.want == nil && base.VersionID != *intent.BaseModelVersionID {
				t.Fatal("original context boundary did not govern admission", err)
			}
		})
	}
}

func TestLiveModelReaderFreezesOriginalVersionBytesAndInventory(t *testing.T) {
	boundary, scope, intent, material := liveModelFixture(t)
	reader, err := NewLive(boundary, scope)
	if err != nil {
		t.Fatal(err)
	}
	base, err := reader.ResolveFinetuningModelFacts(context.Background(), boundary.response.TenantId, intent, material)
	if err != nil || boundary.calls != 1 || boundary.tenant != boundary.response.TenantId || base.ModelID != boundary.response.ModelId || base.VersionID != *intent.BaseModelVersionID || base.Family != "Qwen2.5-0.5B-Instruct" || base.Object.VersionID == nil || *base.Object.VersionID != "original-s3-version" || base.Object.SHA256 != boundary.response.Sha256 || base.Scope != scope || len(base.Files) != 4 {
		t.Fatal("original Model facts were replaced", err)
	}
	if _, err := base.BaseManifestSHA256(); err != nil {
		t.Fatal(err)
	}
	// Another valid original digest is accepted by the same program structure;
	// there is no second editable weight hash allowlist.
	boundary.response.Sha256 = strings.Repeat("b", 64)
	boundary.response.ObjectVersionId = "another-original-s3-version"
	version := "88888888-8888-4888-8888-888888888888"
	intent.BaseModelVersionID = &version
	boundary.response.ModelVersionId = version
	if next, err := reader.ResolveFinetuningModelFacts(context.Background(), boundary.response.TenantId, intent, material); err != nil || next.Object.SHA256 != boundary.response.Sha256 {
		t.Fatal("program capability became a weight approval", err)
	}
}

func TestLiveModelReaderRejectsUnfixedForeignOrUnsupportedOriginalFacts(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*modelv1.ResolveFinetuningFactsResponse)
	}{
		{"null version", func(r *modelv1.ResolveFinetuningFactsResponse) { r.ObjectVersionId = "null" }},
		{"wrong tenant", func(r *modelv1.ResolveFinetuningFactsResponse) { r.TenantId = "99999999-9999-4999-8999-999999999999" }},
		{"wrong target", func(r *modelv1.ResolveFinetuningFactsResponse) {
			r.Target = &modelv1.ResolveFinetuningFactsResponse_NewModelName{NewModelName: "another-target"}
		}},
		{"foreign bucket", func(r *modelv1.ResolveFinetuningFactsResponse) { r.Bucket = "other-models" }},
		{"unapproved prefix", func(r *modelv1.ResolveFinetuningFactsResponse) { r.ObjectKey = r.TenantId + "/another/model.tar" }},
		{"unverified source", func(r *modelv1.ResolveFinetuningFactsResponse) { r.VerificationSource = "caller-assertion" }},
		{"changed config bytes", func(r *modelv1.ResolveFinetuningFactsResponse) { r.ConfigJson = append(r.ConfigJson, ' ') }},
		{"changed tokenizer bytes", func(r *modelv1.ResolveFinetuningFactsResponse) {
			r.TokenizerConfigJson = append(r.TokenizerConfigJson, ' ')
		}},
		{"qwen3 family", func(r *modelv1.ResolveFinetuningFactsResponse) { changeLiveConfig(t, r, "model_type", "qwen3") }},
		{"unsupported dimensions", func(r *modelv1.ResolveFinetuningFactsResponse) { changeLiveConfig(t, r, "hidden_size", 1024) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			b, scope, intent, material := liveModelFixture(t)
			test.change(b.response)
			reader, err := NewLive(b, scope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reader.ResolveFinetuningModelFacts(context.Background(), conformance.SnapshotV1().IdentityRef.TenantID, intent, material); !errors.Is(err, biz.ErrModelDependencyUnavailable) || b.calls != 1 {
				t.Fatal("unsafe original Model response became a base snapshot", err)
			}
		})
	}
}

func TestLiveModelReaderChecksInstalledByteBounds(t *testing.T) {
	b, scope, intent, material := liveModelFixture(t)
	material.Finetuning.Budgets.MaxBaseModelBytes = b.response.SizeBytes - 1
	reader, _ := NewLive(b, scope)
	if _, err := reader.ResolveFinetuningModelFacts(context.Background(), b.response.TenantId, intent, material); !errors.Is(err, biz.ErrModelDependencyUnavailable) {
		t.Fatal("installed base byte bound omitted", err)
	}
}

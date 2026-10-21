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
	config := []byte(`{"model_type":"qwen2","architectures":["Qwen2ForCausalLM"],"hidden_size":896,"num_hidden_layers":24,"num_attention_heads":14,"num_key_value_heads":2,"intermediate_size":4864,"vocab_size":151936,"torch_dtype":"bfloat16"}`)
	tokenizer := []byte(`{"tokenizer_class":"Qwen2Tokenizer"}`)
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

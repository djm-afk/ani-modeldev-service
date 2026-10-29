package biz_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func TestFinetuningTemplateSelectionFreezesIntentAssociationOrNative(t *testing.T) {
	for _, tc := range []struct {
		name                string
		intent, association bool
		native              []cpup01.TemplateSource
		mode, source        string
		failure             error
	}{
		{name: "explicit", intent: true, association: true, mode: "NAMED", source: "USER"},
		{name: "version association", association: true, mode: "NAMED", source: "MODEL_ASSOCIATION"},
		{name: "native embedded", native: []cpup01.TemplateSource{{Kind: "EMBEDDED", Path: "tokenizer_config.json", Name: "default", Default: true, SizeBytes: 14, SHA256: strings.Repeat("a", 64)}}, mode: "TOKENIZER_NATIVE", source: "MODEL_TOKENIZER"},
		{name: "multiple without default", native: []cpup01.TemplateSource{{Kind: "EMBEDDED", Path: "tokenizer_config.json", Name: "chat", SizeBytes: 14, SHA256: strings.Repeat("a", 64)}, {Kind: "EMBEDDED", Path: "tokenizer_config.json", Name: "tools", SizeBytes: 14, SHA256: strings.Repeat("b", 64)}}, failure: biz.ErrFinetuningTemplateRequired},
		{name: "single named alternative is not default", native: []cpup01.TemplateSource{{Kind: "EMBEDDED", Path: "tokenizer_config.json", Name: "chat", SizeBytes: 14, SHA256: strings.Repeat("a", 64)}}, failure: biz.ErrFinetuningTemplateRequired},
		{name: "no template", failure: biz.ErrFinetuningTemplateRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			intent := conformance.FinetuningIntentV1()
			material := conformance.FinetuningMaterialV21()
			material.Finetuning.ModelTemplateAssociations = nil
			base := conformance.FinetuningSnapshotV1().Finetuning.BaseModel
			base.Facts.TemplateSources = tc.native
			if tc.intent {
				id := "qwen"
				intent.FinetuningTemplate = &id
			}
			if tc.association {
				material.Finetuning.ModelTemplateAssociations = []cpup01.ModelTemplateAssociation{{ModelVersionID: base.VersionID, TemplateID: "qwen"}}
			}
			plan, err := biz.ResolveFinetuningTemplate(intent, base, material)
			if !errors.Is(err, tc.failure) || tc.failure == nil && (plan.Mode != tc.mode || plan.SelectionSource != tc.source || plan.ValidateFor(material.Finetuning.RuntimeCapabilities) != nil) {
				t.Fatalf("plan=%+v err=%v", plan, err)
			}
		})
	}
}
func TestFinetuningTemplateIndependentDefaultFilePrecedesEmbedded(t *testing.T) {
	intent := conformance.FinetuningIntentV1()
	material := conformance.FinetuningMaterialV21()
	material.Finetuning.ModelTemplateAssociations = nil
	base := conformance.FinetuningSnapshotV1().Finetuning.BaseModel
	base.Facts.TemplateSources = []cpup01.TemplateSource{{Kind: "EMBEDDED", Path: "tokenizer_config.json", Name: "default", Default: true, SizeBytes: 5, SHA256: strings.Repeat("a", 64)}, {Kind: "FILE", Path: "chat_template.jinja", Name: "default", Default: true, SizeBytes: 7, SHA256: strings.Repeat("b", 64)}}
	plan, err := biz.ResolveFinetuningTemplate(intent, base, material)
	if err != nil || plan.Source == nil || plan.Source.Kind != "FILE" || plan.SourceSHA256 != strings.Repeat("b", 64) {
		t.Fatal("independent file lost native precedence", err)
	}
	id := "unregistered"
	intent.FinetuningTemplate = &id
	if _, err := biz.ResolveFinetuningTemplate(intent, base, material); !errors.Is(err, biz.ErrFinetuningTemplateUnsupported) {
		t.Fatal("unknown explicit template fell back", err)
	}
}

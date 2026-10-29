package cpup01_test

import (
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

func TestCurrentEffectiveTemplateFreezesExactBodyAndRuntimeIdentity(t *testing.T) {
	plan, capabilities := conformance.FinetuningTemplatePlan(), conformance.FinetuningRuntimeCapabilities()
	planDigest, _ := plan.Digest()
	value := cpup01.EffectiveTemplate{SchemaVersion: cpup01.EffectiveTemplateSchemaVersion, TemplatePlanSHA256: planDigest, RuntimeCapabilitiesSHA256: plan.RuntimeCapabilitiesSHA256, ImageDigest: plan.ImageDigest, SourceSHA256: plan.SourceSHA256, TemplateFile: cpup01.FileFact{Path: "reports/train/effective-template.jinja", SizeBytes: 10, SHA256: strings.Repeat("a", 64)}, VocabSize: 11, SpecialTokens: []cpup01.SpecialToken{{Name: "eos_token", Token: "<end>", ID: 10}}, EOSIDs: []int64{10}}
	if err := value.ValidateFor(plan, capabilities); err != nil {
		t.Fatal(err)
	}
	raw, err := value.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := cpup01.ParseEffectiveTemplate(raw)
	if err != nil || decoded.ValidateFor(plan, capabilities) != nil {
		t.Fatal("effective template identity lost", err)
	}
	for name, mutate := range map[string]func(*cpup01.EffectiveTemplate){
		"alternate path": func(v *cpup01.EffectiveTemplate) { v.TemplateFile.Path = "elsewhere.jinja" },
		"wrong plan":     func(v *cpup01.EffectiveTemplate) { v.TemplatePlanSHA256 = strings.Repeat("b", 64) },
		"oversize body":  func(v *cpup01.EffectiveTemplate) { v.TemplateFile.SizeBytes = cpup01.MaxModelConfigBytes + 1 },
		"duplicate EOS":  func(v *cpup01.EffectiveTemplate) { v.EOSIDs = []int64{10, 10} },
		"negative token": func(v *cpup01.EffectiveTemplate) {
			v.SpecialTokens = []cpup01.SpecialToken{{Name: "eos", Token: "<end>", ID: -1}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			copy := value
			mutate(&copy)
			if copy.ValidateFor(plan, capabilities) == nil {
				t.Fatal("invalid effective template accepted")
			}
		})
	}
}

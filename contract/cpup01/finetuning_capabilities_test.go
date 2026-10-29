package cpup01_test

import (
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

func TestCurrentBaseInventoryAllowsTrueEmptyOrdinaryMember(t *testing.T) {
	snapshot := conformance.FinetuningSnapshotV1()
	base := &snapshot.Finetuning.BaseModel
	base.Files = append(base.Files, cpup01.BaseModelFile{Path: "README.txt", SizeBytes: 0, SHA256: cpup01.EmptyContentSHA256})
	if _, err := base.BaseManifestBytes(); err != nil {
		t.Fatal("ordinary empty source member rejected", err)
	}
	if err := base.Facts.ValidateFor(base.Files); err != nil {
		t.Fatal(err)
	}
	base.Files[len(base.Files)-1].SHA256 = strings.Repeat("a", 64)
	if _, err := base.BaseManifestBytes(); err == nil {
		t.Fatal("claimed nonempty checksum for empty bytes accepted")
	}
}

func TestCurrentTemplatePlanBindsRegisteredTemplateAndRuntime(t *testing.T) {
	image := "registry.example.test/native/finetune@sha256:" + strings.Repeat("a", 64)
	capabilities := cpup01.RuntimeCapabilities{SchemaVersion: cpup01.FinetuningCapabilitiesSchemaVersion, ImageDigest: image, EntrypointSHA256: strings.Repeat("b", 64), CausalLMModelTypes: []string{"qwen2", "llama"}, RegisteredTemplates: []cpup01.RegisteredTemplate{{ID: "native", SourceSHA256: strings.Repeat("c", 64)}}, ContextLimitFields: []cpup01.ContextLimitField{{Source: "config.json", Field: "max_position_embeddings"}}, Packages: []cpup01.RuntimePackage{{Name: "transformers", Version: "actual-image-version"}}}
	digest, err := capabilities.Digest()
	if err != nil {
		t.Fatal(err)
	}
	id := "native"
	plan := cpup01.TemplatePlan{SchemaVersion: cpup01.TemplatePlanSchemaVersion, Mode: "NAMED", SelectionSource: "USER", TemplateID: &id, ImageDigest: image, RuntimeCapabilitiesSHA256: digest, SourceSHA256: strings.Repeat("c", 64)}
	if err := plan.ValidateFor(capabilities); err != nil {
		t.Fatal(err)
	}
	id = "unregistered"
	if err := plan.ValidateFor(capabilities); err == nil {
		t.Fatal("unregistered named template accepted")
	}
}

func TestNativeModelMayOmitDescriptiveArchitectureNames(t *testing.T) {
	base := conformance.FinetuningSnapshotV1().Finetuning.BaseModel
	base.Facts.Architectures = []string{}
	if err := base.Facts.ValidateFor(base.Files); err != nil {
		t.Fatal("native model_type is sufficient when architectures is absent", err)
	}
	base.Facts.Architectures = []string{""}
	if err := base.Facts.ValidateFor(base.Files); err == nil {
		t.Fatal("empty declared architecture accepted")
	}
}

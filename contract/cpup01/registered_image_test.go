package cpup01_test

import (
	"bytes"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

func TestCurrentTrainingMaterialNamesOnlyTheCatalogueDefault(t *testing.T) {
	material := conformance.MaterialV21()
	material.Training.DefaultImage = cpup01.DefaultTrainingImage{ImageID: "img_72a769640ebe4abb8b4c19e482d4a50e", Scope: cpup01.ImageScopePlatform}
	raw, err := material.Canonical()
	if err != nil {
		t.Fatal("registered catalogue default rejected", err)
	}
	if bytes.Contains(raw, []byte("image_version_id")) || bytes.Contains(raw, []byte(`"image_digest"`)) || bytes.Contains(raw, []byte(`"image_version"`)) {
		t.Fatal("training material still pins a retired or stale image identity")
	}
	decoded, _, err := cpup01.ParseMaterial(raw)
	if err != nil || decoded.Training.DefaultImage.ImageID != "img_72a769640ebe4abb8b4c19e482d4a50e" || decoded.Training.DefaultImage.Scope != cpup01.ImageScopePlatform {
		t.Fatal("catalogue default changed", err)
	}
	for _, scope := range []cpup01.ImageScope{"", "public", "PLATFORM"} {
		bad := material
		training := *material.Training
		training.DefaultImage.Scope = scope
		bad.Training = &training
		if _, err := bad.Canonical(); err == nil {
			t.Fatalf("invalid scope %q accepted", scope)
		}
	}
	material.Training.DefaultImage.ImageID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	if _, err := material.Canonical(); err == nil {
		t.Fatal("retired image UUID admitted as current ImageID")
	}
}

func TestCurrentFinetuningMaterialStillFreezesTheImageIdentity(t *testing.T) {
	material := conformance.FinetuningMaterialV21()
	raw, err := material.Canonical()
	if err != nil {
		t.Fatal("finetuning material rejected", err)
	}
	if material.Program == nil {
		t.Fatal("finetuning material lost its frozen program")
	}
	decoded, _, err := cpup01.ParseMaterial(raw)
	if err != nil || decoded.Program == nil || decoded.Program.ImageID != material.Program.ImageID || decoded.Program.ImageVersion != material.Program.ImageVersion {
		t.Fatal("finetuning image identity changed", err)
	}
	for _, version := range []int64{0, -1} {
		bad := material
		program := *material.Program
		program.ImageVersion = version
		bad.Program = &program
		if _, err := bad.Canonical(); err == nil {
			t.Fatal("missing image version accepted")
		}
	}
}

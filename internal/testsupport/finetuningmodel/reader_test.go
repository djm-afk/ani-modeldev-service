package finetuningmodel_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/finetuningmodel"
)

const (
	controlledTenant  = "8f79e7cc-769b-4b2d-87d3-a69c53c946f3"
	controlledVersion = "66666666-6666-4666-8666-666666666666"
)

// writeControlledDocument writes a pinned document and returns its real digest.
// The fixture only proves the reader's copy/validate behavior; it is never a
// Model permission, approval or protected-status record.
func writeControlledDocument(t *testing.T, mutate func(map[string]any)) (string, string) {
	t.Helper()
	base := conformance.FinetuningSnapshotV1().Finetuning.BaseModel
	base.VersionID = controlledVersion
	document := map[string]any{
		"schema_version": "ani.modeldev.controlled-model-facts.v1",
		"tenant_id":      controlledTenant,
		"version_id":     controlledVersion,
		"base_model":     base,
	}
	if mutate != nil {
		mutate(document)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "controlled-model.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	return path, hex.EncodeToString(digest[:])
}

func materialForModel() cpup01.ImplementationMaterial {
	return conformance.FinetuningMaterialV21()
}

func intentForVersion(version string) cpup01.Intent {
	intent := conformance.FinetuningIntentV1()
	intent.BaseModelVersionID = &version
	return intent
}

func TestResolveReturnsFrozenBaseModel(t *testing.T) {
	path, digest := writeControlledDocument(t, nil)
	reader, err := finetuningmodel.Load(context.Background(), []finetuningmodel.FileSource{{Path: path, SHA256: digest}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := reader.ResolveFinetuningModelFacts(context.Background(), controlledTenant, intentForVersion(controlledVersion), materialForModel())
	if err != nil {
		t.Fatal(err)
	}
	if got.VersionID != controlledVersion || len(got.Files) != 4 {
		t.Fatalf("unexpected base model: %+v", got)
	}
	// A caller mutation must not change a later read.
	got.Files[0].Path = "mutated"
	again, err := reader.ResolveFinetuningModelFacts(context.Background(), controlledTenant, intentForVersion(controlledVersion), materialForModel())
	if err != nil || again.Files[0].Path == "mutated" {
		t.Fatalf("reader leaked internal state: %+v, err %v", again, err)
	}
}

func TestResolveRejectsUnknownTenantOrVersion(t *testing.T) {
	path, digest := writeControlledDocument(t, nil)
	reader, err := finetuningmodel.Load(context.Background(), []finetuningmodel.FileSource{{Path: path, SHA256: digest}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ResolveFinetuningModelFacts(context.Background(), "33333333-3333-4333-8333-333333333333", intentForVersion(controlledVersion), materialForModel()); err != biz.ErrModelDependencyUnavailable {
		t.Fatalf("unknown tenant error = %v", err)
	}
	if _, err := reader.ResolveFinetuningModelFacts(context.Background(), controlledTenant, intentForVersion("77777777-7777-4777-8777-777777777777"), materialForModel()); err != biz.ErrModelDependencyUnavailable {
		t.Fatalf("version mismatch error = %v", err)
	}
}

func TestResolveStaysNotEnabledWithoutReader(t *testing.T) {
	var reader *finetuningmodel.Reader
	if _, err := reader.ResolveFinetuningModelFacts(context.Background(), controlledTenant, intentForVersion(controlledVersion), materialForModel()); err != biz.ErrFinetuningNotEnabled {
		t.Fatalf("absent reader error = %v", err)
	}
}

func TestLoadRejectsTamperedDigestAndUnknownFields(t *testing.T) {
	path, _ := writeControlledDocument(t, nil)
	if _, err := finetuningmodel.Load(context.Background(), []finetuningmodel.FileSource{{Path: path, SHA256: strings.Repeat("0", 64)}}); err == nil {
		t.Fatal("tampered digest accepted")
	}
	unknownPath, unknownDigest := writeControlledDocument(t, func(document map[string]any) {
		document["unexpected"] = true
	})
	if _, err := finetuningmodel.Load(context.Background(), []finetuningmodel.FileSource{{Path: unknownPath, SHA256: unknownDigest}}); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestLoadRejectsEmptyAndDuplicateSources(t *testing.T) {
	if _, err := finetuningmodel.Load(context.Background(), nil); err == nil {
		t.Fatal("empty sources accepted")
	}
	path, digest := writeControlledDocument(t, nil)
	source := finetuningmodel.FileSource{Path: path, SHA256: digest}
	if _, err := finetuningmodel.Load(context.Background(), []finetuningmodel.FileSource{source, source}); err == nil {
		t.Fatal("duplicate document accepted")
	}
}

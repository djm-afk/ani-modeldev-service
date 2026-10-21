package catalogue_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/catalogue"
)

func materialFixture(t *testing.T) (cpup01.ImplementationMaterial, []byte) {
	t.Helper()
	m := conformance.MaterialV21()
	raw, err := m.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	return m, raw
}
func writeMaterial(t *testing.T, directory, id, mode string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, id+"."+mode+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestInstalledFiniteImplementationMaterialHasExactBytesAndFreshCopies(t *testing.T) {
	want, raw := materialFixture(t)
	directory := t.TempDir()
	writeMaterial(t, directory, "builtin.mlp", "cpu", raw)
	parsed, digest, err := cpup01.ParseMaterial(raw)
	sum := sha256.Sum256(raw)
	if err != nil || digest != hex.EncodeToString(sum[:]) || !reflect.DeepEqual(parsed, want) {
		t.Fatal("fixed software material lost bytes", err)
	}
	reader := catalogue.NewReader(directory)
	if err := reader.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, err := reader.ReadImplementationMaterial(context.Background(), "builtin.mlp", "cpu")
	if err != nil || !reflect.DeepEqual(first, want) {
		t.Fatal(err)
	}
	first.Program.Command[0] = "caller-mutation"
	first.OutputContract.RequiredFiles[0].RelativePath = "caller-mutation"
	again, err := catalogue.ReadImplementationMaterial(context.Background(), directory, "builtin.mlp", "cpu")
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatal("caller mutated installed material", err)
	}
	// Installing another finite device file cannot replace the CPU selection.
	other := conformance.MaterialV21()
	other.Program.ImageVersionID = "66666666-6666-4666-8666-666666666666"
	otherRaw, err := other.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	writeMaterial(t, directory, "builtin.mlp", "gpu", otherRaw)
	again, err = reader.ReadImplementationMaterial(context.Background(), "builtin.mlp", "cpu")
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatal("device selection changed existing material", err)
	}
	gpu, err := reader.ReadImplementationMaterial(context.Background(), "builtin.mlp", "gpu")
	if err != nil || !reflect.DeepEqual(gpu, other) {
		t.Fatal("finite GPU software source not selected", err)
	}
}

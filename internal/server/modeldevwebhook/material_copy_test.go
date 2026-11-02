package modeldevwebhook_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server/modeldevwebhook"
)

func TestInitCopiesOnlyPinnedBundleIntoRegularReadOnlyFiles(t *testing.T) {
	source, destination := t.TempDir(), filepath.Join(t.TempDir(), "regular")
	files := map[string]string{}
	for _, name := range []string{"builtin.mlp.cpu.json", "builtin.mlp.gpu.json", "builtin.sft_lora.cpu.json", "builtin.sft_lora.gpu.json", "ir/modeldev-mlp-cpu.yaml", "ir/modeldev-mlp-gpu.yaml", "ir/modeldev-sft-lora-cpu.yaml", "ir/modeldev-sft-lora-gpu.yaml"} {
		raw := []byte("pinned:" + name)
		digest := sha256.Sum256(raw)
		files[name] = hex.EncodeToString(digest[:])
		real := filepath.Join(t.TempDir(), "content")
		if err := os.WriteFile(real, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, filepath.Join(source, strings.ReplaceAll(name, "/", "__"))); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(map[string]any{"schema": "ani.modeldev.material-bundle.v1", "files": files})
	digest := sha256.Sum256(raw)
	expected := hex.EncodeToString(digest[:])
	if err := os.WriteFile(filepath.Join(source, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for retry := 0; retry < 2; retry++ {
		if err := modeldevwebhook.CopyMaterials(source, destination, expected); err != nil {
			t.Fatal(err)
		}
	}
	for name := range files {
		info, err := os.Lstat(filepath.Join(destination, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0222 != 0 {
			t.Fatal("runtime material is symlink or writable", err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "manifest.json"), []byte(`{"tampered":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if modeldevwebhook.CopyMaterials(source, filepath.Join(t.TempDir(), "new"), expected) == nil {
		t.Fatal("manifest mutation accepted")
	}
}

func TestPlatformInitCopiesPinnedProjectionWithoutChangingItsBytes(t *testing.T) {
	raw := []byte(`{"schema_version":"ani.modeldev.tenant-environment-platform.v1","material_bundle_digest":"independently-pinned"}`)
	digest := sha256.Sum256(raw)
	expected := hex.EncodeToString(digest[:])
	regular := filepath.Join(t.TempDir(), "platform.json")
	if err := os.WriteFile(regular, raw, 0600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "platform.json")
	if err := os.Symlink(regular, source); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "runtime", "platform.json")
	for retry := 0; retry < 2; retry++ {
		if err := modeldevwebhook.CopyPlatform(source, destination, expected); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Lstat(destination)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0400 {
		t.Fatal("platform is not a read-only regular file", err)
	}
	actual, err := os.ReadFile(destination)
	if err != nil || string(actual) != string(raw) {
		t.Fatal("platform bytes changed", err)
	}
	if err := os.WriteFile(regular, []byte(`{"changed":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if modeldevwebhook.CopyPlatform(source, destination, expected) == nil {
		t.Fatal("changed projected config accepted")
	}
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(regular, destination); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(regular, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if modeldevwebhook.CopyPlatform(source, destination, expected) == nil {
		t.Fatal("symlink runtime target accepted")
	}
}

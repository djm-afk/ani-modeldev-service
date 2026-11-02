package tenantenvironment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func TestEnvironmentFixedImagesCreatePVCResource(t *testing.T) {
	managed := "registry.example.test/platform/step@sha256:" + strings.Repeat("a", 64)
	anonymous := "support.example.test/kfp/driver@sha256:" + strings.Repeat("b", 64)
	p := &Platform{config: Config{RegistryAuthority: "registry.example.test", PlatformProject: "platform", AnonymousImages: []AnonymousImage{{Reference: anonymous, EvidenceSHA256: strings.Repeat("c", 64)}}}}
	// This is the official CreatePVC executor emitted in the fixed pipeline IR.
	base := map[string]any{"deploymentSpec": map[string]any{"executors": map[string]any{
		"exec-createpvc": map[string]any{"container": map[string]any{"image": "argostub/createpvc"}},
		"exec-prepare":   map[string]any{"container": map[string]any{"image": managed}},
		"exec-driver":    map[string]any{"container": map[string]any{"image": anonymous}},
	}}}
	clone := func() map[string]any {
		raw, err := json.Marshal(base)
		if err != nil {
			t.Fatal(err)
		}
		var spec map[string]any
		if err := json.Unmarshal(raw, &spec); err != nil {
			t.Fatal(err)
		}
		return spec
	}
	executors := func(spec map[string]any) map[string]any {
		return spec["deploymentSpec"].(map[string]any)["executors"].(map[string]any)
	}
	for _, wrapped := range []bool{false, true} {
		name := "single-document"
		if wrapped {
			name = "pipeline-spec-wrapper"
		}
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(base)
			if err != nil {
				t.Fatal(err)
			}
			if wrapped {
				raw = append(raw, []byte("\n---\nkubernetes: {}\n")...)
			}
			file := filepath.Join(t.TempDir(), "fixed-ir.yaml")
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			digest := biz.EnvironmentDigest(raw)
			p.config.IRSources = []IRSource{{ProfileID: name, File: file, SHA256: digest}}
			spec, err := p.readIR(name, digest)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.verifyFixedImages(spec); err != nil {
				t.Fatalf("official CreatePVC resource prevented fixed-material verification: %v", err)
			}
		})
	}
	tests := map[string]func(map[string]any) any{
		"marker-with-command": func(spec map[string]any) any {
			executors(spec)["exec-createpvc"].(map[string]any)["container"].(map[string]any)["command"] = []any{"/bin/run"}
			return spec
		},
		"marker-with-args": func(spec map[string]any) any {
			executors(spec)["exec-createpvc"].(map[string]any)["container"].(map[string]any)["args"] = []any{}
			return spec
		},
		"marker-with-executor-field": func(spec map[string]any) any {
			executors(spec)["exec-createpvc"].(map[string]any)["other"] = true
			return spec
		},
		"marker-in-other-executor": func(spec map[string]any) any {
			executors(spec)["exec-other"] = executors(spec)["exec-createpvc"]
			return spec
		},
		"marker-in-nested-spec": func(spec map[string]any) any { return map[string]any{"other": spec} },
		"marker-in-path-like-key": func(spec map[string]any) any {
			return map[string]any{"deploymentSpec/executors/exec-createpvc": executors(spec)["exec-createpvc"]}
		},
		"runtime-image-tag": func(spec map[string]any) any {
			executors(spec)["exec-prepare"].(map[string]any)["container"].(map[string]any)["image"] = "registry.example.test/platform/step:latest"
			return spec
		},
		"runtime-image-other-project": func(spec map[string]any) any {
			executors(spec)["exec-prepare"].(map[string]any)["container"].(map[string]any)["image"] = strings.Replace(managed, "/platform/", "/foreign/", 1)
			return spec
		},
		"runtime-image-short-digest": func(spec map[string]any) any {
			executors(spec)["exec-prepare"].(map[string]any)["container"].(map[string]any)["image"] = managed[:len(managed)-1]
			return spec
		},
		"unapproved-support-image": func(spec map[string]any) any {
			executors(spec)["exec-driver"].(map[string]any)["container"].(map[string]any)["image"] = strings.Replace(anonymous, "driver", "other", 1)
			return spec
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			if err := p.verifyFixedImages(mutate(clone())); err == nil || !strings.Contains(err.Error(), "MANAGED_IMAGE_REGISTRY_MISMATCH") {
				t.Fatalf("expected fixed-image rejection, got %v", err)
			}
		})
	}
}

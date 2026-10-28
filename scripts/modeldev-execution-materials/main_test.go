package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

// These identities are authored only to exercise rendering. They do not name
// installed images, a confirmed live pipeline, or an enabled environment.
func fixture(t *testing.T, id, device string) (facts, string, string) {
	t.Helper()
	entry := "../../training/train_mlp.py"
	if id == "builtin.sft_lora" {
		entry = "../../training/finetuning/finetune_entry.py"
	}
	entrySHA, err := fileHash(entry)
	if err != nil {
		t.Fatal(err)
	}
	mappingSHA, err := fileHash("../../training/finetuning/configuration.py")
	if err != nil {
		t.Fatal(err)
	}
	uuid := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	i := image{ImageID: "img_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ImageVersion: 1, ImageScope: cpup01.ImageScopePlatform, Digest: "registry.example.test/fixture@sha256:" + strings.Repeat("a", 64), EntrySHA256: entrySHA, PythonExecutable: "/actual/image/python"}
	f := facts{SchemaVersion: "ani.modeldev.material-input.v3", ImplementationID: id, DeviceMode: device, TrainingImage: i, Pipeline: pipelineReceipt{Phase: "CONFIRMED", PipelineID: uuid, PipelineVersionID: uuid, PipelineIRSHA256: strings.Repeat("b", 64)}, WorkspaceStorageClass: "test-storage", WorkspaceCapacityBytes: 20 << 30}
	f.ComponentResources = conformance.ComponentResourceFixtures(cpup01.Implementations()[id].Kind)
	if id == "builtin.sft_lora" {
		capabilities := conformance.FinetuningRuntimeCapabilities()
		capabilities.ImageDigest = i.Digest
		capabilities.EntrypointSHA256 = entrySHA
		f.Finetuning = &finetuningFacts{ExportImage: i, VerifyImage: i, MappingSHA256: mappingSHA, RuntimeCapabilities: capabilities}
	}
	return f, entrySHA, mappingSHA
}

func TestFiniteCPULoRAMaterialsUseActualSourcesAndOneDefaultDefinition(t *testing.T) {
	for _, id := range []string{"builtin.mlp", "builtin.sft_lora"} {
		for _, device := range []string{"cpu", "gpu"} {
			t.Run(id+"/"+device, func(t *testing.T) {
				f, entry, mapping := fixture(t, id, device)
				m, err := material(f, entry, mapping)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := m.Canonical()
				if err != nil {
					t.Fatal(err)
				}
				parsed, digest, err := cpup01.ParseMaterial(raw)
				if err != nil || digest != hash(raw) || parsed.Implementation.ParameterContractVersion != cpup01.Implementations()[id].ParameterContractVersion {
					t.Fatal("formal material codec/provenance mismatch", err)
				}
				for _, retired := range []string{`"release_id"`, `"preset_id"`, `"runtime"`, `"resources"`, `"allowed_parameter_sets"`, `"execution_timeout_seconds"`} {
					if bytes.Contains(raw, []byte(retired)) {
						t.Fatal("retired authority entered software material", retired)
					}
				}
				if id == "builtin.mlp" {
					found := false
					for _, a := range m.Program.ArgsTemplate {
						if a.Source == "SEED" {
							found = true
						}
					}
					if !found || m.Program.Command[2] != "/opt/ani-modeldev/training/train_mlp.py" {
						t.Fatal("real MLP entry/all parameters missing")
					}
				} else {
					if len(m.OutputContract.RequiredFiles) != 8 || m.Finetuning.ExportProgram.ImplementationSHA256 != entry || m.Finetuning.MappingSHA256 != mapping {
						t.Fatal("LoRA actual software inventory missing")
					}
					settings, err := managedSettings(device)
					if err != nil || settings != m.Finetuning.ManagedSettings {
						t.Fatal("managed defaults diverged", err)
					}
					mode := "cpu"
					if device == "gpu" {
						mode = "cuda"
					}
					if settings.Device != mode {
						t.Fatal("device not frozen")
					}
				}
			})
		}
	}
}
func TestMaterialCannotPromotePendingPipelineMutableImageOrMismatchedSources(t *testing.T) {
	for _, change := range []func(*facts){func(f *facts) { f.Pipeline.Phase = "PENDING" }, func(f *facts) { f.Pipeline.PipelineIRSHA256 = "" }, func(f *facts) { f.TrainingImage.Digest = "sha256:" + strings.Repeat("a", 64) }, func(f *facts) { f.TrainingImage.Digest = "registry.example.test/fixture:latest" }, func(f *facts) { f.TrainingImage.EntrySHA256 = strings.Repeat("0", 64) }, func(f *facts) { f.Finetuning.MappingSHA256 = strings.Repeat("0", 64) }, func(f *facts) { f.Finetuning.VerifyImage.EntrySHA256 = strings.Repeat("0", 64) }, func(f *facts) { f.WorkspaceCapacityBytes = 0 }, func(f *facts) { f.DeviceMode = "arbitrary" }} {
		f, e, m := fixture(t, "builtin.sft_lora", "cpu")
		change(&f)
		if _, err := material(f, e, m); err == nil {
			t.Fatal("unproven software material rendered")
		}
	}
}
func TestInputFactsRejectUnknownDuplicateNullAndRetiredFields(t *testing.T) {
	f, _, _ := fixture(t, "builtin.mlp", "cpu")
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{append(append([]byte{}, raw...), '\n'), []byte(strings.Replace(string(raw), `"device_mode":"cpu"`, `"device_mode":"gpu","device_mode":"cpu"`, 1)), []byte(strings.Replace(string(raw), `"device_mode":"cpu"`, `"device_mode":null`, 1)), []byte(strings.Replace(string(raw), `"implementation_id":`, `"release_id":"retired","implementation_id":`, 1))} {
		path := filepath.Join(t.TempDir(), "facts.json")
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		_, err := readFacts(path)
		if bytes.Equal(body, append(append([]byte{}, raw...), '\n')) {
			if err != nil {
				t.Fatal("ordinary input whitespace refused", err)
			}
		} else if err == nil {
			t.Fatal("ambiguous facts accepted")
		}
	}
}

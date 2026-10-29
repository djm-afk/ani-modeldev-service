package catalogue_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/catalogue"
)

func TestMaterialRejectsAmbiguousIncompleteAndUnsafeSoftware(t *testing.T) {
	_, raw := materialFixture(t)
	text := string(raw)
	mutate := func(change func(*cpup01.ImplementationMaterial)) []byte {
		t.Helper()
		m := conformance.MaterialV21()
		change(&m)
		if _, err := m.Canonical(); err == nil {
			t.Fatal("invalid semantic material acquired canonical bytes")
		}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	cases := map[string][]byte{
		"unknown":          []byte(strings.Replace(text, `"implementation":`, `"enabled":true,"implementation":`, 1)),
		"retired release":  []byte(strings.Replace(text, `"implementation":`, `"release_id":"11111111-1111-4111-8111-111111111111","implementation":`, 1)),
		"duplicate":        []byte(strings.Replace(text, `"schema_version":`, `"schema_version":"duplicate","schema_version":`, 1)),
		"duplicate nested": []byte(strings.Replace(text, `"version":"1"`, `"version":"1","version":"1"`, 1)),
		"case folded":      []byte(strings.Replace(text, `"implementation":`, `"Implementation":`, 1)),
		"null source":      []byte(strings.Replace(text, `{"literal":"--dataset-manifest"}`, `{"literal":"--dataset-manifest","source":null}`, 1)),
		"omitted boolean":  []byte(strings.Replace(text, `"create_tar_bundle":true,`, "", 1)),
		"newline":          append(append([]byte{}, raw...), '\n'), "trailing object": append(append([]byte{}, raw...), '{', '}'), "leading whitespace": append([]byte(" "), raw...), "BOM": append([]byte{0xef, 0xbb, 0xbf}, raw...),
		"invalid UTF8":           []byte(strings.Replace(text, "/workspace/work/train", "/workspace/work/tr\xffain", 1)),
		"unpaired surrogate":     []byte(strings.Replace(text, "/workspace/work/train", `/workspace/work/tr\ud800ain`, 1)),
		"unknown implementation": mutate(func(m *cpup01.ImplementationMaterial) { m.Implementation.ImplementationID = "builtin.unknown" }),
		"wrong contract":         mutate(func(m *cpup01.ImplementationMaterial) { m.Implementation.ParameterContractVersion = "unknown" }),
		"unknown pipeline":       mutate(func(m *cpup01.ImplementationMaterial) { m.Implementation.PipelineVersionID = "UNKNOWN" }),
		"zero pipeline": mutate(func(m *cpup01.ImplementationMaterial) {
			m.Implementation.PipelineID = "00000000-0000-0000-0000-000000000000"
		}),
		"uppercase UUID": mutate(func(m *cpup01.ImplementationMaterial) {
			m.Implementation.PipelineVersionID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
		}),
		"IR absent":         mutate(func(m *cpup01.ImplementationMaterial) { m.Implementation.PipelineIRSHA256 = "" }),
		"invalid scope":     mutate(func(m *cpup01.ImplementationMaterial) { m.Training.DefaultImage.Scope = "public" }),
		"shell":             mutate(func(m *cpup01.ImplementationMaterial) { m.Training.Command = []string{"sh", "-c", "train"} }),
		"arbitrary source":  mutate(func(m *cpup01.ImplementationMaterial) { m.Training.ArgsTemplate[1].Source = "USER_SCRIPT" }),
		"mixed argument":    mutate(func(m *cpup01.ImplementationMaterial) { m.Training.ArgsTemplate[3].Literal = "/arbitrary" }),
		"empty command":     mutate(func(m *cpup01.ImplementationMaterial) { m.Training.Command = nil }),
		"unbound storage":   mutate(func(m *cpup01.ImplementationMaterial) { m.Workspace.StorageClass = "NOT_READY" }),
		"overlap workspace": mutate(func(m *cpup01.ImplementationMaterial) { m.Workspace.TrainingSubpath = "inputs/training" }),
		"wrong output role": mutate(func(m *cpup01.ImplementationMaterial) { m.OutputContract.RequiredFiles[0].Role = "CHECKPOINT" }),
		"oversize":          []byte(strings.Repeat("x", cpup01.MaxMaterialBytes+1)),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			m, digest, err := cpup01.ParseMaterial(raw)
			if !errors.Is(err, cpup01.ErrInvalidArgument) || digest != "" || !reflect.DeepEqual(m, cpup01.ImplementationMaterial{}) {
				t.Fatal("invalid material acquired fixed provenance", err)
			}
			directory := t.TempDir()
			writeMaterial(t, directory, "builtin.mlp", "cpu", raw)
			got, err := catalogue.ReadImplementationMaterial(context.Background(), directory, "builtin.mlp", "cpu")
			if !errors.Is(err, catalogue.ErrInvalidMaterial) || !reflect.DeepEqual(got, cpup01.ImplementationMaterial{}) {
				t.Fatal("reader accepted invalid fixed bytes", err)
			}
		})
	}
}

func TestReadMaterialRejectsWrongImplementationUnsafeFilesAndPaths(t *testing.T) {
	_, raw := materialFixture(t)
	for _, kind := range []string{"symlink", "hardlink", "directory", "fifo", "empty", "oversize", "wrong implementation"} {
		t.Run(kind, func(t *testing.T) {
			directory := t.TempDir()
			name := filepath.Join(directory, "builtin.mlp.cpu.json")
			switch kind {
			case "symlink", "hardlink":
				target := filepath.Join(t.TempDir(), "protected.json")
				if err := os.WriteFile(target, raw, 0600); err != nil {
					t.Fatal(err)
				}
				var err error
				if kind == "symlink" {
					err = os.Symlink(target, name)
				} else {
					err = os.Link(target, name)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(name, 0700); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(name, 0600); err != nil {
					t.Fatal(err)
				}
			case "empty":
				writeMaterial(t, directory, "builtin.mlp", "cpu", nil)
			case "oversize":
				writeMaterial(t, directory, "builtin.mlp", "cpu", []byte(strings.Repeat("x", cpup01.MaxMaterialBytes+1)))
			case "wrong implementation":
				fine := conformance.FinetuningMaterialV21()
				b, err := fine.Canonical()
				if err != nil {
					t.Fatal(err)
				}
				writeMaterial(t, directory, "builtin.mlp", "cpu", b)
			}
			got, err := catalogue.ReadImplementationMaterial(context.Background(), directory, "builtin.mlp", "cpu")
			if err == nil || !reflect.DeepEqual(got, cpup01.ImplementationMaterial{}) {
				t.Fatal("unsafe material acquired contract")
			}
		})
	}
	directory := t.TempDir()
	writeMaterial(t, directory, "builtin.mlp", "cpu", raw)
	for _, test := range []struct{ directory, id, mode string }{{"relative", "builtin.mlp", "cpu"}, {directory + "/../" + filepath.Base(directory), "builtin.mlp", "cpu"}, {directory, "../builtin.mlp", "cpu"}, {directory, "BUILTIN.MLP", "cpu"}, {directory, "builtin.mlp", "../cpu"}, {directory, "builtin.mlp", "CPU"}, {directory, "builtin.other", "gpu"}} {
		got, err := catalogue.ReadImplementationMaterial(context.Background(), test.directory, test.id, test.mode)
		if !errors.Is(err, cpup01.ErrInvalidArgument) || !reflect.DeepEqual(got, cpup01.ImplementationMaterial{}) {
			t.Fatal("unbounded selection accepted", err)
		}
	}
	if _, err := catalogue.ReadImplementationMaterial(context.Background(), directory, "builtin.mlp", "gpu"); !errors.Is(err, catalogue.ErrMaterialNotFound) {
		t.Fatal("missing finite software fabricated", err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(directory, alias); err != nil {
		t.Fatal(err)
	}
	if err := catalogue.NewReader(alias).Check(context.Background()); !errors.Is(err, catalogue.ErrCatalogueUnavailable) {
		t.Fatal("symlink catalogue root accepted", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := catalogue.ReadImplementationMaterial(ctx, directory, "builtin.mlp", "cpu"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	if _, err := catalogue.NewReader(directory).ReadImplementationMaterial(context.Background(), "builtin.mlp", "gpu"); !errors.Is(err, biz.ErrAdmissionEnvironmentNotReady) {
		t.Fatal("safe unavailable mapping lost", err)
	}
}

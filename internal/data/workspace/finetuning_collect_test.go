package workspace_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/workspace"
)

// These synthetic byte files test real collection, disk capture, TLS SDK
// upload and independent byte/archive verification. They are not LoRA evidence.
func TestFinetuningCollectPublishReadsFrozenHFBytesAndOnlyPackagesMergedMembers(t *testing.T) {
	directory, execution, contents := finetuningOutputWorkspace(t)
	output, err := workspace.Collect(context.Background(), directory, execution)
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Files) != len(contents) || output.StorageState != "WORKSPACE_ONLY" {
		t.Fatal("incomplete output inventory")
	}
	store, client := newPipelineS3(t, execution, nil)
	candidate, err := workspace.UploadOutput(context.Background(), client, execution, directory, output)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Bundle == nil || candidate.ID != "" || candidate.ReceiptID != "" || !candidate.VerifiedAt.IsZero() {
		t.Fatal("uploader lost bundle or forged publication receipt")
	}
	verifier := objectstore.NewVerifier(client, execution.Snapshot.PublicationScope.StorageConnectionID, 3<<30)
	for _, file := range candidate.Files {
		if !bytes.Equal(pipelineGet(t, client, file.Object), contents[file.File.RelativePath]) {
			t.Fatal("published file differs from collector bytes")
		}
		if _, err := verifier.Verify(context.Background(), execution.Snapshot.PublicationScope, file.Object); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := verifier.VerifyFinetuningOutputBundle(context.Background(), execution.Snapshot.PublicationScope, *candidate.Bundle, output.Files, execution.Snapshot.OutputContract); err != nil {
		t.Fatal(err)
	}
	archive := tar.NewReader(bytes.NewReader(pipelineGet(t, client, *candidate.Bundle)))
	seen := map[string]bool{}
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(archive)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, contents["export/merged/"+header.Name]) || header.Typeflag != tar.TypeReg || header.Format != tar.FormatUSTAR || seen[header.Name] || strings.HasPrefix(header.Name, "export/") {
			t.Fatalf("wrong main-model member %s", header.Name)
		}
		seen[header.Name] = true
	}
	if len(seen) != 4 {
		t.Fatal("HF file inventory lost")
	}
	before, err := os.Stat(filepath.Join(directory, "publication/spool/bundle.tar"))
	if err != nil {
		t.Fatal(err)
	}
	writes := store.writes()
	again, err := workspace.UploadOutput(context.Background(), client, execution, directory, output)
	if err != nil || !reflect.DeepEqual(again, candidate) || store.writes() != writes {
		t.Fatalf("publication retry changed original objects: %v", err)
	}
	after, err := os.Stat(filepath.Join(directory, "publication/spool/bundle.tar"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("retry repackaged the original durable model tar")
	}
	entries, err := os.ReadDir(filepath.Join(directory, "publication/spool"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "bundle.tar" {
		t.Fatal("spool leaked named capture temporaries")
	}
}

func TestFinetuningCollectRejectsInventoryAndActualReportConflicts(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, string, *biz.Execution, map[string][]byte)
	}{
		{"missing HF file", func(t *testing.T, d string, _ *biz.Execution, _ map[string][]byte) {
			mustRemove(t, filepath.Join(d, "export/merged/tokenizer.json"))
		}},
		{"unapproved extra HF file", func(t *testing.T, d string, _ *biz.Execution, _ map[string][]byte) {
			writeFile(t, filepath.Join(d, "export/merged/extra.bin"), []byte("extra"))
		}},
		{"extra empty structure", func(t *testing.T, d string, _ *biz.Execution, _ map[string][]byte) {
			if os.Mkdir(filepath.Join(d, "export/merged/extra"), 0700) != nil {
				t.Fatal("mkdir")
			}
		}},
		{"ancestor symlink", func(t *testing.T, d string, _ *biz.Execution, _ map[string][]byte) {
			if os.Rename(filepath.Join(d, "export/merged"), filepath.Join(d, "original-merged")) != nil || os.Symlink("../original-merged", filepath.Join(d, "export/merged")) != nil {
				t.Fatal("symlink")
			}
		}},
		{"hard-linked weight", func(t *testing.T, d string, _ *biz.Execution, _ map[string][]byte) {
			if os.Link(filepath.Join(d, "export/merged/model.safetensors"), filepath.Join(d, "weight-alias")) != nil {
				t.Fatal("hardlink")
			}
		}},
		{"forged optimizer fact", func(t *testing.T, d string, _ *biz.Execution, c map[string][]byte) {
			replaceReport(t, d, c, "reports/train/summary.json", "optimizer_steps", 19)
		}},
		{"forged samples", func(t *testing.T, d string, _ *biz.Execution, c map[string][]byte) {
			replaceReport(t, d, c, "reports/train/summary.json", "samples", 63)
		}},
		{"other parameter consumption", func(t *testing.T, d string, _ *biz.Execution, c map[string][]byte) {
			replaceReport(t, d, c, "reports/train/summary.json", "effective_configuration_sha256", strings.Repeat("0", 64))
		}},
		{"other input", func(t *testing.T, d string, _ *biz.Execution, c map[string][]byte) {
			replaceReport(t, d, c, "reports/train/summary.json", "input_sha256", strings.Repeat("0", 64))
		}},
		{"wrong merged digest", func(t *testing.T, d string, _ *biz.Execution, c map[string][]byte) {
			replaceReport(t, d, c, "reports/verify/verify.json", "merged_manifest_sha256", strings.Repeat("0", 64))
		}},
		{"unconfirmed verify", func(t *testing.T, d string, _ *biz.Execution, c map[string][]byte) {
			replaceReport(t, d, c, "reports/verify/verify.json", "finite_logits", false)
		}},
		{"dynamic file changed without updated manifest", func(t *testing.T, d string, _ *biz.Execution, _ map[string][]byte) {
			writeFile(t, filepath.Join(d, "export/merged/model.safetensors"), []byte("other dynamic weights"))
		}},
		{"per-file bound", func(t *testing.T, _ string, e *biz.Execution, _ map[string][]byte) {
			e.Snapshot.OutputContract.RequiredFiles[0].MaxSizeBytes = 1
			refreshSpec(t, e)
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			d, e, c := finetuningOutputWorkspace(t)
			test.change(t, d, &e, c)
			got, err := workspace.Collect(context.Background(), d, e)
			if !errors.Is(err, biz.ErrInvalidWorkspaceOutput) || len(got.Files) != 0 {
				t.Fatalf("invalid output produced inventory: %v", err)
			}
		})
	}
}

func TestFinetuningPublishRejectsLocalMutationAndSpoolBudgetBeforeAnyPUT(t *testing.T) {
	for _, mode := range []string{"nested symlink", "changed after collection", "spool symlink", "spool budget", "tar budget", "original tar changed"} {
		t.Run(mode, func(t *testing.T) {
			d, e, _ := finetuningOutputWorkspace(t)
			if mode == "spool budget" {
				e.Snapshot.Finetuning.Budgets.MaxBaseModelBytes = 1
				e.Snapshot.Finetuning.BaseModel.Object.SizeBytes = 1
				e.Snapshot.Finetuning.Budgets.MaxSpoolBytes = 1
				refreshSpec(t, &e)
			}
			if mode == "tar budget" {
				*e.Snapshot.OutputContract.MaxTarBytes = 1024
				refreshSpec(t, &e)
			}
			output, err := workspace.Collect(context.Background(), d, e)
			if err != nil {
				t.Fatal(err)
			}
			store, client := newPipelineS3(t, e, nil)
			if mode == "nested symlink" {
				if os.Rename(filepath.Join(d, "export/merged"), filepath.Join(d, "original-merged")) != nil || os.Symlink("../original-merged", filepath.Join(d, "export/merged")) != nil {
					t.Fatal("symlink")
				}
			}
			if mode == "changed after collection" {
				writeFile(t, filepath.Join(d, "export/merged/model.safetensors"), []byte("mutated"))
			}
			if mode == "spool symlink" {
				if os.MkdirAll(filepath.Join(d, "publication"), 0700) != nil {
					t.Fatal("mkdir")
				}
				if os.Symlink("/dev/shm", filepath.Join(d, "publication/spool")) != nil {
					t.Fatal("spool symlink")
				}
			}
			if mode == "original tar changed" {
				if _, err := workspace.UploadOutput(context.Background(), client, e, d, output); err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(d, "publication/spool/bundle.tar"), []byte("corrupt original tar"))
			}
			writes := store.writes()
			if _, err := workspace.UploadOutput(context.Background(), client, e, d, output); !errors.Is(err, biz.ErrInvalidWorkspaceOutput) || store.writes() != writes {
				t.Fatalf("unsafe capture reached PUT or repaired original: %v", err)
			}
		})
	}
}

func TestFinetuningMainTarVerificationRejectsUnapprovedStructureWithValidOuterSHA(t *testing.T) {
	for _, mode := range []string{"wrong prefix", "extra platform manifest", "duplicate member", "PAX header", "symlink member", "wrong member SHA", "missing member", "missing end markers", "nonzero tail", "zero padding"} {
		t.Run(mode, func(t *testing.T) {
			d, e, contents := finetuningOutputWorkspace(t)
			output, err := workspace.Collect(context.Background(), d, e)
			if err != nil {
				t.Fatal(err)
			}
			store, client := newPipelineS3(t, e, nil)
			candidate, err := workspace.UploadOutput(context.Background(), client, e, d, output)
			if err != nil {
				t.Fatal(err)
			}
			var archive bytes.Buffer
			writer := tar.NewWriter(&archive)
			members := []cpup01.OutputFile{}
			for _, file := range output.Files {
				if strings.HasPrefix(file.RelativePath, "export/merged/") {
					members = append(members, file)
				}
			}
			for index, file := range members {
				if mode == "missing member" && index == len(members)-1 {
					continue
				}
				data := append([]byte{}, contents[file.RelativePath]...)
				header := &tar.Header{Name: strings.TrimPrefix(file.RelativePath, "export/merged/"), Mode: 0600, Size: file.SizeBytes, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
				if index == 0 {
					switch mode {
					case "wrong prefix":
						header.Name = file.RelativePath
					case "PAX header":
						header.Format = tar.FormatPAX
						header.PAXRecords = map[string]string{"comment": "unapproved"}
					case "symlink member":
						header.Typeflag = tar.TypeSymlink
						header.Linkname = "outside"
						header.Size = 0
						data = nil
					case "wrong member SHA":
						data[0] ^= 1
					}
				}
				if writer.WriteHeader(header) != nil {
					t.Fatal("header")
				}
				if _, err := writer.Write(data); err != nil {
					t.Fatal(err)
				}
				if mode == "duplicate member" && index == 0 {
					if writer.WriteHeader(header) != nil {
						t.Fatal("duplicate header")
					}
					if _, err := writer.Write(data); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "extra platform manifest" {
				if writer.WriteHeader(&tar.Header{Name: "output-manifest.json", Mode: 0600, Size: 1, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}) != nil {
					t.Fatal("header")
				}
				if _, err := writer.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
			}
			if writer.Close() != nil {
				t.Fatal("archive close")
			}
			payload := archive.Bytes()
			if mode == "missing end markers" {
				payload = payload[:len(payload)-1024]
			}
			if mode == "nonzero tail" {
				payload = append(payload, 1)
			}
			if mode == "zero padding" {
				payload = append(payload, make([]byte, 4096)...)
			}
			object := *candidate.Bundle
			object.SizeBytes = int64(len(payload))
			object.SHA256 = pipelineDigest(payload)
			store.corrupt(object, payload)
			verified, err := objectstore.NewVerifier(client, e.Snapshot.PublicationScope.StorageConnectionID, 3<<30).VerifyFinetuningOutputBundle(context.Background(), e.Snapshot.PublicationScope, object, output.Files, e.Snapshot.OutputContract)
			if mode == "zero padding" {
				if err != nil || verified.VerifiedAt.IsZero() {
					t.Fatalf("standard zero padding rejected: %v", err)
				}
			} else if !errors.Is(err, biz.ErrObjectVerification) || !verified.VerifiedAt.IsZero() {
				t.Fatalf("invalid inner archive accepted despite valid outer hash: %v", err)
			}
		})
	}
}

func TestFinetuningCollectorAcceptsDifferentActualWeightsUnderSamePathContract(t *testing.T) {
	d, e, contents := finetuningOutputWorkspace(t)
	first, err := workspace.Collect(context.Background(), d, e)
	if err != nil {
		t.Fatal(err)
	}
	name := "export/merged/model.safetensors"
	contents[name] = []byte("different model bytes from another valid run")
	writeFile(t, filepath.Join(d, name), contents[name])
	var manifest map[string]any
	if json.Unmarshal(contents["reports/export/merged_manifest.json"], &manifest) != nil {
		t.Fatal("manifest")
	}
	for _, value := range manifest["files"].([]any) {
		file := value.(map[string]any)
		if file["path"] == "model.safetensors" {
			file["bytes"] = stringBytes(contents[name])
			file["sha256"] = pipelineDigest(contents[name])
		}
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	contents["reports/export/merged_manifest.json"] = raw
	writeFile(t, filepath.Join(d, "reports/export/merged_manifest.json"), raw)
	replaceReport(t, d, contents, "reports/verify/verify.json", "merged_manifest_sha256", pipelineDigest(raw))
	second, err := workspace.Collect(context.Background(), d, e)
	if err != nil {
		t.Fatalf("structure contract incorrectly fixed initial weights SHA: %v", err)
	}
	if first.ManifestSHA256 == second.ManifestSHA256 {
		t.Fatal("different actual weights produced same output content identity")
	}
}

func finetuningOutputWorkspace(t *testing.T) (string, biz.Execution, map[string][]byte) {
	t.Helper()
	snapshot := conformance.FinetuningSnapshotV1()
	intent := conformance.FinetuningIntentV1()
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	e := biz.Execution{Admission: biz.Admission{TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:user:42", OperationID: "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff", ExecutionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", Intent: intent, IntentHash: intentHash, Snapshot: snapshot, AcceptedAt: snapshot.DeadlineAt.Add(-time.Hour)}}
	refreshSpec(t, &e)
	contents := map[string][]byte{}
	for _, file := range snapshot.OutputContract.RequiredFiles {
		if file.Role == "HF_MODEL" || file.Role == "LORA_ADAPTER" {
			contents[file.RelativePath] = []byte("synthetic " + file.RelativePath)
		}
	}
	baseHash, err := snapshot.Finetuning.BaseModel.BaseManifestSHA256()
	if err != nil {
		t.Fatal(err)
	}
	configuration := cpup01.FinetuningConfiguration{ParameterContractVersion: cpup01.FinetuningParameterContractVersion, Parameters: snapshot.Program.ResolvedParameters, ManagedSettings: snapshot.Finetuning.ManagedSettings}
	effective, err := configuration.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(snapshot.Finetuning.ManagedSettings)
	members := func(prefix string) []map[string]string {
		names := []string{}
		for name := range contents {
			if strings.HasPrefix(name, prefix) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		files := []map[string]string{}
		for _, name := range names {
			files = append(files, map[string]string{"path": strings.TrimPrefix(name, prefix), "bytes": stringBytes(contents[name]), "sha256": pipelineDigest(contents[name])})
		}
		return files
	}
	encode := func(name string, value any) {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		contents[name] = raw
	}
	var metrics bytes.Buffer
	for step := int64(1); step <= 20; step++ {
		line, _ := json.Marshal(map[string]any{"schema": "ani.metric.v1", "step": step, "name": "train.loss", "value": 0.125, "rank": 0, "timestamp": "2026-10-14T00:00:00Z"})
		metrics.Write(line)
		metrics.WriteByte('\n')
	}
	contents["reports/train/metrics.jsonl"] = metrics.Bytes()
	encode("reports/train/summary.json", map[string]any{"schema": "ani.finetune.summary.v1", "status": "SUCCEEDED", "samples": 64, "source_samples": 64, "optimizer_steps": 20, "steps": 20, "metric_samples": 20, "input_sha256": snapshot.Input.Object.SHA256, "input_bytes": stringNumber(snapshot.Input.Object.SizeBytes), "base_manifest_sha256": baseHash, "parameter_contract_version": cpup01.FinetuningParameterContractVersion, "resolved_configuration_sha256": snapshot.Finetuning.ResolvedConfigurationSHA256, "effective_configuration": json.RawMessage(effective), "effective_configuration_sha256": snapshot.Finetuning.ResolvedConfigurationSHA256, "learning_rate": "0.0001", "max_steps": 20, "recipe_sha256": pipelineDigest(settings), "adapter_files": members("training/adapter/")})
	encode("reports/train/adapter_manifest.json", map[string]any{"schema": "ani.finetune.adapter-manifest.v1", "input_sha256": snapshot.Input.Object.SHA256, "base_manifest_sha256": baseHash, "resolved_configuration_sha256": snapshot.Finetuning.ResolvedConfigurationSHA256, "files": members("training/adapter/")})
	encode("reports/export/merged_manifest.json", map[string]any{"schema": "ani.finetune.merged-manifest.v1", "resolved_configuration_sha256": snapshot.Finetuning.ResolvedConfigurationSHA256, "dtype": "float32", "files": members("export/merged/")})
	encode("reports/export/export.json", map[string]any{"schema": "ani.finetune.export.v1", "status": "SUCCEEDED", "dtype": "float32"})
	encode("reports/verify/verify.json", map[string]any{"schema": "ani.finetune.verify.v1", "status": "PASSED", "dtype": "float32", "device": "cpu", "resolved_configuration_sha256": snapshot.Finetuning.ResolvedConfigurationSHA256, "merged_manifest_sha256": pipelineDigest(contents["reports/export/merged_manifest.json"]), "finite_logits": true, "generated_token_ids": []int64{1, 2}})
	// Fedora /tmp is tmpfs. Use this checkout's private disk-backed work area
	// so the positive path exercises the production disk-spool requirement.
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Clean(filepath.Join(working, "../../..", ".work/finetuning-tests"))
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(parent, "workspace-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	for name, data := range contents {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(directory, name)), 0700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(directory, name), data)
	}
	return directory, e, contents
}

func replaceReport(t *testing.T, directory string, contents map[string][]byte, name, key string, value any) {
	t.Helper()
	var report map[string]any
	if json.Unmarshal(contents[name], &report) != nil {
		t.Fatal("report")
	}
	report[key] = value
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	contents[name] = raw
	writeFile(t, filepath.Join(directory, name), raw)
}

func stringBytes(value []byte) string { return stringNumber(int64(len(value))) }
func stringNumber(value int64) string { return strconv.FormatInt(value, 10) }

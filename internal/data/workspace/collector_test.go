package workspace_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/workspace"
)

func TestCollectHashesActualRegisteredBytesWithoutTrustingCandidate(t *testing.T) {
	directory, execution, contents := preparedWorkspace(t)
	// Deliberately forged candidate metadata cannot supply a publication or
	// override hashes. These small byte fixtures test collection, not PyTorch.
	writeFile(t, filepath.Join(directory, "result.json"), []byte(`{"storage_state":"PUBLISHED","sha256":"forged"}`))
	got, err := workspace.Collect(context.Background(), directory, execution)
	if err != nil {
		t.Fatalf("collect valid workspace: %v", err)
	}
	if got.ExecutionID != execution.ExecutionID || got.ExecutionSpecHash != execution.SpecHash || got.StorageState != "WORKSPACE_ONLY" {
		t.Fatalf("collector lost the execution binding or claimed publication: %+v", got)
	}
	want := make([]biz.CollectedFile, 0, len(contents))
	for _, required := range execution.Snapshot.OutputContract.RequiredFiles {
		data := contents[required.RelativePath]
		digest := sha256.Sum256(data)
		want = append(want, biz.CollectedFile{RelativePath: required.RelativePath, Role: required.Role, InTar: required.InTar, SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(digest[:])})
	}
	sort.Slice(want, func(i, j int) bool { return want[i].RelativePath < want[j].RelativePath })
	if !reflect.DeepEqual(got.Files, want) {
		t.Fatalf("actual byte inventory mismatch: got=%+v want=%+v", got.Files, want)
	}
	var manifest struct {
		ExecutionID       string              `json:"execution_id"`
		ExecutionSpecHash string              `json:"execution_spec_hash"`
		StorageState      string              `json:"storage_state"`
		Files             []biz.CollectedFile `json:"files"`
	}
	if err := json.Unmarshal(got.Manifest, &manifest); err != nil {
		t.Fatalf("collector must return a usable manifest: %v", err)
	}
	if manifest.ExecutionID != execution.ExecutionID || manifest.ExecutionSpecHash != execution.SpecHash || manifest.StorageState != "WORKSPACE_ONLY" || !reflect.DeepEqual(manifest.Files, want) {
		t.Fatal("collector manifest did not bind the bytes it actually read")
	}
	manifestDigest := sha256.Sum256(got.Manifest)
	if got.ManifestSHA256 != hex.EncodeToString(manifestDigest[:]) {
		t.Fatal("collector manifest SHA mismatch")
	}
}

func TestCollectRejectsMissingUnsafeOrUnboundedOutput(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, string, *biz.Execution)
	}{
		{"missing checkpoint", func(t *testing.T, directory string, _ *biz.Execution) {
			mustRemove(t, filepath.Join(directory, "model.pt"))
		}},
		{"empty checkpoint", func(t *testing.T, directory string, _ *biz.Execution) {
			writeFile(t, filepath.Join(directory, "model.pt"), nil)
		}},
		{"directory instead of checkpoint", func(t *testing.T, directory string, _ *biz.Execution) {
			path := filepath.Join(directory, "model.pt")
			mustRemove(t, path)
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink checkpoint", func(t *testing.T, directory string, _ *biz.Execution) {
			path := filepath.Join(directory, "model.pt")
			mustRemove(t, path)
			outside := filepath.Join(t.TempDir(), "outside.pt")
			writeFile(t, outside, []byte("outside execution"))
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"hardlink checkpoint", func(t *testing.T, directory string, _ *biz.Execution) {
			if err := os.Link(filepath.Join(directory, "model.pt"), filepath.Join(t.TempDir(), "alias.pt")); err != nil {
				t.Fatal(err)
			}
		}},
		{"frozen content limit", func(t *testing.T, d string, execution *biz.Execution) {
			execution.Snapshot.Limits.MaxContentBytes = execution.Snapshot.Input.Object.SizeBytes
			writeFile(t, filepath.Join(d, "model.pt"), bytes.Repeat([]byte("w"), int(execution.Snapshot.Limits.MaxContentBytes)))
			refreshSpec(t, execution)
		}},
		{"tampered frozen hash", func(_ *testing.T, _ string, execution *biz.Execution) { execution.SpecHash = strings.Repeat("0", 64) }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			directory, execution, _ := preparedWorkspace(t)
			testCase.change(t, directory, &execution)
			got, err := workspace.Collect(context.Background(), directory, execution)
			if !errors.Is(err, biz.ErrInvalidWorkspaceOutput) || !reflect.DeepEqual(got, biz.CollectedOutput{}) {
				t.Fatalf("unsafe output must fail without partial file inventory: got=%+v err=%v", got, err)
			}
		})
	}
}

func TestCollectRejectsSymlinkDirectoryAndCancelledContext(t *testing.T) {
	directory, execution, _ := preparedWorkspace(t)
	alias := filepath.Join(t.TempDir(), "training")
	if err := os.Symlink(directory, alias); err != nil {
		t.Fatal(err)
	}
	if got, err := workspace.Collect(context.Background(), alias, execution); !errors.Is(err, biz.ErrInvalidWorkspaceOutput) || len(got.Files) != 0 {
		t.Fatalf("symlink root: %+v %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := workspace.Collect(ctx, directory, execution); !errors.Is(err, context.Canceled) || len(got.Files) != 0 {
		t.Fatalf("cancelled read: %+v %v", got, err)
	}
}

func preparedWorkspace(t *testing.T) (string, biz.Execution, map[string][]byte) {
	t.Helper()
	snapshot := conformance.SnapshotV1()
	intent := conformance.IntentV1()
	intent.Name = "collector-actual-bytes"
	snapshot.Input.RowCount = 1024
	snapshot.Input.Object.SizeBytes = int64(len(pipelineCSVFixture()))
	snapshot.Input.Object.SHA256 = pipelineDigest(pipelineCSVFixture())
	snapshot.Input.PreparedDataset = cpup01.PreparedDatasetFacts{Format: "CPU_CSV", TransformID: "csv-identity-v3", SizeBytes: snapshot.Input.Object.SizeBytes, SHA256: snapshot.Input.Object.SHA256, RowCount: snapshot.Input.RowCount}
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	execution := biz.Execution{Admission: biz.Admission{
		TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:user:42",
		OperationID: "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff", ExecutionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		Intent: intent, IntentHash: intentHash, Snapshot: snapshot, AcceptedAt: snapshot.Timing.AcceptedAt,
	}}
	refreshSpec(t, &execution)
	directory := t.TempDir()
	contents := mlpOutputFixture(t, execution)
	for name, data := range contents {
		writeFile(t, filepath.Join(directory, name), data)
	}
	return directory, execution, contents
}

func refreshSpec(t *testing.T, execution *biz.Execution) {
	t.Helper()
	execution.Snapshot.IdentityRef = cpup01.IdentityRef{TenantID: execution.TenantID, Actor: execution.Actor, ExecutionID: execution.ExecutionID, OperationID: execution.OperationID, IntentHash: execution.IntentHash}
	var err error
	execution.Snapshot.RuntimePlan, err = biz.CompileRuntimePlan(execution.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	execution.SpecHash, err = execution.Snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := execution.CanonicalPayloads(); err != nil {
		t.Fatalf("invalid admission fixture: %v", err)
	}
}

func writeFile(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func mustRemove(t *testing.T, name string) {
	t.Helper()
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
}

func mlpOutputFixture(t *testing.T, execution biz.Execution) map[string][]byte {
	t.Helper()
	parameters := map[string]string{}
	for _, parameter := range execution.Snapshot.Program.ResolvedParameters {
		parameters[parameter.Name] = parameter.Value
	}
	epochs, _ := strconv.ParseInt(parameters["epochs"], 10, 64)
	batch, _ := strconv.ParseInt(parameters["batch_size"], 10, 64)
	seed, _ := strconv.ParseUint(parameters["seed"], 10, 32)
	samples := int64(execution.Snapshot.Input.RowCount)
	steps := epochs * ((samples-1)/batch + 1)
	device := "cpu"
	if execution.Snapshot.ComputeOrigin.DeviceMode == "gpu" {
		device = "cuda"
	}
	summary, err := json.Marshal(map[string]any{"status": "SUCCEEDED", "error": nil,
		"samples": samples, "epochs": epochs, "batch_size": batch, "seed": seed,
		"learning_rate": parameters["learning_rate"], "steps": steps, "world_size": 1,
		"device": device, "torch_version": "test-fixture", "input_sha256": execution.Snapshot.Input.Object.SHA256,
		"input_bytes": strconv.FormatInt(execution.Snapshot.Input.Object.SizeBytes, 10), "initial_loss": 0.5, "final_loss": 0.75})
	if err != nil {
		t.Fatal(err)
	}
	var metrics strings.Builder
	for step := int64(1); step <= steps; step++ {
		raw, err := json.Marshal(map[string]any{"schema": "ani.metric.v1", "step": step, "name": "train.loss", "value": 0.75, "rank": 0, "timestamp": "2026-10-22T00:00:01Z"})
		if err != nil {
			t.Fatal(err)
		}
		metrics.Write(raw)
		metrics.WriteByte('\n')
	}
	return map[string][]byte{"model.pt": []byte("module checkpoint fixture bytes\x00\xff"),
		"model_config.json": []byte(`{"architecture":"mlp-16-32-2","input_dim":16,"hidden_dim":32,"output_dim":2,"dtype":"float32"}`),
		"metrics.jsonl":     []byte(metrics.String()), "summary.json": summary}
}

func TestCollectMLPUsesFrozenParametersAndIncludesPartialBatch(t *testing.T) {
	directory, execution, _ := preparedWorkspace(t)
	rate, epochs, batch, seed := "1000", int64(2), int64(6), uint32(7)
	execution.Intent.MLP = &cpup01.MLPParameters{LearningRate: &rate, Epochs: &epochs, BatchSize: &batch, Seed: &seed}
	_, execution.IntentHash, _ = cpup01.CanonicalIntent(execution.Intent)
	execution.Snapshot.Program.ResolvedParameters, _ = execution.Intent.ResolveParameters()
	execution.Snapshot.Input.RowCount = 17
	rows := strings.Split(string(pipelineCSVFixture()), "\n")
	input := []byte(strings.Join(rows[:18], "\n") + "\n")
	execution.Snapshot.Input.Object.SizeBytes = int64(len(input))
	execution.Snapshot.Input.Object.SHA256 = pipelineDigest(input)
	execution.Snapshot.Input.PreparedDataset = cpup01.PreparedDatasetFacts{Format: "CPU_CSV", TransformID: "csv-identity-v3", SizeBytes: int64(len(input)), SHA256: pipelineDigest(input), RowCount: 17}
	refreshSpec(t, &execution)
	contents := mlpOutputFixture(t, execution)
	for name, data := range contents {
		writeFile(t, filepath.Join(directory, name), data)
	}
	if _, err := workspace.Collect(context.Background(), directory, execution); err != nil {
		t.Fatalf("valid 17-row/6-batch/2-epoch finite non-decreasing result: %v", err)
	}
	var summary map[string]any
	if json.Unmarshal(contents["summary.json"], &summary) != nil || summary["steps"] != float64(6) {
		t.Fatal("fixture must exercise six real-step observations")
	}
}

func TestCollectMLPRejectsForgedOrMissingWorkloadFacts(t *testing.T) {
	for _, test := range []struct {
		name   string
		path   string
		change func([]byte) []byte
	}{
		{"steps", "summary.json", func(raw []byte) []byte { return changeMLPReport(t, raw, "steps", 47) }},
		{"samples", "summary.json", func(raw []byte) []byte { return changeMLPReport(t, raw, "samples", 1023) }},
		{"seed", "summary.json", func(raw []byte) []byte { return changeMLPReport(t, raw, "seed", 7) }},
		{"missing initial loss", "summary.json", func(raw []byte) []byte { return changeMLPReport(t, raw, "initial_loss", nil) }},
		{"wrong device", "summary.json", func(raw []byte) []byte { return changeMLPReport(t, raw, "device", "cuda") }},
		{"wrong input", "summary.json", func(raw []byte) []byte { return changeMLPReport(t, raw, "input_sha256", strings.Repeat("0", 64)) }},
		{"failed status", "summary.json", func(raw []byte) []byte { return changeMLPReport(t, raw, "status", "FAILED") }},
		{"metric gap", "metrics.jsonl", func(raw []byte) []byte { return []byte(strings.Replace(string(raw), `"step":1,`, `"step":2,`, 1)) }},
		{"metric schema", "metrics.jsonl", func(raw []byte) []byte { return []byte(strings.Replace(string(raw), "ani.metric.v1", "wrong", 1)) }},
		{"wrong architecture", "model_config.json", func(raw []byte) []byte { return changeMLPReport(t, raw, "input_dim", 15) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory, execution, contents := preparedWorkspace(t)
			writeFile(t, filepath.Join(directory, test.path), test.change(contents[test.path]))
			if _, err := workspace.Collect(context.Background(), directory, execution); !errors.Is(err, biz.ErrInvalidWorkspaceOutput) {
				t.Fatalf("forged report accepted: %v", err)
			}
		})
	}
}

func changeMLPReport(t *testing.T, raw []byte, key string, value any) []byte {
	t.Helper()
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	report[key] = value
	changed, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

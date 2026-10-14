package workspace_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/workspace"
)

type finetuningPrepareFixture struct {
	directory              string
	execution              biz.Execution
	binding                biz.WorkspaceBinding
	data                   []byte
	members                map[string][]byte
	dataClient, baseClient *s3.Client
	baseGets               *atomic.Int64
}

func TestFinetuningPreparePreservesOriginalJSONAndFrozenModelConfigBytes(t *testing.T) {
	f := prepareFinetuningInputs(t, "valid")
	prepared, err := workspace.PrepareFinetuningInput(context.Background(), f.dataClient, f.baseClient, f.execution, f.binding, f.directory)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(f.directory, "inputs/data/finetune_data.json"))
	if err != nil || !bytes.Equal(data, f.data) {
		t.Fatal("prepare changed source JSON bytes or normalized dataset")
	}
	datasetInfo, err := os.ReadFile(filepath.Join(f.directory, "inputs/data/dataset_info.json"))
	if err != nil || string(datasetInfo) != workspace.FinetuningDatasetInfo {
		t.Fatal("prepare changed fixed Alpaca mapping")
	}
	for name, content := range f.members {
		raw, err := os.ReadFile(filepath.Join(f.directory, "inputs/model", name))
		if err != nil || !bytes.Equal(raw, content) {
			t.Fatalf("model member %s does not match original frozen bytes", name)
		}
		info, err := os.Stat(filepath.Join(f.directory, "inputs/model", name))
		if err != nil || info.Mode().Perm()&0222 != 0 {
			t.Fatal("model inputs were not materialized read-only")
		}
	}
	configuration, err := os.ReadFile(filepath.Join(f.directory, "inputs/finetune_configuration.json"))
	if err != nil || pipelineDigest(configuration) != f.execution.Snapshot.Finetuning.ResolvedConfigurationSHA256 {
		t.Fatal("prepare did not write shared canonical frozen parameter configuration")
	}
	baseManifest, err := os.ReadFile(filepath.Join(f.directory, "inputs/base_manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantBase, err := f.execution.Snapshot.Finetuning.BaseModel.BaseManifestBytes()
	if err != nil || !bytes.Equal(baseManifest, wantBase) {
		t.Fatal("base inventory changed between admission and materialization")
	}
	manifest, err := os.ReadFile(filepath.Join(f.directory, "reports/prepared-manifest.json"))
	if err != nil || prepared.PreparedManifestSHA256 != pipelineDigest(manifest) || prepared.PreparedManifestBytes != int64(len(manifest)) {
		t.Fatal("prepare returned unbound or missing persisted byte receipt")
	}
	var facts map[string]any
	if json.Unmarshal(manifest, &facts) != nil {
		t.Fatal("manifest")
	}
	if facts["input_sha256"] != f.execution.Snapshot.Input.Object.SHA256 || facts["base_model_archive_sha256"] != f.execution.Snapshot.Finetuning.BaseModel.Object.SHA256 || facts["resolved_configuration_sha256"] != f.execution.Snapshot.Finetuning.ResolvedConfigurationSHA256 || facts["pvc_uid"] != f.binding.PVCUID {
		t.Fatal("prepared manifest lost frozen input/base/config or actual workspace binding")
	}
	for _, directory := range []string{"training/adapter", "export/merged", "reports/train", "reports/export", "reports/verify"} {
		entries, err := os.ReadDir(filepath.Join(f.directory, directory))
		if err != nil || len(entries) != 0 {
			t.Fatal("prepare fabricated later training/export/verify products")
		}
	}
	again, err := workspace.PrepareFinetuningInput(context.Background(), f.dataClient, f.baseClient, f.execution, f.binding, f.directory)
	if err != nil || !reflect.DeepEqual(again, prepared) {
		t.Fatalf("prepare replay changed original materialization receipt: %v", err)
	}
	if f.baseGets.Load() != 1 {
		t.Fatal("prepare replay fetched/re-extracted the fixed model again")
	}
	entries, err := os.ReadDir(filepath.Join(f.directory, "inputs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".base-staging-") {
			t.Fatal("successful prepare replay retained a second complete model staging directory")
		}
	}
}

func TestFinetuningPrepareRejectsUnapprovedArchiveWithoutCompletionReceipt(t *testing.T) {
	for _, mode := range []string{"traversal", "link", "PAX", "extra member", "wrong member SHA", "wrong archive SHA", "wrong version", "nonzero tail"} {
		t.Run(mode, func(t *testing.T) {
			f := prepareFinetuningInputs(t, mode)
			result, err := workspace.PrepareFinetuningInput(context.Background(), f.dataClient, f.baseClient, f.execution, f.binding, f.directory)
			if err == nil || result.PreparedManifestSHA256 != "" || result.PreparedManifestBytes != 0 {
				t.Fatal("bad model archive produced a trusted prepared receipt")
			}
			if _, err := os.Stat(filepath.Join(f.directory, "inputs/model")); !os.IsNotExist(err) {
				t.Fatal("partial/invalid model became ready materialization")
			}
			if _, err := os.Stat(filepath.Join(f.directory, "reports/prepared-manifest.json")); !os.IsNotExist(err) {
				t.Fatal("failed prepare emitted completion manifest")
			}
		})
	}
}

func TestFinetuningPrepareDoesNotReuseUnexpectedModelFilesOrFollowInputSymlink(t *testing.T) {
	f := prepareFinetuningInputs(t, "valid")
	if _, err := workspace.PrepareFinetuningInput(context.Background(), f.dataClient, f.baseClient, f.execution, f.binding, f.directory); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.directory, "inputs/model/unapproved.py"), []byte("unapproved model code"))
	if _, err := workspace.PrepareFinetuningInput(context.Background(), f.dataClient, f.baseClient, f.execution, f.binding, f.directory); err == nil {
		t.Fatal("prepare reused model directory with extra unapproved member")
	}
	other := prepareFinetuningInputs(t, "valid")
	outside := t.TempDir()
	if os.Mkdir(filepath.Join(other.directory, "inputs"), 0700) != nil || os.Symlink(outside, filepath.Join(other.directory, "inputs/data")) != nil {
		t.Fatal("symlink fixture")
	}
	if _, err := workspace.PrepareFinetuningInput(context.Background(), other.dataClient, other.baseClient, other.execution, other.binding, other.directory); err == nil {
		t.Fatal("prepare followed an input ancestor symlink")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("prepare wrote outside execution workspace")
	}
}

func prepareFinetuningInputs(t *testing.T, mode string) finetuningPrepareFixture {
	t.Helper()
	snapshot := conformance.FinetuningSnapshotV1()
	intent := conformance.FinetuningIntentV1()
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]map[string]string, 64)
	for index := range rows {
		rows[index] = map[string]string{"instruction": "answer two plus two", "input": "", "output": "four"}
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	snapshot.Input.Object.SizeBytes = int64(len(data))
	snapshot.Input.Object.SHA256 = pipelineDigest(data)
	members := map[string][]byte{"config.json": []byte(`{"architecture":"synthetic-fixture"}`), "model.safetensors": []byte("synthetic test bytes, not a real HF model"), "tokenizer_config.json": []byte(`{"fixture":true}`), "tokenizer.json": []byte(`{"fixture":true}`)}
	snapshot.Finetuning.BaseModel.Files = nil
	names := []string{}
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for index, name := range names {
		content := append([]byte{}, members[name]...)
		snapshot.Finetuning.BaseModel.Files = append(snapshot.Finetuning.BaseModel.Files, cpup01.BaseModelFile{Path: name, SizeBytes: int64(len(content)), SHA256: pipelineDigest(content)})
		header := &tar.Header{Name: name, Mode: 0600, Size: int64(len(content)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if index == 0 {
			switch mode {
			case "traversal":
				header.Name = "../config.json"
			case "link":
				header.Typeflag = tar.TypeSymlink
				header.Linkname = "outside"
				header.Size = 0
				content = nil
			case "PAX":
				header.Format = tar.FormatPAX
				header.PAXRecords = map[string]string{"comment": "unapproved"}
			case "wrong member SHA":
				content[0] ^= 1
			}
		}
		if writer.WriteHeader(header) != nil {
			t.Fatal("header")
		}
		if _, err := writer.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "extra member" {
		if writer.WriteHeader(&tar.Header{Name: "extra.json", Mode: 0600, Size: 1, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}) != nil {
			t.Fatal("header")
		}
		if _, err := writer.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if writer.Close() != nil {
		t.Fatal("close")
	}
	payload := archive.Bytes()
	if mode == "nonzero tail" {
		payload = append(payload, 1)
	}
	snapshot.Finetuning.BaseModel.Object.SizeBytes = int64(len(payload))
	snapshot.Finetuning.BaseModel.Object.SHA256 = pipelineDigest(payload)
	if mode == "wrong archive SHA" {
		snapshot.Finetuning.BaseModel.Object.SHA256 = strings.Repeat("0", 64)
	}
	baseHash, err := snapshot.Finetuning.BaseModel.BaseManifestSHA256()
	if err != nil {
		t.Fatal(err)
	}
	arguments := func(role string) []string {
		return []string{role, "--configuration", "/workspace/inputs/finetune_configuration.json", "--workspace", "/workspace", "--expected-input-sha256", snapshot.Input.Object.SHA256, "--expected-input-bytes", strconv.FormatInt(snapshot.Input.Object.SizeBytes, 10), "--expected-base-manifest-sha256", baseHash}
	}
	snapshot.Program.ResolvedArgs = arguments("train")
	snapshot.Finetuning.ExportProgram.ResolvedArgs = arguments("export")
	snapshot.Finetuning.VerifyProgram.ResolvedArgs = arguments("verify")
	e := biz.Execution{Admission: biz.Admission{TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:user:prepare-fixture", OperationID: "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff", ExecutionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", Intent: intent, IntentHash: intentHash, Snapshot: snapshot, AcceptedAt: snapshot.DeadlineAt.Add(-time.Hour)}}
	refreshSpec(t, &e)
	_, dataClient := newPipelineS3(t, e, data)
	baseObject := snapshot.Finetuning.BaseModel.Object
	gets := &atomic.Int64{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/"+baseObject.Bucket+"/"+baseObject.Key || r.URL.Query().Get("versionId") != *baseObject.VersionID {
			t.Error("base fetch left its frozen object/version")
			w.WriteHeader(403)
			return
		}
		gets.Add(1)
		version := *baseObject.VersionID
		if mode == "wrong version" {
			version = "wrong-version"
		}
		w.Header().Set("x-amz-version-id", version)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)
	baseClient := s3.New(s3.Options{Region: "test", BaseEndpoint: aws.String(server.URL), UsePathStyle: true, HTTPClient: server.Client(), Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1})
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Clean(filepath.Join(working, "../../..", ".work/finetuning-tests"))
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(parent, "prepare-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	want := snapshot.Workspace
	binding := biz.WorkspaceBinding{Mode: want.Mode, NamespaceName: snapshot.Environment.NamespaceName, NamespaceUID: snapshot.Environment.NamespaceUID, PVCName: "fixture-workspace", PVCUID: "77777777-7777-4777-8777-777777777777", InputSubpath: want.InputSubpath, TrainingSubpath: want.TrainingSubpath, ReportsSubpath: want.ReportsSubpath, PublicationSubpath: want.PublicationSubpath}
	return finetuningPrepareFixture{directory: directory, execution: e, binding: binding, data: data, members: members, dataClient: dataClient, baseClient: baseClient, baseGets: gets}
}

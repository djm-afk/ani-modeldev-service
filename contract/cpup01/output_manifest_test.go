package cpup01_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

func TestOutputManifestBindsActualInventoryWithoutClaimingPublication(t *testing.T) {
	admission, files := manifestFixture(t)
	canonical, hash, err := cpup01.OutputManifestBytes(admission, files)
	if err != nil {
		t.Fatalf("complete inventory: %v", err)
	}
	digest := sha256.Sum256(canonical)
	if hash != hex.EncodeToString(digest[:]) {
		t.Fatal("manifest digest is not the exact returned bytes")
	}
	var decoded struct {
		Schema            string              `json:"schema"`
		TenantID          string              `json:"tenant_id"`
		OperationID       string              `json:"operation_id"`
		ExecutionID       string              `json:"execution_id"`
		ExecutionSpecHash string              `json:"execution_spec_hash"`
		InputVersionID    string              `json:"input_version_id"`
		InputSHA256       string              `json:"input_sha256"`
		ImplementationID  string              `json:"implementation_id"`
		ImageDigest       string              `json:"image_digest"`
		StorageState      string              `json:"storage_state"`
		Files             []cpup01.OutputFile `json:"files"`
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("manifest has unsupported or self-hash fields: %v", err)
	}
	if decoded.Schema != "ani.modeldev.output-manifest.v3" || decoded.StorageState != "WORKSPACE_ONLY" || decoded.TenantID != admission.TenantID || decoded.OperationID != admission.OperationID || decoded.ExecutionID != admission.ExecutionID || decoded.ExecutionSpecHash != admission.SpecHash || decoded.InputVersionID != admission.Snapshot.Input.InputVersionID || decoded.InputSHA256 != admission.Snapshot.Input.Object.SHA256 || decoded.ImplementationID != admission.Snapshot.Implementation.ImplementationID || decoded.ImageDigest != admission.Snapshot.Program.ImageDigest {
		t.Fatalf("manifest lost frozen binding: %+v", decoded)
	}
	wantFiles := []cpup01.OutputFile{files[1], files[0], files[2], files[3]}
	if !reflect.DeepEqual(decoded.Files, wantFiles) {
		t.Fatalf("inventory not preserved/sorted: %+v", decoded.Files)
	}
	reordered := []cpup01.OutputFile{files[3], files[2], files[1], files[0]}
	second, secondHash, err := cpup01.OutputManifestBytes(admission, reordered)
	if err != nil || !bytes.Equal(canonical, second) || secondHash != hash {
		t.Fatal("manifest bytes depend on collector order")
	}
}

func TestOutputManifestRejectsInvalidInventoryAndEnvelope(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*cpup01.AdmissionEnvelope, *[]cpup01.OutputFile)
	}{
		{"missing file", func(_ *cpup01.AdmissionEnvelope, files *[]cpup01.OutputFile) { *files = (*files)[:3] }},
		{"duplicate file", func(_ *cpup01.AdmissionEnvelope, files *[]cpup01.OutputFile) { (*files)[1] = (*files)[0] }},
		{"extra file", func(_ *cpup01.AdmissionEnvelope, files *[]cpup01.OutputFile) {
			*files = append(*files, cpup01.OutputFile{RelativePath: "result.json", Role: "SUMMARY", InTar: true, SizeBytes: 1, SHA256: strings.Repeat("a", 64)})
		}},
		{"traversal", func(_ *cpup01.AdmissionEnvelope, files *[]cpup01.OutputFile) {
			(*files)[0].RelativePath = "../model.pt"
		}},
		{"role changed", func(_ *cpup01.AdmissionEnvelope, files *[]cpup01.OutputFile) { (*files)[0].Role = "SUMMARY" }},
		{"empty file", func(_ *cpup01.AdmissionEnvelope, files *[]cpup01.OutputFile) { (*files)[0].SizeBytes = 0 }},
		{"oversize", func(a *cpup01.AdmissionEnvelope, files *[]cpup01.OutputFile) {
			(*files)[0].SizeBytes = a.Snapshot.Limits.MaxContentBytes + 1
		}},
		{"etag instead of sha", func(_ *cpup01.AdmissionEnvelope, files *[]cpup01.OutputFile) {
			(*files)[0].SHA256 = "d41d8cd98f00b204e9800998ecf8427e-2"
		}},
		{"uppercase sha", func(_ *cpup01.AdmissionEnvelope, files *[]cpup01.OutputFile) {
			(*files)[0].SHA256 = strings.Repeat("A", 64)
		}},
		{"spec tampered", func(a *cpup01.AdmissionEnvelope, _ *[]cpup01.OutputFile) { a.SpecHash = strings.Repeat("0", 64) }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			admission, files := manifestFixture(t)
			test.mutate(&admission, &files)
			canonical, hash, err := cpup01.OutputManifestBytes(admission, files)
			if !errors.Is(err, cpup01.ErrInvalidArgument) || len(canonical) != 0 || hash != "" {
				t.Fatal("invalid inventory produced usable manifest")
			}
		})
	}
}

func manifestFixture(t *testing.T) (cpup01.AdmissionEnvelope, []cpup01.OutputFile) {
	t.Helper()
	snapshot := conformance.SnapshotV1()
	intent := conformance.IntentV1()
	_, hash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	specHash, _ := snapshot.Digest()
	admission := cpup01.AdmissionEnvelope{TenantID: snapshot.IdentityRef.TenantID, Actor: snapshot.IdentityRef.Actor, OperationID: snapshot.IdentityRef.OperationID, ExecutionID: snapshot.IdentityRef.ExecutionID, Intent: intent, IntentHash: hash, Snapshot: snapshot, SpecHash: specHash, AcceptedAt: snapshot.Timing.AcceptedAt}
	files := []cpup01.OutputFile{
		{RelativePath: "model.pt", Role: "CHECKPOINT", InTar: true, SizeBytes: 17, SHA256: strings.Repeat("a", 64)},
		{RelativePath: "metrics.jsonl", Role: "METRICS", InTar: true, SizeBytes: 9, SHA256: strings.Repeat("b", 64)},
		{RelativePath: "model_config.json", Role: "MODEL_CONFIG", InTar: true, SizeBytes: 8, SHA256: strings.Repeat("c", 64)},
		{RelativePath: "summary.json", Role: "SUMMARY", InTar: true, SizeBytes: 7, SHA256: strings.Repeat("d", 64)},
	}
	return admission, files
}

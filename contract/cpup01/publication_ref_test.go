package cpup01_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

func TestCurrentPublicationCandidateBindsScopeInventoryAndExactBytes(t *testing.T) {
	admission, files := manifestFixture(t)
	version := "synthetic-version"
	scope := admission.Snapshot.PublicationScope
	object := func(key string, size int64, digest string) cpup01.FixedObjectRef {
		return cpup01.FixedObjectRef{StorageConnectionID: scope.StorageConnectionID, Bucket: scope.Bucket, Key: scope.ApprovedPrefix + "/" + key, VersionID: &version, SizeBytes: size, SHA256: digest}
	}
	manifest, manifestDigest, err := cpup01.OutputManifestBytes(admission, files)
	if err != nil {
		t.Fatal(err)
	}
	candidate := cpup01.PublicationCandidate{Schema: cpup01.PublicationCandidateSchemaVersion, TenantID: admission.TenantID, OperationID: admission.OperationID, ExecutionID: admission.ExecutionID, ExecutionSpecHash: admission.SpecHash, LogicalPublicationKey: "current-test-publication", ManifestObject: object("output-manifest.json", int64(len(manifest)), manifestDigest), UploadTaskID: "publish-task", UploadPodUID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
	for _, file := range files {
		candidate.Files = append(candidate.Files, cpup01.PublishedFileCandidate{File: file, Object: object(file.RelativePath, file.SizeBytes, file.SHA256)})
	}
	if err := candidate.ValidateFor(admission); err != nil {
		t.Fatal(err)
	}
	raw, err := candidate.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	ref := cpup01.PublicationRef{Schema: cpup01.PublicationRefSchemaVersion, SizeBytes: int64(len(raw)), SHA256: hex.EncodeToString(digest[:])}
	decoded, err := cpup01.VerifyPublicationCandidate(ref, raw)
	if err != nil || decoded.ValidateFor(admission) != nil {
		t.Fatal("exact publication candidate rejected", err)
	}
	raw[0] = ' '
	if _, err := cpup01.VerifyPublicationCandidate(ref, raw); err == nil {
		t.Fatal("mutated candidate accepted")
	}
	candidate.Files[0].Object.Bucket = "other-bucket"
	if candidate.ValidateFor(admission) == nil {
		t.Fatal("publication escaped frozen approved scope")
	}
}

func TestCurrentPublicationRefCannotCarryCandidateOrChoosePath(t *testing.T) {
	ref := cpup01.PublicationRef{Schema: cpup01.PublicationRefSchemaVersion, SizeBytes: cpup01.MaxPublicationCandidateBytes, SHA256: strings.Repeat("a", 64)}
	raw, err := ref.Canonical()
	if err != nil || len(raw) > cpup01.MaxPublicationRefBytes {
		t.Fatal(err)
	}
	decoded, err := cpup01.ParsePublicationRef(raw)
	if err != nil || decoded != ref {
		t.Fatalf("current ref roundtrip: %v", err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), ".v3", ".v1", 1),
		strings.TrimSuffix(string(raw), "}") + `,"path":"elsewhere"}`,
		strings.TrimSuffix(string(raw), "}") + `,"candidate":{}}`,
		strings.Repeat("x", cpup01.MaxPublicationRefBytes+1),
	} {
		if _, err := cpup01.ParsePublicationRef([]byte(bad)); err == nil {
			t.Fatal("non-current or inline/path ref accepted")
		}
	}
	ref.SizeBytes++
	if _, err := ref.Canonical(); err == nil {
		t.Fatal("candidate size exceeds structural limit")
	}
}

package cpup01_test

import (
	"bytes"
	"encoding/json"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"os"
	"strings"
	"testing"
)

func TestV4FreezesDispatchSAUIDsAndManagedPullRefs(t *testing.T) {
	original := conformance.SnapshotV1()
	before, err := original.Digest()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*cpup01.Snapshot){
		"dispatch reference": func(s *cpup01.Snapshot) { s.DispatchOwner.Reference += "-changed" },
		"dispatch revision":  func(s *cpup01.Snapshot) { s.DispatchOwner.RevisionSHA256 = strings.Repeat("b", 64) },
		"artifact bucket": func(s *cpup01.Snapshot) {
			s.DispatchOwner.PipelineRoot = strings.Replace(s.DispatchOwner.PipelineRoot, "kfp-artifacts", "other-artifacts", 1)
		},
		"step UID": func(s *cpup01.Snapshot) {
			s.Environment.Identities.KFPStepServiceAccountUID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		},
		"trainer UID": func(s *cpup01.Snapshot) {
			s.Environment.Identities.TrainerServiceAccountUID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		},
		"verifier UID": func(s *cpup01.Snapshot) {
			s.Environment.Identities.VerifierServiceAccountUID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		},
		"managed registry": func(s *cpup01.Snapshot) {
			s.ManagedImageAccess.PullBindings[0].RegistryAuthority = "registry.other.test"
		},
		"managed ref": func(s *cpup01.Snapshot) {
			s.ManagedImageAccess.PullBindings[0].SecretName = "ani-pull-" + strings.Repeat("b", 32)
		},
	}
	raw, _ := original.Canonical()
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, err := cpup01.DecodeCanonicalSnapshot(raw)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&s)
			after, err := s.Digest()
			if err != nil || after == before {
				t.Fatalf("material not frozen: %s %v", after, err)
			}
		})
	}
}
func TestV4RejectsOldSchemaAndNoncanonicalTenant(t *testing.T) {
	raw, _ := conformance.SnapshotV1().Canonical()
	bad := [][]byte{bytes.Replace(raw, []byte("execution-spec.v4"), []byte("execution-spec.v3"), 1), bytes.Replace(raw, []byte(`"schema_version":"ani.modeldev.execution-spec.v4"`), []byte(`"extra":true,"schema_version":"ani.modeldev.execution-spec.v4"`), 1)}
	for _, v := range bad {
		if _, err := cpup01.DecodeCanonicalSnapshot(v); err == nil {
			t.Fatal("old/unknown contract accepted")
		}
	}
	for _, tenant := range []string{"2", "00000000-0000-0000-0000-000000000000", "64C5C35A-F554-432C-B4BC-7B4F36F08420", " 77777777-7777-4777-8777-777777777777", "77777777777747778777777777777777"} {
		s := conformance.SnapshotV1()
		s.IdentityRef.TenantID = tenant
		if _, err := s.Canonical(); err == nil {
			t.Fatalf("bad tenant accepted: %q", tenant)
		}
	}
	s := conformance.SnapshotV1()
	s.Environment.NamespaceName = "old-modeldev-tenant"
	if _, err := s.Canonical(); err == nil {
		t.Fatal("legacy namespace accepted")
	}
}

func TestV4UsesSharedTenantNamespaceVectors(t *testing.T) {
	raw, err := os.ReadFile("../tenantnamespace/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Inputs []struct {
			ID        string `json:"id"`
			Input     string `json:"input"`
			Valid     bool   `json:"valid"`
			Namespace string `json:"expected_namespace"`
		} `json:"input_cases"`
	}
	if json.Unmarshal(raw, &vectors) != nil || len(vectors.Inputs) == 0 {
		t.Fatal("missing shared namespace vectors")
	}
	for _, vector := range vectors.Inputs {
		t.Run(vector.ID, func(t *testing.T) {
			if cpup01.CanonicalTenantID(vector.Input) != vector.Valid {
				t.Fatal("UUID contract differs from shared vector")
			}
			snapshot := conformance.SnapshotV1()
			snapshot.IdentityRef.TenantID = vector.Input
			snapshot.Environment.NamespaceName = "tenant-" + vector.Input
			snapshot.RuntimePlan.Namespace = snapshot.Environment.NamespaceName
			snapshot.Input.Object.Key = "inputs/" + vector.Input + "/data.csv"
			snapshot.PublicationScope.ApprovedPrefix = "executions/" + vector.Input
			snapshot.DispatchOwner.PipelineRoot = "s3://kfp-artifacts/artifacts/" + vector.Input + "/" + snapshot.IdentityRef.ExecutionID
			_, err := snapshot.Canonical()
			if (err == nil) != vector.Valid {
				t.Fatalf("snapshot identity accepted=%v expected=%v", err == nil, vector.Valid)
			}
			if vector.Valid && snapshot.Environment.NamespaceName != vector.Namespace {
				t.Fatal("namespace differs from shared vector")
			}
		})
	}
}

package cpup01_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

// The training chain is hard-switched: the retired v2.1 training snapshot and
// its ImageVersionID identity are rejected outright, with no legacy decoder.
func TestTrainingSnapshotRejectsRetiredSchemaAndProgramIdentity(t *testing.T) {
	for _, change := range []struct {
		name   string
		mutate func(*cpup01.Snapshot)
	}{
		{"retired v2.1 schema", func(s *cpup01.Snapshot) { s.SchemaVersion = cpup01.SnapshotSchemaVersion }},
		{"retired program identity", func(s *cpup01.Snapshot) {
			s.TrainingProgram = nil
			s.Program = &cpup01.ProgramRef{ImageVersionID: "ffffffff-ffff-4fff-8fff-ffffffffffff", ImageDigest: conformance.TrainingReference, Command: []string{"python", "/opt/mlp/train.py"}}
		}},
		{"both programs", func(s *cpup01.Snapshot) {
			s.Program = &cpup01.ProgramRef{ImageVersionID: "ffffffff-ffff-4fff-8fff-ffffffffffff", ImageDigest: conformance.TrainingReference, Command: []string{"python", "/opt/mlp/train.py"}}
		}},
		{"no program", func(s *cpup01.Snapshot) { s.TrainingProgram = nil }},
	} {
		t.Run(change.name, func(t *testing.T) {
			snapshot := conformance.SnapshotV1()
			change.mutate(&snapshot)
			if snapshot.Validate() == nil {
				t.Fatal("retired or ambiguous training program accepted")
			}
			if _, err := snapshot.Canonical(); err == nil {
				t.Fatal("retired or ambiguous training program acquired canonical bytes")
			}
		})
	}
}

// The finetuning chain keeps its own schema and ProgramRef identity, so the two
// chains can never share a hash or a fallback.
func TestFinetuningSnapshotKeepsItsOwnSchemaAndProgram(t *testing.T) {
	snapshot := conformance.FinetuningSnapshotV1()
	if snapshot.SchemaVersion != cpup01.SnapshotSchemaVersion || snapshot.Program == nil || snapshot.TrainingProgram != nil {
		t.Fatalf("finetuning identity changed: %+v", snapshot.SchemaVersion)
	}
	if snapshot.Validate() != nil {
		t.Fatal("finetuning snapshot rejected")
	}
	snapshot.SchemaVersion = cpup01.TrainingSnapshotSchemaVersion
	if snapshot.Validate() == nil {
		t.Fatal("finetuning accepted the training schema")
	}
}

// A catalogue selection is optional (the material default applies), but a
// partial pair is never defaulted and a bare reference is not a selection.
func TestTrainingImageSelectionIsOptionalButNeverPartial(t *testing.T) {
	base := conformance.IntentRequest
	for _, test := range []struct {
		name, request string
		accepted      bool
	}{
		{"explicit selection", base, true},
		{"material default", strings.Replace(base, `,"training_image_id":"img_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","training_image_scope":"platform"`, "", 1), true},
		{"id without scope", strings.Replace(base, `,"training_image_scope":"platform"`, "", 1), false},
		{"scope without id", strings.Replace(base, `"training_image_id":"img_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",`, "", 1), false},
		{"bare reference", strings.Replace(base, `"img_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"registry.example.test/cpu/mlp:latest"`, 1), false},
		{"tag instead of id", strings.Replace(base, `"img_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"img_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, 1), false},
		{"unknown scope", strings.Replace(base, `"training_image_scope":"platform"`, `"training_image_scope":"global"`, 1), false},
		{"empty scope", strings.Replace(base, `"training_image_scope":"platform"`, `"training_image_scope":""`, 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			intent, err := cpup01.ParseIntent([]byte(test.request))
			if test.accepted {
				if err != nil {
					t.Fatalf("valid selection rejected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("partial or non-catalogue selection accepted: %+v", intent)
			}
		})
	}
}

// The finetuning intent must not carry a training image selection at all.
func TestFinetuningIntentRejectsTrainingImageSelection(t *testing.T) {
	raw, err := json.Marshal(conformance.FinetuningIntentV1())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cpup01.ParseIntent(raw); err != nil {
		t.Fatalf("finetuning fixture rejected: %v", err)
	}
	withImage := strings.Replace(string(raw), `"parameters":`, `"training_image_id":"img_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","training_image_scope":"platform","parameters":`, 1)
	if _, err := cpup01.ParseIntent([]byte(withImage)); err == nil {
		t.Fatal("finetuning intent accepted a training image selection")
	}
}

// The frozen snapshot image must match the accepted intent exactly. A different
// image, scope or a missing resolved program is a contract violation.
func TestAdmissionRejectsTrainingImageSubstitution(t *testing.T) {
	envelope := func() cpup01.AdmissionEnvelope {
		snapshot := conformance.SnapshotV1()
		intent := conformance.IntentV1()
		_, intentHash, err := cpup01.CanonicalIntent(intent)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.IdentityRef.IntentHash = intentHash
		specHash, err := snapshot.Digest()
		if err != nil {
			t.Fatal(err)
		}
		return cpup01.AdmissionEnvelope{
			TenantID: snapshot.IdentityRef.TenantID, Actor: snapshot.IdentityRef.Actor,
			OperationID: snapshot.IdentityRef.OperationID, ExecutionID: snapshot.IdentityRef.ExecutionID,
			Intent: intent, IntentHash: intentHash, Snapshot: snapshot, SpecHash: specHash,
			AcceptedAt: snapshot.Timing.AcceptedAt,
		}
	}
	if _, _, err := envelope().CanonicalPayloads(); err != nil {
		t.Fatalf("valid training admission rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*cpup01.AdmissionEnvelope)
	}{
		{"different image id", func(a *cpup01.AdmissionEnvelope) {
			a.Snapshot.TrainingProgram.Image.ImageID = "img_" + strings.Repeat("b", 32)
		}},
		{"different scope", func(a *cpup01.AdmissionEnvelope) { a.Snapshot.TrainingProgram.Image.Scope = "tenant" }},
		{"missing resolved program", func(a *cpup01.AdmissionEnvelope) { a.Snapshot.TrainingProgram = nil }},
		{"partial intent pair", func(a *cpup01.AdmissionEnvelope) { a.Intent.TrainingImageScope = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := envelope()
			test.mutate(&value)
			if _, _, err := value.CanonicalPayloads(); err == nil {
				t.Fatal("substituted training image accepted")
			}
		})
	}
}

// The shared Secret name is scoped by tenant, cluster, namespace and registry.
// cluster_id must stay in the hash so equal namespace names cannot collide.
func TestPullSecretNameCoversEveryScopeDimension(t *testing.T) {
	const tenant, cluster, namespace, registry = "77777777-7777-4777-8777-777777777777", "22222222-2222-4222-8222-222222222222", "cpu-contract-fixture", "registry.example.test"
	base, err := cpup01.PullSecretName(tenant, cluster, namespace, registry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(base, "ani-pull-") || len(base) != len("ani-pull-")+32 || strings.ToLower(base) != base {
		t.Fatalf("unexpected shared Secret name: %s", base)
	}
	for _, variant := range []string{"registry.example.test", "https://registry.example.test/", "registry.example.test:443", "REGISTRY.EXAMPLE.TEST"} {
		if got, err := cpup01.PullSecretName(tenant, cluster, namespace, variant); err != nil || got != base {
			t.Fatalf("registry normalization changed the name: %s %v", got, err)
		}
	}
	for _, mutate := range []func() (string, error){
		func() (string, error) { return cpup01.PullSecretName("88888888-8888-4888-8888-888888888888", cluster, namespace, registry) },
		func() (string, error) { return cpup01.PullSecretName(tenant, "33333333-3333-4333-8333-333333333333", namespace, registry) },
		func() (string, error) { return cpup01.PullSecretName(tenant, cluster, "other-namespace", registry) },
		func() (string, error) { return cpup01.PullSecretName(tenant, cluster, namespace, "registry.example.test:8443") },
	} {
		if got, err := mutate(); err != nil || got == base {
			t.Fatalf("scope dimension did not change the name: %s %v", got, err)
		}
	}
	for _, incomplete := range [][4]string{
		{"", cluster, namespace, registry},
		{tenant, "", namespace, registry},
		{tenant, cluster, "", registry},
		{tenant, cluster, namespace, ""},
	} {
		if _, err := cpup01.PullSecretName(incomplete[0], incomplete[1], incomplete[2], incomplete[3]); err == nil {
			t.Fatal("incomplete pull Secret scope accepted")
		}
	}
}

// A frozen selection must be internally consistent: the reference carries the
// digest, the reference lives under the authoritative registry, and the Secret
// reference is the shared name shape. Credentials never appear.
func TestTrainingImageSelectionRejectsInconsistentFacts(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*cpup01.TrainingImageSelection)
	}{
		{"digest without prefix", func(s *cpup01.TrainingImageSelection) { s.Digest = strings.Repeat("e", 64) }},
		{"reference without digest", func(s *cpup01.TrainingImageSelection) { s.Reference = conformance.RegistryAuthority + "/cpu/mlp:latest" }},
		{"reference outside authority", func(s *cpup01.TrainingImageSelection) {
			s.Reference = "registry.other.test/cpu/mlp@" + s.Digest
		}},
		{"unnormalized authority", func(s *cpup01.TrainingImageSelection) { s.RegistryAuthority = "Registry.Example.Test" }},
		{"missing authority", func(s *cpup01.TrainingImageSelection) { s.RegistryAuthority = "" }},
		{"unknown protocol", func(s *cpup01.TrainingImageSelection) { s.ProtocolVersion = "ani.modeldev.training-image-protocol.v2" }},
		{"foreign pull secret", func(s *cpup01.TrainingImageSelection) { s.PullSecretRef = "ani-inference-pull-tenant" }},
		{"uppercase pull secret", func(s *cpup01.TrainingImageSelection) { s.PullSecretRef = strings.ToUpper(s.PullSecretRef) }},
		{"missing platform", func(s *cpup01.TrainingImageSelection) { s.Platform = cpup01.TrainingPlatform{} }},
		{"unknown scope", func(s *cpup01.TrainingImageSelection) { s.Scope = "global" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := conformance.SnapshotV1()
			test.mutate(&snapshot.TrainingProgram.Image)
			if snapshot.Validate() == nil {
				t.Fatal("inconsistent training image selection accepted")
			}
		})
	}
}

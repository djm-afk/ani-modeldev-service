package cpup01_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

const (
	fixtureImageID = "img_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fixtureTenant  = "64c5c35a-f554-432c-b4bc-7b4f36f08420"
	fixtureCluster = "f39bf6e2-f3ed-4191-ada3-caeebf86c220"
	fixtureNS      = "ani-modeldev-tenant-a-v21"
	fixtureReg     = "10.10.1.66:30003"
)

func selectionJSON(id, scope string) string {
	return `"training_image_id":"` + id + `","training_image_scope":"` + scope + `",`
}

func withSelection(base, fields string) string {
	return strings.Replace(base, `"parameters":`, fields+`"parameters":`, 1)
}

// A catalogue selection is optional (the trusted material default applies), but
// a partial pair is never defaulted and a bare reference is not a selection.
func TestTrainingImageSelectionIsOptionalButNeverPartial(t *testing.T) {
	base := conformance.IntentRequest
	for _, test := range []struct {
		name     string
		request  string
		accepted bool
	}{
		{"material default", base, true},
		{"platform selection", withSelection(base, selectionJSON(fixtureImageID, "platform")), true},
		{"tenant selection", withSelection(base, selectionJSON(fixtureImageID, "tenant")), true},
		{"id without scope", withSelection(base, `"training_image_id":"`+fixtureImageID+`",`), false},
		{"scope without id", withSelection(base, `"training_image_scope":"platform",`), false},
		{"bare reference", withSelection(base, selectionJSON("registry.example.test/cpu/mlp:latest", "platform")), false},
		{"short id", withSelection(base, selectionJSON("img_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "platform")), false},
		{"uppercase id", withSelection(base, selectionJSON(strings.ToUpper(fixtureImageID), "platform")), false},
		{"unknown scope", withSelection(base, selectionJSON(fixtureImageID, "global")), false},
		{"empty scope", withSelection(base, selectionJSON(fixtureImageID, "")), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := cpup01.ParseIntent([]byte(test.request))
			if test.accepted && err != nil {
				t.Fatalf("valid selection rejected: %v", err)
			}
			if !test.accepted && err == nil {
				t.Fatal("partial or non-catalogue selection accepted")
			}
		})
	}
}

// The finetuning intent keeps its material-fixed image and carries no selection.
func TestFinetuningIntentRejectsTrainingImageSelection(t *testing.T) {
	raw, err := json.Marshal(conformance.FinetuningIntentV1())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cpup01.ParseIntent(raw); err != nil {
		t.Fatalf("finetuning fixture rejected: %v", err)
	}
	if _, err := cpup01.ParseIntent([]byte(withSelection(string(raw), selectionJSON(fixtureImageID, "platform")))); err == nil {
		t.Fatal("finetuning intent accepted a training image selection")
	}
}

// General training always runs a catalogue image resolved by Resource; the
// material default does not freeze a digest and cannot stand alone.
func TestTrainingSnapshotRequiresConsistentResolvedImageFacts(t *testing.T) {
	valid := conformance.SnapshotV1()
	if err := valid.Validate(); err != nil {
		t.Fatalf("training fixture rejected: %v", err)
	}
	facts := *valid.TrainingImage
	mutations := map[string]func(*cpup01.Snapshot){
		"missing facts": func(s *cpup01.Snapshot) { s.TrainingImage = nil },
		"unnormalized authority": func(s *cpup01.Snapshot) {
			f := facts
			f.RegistryAuthority = "HTTPS://10.10.1.66:30003/"
			s.TrainingImage = &f
		},
		"image outside authority": func(s *cpup01.Snapshot) {
			s.Program.ImageDigest = "mirror.example.test/cpu/mlp@sha256:" + strings.Repeat("e", 64)
		},
		"unknown protocol": func(s *cpup01.Snapshot) {
			f := facts
			f.ProtocolVersion = "ani.modeldev.training-image-protocol.v0"
			s.TrainingImage = &f
		},
		"invalid secret name": func(s *cpup01.Snapshot) {
			f := facts
			f.PullSecretRef = "ani-inference-pull-" + strings.Repeat("a", 32)
			s.TrainingImage = &f
		},
		"missing platform": func(s *cpup01.Snapshot) {
			f := facts
			f.Platform = cpup01.TrainingPlatform{}
			s.TrainingImage = &f
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			snapshot := valid
			mutate(&snapshot)
			if snapshot.Validate() == nil {
				t.Fatal("inconsistent training image facts accepted")
			}
			if _, err := snapshot.Canonical(); err == nil {
				t.Fatal("inconsistent training image facts acquired canonical bytes")
			}
		})
	}
	// The finetuning chain keeps its own material-fixed identity.
	fine := conformance.FinetuningSnapshotV1()
	if fine.TrainingImage != nil {
		t.Fatal("finetuning fixture carries catalogue facts")
	}
	if err := fine.Validate(); err != nil {
		t.Fatalf("finetuning fixture rejected: %v", err)
	}
	fine.TrainingImage = valid.TrainingImage
	if fine.Validate() == nil {
		t.Fatal("finetuning snapshot accepted training image facts")
	}
}

// The shared Secret name is scoped by tenant, cluster, namespace and registry
// authority only; the workload kind never participates.
func TestPullSecretNameIsScopedByAllFourDimensions(t *testing.T) {
	base, err := cpup01.PullSecretName(fixtureTenant, fixtureCluster, fixtureNS, fixtureReg)
	if err != nil || !strings.HasPrefix(base, "ani-pull-") || len(base) != len("ani-pull-")+32 {
		t.Fatalf("unexpected Secret name %q %v", base, err)
	}
	again, err := cpup01.PullSecretName(strings.ToUpper(fixtureTenant), strings.ToUpper(fixtureCluster), strings.ToUpper(fixtureNS), "https://"+fixtureReg+"/")
	if err != nil || again != base {
		t.Fatalf("case, scheme or trailing path changed the name: %q %v", again, err)
	}
	for name, changed := range map[string]string{
		"tenant":    mustName(t, "11111111-1111-4111-8111-111111111111", fixtureCluster, fixtureNS, fixtureReg),
		"cluster":   mustName(t, fixtureTenant, "22222222-2222-4222-8222-222222222222", fixtureNS, fixtureReg),
		"namespace": mustName(t, fixtureTenant, fixtureCluster, "ani-modeldev-tenant-b", fixtureReg),
		"registry":  mustName(t, fixtureTenant, fixtureCluster, fixtureNS, "10.10.1.67:30003"),
	} {
		if changed == base {
			t.Fatalf("%s is not part of the Secret scope", name)
		}
	}
	if same := mustName(t, fixtureTenant, fixtureCluster, fixtureNS, fixtureReg+":443"); same != base {
		t.Fatal("default registry port changed the name")
	}
	for _, incomplete := range [][4]string{
		{"", fixtureCluster, fixtureNS, fixtureReg},
		{fixtureTenant, "", fixtureNS, fixtureReg},
		{fixtureTenant, fixtureCluster, "", fixtureReg},
		{fixtureTenant, fixtureCluster, fixtureNS, ""},
	} {
		if _, err := cpup01.PullSecretName(incomplete[0], incomplete[1], incomplete[2], incomplete[3]); !errors.Is(err, cpup01.ErrIncompletePullScope) {
			t.Fatal("incomplete pull scope accepted")
		}
	}
}

// The default image names only a catalogue entry; Resource resolves version,
// digest and platform per execution.
func TestDefaultTrainingImageHasNoStaleIdentity(t *testing.T) {
	material := conformance.MaterialV21()
	if material.Program != nil || material.Training == nil {
		t.Fatal("training material still freezes a program")
	}
	raw, err := material.Canonical()
	if err != nil {
		t.Fatalf("fixture default rejected: %v", err)
	}
	for _, retired := range []string{"image_digest", "image_version", "image_version_id"} {
		if bytesContains(raw, retired) {
			t.Fatalf("training material still pins %s", retired)
		}
	}
	training := *material.Training
	training.DefaultImage.Scope = cpup01.ImageScope("public")
	material.Training = &training
	if _, err := material.Canonical(); err == nil {
		t.Fatal("unknown image scope accepted")
	}
}

func mustName(t *testing.T, tenant, cluster, namespace, registry string) string {
	t.Helper()
	name, err := cpup01.PullSecretName(tenant, cluster, namespace, registry)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func bytesContains(raw []byte, value string) bool {
	return strings.Contains(string(raw), `"`+value+`"`)
}

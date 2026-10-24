package cpup01_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

func TestFinetuningArtifactsOnlyIntentCanonical(t *testing.T) {
	const body = `{"name":"CPU FT","kind":"FINETUNING","implementation_id":"builtin.sft_lora","compute_preset_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","dataset_version_id":"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee","base_model_version_id":"55555555-5555-4555-8555-555555555555","parameters":{}}`
	const canonical = `{"base_model_version_id":"55555555-5555-4555-8555-555555555555","compute_preset_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","dataset_version_id":"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee","implementation_id":"builtin.sft_lora","kind":"FINETUNING","name":"CPU FT","parameters":{},"schema":"ani.modeldev.intent.v2.1"}`
	intent, err := cpup01.ParseIntent([]byte(body))
	if err != nil {
		t.Fatalf("finetuning without a registration target must publish artifacts: %v", err)
	}
	got, hash, err := cpup01.CanonicalIntent(intent)
	if err != nil || string(got) != canonical || hash != "a07df9fbe5886666de046bc2c5bf4defb7a9cde9f28f6e252fb1c92b2bbbe80e" {
		t.Fatalf("artifacts-only canonical: %s %s %v", got, hash, err)
	}
	decoded, err := cpup01.DecodeCanonicalIntent(got)
	if err != nil || decoded.TargetModelID != nil || decoded.NewModelName != nil || decoded.BaseModelVersionID == nil || *decoded.BaseModelVersionID != "55555555-5555-4555-8555-555555555555" {
		t.Fatalf("stored intent must preserve artifacts-only choice and base identity: %+v %v", decoded, err)
	}
	registered, registeredHash, err := cpup01.CanonicalIntent(conformance.FinetuningIntentV1())
	if err != nil || registeredHash != "378d2a93b683f8751169c95f4c1a4ac9f742e705018a12a234e9fcc6621f6ecb" || registeredHash == hash || bytes.Equal(got, registered) {
		t.Fatal("registration choice must participate in the original canonical hash", err)
	}
	for _, extra := range []string{
		`,"target_model_id":"66666666-6666-4666-8666-666666666666","new_model_name":"model"`,
		`,"target_model_id":""`,
		`,"new_model_name":""`,
		`,"target_model_id":null`,
		`,"new_model_name":null`,
		`,"registration_mode":"none"`,
	} {
		if _, err := cpup01.ParseIntent([]byte(strings.TrimSuffix(body, "}") + extra + "}")); err == nil {
			t.Fatalf("ambiguous or invalid target accepted: %s", extra)
		}
	}
}

func artifactsOnlySnapshot(t *testing.T) (cpup01.Intent, cpup01.Snapshot) {
	t.Helper()
	intent := conformance.FinetuningIntentV1()
	intent.NewModelName = nil
	snapshot := conformance.FinetuningSnapshotV1()
	snapshot.Finetuning.NewModelName = nil
	_, hash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.IdentityRef.IntentHash = hash
	return intent, snapshot
}

func TestFinetuningArtifactsOnlySnapshotBindsChoiceAndRetainsValidation(t *testing.T) {
	intent, snapshot := artifactsOnlySnapshot(t)
	raw, err := snapshot.Canonical()
	if err != nil {
		t.Fatalf("artifacts-only snapshot must retain a complete executable plan: %v", err)
	}
	decoded, err := cpup01.DecodeCanonicalSnapshot(raw)
	if err != nil || decoded.Finetuning == nil || decoded.Finetuning.TargetModelID != nil || decoded.Finetuning.NewModelName != nil {
		t.Fatalf("snapshot lost the absent registration target: %v", err)
	}
	original := conformance.FinetuningSnapshotV1()
	var absent *cpup01.FinetuningSnapshot
	if snapshot.Finetuning.RequiresModelRegistration() || absent.RequiresModelRegistration() || !original.Finetuning.RequiresModelRegistration() {
		t.Fatal("target presence must select registration without adding a new mode")
	}
	originalBytes, err := original.Canonical()
	if err != nil || bytes.Equal(raw, originalBytes) {
		t.Fatal("artifacts-only choice must change snapshot canonical", err)
	}
	originalHash := sha256.Sum256(originalBytes)
	if hex.EncodeToString(originalHash[:]) != "2f39d7c99d1292998e8f4da8d0fff67ce143e7552f7553b6ca89532ee48c6b6c" {
		t.Fatal("existing registration snapshot canonical changed")
	}
	specHash, err := snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	admission := cpup01.AdmissionEnvelope{TenantID: snapshot.IdentityRef.TenantID, Actor: snapshot.IdentityRef.Actor, OperationID: snapshot.IdentityRef.OperationID, ExecutionID: snapshot.IdentityRef.ExecutionID, Intent: intent, IntentHash: snapshot.IdentityRef.IntentHash, Snapshot: snapshot, SpecHash: specHash, AcceptedAt: snapshot.Timing.AcceptedAt}
	if _, _, err := admission.CanonicalPayloads(); err != nil {
		t.Fatal("valid artifacts-only admission rejected", err)
	}
	name := "unrequested-model"
	admission.Snapshot.Finetuning.NewModelName = &name
	admission.SpecHash, err = admission.Snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := admission.CanonicalPayloads(); err == nil {
		t.Fatal("a canonical snapshot may not add registration to artifacts-only intent")
	}
	for name, mutate := range map[string]func(*cpup01.Snapshot){
		"base model": func(s *cpup01.Snapshot) { s.Finetuning.BaseModel.VersionID = "" },
		"input":      func(s *cpup01.Snapshot) { s.Input.RowCount = 0 },
		"timing":     func(s *cpup01.Snapshot) { s.Timing.StepTimeoutSeconds = 0 },
		"resources":  func(s *cpup01.Snapshot) { s.Resources.RequestMillicpu++ },
		"empty target": func(s *cpup01.Snapshot) {
			empty := ""
			s.Finetuning.TargetModelID = &empty
		},
		"both targets": func(s *cpup01.Snapshot) {
			id, model := "66666666-6666-4666-8666-666666666666", "model"
			s.Finetuning.TargetModelID, s.Finetuning.NewModelName = &id, &model
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, bad := artifactsOnlySnapshot(t)
			mutate(&bad)
			if bad.Validate() == nil {
				t.Fatal("artifacts-only choice bypassed a required validation")
			}
		})
	}
}

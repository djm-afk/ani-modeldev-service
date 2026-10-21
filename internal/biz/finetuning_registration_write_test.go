package biz

import (
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"strings"
	"testing"
)

// Dispatch/Stop/reconnect cases formerly used a fake permit repository here.
// They now run through the actual lifecycle PostgreSQL repository in
// internal/data/lifecycle/registration_orchestrator_test.go. Only the external
// Model and object-transfer boundaries are substituted there.
func TestFinetuningReceiptRequiresOriginalTargetAndCompleteBytes(t *testing.T) {
	tenant := "11111111-1111-4111-8111-111111111111"
	model := "22222222-2222-4222-8222-222222222222"
	session := TrainedArtifactSession{RegistrationID: "33333333-3333-4333-8333-333333333333", StoragePath: tenant + "/trained-artifacts/model.tar"}
	registration := FinetuningRegistration{TargetModelID: &model, Session: &session, Bundle: cpup01.FixedObjectRef{SizeBytes: 23, SHA256: strings.Repeat("a", 64)}}
	receipt := FinetuningModelReceipt{RegistrationID: session.RegistrationID, StoragePath: session.StoragePath, ModelID: model, ModelVersionID: "44444444-4444-4444-8444-444444444444", SizeBytes: 23, SHA256: registration.Bundle.SHA256}
	if !ValidFinetuningModelReceipt(tenant, registration, &receipt) {
		t.Fatal("original complete receipt rejected")
	}
	for _, change := range []func(*FinetuningModelReceipt){func(r *FinetuningModelReceipt) { r.ModelID = "55555555-5555-4555-8555-555555555555" }, func(r *FinetuningModelReceipt) { r.SizeBytes++ }, func(r *FinetuningModelReceipt) { r.SHA256 = strings.Repeat("b", 64) }, func(r *FinetuningModelReceipt) { r.StoragePath = tenant + "/other/model.tar" }, func(r *FinetuningModelReceipt) { r.RegistrationID = "66666666-6666-4666-8666-666666666666" }, func(r *FinetuningModelReceipt) { r.ModelVersionID = "" }} {
		bad := receipt
		change(&bad)
		if ValidFinetuningModelReceipt(tenant, registration, &bad) {
			t.Fatal("changed receipt accepted", bad)
		}
	}
	observation := TrainedArtifactObservation{Session: session, State: "ready", Receipt: &receipt}
	if ValidTrainedArtifactObservation(tenant, registration, observation) {
		t.Fatal("READY without independently verified object accepted")
	}
	observation.ObjectVerified = true
	if !ValidTrainedArtifactObservation(tenant, registration, observation) {
		t.Fatal("complete original observation rejected")
	}
}

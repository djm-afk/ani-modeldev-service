package service

import (
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// The detail must name the base model the finetuning started from, without
// publishing the object key, storage scope or file list the owner keeps for its
// own reads. Those facts live on the same snapshot value, so a careless
// pass-through would leak them.
func TestExecutionViewPublishesOnlyBaseModelIdentity(t *testing.T) {
	snapshot := conformance.FinetuningSnapshotV1()
	intent := conformance.FinetuningIntentV1()
	hash, err := snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	record := biz.QueryRecord{
		Execution: biz.Execution{Admission: biz.Admission{
			TenantID: snapshot.IdentityRef.TenantID, Actor: snapshot.IdentityRef.Actor,
			OperationID: snapshot.IdentityRef.OperationID, ExecutionID: snapshot.IdentityRef.ExecutionID,
			Intent: intent, IntentHash: snapshot.IdentityRef.IntentHash, Snapshot: snapshot, SpecHash: hash,
			AcceptedAt: snapshot.Timing.AcceptedAt,
		}, States: biz.ExecutionStates{Compute: biz.ComputeStateAccepted, Delivery: biz.DeliveryStatePending, Close: biz.CloseStateOpen, Resource: biz.ResourceStateNotApplicable}},
		Runtime: biz.ExecutionRuntime{},
	}
	view, err := executionView(record)
	if err != nil {
		t.Fatal(err)
	}
	if view.Kind.String() != "EXECUTION_KIND_FINETUNING" {
		t.Fatalf("kind=%v", view.Kind)
	}
	base := view.BaseModel
	if base == nil {
		t.Fatal("base model missing from the finetuning detail")
	}
	want := snapshot.Finetuning.BaseModel
	if base.ModelId != want.ModelID || base.VersionId != want.VersionID || base.Format != want.Format {
		t.Fatalf("base model=%+v, want identity of %+v", base, want)
	}
	// The identity message has no field for storage facts at all: the check is
	// that the published value carries no scope or file data.
	if want.Scope.Bucket == "" || want.Object.Key == "" || len(want.Files) == 0 {
		t.Fatal("fixture no longer carries storage facts, so the leak check proves nothing")
	}

	// General training has no base model.
	general := conformance.SnapshotV1()
	generalHash, err := general.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plain, err := executionView(biz.QueryRecord{
		Execution: biz.Execution{Admission: biz.Admission{
			TenantID: general.IdentityRef.TenantID, Actor: general.IdentityRef.Actor,
			OperationID: general.IdentityRef.OperationID, ExecutionID: general.IdentityRef.ExecutionID,
			Intent: conformance.IntentV1(), IntentHash: general.IdentityRef.IntentHash, Snapshot: general, SpecHash: generalHash,
			AcceptedAt: general.Timing.AcceptedAt,
		}, States: biz.ExecutionStates{Compute: biz.ComputeStateAccepted, Delivery: biz.DeliveryStatePending, Close: biz.CloseStateOpen, Resource: biz.ResourceStateNotApplicable}},
		Runtime: biz.ExecutionRuntime{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plain.BaseModel != nil {
		t.Fatalf("general training published a base model: %+v", plain.BaseModel)
	}
}

package protobuf_test

import (
	"bytes"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	contractproto "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
)

func TestCurrentSnapshotPreservesFrozenLimitsAndDatasetOverWire(t *testing.T) {
	for _, snapshot := range []cpup01.Snapshot{conformance.SnapshotV1(), conformance.FinetuningSnapshotV1()} {
		want, err := snapshot.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		wire, err := contractproto.EncodeSnapshot(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		got, err := contractproto.DecodeSnapshot(wire)
		if err != nil {
			t.Fatalf("current snapshot lost mandatory frozen facts: %v", err)
		}
		canonical, err := got.Canonical()
		if err != nil || !bytes.Equal(canonical, want) {
			t.Fatalf("frozen snapshot identity changed over RPC: %v", err)
		}
	}
}

func TestCurrentTemplateSurvivesAuthenticatedWireRoundTrip(t *testing.T) {
	intent := conformance.FinetuningIntentV1()
	name := "native_model_template"
	intent.FinetuningTemplate = &name
	wire, err := contractproto.EncodeIntent(intent)
	if err != nil || wire.FinetuningTemplate == nil || *wire.FinetuningTemplate != name {
		t.Fatalf("wire loses explicit template: %#v, %v", wire, err)
	}
	decoded, err := contractproto.DecodeIntent(wire)
	_, expected, _ := cpup01.CanonicalIntent(intent)
	_, actual, hashErr := cpup01.CanonicalIntent(decoded)
	if err != nil || hashErr != nil || actual != expected {
		t.Fatalf("canonical identity changed over wire: %v, %v", err, hashErr)
	}
}

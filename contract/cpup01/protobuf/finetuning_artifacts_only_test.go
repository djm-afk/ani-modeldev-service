package protobuf_test

import (
	"bytes"
	"testing"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	codec "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"google.golang.org/protobuf/proto"
)

func TestFinetuningArtifactsOnlyTypedWirePreservesCanonicalChoice(t *testing.T) {
	intent := conformance.FinetuningIntentV1()
	intent.NewModelName = nil
	originalIntent, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	message, err := codec.EncodeIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var received modeldevv1.UserIntent
	if err := proto.Unmarshal(wire, &received); err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.DecodeIntent(&received)
	if err != nil {
		t.Fatal(err)
	}
	after, afterHash, err := cpup01.CanonicalIntent(decoded)
	if err != nil || !bytes.Equal(after, originalIntent) || afterHash != intentHash || decoded.TargetModelID != nil || decoded.NewModelName != nil {
		t.Fatal("typed wire changed artifacts-only intent", err)
	}
	snapshot := conformance.FinetuningSnapshotV1()
	snapshot.Finetuning.NewModelName = nil
	snapshot.IdentityRef.IntentHash = intentHash
	originalSnapshot, err := snapshot.Canonical()
	if err != nil {
		t.Fatalf("artifacts-only snapshot must encode without a target: %v", err)
	}
	encoded, err := codec.EncodeSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	wire, err = proto.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var receivedSnapshot modeldevv1.ExecutionSnapshot
	if err := proto.Unmarshal(wire, &receivedSnapshot); err != nil {
		t.Fatal(err)
	}
	decodedSnapshot, err := codec.DecodeSnapshot(&receivedSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	after, err = decodedSnapshot.Canonical()
	if err != nil || !bytes.Equal(after, originalSnapshot) || decodedSnapshot.Finetuning.TargetModelID != nil || decodedSnapshot.Finetuning.NewModelName != nil {
		t.Fatal("typed wire changed artifacts-only snapshot", err)
	}
	for _, corrupt := range []func(*modeldevv1.UserIntent){
		func(m *modeldevv1.UserIntent) { empty := ""; m.TargetModelId = &empty },
		func(m *modeldevv1.UserIntent) { empty := ""; m.NewModelName = &empty },
		func(m *modeldevv1.UserIntent) { m.GetSftLora().ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 1}) },
	} {
		bad := proto.Clone(message).(*modeldevv1.UserIntent)
		corrupt(bad)
		if _, err := codec.DecodeIntent(bad); err == nil {
			t.Fatal("artifacts-only branch accepted an empty typed target or nested unknown field")
		}
	}
	badSnapshot := proto.Clone(encoded).(*modeldevv1.ExecutionSnapshot)
	badSnapshot.Finetuning.ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 1})
	if _, err := codec.DecodeSnapshot(badSnapshot); err == nil {
		t.Fatal("artifacts-only snapshot accepted a nested unknown field")
	}
}

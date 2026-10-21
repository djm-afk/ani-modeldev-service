package protobuf_test

import (
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	codec "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"testing"
)

func TestFinetuningWireChecksNestedUnknownAndTypedBranch(t *testing.T) {
	m, e := codec.EncodeIntent(conformance.FinetuningIntentV1())
	if e != nil {
		t.Fatal(e)
	}
	m.GetSftLora().ProtoReflect().SetUnknown([]byte{0x50, 1})
	if _, e := codec.DecodeIntent(m); e == nil {
		t.Fatal("nested unknown accepted")
	}
	snapshot, e := codec.EncodeSnapshot(conformance.FinetuningSnapshotV1())
	if e != nil {
		t.Fatal(e)
	}
	snapshot.Finetuning.ManagedSettings.LoraRank++
	if _, e := codec.DecodeSnapshot(snapshot); e == nil {
		t.Fatal("managed parameter drift accepted")
	}
}

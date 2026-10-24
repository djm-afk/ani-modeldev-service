package protobuf_test

import (
	"bytes"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	codec "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestSingleTypedWireRoundTripPreservesEveryParameterAndSnapshotHash(t *testing.T) {
	for _, i := range []cpup01.Intent{conformance.IntentV1(), conformance.FinetuningIntentV1()} {
		m, e := codec.EncodeIntent(i)
		if e != nil {
			t.Fatal(e)
		}
		wire, e := proto.Marshal(m)
		if e != nil {
			t.Fatal(e)
		}
		var message modeldevv1.UserIntent
		if e := proto.Unmarshal(wire, &message); e != nil {
			t.Fatal(e)
		}
		got, e := codec.DecodeIntent(&message)
		if e != nil {
			t.Fatal(e)
		}
		before, bh, _ := cpup01.CanonicalIntent(i)
		after, ah, _ := cpup01.CanonicalIntent(got)
		if bh != ah || !bytes.Equal(before, after) {
			t.Fatal("intent wire changed canonical")
		}
	}
	for _, s := range []cpup01.Snapshot{conformance.SnapshotV1(), conformance.FinetuningSnapshotV1()} {
		m, e := codec.EncodeSnapshot(s)
		if e != nil {
			t.Fatal(e)
		}
		wire, e := proto.Marshal(m)
		if e != nil {
			t.Fatal(e)
		}
		var message modeldevv1.ExecutionSnapshot
		if e := proto.Unmarshal(wire, &message); e != nil {
			t.Fatal(e)
		}
		got, e := codec.DecodeSnapshot(&message)
		if e != nil {
			t.Fatal(e)
		}
		before, _ := s.Canonical()
		after, _ := got.Canonical()
		if !bytes.Equal(before, after) {
			t.Fatal("snapshot wire changed canonical")
		}
	}
}
func TestWireRejectsRetiredAndUnknownFields(t *testing.T) {
	m, _ := codec.EncodeIntent(conformance.IntentV1())
	for _, wire := range [][]byte{{0x1a, 1, 'x'}, {0x2a, 1, 'x'}, {0x4a, 0}} {
		v := proto.Clone(m).(*modeldevv1.UserIntent)
		v.ProtoReflect().SetUnknown(wire)
		if _, e := codec.DecodeIntent(v); e == nil {
			t.Fatal("reserved/unknown intent accepted")
		}
	}
	m.Parameters = nil
	if _, e := codec.DecodeIntent(m); e == nil {
		t.Fatal("missing typed parameter object accepted")
	}
	snapshot, _ := codec.EncodeSnapshot(conformance.SnapshotV1())
	snapshot.RuntimePlan.ExpectedSpecSha256 = "wrong"
	if _, e := codec.DecodeSnapshot(snapshot); e == nil {
		t.Fatal("runtime drift accepted")
	}
}
func TestOfficialSelectionUsesProviderCanonicalAndDigest(t *testing.T) {
	s := conformance.SnapshotV1()
	origin, e := codec.DecodeComputeSelection([]byte(s.ComputeOrigin.SelectionJSON), s.ComputeOrigin.SelectionDigest)
	if e != nil || origin != s.ComputeOrigin {
		t.Fatal("formal selection decode", e)
	}
	if _, e := codec.DecodeComputeSelection([]byte(s.ComputeOrigin.SelectionJSON), "wrong"); e == nil {
		t.Fatal("invalid selection accepted")
	}
}

func TestWireRejectsOutOfRangeAndUnnormalizedTimingTimestamps(t *testing.T) {
	for _, mutate := range []func(*modeldevv1.ExecutionSnapshot){
		func(s *modeldevv1.ExecutionSnapshot) { s.Timing.AcceptedAt.Nanos = 1_000_000_000 },
		func(s *modeldevv1.ExecutionSnapshot) { s.Timing.ExecutionDeadlineAt.Nanos = -1 },
		func(s *modeldevv1.ExecutionSnapshot) { s.Timing.AcceptedAt.Seconds = 253402300800 },
	} {
		message, err := codec.EncodeSnapshot(conformance.SnapshotV1())
		if err != nil {
			t.Fatal(err)
		}
		mutate(message)
		if _, err := codec.DecodeSnapshot(message); err == nil {
			t.Fatal("invalid protobuf timing timestamp accepted")
		}
	}
}

package cpup01_test

import (
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"testing"
)

func admissionFixture() cpup01.AdmissionEnvelope {
	s := conformance.SnapshotV1()
	i := conformance.IntentV1()
	_, h, _ := cpup01.CanonicalIntent(i)
	spec, _ := s.Digest()
	return cpup01.AdmissionEnvelope{TenantID: s.IdentityRef.TenantID, Actor: s.IdentityRef.Actor, ExecutionID: s.IdentityRef.ExecutionID, OperationID: s.IdentityRef.OperationID, Intent: i, IntentHash: h, Snapshot: s, SpecHash: spec, AcceptedAt: s.Timing.AcceptedAt}
}
func TestAdmissionEnvelopeBindsOriginalCandidate(t *testing.T) {
	a := admissionFixture()
	if _, _, e := a.CanonicalPayloads(); e != nil {
		t.Fatal(e)
	}
	a.OperationID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if _, _, e := a.CanonicalPayloads(); e == nil {
		t.Fatal("different operation accepted")
	}
	a = admissionFixture()
	a.Snapshot.IdentityRef.IntentHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	a.SpecHash, _ = a.Snapshot.Digest()
	if _, _, e := a.CanonicalPayloads(); e == nil {
		t.Fatal("different intent binding accepted")
	}
}
func TestCandidateCanonicalRoundTripRejectsWrongHash(t *testing.T) {
	s := conformance.SnapshotV1()
	s.RuntimePlan = cpup01.RuntimePlan{}
	c := cpup01.PreparedCandidate{SchemaVersion: cpup01.CandidateSchemaVersion, Intent: conformance.IntentV1(), Snapshot: s}
	raw, e := c.Canonical()
	if e != nil {
		t.Fatal(e)
	}
	h, _ := c.Digest()
	if _, e := cpup01.DecodePreparedCandidate(raw, h); e != nil {
		t.Fatal(e)
	}
	if _, e := cpup01.DecodePreparedCandidate(raw, "wrong"); e == nil {
		t.Fatal("wrong candidate hash accepted")
	}
}

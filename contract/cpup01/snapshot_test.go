package cpup01_test

import (
	"bytes"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"testing"
	"time"
)

func snapshotFixture() cpup01.Snapshot { return conformance.SnapshotV1() }
func TestSnapshotFreezesCompletePlanAndRejectsDrift(t *testing.T) {
	s := snapshotFixture()
	raw, e := s.Canonical()
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(raw, []byte(`"release"`)) || bytes.Contains(raw, []byte(`"accepted_binding_generation"`)) {
		t.Fatal("retired contract in snapshot")
	}
	for _, change := range []func(*cpup01.Snapshot){func(s *cpup01.Snapshot) { s.Input.RowCount = 0 }, func(s *cpup01.Snapshot) { s.Resources.RequestMillicpu++ }, func(s *cpup01.Snapshot) { s.Timing.StepTimeoutSeconds = 0 }, func(s *cpup01.Snapshot) { s.DeadlineAt = s.DeadlineAt.Add(time.Second) }, func(s *cpup01.Snapshot) { s.RuntimePlan.ExpectedSpec = []byte(`{}`) }, func(s *cpup01.Snapshot) { s.RuntimePlan.Name = "other" }, func(s *cpup01.Snapshot) { s.IdentityRef.IntentHash = "" }} {
		v := snapshotFixture()
		change(&v)
		if v.Validate() == nil {
			t.Fatal("drift accepted")
		}
	}
	s.Input.RowCount = 11
	s.Input.PreparedDataset.RowCount = 11
	if s.Validate() != nil {
		t.Fatal("finite nonbaseline dataset rejected")
	}
}
func TestTimingHasOneFrozenWindowWithoutRenewal(t *testing.T) {
	start := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	v, e := (cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: 240, ExecutionTimeoutMaxSeconds: 7200, StepTimeoutSeconds: 90}).Resolve(start, nil)
	if e != nil {
		t.Fatal(e)
	}
	deadline, e := v.DeadlineFor(start.Add(180 * time.Second))
	if e != nil || !deadline.Equal(start.Add(240*time.Second)) {
		t.Fatal("step extended total deadline")
	}
	for _, c := range []cpup01.TimingConfiguration{{}, {ExecutionTimeoutDefaultSeconds: 2, ExecutionTimeoutMaxSeconds: 1, StepTimeoutSeconds: 1}, {ExecutionTimeoutDefaultSeconds: 1, ExecutionTimeoutMaxSeconds: 2, StepTimeoutSeconds: 3}, {ExecutionTimeoutDefaultSeconds: 1, ExecutionTimeoutMaxSeconds: ^uint64(0), StepTimeoutSeconds: 1}} {
		if _, e := c.Resolve(start, nil); e == nil {
			t.Fatal("invalid timing configuration accepted")
		}
	}
}

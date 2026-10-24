// Package admissionfixture binds authored test facts to the single active
// admission envelope. It supplies no transport, authorization or persistence.
package admissionfixture

import (
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// Bind is used only when constructing a valid fixture, never when asserting
// rejection of a deliberately incoherent admission.
func Bind(t testing.TB, a biz.Admission) biz.Admission {
	t.Helper()
	_, hash, err := cpup01.CanonicalIntent(a.Intent)
	if err != nil {
		t.Fatal("fixture intent", err)
	}
	a.IntentHash = hash
	a.Snapshot.IdentityRef = cpup01.IdentityRef{TenantID: a.TenantID, Actor: a.Actor, OperationID: a.OperationID, ExecutionID: a.ExecutionID, IntentHash: hash}
	seconds := uint64((a.Snapshot.DeadlineAt.Sub(a.AcceptedAt) + time.Second - 1) / time.Second)
	if seconds == 0 {
		t.Fatal("fixture requires positive execution lifetime")
	}
	step := a.Snapshot.Timing.StepTimeoutSeconds
	if step == 0 {
		step = 900
	}
	maximum := a.Snapshot.Timing.ExecutionTimeoutMaxSeconds
	if maximum < seconds {
		maximum = seconds
	}
	configuration := cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: seconds, ExecutionTimeoutMaxSeconds: maximum, StepTimeoutSeconds: step}
	timing, err := configuration.Resolve(a.AcceptedAt, a.Intent.TimeoutSeconds)
	if err != nil {
		t.Fatal("fixture timing", err)
	}
	a.Snapshot.Timing, a.Snapshot.DeadlineAt = timing, timing.ExecutionDeadlineAt
	a.Snapshot.RuntimePlan, err = biz.CompileRuntimePlan(a.Snapshot)
	if err != nil {
		t.Fatal("fixture runtime", err)
	}
	a.SpecHash, err = a.Snapshot.Digest()
	if err != nil {
		t.Fatal("fixture snapshot", err)
	}
	raw, err := a.Snapshot.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshot, err = cpup01.DecodeCanonicalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

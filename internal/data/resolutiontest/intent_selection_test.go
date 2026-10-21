package resolutiontest

import (
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"testing"
)

func TestPreparePresenceOriginalIdentityAndFinalizeMismatchBoundaries(t *testing.T) {
	f := prepareResolution(t)
	omitted := f.request
	omitted.Intent.MLP = &cpup01.MLPParameters{}
	rehashRequest(&omitted)
	first, e := f.resolver.Prepare(f.ctx, omitted)
	if e != nil {
		t.Fatal(e)
	}
	explicit := omitted
	epochs := int64(3)
	explicit.Intent.MLP = &cpup01.MLPParameters{Epochs: &epochs}
	rehashRequest(&explicit)
	second, e := f.resolver.Prepare(f.ctx, explicit)
	if e != nil {
		t.Fatal(e)
	}
	if first.Snapshot.IdentityRef.IntentHash == second.Snapshot.IdentityRef.IntentHash {
		t.Fatal("presence collapsed")
	}
	if len(first.Snapshot.Program.ResolvedParameters) != 4 {
		t.Fatal("defaults incomplete")
	}
	for _, change := range []func(*cpup01.PreparedCandidate){func(c *cpup01.PreparedCandidate) {
		c.Snapshot.IdentityRef.IntentHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}, func(c *cpup01.PreparedCandidate) {
		c.Snapshot.ComputeOrigin.ComputePresetID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	}, func(c *cpup01.PreparedCandidate) { c.Snapshot.Implementation.Version = "unrecognized" }, func(c *cpup01.PreparedCandidate) { c.Snapshot.Program.ResolvedParameters[0].Value = "0" }, func(c *cpup01.PreparedCandidate) {
		c.Snapshot.Timing.StepTimeoutSeconds = c.Snapshot.Timing.ExecutionTimeoutMaxSeconds + 1
	}} {
		c := cloneCandidate(t, first)
		change(&c)
		if _, e := f.resolver.Finalize(f.ctx, c, nil); e == nil {
			t.Fatal("inconsistent CPU candidate or GPU mixing accepted")
		}
	}
	wrong := f.request
	wrong.IdentityRef.IntentHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, e := f.resolver.Prepare(f.ctx, wrong); e != biz.ErrInvalidAdmission {
		t.Fatal("scope hash mismatch accepted", e)
	}
	requireNoResolvedExecution(t, f)
}

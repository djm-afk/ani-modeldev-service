package biz_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

type deadlineAdmissionBoundary struct {
	material cpup01.ImplementationMaterial
	facts    biz.TenantAdmissionFacts
	observed time.Time
}

func (b *deadlineAdmissionBoundary) ReadImplementationMaterial(context.Context, string, string) (cpup01.ImplementationMaterial, error) {
	return b.material, nil
}
func (b *deadlineAdmissionBoundary) ReadAdmissionFacts(context.Context, string, string) (biz.TenantAdmissionFacts, error) {
	return b.facts, nil
}
func (b *deadlineAdmissionBoundary) Get(ctx context.Context, _, _ string) (biz.InputVersion, error) {
	b.observed, _ = ctx.Deadline()
	<-ctx.Done()
	return biz.InputVersion{}, ctx.Err()
}

func TestPrepareConsumesFrozenExecutionDeadlineWithoutRenewal(t *testing.T) {
	snapshot := conformance.SnapshotV1()
	intent := conformance.IntentV1()
	seconds := uint64(1)
	intent.TimeoutSeconds = &seconds
	_, hash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	identity := snapshot.IdentityRef
	identity.IntentHash = hash
	accepted := time.Now().UTC().Truncate(time.Microsecond).Add(-900 * time.Millisecond)
	boundary := &deadlineAdmissionBoundary{material: conformance.MaterialV21(), facts: biz.TenantAdmissionFacts{TenantID: identity.TenantID, Workspace: snapshot.Workspace, Timing: cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: 3600, ExecutionTimeoutMaxSeconds: 7200, StepTimeoutSeconds: 900}}}
	resolver := biz.NewAdmissionResolver(boundary, boundary, boundary, snapshot.Limits)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err = resolver.Prepare(ctx, biz.PrepareAdmissionInput{IdentityRef: identity, Intent: intent, ComputeOrigin: snapshot.ComputeOrigin, AcceptedAt: accepted})
	if !errors.Is(err, context.DeadlineExceeded) || !boundary.observed.Equal(accepted.Add(time.Second)) {
		t.Fatalf("Prepare renewed/lost execution budget: err=%v observed=%v frozen=%v", err, boundary.observed, accepted.Add(time.Second))
	}
}

func TestFinalizeRejectsExpiredOriginalCandidateWithoutRenewal(t *testing.T) {
	snapshot := conformance.SnapshotV1()
	snapshot.RuntimePlan = cpup01.RuntimePlan{}
	candidate := cpup01.PreparedCandidate{SchemaVersion: cpup01.CandidateSchemaVersion, Intent: conformance.IntentV1(), Snapshot: snapshot}
	if err := candidate.Validate(); err != nil {
		t.Fatal("invalid tracer candidate", err)
	}
	resolver := biz.NewAdmissionResolver(nil, nil, nil, snapshot.Limits)
	if _, err := resolver.Finalize(context.Background(), candidate, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired candidate was finalized: %v", err)
	}
}

package submission_test

import (
	"bytes"
	"context"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestReserveSubmissionCommitsOnePermitAcrossIndependentPools(t *testing.T) {
	openPool := postgres.Prepare(t)
	request := validDispatchRequest(t)
	expectedPlan, err := request.Freeze()
	if err != nil {
		t.Fatalf("invalid dispatch fixture: %v", err)
	}
	expectedCanonical, err := expectedPlan.Canonical()
	if err != nil {
		t.Fatalf("canonical dispatch fixture: %v", err)
	}
	expectedHash, err := expectedPlan.Digest()
	if err != nil {
		t.Fatalf("dispatch fixture hash: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admissionPool := openPool()
	if _, err := execution.New(admissionPool).Accept(ctx, request.Admission); err != nil {
		t.Fatalf("real Admission setup failed; reservation behavior NOT_RUN: %v", err)
	}
	admissionPool.Close()

	firstPool, secondPool := openPool(), openPool()
	repositories := []*submission.Repository{submission.New(firstPool), submission.New(secondPool)}
	type result struct {
		reservation biz.PipelineDispatchReservation
		err         error
	}
	start := make(chan struct{})
	results := make(chan result, len(repositories))
	for _, repository := range repositories {
		go func(repository *submission.Repository) {
			<-start
			reservation, err := repository.Reserve(ctx, request)
			results <- result{reservation: reservation, err: err}
		}(repository)
	}
	close(start)
	var first biz.PipelineDispatch
	permits := 0
	for range repositories {
		var got result
		select {
		case got = <-results:
		case <-ctx.Done():
			t.Fatal("CPU07_RESERVATION_BEHAVIOR: two-pool reservation did not finish")
		}
		if got.err != nil {
			t.Errorf("CPU07_RESERVATION_BEHAVIOR: Reserve rejected a committed valid admission: %v", got.err)
			continue
		}
		dispatch := got.reservation.Dispatch
		assertFrozenDispatch(t, dispatch, request, expectedCanonical, expectedHash)
		if first.AttemptID == "" {
			first = dispatch
		} else if dispatch.AttemptID != first.AttemptID || !dispatch.ReservedAt.Equal(first.ReservedAt) {
			t.Error("concurrent reservation changed the original attempt or reservation time")
		}
		if permit := got.reservation.SendPermit; permit != nil {
			permits++
			if permit.TenantID != expectedPlan.TenantID || permit.ExecutionID != expectedPlan.ExecutionID || permit.AttemptID != dispatch.AttemptID || permit.PlanHash != expectedHash {
				t.Error("send permit is not bound to the exact frozen dispatch")
			}
			// This connection is independent of both competitors. Visibility here
			// proves the reservation committed before its send permit was returned.
			readerPool := openPool()
			visible, err := submission.New(readerPool).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
			readerPool.Close()
			if err != nil {
				t.Errorf("send permit returned before independently readable SUBMITTING: %v", err)
			} else {
				assertFrozenDispatch(t, visible, request, expectedCanonical, expectedHash)
				if visible.AttemptID != dispatch.AttemptID || !visible.ReservedAt.Equal(dispatch.ReservedAt) {
					t.Error("independent reader did not observe the permit's original reservation")
				}
			}
		}
	}
	if permits != 1 {
		t.Fatalf("CPU07_RESERVATION_BEHAVIOR: send permits = %d, want exactly one", permits)
	}
	firstPool.Close()
	secondPool.Close()
	restarted := submission.New(openPool())
	replay, err := restarted.Reserve(ctx, request)
	if err != nil {
		t.Fatalf("reconnected reservation replay: %v", err)
	}
	if replay.SendPermit != nil {
		t.Fatal("reconnected replay granted another send permit")
	}
	assertFrozenDispatch(t, replay.Dispatch, request, expectedCanonical, expectedHash)
	if replay.Dispatch.AttemptID != first.AttemptID || !replay.Dispatch.ReservedAt.Equal(first.ReservedAt) {
		t.Fatal("reconnected replay replaced the original reservation")
	}
	admitted, err := execution.New(openPool()).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil {
		t.Fatalf("read original admission after reservation: %v", err)
	}
	beforeIntent, beforeSnapshot, _ := request.Admission.CanonicalPayloads()
	afterIntent, afterSnapshot, err := admitted.CanonicalPayloads()
	if err != nil || !bytes.Equal(beforeIntent, afterIntent) || !bytes.Equal(beforeSnapshot, afterSnapshot) || admitted.SpecHash != request.Admission.SpecHash {
		t.Fatal("dispatch reservation rewrote the immutable shared admission")
	}
}

func assertFrozenDispatch(t *testing.T, got biz.PipelineDispatch, request biz.PipelineDispatchRequest, canonical []byte, hash string) {
	t.Helper()
	id, err := uuid.Parse(got.AttemptID)
	if err != nil || id == uuid.Nil || id.String() != got.AttemptID {
		t.Error("reservation has no canonical nonzero attempt UUID")
	}
	actual, err := got.Plan.Canonical()
	if err != nil || !bytes.Equal(actual, canonical) || got.PlanHash != hash {
		t.Error("reservation changed the complete frozen dispatch plan or its hash")
	}
	if got.Plan.Owner != request.Owner || got.Plan.Environment != request.Admission.Snapshot.Environment ||
		got.Plan.PipelineID != request.Admission.Snapshot.Implementation.PipelineID || got.Plan.PipelineVersionID != request.Admission.Snapshot.Implementation.PipelineVersionID ||
		got.Plan.SpecHash != request.Admission.SpecHash || !got.Plan.DeadlineAt.Equal(request.Admission.Snapshot.DeadlineAt) {
		t.Error("reservation did not retain the explicit owner revision/root and original admission's execution settings")
	}
	if got.State != biz.PipelineDispatchSubmitting || got.ReservedAt.IsZero() {
		t.Error("reservation is not a durable SUBMITTING fact with a reservation time")
	}
}

func validDispatchRequest(t *testing.T) biz.PipelineDispatchRequest {
	t.Helper()
	snapshot := conformance.SnapshotV1()
	acceptedAt := time.Now().UTC().Truncate(time.Microsecond)
	snapshot.DeadlineAt = acceptedAt.Add(time.Hour)
	intent := cpup01.Intent{Name: "durable-dispatch", Kind: "GENERAL_TRAINING", ImplementationID: snapshot.Implementation.ImplementationID, ComputePresetID: snapshot.ComputeOrigin.ComputePresetID, MLP: &cpup01.MLPParameters{}, DatasetVersionID: snapshot.Input.InputVersionID}
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatalf("intent fixture: %v", err)
	}
	specHash := ""
	return biz.PipelineDispatchRequest{
		Admission: admissionfixture.Bind(t, biz.Admission{
			TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:user:42",
			OperationID: "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff", ExecutionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
			Intent: intent, IntentHash: intentHash, Snapshot: snapshot, SpecHash: specHash, AcceptedAt: acceptedAt,
		}),
		Owner: biz.PipelineOwnerConfiguration{Reference: "cpu07-fixture-owner", RevisionSHA256: strings.Repeat("a", 64), PipelineRoot: "s3://fixture-kfp-artifacts/managed-root"},
	}
}

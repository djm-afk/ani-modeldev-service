package submission_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestRunAuthorityBindsOnceAfterRealSubmissionAndSurvivesReconnect(t *testing.T) {
	openPool := postgres.Prepare(t)
	request := validDispatchRequest(t, openPool())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admissionPool, writerPool := openPool(), openPool()
	admissions, writer := execution.New(admissionPool), submission.New(writerPool)
	accepted, err := admissions.Accept(ctx, request.Admission)
	if err != nil || accepted.Replayed {
		t.Fatalf("CPU07_AUTHORITY_PREFLIGHT: real Admission failed; authority behavior NOT_RUN: %v", err)
	}
	reserved, err := writer.Reserve(ctx, request)
	if err != nil || reserved.SendPermit == nil {
		t.Fatalf("CPU07_AUTHORITY_PREFLIGHT: real reservation lacks its original permit; authority behavior NOT_RUN: %v", err)
	}
	permit := *reserved.SendPermit
	observedAt, err := writer.SubmissionObservationTime(ctx, permit)
	if err != nil {
		t.Fatalf("CPU07_AUTHORITY_PREFLIGHT: recording time unavailable; authority behavior NOT_RUN: %v", err)
	}
	confirmed, err := writer.RecordSubmissionConfirmed(ctx, permit, confirmedObservation(confirmedRunID), observedAt)
	if err != nil || confirmed.ConflictingRuns || len(confirmed.Dispatch.ConfirmedRuns) != 1 || confirmed.Dispatch.ConfirmedRuns[0].RunID != confirmedRunID {
		t.Fatalf("CPU07_AUTHORITY_PREFLIGHT: real confirmed Run persistence failed; authority behavior NOT_RUN: %v", err)
	}
	// Only the external Run/Workflow identity is synthetic. Admission,
	// reservation, observation and the authority CAS use real business code and
	// restricted PostgreSQL. This does not prove a live managed step or Trainer.
	candidate := biz.RunAuthorityCandidate{
		TenantID: request.Admission.TenantID, ExecutionID: request.Admission.ExecutionID,
		OperationID: request.Admission.OperationID, SpecHash: request.Admission.SpecHash,
		AttemptID: permit.AttemptID, PlanHash: permit.PlanHash, RunID: confirmedRunID,
		NamespaceName: request.Admission.Snapshot.Environment.NamespaceName,
		NamespaceUID:  request.Admission.Snapshot.Environment.NamespaceUID,
		WorkflowName:  "cpu07-authority-fixture", WorkflowUID: "cccccccc-dddd-4eee-8fff-111111111111",
	}
	t.Log("CPU07_AUTHORITY_PREFLIGHT PASS: real Admission, reservation and confirmed Run committed; external Run/Workflow identity is a synthetic boundary")
	first, err := writer.BindRunAuthority(ctx, candidate)
	if err != nil {
		t.Fatalf("CPU07_AUTHORITY_BEHAVIOR: first eligible Run could not bind authority: %v", err)
	}
	if first.Replayed || first.RunAuthorityCandidate != candidate || first.BoundAt.IsZero() || first.BoundAt.Before(observedAt) || first.BoundAt.Nanosecond()%1000 != 0 || first.OwnerRevision != confirmed.Dispatch.OwnerRevision+1 {
		t.Fatal("CPU07_AUTHORITY_BEHAVIOR: first binding lost its association, database time or single aggregate revision advance")
	}
	readerPool := openPool()
	reader := submission.New(readerPool)
	visible, err := reader.GetRunAuthority(ctx, candidate.TenantID, candidate.ExecutionID)
	if err != nil || visible.RunAuthorityCandidate != candidate || !visible.BoundAt.Equal(first.BoundAt) || visible.OwnerRevision != first.OwnerRevision {
		t.Fatalf("CPU07_AUTHORITY_BEHAVIOR: receipt returned without the exact binding being independently visible: %v", err)
	}
	admitted, err := execution.New(readerPool).Get(ctx, candidate.TenantID, candidate.ExecutionID)
	if err != nil || admitted.OwnerRevision != first.OwnerRevision {
		t.Fatalf("CPU07_AUTHORITY_BEHAVIOR: authority binding and execution revision were not committed together: %v", err)
	}
	writerPool.Close()
	admissionPool.Close()
	restarted := submission.New(openPool())
	replayed, err := restarted.BindRunAuthority(ctx, candidate)
	if err != nil || !replayed.Replayed || replayed.RunAuthorityCandidate != candidate || !replayed.BoundAt.Equal(first.BoundAt) || replayed.OwnerRevision != first.OwnerRevision {
		t.Fatalf("CPU07_AUTHORITY_BEHAVIOR: same Run after reconnect changed authority, first binding time or aggregate revision: %v", err)
	}
	visible, err = reader.GetRunAuthority(ctx, candidate.TenantID, candidate.ExecutionID)
	if err != nil || visible.RunAuthorityCandidate != candidate || !visible.BoundAt.Equal(first.BoundAt) || visible.OwnerRevision != first.OwnerRevision {
		t.Fatalf("CPU07_AUTHORITY_BEHAVIOR: replay mutated the independently visible authority: %v", err)
	}
	// Retaining a distinct confirmed Run is legitimate evidence, not permission
	// to replace the winner. Its observation advances revision; rejected CAS must
	// not add another advance or erase either confirmed observation.
	second := candidate
	second.RunID = "66666666-7777-4888-8999-aaaaaaaaaaaa"
	second.WorkflowName = "cpu07-second-workflow-fixture"
	second.WorkflowUID = "dddddddd-eeee-4fff-8111-222222222222"
	secondObservedAt, err := restarted.SubmissionObservationTime(ctx, permit)
	if err != nil {
		t.Fatalf("CPU07_AUTHORITY_BEHAVIOR: second observation recording time unavailable: %v", err)
	}
	secondConfirmed, err := restarted.RecordSubmissionConfirmed(ctx, permit, confirmedObservation(second.RunID), secondObservedAt)
	if err != nil || !secondConfirmed.ConflictingRuns || len(secondConfirmed.Dispatch.ConfirmedRuns) != 2 || secondConfirmed.Dispatch.OwnerRevision != first.OwnerRevision+1 {
		t.Fatalf("CPU07_AUTHORITY_BEHAVIOR: distinct Run evidence was not independently retained: %v", err)
	}
	rejected, err := restarted.BindRunAuthority(ctx, second)
	if !errors.Is(err, biz.ErrRunAuthorityConflict) || rejected != (biz.RunAuthorityReceipt{}) {
		t.Fatalf("CPU07_AUTHORITY_BEHAVIOR: second Run received authority or a successful receipt: %v", err)
	}
	visible, err = reader.GetRunAuthority(ctx, candidate.TenantID, candidate.ExecutionID)
	if err != nil || visible.RunAuthorityCandidate != candidate || !visible.BoundAt.Equal(first.BoundAt) || visible.OwnerRevision != secondConfirmed.Dispatch.OwnerRevision {
		t.Fatalf("CPU07_AUTHORITY_BEHAVIOR: rejected second Run changed the original authority or aggregate revision: %v", err)
	}
	dispatch, err := reader.Get(ctx, candidate.TenantID, candidate.ExecutionID)
	if err != nil || dispatch.OwnerRevision != secondConfirmed.Dispatch.OwnerRevision || len(dispatch.ConfirmedRuns) != 2 {
		t.Fatalf("CPU07_AUTHORITY_BEHAVIOR: rejected second Run lost evidence or advanced aggregate revision: %v", err)
	}
}

func TestRunAuthorityCannotBindAfterCloseOrAcrossFrozenScope(t *testing.T) {
	for _, test := range []struct {
		name           string
		close          bool
		otherTenant    bool
		otherNamespace bool
	}{
		{name: "closed before Begin", close: true},
		{name: "another tenant", otherTenant: true},
		{name: "recreated namespace", otherNamespace: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			openPool := postgres.Prepare(t)
			request := validDispatchRequest(t, openPool())
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			pool := openPool()
			admissions, repository := execution.New(pool), submission.New(pool)
			if _, err := admissions.Accept(ctx, request.Admission); err != nil {
				t.Fatalf("admission setup: %v", err)
			}
			reserved, err := repository.Reserve(ctx, request)
			if err != nil || reserved.SendPermit == nil {
				t.Fatalf("reservation setup: %v", err)
			}
			candidate := biz.RunAuthorityCandidate{
				TenantID: request.Admission.TenantID, ExecutionID: request.Admission.ExecutionID,
				OperationID: request.Admission.OperationID, SpecHash: request.Admission.SpecHash,
				AttemptID: reserved.Dispatch.AttemptID, PlanHash: reserved.Dispatch.PlanHash, RunID: confirmedRunID,
				NamespaceName: request.Admission.Snapshot.Environment.NamespaceName,
				NamespaceUID:  request.Admission.Snapshot.Environment.NamespaceUID,
				WorkflowName:  "authority-refusal-fixture", WorkflowUID: "cccccccc-dddd-4eee-8fff-111111111111",
			}
			var expected error
			if test.close {
				if _, err := admissions.ApplyCloseIntent(ctx, dispatchCloseIntent(request)); err != nil {
					t.Fatalf("close setup: %v", err)
				}
				expected = biz.ErrPipelineDispatchBlocked
			}
			if test.otherTenant {
				candidate.TenantID = "dddddddd-eeee-4fff-8111-222222222222"
				expected = biz.ErrExecutionNotFound
			}
			if test.otherNamespace {
				candidate.NamespaceUID = "dddddddd-eeee-4fff-8111-222222222222"
				expected = biz.ErrAdmissionConflict
			}
			before, err := admissions.Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
			if err != nil {
				t.Fatalf("read setup: %v", err)
			}
			receipt, err := repository.BindRunAuthority(ctx, candidate)
			if !errors.Is(err, expected) || receipt != (biz.RunAuthorityReceipt{}) {
				t.Fatalf("invalid Begin association received authority: %v", err)
			}
			reader := submission.New(openPool())
			if _, err := reader.GetRunAuthority(ctx, request.Admission.TenantID, request.Admission.ExecutionID); !errors.Is(err, biz.ErrExecutionNotFound) {
				t.Fatalf("rejected Begin left an authority row: %v", err)
			}
			after, err := execution.New(openPool()).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
			if err != nil || after.OwnerRevision != before.OwnerRevision {
				t.Fatalf("rejected Begin changed the aggregate revision: %v", err)
			}
		})
	}
}

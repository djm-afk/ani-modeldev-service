package submission_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

const otherDispatchTenant = "22222222-3333-4444-8555-666666666666"

func TestCloseBeforeReservationRejectsFirstSendPermit(t *testing.T) {
	for _, tombstoneFirst := range []bool{false, true} {
		name := "admission before close"
		if tombstoneFirst {
			name = "close tombstone before late admission"
		}
		t.Run(name, func(t *testing.T) {
			openPool := postgres.Prepare(t)
			request := validDispatchRequest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			admissions := execution.New(openPool())
			if !tombstoneFirst {
				acceptDispatchAdmission(t, ctx, admissions, request)
			}
			closed, err := admissions.ApplyCloseIntent(ctx, dispatchCloseIntent(request))
			if err != nil || closed.State != biz.CloseStateClosing {
				t.Fatalf("committed close fixture: %v", err)
			}
			repository := submission.New(openPool())
			if tombstoneFirst {
				got, err := repository.Reserve(ctx, request)
				assertRejectedReservation(t, got, err, biz.ErrExecutionNotFound)
				assertNoDispatch(t, ctx, repository, request.Admission.TenantID, request.Admission.ExecutionID)
				acceptDispatchAdmission(t, ctx, admissions, request)
			}
			got, err := repository.Reserve(ctx, request)
			assertRejectedReservation(t, got, err, biz.ErrPipelineDispatchBlocked)
			assertNoDispatch(t, ctx, submission.New(openPool()), request.Admission.TenantID, request.Admission.ExecutionID)
			stored, err := admissions.Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
			if err != nil || stored.Close == nil || !reflect.DeepEqual(*stored.Close, closed.CloseRecord) {
				t.Fatal("rejected reservation changed the original closing fact")
			}
			assertSameDispatchAdmission(t, stored.Admission, request.Admission)
		})
	}
}

func TestReservedDispatchAfterCloseReplaysOnlyOriginalFact(t *testing.T) {
	openPool := postgres.Prepare(t)
	request := validDispatchRequest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admissions := execution.New(openPool())
	acceptDispatchAdmission(t, ctx, admissions, request)
	original := reserveFirstDispatch(t, ctx, submission.New(openPool()), request)
	if _, err := admissions.ApplyCloseIntent(ctx, dispatchCloseIntent(request)); err != nil {
		t.Fatalf("close after reservation: %v", err)
	}
	original.OwnerRevision = 3
	assertDispatchReplay(t, ctx, submission.New(openPool()), request, original)
}

func TestReservationRejectsDifferentCompleteAdmissionBeforeAndAfterFirstPermit(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*biz.Admission)
	}{
		{"operation", func(a *biz.Admission) { a.OperationID = "cccccccc-dddd-4eee-8fff-aaaaaaaaaaaa" }},
		{"actor", func(a *biz.Admission) { a.Actor = "governance:user:43" }},
		{"accepted_at", func(a *biz.Admission) { a.AcceptedAt = a.AcceptedAt.Add(time.Microsecond) }},
		{"intent", func(a *biz.Admission) { a.Intent.Name = "different-training" }},
		{"intent presence", func(a *biz.Admission) { seed := uint32(42); a.Intent.MLP = &cpup01.MLPParameters{Seed: &seed} }},
		{"release digest", func(a *biz.Admission) { a.Snapshot.Implementation.PipelineIRSHA256 = strings.Repeat("9", 64) }},
		{"pipeline", func(a *biz.Admission) { a.Snapshot.Implementation.PipelineID = "cccccccc-dddd-4eee-8fff-aaaaaaaaaaaa" }},
		{"pipeline version", func(a *biz.Admission) {
			a.Snapshot.Implementation.PipelineVersionID = "cccccccc-dddd-4eee-8fff-aaaaaaaaaaaa"
		}},
		{"experiment", func(a *biz.Admission) { a.Snapshot.Environment.ExperimentID = "cccccccc-dddd-4eee-8fff-aaaaaaaaaaaa" }},
		{"service account", func(a *biz.Admission) { a.Snapshot.Environment.Identities.KFPStepServiceAccount = "other-managed-step" }},
		{"deadline", func(a *biz.Admission) { a.Snapshot.DeadlineAt = a.Snapshot.DeadlineAt.Add(time.Minute) }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			openPool := postgres.Prepare(t)
			request := validDispatchRequest(t)
			candidate := request
			testCase.mutate(&candidate.Admission)
			refreshDispatchHashes(t, &candidate.Admission)
			if _, err := candidate.Freeze(); err != nil {
				t.Fatalf("conflicting fixture must be individually valid: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			admissions := execution.New(openPool())
			acceptDispatchAdmission(t, ctx, admissions, request)
			repository := submission.New(openPool())
			got, err := repository.Reserve(ctx, candidate)
			assertRejectedReservation(t, got, err, biz.ErrAdmissionConflict)
			assertNoDispatch(t, ctx, repository, request.Admission.TenantID, request.Admission.ExecutionID)
			original := reserveFirstDispatch(t, ctx, repository, request)
			got, err = submission.New(openPool()).Reserve(ctx, candidate)
			assertRejectedReservation(t, got, err, biz.ErrAdmissionConflict)
			assertDispatchReplay(t, ctx, submission.New(openPool()), request, original)
			stored, err := admissions.Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
			if err != nil {
				t.Fatalf("read original admission: %v", err)
			}
			assertSameDispatchAdmission(t, stored.Admission, request.Admission)
		})
	}
}

func TestReservationCannotReplaceFrozenOwnerConfiguration(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*biz.PipelineOwnerConfiguration)
	}{
		{"reference", func(owner *biz.PipelineOwnerConfiguration) { owner.Reference = "other-owner" }},
		{"revision", func(owner *biz.PipelineOwnerConfiguration) { owner.RevisionSHA256 = strings.Repeat("b", 64) }},
		{"root", func(owner *biz.PipelineOwnerConfiguration) {
			owner.PipelineRoot = "s3://other-kfp-artifacts/managed-root"
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			openPool := postgres.Prepare(t)
			request := validDispatchRequest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			acceptDispatchAdmission(t, ctx, execution.New(openPool()), request)
			original := reserveFirstDispatch(t, ctx, submission.New(openPool()), request)
			candidate := request
			testCase.mutate(&candidate.Owner)
			got, err := submission.New(openPool()).Reserve(ctx, candidate)
			assertRejectedReservation(t, got, err, biz.ErrAdmissionConflict)
			assertDispatchReplay(t, ctx, submission.New(openPool()), request, original)
		})
	}
}

func TestReservationAndGetKeepTenantBoundaryAndCanonicalUUIDAliases(t *testing.T) {
	openPool := postgres.Prepare(t)
	request := validDispatchRequest(t)
	request.Admission.TenantID = "aaaaaaaa-2222-4333-8444-555555555555"
	refreshDispatchHashes(t, &request.Admission)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repository := submission.New(openPool())
	got, err := repository.Reserve(ctx, request)
	assertRejectedReservation(t, got, err, biz.ErrExecutionNotFound)
	assertNoDispatch(t, ctx, repository, request.Admission.TenantID, request.Admission.ExecutionID)
	acceptDispatchAdmission(t, ctx, execution.New(openPool()), request)
	alias := request
	alias.Admission.TenantID = strings.ToUpper(alias.Admission.TenantID)
	alias.Admission.ExecutionID = strings.ToUpper(alias.Admission.ExecutionID)
	alias.Admission.OperationID = strings.ToUpper(alias.Admission.OperationID)
	first, err := repository.Reserve(ctx, alias)
	if err != nil || first.SendPermit == nil {
		t.Fatalf("legal UUID alias must share the admitted identity: %v", err)
	}
	if first.SendPermit.TenantID != request.Admission.TenantID || first.SendPermit.ExecutionID != request.Admission.ExecutionID ||
		first.Dispatch.Plan.TenantID != request.Admission.TenantID || first.Dispatch.Plan.ExecutionID != request.Admission.ExecutionID || first.Dispatch.Plan.OperationID != request.Admission.OperationID {
		t.Fatal("UUID aliases were not stored and returned in canonical form")
	}
	assertDispatchReplay(t, ctx, submission.New(openPool()), request, first.Dispatch)
	assertDispatchReplay(t, ctx, submission.New(openPool()), alias, first.Dispatch)
	wrongTenant := request
	wrongTenant.Admission.TenantID = otherDispatchTenant
	refreshDispatchHashes(t, &wrongTenant.Admission)
	got, err = repository.Reserve(ctx, wrongTenant)
	assertRejectedReservation(t, got, err, biz.ErrExecutionNotFound)
	assertNoDispatch(t, ctx, repository, otherDispatchTenant, request.Admission.ExecutionID)
	assertNoDispatch(t, ctx, repository, request.Admission.TenantID, "dddddddd-eeee-4fff-8aaa-bbbbbbbbbbbb")
	malformed, err := repository.Get(ctx, request.Admission.TenantID, strings.ReplaceAll(request.Admission.ExecutionID, "-", "X"))
	if !errors.Is(err, biz.ErrInvalidAdmission) || !reflect.DeepEqual(malformed, biz.PipelineDispatch{}) {
		t.Fatal("malformed UUID separators returned a dispatch")
	}
	assertDispatchReplay(t, ctx, repository, request, first.Dispatch)
}

func dispatchCloseIntent(request biz.PipelineDispatchRequest) biz.CloseIntent {
	return biz.CloseIntent{
		TenantID: request.Admission.TenantID, ExecutionID: request.Admission.ExecutionID, OperationID: request.Admission.OperationID,
		SpecHash: request.Admission.SpecHash, SourceGeneration: 1, Reason: biz.CloseReasonUserStop,
		RequestedAt: request.Admission.AcceptedAt, RequestedActor: request.Admission.Actor,
	}
}

func acceptDispatchAdmission(t *testing.T, ctx context.Context, repository *execution.Repository, request biz.PipelineDispatchRequest) {
	t.Helper()
	if _, err := repository.Accept(ctx, request.Admission); err != nil {
		t.Fatalf("real Admission fixture failed; reservation behavior NOT_RUN: %v", err)
	}
}

func reserveFirstDispatch(t *testing.T, ctx context.Context, repository *submission.Repository, request biz.PipelineDispatchRequest) biz.PipelineDispatch {
	t.Helper()
	got, err := repository.Reserve(ctx, request)
	if err != nil || got.SendPermit == nil {
		t.Fatalf("first reservation needs a committed dispatch and permit: %v", err)
	}
	return got.Dispatch
}

func assertRejectedReservation(t *testing.T, got biz.PipelineDispatchReservation, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || !reflect.DeepEqual(got, biz.PipelineDispatchReservation{}) {
		t.Fatalf("CPU07_RESERVATION_BEHAVIOR: want %v with no fact/permit, got %v nonempty=%t", want, err, !reflect.DeepEqual(got, biz.PipelineDispatchReservation{}))
	}
}

func assertNoDispatch(t *testing.T, ctx context.Context, repository *submission.Repository, tenant, executionID string) {
	t.Helper()
	got, err := repository.Get(ctx, tenant, executionID)
	if !errors.Is(err, biz.ErrExecutionNotFound) || !reflect.DeepEqual(got, biz.PipelineDispatch{}) {
		t.Fatalf("CPU07_RESERVATION_BEHAVIOR: unexpected durable dispatch: err=%v nonempty=%t", err, !reflect.DeepEqual(got, biz.PipelineDispatch{}))
	}
}

func assertDispatchReplay(t *testing.T, ctx context.Context, repository *submission.Repository, request biz.PipelineDispatchRequest, original biz.PipelineDispatch) {
	t.Helper()
	got, err := repository.Reserve(ctx, request)
	if err != nil || got.SendPermit != nil || !reflect.DeepEqual(got.Dispatch, original) {
		t.Fatalf("CPU07_RESERVATION_BEHAVIOR: replay changed the original fact or granted another permit: %v", err)
	}
	stored, err := repository.Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || !reflect.DeepEqual(stored, original) {
		t.Fatalf("Get changed the original dispatch: %v", err)
	}
}

func refreshDispatchHashes(t *testing.T, admission *biz.Admission) {
	t.Helper()
	*admission = admissionfixture.Bind(t, *admission)
}

func assertSameDispatchAdmission(t *testing.T, got, original biz.Admission) {
	t.Helper()
	wantIntent, wantSnapshot, _ := original.CanonicalPayloads()
	gotIntent, gotSnapshot, err := got.CanonicalPayloads()
	if err != nil || !bytes.Equal(gotIntent, wantIntent) || !bytes.Equal(gotSnapshot, wantSnapshot) ||
		got.TenantID != original.TenantID || got.ExecutionID != original.ExecutionID || got.OperationID != original.OperationID ||
		got.Actor != original.Actor || got.IntentHash != original.IntentHash || got.SpecHash != original.SpecHash || !got.AcceptedAt.Equal(original.AcceptedAt) {
		t.Fatal("reservation changed the complete original Admission")
	}
}

func databaseTime(t *testing.T, ctx context.Context, pool *pgxpool.Pool) time.Time {
	t.Helper()
	var now time.Time
	if err := pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		t.Fatal("CPU07_DB_PREFLIGHT: database time unavailable; behavior NOT_RUN")
	}
	return now.UTC().Truncate(time.Microsecond)
}

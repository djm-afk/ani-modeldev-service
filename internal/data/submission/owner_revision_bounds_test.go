package submission_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

const revisionBoundsMax uint64 = 18446744073709551615
const revisionBoundsMaxText = "18446744073709551615"

func TestOwnerRevisionMaximumIsExactAndAllExistingFactsReplay(t *testing.T) {
	openPool := postgres.Prepare(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	request := validDispatchRequest(t, openPool())
	writer, observer := openPool(), openPool()
	admissions, dispatches := execution.New(writer), submission.New(writer)
	accepted, err := admissions.Accept(ctx, request.Admission)
	if err != nil || accepted.Replayed {
		t.Fatalf("REVISION_BOUNDS_PREFLIGHT: actual Admission failed; behavior NOT_RUN: %v", err)
	}
	first, err := dispatches.Reserve(ctx, request)
	if err != nil || first.SendPermit == nil {
		t.Fatalf("REVISION_BOUNDS_PREFLIGHT: actual reservation lacks its permit; behavior NOT_RUN: %v", err)
	}
	permit := *first.SendPermit
	uncertainAt := first.Dispatch.ReservedAt.Add(time.Microsecond)
	if _, err := dispatches.MarkSubmissionUncertain(ctx, permit, uncertainAt); err != nil {
		t.Fatalf("REVISION_BOUNDS_PREFLIGHT: first uncertainty failed; behavior NOT_RUN: %v", err)
	}
	notSentAt := uncertainAt.Add(time.Microsecond)
	if _, err := dispatches.MarkSubmissionNotSent(ctx, permit, notSentAt); err != nil {
		t.Fatalf("REVISION_BOUNDS_PREFLIGHT: first no-send failed; behavior NOT_RUN: %v", err)
	}
	observation := confirmedObservation(confirmedRunID)
	confirmed, err := dispatches.RecordSubmissionConfirmed(ctx, permit, observation, notSentAt.Add(time.Microsecond))
	if err != nil || confirmed.ConflictingRuns {
		t.Fatalf("REVISION_BOUNDS_PREFLIGHT: first Run failed; behavior NOT_RUN: %v", err)
	}
	firstClose := dispatchCloseIntent(request)
	closed, err := admissions.ApplyCloseIntent(ctx, firstClose)
	if err != nil || closed.Replayed || closed.Generation != 1 {
		t.Fatalf("REVISION_BOUNDS_PREFLIGHT: first close failed; behavior NOT_RUN: %v", err)
	}
	setRevisionBoundsValue(t, ctx, writer, request, "18446744073709551614")
	if got := readRevisionBoundsRows(t, ctx, observer, request).revision; got != "18446744073709551614" {
		t.Fatal("REVISION_BOUNDS_PREFLIGHT: Max-1 not independently visible; behavior NOT_RUN")
	}
	secondClose := firstClose
	secondClose.SourceGeneration = 2
	secondClose.RequestedAt = firstClose.RequestedAt.Add(time.Microsecond)
	wantClose := biz.CloseRecord{CloseIntent: secondClose, Generation: 2, State: biz.CloseStateClosing}
	advanced, err := admissions.ApplyCloseIntent(ctx, secondClose)
	if err != nil || advanced.Replayed || advanced.OwnerRevision != revisionBoundsMax || !reflect.DeepEqual(advanced.CloseRecord, wantClose) {
		t.Fatalf("Max-1 did not commit exactly Max with one new close/fence: %v", err)
	}
	wantExecution := accepted.Execution
	wantExecution.OwnerRevision, wantExecution.Close = revisionBoundsMax, &wantClose
	wantExecution.States = biz.ExecutionStates{
		Compute: biz.ComputeStateSubmissionConfirmed, Delivery: biz.DeliveryStatePending,
		Resource: biz.ResourceStateNotApplicable, Close: biz.CloseStateClosing,
	}
	wantDispatch := confirmed.Dispatch
	wantDispatch.OwnerRevision = revisionBoundsMax
	storedExecution, err := execution.New(observer).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || !reflect.DeepEqual(storedExecution, wantExecution) {
		t.Fatalf("independent Execution.Get lost exact Max or original aggregate facts: %v", err)
	}
	storedDispatch, err := submission.New(observer).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || !reflect.DeepEqual(storedDispatch, wantDispatch) {
		t.Fatalf("independent Submission.Get lost exact Max or first observations: %v", err)
	}
	original := readRevisionBoundsRows(t, ctx, observer, request)
	if original.revision != revisionBoundsMaxText {
		t.Fatal("database did not retain exact uint64 maximum")
	}
	// Replay every kind of fact through a newly connected pool. Neither
	// saturation nor a later close may refresh observations or grant a permit.
	reconnected := openPool()
	admissions, dispatches = execution.New(reconnected), submission.New(reconnected)
	later := notSentAt.Add(10 * time.Microsecond)
	replays := []struct {
		name string
		call func() (any, error)
		want any
	}{
		{"Admission", func() (any, error) { return admissions.Accept(ctx, request.Admission) }, biz.AcceptReceipt{Execution: wantExecution, Replayed: true}},
		{"reservation", func() (any, error) { return dispatches.Reserve(ctx, request) }, biz.PipelineDispatchReservation{Dispatch: wantDispatch}},
		{"uncertainty", func() (any, error) { return dispatches.MarkSubmissionUncertain(ctx, permit, later) }, wantDispatch},
		{"no-send", func() (any, error) { return dispatches.MarkSubmissionNotSent(ctx, permit, later) }, wantDispatch},
		{"confirmed Run", func() (any, error) { return dispatches.RecordSubmissionConfirmed(ctx, permit, observation, later) }, biz.PipelineConfirmationReceipt{Dispatch: wantDispatch}},
		{"original source close", func() (any, error) { return admissions.ApplyCloseIntent(ctx, firstClose) }, biz.CloseReceipt{CloseRecord: closed.CloseRecord, OwnerRevision: revisionBoundsMax, Replayed: true}},
		{"latest source close", func() (any, error) { return admissions.ApplyCloseIntent(ctx, secondClose) }, biz.CloseReceipt{CloseRecord: wantClose, OwnerRevision: revisionBoundsMax, Replayed: true}},
	}
	for _, replay := range replays {
		t.Run(replay.name, func(t *testing.T) {
			got, err := replay.call()
			if err != nil || !reflect.DeepEqual(got, replay.want) {
				t.Fatalf("saturated replay changed its full receipt or failed: %v", err)
			}
			if after := readRevisionBoundsRows(t, ctx, observer, request); after != original {
				t.Fatal("saturated replay changed persisted identity, Admission, close, dispatch or Run rows")
			}
		})
	}
}

func TestOwnerRevisionSaturationRollsBackEveryNewFact(t *testing.T) {
	cases := []struct {
		name    string
		reserve bool
		confirm bool
	}{
		{"late Admission after close", false, false},
		{"new Close", false, false},
		{"first Reserve", false, false},
		{"first Uncertain", true, false},
		{"first NotSent", true, false},
		{"NotSent after confirmed", true, true},
		{"first confirmed Run", true, false},
		{"additional confirmed Run", true, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			openPool := postgres.Prepare(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			request := validDispatchRequest(t, openPool())
			writer, observer := openPool(), openPool()
			admissions, dispatches := execution.New(writer), submission.New(writer)
			if test.name == "late Admission after close" {
				if _, err := admissions.ApplyCloseIntent(ctx, dispatchCloseIntent(request)); err != nil {
					t.Fatalf("REVISION_BOUNDS_PREFLIGHT: close-only identity failed; behavior NOT_RUN: %v", err)
				}
			} else {
				acceptDispatchAdmission(t, ctx, admissions, request)
			}
			var permit biz.PipelineSendPermit
			var observedAt time.Time
			if test.reserve {
				first, err := dispatches.Reserve(ctx, request)
				if err != nil || first.SendPermit == nil {
					t.Fatalf("REVISION_BOUNDS_PREFLIGHT: original permit unavailable; behavior NOT_RUN: %v", err)
				}
				permit = *first.SendPermit
				observedAt = first.Dispatch.ReservedAt.Add(time.Microsecond)
			}
			if test.confirm {
				if _, err := dispatches.RecordSubmissionConfirmed(ctx, permit, confirmedObservation(confirmedRunID), observedAt); err != nil {
					t.Fatalf("REVISION_BOUNDS_PREFLIGHT: original confirmed Run unavailable; behavior NOT_RUN: %v", err)
				}
				observedAt = observedAt.Add(time.Microsecond)
			}
			var call func() (any, error)
			var zero any
			switch test.name {
			case "late Admission after close":
				call = func() (any, error) { return admissions.Accept(ctx, request.Admission) }
				zero = biz.AcceptReceipt{}
			case "new Close":
				call = func() (any, error) { return admissions.ApplyCloseIntent(ctx, dispatchCloseIntent(request)) }
				zero = biz.CloseReceipt{}
			case "first Reserve":
				call = func() (any, error) { return dispatches.Reserve(ctx, request) }
				zero = biz.PipelineDispatchReservation{}
			case "first Uncertain":
				call = func() (any, error) { return dispatches.MarkSubmissionUncertain(ctx, permit, observedAt) }
				zero = biz.PipelineDispatch{}
			case "first NotSent", "NotSent after confirmed":
				call = func() (any, error) { return dispatches.MarkSubmissionNotSent(ctx, permit, observedAt) }
				zero = biz.PipelineDispatch{}
			case "first confirmed Run", "additional confirmed Run":
				call = func() (any, error) {
					return dispatches.RecordSubmissionConfirmed(ctx, permit, confirmedObservation("66666666-7777-4888-8999-aaaaaaaaaaaa"), observedAt)
				}
				zero = biz.PipelineConfirmationReceipt{}
			default:
				t.Fatal("invalid saturation test case")
			}
			setRevisionBoundsValue(t, ctx, writer, request, revisionBoundsMaxText)
			before := readRevisionBoundsRows(t, ctx, observer, request)
			if before.revision != revisionBoundsMaxText {
				t.Fatal("REVISION_BOUNDS_PREFLIGHT: exact Max unavailable; behavior NOT_RUN")
			}
			t.Log("REVISION_BOUNDS_PREFLIGHT PASS: real original facts and uint64 Max visible through an independent runtime pool")
			got, err := call()
			if !errors.Is(err, biz.ErrPersistence) || !reflect.DeepEqual(got, zero) {
				t.Fatalf("saturated new fact must fail with a completely empty result: error=%v nonempty=%t", err, !reflect.DeepEqual(got, zero))
			}
			if after := readRevisionBoundsRows(t, ctx, observer, request); after != before {
				t.Fatal("saturation failed to roll back all business rows, close fence and aggregate revision together")
			}
			// Only this test resets the exact row through its explicit column
			// grant. A successful identical operation then rules out an unrelated
			// validation rejection or an always-failing writer as the explanation.
			setRevisionBoundsValue(t, ctx, writer, request, "10")
			got, err = call()
			if err != nil || reflect.DeepEqual(got, zero) {
				t.Fatalf("same valid new fact failed after removing only saturation: %v", err)
			}
			if after := readRevisionBoundsRows(t, ctx, observer, request); after.revision != "11" {
				t.Fatal("healthy control did not independently commit exactly one new revision")
			}
		})
	}
}

func TestOwnerRevisionDatabaseRejectsInvalidNumbersAndImmutableUpdates(t *testing.T) {
	openPool := postgres.Prepare(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := validDispatchRequest(t, openPool())
	writer, observer := openPool(), openPool()
	admissions := execution.New(writer)
	acceptDispatchAdmission(t, ctx, admissions, request)
	setRevisionBoundsValue(t, ctx, writer, request, "1.00")
	stored, err := execution.New(observer).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || stored.OwnerRevision != 1 {
		t.Fatalf("valid integer numeric scale was rejected or changed by repository read: %v", err)
	}
	assertSameDispatchAdmission(t, stored.Admission, request.Admission)
	before := readRevisionBoundsRows(t, ctx, observer, request)
	if before.revision != "1.00" {
		t.Fatal("REVISION_BOUNDS_PREFLIGHT: PostgreSQL did not retain the legal scale fixture; behavior NOT_RUN")
	}
	for _, value := range []string{"-1", "1.5", "NaN", "Infinity", "-Infinity", "18446744073709551616"} {
		t.Run(value, func(t *testing.T) {
			_, err := writer.Exec(ctx, `UPDATE modeldev_execution_identities SET owner_revision = $3::text::numeric
				WHERE tenant_id = $1::uuid AND execution_id = $2::uuid`, request.Admission.TenantID, request.Admission.ExecutionID, value)
			requireRevisionBoundsSQLState(t, err, "23514")
			if after := readRevisionBoundsRows(t, ctx, observer, request); after != before {
				t.Fatal("rejected numeric assignment changed the original version or business facts")
			}
		})
	}
	for _, column := range []string{"tenant_id", "execution_id", "operation_id", "spec_hash"} {
		t.Run("immutable "+column, func(t *testing.T) {
			// Each name is a fixed test literal. A no-op UPDATE still requires
			// its column privilege, without introducing an unrelated FK error.
			_, err := writer.Exec(ctx, "UPDATE modeldev_execution_identities SET "+column+" = "+column+" WHERE tenant_id = $1::uuid AND execution_id = $2::uuid", request.Admission.TenantID, request.Admission.ExecutionID)
			requireRevisionBoundsSQLState(t, err, "42501")
			if after := readRevisionBoundsRows(t, ctx, observer, request); after != before {
				t.Fatal("denied immutable update changed the original aggregate")
			}
		})
	}
	replayed, err := admissions.Accept(ctx, request.Admission)
	if err != nil || !replayed.Replayed || replayed.OwnerRevision != 1 || !reflect.DeepEqual(replayed.Execution, stored) {
		t.Fatalf("valid scaled revision did not remain replayable after rejected SQL: %v", err)
	}
	if after := readRevisionBoundsRows(t, ctx, observer, request); after != before {
		t.Fatal("replay normalized away the original numeric scale or changed durable facts")
	}
}

func setRevisionBoundsValue(t *testing.T, ctx context.Context, pool *pgxpool.Pool, request biz.PipelineDispatchRequest, value string) {
	t.Helper()
	result, err := pool.Exec(ctx, `UPDATE modeldev_execution_identities SET owner_revision = $3::text::numeric
		WHERE tenant_id = $1::uuid AND execution_id = $2::uuid`, request.Admission.TenantID, request.Admission.ExecutionID, value)
	if err != nil || result.RowsAffected() != 1 {
		t.Fatal("REVISION_BOUNDS_PREFLIGHT: exact isolated runtime revision assignment failed; behavior NOT_RUN")
	}
}

type revisionBoundsRows struct {
	revision string
	facts    string
}

// One statement snapshots every column of every row for this exact aggregate,
// including child Runs and close fences that a public projection might omit.
// Callers always use a pool independent of the writer being checked.
func readRevisionBoundsRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, request biz.PipelineDispatchRequest) revisionBoundsRows {
	t.Helper()
	var result revisionBoundsRows
	err := pool.QueryRow(ctx, `SELECT identity.owner_revision::text, jsonb_build_object(
		'identity', to_jsonb(identity),
		'admission', (SELECT to_jsonb(admission) FROM modeldev_executions admission
			WHERE admission.tenant_id = identity.tenant_id AND admission.execution_id = identity.execution_id),
		'closes', (SELECT COALESCE(jsonb_agg(to_jsonb(closing) ORDER BY closing.source_kind, closing.source_generation), '[]'::jsonb)
			FROM modeldev_close_intents closing WHERE closing.tenant_id = identity.tenant_id AND closing.execution_id = identity.execution_id),
		'dispatch', (SELECT to_jsonb(dispatch) FROM modeldev_pipeline_dispatches dispatch
			WHERE dispatch.tenant_id = identity.tenant_id AND dispatch.execution_id = identity.execution_id),
		'runs', (SELECT COALESCE(jsonb_agg(to_jsonb(observed) ORDER BY observed.attempt_id, observed.run_id), '[]'::jsonb)
			FROM modeldev_pipeline_confirmed_runs observed WHERE observed.tenant_id = identity.tenant_id AND observed.execution_id = identity.execution_id)
	)::text FROM modeldev_execution_identities identity
	WHERE identity.tenant_id = $1::uuid AND identity.execution_id = $2::uuid`, request.Admission.TenantID, request.Admission.ExecutionID).Scan(&result.revision, &result.facts)
	if err != nil {
		t.Fatal("REVISION_BOUNDS_PREFLIGHT: independent aggregate snapshot unavailable; behavior NOT_RUN")
	}
	return result
}

func requireRevisionBoundsSQLState(t *testing.T, err error, want string) {
	t.Helper()
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != want {
		t.Fatalf("actual runtime SQL rejection must have SQLSTATE %s", want)
	}
}

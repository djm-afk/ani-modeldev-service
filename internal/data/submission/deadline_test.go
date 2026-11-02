package submission_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestExpiredDatabaseDeadlineRejectsFirstReservation(t *testing.T) {
	openPool := postgres.Prepare(t)
	observer := openPool()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := databaseTime(t, ctx, observer)
	request := validDispatchRequest(t, openPool())
	request.Admission.AcceptedAt = now.Add(-2 * time.Minute)
	request.Admission.Snapshot.DeadlineAt = now.Add(-time.Minute)
	refreshDispatchHashes(t, openPool(), &request)
	acceptDispatchAdmission(t, ctx, execution.New(openPool()), request)
	repository := submission.New(openPool())
	got, err := repository.Reserve(ctx, request)
	assertRejectedReservation(t, got, err, biz.ErrPipelineDispatchBlocked)
	assertNoDispatch(t, ctx, submission.New(openPool()), request.Admission.TenantID, request.Admission.ExecutionID)
}

func TestReservedDispatchAfterDeadlineReplaysOnlyOriginalFact(t *testing.T) {
	openPool := postgres.Prepare(t)
	observer := openPool()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := databaseTime(t, ctx, observer)
	request := validDispatchRequest(t, openPool())
	request.Admission.AcceptedAt = now.Add(-time.Minute)
	request.Admission.Snapshot.DeadlineAt = now.Add(3 * time.Second)
	refreshDispatchHashes(t, openPool(), &request)
	acceptDispatchAdmission(t, ctx, execution.New(openPool()), request)
	original := reserveFirstDispatch(t, ctx, submission.New(openPool()), request)
	waitForDatabaseDeadline(t, ctx, observer, request.Admission.Snapshot.DeadlineAt)
	assertDispatchReplay(t, ctx, submission.New(openPool()), request, original)
}

func TestReservationWaitingOnIdentityUsesDatabaseTimeAfterLock(t *testing.T) {
	openPool := postgres.Prepare(t)
	observer := openPool()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	now := databaseTime(t, ctx, observer)
	request := validDispatchRequest(t, openPool())
	request.Admission.AcceptedAt = now.Add(-time.Minute)
	request.Admission.Snapshot.DeadlineAt = now.Add(3 * time.Second)
	refreshDispatchHashes(t, openPool(), &request)
	acceptDispatchAdmission(t, ctx, execution.New(openPool()), request)

	// Direct SQL is test synchronization only. The behavior under test remains
	// Reserve/Get; no durable product facts are inserted through a side channel.
	locker, err := openPool().Begin(ctx)
	if err != nil {
		t.Fatal("CPU07_DB_PREFLIGHT: identity-lock fixture unavailable; behavior NOT_RUN")
	}
	defer func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		_ = locker.Rollback(cleanup)
	}()
	var heldExecution pgtype.UUID
	if err := locker.QueryRow(ctx, "SELECT execution_id FROM modeldev_execution_identities WHERE tenant_id = $1::uuid AND execution_id = $2::uuid FOR UPDATE", request.Admission.TenantID, request.Admission.ExecutionID).Scan(&heldExecution); err != nil {
		t.Fatal("CPU07_DB_PREFLIGHT: exact identity lock unavailable; behavior NOT_RUN")
	}
	var lockerPID int32
	if err := locker.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&lockerPID); err != nil {
		t.Fatal("CPU07_DB_PREFLIGHT: lock owner cannot be observed; behavior NOT_RUN")
	}
	applicationName := "cpu07-deadline-" + uuid.NewString()
	config := observer.Config()
	config.MaxConns = 1
	config.ConnConfig.RuntimeParams["application_name"] = applicationName
	contender, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("CPU07_DB_PREFLIGHT: contender pool unavailable; behavior NOT_RUN")
	}
	t.Cleanup(contender.Close)
	if err := contender.Ping(ctx); err != nil {
		t.Fatal("CPU07_DB_PREFLIGHT: contender connection unavailable; behavior NOT_RUN")
	}
	type result struct {
		reservation biz.PipelineDispatchReservation
		err         error
	}
	completed := make(chan result, 1)
	go func() {
		reservation, err := submission.New(contender).Reserve(ctx, request)
		completed <- result{reservation: reservation, err: err}
	}()
	// The polling cadence does not establish ordering. PostgreSQL must expose
	// this exact contender blocked by our backend before the original deadline.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var observedAt time.Time
		var blocked bool
		if err := observer.QueryRow(ctx, `SELECT clock_timestamp(), EXISTS (
SELECT 1 FROM pg_stat_activity
WHERE application_name = $1 AND $2::integer = ANY (pg_blocking_pids(pid)))`, applicationName, lockerPID).Scan(&observedAt, &blocked); err != nil {
			t.Fatal("CPU07_DB_PREFLIGHT: real lock wait cannot be observed; behavior NOT_RUN")
		}
		if !observedAt.Before(request.Admission.Snapshot.DeadlineAt) {
			t.Fatal("CPU07_DB_PREFLIGHT: did not observe contender blocked before deadline; lock-crossing behavior NOT_RUN")
		}
		if blocked {
			t.Log("CPU07_DB_ORDER: contender is blocked by the held identity lock before the database deadline")
			break
		}
		select {
		case got := <-completed:
			t.Fatalf("CPU07_RESERVATION_BEHAVIOR: Reserve returned before acquiring the held identity lock: %v", got.err)
		case <-ctx.Done():
			t.Fatal("CPU07_DB_PREFLIGHT: lock observation timed out; behavior NOT_RUN")
		case <-ticker.C:
		}
	}
	waitForDatabaseDeadline(t, ctx, observer, request.Admission.Snapshot.DeadlineAt)
	select {
	case got := <-completed:
		t.Fatalf("CPU07_RESERVATION_BEHAVIOR: Reserve completed while identity lock was held: %v", got.err)
	default:
	}
	if err := locker.Commit(ctx); err != nil {
		t.Fatal("CPU07_DB_PREFLIGHT: identity-lock release failed; behavior NOT_RUN")
	}
	select {
	case got := <-completed:
		assertRejectedReservation(t, got.reservation, got.err, biz.ErrPipelineDispatchBlocked)
	case <-ctx.Done():
		t.Fatal("CPU07_RESERVATION_BEHAVIOR: reservation did not finish after identity-lock release")
	}
	assertNoDispatch(t, ctx, submission.New(openPool()), request.Admission.TenantID, request.Admission.ExecutionID)
}

func waitForDatabaseDeadline(t *testing.T, ctx context.Context, observer *pgxpool.Pool, deadline time.Time) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !databaseTime(t, ctx, observer).Before(deadline) {
			t.Log("CPU07_DB_ORDER: database clock reached the original deadline")
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("CPU07_DB_PREFLIGHT: database deadline observation timed out; behavior NOT_RUN")
		case <-ticker.C:
		}
	}
}

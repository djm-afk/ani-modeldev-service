package lifecycle_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

// Only corruption injection uses the migration role, in the random schema
// installed by the shared fixture. All discovery and release use runtime ports.
func discoveryFaultConnection(t *testing.T, pool *pgxpool.Pool) *pgx.Conn {
	t.Helper()
	config, err := pgx.ParseConfig(os.Getenv("CPU_P01_TEST_DATABASE_ADMIN_URL"))
	runtime := pool.Config().ConnConfig
	schema := runtime.RuntimeParams["search_path"]
	if err != nil || !strings.HasPrefix(schema, "cpu04_") || config.Host != runtime.Host || config.Port != runtime.Port || config.Database != runtime.Database || config.User == runtime.User {
		t.Fatal("isolated discovery migration identity required")
	}
	config.RuntimeParams["search_path"] = schema
	conn, err := pgx.ConnectConfig(context.Background(), config)
	if err != nil {
		t.Fatal("discovery migration connection unavailable")
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func TestCloseScanHelperBoundsMalformedSnapshotsAndDoesNotSwallowCancellation(t *testing.T) {
	open := postgres.Prepare(t)
	pool := open()
	ctx := context.Background()
	cases := map[string][]byte{"utf8": {0xff}, "json": []byte(`{"broken":`), "array": []byte(`[]`), "scalar": []byte(`1`), "empty": {}, "oversize": []byte(strings.Repeat(" ", 8388609)), "depth": []byte(`{"a":` + strings.Repeat("[", 20000) + `0` + strings.Repeat("]", 20000) + `}`)}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			var missing bool
			if err := pool.QueryRow(ctx, "SELECT public.modeldev_scan_snapshot_jsonb($1::bytea) IS NULL", raw).Scan(&missing); err != nil || !missing {
				t.Fatal("malformed snapshot escaped bounded decoder", err)
			}
		})
	}
	var actual string
	if err := pool.QueryRow(ctx, "SELECT public.modeldev_scan_snapshot_jsonb($1::bytea)::text", []byte(`{"ok":true}`)).Scan(&actual); err != nil || actual != `{"ok": true}` {
		t.Fatal("valid object lost", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout='20ms'"); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, "SELECT public.modeldev_scan_snapshot_jsonb('{}'::bytea), pg_sleep(1)")
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "57014" {
		t.Fatal("database cancellation was swallowed", err)
	}
}

func TestCloseDiscoveryAdvancesPastCorruptSnapshotsProjectionAndRuntime(t *testing.T) {
	open := postgres.Prepare(t)
	pool := open()
	admin := discoveryFaultConnection(t, pool)
	ctx := context.Background()
	original := gpuReleaseAdmission(t)
	original.Snapshot.ComputeOrigin.DeviceMode = "gpu"
	base := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	ids := []string{}
	for i := range 7 {
		a := original
		a.ExecutionID = uuid.NewString()
		a.OperationID = uuid.NewString()
		a.AcceptedAt = base.Add(time.Duration(i) * time.Microsecond)
		a.Snapshot.DeadlineAt = a.AcceptedAt.Add(time.Hour)
		a = admissionfixture.Bind(t, a)
		if _, err := execution.New(pool).Accept(ctx, a); err != nil {
			t.Fatal("real admission", err)
		}
		if _, err := execution.New(pool).ApplyCloseIntent(ctx, biz.CloseIntent{TenantID: a.TenantID, ExecutionID: a.ExecutionID, OperationID: a.OperationID, SpecHash: a.SpecHash, SourceGeneration: 1, Reason: biz.CloseReasonUserStop, RequestedAt: time.Now().UTC().Truncate(time.Microsecond), RequestedActor: a.Actor}); err != nil {
			t.Fatal(err)
		}
		if _, err := lifecycle.New(pool).MarkOwnerCloseReview(ctx, a.TenantID, a.ExecutionID, "KFP_CREATE_UNRESOLVED"); err != nil {
			t.Fatal("persist original close", err)
		}
		ids = append(ids, a.ExecutionID)
		var err error
		switch i {
		case 0:
			_, err = admin.Exec(ctx, "UPDATE modeldev_executions SET snapshot_canonical=$1 WHERE execution_id=$2", []byte{0xff}, a.ExecutionID)
		case 1:
			_, err = admin.Exec(ctx, "UPDATE modeldev_executions SET snapshot_canonical=$1 WHERE execution_id=$2", []byte(`{"broken":`), a.ExecutionID)
		case 2:
			_, err = admin.Exec(ctx, "UPDATE modeldev_executions SET snapshot_canonical=$1 WHERE execution_id=$2", []byte(`[]`), a.ExecutionID)
		case 3:
			_, err = admin.Exec(ctx, "UPDATE modeldev_executions SET deadline_at=deadline_at+interval '1 second' WHERE execution_id=$1", a.ExecutionID)
		case 4:
			_, err = admin.Exec(ctx, "UPDATE modeldev_execution_runtimes SET facts=jsonb_set(facts,'{TrainingWindow}','\"not-a-timestamp-or-object\"'::jsonb) WHERE execution_id=$1", a.ExecutionID)
		case 5:
			_, err = admin.Exec(ctx, "UPDATE modeldev_execution_runtimes SET facts=jsonb_set(facts,'{StepTimings}','{}'::jsonb) WHERE execution_id=$1", a.ExecutionID)
		}
		if err != nil {
			t.Fatal("isolated corruption injection", err)
		}
	}
	repository := lifecycle.New(pool)
	tenants, err := repository.ListPendingCloseTenants(ctx, original.Snapshot.Environment.ClusterID, "", 16)
	if err != nil || len(tenants) != 1 || tenants[0] != original.TenantID {
		t.Fatal("outer query failed on malformed data", tenants, err)
	}
	before, err := repository.CloseDiscoveryTime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cursor := biz.DiscoveryCursor{SweepBefore: before}
	examined := 0
	bad := 0
	valid := 0
	for {
		page, err := repository.ListPendingClosures(ctx, original.Snapshot.Environment.ClusterID, original.TenantID, cursor, 4)
		if err != nil {
			t.Fatal("one damaged row broke page", err)
		}
		if page.ExaminedCount != len(page.Rows) {
			t.Fatal("page lost examined tuples")
		}
		for _, row := range page.Rows {
			if row.ExecutionID != ids[examined] {
				t.Fatal("unstable keyset order")
			}
			examined++
			if row.DecodeProblem != "" {
				bad++
			} else if row.Reasons&biz.NeedsClose != 0 {
				valid++
			}
			cursor.AfterAcceptedAt = row.AcceptedAt
			cursor.AfterExecutionID = row.ExecutionID
		}
		if len(page.Rows) < 4 {
			break
		}
	}
	if examined != 7 || bad != 6 || valid != 1 {
		t.Fatalf("corrupt rows starved valid successor: examined=%d bad=%d valid=%d", examined, bad, valid)
	}
}

func TestSealedGPUOutboxWorkerDeliversWithoutEnvironmentOrNamespace(t *testing.T) {
	open := postgres.Prepare(t)
	a, _ := closedGPURelease(t, open)
	pool := open()
	repository := lifecycle.New(pool)
	ctx := context.Background()
	if _, err := repository.SealGPURelease(ctx, a.TenantID, a.ExecutionID); err != nil {
		t.Fatal(err)
	}
	var environments int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM modeldev_tenant_execution_environments").Scan(&environments); err != nil || environments != 0 {
		t.Fatal("test must have no current environment", err)
	}
	calls := 0
	release, err := biz.NewGPUReleaseReconciler(repository, releaseReporterFunc(func(_ context.Context, event biz.GPUResourceReleasedEvent) (biz.GPUReleaseACK, error) {
		calls++
		if event.ResourceID != a.ExecutionID || event.CreateOperationID != a.OperationID {
			t.Error("release identity changed")
		}
		return biz.GPUReleaseACK{ReleaseEventID: event.ReleaseEventID, CreateOperationID: event.CreateOperationID, ResourceID: event.ResourceID, Status: "APPLIED"}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	// No live client, material reader or configured closer is installed. The
	// original sealed event is an independent durable delivery branch.
	worker, err := biz.NewCloseWorker(repository, &biz.ExecutionCloser{}, a.Snapshot.Environment.ClusterID, time.Second, release)
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.ReconcileOnce(ctx)
	if err != nil || result.Closed != 1 || result.Unresolved != 0 || calls != 1 {
		t.Fatal("sealed delivery depended on live environment", result, err)
	}
	durable, err := repository.GetRuntime(ctx, a.TenantID, a.ExecutionID)
	if err != nil || durable.GPURelease.Outbox.ACK == nil || durable.GPURelease.Outbox.DeliveredAt == nil {
		t.Fatal("ACK not durable", err)
	}
	tenants, err := repository.ListPendingCloseTenants(ctx, a.Snapshot.Environment.ClusterID, "", 16)
	if err != nil || len(tenants) != 0 {
		t.Fatal("fully ACKed no-dispatch execution remains pending", err)
	}
}

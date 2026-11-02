package submittest_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	projection "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	environment "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

type discoveryReadyGate struct{ pool *pgxpool.Pool }

func (g discoveryReadyGate) VerifyDispatchAdmission(ctx context.Context, a biz.Admission) error {
	binding, err := environment.NewRepository(g.pool).ReadReadyBinding(ctx, a.TenantID, a.Snapshot.Environment.ClusterID)
	if err != nil {
		return err
	}
	raw, err := binding.Canonical()
	if err != nil {
		return err
	}
	if binding.Snapshot(projection.Digest(raw)) != a.Snapshot.Environment {
		return biz.ErrAdmissionConflict
	}
	return nil
}

func TestDispatchDiscovery33EnvironmentsBadFirstPageAndNewArrivalsDoNotStarve(t *testing.T) {
	open := postgres.Prepare(t)
	pool := open()
	ctx := context.Background()
	original := dispatchRequest(t)
	base := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	// Every row starts with the production Ensure state machine and persists its
	// real READY receipt. The first 32 then suffer a durable dependency block.
	for i := range 32 {
		a := dispatchRequest(t).Admission
		a.TenantID = fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		a.ExecutionID = uuid.NewString()
		a.OperationID = uuid.NewString()
		a = admissionfixture.Bind(t, a)
		a = admissionfixture.EnsureEnvironment(t, pool, a)
		if _, err := execution.New(pool).Accept(ctx, a); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "UPDATE modeldev_tenant_execution_environments SET state='BLOCKED',reason_code='RBAC_UNSAFE',reason_stage='VERIFY' WHERE tenant_id=$1", a.TenantID); err != nil {
			t.Fatal(err)
		}
	}
	original.Admission = admissionfixture.EnsureEnvironment(t, pool, original.Admission)
	original.Owner = original.Admission.Snapshot.DispatchOwner
	adminConfig, err := pgx.ParseConfig(os.Getenv("CPU_P01_TEST_DATABASE_ADMIN_URL"))
	runtime := pool.Config().ConnConfig
	if err != nil || adminConfig.Host != runtime.Host || adminConfig.Database != runtime.Database || adminConfig.User == runtime.User || !strings.HasPrefix(runtime.RuntimeParams["search_path"], "cpu04_") {
		t.Fatal("isolated migration role required")
	}
	adminConfig.RuntimeParams["search_path"] = runtime.RuntimeParams["search_path"]
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	var fifth biz.Admission
	for i := range 5 {
		a := original.Admission
		a.ExecutionID = uuid.NewString()
		a.OperationID = uuid.NewString()
		a.AcceptedAt = base.Add(time.Duration(i) * time.Microsecond)
		a.Snapshot.DeadlineAt = a.AcceptedAt.Add(time.Hour)
		a = admissionfixture.Bind(t, a)
		if _, err := execution.New(pool).Accept(ctx, a); err != nil {
			t.Fatal(err)
		}
		if i < 4 {
			if _, err := admin.Exec(ctx, "UPDATE modeldev_executions SET snapshot_canonical=$1 WHERE execution_id=$2", []byte{0xff}, a.ExecutionID); err != nil {
				t.Fatal(err)
			}
		} else {
			fifth = a
		}
	}
	var posts atomic.Int32
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer peer.Close()
	repository := submission.New(pool)
	submitter := newSubmitter(t, repository, peer, tokenProviderFunc(func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
		return "synthetic-discovery-token", nil
	}))
	worker, err := biz.NewDispatchWorker(repository, submitter, fifth.Snapshot.Environment.ClusterID, discoveryReadyGate{pool}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		result, err := worker.DispatchOnce(ctx)
		if err != nil || result.Examined > 64 {
			t.Fatal("bounded discovery failed", result, err)
		}
		if i >= 2 {
			// These rows are newer than the target tenant's first captured sweep. They
			// must wait for its old fifth row rather than continually resetting it.
			a := original.Admission
			a.ExecutionID = uuid.NewString()
			a.OperationID = uuid.NewString()
			a.AcceptedAt = time.Now().UTC().Truncate(time.Microsecond)
			a.Snapshot.DeadlineAt = a.AcceptedAt.Add(time.Hour)
			a = admissionfixture.Bind(t, a)
			if _, err := execution.New(pool).Accept(ctx, a); err != nil {
				t.Fatal("continuous arrival", err)
			}
		}
		if i < 5 && posts.Load() != 0 {
			t.Fatal("new arrival crossed old sweep boundary")
		}
	}
	if posts.Load() != 1 {
		t.Fatalf("33rd environment fifth candidate starved: POST=%d", posts.Load())
	}
	dispatch, err := repository.Get(ctx, fifth.TenantID, fifth.ExecutionID)
	if err != nil || dispatch.Plan.Owner != fifth.Snapshot.DispatchOwner {
		t.Fatal("fifth admission did not reach original Reserve with frozen owner", err)
	}
	var bindings int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM modeldev_tenant_execution_environments").Scan(&bindings); err != nil || bindings != 33 {
		t.Fatal("logical environment coverage lost", bindings, err)
	}
}

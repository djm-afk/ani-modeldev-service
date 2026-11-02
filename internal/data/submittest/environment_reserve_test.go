package submittest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestFirstReserveRequiresOriginalReadyRowButCommittedReplayDoesNot(t *testing.T) {
	for _, prior := range []bool{false, true} {
		name := "first reserve"
		if prior {
			name = "committed replay"
		}
		t.Run(name, func(t *testing.T) {
			open := postgres.Prepare(t)
			pool := open()
			a := dispatchRequest(t)
			a.Admission = admissionfixture.EnsureEnvironment(t, pool, a.Admission)
			a.Owner = a.Admission.Snapshot.DispatchOwner
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			if _, err := execution.New(pool).Accept(ctx, a.Admission); err != nil {
				t.Fatal(err)
			}
			repo := submission.New(pool)
			var original biz.PipelineDispatch
			if prior {
				r, err := repo.Reserve(ctx, a)
				if err != nil || r.SendPermit == nil {
					t.Fatal("first durable permit", err)
				}
				original = r.Dispatch
			}
			// The environment writer gets the row lock first. Its BLOCKED transition
			// commits before the contending Reserve can inspect the original binding.
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := tx.Exec(ctx, "UPDATE modeldev_tenant_execution_environments SET state='BLOCKED',reason_code='RBAC_UNSAFE',reason_stage='VERIFY' WHERE tenant_id=$1", a.Admission.TenantID); err != nil {
				t.Fatal(err)
			}
			result := make(chan biz.PipelineDispatchReservation, 1)
			failure := make(chan error, 1)
			go func() { r, e := repo.Reserve(ctx, a); result <- r; failure <- e }()
			// Existing committed dispatch must remain immediately replayable even while
			// environment verification is unavailable; it cannot mint a new permit.
			if prior {
				select {
				case r := <-result:
					err := <-failure
					if err != nil || r.SendPermit != nil || r.Dispatch.AttemptID != original.AttemptID {
						t.Fatal("old dispatch replay gated by current environment", err)
					}
				case <-ctx.Done():
					t.Fatal("committed replay blocked on current environment")
				}
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			r := <-result
			err = <-failure
			if !errors.Is(err, biz.ErrPipelineDispatchBlocked) || r.SendPermit != nil {
				t.Fatal("first Reserve crossed non-READY environment", err)
			}
			if _, err = repo.Get(ctx, a.Admission.TenantID, a.Admission.ExecutionID); !errors.Is(err, biz.ErrExecutionNotFound) {
				t.Fatal("blocked first Reserve persisted dispatch", err)
			}
		})
	}
}

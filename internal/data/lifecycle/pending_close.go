package lifecycle

import (
	"bytes"
	"context"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	lifecyclesql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle/sqlc"
	"time"
)

var _ biz.PendingCloseRepository = (*Repository)(nil)

func (r *Repository) CloseDiscoveryTime(ctx context.Context) (time.Time, error) {
	v, e := lifecyclesql.New(r.pool).CloseDiscoveryTime(ctx)
	if e != nil || !v.Valid {
		return time.Time{}, biz.ErrPersistence
	}
	return v.Time.UTC(), nil
}
func (r *Repository) ListPendingCloseTenants(ctx context.Context, cluster, after string, limit int) ([]string, error) {
	if !cpup01.CanonicalTenantID(cluster) || limit < 1 || limit > 16 {
		return nil, biz.ErrInvalidDispatchWorker
	}
	id, _ := databaseID(cluster)
	var a pgtype.UUID
	if after != "" {
		if !cpup01.CanonicalTenantID(after) {
			return nil, biz.ErrInvalidAdmission
		}
		a, _ = databaseID(after)
	}
	rows, e := lifecyclesql.New(r.pool).ListPendingCloseTenants(ctx, lifecyclesql.ListPendingCloseTenantsParams{ClusterID: id, AfterTenantID: a, BatchSize: int32(limit)})
	if e != nil {
		return nil, biz.ErrPersistence
	}
	out := make([]string, len(rows))
	for i, v := range rows {
		out[i] = v.String()
	}
	return out, nil
}
func (r *Repository) ListPendingClosures(ctx context.Context, cluster, tenant string, cursor biz.DiscoveryCursor, limit int) (biz.PendingClosePage, error) {
	page := biz.PendingClosePage{}
	if !cpup01.CanonicalTenantID(cluster) || !cpup01.CanonicalTenantID(tenant) || limit < 1 || limit > 4 || cursor.SweepBefore.IsZero() || (cursor.AfterExecutionID == "") != cursor.AfterAcceptedAt.IsZero() {
		return page, biz.ErrInvalidAdmission
	}
	c, _ := databaseID(cluster)
	t, _ := databaseID(tenant)
	var after pgtype.UUID
	var at pgtype.Timestamptz
	if cursor.AfterExecutionID != "" {
		if !cpup01.CanonicalTenantID(cursor.AfterExecutionID) {
			return page, biz.ErrInvalidAdmission
		}
		after, _ = databaseID(cursor.AfterExecutionID)
		at = pgtype.Timestamptz{Time: cursor.AfterAcceptedAt, Valid: true}
	}
	rows, e := lifecyclesql.New(r.pool).ListPendingClosures(ctx, lifecyclesql.ListPendingClosuresParams{ClusterID: c, TenantID: t, SweepBefore: pgtype.Timestamptz{Time: cursor.SweepBefore, Valid: true}, AfterAcceptedAt: at, AfterExecutionID: after, BatchSize: int32(limit)})
	if e != nil {
		return page, biz.ErrPersistence
	}
	page.ExaminedCount = len(rows)
	for _, row := range rows {
		item := biz.PendingCloseRow{TenantID: row.TenantID.String(), ExecutionID: row.ExecutionID.String(), AcceptedAt: row.AcceptedAt.Time.UTC()}
		a := biz.Admission{TenantID: item.TenantID, ExecutionID: item.ExecutionID, OperationID: row.OperationID.String(), Actor: row.Actor, IntentHash: row.IntentHash, SpecHash: row.SpecHash, AcceptedAt: item.AcceptedAt}
		var ie, se error
		a.Intent, ie = cpup01.DecodeCanonicalIntent(row.IntentCanonical)
		a.Snapshot, se = cpup01.DecodeCanonicalSnapshot(row.SnapshotCanonical)
		intent, snapshot, ce := a.CanonicalPayloads()
		if ie != nil || se != nil || ce != nil || !bytes.Equal(intent, row.IntentCanonical) || !bytes.Equal(snapshot, row.SnapshotCanonical) || row.ClusterID.String() != a.Snapshot.Environment.ClusterID || row.EnvironmentBindingID.String() != a.Snapshot.Environment.BindingID || !row.DeadlineAt.Valid || !row.DeadlineAt.Time.Equal(a.Snapshot.DeadlineAt) {
			item.DecodeProblem = "PERSISTED_ADMISSION_CONFLICT"
			page.Rows = append(page.Rows, item)
			continue
		}
		state, e := r.GetRuntime(ctx, tenant, item.ExecutionID)
		if e != nil {
			item.DecodeProblem = "PERSISTED_RUNTIME_CONFLICT"
			page.Rows = append(page.Rows, item)
			continue
		}
		if state.ClosedAt == nil {
			if state.CloseGeneration > 0 || !biz.ActiveBusinessDeadline(biz.Execution{Admission: a}, state).After(cursor.SweepBefore) {
				item.Reasons |= biz.NeedsClose
			}
		} else {
			if (state.CloseEvidence == nil || !state.CloseEvidence.NoDispatch) && (state.StepExitCleanup == nil || state.StepExitCleanup.CompletedAt == nil || state.StepExitCleanup.CompletedAt.IsZero() || state.ComputeCleanup == nil || state.ComputeCleanup.Phase != "APPLIED" && (state.ComputeCleanup.Attempts < 3 || state.ComputeCleanup.Phase == "STARTED")) {
				item.Reasons |= biz.NeedsStepOrComputeCleanup
			}
			if a.Snapshot.GPU != nil {
				if state.GPURelease == nil {
					item.Reasons |= biz.GPUReleaseNotSealed
				} else if state.GPURelease.Outbox == nil || (state.GPURelease.Outbox.ACK == nil) != (state.GPURelease.Outbox.DeliveredAt == nil) {
					item.DecodeProblem = "GPU_RELEASE_PERSISTED_CONFLICT"
					item.Reasons = 0
				} else if state.GPURelease.Outbox.ACK == nil {
					item.Reasons |= biz.PendingGPUOutbox
				}
			}
		}
		page.Rows = append(page.Rows, item)
	}
	return page, nil
}

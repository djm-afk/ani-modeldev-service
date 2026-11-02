package submission

import (
	"bytes"
	"context"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	submissionsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission/sqlc"
	"time"
)

var _ biz.PendingAdmissionRepository = (*Repository)(nil)

func (r *Repository) DispatchDiscoveryTime(ctx context.Context) (time.Time, error) {
	v, e := submissionsql.New(r.pool).DispatchDiscoveryTime(ctx)
	if e != nil || !v.Valid {
		return time.Time{}, biz.ErrPersistence
	}
	return v.Time.UTC(), nil
}
func (r *Repository) ListPendingDispatchTenants(ctx context.Context, cluster, after string, limit int) ([]string, error) {
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
	rows, e := submissionsql.New(r.pool).ListPendingDispatchTenants(ctx, submissionsql.ListPendingDispatchTenantsParams{ClusterID: id, AfterTenantID: a, BatchSize: int32(limit)})
	if e != nil {
		return nil, biz.ErrPersistence
	}
	out := make([]string, len(rows))
	for i, v := range rows {
		out[i] = v.String()
	}
	return out, nil
}
func (r *Repository) ListPendingAdmissions(ctx context.Context, cluster, tenant string, cursor biz.DiscoveryCursor, limit int) (biz.PendingAdmissionPage, error) {
	page := biz.PendingAdmissionPage{}
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
	rows, e := submissionsql.New(r.pool).ListPendingAdmissions(ctx, submissionsql.ListPendingAdmissionsParams{ClusterID: c, TenantID: t, SweepBefore: pgtype.Timestamptz{Time: cursor.SweepBefore, Valid: true}, AfterAcceptedAt: at, AfterExecutionID: after, BatchSize: int32(limit)})
	if e != nil {
		return page, biz.ErrPersistence
	}
	page.ExaminedCount = len(rows)
	for _, row := range rows {
		item := biz.PendingAdmissionRow{TenantID: row.TenantID.String(), ExecutionID: row.ExecutionID.String(), AcceptedAt: row.AcceptedAt.Time.UTC()}
		a := biz.Admission{TenantID: item.TenantID, ExecutionID: item.ExecutionID, OperationID: row.OperationID.String(), Actor: row.Actor, IntentHash: row.IntentHash, SpecHash: row.SpecHash, AcceptedAt: item.AcceptedAt}
		var intentErr, snapshotErr error
		a.Intent, intentErr = cpup01.DecodeCanonicalIntent(row.IntentCanonical)
		a.Snapshot, snapshotErr = cpup01.DecodeCanonicalSnapshot(row.SnapshotCanonical)
		intent, snapshot, canonicalErr := a.CanonicalPayloads()
		if intentErr != nil || snapshotErr != nil || canonicalErr != nil || !bytes.Equal(intent, row.IntentCanonical) || !bytes.Equal(snapshot, row.SnapshotCanonical) || row.ClusterID.String() != a.Snapshot.Environment.ClusterID || row.EnvironmentBindingID.String() != a.Snapshot.Environment.BindingID || !row.DeadlineAt.Valid || !row.DeadlineAt.Time.Equal(a.Snapshot.DeadlineAt) {
			item.DecodeProblem = "PERSISTED_ADMISSION_CONFLICT"
		} else {
			item.Admission = a
		}
		page.Rows = append(page.Rows, item)
	}
	return page, nil
}

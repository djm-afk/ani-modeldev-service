package dataset

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	datasetsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/dataset/sqlc"
)

var _ biz.DatasetWorkerRepository = (*Repository)(nil)

func attemptFromRow(row datasetsql.ModeldevDatasetImportAttempt) (biz.DatasetImportAttempt, error) {
	if !row.TenantID.Valid || !row.AttemptID.Valid || !row.DatasetID.Valid || !row.TaskID.Valid || !row.DeadlineAt.Valid {
		return biz.DatasetImportAttempt{}, biz.ErrPersistence
	}
	attempt := biz.DatasetImportAttempt{
		TenantID: row.TenantID.String(), AttemptID: row.AttemptID.String(),
		DatasetID: row.DatasetID.String(), TaskID: row.TaskID.String(),
		AttemptNo: row.AttemptNo, RecoveryGeneration: row.RecoveryGeneration,
		ClusterID: uuidText(row.ClusterID), NamespaceName: row.NamespaceName, NamespaceUID: uuidText(row.NamespaceUid),
		JobName: row.JobName, PVCName: row.PvcName, SecretName: row.SecretName,
		RuntimePlan: row.RuntimePlan, RuntimePlanSHA256: row.RuntimePlanSha256,
		DataObjectKey: row.DataObjectKey, ResultObjectKey: row.ResultObjectKey,
		DeadlineAt: row.DeadlineAt.Time.UTC(), CleanupState: row.CleanupState,
		ReconcileOwner: row.ReconcileOwner.String, ReconcileEpoch: row.ReconcileEpoch,
	}
	if row.WritersStoppedAt.Valid {
		at := row.WritersStoppedAt.Time.UTC()
		attempt.WritersStoppedAt = &at
	}
	if row.DispatchClosedAt.Valid {
		at := row.DispatchClosedAt.Time.UTC()
		attempt.DispatchClosedAt = &at
	}
	if row.ReconcileUntil.Valid {
		at := row.ReconcileUntil.Time.UTC()
		attempt.ReconcileUntil = &at
	}
	if row.ReconcileNextAt.Valid {
		at := row.ReconcileNextAt.Time.UTC()
		attempt.ReconcileNextAt = &at
	}
	if row.ResultObjectVersionID.Valid {
		attempt.ResultVersionID = row.ResultObjectVersionID.String
	}
	if row.ResultSha256.Valid {
		attempt.ResultSHA256 = row.ResultSha256.String
	}
	return attempt, nil
}

func (r *Repository) ListDueTasks(ctx context.Context, now time.Time, batch int32) ([]biz.DatasetImportTask, error) {
	if r == nil || r.pool == nil {
		return nil, biz.ErrPersistence
	}
	if batch <= 0 || batch > 200 {
		batch = 50
	}
	rows, err := datasetsql.New(r.pool).ListDueTasks(ctx, datasetsql.ListDueTasksParams{
		Now: pgtype.Timestamptz{Time: now.UTC(), Valid: true}, BatchSize: batch,
	})
	if err != nil {
		return nil, biz.ErrPersistence
	}
	tasks := make([]biz.DatasetImportTask, 0, len(rows))
	for _, row := range rows {
		task, err := taskFromRow(row)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func (r *Repository) AcquireTaskLease(ctx context.Context, tenantID, taskID, owner string, ttl time.Duration) (biz.DatasetImportTask, error) {
	if r == nil || r.pool == nil || owner == "" || ttl <= 0 {
		return biz.DatasetImportTask{}, biz.ErrPersistence
	}
	tenant, id, err := tenantAndDataset(tenantID, taskID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	row, err := datasetsql.New(r.pool).AcquireTaskLease(ctx, datasetsql.AcquireTaskLeaseParams{
		Owner: pgtype.Text{String: owner, Valid: true}, Ttl: pgtype.Interval{Microseconds: ttl.Microseconds()},
		TenantID: tenant, TaskID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportTask{}, biz.ErrTaskLeaseLost
	}
	if err != nil {
		return biz.DatasetImportTask{}, biz.ErrPersistence
	}
	return taskFromRow(row)
}

func (r *Repository) ReleaseTaskLease(ctx context.Context, tenantID, taskID, owner string, epoch int64) error {
	if r == nil || r.pool == nil {
		return biz.ErrPersistence
	}
	tenant, id, err := tenantAndDataset(tenantID, taskID)
	if err != nil {
		return err
	}
	if err := datasetsql.New(r.pool).ReleaseTaskLease(ctx, datasetsql.ReleaseTaskLeaseParams{
		TenantID: tenant, TaskID: id, Owner: pgtype.Text{String: owner, Valid: true}, Epoch: epoch,
	}); err != nil {
		return biz.ErrPersistence
	}
	return nil
}

func (r *Repository) CountRunningAttempts(ctx context.Context) (int64, error) {
	if r == nil || r.pool == nil {
		return 0, biz.ErrPersistence
	}
	total, err := datasetsql.New(r.pool).CountRunningAttempts(ctx)
	if err != nil {
		return 0, biz.ErrPersistence
	}
	return total, nil
}

func (r *Repository) InsertAttempt(ctx context.Context, attempt biz.DatasetImportAttempt) (biz.DatasetImportAttempt, error) {
	if r == nil || r.pool == nil {
		return biz.DatasetImportAttempt{}, biz.ErrPersistence
	}
	tenant, datasetID, err := tenantAndDataset(attempt.TenantID, attempt.DatasetID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	attemptID, err := databaseID(attempt.AttemptID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	taskID, err := databaseID(attempt.TaskID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	cluster, err := databaseID(attempt.ClusterID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	namespaceUID, err := databaseID(attempt.NamespaceUID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	row, err := datasetsql.New(r.pool).InsertAttempt(ctx, datasetsql.InsertAttemptParams{
		TenantID: tenant, AttemptID: attemptID, DatasetID: datasetID, TaskID: taskID,
		AttemptNo: attempt.AttemptNo, RecoveryGeneration: attempt.RecoveryGeneration,
		ClusterID: cluster, NamespaceName: attempt.NamespaceName, NamespaceUid: namespaceUID,
		JobName: attempt.JobName, PvcName: attempt.PVCName, SecretName: attempt.SecretName,
		RuntimePlan: attempt.RuntimePlan, RuntimePlanSha256: attempt.RuntimePlanSHA256,
		DataObjectKey: attempt.DataObjectKey, ResultObjectKey: attempt.ResultObjectKey,
		DeadlineAt: pgtype.Timestamptz{Time: attempt.DeadlineAt.UTC(), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportAttempt{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.DatasetImportAttempt{}, biz.ErrPersistence
	}
	return attemptFromRow(row)
}

func (r *Repository) SetCurrentAttempt(ctx context.Context, tenantID, taskID, attemptID string) (biz.DatasetImportTask, error) {
	if r == nil || r.pool == nil {
		return biz.DatasetImportTask{}, biz.ErrPersistence
	}
	tenant, task, err := tenantAndDataset(tenantID, taskID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	attempt, err := databaseID(attemptID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	row, err := datasetsql.New(r.pool).SetCurrentAttempt(ctx, datasetsql.SetCurrentAttemptParams{
		AttemptID: attempt, TenantID: tenant, TaskID: task,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportTask{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.DatasetImportTask{}, biz.ErrPersistence
	}
	return taskFromRow(row)
}

func (r *Repository) ListDueAttempts(ctx context.Context, now time.Time, batch int32) ([]biz.DatasetImportAttempt, error) {
	if r == nil || r.pool == nil {
		return nil, biz.ErrPersistence
	}
	if batch <= 0 || batch > 200 {
		batch = 50
	}
	rows, err := datasetsql.New(r.pool).ListDueAttempts(ctx, datasetsql.ListDueAttemptsParams{
		Now: pgtype.Timestamptz{Time: now.UTC(), Valid: true}, BatchSize: batch,
	})
	if err != nil {
		return nil, biz.ErrPersistence
	}
	attempts := make([]biz.DatasetImportAttempt, 0, len(rows))
	for _, row := range rows {
		attempt, err := attemptFromRow(row)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, nil
}

func (r *Repository) AcquireAttemptLease(ctx context.Context, tenantID, attemptID, owner string, ttl time.Duration) (biz.DatasetImportAttempt, error) {
	if r == nil || r.pool == nil || owner == "" || ttl <= 0 {
		return biz.DatasetImportAttempt{}, biz.ErrPersistence
	}
	tenant, err := databaseID(tenantID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	id, err := databaseID(attemptID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	row, err := datasetsql.New(r.pool).AcquireAttemptLease(ctx, datasetsql.AcquireAttemptLeaseParams{
		Owner: pgtype.Text{String: owner, Valid: true}, Ttl: pgtype.Interval{Microseconds: ttl.Microseconds()},
		TenantID: tenant, AttemptID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportAttempt{}, biz.ErrAttemptLeaseLost
	}
	if err != nil {
		return biz.DatasetImportAttempt{}, biz.ErrPersistence
	}
	return attemptFromRow(row)
}

func (r *Repository) ClaimAttemptCleanup(ctx context.Context, tenantID, attemptID string) (biz.DatasetImportAttempt, error) {
	if r == nil || r.pool == nil {
		return biz.DatasetImportAttempt{}, biz.ErrPersistence
	}
	tenant, err := databaseID(tenantID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	id, err := databaseID(attemptID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	row, err := datasetsql.New(r.pool).ClaimAttemptCleanup(ctx, datasetsql.ClaimAttemptCleanupParams{TenantID: tenant, AttemptID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		// Not claimable: another worker owns it, or writers have not stopped.
		return biz.DatasetImportAttempt{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.DatasetImportAttempt{}, biz.ErrPersistence
	}
	return attemptFromRow(row)
}

func (r *Repository) FinishAttemptCleanup(ctx context.Context, tenantID, attemptID string) (biz.DatasetImportAttempt, error) {
	if r == nil || r.pool == nil {
		return biz.DatasetImportAttempt{}, biz.ErrPersistence
	}
	tenant, err := databaseID(tenantID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	id, err := databaseID(attemptID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	row, err := datasetsql.New(r.pool).FinishAttemptCleanup(ctx, datasetsql.FinishAttemptCleanupParams{TenantID: tenant, AttemptID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportAttempt{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.DatasetImportAttempt{}, biz.ErrPersistence
	}
	return attemptFromRow(row)
}

func (r *Repository) FreezeCandidate(ctx context.Context, tenantID, attemptID, versionID, sha256 string) (biz.DatasetImportAttempt, error) {
	if r == nil || r.pool == nil || versionID == "" || len(sha256) != 64 {
		return biz.DatasetImportAttempt{}, biz.ErrPersistence
	}
	tenant, err := databaseID(tenantID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	id, err := databaseID(attemptID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	row, err := datasetsql.New(r.pool).FreezeCandidate(ctx, datasetsql.FreezeCandidateParams{
		TenantID: tenant, AttemptID: id, VersionID: pgtype.Text{String: versionID, Valid: true},
		Sha256: pgtype.Text{String: sha256, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportAttempt{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.DatasetImportAttempt{}, biz.ErrPersistence
	}
	return attemptFromRow(row)
}

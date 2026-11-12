package dataset

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	datasetsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/dataset/sqlc"
)

// AllocateAttempt is the one place a physical attempt is born. Design §8.3
// requires the capacity decision and the insert to share a transaction: the
// capacity advisory lock is taken first, the live attempts are counted under it,
// and only then is the row written. Counting outside the lock would let two
// replicas both observe the last free slot.
func (r *Repository) AllocateAttempt(ctx context.Context, request biz.AttemptAllocationRequest) (biz.DatasetImportAttempt, error) {
	if r == nil || r.pool == nil || r.limit <= 0 {
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: attempt allocator not configured", biz.ErrPersistence)
	}
	tenant, datasetID, err := tenantAndDataset(request.Task.TenantID, request.Task.DatasetID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	taskID, err := databaseID(request.Task.TaskID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	attemptID, err := databaseID(request.AttemptID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	clusterID, err := databaseID(request.Task.ClusterID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	namespaceUID, err := databaseID(request.Task.NamespaceUID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	if request.Plan == nil {
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: attempt plan callback is required", biz.ErrPersistence)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
	}
	defer tx.Rollback(ctx)
	queries := datasetsql.New(tx)

	if err := queries.LockImportCapacity(ctx); err != nil {
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: lock import capacity: %v", biz.ErrPersistence, err)
	}
	running, err := queries.CountRunningAttempts(ctx)
	if err != nil {
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: count running attempts: %v", biz.ErrPersistence, err)
	}
	if err := biz.ClaimsSlot(r.limit, running); err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	attemptNo, err := queries.NextAttemptNumber(ctx, datasetsql.NextAttemptNumberParams{TenantID: tenant, TaskID: taskID})
	if err != nil {
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: next attempt number: %v", biz.ErrPersistence, err)
	}
	plan, err := request.Plan(ctx, attemptNo)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	if plan.DeadlineAt.IsZero() || len(plan.RuntimePlan) == 0 || plan.RuntimePlanSHA256 == "" ||
		plan.JobName == "" || plan.PVCName == "" || plan.SecretName == "" ||
		plan.DataObjectKey == "" || plan.ResultObjectKey == "" {
		return biz.DatasetImportAttempt{}, biz.ErrDatasetInvalidMetadata
	}
	row, err := queries.InsertAttempt(ctx, datasetsql.InsertAttemptParams{
		TenantID: tenant, AttemptID: attemptID, DatasetID: datasetID, TaskID: taskID,
		AttemptNo: attemptNo, RecoveryGeneration: request.Task.RecoveryGeneration,
		ClusterID: clusterID, NamespaceName: request.Task.NamespaceName, NamespaceUid: namespaceUID,
		JobName: plan.JobName, PvcName: plan.PVCName, SecretName: plan.SecretName,
		RuntimePlan: plan.RuntimePlan, RuntimePlanSha256: plan.RuntimePlanSHA256,
		DataObjectKey: plan.DataObjectKey, ResultObjectKey: plan.ResultObjectKey,
		DeadlineAt: pgtype.Timestamptz{Time: plan.DeadlineAt.UTC(), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT DO NOTHING: the attempt identity is already taken.
		return biz.DatasetImportAttempt{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: insert attempt: %v", biz.ErrPersistence, err)
	}
	// Binding happens in the same transaction as the insert. Splitting them let a
	// second party bind the attempt first, after which this UPDATE matched no row
	// and the task was failed as a conflict while the slot stayed taken. Rolling
	// back here releases the slot with the attempt.
	if _, err := queries.SetCurrentAttempt(ctx, datasetsql.SetCurrentAttemptParams{
		AttemptID: attemptID, TenantID: tenant, TaskID: taskID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The task moved on (cancelled, or already bound elsewhere) between the
			// scan and this transaction. Nothing was allocated.
			return biz.DatasetImportAttempt{}, biz.ErrImportStateConflict
		}
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: bind attempt: %v", biz.ErrPersistence, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
	}
	return attemptFromRow(row)
}

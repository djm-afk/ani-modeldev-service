package dataset

import (
	"context"
	"errors"
	"fmt"
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
		JobUID: row.JobUid.String, JobResourceVersion: row.JobResourceVersion.String,
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

func (r *Repository) ListDueTasks(ctx context.Context, now time.Time, batch int32, includeDispatch bool) ([]biz.DatasetImportTask, error) {
	if r == nil || r.pool == nil {
		return nil, biz.ErrPersistence
	}
	if batch <= 0 || batch > 200 {
		batch = 50
	}
	rows, err := datasetsql.New(r.pool).ListDueTasks(ctx, datasetsql.ListDueTasksParams{
		Now: pgtype.Timestamptz{Time: now.UTC(), Valid: true}, BatchSize: batch,
		IncludeDispatch: includeDispatch,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: list due tasks: %v", biz.ErrPersistence, err)
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
		Owner: pgtype.Text{String: owner, Valid: true}, TtlSeconds: ttl.Seconds(),
		TenantID: tenant, TaskID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportTask{}, biz.ErrTaskLeaseLost
	}
	if err != nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: acquire task lease: %v", biz.ErrPersistence, err)
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

func (r *Repository) GetAttempt(ctx context.Context, tenantID, attemptID string) (biz.DatasetImportAttempt, error) {
	if r == nil || r.pool == nil {
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: repository not configured", biz.ErrPersistence)
	}
	tenant, err := databaseID(tenantID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	id, err := databaseID(attemptID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	row, err := datasetsql.New(r.pool).GetAttempt(ctx, datasetsql.GetAttemptParams{TenantID: tenant, AttemptID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportAttempt{}, biz.ErrImportTaskNotFound
	}
	if err != nil {
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: read attempt: %v", biz.ErrPersistence, err)
	}
	return attemptFromRow(row)
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
		Owner: pgtype.Text{String: owner, Valid: true}, TtlSeconds: ttl.Seconds(),
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

func (r *Repository) ReleaseAttemptLease(ctx context.Context, tenantID, attemptID, owner string, epoch int64) error {
	if r == nil || r.pool == nil {
		return biz.ErrPersistence
	}
	tenant, err := databaseID(tenantID)
	if err != nil {
		return err
	}
	id, err := databaseID(attemptID)
	if err != nil {
		return err
	}
	if err := datasetsql.New(r.pool).ReleaseAttemptLease(ctx, datasetsql.ReleaseAttemptLeaseParams{
		TenantID: tenant, AttemptID: id, Owner: pgtype.Text{String: owner, Valid: true}, Epoch: epoch,
	}); err != nil {
		return biz.ErrPersistence
	}
	return nil
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
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: freeze candidate: %v", biz.ErrPersistence, err)
	}
	return attemptFromRow(row)
}

// StartTask moves a QUEUED task to RUNNING under the caller's live lease.
func (r *Repository) StartTask(ctx context.Context, tenantID, taskID, leaseOwner string, leaseEpoch int64) (biz.DatasetImportTask, error) {
	if r == nil || r.pool == nil || leaseOwner == "" || leaseEpoch <= 0 {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: task start requires a live lease", biz.ErrPersistence)
	}
	tenant, task, err := tenantAndDataset(tenantID, taskID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	row, err := datasetsql.New(r.pool).StartTask(ctx, datasetsql.StartTaskParams{
		TenantID: tenant, TaskID: task, LeaseOwner: pgtype.Text{String: leaseOwner, Valid: true}, LeaseEpoch: leaseEpoch,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportTask{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: start import task: %v", biz.ErrPersistence, err)
	}
	return taskFromRow(row)
}

// FreezeTaskCandidate pins the task to exactly one verified attempt.
func (r *Repository) FreezeTaskCandidate(ctx context.Context, freeze biz.CandidateFreeze) (biz.DatasetImportTask, error) {
	if r == nil || r.pool == nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: repository not configured", biz.ErrPersistence)
	}
	tenant, task, err := tenantAndDataset(freeze.TenantID, freeze.TaskID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	attempt, err := databaseID(freeze.AttemptID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	row, err := datasetsql.New(r.pool).FreezeTaskCandidate(ctx, datasetsql.FreezeTaskCandidateParams{
		TenantID: tenant, TaskID: task, AttemptID: attempt,
		ObjectKey: pgtype.Text{String: freeze.ObjectKey, Valid: true},
		VersionID: pgtype.Text{String: freeze.VersionID, Valid: true},
		SizeBytes: freeze.SizeBytes, Sha256: pgtype.Text{String: freeze.SHA256, Valid: true},
		ReportVersionID: pgtype.Text{String: freeze.ReportVersionID, Valid: true},
		ReportSha256:    pgtype.Text{String: freeze.ReportSHA256, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportTask{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: freeze task candidate: %v", biz.ErrPersistence, err)
	}
	return taskFromRow(row)
}

// FailTask records a terminal failure with the caller's retryability verdict.
// The IMPORT_FAILED history entry is written in the same transaction, so a task
// can never reach FAILED without its event.
func (r *Repository) FailTask(ctx context.Context, failure biz.TaskFailure) (biz.DatasetImportTask, error) {
	if r == nil || r.pool == nil || failure.Code == "" || failure.OperationID == "" || failure.Actor == "" {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: task failure requires a code, an operation id and an actor", biz.ErrPersistence)
	}
	tenant, task, err := tenantAndDataset(failure.TenantID, failure.TaskID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	detail := failure.Detail
	if len(detail) == 0 {
		detail = []byte("{}")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
	}
	defer tx.Rollback(ctx)
	queries := datasetsql.New(tx)
	row, err := queries.FailTask(ctx, datasetsql.FailTaskParams{
		TenantID: tenant, TaskID: task, FailureCode: pgtype.Text{String: failure.Code, Valid: true},
		FailureDetail: detail, Retryable: failure.Retryable,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportTask{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: fail import task: %v", biz.ErrPersistence, err)
	}
	if err := insertTerminalOperation(ctx, queries, terminalOperation{
		TenantID: failure.TenantID, OperationID: failure.OperationID, DatasetID: row.DatasetID.String(),
		TaskID: failure.TaskID, Actor: failure.Actor, EventType: biz.EventImportFailed,
		Result: "FAILED", FailureCode: failure.Code, RecoveryGeneration: row.RecoveryGeneration,
	}); err != nil {
		return biz.DatasetImportTask{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
	}
	return taskFromRow(row)
}

// FinishCancelledTask ends a CANCELING task once no writer remains, together
// with its IMPORT_CANCELLED history entry.
func (r *Repository) FinishCancelledTask(ctx context.Context, cancellation biz.TaskCancellation) (biz.DatasetImportTask, error) {
	if r == nil || r.pool == nil || cancellation.OperationID == "" || cancellation.Actor == "" {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: cancellation requires an operation id and an actor", biz.ErrPersistence)
	}
	tenant, task, err := tenantAndDataset(cancellation.TenantID, cancellation.TaskID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
	}
	defer tx.Rollback(ctx)
	queries := datasetsql.New(tx)
	row, err := queries.FinishCancelledTask(ctx, datasetsql.FinishCancelledTaskParams{TenantID: tenant, TaskID: task})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportTask{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: finish cancelled task: %v", biz.ErrPersistence, err)
	}
	if err := insertTerminalOperation(ctx, queries, terminalOperation{
		TenantID: cancellation.TenantID, OperationID: cancellation.OperationID, DatasetID: row.DatasetID.String(),
		TaskID: cancellation.TaskID, Actor: cancellation.Actor, EventType: biz.EventImportCanceled,
		Result: "CANCELLED", RecoveryGeneration: row.RecoveryGeneration,
	}); err != nil {
		return biz.DatasetImportTask{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
	}
	return taskFromRow(row)
}

// terminalOperation is one task-result history entry. It is written inside the
// same transaction as the state change it describes.
type terminalOperation struct {
	TenantID           string
	OperationID        string
	DatasetID          string
	TaskID             string
	Actor              string
	EventType          biz.DatasetEventType
	Result             string
	FailureCode        string
	RecoveryGeneration int64
}

// insertTerminalOperation appends the result event. The caller owns the
// transaction, so a failed insert rolls the state change back with it.
func insertTerminalOperation(ctx context.Context, queries *datasetsql.Queries, operation terminalOperation) error {
	tenant, datasetID, err := tenantAndDataset(operation.TenantID, operation.DatasetID)
	if err != nil {
		return err
	}
	operationID, err := databaseID(operation.OperationID)
	if err != nil {
		return err
	}
	taskID, err := databaseID(operation.TaskID)
	if err != nil {
		return err
	}
	params := datasetsql.InsertDatasetOperationParams{
		TenantID: tenant, OperationID: operationID, DatasetID: datasetID, TaskID: taskID,
		Actor: operation.Actor, EventType: string(operation.EventType), Result: operation.Result,
		Detail: []byte(`{}`),
	}
	if operation.FailureCode != "" {
		params.FailureCode = pgtype.Text{String: operation.FailureCode, Valid: true}
	}
	if operation.RecoveryGeneration > 0 {
		params.RecoveryGeneration = pgtype.Int8{Int64: operation.RecoveryGeneration, Valid: true}
	}
	if _, err := queries.InsertDatasetOperation(ctx, params); err != nil {
		return fmt.Errorf("%w: insert terminal operation: %v", biz.ErrPersistence, err)
	}
	return nil
}

// DeferTask pushes the next scan out without ending the task.
func (r *Repository) DeferTask(ctx context.Context, deferral biz.TaskDeferral) error {
	if r == nil || r.pool == nil || deferral.Delay <= 0 {
		return fmt.Errorf("%w: deferral requires a positive delay", biz.ErrPersistence)
	}
	tenant, task, err := tenantAndDataset(deferral.TenantID, deferral.TaskID)
	if err != nil {
		return err
	}
	if err := datasetsql.New(r.pool).DeferTask(ctx, datasetsql.DeferTaskParams{
		TenantID: tenant, TaskID: task,
		// Valid must be set: an unbound pgtype.Interval is written as NULL, which
		// violates next_run_at NOT NULL and turns a bounded retry into a hard
		// persistence failure.
		Delay:       pgtype.Interval{Microseconds: deferral.Delay.Microseconds(), Valid: true},
		FailureCode: pgtype.Text{String: deferral.FailureCode, Valid: deferral.FailureCode != ""},
	}); err != nil {
		return fmt.Errorf("%w: defer import task: %v", biz.ErrPersistence, err)
	}
	return nil
}

// CloseAttempt stops dispatch for one attempt and records why.
func (r *Repository) CloseAttempt(ctx context.Context, tenantID, attemptID, reason string) (biz.DatasetImportAttempt, error) {
	if r == nil || r.pool == nil || reason == "" {
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: attempt close requires a reason", biz.ErrPersistence)
	}
	tenant, err := databaseID(tenantID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	id, err := databaseID(attemptID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	row, err := datasetsql.New(r.pool).CloseAttempt(ctx, datasetsql.CloseAttemptParams{
		TenantID: tenant, AttemptID: id, CloseReason: pgtype.Text{String: reason, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportAttempt{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: close attempt: %v", biz.ErrPersistence, err)
	}
	return attemptFromRow(row)
}

// ConfirmWritersStopped records that the attempt no longer owns a live writer.
// It is the only write that releases a capacity slot, and it requires the
// dispatch gate to be closed first.
func (r *Repository) ConfirmWritersStopped(ctx context.Context, tenantID, attemptID string) (biz.DatasetImportAttempt, error) {
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
	row, err := datasetsql.New(r.pool).ConfirmWritersStopped(ctx, datasetsql.ConfirmWritersStoppedParams{
		TenantID: tenant, AttemptID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Already confirmed, or the dispatch gate is still open. Both are a state
		// conflict for the caller, never a silent success.
		return biz.DatasetImportAttempt{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: confirm writers stopped: %v", biz.ErrPersistence, err)
	}
	return attemptFromRow(row)
}

// RecordAttemptJob binds the observed Job identity after a create. A different
// UID is a conflict, never an adoption.
func (r *Repository) RecordAttemptJob(ctx context.Context, tenantID, attemptID, jobUID, resourceVersion string) (biz.DatasetImportAttempt, error) {
	if r == nil || r.pool == nil || jobUID == "" || resourceVersion == "" {
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: attempt Job identity is incomplete", biz.ErrPersistence)
	}
	tenant, err := databaseID(tenantID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	id, err := databaseID(attemptID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	row, err := datasetsql.New(r.pool).RecordAttemptJob(ctx, datasetsql.RecordAttemptJobParams{
		TenantID: tenant, AttemptID: id,
		JobUid: pgtype.Text{String: jobUID, Valid: true}, JobResourceVersion: pgtype.Text{String: resourceVersion, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportAttempt{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: record attempt Job: %v", biz.ErrPersistence, err)
	}
	return attemptFromRow(row)
}

// RecordAttemptObservation stores the latest bounded runtime observation.
func (r *Repository) RecordAttemptObservation(ctx context.Context, tenantID, attemptID string, observation []byte) (biz.DatasetImportAttempt, error) {
	if r == nil || r.pool == nil || len(observation) == 0 {
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: observation is empty", biz.ErrPersistence)
	}
	tenant, err := databaseID(tenantID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	id, err := databaseID(attemptID)
	if err != nil {
		return biz.DatasetImportAttempt{}, err
	}
	row, err := datasetsql.New(r.pool).RecordAttemptObservation(ctx, datasetsql.RecordAttemptObservationParams{
		TenantID: tenant, AttemptID: id, Observation: observation,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportAttempt{}, biz.ErrImportTaskNotFound
	}
	if err != nil {
		return biz.DatasetImportAttempt{}, fmt.Errorf("%w: record attempt observation: %v", biz.ErrPersistence, err)
	}
	return attemptFromRow(row)
}

// ClearCurrentAttempt detaches a closed attempt from the task so the next
// attempt can be allocated.
func (r *Repository) ClearCurrentAttempt(ctx context.Context, tenantID, taskID, attemptID string) (biz.DatasetImportTask, error) {
	if r == nil || r.pool == nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: repository not configured", biz.ErrPersistence)
	}
	tenant, task, err := tenantAndDataset(tenantID, taskID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	attempt, err := databaseID(attemptID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	row, err := datasetsql.New(r.pool).ClearCurrentAttempt(ctx, datasetsql.ClearCurrentAttemptParams{
		TenantID: tenant, TaskID: task, AttemptID: attempt,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportTask{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: clear current attempt: %v", biz.ErrPersistence, err)
	}
	return taskFromRow(row)
}

// SetTaskEnvironment records the execution-registry row seen at dispatch time.
func (r *Repository) SetTaskEnvironment(ctx context.Context, tenantID, taskID, environmentID string) (biz.DatasetImportTask, error) {
	if r == nil || r.pool == nil || environmentID == "" {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: environment id is required", biz.ErrPersistence)
	}
	tenant, task, err := tenantAndDataset(tenantID, taskID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	environment, err := databaseID(environmentID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	row, err := datasetsql.New(r.pool).SetTaskEnvironment(ctx, datasetsql.SetTaskEnvironmentParams{
		TenantID: tenant, TaskID: task, EnvironmentID: environment,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportTask{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: set task environment: %v", biz.ErrPersistence, err)
	}
	return taskFromRow(row)
}

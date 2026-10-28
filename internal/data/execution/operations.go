package execution

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	executionsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution/sqlc"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
)

func (repository *Repository) GetOperationRecord(ctx context.Context, tenant, execution string) (biz.OperationRecord, error) {
	if ctx == nil || repository == nil || repository.pool == nil {
		return biz.OperationRecord{}, biz.ErrPersistence
	}
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return biz.OperationRecord{}, biz.ErrPersistence
	}
	defer rollbackExecutionTransaction(transaction)
	record, err := operationRecord(ctx, transaction, tenant, execution)
	if err != nil {
		return biz.OperationRecord{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return biz.OperationRecord{}, biz.ErrPersistence
	}
	return record, nil
}

func operationRecord(ctx context.Context, transaction pgx.Tx, tenant, execution string) (biz.OperationRecord, error) {
	tenantID, err := databaseID(tenant)
	if err != nil {
		return biz.OperationRecord{}, err
	}
	executionID, err := databaseID(execution)
	if err != nil {
		return biz.OperationRecord{}, err
	}
	query, err := queryRecord(ctx, transaction, tenantID, executionID)
	if err != nil {
		return biz.OperationRecord{}, err
	}
	record := biz.OperationRecord{QueryRecord: query}
	var audit []byte
	err = transaction.QueryRow(ctx, "SELECT receipt FROM modeldev_execution_cleanup_audits WHERE tenant_id=$1 AND execution_id=$2 ORDER BY started_at DESC, plan_sha256 DESC LIMIT 1", tenantID, executionID).Scan(&audit)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return biz.OperationRecord{}, biz.ErrPersistence
	}
	if err == nil {
		receipt, decodeErr := decodeCurrentCleanupReceipt(audit)
		if decodeErr != nil || receipt.ExecutionID != execution {
			return biz.OperationRecord{}, biz.ErrPersistence
		}
		record.Cleanup = &receipt
	}
	dispatch, err := submission.ReadInTransaction(ctx, transaction, tenant, execution)
	if errors.Is(err, biz.ErrExecutionNotFound) {
		return record, nil
	}
	if err != nil || dispatch.OwnerRevision != query.Execution.OwnerRevision {
		return biz.OperationRecord{}, biz.ErrPersistence
	}
	record.Dispatch = &dispatch
	authority, err := submission.ReadAuthorityInTransaction(ctx, transaction, tenant, execution)
	if errors.Is(err, biz.ErrExecutionNotFound) {
		record.Authority = query.Runtime.CloseAuthority
		return record, nil
	}
	if err != nil || authority.OwnerRevision != query.Execution.OwnerRevision {
		return biz.OperationRecord{}, biz.ErrPersistence
	}
	record.Authority = &authority.RunAuthorityCandidate
	return record, nil
}

func (repository *Repository) ReserveCleanup(ctx context.Context, plan biz.CleanupPlan, actor string) (biz.CleanupReceipt, bool, error) {
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return biz.CleanupReceipt{}, false, biz.ErrPersistence
	}
	defer rollbackExecutionTransaction(transaction)
	if err := lockOperation(ctx, transaction, plan.TenantID, plan.ExecutionID); err != nil {
		return biz.CleanupReceipt{}, false, err
	}
	record, err := operationRecord(ctx, transaction, plan.TenantID, plan.ExecutionID)
	if err != nil {
		return biz.CleanupReceipt{}, false, err
	}
	current, err := biz.CleanupPlanFor(record)
	if err != nil || current.PlanHash != plan.PlanHash {
		return biz.CleanupReceipt{}, false, biz.ErrCleanupConflict
	}
	var previous []byte
	err = transaction.QueryRow(ctx, "SELECT receipt FROM modeldev_execution_cleanup_audits WHERE tenant_id=$1::uuid AND execution_id=$2::uuid AND plan_sha256=$3", plan.TenantID, plan.ExecutionID, plan.PlanHash).Scan(&previous)
	if err == nil {
		receipt, decodeErr := decodeCurrentCleanupReceipt(previous)
		if decodeErr != nil {
			return biz.CleanupReceipt{}, false, biz.ErrPersistence
		}
		return receipt, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return biz.CleanupReceipt{}, false, biz.ErrPersistence
	}
	var now time.Time
	if err := transaction.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return biz.CleanupReceipt{}, false, biz.ErrPersistence
	}
	receipt := biz.CleanupReceipt{ExecutionID: plan.ExecutionID, PlanHash: plan.PlanHash, Phase: "STARTED", Actor: actor, StartedAt: now.UTC(), Requested: []biz.RuntimeResource{}, ConfirmedAbsent: []biz.RuntimeResource{}}
	planBytes, err := json.Marshal(plan)
	if err != nil {
		return biz.CleanupReceipt{}, false, biz.ErrPersistence
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return biz.CleanupReceipt{}, false, biz.ErrPersistence
	}
	_, err = transaction.Exec(ctx, "INSERT INTO modeldev_execution_cleanup_audits (tenant_id,execution_id,plan_sha256,actor,phase,plan,receipt,started_at) VALUES ($1::uuid,$2::uuid,$3,$4,'STARTED',$5::jsonb,$6::jsonb,$7)", plan.TenantID, plan.ExecutionID, plan.PlanHash, actor, planBytes, encoded, now)
	if err != nil {
		return biz.CleanupReceipt{}, false, biz.ErrPersistence
	}
	if err := transaction.Commit(ctx); err != nil {
		return biz.CleanupReceipt{}, false, biz.ErrPersistence
	}
	return receipt, false, nil
}

func (repository *Repository) ExecuteCleanup(ctx context.Context, tenant, execution, hash string, apply func(biz.OperationRecord) (biz.CleanupReceipt, error)) (biz.CleanupReceipt, error) {
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return biz.CleanupReceipt{}, biz.ErrPersistence
	}
	defer rollbackExecutionTransaction(transaction)
	if err := lockOperation(ctx, transaction, tenant, execution); err != nil {
		return biz.CleanupReceipt{}, err
	}
	record, err := operationRecord(ctx, transaction, tenant, execution)
	if err != nil {
		return biz.CleanupReceipt{}, err
	}
	if record.Cleanup == nil || record.Cleanup.PlanHash != hash || record.Cleanup.Phase != "STARTED" || apply == nil {
		return biz.CleanupReceipt{}, biz.ErrCleanupUncertain
	}
	// The first committed STARTED row survives this transaction failing or the
	// process exiting after a DELETE request. No automatic re-send is granted.
	receipt, applyErr := apply(record)
	if receipt.ExecutionID != execution || receipt.PlanHash != hash || receipt.Actor != record.Cleanup.Actor || receipt.StartedAt != record.Cleanup.StartedAt || (receipt.Phase != "APPLIED" && receipt.Phase != "NEEDS_REVIEW") {
		return biz.CleanupReceipt{}, biz.ErrCleanupUncertain
	}
	var completed time.Time
	if err := transaction.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&completed); err != nil {
		return receipt, biz.ErrCleanupUncertain
	}
	receipt.CompletedAt = completed.UTC()
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return receipt, biz.ErrCleanupUncertain
	}
	result, err := transaction.Exec(ctx, "UPDATE modeldev_execution_cleanup_audits SET phase=$4,receipt=$5::jsonb,completed_at=$6 WHERE tenant_id=$1::uuid AND execution_id=$2::uuid AND plan_sha256=$3 AND phase='STARTED'", tenant, execution, hash, receipt.Phase, encoded, completed)
	if err != nil || result.RowsAffected() != 1 {
		return receipt, biz.ErrCleanupUncertain
	}
	if err := transaction.Commit(ctx); err != nil {
		return receipt, biz.ErrCleanupUncertain
	}
	return receipt, applyErr
}

// ReconcileCleanup verifies a prior uncertain attempt without granting another
// DELETE. The external observation runs after the read transaction has ended.
func (repository *Repository) ReconcileCleanup(ctx context.Context, tenant, execution, hash string, resolve func(biz.OperationRecord) (biz.CleanupReceipt, error)) (biz.CleanupReceipt, error) {
	if ctx == nil || repository == nil || repository.pool == nil {
		return biz.CleanupReceipt{}, biz.ErrPersistence
	}
	snapshot, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return biz.CleanupReceipt{}, biz.ErrPersistence
	}
	defer rollbackExecutionTransaction(snapshot)
	record, plan, originalDatabaseReceipt, err := cleanupRecoverySnapshot(ctx, snapshot, tenant, execution, hash)
	if err != nil {
		return biz.CleanupReceipt{}, err
	}
	if err := snapshot.Commit(ctx); err != nil {
		return biz.CleanupReceipt{}, biz.ErrPersistence
	}
	originalBytes, err := json.Marshal(record.Cleanup)
	if err != nil {
		return biz.CleanupReceipt{}, biz.ErrPersistence
	}
	// Freeze the audit independently of the callback's mutable record/slices.
	var original biz.CleanupReceipt
	if json.Unmarshal(originalBytes, &original) != nil {
		return biz.CleanupReceipt{}, biz.ErrPersistence
	}
	if original.Phase == "APPLIED" || original.Phase == "RECONCILED" {
		return original, nil
	}
	if (original.Phase != "STARTED" && original.Phase != "NEEDS_REVIEW") || resolve == nil {
		return original, biz.ErrCleanupUncertain
	}
	if len(original.ResolvedAbsent) != 0 || !original.ReconciledAt.IsZero() || original.ReconciledFromPhase != "" {
		return original, biz.ErrCleanupConflict
	}
	currentPlan, err := biz.CleanupPlanFor(record)
	if err != nil || !reflect.DeepEqual(currentPlan, plan) {
		return original, biz.ErrCleanupConflict
	}
	receipt, err := resolve(record)
	if err != nil {
		return original, err
	}
	if receipt.ExecutionID != original.ExecutionID || receipt.PlanHash != original.PlanHash || receipt.Phase != "RECONCILED" || receipt.Actor != original.Actor ||
		!receipt.StartedAt.Equal(original.StartedAt) || !receipt.CompletedAt.Equal(original.CompletedAt) ||
		!reflect.DeepEqual(receipt.Requested, original.Requested) || !reflect.DeepEqual(receipt.ConfirmedAbsent, original.ConfirmedAbsent) ||
		receipt.ReconciledFromPhase != original.Phase || !receipt.ReconciledAt.IsZero() || !reflect.DeepEqual(receipt.ResolvedAbsent, plan.Targets) {
		return original, biz.ErrCleanupConflict
	}
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return original, biz.ErrPersistence
	}
	defer rollbackExecutionTransaction(transaction)
	if err := lockOperation(ctx, transaction, tenant, execution); err != nil {
		return original, err
	}
	fresh, freshPlan, _, err := cleanupRecoverySnapshot(ctx, transaction, tenant, execution, hash)
	if err != nil || fresh.Execution.OwnerRevision != record.Execution.OwnerRevision || !reflect.DeepEqual(freshPlan, plan) || !reflect.DeepEqual(fresh.Cleanup, &original) {
		return original, biz.ErrCleanupConflict
	}
	currentPlan, err = biz.CleanupPlanFor(fresh)
	if err != nil || !reflect.DeepEqual(currentPlan, plan) {
		return original, biz.ErrCleanupConflict
	}
	var now time.Time
	if err := transaction.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return original, biz.ErrPersistence
	}
	// Keep the original apply audit, including a NULL completed_at for STARTED.
	receipt = original
	receipt.Phase, receipt.ReconciledFromPhase = "RECONCILED", original.Phase
	receipt.ResolvedAbsent, receipt.ReconciledAt = plan.Targets, now.UTC()
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return original, biz.ErrPersistence
	}
	planBytes, err := json.Marshal(plan)
	if err != nil {
		return original, biz.ErrPersistence
	}
	// Compare the original stored JSON value; never replace the audit snapshot
	// used by the optimistic concurrency guard with a re-encoded value.
	result, err := transaction.Exec(ctx, "UPDATE modeldev_execution_cleanup_audits SET phase='RECONCILED',receipt=$4::jsonb WHERE tenant_id=$1::uuid AND execution_id=$2::uuid AND plan_sha256=$3 AND phase=$5 AND receipt=$6::jsonb AND plan=$7::jsonb AND actor=$8 AND started_at=$9", tenant, execution, hash, encoded, original.Phase, originalDatabaseReceipt, planBytes, original.Actor, original.StartedAt)
	if err != nil {
		return original, biz.ErrCleanupUncertain
	}
	if result.RowsAffected() != 1 {
		return original, biz.ErrCleanupConflict
	}
	if err := transaction.Commit(ctx); err != nil {
		return original, biz.ErrCleanupUncertain
	}
	return receipt, nil
}

func decodeCurrentCleanupReceipt(encoded []byte) (biz.CleanupReceipt, error) {
	// The current writer emits a timestamp string even before reconciliation.
	// A missing/null field is not the current audit format. The other resolution
	// fields intentionally remain optional until a real observation is recorded.
	var stamp struct {
		ReconciledAt *time.Time `json:"reconciled_at"`
	}
	var receipt biz.CleanupReceipt
	if json.Unmarshal(encoded, &stamp) != nil || stamp.ReconciledAt == nil || json.Unmarshal(encoded, &receipt) != nil {
		return biz.CleanupReceipt{}, biz.ErrPersistence
	}
	return receipt, nil
}

func cleanupRecoverySnapshot(ctx context.Context, transaction pgx.Tx, tenant, execution, hash string) (biz.OperationRecord, biz.CleanupPlan, []byte, error) {
	record, err := operationRecord(ctx, transaction, tenant, execution)
	if err != nil {
		return biz.OperationRecord{}, biz.CleanupPlan{}, nil, err
	}
	if record.Cleanup == nil || record.Cleanup.PlanHash != hash {
		return biz.OperationRecord{}, biz.CleanupPlan{}, nil, biz.ErrCleanupConflict
	}
	var phase, actor string
	var started time.Time
	var completed *time.Time
	var encoded []byte
	var receipt []byte
	err = transaction.QueryRow(ctx, "SELECT phase,actor,started_at,completed_at,plan,receipt FROM modeldev_execution_cleanup_audits WHERE tenant_id=$1::uuid AND execution_id=$2::uuid AND plan_sha256=$3", tenant, execution, hash).Scan(&phase, &actor, &started, &completed, &encoded, &receipt)
	var plan biz.CleanupPlan
	if err != nil || json.Unmarshal(encoded, &plan) != nil || plan.TenantID != tenant || plan.ExecutionID != execution || plan.PlanHash != hash || phase != record.Cleanup.Phase || actor != record.Cleanup.Actor || !started.Equal(record.Cleanup.StartedAt) ||
		(completed == nil && !record.Cleanup.CompletedAt.IsZero()) || (completed != nil && !completed.Equal(record.Cleanup.CompletedAt)) {
		return biz.OperationRecord{}, biz.CleanupPlan{}, nil, biz.ErrCleanupConflict
	}
	return record, plan, receipt, nil
}

func lockOperation(ctx context.Context, transaction pgx.Tx, tenant, execution string) error {
	tenantID, err := databaseID(tenant)
	if err != nil {
		return err
	}
	executionID, err := databaseID(execution)
	if err != nil {
		return err
	}
	_, err = executionsql.New(transaction).LockExecutionIdentity(ctx, executionsql.LockExecutionIdentityParams{TenantID: tenantID, ExecutionID: executionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.ErrExecutionNotFound
	}
	if err != nil {
		return biz.ErrPersistence
	}
	return nil
}

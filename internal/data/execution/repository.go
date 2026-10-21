// Package execution implements the ModelDev execution persistence boundary.
package execution

import (
	"bytes"
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	executionsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution/sqlc"
)

type Repository struct {
	pool *pgxpool.Pool
}

var _ biz.ExecutionRepository = (*Repository)(nil)

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Accept(ctx context.Context, admission biz.Admission) (biz.AcceptReceipt, error) {
	intent, snapshot, err := admission.CanonicalPayloads()
	if err != nil {
		return biz.AcceptReceipt{}, err
	}
	tenantID, err := databaseID(admission.TenantID)
	if err != nil {
		return biz.AcceptReceipt{}, err
	}
	executionID, err := databaseID(admission.ExecutionID)
	if err != nil {
		return biz.AcceptReceipt{}, err
	}
	operationID, err := databaseID(admission.OperationID)
	if err != nil {
		return biz.AcceptReceipt{}, err
	}
	command := executionsql.InsertExecutionParams{
		TenantID:          tenantID,
		ExecutionID:       executionID,
		OperationID:       operationID,
		Actor:             admission.Actor,
		IntentCanonical:   intent,
		IntentHash:        admission.IntentHash,
		SnapshotCanonical: snapshot,
		SpecHash:          admission.SpecHash,
		AcceptedAt:        pgtype.Timestamptz{Time: admission.AcceptedAt.UTC(), Valid: true},
	}
	transaction, queries, err := r.lockIdentity(ctx, executionsql.InsertExecutionIdentityParams{
		TenantID: tenantID, ExecutionID: executionID, OperationID: operationID, SpecHash: admission.SpecHash,
	})
	if err != nil {
		return biz.AcceptReceipt{}, err
	}
	defer rollbackExecutionTransaction(transaction)
	// The identity reservation and admission payload commit together. A close
	// tombstone and Admission can never reserve this identity independently.
	replayed := false
	row, err := queries.InsertExecution(ctx, command)
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = queries.GetExecution(ctx, executionsql.GetExecutionParams{
			TenantID: tenantID, ExecutionID: executionID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return biz.AcceptReceipt{}, biz.ErrAdmissionConflict
		}
		if err != nil {
			return biz.AcceptReceipt{}, biz.ErrPersistence
		}
		if !sameAdmission(row, command) {
			return biz.AcceptReceipt{}, biz.ErrAdmissionConflict
		}
		replayed = true
	} else if err != nil {
		return biz.AcceptReceipt{}, biz.ErrPersistence
	}
	execution, err := executionWithClose(ctx, queries, row)
	if err != nil {
		return biz.AcceptReceipt{}, err
	}
	if replayed {
		execution.OwnerRevision, err = currentOwnerRevision(ctx, queries, tenantID, executionID)
	} else {
		execution.OwnerRevision, err = advanceOwnerRevision(ctx, queries, tenantID, executionID)
	}
	if err != nil {
		return biz.AcceptReceipt{}, err
	}
	execution.States, err = executionStates(ctx, transaction, execution)
	if err != nil {
		return biz.AcceptReceipt{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return biz.AcceptReceipt{}, biz.ErrPersistence
	}
	return biz.AcceptReceipt{Execution: execution, Replayed: replayed}, nil
}

func sameAdmission(row executionsql.ModeldevExecution, command executionsql.InsertExecutionParams) bool {
	return row.TenantID == command.TenantID &&
		row.ExecutionID == command.ExecutionID &&
		row.OperationID == command.OperationID &&
		row.Actor == command.Actor &&
		row.IntentHash == command.IntentHash &&
		row.SpecHash == command.SpecHash &&
		bytes.Equal(row.IntentCanonical, command.IntentCanonical) &&
		bytes.Equal(row.SnapshotCanonical, command.SnapshotCanonical) &&
		row.AcceptedAt.Valid && row.AcceptedAt.InfinityModifier == pgtype.Finite &&
		row.AcceptedAt.Time.Equal(command.AcceptedAt.Time)
}

func (r *Repository) Get(ctx context.Context, tenant, execution string) (biz.Execution, error) {
	tenantID, err := databaseID(tenant)
	if err != nil {
		return biz.Execution{}, err
	}
	executionID, err := databaseID(execution)
	if err != nil {
		return biz.Execution{}, err
	}
	// Admission, latest close and aggregate revision must describe one
	// committed snapshot, even while a writer advances the shared identity.
	transaction, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return biz.Execution{}, biz.ErrPersistence
	}
	defer rollbackExecutionTransaction(transaction)
	result, err := readExecution(ctx, transaction, tenantID, executionID)
	if err != nil {
		return biz.Execution{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return biz.Execution{}, biz.ErrPersistence
	}
	return result, nil
}

// readExecution keeps all aggregate facts within its caller's transaction.
func readExecution(ctx context.Context, transaction pgx.Tx, tenantID, executionID pgtype.UUID) (biz.Execution, error) {
	queries := executionsql.New(transaction)
	row, err := queries.GetExecution(ctx, executionsql.GetExecutionParams{
		TenantID: tenantID, ExecutionID: executionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.Execution{}, biz.ErrExecutionNotFound
	}
	if err != nil {
		return biz.Execution{}, biz.ErrPersistence
	}
	// This read reports durable facts, not permission to create resources.
	// A creator must check and persist its intent under the shared identity lock.
	result, err := executionWithClose(ctx, queries, row)
	if err != nil {
		return biz.Execution{}, err
	}
	result.OwnerRevision, err = currentOwnerRevision(ctx, queries, tenantID, executionID)
	if err != nil {
		return biz.Execution{}, err
	}
	result.States, err = executionStates(ctx, transaction, result)
	if err != nil {
		return biz.Execution{}, err
	}
	return result, nil
}

func executionWithClose(ctx context.Context, queries *executionsql.Queries, row executionsql.ModeldevExecution) (biz.Execution, error) {
	execution, err := executionFromRow(row)
	if err != nil {
		return biz.Execution{}, err
	}
	closeRow, err := queries.GetCloseIntent(ctx, executionsql.GetCloseIntentParams{
		TenantID: row.TenantID, ExecutionID: row.ExecutionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return execution, nil
	}
	if err != nil {
		return biz.Execution{}, biz.ErrPersistence
	}
	closeRecord, err := closeRecordFromRow(closeRow)
	if err != nil {
		return biz.Execution{}, err
	}
	if closeRecord.TenantID != execution.TenantID || closeRecord.OperationID != execution.OperationID || closeRecord.ExecutionID != execution.ExecutionID || closeRecord.SpecHash != execution.SpecHash {
		return biz.Execution{}, biz.ErrPersistence
	}
	execution.Close = &closeRecord
	return execution, nil
}

func databaseID(value string) (pgtype.UUID, error) {
	// pgtype.UUID.Scan accepts arbitrary characters at the separator positions.
	// Enforce the public standard UUID shape before the driver decodes its hex.
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return pgtype.UUID{}, biz.ErrInvalidAdmission
	}
	var id pgtype.UUID
	if id.Scan(value) != nil || !id.Valid || id.Bytes == [16]byte{} {
		return pgtype.UUID{}, biz.ErrInvalidAdmission
	}
	return id, nil
}

func executionFromRow(row executionsql.ModeldevExecution) (biz.Execution, error) {
	if !row.TenantID.Valid || !row.ExecutionID.Valid || !row.OperationID.Valid || !row.AcceptedAt.Valid || row.AcceptedAt.InfinityModifier != pgtype.Finite {
		return biz.Execution{}, biz.ErrPersistence
	}
	execution := biz.Execution{Admission: biz.Admission{
		TenantID: row.TenantID.String(), Actor: row.Actor,
		OperationID: row.OperationID.String(), ExecutionID: row.ExecutionID.String(),
		IntentHash: row.IntentHash, SpecHash: row.SpecHash,
		AcceptedAt: row.AcceptedAt.Time.UTC(),
	}}
	// CanonicalIntent's schema field is checked by canonical byte comparison
	// below. Unknown or noncanonical stored fields cannot silently disappear.
	var intentErr, snapshotErr error
	execution.Intent, intentErr = cpup01.DecodeCanonicalIntent(row.IntentCanonical)
	execution.Snapshot, snapshotErr = cpup01.DecodeCanonicalSnapshot(row.SnapshotCanonical)
	if intentErr != nil || snapshotErr != nil {
		return biz.Execution{}, biz.ErrPersistence
	}
	intent, snapshot, err := execution.Admission.CanonicalPayloads()
	if err != nil || !bytes.Equal(intent, row.IntentCanonical) || !bytes.Equal(snapshot, row.SnapshotCanonical) {
		return biz.Execution{}, biz.ErrPersistence
	}
	return execution, nil
}

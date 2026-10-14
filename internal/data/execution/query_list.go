package execution

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	executionsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution/sqlc"
)

func (r *Repository) ListQueryRecords(ctx context.Context, tenant string, filter biz.ExecutionQuery) ([]biz.QueryRecord, string, error) {
	tenantID, err := databaseID(tenant)
	if err != nil {
		return nil, "", err
	}
	cursor := pgtype.UUID{Valid: true}
	if filter.AfterID != "" {
		cursor, err = databaseID(filter.AfterID)
		if err != nil {
			return nil, "", err
		}
	}
	if ctx == nil || r == nil || r.pool == nil || filter.Limit < 1 || filter.Limit > 100 {
		return nil, "", biz.ErrPersistence
	}
	transaction, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, "", biz.ErrPersistence
	}
	defer rollbackExecutionTransaction(transaction)
	queries := executionsql.New(transaction)
	var result []biz.QueryRecord
	next := ""
	for {
		ids, err := queries.ListTenantExecutionIDs(ctx, executionsql.ListTenantExecutionIDsParams{TenantID: tenantID, AfterExecutionID: cursor})
		if err != nil {
			return nil, "", biz.ErrPersistence
		}
		for _, id := range ids {
			cursor = id
			record, err := queryRecord(ctx, transaction, tenantID, id)
			if err != nil {
				return nil, "", err
			}
			if !filter.Matches(record.Execution.States) {
				continue
			}
			if len(result) == filter.Limit {
				next = result[len(result)-1].Execution.ExecutionID
				break
			}
			result = append(result, record)
		}
		if next != "" || len(ids) < 128 {
			break
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return nil, "", biz.ErrPersistence
	}
	return result, next, nil
}

// CountQueryRecords returns the total number of executions matching the same
// state filters, independent of the pagination window. The state projection
// spans several tables and is not expressible as a plain SQL COUNT, so the same
// validated batch scan used to fill a page is reused to count every match.
func (r *Repository) CountQueryRecords(ctx context.Context, tenant string, filter biz.ExecutionQuery) (uint64, error) {
	tenantID, err := databaseID(tenant)
	if err != nil {
		return 0, err
	}
	if ctx == nil || r == nil || r.pool == nil {
		return 0, biz.ErrPersistence
	}
	transaction, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return 0, biz.ErrPersistence
	}
	defer rollbackExecutionTransaction(transaction)
	queries := executionsql.New(transaction)
	cursor := pgtype.UUID{Valid: true}
	var total uint64
	for {
		ids, err := queries.ListTenantExecutionIDs(ctx, executionsql.ListTenantExecutionIDsParams{TenantID: tenantID, AfterExecutionID: cursor})
		if err != nil {
			return 0, biz.ErrPersistence
		}
		for _, id := range ids {
			cursor = id
			record, err := queryRecord(ctx, transaction, tenantID, id)
			if err != nil {
				return 0, err
			}
			if filter.Matches(record.Execution.States) {
				total++
			}
		}
		if len(ids) < 128 {
			break
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return 0, biz.ErrPersistence
	}
	return total, nil
}

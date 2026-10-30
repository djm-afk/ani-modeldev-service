package execution

import (
	"errors"
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	executionsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution/sqlc"
)

// executionCursor is the position of the last row already consumed by the
// scan. Both halves are needed: acceptance time is the list order and
// execution_id breaks ties between rows admitted in the same microsecond.
type executionCursor struct {
	acceptedAt  pgtype.Timestamptz
	executionID pgtype.UUID
}

// next returns the cursor positioned after one scanned row, and whether the
// row carried a usable acceptance time. A row without one would silently
// restart the walk, so the caller treats it as a persistence failure.
func (c executionCursor) next(row executionsql.ListTenantExecutionIDsRow) (executionCursor, bool) {
	if !row.AcceptedAt.Valid || row.AcceptedAt.InfinityModifier != pgtype.Finite {
		return c, false
	}
	return executionCursor{acceptedAt: row.AcceptedAt, executionID: row.ExecutionID}, true
}

// ListQueryRecords returns one page window of the tenant's executions, newest
// admission first. The filters are applied through the same validated
// aggregate projection as GetExecution before the offset window is taken.
func (r *Repository) ListQueryRecords(ctx context.Context, tenant string, filter biz.ExecutionQuery) ([]biz.QueryRecord, error) {
	tenantID, err := databaseID(tenant)
	if err != nil {
		return nil, err
	}
	if ctx == nil || r == nil || r.pool == nil || filter.Limit < 1 || filter.Limit > 100 || filter.Offset < 0 {
		return nil, biz.ErrPersistence
	}
	transaction, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, biz.ErrPersistence
	}
	defer rollbackExecutionTransaction(transaction)
	queries := executionsql.New(transaction)
	cursor := executionCursor{}
	var result []biz.QueryRecord
	matched, full := 0, false
	for {
		rows, err := queries.ListTenantExecutionIDs(ctx, executionsql.ListTenantExecutionIDsParams{
			TenantID: tenantID, AfterAcceptedAt: cursor.acceptedAt, AfterExecutionID: cursor.executionID,
		})
		if err != nil {
			return nil, biz.ErrPersistence
		}
		for _, row := range rows {
			next, ok := cursor.next(row)
			if !ok {
				return nil, biz.ErrPersistence
			}
			cursor = next
			record, err := queryRecord(ctx, transaction, tenantID, row.ExecutionID)
			if errors.Is(err, ErrUndecodableExecution) {
				// Legacy history stays visible to nobody but remains untouched.
				continue
			}
			if err != nil {
				return nil, err
			}
			if !filter.Matches(record.Execution) {
				continue
			}
			if matched < filter.Offset {
				matched++
				continue
			}
			if len(result) == filter.Limit {
				full = true
				break
			}
			result = append(result, record)
			matched++
		}
		if full || len(rows) < 128 {
			break
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return nil, biz.ErrPersistence
	}
	return result, nil
}

// CountQueryRecords returns the total number of executions matching the same
// filters, independent of the pagination window. The projection spans several
// tables and is not expressible as a plain SQL COUNT, so the same validated
// batch scan used to fill a page is reused to count every match.
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
	cursor := executionCursor{}
	var total uint64
	for {
		rows, err := queries.ListTenantExecutionIDs(ctx, executionsql.ListTenantExecutionIDsParams{
			TenantID: tenantID, AfterAcceptedAt: cursor.acceptedAt, AfterExecutionID: cursor.executionID,
		})
		if err != nil {
			return 0, biz.ErrPersistence
		}
		for _, row := range rows {
			next, ok := cursor.next(row)
			if !ok {
				return 0, biz.ErrPersistence
			}
			cursor = next
			record, err := queryRecord(ctx, transaction, tenantID, row.ExecutionID)
			if errors.Is(err, ErrUndecodableExecution) {
				continue
			}
			if err != nil {
				return 0, err
			}
			if filter.Matches(record.Execution) {
				total++
			}
		}
		if len(rows) < 128 {
			break
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return 0, biz.ErrPersistence
	}
	return total, nil
}

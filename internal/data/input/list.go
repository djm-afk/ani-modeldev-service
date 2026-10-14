package input

import (
	"context"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	inputsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input/sqlc"
)

func (r *Repository) List(ctx context.Context, tenant string, state biz.InputState, after string, limit int) ([]biz.InputVersion, string, error) {
	tenantID, err := databaseID(tenant)
	if err != nil {
		return nil, "", err
	}
	var afterID pgtype.UUID
	if after != "" {
		afterID, err = databaseID(after)
		if err != nil {
			return nil, "", err
		}
	}
	if limit < 1 || limit > 100 || (state != "" && state != biz.InputStateValidating && state != biz.InputStateReady && state != biz.InputStateRejected) {
		return nil, "", biz.ErrInvalidInput
	}
	if r == nil || r.pool == nil {
		return nil, "", biz.ErrPersistence
	}
	rows, err := inputsql.New(r.pool).ListInputVersions(ctx, inputsql.ListInputVersionsParams{TenantID: tenantID, StateFilter: string(state), AfterID: afterID, RowLimit: int32(limit + 1)})
	if err != nil {
		return nil, "", biz.ErrPersistence
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[len(rows)-1].InputVersionID.String()
	}
	out := make([]biz.InputVersion, 0, len(rows))
	for _, row := range rows {
		value, err := versionFromRow(row)
		if err != nil {
			return nil, "", err
		}
		out = append(out, value)
	}
	return out, next, nil
}

func (r *Repository) Count(ctx context.Context, tenant string, state biz.InputState) (uint64, error) {
	tenantID, err := databaseID(tenant)
	if err != nil {
		return 0, err
	}
	if state != "" && state != biz.InputStateValidating && state != biz.InputStateReady && state != biz.InputStateRejected {
		return 0, biz.ErrInvalidInput
	}
	if r == nil || r.pool == nil {
		return 0, biz.ErrPersistence
	}
	total, err := inputsql.New(r.pool).CountInputVersions(ctx, inputsql.CountInputVersionsParams{TenantID: tenantID, StateFilter: string(state)})
	if err != nil {
		return 0, biz.ErrPersistence
	}
	return uint64(total), nil
}

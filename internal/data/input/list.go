package input

import (
	"context"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	inputsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input/sqlc"
)

// List returns one page window of a tenant's input versions in input_version_id
// order. The optional state filter is applied inside the same snapshot as Count.
func (r *Repository) List(ctx context.Context, tenant string, state biz.InputState, offset, limit int) ([]biz.InputVersion, error) {
	tenantID, err := databaseID(tenant)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 || offset < 0 || (state != "" && state != biz.InputStateValidating && state != biz.InputStateReady && state != biz.InputStateRejected) {
		return nil, biz.ErrInvalidInput
	}
	if r == nil || r.pool == nil {
		return nil, biz.ErrPersistence
	}
	rows, err := inputsql.New(r.pool).ListInputVersions(ctx, inputsql.ListInputVersionsParams{TenantID: tenantID, StateFilter: string(state), RowLimit: int32(limit), RowOffset: int32(offset)})
	if err != nil {
		return nil, biz.ErrPersistence
	}
	out := make([]biz.InputVersion, 0, len(rows))
	for _, row := range rows {
		value, err := versionFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
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

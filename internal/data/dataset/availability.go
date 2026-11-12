package dataset

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	datasetsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/dataset/sqlc"
)

// CheckDatasetAvailable reports whether the dataset that owns an input version
// is still ACTIVE.
//
// A version that belongs to no dataset is a legacy CSV input: the dataset
// lifecycle does not govern it, so it stays available. That distinction is why
// this reads the join rather than the dataset row alone.
func (r *Repository) CheckDatasetAvailable(ctx context.Context, tenantID, inputVersionID string) error {
	if r == nil || r.pool == nil {
		return biz.ErrPersistence
	}
	tenant, err := databaseID(tenantID)
	if err != nil {
		return err
	}
	version, err := databaseID(inputVersionID)
	if err != nil {
		return err
	}
	state, err := datasetsql.New(r.pool).DatasetStateForInputVersion(ctx, datasetsql.DatasetStateForInputVersionParams{
		TenantID: tenant, InputVersionID: version,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// No dataset owns this version: a legacy input, not an archived one.
		return nil
	}
	if err != nil {
		return biz.ErrPersistence
	}
	if state != string(biz.DatasetActive) {
		return biz.ErrDatasetArchived
	}
	return nil
}

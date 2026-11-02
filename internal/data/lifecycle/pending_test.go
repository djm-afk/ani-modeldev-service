package lifecycle_test

import (
	"context"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
)

func testPendingCloseIDs(ctx context.Context, r *lifecycle.Repository, binding admissionfixture.DispatchBinding) ([]string, error) {
	before, err := r.CloseDiscoveryTime(ctx)
	if err != nil {
		return nil, err
	}
	page, err := r.ListPendingClosures(ctx, binding.Environment.ClusterID, binding.TenantID, biz.DiscoveryCursor{SweepBefore: before}, 4)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, row := range page.Rows {
		if row.DecodeProblem != "" {
			return nil, biz.ErrPersistence
		}
		if row.Reasons != 0 {
			ids = append(ids, row.ExecutionID)
		}
	}
	return ids, nil
}

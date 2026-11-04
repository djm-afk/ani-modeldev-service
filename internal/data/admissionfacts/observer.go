package admissionfacts

import (
	"context"
	"errors"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/dataset"
)

// EnvironmentRegistry is the read-only registry surface the dataset target
// reader needs. It is deliberately smaller than ReadReadyBinding: a dataset
// import must not require a READY training environment.
type EnvironmentRegistry interface {
	Get(ctx context.Context, tenant, cluster string) (biz.TenantExecutionEnvironment, error)
}

// Observer adapts the tenant environment registry to the dataset observation
// port. It answers three facts only: is there a row, what is its pinned
// Namespace UID, and is it currently held for review.
type Observer struct {
	registry EnvironmentRegistry
}

func NewObserver(registry EnvironmentRegistry) (*Observer, error) {
	if registry == nil {
		return nil, errors.New("INVALID_MANAGED_ADMISSION_FACTS")
	}
	return &Observer{registry: registry}, nil
}

var _ dataset.EnvironmentObserver = (*Observer)(nil)

func (observer *Observer) ObserveEnvironment(ctx context.Context, tenantID, clusterID string) (dataset.EnvironmentObservation, error) {
	if err := ctx.Err(); err != nil {
		return dataset.EnvironmentObservation{}, err
	}
	row, err := observer.registry.Get(ctx, tenantID, clusterID)
	if errors.Is(err, biz.ErrTenantEnvironmentNotRegistered) {
		// A missing row is normal: it neither grants nor revokes anything.
		return dataset.EnvironmentObservation{Present: false}, nil
	}
	if err != nil {
		// A read failure must not be confused with "no row".
		return dataset.EnvironmentObservation{}, dataset.ErrRegistryObservationFailed
	}
	observation := dataset.EnvironmentObservation{
		Present:      true,
		State:        string(row.State),
		NamespaceUID: row.NamespaceUID,
	}
	// NEEDS_REVIEW holds new work; the reason stays with the environment and is
	// never cleared or restated by the dataset path.
	if row.State == biz.EnvironmentNeedsReview {
		observation.Blocked = true
		observation.BlockReason = row.ReasonCode
	}
	return observation, nil
}

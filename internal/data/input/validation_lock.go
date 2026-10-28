package input

import (
	"context"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// AcquireValidationLock owns a session, never a long database transaction.
// A lost session releases its lock automatically; a failed explicit unlock
// closes the session so a pooled connection cannot leak validation authority.
func (repository *Repository) AcquireValidationLock(ctx context.Context, tenant, input string) (func(), error) {
	if repository == nil || repository.pool == nil {
		return nil, biz.ErrPersistence
	}
	connection, err := repository.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	key := "ani.modeldev.input-validation.v3:" + tenant + ":" + input
	var locked bool
	if err := connection.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,0))", key).Scan(&locked); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = connection.Hijack().Close(cleanup)
		return nil, err
	}
	if !locked {
		connection.Release()
		return nil, biz.ErrInputValidationBusy
	}
	return func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var unlocked bool
		if err := connection.QueryRow(cleanup, "SELECT pg_advisory_unlock(hashtextextended($1,0))", key).Scan(&unlocked); err != nil || !unlocked {
			_ = connection.Hijack().Close(cleanup)
			return
		}
		connection.Release()
	}, nil
}

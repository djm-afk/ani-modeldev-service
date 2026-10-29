package lifecycle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestCurrentRuntimeMissingTableIsPersistenceFailure(t *testing.T) {
	pool := postgres.Prepare(t)()
	defer pool.Close()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO pg_catalog"); err != nil {
		t.Fatal(err)
	}
	_, err = lifecycle.ReadInTransaction(ctx, tx, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")
	if !errors.Is(err, biz.ErrPersistence) || errors.Is(err, biz.ErrExecutionNotFound) {
		t.Fatalf("missing current table was hidden as absent execution: %v", err)
	}
}

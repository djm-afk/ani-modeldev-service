package execution

import (
	"errors"
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	executionsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution/sqlc"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
)

var _ biz.ArtifactQueryRepository = (*Repository)(nil)

func (r *Repository) GetQueryRecord(ctx context.Context, tenant, execution string) (biz.QueryRecord, error) {
	tenantID, err := databaseID(tenant)
	if err != nil {
		return biz.QueryRecord{}, err
	}
	executionID, err := databaseID(execution)
	if err != nil {
		return biz.QueryRecord{}, err
	}
	if ctx == nil || r == nil || r.pool == nil {
		return biz.QueryRecord{}, biz.ErrPersistence
	}
	transaction, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return biz.QueryRecord{}, biz.ErrPersistence
	}
	defer rollbackExecutionTransaction(transaction)
	result, err := queryRecord(ctx, transaction, tenantID, executionID)
	if errors.Is(err, ErrUndecodableExecution) {
		// A retired payload is not a readable execution; it is also not a
		// server failure the caller can retry.
		return biz.QueryRecord{}, biz.ErrExecutionNotFound
	}
	if err != nil {
		return biz.QueryRecord{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return biz.QueryRecord{}, biz.ErrPersistence
	}
	return result, nil
}

// FindPublishedArtifact accepts a durable artifact identity, never an object key
// or endpoint. The lookup and complete publication validation share one snapshot.
func (r *Repository) FindPublishedArtifact(ctx context.Context, tenant, artifact string) (biz.QueryRecord, biz.PublishedRuntimeFile, error) {
	tenantID, err := databaseID(tenant)
	if err != nil {
		return biz.QueryRecord{}, biz.PublishedRuntimeFile{}, biz.ErrArtifactNotFound
	}
	artifactID, err := databaseID(artifact)
	if err != nil {
		return biz.QueryRecord{}, biz.PublishedRuntimeFile{}, biz.ErrArtifactNotFound
	}
	if ctx == nil || r == nil || r.pool == nil {
		return biz.QueryRecord{}, biz.PublishedRuntimeFile{}, biz.ErrPersistence
	}
	transaction, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return biz.QueryRecord{}, biz.PublishedRuntimeFile{}, biz.ErrPersistence
	}
	defer rollbackExecutionTransaction(transaction)
	candidates, err := executionsql.New(transaction).FindPublishedArtifactExecution(ctx, executionsql.FindPublishedArtifactExecutionParams{TenantID: tenantID, ArtifactID: artifactID.String()})
	if err != nil {
		return biz.QueryRecord{}, biz.PublishedRuntimeFile{}, biz.ErrPersistence
	}
	if len(candidates) == 0 {
		return biz.QueryRecord{}, biz.PublishedRuntimeFile{}, biz.ErrArtifactNotFound
	}
	if len(candidates) != 1 {
		return biz.QueryRecord{}, biz.PublishedRuntimeFile{}, biz.ErrPersistence
	}
	record, err := queryRecord(ctx, transaction, tenantID, candidates[0])
	if err != nil {
		return biz.QueryRecord{}, biz.PublishedRuntimeFile{}, err
	}
	if record.Runtime.Publication == nil || record.Execution.States.Delivery != biz.DeliveryStatePublished {
		return biz.QueryRecord{}, biz.PublishedRuntimeFile{}, biz.ErrArtifactNotFound
	}
	for _, file := range record.Runtime.Publication.Files {
		if file.ArtifactID == artifactID.String() {
			if err := transaction.Commit(ctx); err != nil {
				return biz.QueryRecord{}, biz.PublishedRuntimeFile{}, biz.ErrPersistence
			}
			return record, file, nil
		}
	}
	return biz.QueryRecord{}, biz.PublishedRuntimeFile{}, biz.ErrPersistence
}

func queryRecord(ctx context.Context, transaction pgx.Tx, tenantID, executionID pgtype.UUID) (biz.QueryRecord, error) {
	execution, err := readExecution(ctx, transaction, tenantID, executionID)
	if err != nil {
		return biz.QueryRecord{}, err
	}
	runtime, err := lifecycle.ReadInTransaction(ctx, transaction, tenantID.String(), executionID.String())
	if err != nil || runtime.OwnerRevision != execution.OwnerRevision {
		return biz.QueryRecord{}, biz.ErrPersistence
	}
	return biz.QueryRecord{Execution: execution, Runtime: runtime}, nil
}

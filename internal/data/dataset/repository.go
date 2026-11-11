// Package dataset persists dataset collections, command receipts and the
// immutable operation history. It does not authorize callers, reach external
// hubs, or decide import policy.
package dataset

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	datasetsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/dataset/sqlc"
)

// InputPublisher is the input package's transaction-aware publish entry. It is
// declared here as a narrow port so the dataset package never imports the input
// package's SQL, only its one guarded write.
type InputPublisher interface {
	PublishVerifiedAlpacaInTx(ctx context.Context, tx pgx.Tx, expected biz.InputImport, verified biz.VerifiedAlpaca) (biz.InputVersion, error)
	// FreezeImport writes the immutable frozen input row. The publication
	// transaction locks that row, so it has to exist first.
	FreezeImport(ctx context.Context, request biz.InputImport) (biz.InputVersion, error)
}

type Repository struct {
	pool   *pgxpool.Pool
	inputs InputPublisher
	// limit is the global running-attempt budget. It is a setting of the whole
	// deployment, not a per-replica goroutine count, and it only takes effect
	// inside AllocateAttempt's capacity transaction.
	limit int
}

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// WithConcurrencyLimit sets the shared import budget. A non-positive value
// leaves allocation failing closed.
func (r *Repository) WithConcurrencyLimit(limit int) *Repository {
	if r != nil {
		r.limit = limit
	}
	return r
}

// WithInputPublisher attaches the input writer used by publication. Without it
// publication fails closed rather than writing a version nobody verified.
func (r *Repository) WithInputPublisher(inputs InputPublisher) *Repository {
	if r != nil {
		r.inputs = inputs
	}
	return r
}

var (
	_ biz.DatasetRepository          = (*Repository)(nil)
	_ biz.DatasetCommandRepository   = (*Repository)(nil)
	_ biz.DatasetOperationRepository = (*Repository)(nil)
)

func databaseID(value string) (pgtype.UUID, error) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return pgtype.UUID{}, biz.ErrDatasetInvalidMetadata
	}
	var id pgtype.UUID
	if id.Scan(value) != nil || !id.Valid || id.Bytes == [16]byte{} {
		return pgtype.UUID{}, biz.ErrDatasetInvalidMetadata
	}
	return id, nil
}

func datasetFromRow(row datasetsql.ModeldevDataset) (biz.Dataset, error) {
	if !row.TenantID.Valid || !row.DatasetID.Valid || !row.CreatedAt.Valid || !row.UpdatedAt.Valid || row.CreatedAt.InfinityModifier != pgtype.Finite || row.UpdatedAt.InfinityModifier != pgtype.Finite {
		return biz.Dataset{}, biz.ErrDatasetInvalidMetadata
	}
	value := biz.Dataset{
		TenantID: row.TenantID.String(), DatasetID: row.DatasetID.String(),
		Name: row.Name, Description: row.Description, State: biz.DatasetState(row.State),
		Revision: row.Revision, NextVersionNumber: row.NextVersionNumber,
		CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy,
		CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(),
	}
	if row.ArchivedAt.Valid {
		archived := row.ArchivedAt.Time.UTC()
		value.ArchivedAt = &archived
	}
	return value, nil
}

func (r *Repository) CreateDataset(ctx context.Context, value biz.Dataset) (biz.Dataset, error) {
	if r == nil || r.pool == nil {
		return biz.Dataset{}, biz.ErrPersistence
	}
	tenant, err := databaseID(value.TenantID)
	if err != nil {
		return biz.Dataset{}, err
	}
	datasetID, err := databaseID(value.DatasetID)
	if err != nil {
		return biz.Dataset{}, err
	}
	row, err := datasetsql.New(r.pool).InsertDataset(ctx, datasetsql.InsertDatasetParams{
		TenantID: tenant, DatasetID: datasetID, Name: value.Name,
		Description: value.Description, CreatedBy: value.CreatedBy, UpdatedBy: value.UpdatedBy,
	})
	// ON CONFLICT DO NOTHING covers both the primary key and the unique name;
	// distinguish them by reading the winner so the caller gets the right reason.
	if errors.Is(err, pgx.ErrNoRows) {
		existing, lookupErr := datasetsql.New(r.pool).DatasetNameTaken(ctx, datasetsql.DatasetNameTakenParams{TenantID: tenant, Name: value.Name})
		if lookupErr == nil && existing != "" {
			return biz.Dataset{}, biz.ErrDatasetNameConflict
		}
		return biz.Dataset{}, biz.ErrDatasetInvalidMetadata
	}
	if err != nil {
		return biz.Dataset{}, biz.ErrPersistence
	}
	return datasetFromRow(row)
}

func (r *Repository) GetDataset(ctx context.Context, tenantID, datasetID string) (biz.Dataset, error) {
	if r == nil || r.pool == nil {
		return biz.Dataset{}, biz.ErrPersistence
	}
	tenant, err := databaseID(tenantID)
	if err != nil {
		return biz.Dataset{}, err
	}
	id, err := databaseID(datasetID)
	if err != nil {
		return biz.Dataset{}, err
	}
	row, err := datasetsql.New(r.pool).GetDataset(ctx, datasetsql.GetDatasetParams{TenantID: tenant, DatasetID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.Dataset{}, biz.ErrDatasetNotFound
	}
	if err != nil {
		return biz.Dataset{}, biz.ErrPersistence
	}
	value, err := datasetFromRow(row)
	if err != nil {
		return biz.Dataset{}, err
	}
	if err := r.attachDatasetFacts(ctx, tenant, []biz.Dataset{value}, func(index int, stats datasetStats, latest *biz.DatasetImportTask) {
		value.ReadyVersionCount = stats.readyVersionCount
		value.LatestReadyVersionNumber = stats.latestReadyVersionNumber
		value.LatestImport = latest
	}); err != nil {
		return biz.Dataset{}, err
	}
	return value, nil
}

func (r *Repository) ListDatasets(ctx context.Context, filter biz.DatasetListFilter) (biz.DatasetPage, error) {
	if r == nil || r.pool == nil {
		return biz.DatasetPage{}, biz.ErrPersistence
	}
	tenant, err := databaseID(filter.TenantID)
	if err != nil {
		return biz.DatasetPage{}, err
	}
	pageSize, offset := normalizePage(filter.Page, filter.PageSize)
	params := datasetsql.ListDatasetsParams{TenantID: tenant, State: filter.State, NameFilter: filter.NameFilter, PageSize: pageSize, PageOffset: offset}
	rows, err := datasetsql.New(r.pool).ListDatasets(ctx, params)
	if err != nil {
		return biz.DatasetPage{}, biz.ErrPersistence
	}
	total, err := datasetsql.New(r.pool).CountDatasets(ctx, datasetsql.CountDatasetsParams{TenantID: tenant, State: filter.State, NameFilter: filter.NameFilter})
	if err != nil {
		return biz.DatasetPage{}, biz.ErrPersistence
	}
	pageValue := biz.DatasetPage{Total: total, Items: make([]biz.Dataset, 0, len(rows))}
	for _, row := range rows {
		item, err := datasetFromRow(row)
		if err != nil {
			return biz.DatasetPage{}, err
		}
		pageValue.Items = append(pageValue.Items, item)
	}
	if err := r.attachDatasetFacts(ctx, tenant, pageValue.Items, func(index int, stats datasetStats, latest *biz.DatasetImportTask) {
		pageValue.Items[index].ReadyVersionCount = stats.readyVersionCount
		pageValue.Items[index].LatestReadyVersionNumber = stats.latestReadyVersionNumber
		pageValue.Items[index].LatestImport = latest
	}); err != nil {
		return biz.DatasetPage{}, err
	}
	return pageValue, nil
}

// datasetStats is the published-version summary of one collection.
type datasetStats struct {
	readyVersionCount        int64
	latestReadyVersionNumber int64
}

// attachDatasetFacts fills the read-only summary fields for one page of
// collections in two bounded queries. Both are keyed by the page's own ids, so
// the cost does not grow with the tenant's history.
func (r *Repository) attachDatasetFacts(ctx context.Context, tenant pgtype.UUID, items []biz.Dataset, apply func(int, datasetStats, *biz.DatasetImportTask)) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]pgtype.UUID, 0, len(items))
	for _, item := range items {
		id, err := databaseID(item.DatasetID)
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	queries := datasetsql.New(r.pool)
	stats := make(map[string]datasetStats, len(items))
	rows, err := queries.DatasetVersionStats(ctx, datasetsql.DatasetVersionStatsParams{TenantID: tenant, DatasetIds: ids})
	if err != nil {
		return biz.ErrPersistence
	}
	for _, row := range rows {
		stats[row.DatasetID.String()] = datasetStats{readyVersionCount: row.ReadyVersionCount, latestReadyVersionNumber: row.LatestReadyVersionNumber}
	}
	latest := make(map[string]*biz.DatasetImportTask, len(items))
	tasks, err := queries.LatestImportTasks(ctx, datasetsql.LatestImportTasksParams{TenantID: tenant, DatasetIds: ids})
	if err != nil {
		return biz.ErrPersistence
	}
	latestItems := make([]biz.DatasetImportTask, 0, len(tasks))
	for _, row := range tasks {
		task, err := taskFromRow(row)
		if err != nil {
			return err
		}
		latestItems = append(latestItems, task)
	}
	if err := r.attachAttemptCounts(ctx, tenant, latestItems, func(index int, count int64) {
		latestItems[index].AttemptCount = count
	}); err != nil {
		return err
	}
	for index := range latestItems {
		task := latestItems[index]
		latest[task.DatasetID] = &task
	}
	for position, item := range items {
		apply(position, stats[item.DatasetID], latest[item.DatasetID])
	}
	return nil
}

func (r *Repository) UpdateDatasetMetadata(ctx context.Context, update biz.DatasetMetadataUpdate) (biz.Dataset, error) {
	if r == nil || r.pool == nil {
		return biz.Dataset{}, biz.ErrPersistence
	}
	tenant, datasetID, err := tenantAndDataset(update.TenantID, update.DatasetID)
	if err != nil {
		return biz.Dataset{}, err
	}
	row, err := datasetsql.New(r.pool).UpdateDatasetMetadata(ctx, datasetsql.UpdateDatasetMetadataParams{
		TenantID: tenant, DatasetID: datasetID, Name: update.Name, Description: update.Description,
		UpdatedBy: update.ActorID, ExpectedRevision: update.ExpectedRevision,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.Dataset{}, biz.ErrRevisionConflict
	}
	if err != nil {
		return biz.Dataset{}, biz.ErrPersistence
	}
	return datasetFromRow(row)
}

func (r *Repository) ArchiveDataset(ctx context.Context, change biz.DatasetStateChange) (biz.Dataset, error) {
	return r.transitionState(ctx, change, true)
}

func (r *Repository) RestoreDataset(ctx context.Context, change biz.DatasetStateChange) (biz.Dataset, error) {
	return r.transitionState(ctx, change, false)
}

func (r *Repository) transitionState(ctx context.Context, change biz.DatasetStateChange, archive bool) (biz.Dataset, error) {
	if r == nil || r.pool == nil {
		return biz.Dataset{}, biz.ErrPersistence
	}
	tenant, datasetID, err := tenantAndDataset(change.TenantID, change.DatasetID)
	if err != nil {
		return biz.Dataset{}, err
	}
	queries := datasetsql.New(r.pool)
	var row datasetsql.ModeldevDataset
	if archive {
		row, err = queries.ArchiveDataset(ctx, datasetsql.ArchiveDatasetParams{TenantID: tenant, DatasetID: datasetID, UpdatedBy: change.ActorID, ExpectedRevision: change.ExpectedRevision})
	} else {
		row, err = queries.RestoreDataset(ctx, datasetsql.RestoreDatasetParams{TenantID: tenant, DatasetID: datasetID, UpdatedBy: change.ActorID, ExpectedRevision: change.ExpectedRevision})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// Either the revision moved or the lifecycle is already in that state.
		// The caller distinguishes by re-reading, so report the generic conflict.
		return biz.Dataset{}, biz.ErrRevisionConflict
	}
	if err != nil {
		return biz.Dataset{}, biz.ErrPersistence
	}
	return datasetFromRow(row)
}

func (r *Repository) FindReceipt(ctx context.Context, tenantID, actorID string, kind biz.OperationKind, key string) (biz.CommandReceipt, error) {
	if r == nil || r.pool == nil {
		return biz.CommandReceipt{}, biz.ErrPersistence
	}
	tenant, err := databaseID(tenantID)
	if err != nil {
		return biz.CommandReceipt{}, err
	}
	row, err := datasetsql.New(r.pool).GetCommandReceipt(ctx, datasetsql.GetCommandReceiptParams{
		TenantID: tenant, ActorID: actorID, OperationKind: string(kind), IdempotencyKey: key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.CommandReceipt{}, biz.ErrDatasetNotFound
	}
	if err != nil {
		return biz.CommandReceipt{}, biz.ErrPersistence
	}
	return receiptFromRow(row)
}

func (r *Repository) InsertReceipt(ctx context.Context, receipt biz.CommandReceipt) (biz.CommandReceipt, error) {
	if r == nil || r.pool == nil {
		return biz.CommandReceipt{}, biz.ErrPersistence
	}
	tenant, err := databaseID(receipt.TenantID)
	if err != nil {
		return biz.CommandReceipt{}, err
	}
	receiptID, err := databaseID(receipt.ReceiptID)
	if err != nil {
		return biz.CommandReceipt{}, err
	}
	datasetID, datasetErr := databaseID(receipt.DatasetID)
	if datasetErr != nil {
		return biz.CommandReceipt{}, datasetErr
	}
	var taskID pgtype.UUID
	if receipt.TaskID != "" {
		parsed, err := databaseID(receipt.TaskID)
		if err != nil {
			return biz.CommandReceipt{}, err
		}
		taskID = parsed
	}
	body, err := json.Marshal(map[string]string{"receipt_id": receipt.ReceiptID, "operation": string(receipt.Kind), "outcome": receipt.Outcome})
	if err != nil {
		return biz.CommandReceipt{}, biz.ErrPersistence
	}
	canonical, err := json.Marshal(map[string]string{"request_sha256": receipt.RequestSHA256})
	if err != nil {
		return biz.CommandReceipt{}, biz.ErrPersistence
	}
	row, err := datasetsql.New(r.pool).InsertCommandReceipt(ctx, datasetsql.InsertCommandReceiptParams{
		TenantID: tenant, ReceiptID: receiptID, ActorID: receipt.ActorID,
		OperationKind: string(receipt.Kind), IdempotencyKey: receipt.IdempotencyKey,
		RequestSha256: receipt.RequestSHA256, CanonicalRequest: canonical,
		DatasetID: datasetID, TaskID: taskID, ResponseStatus: int32(receipt.ResponseStatus),
		Receipt: body, AcceptedAt: pgtype.Timestamptz{Time: receipt.AcceptedAt.UTC(), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.CommandReceipt{}, biz.ErrIdempotencyConflict
	}
	if err != nil {
		return biz.CommandReceipt{}, biz.ErrPersistence
	}
	return receiptFromRow(row)
}

func receiptFromRow(row datasetsql.ModeldevDatasetCommandReceipt) (biz.CommandReceipt, error) {
	if !row.TenantID.Valid || !row.ReceiptID.Valid || !row.DatasetID.Valid || !row.AcceptedAt.Valid {
		return biz.CommandReceipt{}, biz.ErrPersistence
	}
	receipt := biz.CommandReceipt{
		TenantID: row.TenantID.String(), ReceiptID: row.ReceiptID.String(),
		ActorID: row.ActorID, Kind: biz.OperationKind(row.OperationKind),
		IdempotencyKey: row.IdempotencyKey, RequestSHA256: row.RequestSha256,
		DatasetID: row.DatasetID.String(), ResponseStatus: int(row.ResponseStatus),
		AcceptedAt: row.AcceptedAt.Time.UTC(),
	}
	// The stored body carries the verdict the command actually reached, so a
	// replayed receipt reports the same outcome as the first response.
	var stored struct {
		Outcome string `json:"outcome"`
	}
	if len(row.Receipt) > 0 && json.Unmarshal(row.Receipt, &stored) == nil {
		receipt.Outcome = stored.Outcome
	}
	if row.TaskID.Valid {
		receipt.TaskID = row.TaskID.String()
	}
	return receipt, nil
}

func (r *Repository) InsertOperation(ctx context.Context, operation biz.DatasetOperation) (biz.DatasetOperation, error) {
	if r == nil || r.pool == nil {
		return biz.DatasetOperation{}, biz.ErrPersistence
	}
	tenant, datasetID, err := tenantAndDataset(operation.TenantID, operation.DatasetID)
	if err != nil {
		return biz.DatasetOperation{}, err
	}
	operationID, err := databaseID(operation.OperationID)
	if err != nil {
		return biz.DatasetOperation{}, err
	}
	params := datasetsql.InsertDatasetOperationParams{
		TenantID: tenant, OperationID: operationID, DatasetID: datasetID,
		Actor: operation.Actor, EventType: string(operation.EventType),
		Result: operation.Result, BeforeState: operation.BeforeState, AfterState: operation.AfterState,
		Detail: []byte(`{}`),
	}
	if operation.TaskID != "" {
		if params.TaskID, err = databaseID(operation.TaskID); err != nil {
			return biz.DatasetOperation{}, err
		}
	}
	if operation.AttemptID != "" {
		if params.AttemptID, err = databaseID(operation.AttemptID); err != nil {
			return biz.DatasetOperation{}, err
		}
	}
	if operation.ReceiptID != "" {
		if params.ReceiptID, err = databaseID(operation.ReceiptID); err != nil {
			return biz.DatasetOperation{}, err
		}
	}
	if operation.BeforeRevision > 0 {
		params.BeforeRevision = pgtype.Int8{Int64: operation.BeforeRevision, Valid: true}
	}
	if operation.AfterRevision > 0 {
		params.AfterRevision = pgtype.Int8{Int64: operation.AfterRevision, Valid: true}
	}
	if operation.RecoveryGeneration > 0 {
		params.RecoveryGeneration = pgtype.Int8{Int64: operation.RecoveryGeneration, Valid: true}
	}
	if operation.FailureCode != "" {
		params.FailureCode = pgtype.Text{String: operation.FailureCode, Valid: true}
	}
	row, err := datasetsql.New(r.pool).InsertDatasetOperation(ctx, params)
	if err != nil {
		return biz.DatasetOperation{}, biz.ErrPersistence
	}
	return operationFromRow(row)
}

func (r *Repository) ListOperations(ctx context.Context, filter biz.DatasetOperationFilter) (biz.DatasetOperationPage, error) {
	if r == nil || r.pool == nil {
		return biz.DatasetOperationPage{}, biz.ErrPersistence
	}
	tenant, datasetID, err := tenantAndDataset(filter.TenantID, filter.DatasetID)
	if err != nil {
		return biz.DatasetOperationPage{}, err
	}
	pageSize, offset := normalizePage(filter.Page, filter.PageSize)
	var taskID pgtype.UUID
	if filter.TaskID != "" {
		if taskID, err = databaseID(filter.TaskID); err != nil {
			return biz.DatasetOperationPage{}, err
		}
	}
	queries := datasetsql.New(r.pool)
	rows, err := queries.ListDatasetOperations(ctx, datasetsql.ListDatasetOperationsParams{
		TenantID: tenant, DatasetID: datasetID, EventType: filter.EventType, TaskID: taskID,
		PageSize: pageSize, PageOffset: offset,
	})
	if err != nil {
		return biz.DatasetOperationPage{}, biz.ErrPersistence
	}
	total, err := queries.CountDatasetOperations(ctx, datasetsql.CountDatasetOperationsParams{
		TenantID: tenant, DatasetID: datasetID, EventType: filter.EventType, TaskID: taskID,
	})
	if err != nil {
		return biz.DatasetOperationPage{}, biz.ErrPersistence
	}
	page := biz.DatasetOperationPage{Total: total, Items: make([]biz.DatasetOperation, 0, len(rows))}
	for _, row := range rows {
		item, err := operationFromRow(row)
		if err != nil {
			return biz.DatasetOperationPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}

func operationFromRow(row datasetsql.ModeldevDatasetOperation) (biz.DatasetOperation, error) {
	if !row.TenantID.Valid || !row.OperationID.Valid || !row.DatasetID.Valid || !row.OccurredAt.Valid {
		return biz.DatasetOperation{}, biz.ErrPersistence
	}
	operation := biz.DatasetOperation{
		TenantID: row.TenantID.String(), OperationID: row.OperationID.String(),
		DatasetID: row.DatasetID.String(), Actor: row.Actor,
		EventType: biz.DatasetEventType(row.EventType), Result: row.Result,
		BeforeState: row.BeforeState, AfterState: row.AfterState,
		OccurredAt: row.OccurredAt.Time.UTC(),
	}
	if row.TaskID.Valid {
		operation.TaskID = row.TaskID.String()
	}
	if row.AttemptID.Valid {
		operation.AttemptID = row.AttemptID.String()
	}
	if row.ReceiptID.Valid {
		operation.ReceiptID = row.ReceiptID.String()
	}
	if row.BeforeRevision.Valid {
		operation.BeforeRevision = row.BeforeRevision.Int64
	}
	if row.AfterRevision.Valid {
		operation.AfterRevision = row.AfterRevision.Int64
	}
	if row.RecoveryGeneration.Valid {
		operation.RecoveryGeneration = row.RecoveryGeneration.Int64
	}
	if row.FailureCode.Valid {
		operation.FailureCode = row.FailureCode.String
	}
	return operation, nil
}

func tenantAndDataset(tenantID, datasetID string) (pgtype.UUID, pgtype.UUID, error) {
	tenant, err := databaseID(tenantID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	id, err := databaseID(datasetID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	return tenant, id, nil
}

// Page bounds mirror the documented list contract: default 20, maximum 100.
func normalizePage(page, pageSize int32) (int32, int32) {
	if page <= 0 {
		page = 1
	}
	size := pageSize
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return size, int32(int64(page-1) * int64(size))
}

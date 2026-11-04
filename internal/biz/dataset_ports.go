package biz

import "context"

// DatasetRepository is the persistence port for dataset collections and their
// command receipts. Every method is tenant-scoped: a caller that passes another
// tenant's id gets "not found", never that tenant's data.
type DatasetRepository interface {
	// CreateDataset inserts one collection. A duplicate name in the same tenant
	// returns ErrDatasetNameConflict.
	CreateDataset(ctx context.Context, value Dataset) (Dataset, error)
	GetDataset(ctx context.Context, tenantID, datasetID string) (Dataset, error)
	ListDatasets(ctx context.Context, filter DatasetListFilter) (DatasetPage, error)
	// UpdateDatasetMetadata compares expected_revision and bumps revision.
	// A stale revision returns ErrRevisionConflict without writing.
	UpdateDatasetMetadata(ctx context.Context, update DatasetMetadataUpdate) (Dataset, error)
	ArchiveDataset(ctx context.Context, change DatasetStateChange) (Dataset, error)
	RestoreDataset(ctx context.Context, change DatasetStateChange) (Dataset, error)
}

// DatasetListFilter is one page of the collection list. An empty State means
// "no lifecycle filter"; ALL is expressed by leaving it empty.
type DatasetListFilter struct {
	TenantID   string
	State      string
	NameFilter string
	Page       int32
	PageSize   int32
}

type DatasetPage struct {
	Total int64
	Items []Dataset
}

// DatasetMetadataUpdate carries the already-normalized values plus the revision
// the caller observed. Only present fields are applied.
type DatasetMetadataUpdate struct {
	TenantID          string
	DatasetID         string
	ActorID           string
	Name              string
	Description       string
	UpdateName        bool
	UpdateDescription bool
	ExpectedRevision  int64
}

// DatasetStateChange is an archive or restore with its observed revision.
type DatasetStateChange struct {
	TenantID         string
	DatasetID        string
	ActorID          string
	ExpectedRevision int64
}

// DatasetCommandRepository owns idempotent command receipts. A receipt is
// written in the same transaction as the change it describes, so a replay
// always finds the original outcome.
type DatasetCommandRepository interface {
	FindReceipt(ctx context.Context, tenantID, actorID string, kind OperationKind, key string) (CommandReceipt, error)
	InsertReceipt(ctx context.Context, receipt CommandReceipt) (CommandReceipt, error)
}

// DatasetOperationRepository is the append-only history.
type DatasetOperationRepository interface {
	InsertOperation(ctx context.Context, operation DatasetOperation) (DatasetOperation, error)
	ListOperations(ctx context.Context, filter DatasetOperationFilter) (DatasetOperationPage, error)
}

type DatasetOperationFilter struct {
	TenantID  string
	DatasetID string
	EventType string
	TaskID    string
	Page      int32
	PageSize  int32
}

type DatasetOperationPage struct {
	Total int64
	Items []DatasetOperation
}

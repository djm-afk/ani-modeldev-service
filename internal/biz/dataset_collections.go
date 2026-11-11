package biz

import (
	"context"
	"errors"
	"time"
)

// DatasetCollections is the CRUD use case for collections. Every write is an
// idempotent command: the receipt is written by the same repository call that
// performs the change, and a replay returns the original outcome.
type DatasetCollections struct {
	datasets   DatasetRepository
	commands   DatasetCommandRepository
	operations DatasetOperationRepository
	versions   DatasetVersionRepository
	tasks      DatasetImportTaskRepository
	now        func() time.Time
	newID      func() (string, error)
}

type DatasetCollectionsDeps struct {
	Datasets   DatasetRepository
	Commands   DatasetCommandRepository
	Operations DatasetOperationRepository
	Versions   DatasetVersionRepository
	Tasks      DatasetImportTaskRepository
	Now        func() time.Time
	NewID      func() (string, error)
}

func NewDatasetCollections(deps DatasetCollectionsDeps) (*DatasetCollections, error) {
	if deps.Datasets == nil || deps.Commands == nil || deps.Operations == nil {
		return nil, ErrDatasetInvalidMetadata
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	newID := deps.NewID
	if newID == nil {
		newID = defaultUUID
	}
	return &DatasetCollections{datasets: deps.Datasets, commands: deps.Commands, operations: deps.Operations, versions: deps.Versions, tasks: deps.Tasks, now: now, newID: newID}, nil
}

// CreateDatasetInput is one creation command.
type CreateDatasetInput struct {
	TenantID       string
	ActorID        string
	IdempotencyKey string
	Name           string
	Description    string
}

// CreateDataset normalizes the metadata, then writes the collection, its
// receipt and the history event. A duplicate name is a business conflict, not
// an idempotent replay.
func (collections *DatasetCollections) CreateDataset(ctx context.Context, input CreateDatasetInput) (Dataset, CommandReceipt, error) {
	if collections == nil || !CanonicalEnvironmentUUID(input.TenantID) || input.ActorID == "" || !ValidIdempotencyKey(input.IdempotencyKey) {
		return Dataset{}, CommandReceipt{}, ErrDatasetInvalidMetadata
	}
	name, err := NormalizeDatasetName(input.Name)
	if err != nil {
		return Dataset{}, CommandReceipt{}, err
	}
	description, err := NormalizeDatasetDescription(input.Description)
	if err != nil {
		return Dataset{}, CommandReceipt{}, err
	}
	// The dataset id is generated first so the intent can name its target.
	datasetID, err := collections.newID()
	if err != nil {
		return Dataset{}, CommandReceipt{}, ErrPersistence
	}
	intent := DatasetCommand{TenantID: input.TenantID, ActorID: input.ActorID, Kind: OpCreateDataset,
		IdempotencyKey: input.IdempotencyKey, DatasetID: datasetID, Name: name, Description: description, UpdateName: true}
	if receipt, dataset, ok, err := collections.replay(ctx, input.TenantID, input.ActorID, intent); err != nil {
		return Dataset{}, CommandReceipt{}, err
	} else if ok {
		return dataset, receipt, nil
	}

	dataset, err := collections.datasets.CreateDataset(ctx, Dataset{
		TenantID: input.TenantID, DatasetID: datasetID, Name: name, Description: description,
		CreatedBy: input.ActorID, UpdatedBy: input.ActorID,
	})
	if errors.Is(err, ErrDatasetNameConflict) {
		return Dataset{}, CommandReceipt{}, err
	}
	if err != nil {
		return Dataset{}, CommandReceipt{}, err
	}
	receipt, err := collections.writeReceipt(ctx, intent, datasetID, "", ReceiptStatusCreated)
	if err != nil {
		return Dataset{}, CommandReceipt{}, err
	}
	if err := collections.writeEvent(ctx, dataset.TenantID, dataset.DatasetID, "", receipt.ReceiptID, input.ActorID, EventCreated, "SUCCEEDED", "", string(dataset.State), 0, dataset.Revision); err != nil {
		return Dataset{}, CommandReceipt{}, err
	}
	return dataset, receipt, nil
}

// UpdateDatasetInput is one metadata edit. Only the fields present are applied.
type UpdateDatasetInput struct {
	TenantID          string
	ActorID           string
	DatasetID         string
	IdempotencyKey    string
	Name              string
	Description       string
	UpdateName        bool
	UpdateDescription bool
	ExpectedRevision  int64
}

// UpdateDataset applies a version-checked metadata change. A no-op edit is an
// idempotent success that does not advance the revision or emit an event.
func (collections *DatasetCollections) UpdateDataset(ctx context.Context, input UpdateDatasetInput) (Dataset, CommandReceipt, error) {
	if collections == nil || !CanonicalEnvironmentUUID(input.TenantID) || !CanonicalEnvironmentUUID(input.DatasetID) || !ValidIdempotencyKey(input.IdempotencyKey) || input.ExpectedRevision <= 0 {
		return Dataset{}, CommandReceipt{}, ErrDatasetInvalidMetadata
	}
	current, err := collections.datasets.GetDataset(ctx, input.TenantID, input.DatasetID)
	if err != nil {
		return Dataset{}, CommandReceipt{}, err
	}
	name, description := current.Name, current.Description
	if input.UpdateName {
		if name, err = NormalizeDatasetName(input.Name); err != nil {
			return Dataset{}, CommandReceipt{}, err
		}
	}
	if input.UpdateDescription {
		if description, err = NormalizeDatasetDescription(input.Description); err != nil {
			return Dataset{}, CommandReceipt{}, err
		}
	}
	intent := DatasetCommand{TenantID: input.TenantID, ActorID: input.ActorID, Kind: OpUpdateDataset,
		IdempotencyKey: input.IdempotencyKey, DatasetID: input.DatasetID, Name: name, Description: description,
		UpdateName: input.UpdateName, UpdateDesc: input.UpdateDescription, ExpectedRevision: input.ExpectedRevision}
	if receipt, dataset, ok, err := collections.replay(ctx, input.TenantID, input.ActorID, intent); err != nil {
		return Dataset{}, CommandReceipt{}, err
	} else if ok {
		return dataset, receipt, nil
	}

	if name == current.Name && description == current.Description {
		// Same values: report the current revision without a fake change.
		receipt, err := collections.writeReceipt(ctx, intent, input.DatasetID, "", ReceiptStatusOK)
		if err != nil {
			return Dataset{}, CommandReceipt{}, err
		}
		if err := collections.writeEvent(ctx, input.TenantID, input.DatasetID, "", receipt.ReceiptID, input.ActorID, EventUpdated, "NO_CHANGE", string(current.State), string(current.State), current.Revision, current.Revision); err != nil {
			return Dataset{}, CommandReceipt{}, err
		}
		return current, receipt, nil
	}
	updated, err := collections.datasets.UpdateDatasetMetadata(ctx, DatasetMetadataUpdate{
		TenantID: input.TenantID, DatasetID: input.DatasetID, ActorID: input.ActorID,
		Name: name, Description: description, UpdateName: input.UpdateName, UpdateDescription: input.UpdateDescription,
		ExpectedRevision: input.ExpectedRevision,
	})
	if errors.Is(err, ErrRevisionConflict) {
		return Dataset{}, CommandReceipt{}, err
	}
	if err != nil {
		return Dataset{}, CommandReceipt{}, err
	}
	receipt, err := collections.writeReceipt(ctx, intent, input.DatasetID, "", ReceiptStatusOK)
	if err != nil {
		return Dataset{}, CommandReceipt{}, err
	}
	if err := collections.writeEvent(ctx, updated.TenantID, updated.DatasetID, "", receipt.ReceiptID, input.ActorID, EventUpdated, "SUCCEEDED", string(current.State), string(updated.State), current.Revision, updated.Revision); err != nil {
		return Dataset{}, CommandReceipt{}, err
	}
	return updated, receipt, nil
}

// DatasetLifecycleInput archives or restores one collection.
type DatasetLifecycleInput struct {
	TenantID         string
	ActorID          string
	DatasetID        string
	IdempotencyKey   string
	ExpectedRevision int64
}

// ArchiveDataset closes new imports and new training admission. The collection
// keeps its name; its versions stay readable.
func (collections *DatasetCollections) ArchiveDataset(ctx context.Context, input DatasetLifecycleInput) (Dataset, CommandReceipt, error) {
	return collections.changeLifecycle(ctx, input, true)
}

// RestoreDataset reopens a collection. It never revives cancelled work.
func (collections *DatasetCollections) RestoreDataset(ctx context.Context, input DatasetLifecycleInput) (Dataset, CommandReceipt, error) {
	return collections.changeLifecycle(ctx, input, false)
}

func (collections *DatasetCollections) changeLifecycle(ctx context.Context, input DatasetLifecycleInput, archive bool) (Dataset, CommandReceipt, error) {
	if collections == nil || !CanonicalEnvironmentUUID(input.TenantID) || !CanonicalEnvironmentUUID(input.DatasetID) || !ValidIdempotencyKey(input.IdempotencyKey) || input.ExpectedRevision <= 0 {
		return Dataset{}, CommandReceipt{}, ErrDatasetInvalidMetadata
	}
	kind, target, event := OpArchiveDataset, DatasetArchived, EventArchived
	if !archive {
		kind, target, event = OpRestoreDataset, DatasetActive, EventRestored
	}
	intent := DatasetCommand{TenantID: input.TenantID, ActorID: input.ActorID, Kind: kind,
		IdempotencyKey: input.IdempotencyKey, DatasetID: input.DatasetID, ExpectedRevision: input.ExpectedRevision}
	if receipt, dataset, ok, err := collections.replay(ctx, input.TenantID, input.ActorID, intent); err != nil {
		return Dataset{}, CommandReceipt{}, err
	} else if ok {
		return dataset, receipt, nil
	}
	current, err := collections.datasets.GetDataset(ctx, input.TenantID, input.DatasetID)
	if err != nil {
		return Dataset{}, CommandReceipt{}, err
	}
	if current.State == target {
		// Already in the requested state: an idempotent no-op.
		receipt, err := collections.writeReceipt(ctx, intent, input.DatasetID, "", ReceiptStatusOK)
		if err != nil {
			return Dataset{}, CommandReceipt{}, err
		}
		if err := collections.writeEvent(ctx, input.TenantID, input.DatasetID, "", receipt.ReceiptID, input.ActorID, event, "NO_CHANGE", string(current.State), string(current.State), current.Revision, current.Revision); err != nil {
			return Dataset{}, CommandReceipt{}, err
		}
		return current, receipt, nil
	}
	change := DatasetStateChange{TenantID: input.TenantID, DatasetID: input.DatasetID, ActorID: input.ActorID, ExpectedRevision: input.ExpectedRevision}
	var updated Dataset
	if archive {
		updated, err = collections.datasets.ArchiveDataset(ctx, change)
	} else {
		updated, err = collections.datasets.RestoreDataset(ctx, change)
	}
	if err != nil {
		return Dataset{}, CommandReceipt{}, err
	}
	receipt, err := collections.writeReceipt(ctx, intent, input.DatasetID, "", ReceiptStatusOK)
	if err != nil {
		return Dataset{}, CommandReceipt{}, err
	}
	if err := collections.writeEvent(ctx, updated.TenantID, updated.DatasetID, "", receipt.ReceiptID, input.ActorID, event, "SUCCEEDED", string(current.State), string(updated.State), current.Revision, updated.Revision); err != nil {
		return Dataset{}, CommandReceipt{}, err
	}
	return updated, receipt, nil
}

// replay answers an existing receipt. It returns ok=false when the command is
// genuinely new, so the caller proceeds to the state check.
func (collections *DatasetCollections) replay(ctx context.Context, tenantID, actorID string, intent DatasetCommand) (CommandReceipt, Dataset, bool, error) {
	receipt, err := collections.commands.FindReceipt(ctx, tenantID, actorID, intent.Kind, intent.IdempotencyKey)
	if errors.Is(err, ErrDatasetNotFound) {
		return CommandReceipt{}, Dataset{}, false, nil
	}
	if err != nil {
		return CommandReceipt{}, Dataset{}, false, err
	}
	if !receipt.SameIntent(intent) {
		return CommandReceipt{}, Dataset{}, false, ErrIdempotencyConflict
	}
	dataset, err := collections.datasets.GetDataset(ctx, tenantID, receipt.DatasetID)
	if err != nil {
		return CommandReceipt{}, Dataset{}, false, err
	}
	return receipt, dataset, true, nil
}

func (collections *DatasetCollections) writeReceipt(ctx context.Context, intent DatasetCommand, datasetID, taskID string, status int) (CommandReceipt, error) {
	receiptID, err := collections.newID()
	if err != nil {
		return CommandReceipt{}, ErrPersistence
	}
	return collections.commands.InsertReceipt(ctx, CommandReceipt{
		TenantID: intent.TenantID, ReceiptID: receiptID, ActorID: intent.ActorID, Kind: intent.Kind,
		IdempotencyKey: intent.IdempotencyKey, RequestSHA256: intent.Digest(),
		DatasetID: datasetID, TaskID: taskID, ResponseStatus: status,
		AcceptedAt: collections.now().UTC().Truncate(time.Microsecond),
	})
}

func (collections *DatasetCollections) writeEvent(ctx context.Context, tenantID, datasetID, taskID, receiptID, actor string, event DatasetEventType, result, before, after string, beforeRevision, afterRevision int64) error {
	if collections.operations == nil {
		return nil
	}
	operationID, err := collections.newID()
	if err != nil {
		return ErrPersistence
	}
	_, err = collections.operations.InsertOperation(ctx, DatasetOperation{
		TenantID: tenantID, OperationID: operationID, DatasetID: datasetID, TaskID: taskID,
		ReceiptID: receiptID, Actor: actor, EventType: event, Result: result,
		BeforeState: before, AfterState: after, BeforeRevision: beforeRevision, AfterRevision: afterRevision,
	})
	return err
}

// GetDataset reads one collection.
func (collections *DatasetCollections) GetDataset(ctx context.Context, tenantID, datasetID string) (Dataset, error) {
	if collections == nil {
		return Dataset{}, ErrDatasetNotFound
	}
	return collections.datasets.GetDataset(ctx, tenantID, datasetID)
}

// ListDatasets returns one page.
func (collections *DatasetCollections) ListDatasets(ctx context.Context, filter DatasetListFilter) (DatasetPage, error) {
	if collections == nil {
		return DatasetPage{}, ErrDatasetNotFound
	}
	return collections.datasets.ListDatasets(ctx, filter)
}

// ListVersions returns one page of published versions.
func (collections *DatasetCollections) ListVersions(ctx context.Context, filter DatasetVersionFilter) (DatasetVersionPage, error) {
	if collections == nil || collections.versions == nil {
		return DatasetVersionPage{}, ErrDatasetNotFound
	}
	return collections.versions.ListVersions(ctx, filter)
}

// GetVersion reads the dataset side of one published InputVersion.
func (collections *DatasetCollections) GetVersion(ctx context.Context, tenantID, inputVersionID string) (DatasetVersion, error) {
	if collections == nil || collections.versions == nil {
		return DatasetVersion{}, ErrDatasetNotFound
	}
	return collections.versions.GetVersion(ctx, tenantID, inputVersionID)
}

// ListImportTasks returns one page of tasks for a collection.
func (collections *DatasetCollections) ListImportTasks(ctx context.Context, filter DatasetImportFilter) (DatasetImportTaskPage, error) {
	if collections == nil || collections.tasks == nil {
		return DatasetImportTaskPage{}, ErrDatasetNotFound
	}
	return collections.tasks.ListImportTasks(ctx, filter)
}

// GetImportTask reads one task.
func (collections *DatasetCollections) GetImportTask(ctx context.Context, tenantID, taskID string) (DatasetImportTask, error) {
	if collections == nil || collections.tasks == nil {
		return DatasetImportTask{}, ErrImportTaskNotFound
	}
	return collections.tasks.GetImportTask(ctx, tenantID, taskID)
}

// ListOperations returns one page of the operation history.
func (collections *DatasetCollections) ListOperations(ctx context.Context, filter DatasetOperationFilter) (DatasetOperationPage, error) {
	if collections == nil || collections.operations == nil {
		return DatasetOperationPage{}, ErrDatasetNotFound
	}
	return collections.operations.ListOperations(ctx, filter)
}

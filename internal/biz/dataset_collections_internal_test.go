package biz

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type memDatasets struct {
	byID     map[string]Dataset
	conflict bool
	revision int64
}

func newMemDatasets() *memDatasets { return &memDatasets{byID: map[string]Dataset{}, revision: 1} }

func (m *memDatasets) CreateDataset(ctx context.Context, value Dataset) (Dataset, error) {
	if m.conflict {
		return Dataset{}, ErrDatasetNameConflict
	}
	for _, existing := range m.byID {
		if existing.TenantID == value.TenantID && existing.Name == value.Name {
			return Dataset{}, ErrDatasetNameConflict
		}
	}
	value.State = DatasetActive
	value.Revision = 1
	value.NextVersionNumber = 1
	m.byID[value.DatasetID] = value
	return value, nil
}
func (m *memDatasets) GetDataset(ctx context.Context, tenantID, datasetID string) (Dataset, error) {
	value, ok := m.byID[datasetID]
	if !ok {
		return Dataset{}, ErrDatasetNotFound
	}
	return value, nil
}
func (m *memDatasets) ListDatasets(ctx context.Context, filter DatasetListFilter) (DatasetPage, error) {
	return DatasetPage{}, nil
}
func (m *memDatasets) UpdateDatasetMetadata(ctx context.Context, update DatasetMetadataUpdate) (Dataset, error) {
	value, ok := m.byID[update.DatasetID]
	if !ok {
		return Dataset{}, ErrDatasetNotFound
	}
	if value.Revision != update.ExpectedRevision {
		return Dataset{}, ErrRevisionConflict
	}
	if update.UpdateName {
		value.Name = update.Name
	}
	if update.UpdateDescription {
		value.Description = update.Description
	}
	value.Revision++
	m.byID[update.DatasetID] = value
	return value, nil
}
func (m *memDatasets) ArchiveDataset(ctx context.Context, change DatasetStateChange) (Dataset, error) {
	value, ok := m.byID[change.DatasetID]
	if !ok || value.Revision != change.ExpectedRevision {
		return Dataset{}, ErrRevisionConflict
	}
	value.State = DatasetArchived
	value.Revision++
	m.byID[change.DatasetID] = value
	return value, nil
}
func (m *memDatasets) RestoreDataset(ctx context.Context, change DatasetStateChange) (Dataset, error) {
	value, ok := m.byID[change.DatasetID]
	if !ok || value.Revision != change.ExpectedRevision {
		return Dataset{}, ErrRevisionConflict
	}
	value.State = DatasetActive
	value.Revision++
	m.byID[change.DatasetID] = value
	return value, nil
}

type memCommands struct{ byKey map[string]CommandReceipt }

func newMemCommands() *memCommands { return &memCommands{byKey: map[string]CommandReceipt{}} }

func (m *memCommands) FindReceipt(ctx context.Context, tenantID, actorID string, kind OperationKind, key string) (CommandReceipt, error) {
	receipt, ok := m.byKey[string(kind)+"|"+key]
	if !ok {
		return CommandReceipt{}, ErrDatasetNotFound
	}
	return receipt, nil
}
func (m *memCommands) InsertReceipt(ctx context.Context, receipt CommandReceipt) (CommandReceipt, error) {
	m.byKey[string(receipt.Kind)+"|"+receipt.IdempotencyKey] = receipt
	return receipt, nil
}

type memOperations struct{ events []DatasetOperation }

func (m *memOperations) InsertOperation(ctx context.Context, operation DatasetOperation) (DatasetOperation, error) {
	m.events = append(m.events, operation)
	return operation, nil
}
func (m *memOperations) ListOperations(ctx context.Context, filter DatasetOperationFilter) (DatasetOperationPage, error) {
	return DatasetOperationPage{}, nil
}

type collectionsFixture struct {
	collections *DatasetCollections
	datasets    *memDatasets
	commands    *memCommands
	operations  *memOperations
}

func newCollectionsFixture() *collectionsFixture {
	f := &collectionsFixture{datasets: newMemDatasets(), commands: newMemCommands(), operations: &memOperations{}}
	counter := 0
	collections, err := NewDatasetCollections(DatasetCollectionsDeps{
		Datasets: f.datasets, Commands: f.commands, Operations: f.operations,
		Now: func() time.Time { return time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC) },
		NewID: func() (string, error) {
			counter++
			return "0000000" + string(rune('0'+counter)) + "-0000-4000-8000-000000000000", nil
		},
	})
	if err != nil {
		panic(err)
	}
	f.collections = collections
	return f
}

func createInput() CreateDatasetInput {
	return CreateDatasetInput{
		TenantID: fixtureTenant, ActorID: "governance:user:7",
		IdempotencyKey: "create-1", Name: "客服问答", Description: "说明",
	}
}

func TestCreateDatasetWritesReceiptAndEvent(t *testing.T) {
	f := newCollectionsFixture()
	dataset, receipt, err := f.collections.CreateDataset(context.Background(), createInput())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if dataset.Name != "客服问答" || dataset.State != DatasetActive || receipt.ResponseStatus != ReceiptStatusCreated {
		t.Fatalf("dataset=%+v receipt=%+v", dataset, receipt)
	}
	if len(f.operations.events) != 1 || f.operations.events[0].EventType != EventCreated {
		t.Fatalf("events = %+v", f.operations.events)
	}
}

func TestCreateDatasetRejectsIllegalName(t *testing.T) {
	f := newCollectionsFixture()
	for _, name := range []string{"", " leading", strings.Repeat("a", 129), "e\u0301"} {
		input := createInput()
		input.Name = name
		if _, _, err := f.collections.CreateDataset(context.Background(), input); !errors.Is(err, ErrDatasetInvalidMetadata) {
			t.Fatalf("%q accepted: %v", name, err)
		}
	}
}

func TestCreateDatasetRejectsTenantAndKeyShape(t *testing.T) {
	f := newCollectionsFixture()
	for name, mutate := range map[string]func(*CreateDatasetInput){
		"bad tenant": func(i *CreateDatasetInput) { i.TenantID = "not-a-uuid" },
		"bad key":    func(i *CreateDatasetInput) { i.IdempotencyKey = "-bad" },
		"no actor":   func(i *CreateDatasetInput) { i.ActorID = "" },
	} {
		input := createInput()
		mutate(&input)
		if _, _, err := f.collections.CreateDataset(context.Background(), input); !errors.Is(err, ErrDatasetInvalidMetadata) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

func TestCreateDatasetReplayReturnsOriginal(t *testing.T) {
	f := newCollectionsFixture()
	first, receipt, err := f.collections.CreateDataset(context.Background(), createInput())
	if err != nil {
		t.Fatal(err)
	}
	second, replay, err := f.collections.CreateDataset(context.Background(), createInput())
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if second.DatasetID != first.DatasetID || replay.ReceiptID != receipt.ReceiptID {
		t.Fatal("replay created a second collection")
	}
	// The replay must not add a second history event.
	if len(f.operations.events) != 1 {
		t.Fatalf("replay wrote %d events", len(f.operations.events))
	}
}

func TestCreateDatasetDuplicateNameConflicts(t *testing.T) {
	f := newCollectionsFixture()
	if _, _, err := f.collections.CreateDataset(context.Background(), createInput()); err != nil {
		t.Fatal(err)
	}
	second := createInput()
	second.IdempotencyKey = "create-2"
	if _, _, err := f.collections.CreateDataset(context.Background(), second); !errors.Is(err, ErrDatasetNameConflict) {
		t.Fatalf("duplicate name returned %v", err)
	}
}

func seedDataset(t *testing.T, f *collectionsFixture) string {
	t.Helper()
	dataset, _, err := f.collections.CreateDataset(context.Background(), createInput())
	if err != nil {
		t.Fatal(err)
	}
	return dataset.DatasetID
}

func TestUpdateDatasetNoOpDoesNotAdvanceRevision(t *testing.T) {
	f := newCollectionsFixture()
	datasetID := seedDataset(t, f)
	updated, receipt, err := f.collections.UpdateDataset(context.Background(), UpdateDatasetInput{
		TenantID: fixtureTenant, ActorID: "governance:user:7", DatasetID: datasetID,
		IdempotencyKey: "update-1", Name: "客服问答", UpdateName: true, ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Revision != 1 {
		t.Fatalf("no-op advanced revision to %d", updated.Revision)
	}
	last := f.operations.events[len(f.operations.events)-1]
	if last.Result != "NO_CHANGE" {
		t.Fatalf("no-op event result = %q", last.Result)
	}
	if receipt.ReceiptID == "" {
		t.Fatal("no-op did not record a receipt")
	}
}

func TestUpdateDatasetStaleRevisionConflicts(t *testing.T) {
	f := newCollectionsFixture()
	datasetID := seedDataset(t, f)
	_, _, err := f.collections.UpdateDataset(context.Background(), UpdateDatasetInput{
		TenantID: fixtureTenant, ActorID: "a:b:1", DatasetID: datasetID,
		IdempotencyKey: "update-2", Name: "新名字", UpdateName: true, ExpectedRevision: 9,
	})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale revision returned %v", err)
	}
}

func TestArchiveAndRestoreAreIdempotentNoOps(t *testing.T) {
	f := newCollectionsFixture()
	datasetID := seedDataset(t, f)
	base := DatasetLifecycleInput{TenantID: fixtureTenant, ActorID: "a:b:1", DatasetID: datasetID, IdempotencyKey: "archive-1", ExpectedRevision: 1}
	archived, _, err := f.collections.ArchiveDataset(context.Background(), base)
	if err != nil || archived.State != DatasetArchived {
		t.Fatalf("archive: %+v %v", archived, err)
	}
	// Archiving again is a no-op at the new revision.
	second := base
	second.IdempotencyKey = "archive-2"
	second.ExpectedRevision = archived.Revision
	same, _, err := f.collections.ArchiveDataset(context.Background(), second)
	if err != nil || same.Revision != archived.Revision || same.State != DatasetArchived {
		t.Fatalf("second archive: %+v %v", same, err)
	}
	restore := DatasetLifecycleInput{TenantID: fixtureTenant, ActorID: "a:b:1", DatasetID: datasetID, IdempotencyKey: "restore-1", ExpectedRevision: same.Revision}
	active, _, err := f.collections.RestoreDataset(context.Background(), restore)
	if err != nil || active.State != DatasetActive {
		t.Fatalf("restore: %+v %v", active, err)
	}
}

func TestArchivedDatasetStillOccupiesItsName(t *testing.T) {
	f := newCollectionsFixture()
	datasetID := seedDataset(t, f)
	if _, _, err := f.collections.ArchiveDataset(context.Background(), DatasetLifecycleInput{
		TenantID: fixtureTenant, ActorID: "a:b:1", DatasetID: datasetID, IdempotencyKey: "archive-3", ExpectedRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	second := createInput()
	second.IdempotencyKey = "create-3"
	if _, _, err := f.collections.CreateDataset(context.Background(), second); !errors.Is(err, ErrDatasetNameConflict) {
		t.Fatalf("archived name was reused: %v", err)
	}
}

func TestUpdateDatasetRejectsWrongIdempotencyIntent(t *testing.T) {
	f := newCollectionsFixture()
	datasetID := seedDataset(t, f)
	if _, _, err := f.collections.UpdateDataset(context.Background(), UpdateDatasetInput{
		TenantID: fixtureTenant, ActorID: "governance:user:7", DatasetID: datasetID,
		IdempotencyKey: "update-3", Name: "A", UpdateName: true, ExpectedRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// Same key, different name: a different intent must not reuse the receipt.
	if _, _, err := f.collections.UpdateDataset(context.Background(), UpdateDatasetInput{
		TenantID: fixtureTenant, ActorID: "governance:user:7", DatasetID: datasetID,
		IdempotencyKey: "update-3", Name: "B", UpdateName: true, ExpectedRevision: 1,
	}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different intent reused the key: %v", err)
	}
}

package biz

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	fixtureTenant    = "11111111-1111-4111-8111-111111111111"
	fixtureDataset   = "22222222-2222-4222-8222-222222222222"
	fixtureTask      = "33333333-3333-4333-8333-333333333333"
	fixtureCluster   = "44444444-4444-4444-8444-444444444444"
	fixtureNamespace = "55555555-5555-4555-8555-555555555555"
)

type fakeDatasets struct {
	value      Dataset
	err        error
	readCalls  int
	archiveErr error
}

func (f *fakeDatasets) CreateDataset(ctx context.Context, value Dataset) (Dataset, error) {
	return value, nil
}
func (f *fakeDatasets) GetDataset(ctx context.Context, tenantID, datasetID string) (Dataset, error) {
	f.readCalls++
	return f.value, f.err
}
func (f *fakeDatasets) ListDatasets(ctx context.Context, filter DatasetListFilter) (DatasetPage, error) {
	return DatasetPage{}, nil
}
func (f *fakeDatasets) UpdateDatasetMetadata(ctx context.Context, update DatasetMetadataUpdate) (Dataset, error) {
	return Dataset{}, nil
}
func (f *fakeDatasets) ArchiveDataset(ctx context.Context, change DatasetStateChange) (Dataset, error) {
	return Dataset{}, f.archiveErr
}
func (f *fakeDatasets) RestoreDataset(ctx context.Context, change DatasetStateChange) (Dataset, error) {
	return Dataset{}, nil
}

type fakeCommands struct {
	receipt  CommandReceipt
	err      error
	inserted []CommandReceipt
}

func (f *fakeCommands) FindReceipt(ctx context.Context, tenantID, actorID string, kind OperationKind, key string) (CommandReceipt, error) {
	return f.receipt, f.err
}
func (f *fakeCommands) InsertReceipt(ctx context.Context, receipt CommandReceipt) (CommandReceipt, error) {
	f.inserted = append(f.inserted, receipt)
	return receipt, nil
}

type fakeTasks struct {
	value DatasetImportTask
	err   error
}

func (f *fakeTasks) GetImportTask(ctx context.Context, tenantID, taskID string) (DatasetImportTask, error) {
	return f.value, f.err
}
func (f *fakeTasks) ListImportTasks(ctx context.Context, filter DatasetImportFilter) (DatasetImportTaskPage, error) {
	return DatasetImportTaskPage{}, nil
}

type fakeVersions struct{}

func (f *fakeVersions) GetVersion(ctx context.Context, tenantID, inputVersionID string) (DatasetVersion, error) {
	return DatasetVersion{}, nil
}
func (f *fakeVersions) ListVersions(ctx context.Context, filter DatasetVersionFilter) (DatasetVersionPage, error) {
	return DatasetVersionPage{}, nil
}

type fakeOperations struct{ inserted []DatasetOperation }

func (f *fakeOperations) InsertOperation(ctx context.Context, operation DatasetOperation) (DatasetOperation, error) {
	f.inserted = append(f.inserted, operation)
	return operation, nil
}
func (f *fakeOperations) ListOperations(ctx context.Context, filter DatasetOperationFilter) (DatasetOperationPage, error) {
	return DatasetOperationPage{}, nil
}

type fakeAcceptance struct {
	got AcceptanceRequest
	err error
}

func (f *fakeAcceptance) AcceptImport(ctx context.Context, request AcceptanceRequest) (AcceptanceResult, error) {
	f.got = request
	if f.err != nil {
		return AcceptanceResult{}, f.err
	}
	return AcceptanceResult{ResponseStatus: ReceiptStatusAccepted}, nil
}

type fakePublication struct{}

func (f *fakePublication) ReserveImport(ctx context.Context, request InputImport) (InputVersion, error) {
	return InputVersion{}, nil
}

func (f *fakePublication) PublishVersion(ctx context.Context, request PublicationRequest) (PublicationResult, error) {
	return PublicationResult{}, nil
}

type fakeTransitions struct {
	cancelled, retried bool
	err                error
}

func (f *fakeTransitions) RequestCancel(ctx context.Context, request TaskCommandRequest) (DatasetImportTask, error) {
	f.cancelled = true
	return DatasetImportTask{}, f.err
}
func (f *fakeTransitions) RetryTask(ctx context.Context, request TaskCommandRequest) (DatasetImportTask, error) {
	f.retried = true
	return DatasetImportTask{}, f.err
}

type fakeTargets struct {
	value DatasetImportTarget
	err   error
}

func (f *fakeTargets) ReadDatasetImportTarget(ctx context.Context, tenantID string) (DatasetImportTarget, error) {
	return f.value, f.err
}

// fakeSources returns a fixed fact and records the paths it was asked about.
type fakeSources struct {
	fact  SourceFileFact
	err   error
	paths []string
}

func (f *fakeSources) InspectFile(ctx context.Context, provider SourceProvider, repoID, revision, filePath string) (SourceFileFact, error) {
	f.paths = append(f.paths, filePath)
	return f.fact, f.err
}
func (f *fakeSources) ListDirectory(ctx context.Context, provider SourceProvider, repoID, revision, directoryPath, pageToken string, pageSize int32) (SourcePage, error) {
	return SourcePage{}, nil
}

type importsFixture struct {
	imports     *DatasetImports
	datasets    *fakeDatasets
	commands    *fakeCommands
	tasks       *fakeTasks
	operations  *fakeOperations
	acceptance  *fakeAcceptance
	transitions *fakeTransitions
	targets     *fakeTargets
	sources     *fakeSources
}

func newImportsFixture() *importsFixture {
	f := &importsFixture{
		datasets:    &fakeDatasets{value: Dataset{TenantID: fixtureTenant, DatasetID: fixtureDataset, State: DatasetActive, Revision: 1}},
		commands:    &fakeCommands{err: ErrDatasetNotFound},
		tasks:       &fakeTasks{},
		operations:  &fakeOperations{},
		acceptance:  &fakeAcceptance{},
		transitions: &fakeTransitions{},
		targets:     &fakeTargets{value: fixtureTarget()},
		sources:     &fakeSources{fact: fixtureFact()},
	}
	imports, err := NewDatasetImports(DatasetImportsDeps{
		Datasets: f.datasets, Commands: f.commands, Tasks: f.tasks, Versions: &fakeVersions{},
		Operations: f.operations, Acceptance: f.acceptance, Publication: &fakePublication{},
		Transitions: f.transitions, Targets: f.targets, Sources: f.sources,
		Now:   func() time.Time { return time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC) },
		NewID: func() (string, error) { return fixtureTask, nil },
	})
	if err != nil {
		panic(err)
	}
	f.imports = imports
	return f
}

func fixtureTarget() DatasetImportTarget {
	return DatasetImportTarget{
		ClusterID: fixtureCluster, NamespaceName: "tenant-" + fixtureTenant, NamespaceUID: fixtureNamespace,
		InputScope: ImportTargetScope{
			StorageConnectionID: "input-store-v1", Bucket: "inputs",
			ApprovedPrefix: "inputs/" + fixtureTenant, CredentialReference: "input-read",
		},
		InputScopeSHA256: strings.Repeat("a", 64),
	}
}

func fixtureFact() SourceFileFact {
	return SourceFileFact{
		Provider: ProviderHuggingFace, RepoID: "example/support-alpaca",
		RequestedRevision: "main", FilePath: "alpaca.json",
		LockKind: LockImmutableRevision, ResolvedRevision: "abcdef0123456789",
		SizeBytes: 4096, FileSHA256: strings.Repeat("b", 64),
	}
}

func selectionFor(fact SourceFileFact) SourceSelection {
	return SourceSelection{
		Provider: fact.Provider, RepoID: fact.RepoID, RequestedRevision: fact.RequestedRevision,
		FilePath: fact.FilePath, LockKind: fact.LockKind, ResolvedRevision: fact.ResolvedRevision,
		ExpectedSizeBytes: fact.SizeBytes, ExpectedFileSHA256: fact.FileSHA256,
	}
}

func validAcceptance() ImportAcceptance {
	fact := fixtureFact()
	return ImportAcceptance{
		TenantID: fixtureTenant, ActorID: "governance:user:7", DatasetID: fixtureDataset,
		IdempotencyKey: "import-support-v1-01",
		Selection:      selectionFor(fact), SelectionFingerprint: selectionFor(fact).SelectionFingerprint(),
		AcceptedLimit: 1 << 30,
	}
}

func TestAcceptImportWritesFrozenFacts(t *testing.T) {
	f := newImportsFixture()
	result, err := f.imports.AcceptImport(context.Background(), validAcceptance())
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if result.ResponseStatus != ReceiptStatusAccepted {
		t.Fatalf("status = %d", result.ResponseStatus)
	}
	got := f.acceptance.got
	if got.ExpectedSizeBytes != 4096 || got.AcceptedLimitBytes != 1<<30 {
		t.Fatalf("frozen size/limit = %d/%d", got.ExpectedSizeBytes, got.AcceptedLimitBytes)
	}
	if got.Target.NamespaceName != "tenant-"+fixtureTenant {
		t.Fatalf("namespace = %q", got.Target.NamespaceName)
	}
	if len(got.SourceSnapshot) == 0 || len(got.TargetSnapshot) == 0 || len(got.RecoveryPolicy) == 0 || len(got.AuthorizationScope) == 0 {
		t.Fatal("acceptance did not freeze every snapshot")
	}
	// The reserved identities must be distinct pre-generated values.
	if got.TaskID == "" || got.ReceiptID == "" || got.ReservedInputVersionID == "" || got.InputRequestID == "" {
		t.Fatal("acceptance left an identity unset")
	}
}

func TestAcceptImportRejectsChangedSource(t *testing.T) {
	// The client confirmed one file, but the hub now resolves the same path to
	// different bytes. The re-resolution produces a fingerprint that no longer
	// matches the one the user echoed back, so acceptance must refuse.
	f := newImportsFixture()
	f.sources.fact.ResolvedRevision = "different-revision"
	acceptance := validAcceptance() // fingerprint still describes the original fact
	if _, err := f.imports.AcceptImport(context.Background(), acceptance); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("changed source returned %v", err)
	}
}

func TestAcceptImportRejectsArchivedDataset(t *testing.T) {
	f := newImportsFixture()
	f.datasets.value.State = DatasetArchived
	if _, err := f.imports.AcceptImport(context.Background(), validAcceptance()); !errors.Is(err, ErrDatasetArchived) {
		t.Fatalf("archived dataset accepted: %v", err)
	}
}

func TestAcceptImportReplayDoesNotTouchTheHub(t *testing.T) {
	f := newImportsFixture()
	acceptance := validAcceptance()
	intent := DatasetCommand{TenantID: acceptance.TenantID, ActorID: acceptance.ActorID, Kind: OpImportVersion, IdempotencyKey: acceptance.IdempotencyKey, DatasetID: acceptance.DatasetID, SelectionFingerprint: acceptance.SelectionFingerprint}
	f.commands.err = nil
	f.commands.receipt = CommandReceipt{
		TenantID: acceptance.TenantID, ReceiptID: "66666666-6666-4666-8666-666666666666", ActorID: acceptance.ActorID,
		Kind: OpImportVersion, IdempotencyKey: acceptance.IdempotencyKey,
		RequestSHA256: intent.Digest(), DatasetID: acceptance.DatasetID, TaskID: fixtureTask,
		ResponseStatus: ReceiptStatusAccepted,
	}
	f.datasets.readCalls = 0
	result, err := f.imports.AcceptImport(context.Background(), acceptance)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !result.Replayed {
		t.Fatal("replay was not reported as such")
	}
	if len(f.sources.paths) != 0 {
		t.Fatal("replay reached the hub")
	}
	if f.datasets.readCalls != 0 {
		t.Fatal("replay re-read the dataset")
	}
}

func TestAcceptImportReplayWithDifferentIntentConflicts(t *testing.T) {
	f := newImportsFixture()
	acceptance := validAcceptance()
	f.commands.err = nil
	f.commands.receipt = CommandReceipt{
		Kind: OpImportVersion, IdempotencyKey: acceptance.IdempotencyKey,
		RequestSHA256: strings.Repeat("c", 64), TaskID: fixtureTask,
	}
	if _, err := f.imports.AcceptImport(context.Background(), acceptance); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different intent reused the key: %v", err)
	}
}

func TestAcceptImportRequiresSizeAndLimit(t *testing.T) {
	for name, mutate := range map[string]func(*ImportAcceptance){
		"zero limit": func(a *ImportAcceptance) { a.AcceptedLimit = 0 },
		"zero size":  func(a *ImportAcceptance) { a.Selection.ExpectedSizeBytes = 0 },
		"bad key":    func(a *ImportAcceptance) { a.IdempotencyKey = "-bad" },
		"bad tenant": func(a *ImportAcceptance) { a.TenantID = "not-a-uuid" },
	} {
		f := newImportsFixture()
		acceptance := validAcceptance()
		mutate(&acceptance)
		if _, err := f.imports.AcceptImport(context.Background(), acceptance); !errors.Is(err, ErrDatasetInvalidMetadata) && !errors.Is(err, ErrSourceSizeUnknown) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

func TestCancelImportTransitionsLiveTaskOnly(t *testing.T) {
	f := newImportsFixture()
	f.tasks.value = DatasetImportTask{TenantID: fixtureTenant, TaskID: fixtureTask, DatasetID: fixtureDataset, State: TaskRunning}
	if _, err := f.imports.CancelImport(context.Background(), TaskCommand{TenantID: fixtureTenant, ActorID: "governance:user:7", DatasetID: fixtureDataset, TaskID: fixtureTask, IdempotencyKey: "cancel-1"}); err != nil {
		t.Fatalf("cancel live task: %v", err)
	}
	if !f.transitions.cancelled {
		t.Fatal("cancel did not reach the transition port")
	}
}

// Cancelling a task that already reached a terminal state is a stable no-op:
// the receipt says ALREADY_TERMINAL, nothing is undone, and no history event
// claims a cancellation that never happened.
func TestCancelImportIsNoopOnTerminalTask(t *testing.T) {
	for _, state := range []ImportTaskState{TaskSucceeded, TaskFailed, TaskCancelled} {
		f := newImportsFixture()
		f.tasks.value = DatasetImportTask{TenantID: fixtureTenant, TaskID: fixtureTask, DatasetID: fixtureDataset, State: state}
		receipt, err := f.imports.CancelImport(context.Background(), TaskCommand{TenantID: fixtureTenant, ActorID: "a:b:1", DatasetID: fixtureDataset, TaskID: fixtureTask, IdempotencyKey: "cancel-2"})
		if err != nil {
			t.Fatalf("%s cancel returned %v", state, err)
		}
		if receipt.Outcome != ReceiptOutcomeAlreadyTerminal {
			t.Fatalf("%s cancel outcome = %q, want %q", state, receipt.Outcome, ReceiptOutcomeAlreadyTerminal)
		}
		if f.transitions.cancelled {
			t.Fatalf("%s reached the transition port", state)
		}
		if len(f.operations.inserted) != 0 {
			t.Fatalf("%s wrote %d history events, want 0", state, len(f.operations.inserted))
		}
	}
}

func TestRetryImportRequiresRetryableFailure(t *testing.T) {
	f := newImportsFixture()
	f.tasks.value = DatasetImportTask{TenantID: fixtureTenant, TaskID: fixtureTask, DatasetID: fixtureDataset, State: TaskFailed, Retryable: true}
	if _, err := f.imports.RetryImport(context.Background(), TaskCommand{TenantID: fixtureTenant, ActorID: "a:b:1", DatasetID: fixtureDataset, TaskID: fixtureTask, IdempotencyKey: "retry-1"}); err != nil {
		t.Fatalf("retry retryable failure: %v", err)
	}
	if !f.transitions.retried {
		t.Fatal("retry did not reach the transition port")
	}

	for name, task := range map[string]DatasetImportTask{
		"not retryable": {State: TaskFailed, Retryable: false},
		"still running": {State: TaskRunning, Retryable: true},
		"cancelled":     {State: TaskCancelled, Retryable: true},
	} {
		g := newImportsFixture()
		task.TenantID, task.TaskID, task.DatasetID = fixtureTenant, fixtureTask, fixtureDataset
		g.tasks.value = task
		if _, err := g.imports.RetryImport(context.Background(), TaskCommand{TenantID: fixtureTenant, ActorID: "a:b:1", DatasetID: fixtureDataset, TaskID: fixtureTask, IdempotencyKey: "retry-2"}); !errors.Is(err, ErrImportStateConflict) {
			t.Fatalf("%s retry returned %v", name, err)
		}
	}
}

func TestRetryImportRejectsArchivedDataset(t *testing.T) {
	f := newImportsFixture()
	f.datasets.value.State = DatasetArchived
	f.tasks.value = DatasetImportTask{TenantID: fixtureTenant, TaskID: fixtureTask, DatasetID: fixtureDataset, State: TaskFailed, Retryable: true}
	if _, err := f.imports.RetryImport(context.Background(), TaskCommand{TenantID: fixtureTenant, ActorID: "a:b:1", DatasetID: fixtureDataset, TaskID: fixtureTask, IdempotencyKey: "retry-3"}); !errors.Is(err, ErrDatasetArchived) {
		t.Fatalf("archived dataset retried: %v", err)
	}
	if f.transitions.retried {
		t.Fatal("archived dataset reached the transition port")
	}
}

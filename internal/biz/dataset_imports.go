package biz

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// DatasetImports is the use case for accepting, cancelling, retrying and
// publishing imports. It owns ordering and legality; the data layer owns
// transactions and SQL.
type DatasetImports struct {
	datasets    DatasetRepository
	commands    DatasetCommandRepository
	tasks       DatasetImportTaskRepository
	versions    DatasetVersionRepository
	operations  DatasetOperationRepository
	acceptance  DatasetAcceptanceRepository
	publication DatasetPublicationRepository
	transitions DatasetTaskTransitionRepository
	targets     DatasetImportTargetReader
	sources     SourceProviderPort
	now         func() time.Time
	newID       func() (string, error)
}

// DatasetImportsDeps keeps the constructor readable; every port is required.
type DatasetImportsDeps struct {
	Datasets    DatasetRepository
	Commands    DatasetCommandRepository
	Tasks       DatasetImportTaskRepository
	Versions    DatasetVersionRepository
	Operations  DatasetOperationRepository
	Acceptance  DatasetAcceptanceRepository
	Publication DatasetPublicationRepository
	Transitions DatasetTaskTransitionRepository
	Targets     DatasetImportTargetReader
	Sources     SourceProviderPort
	Now         func() time.Time
	NewID       func() (string, error)
}

func NewDatasetImports(deps DatasetImportsDeps) (*DatasetImports, error) {
	if deps.Datasets == nil || deps.Commands == nil || deps.Tasks == nil || deps.Versions == nil ||
		deps.Operations == nil || deps.Acceptance == nil || deps.Publication == nil ||
		deps.Transitions == nil || deps.Targets == nil || deps.Sources == nil {
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
	return &DatasetImports{
		datasets: deps.Datasets, commands: deps.Commands, tasks: deps.Tasks, versions: deps.Versions,
		operations: deps.Operations, acceptance: deps.Acceptance, publication: deps.Publication,
		transitions: deps.Transitions, targets: deps.Targets, sources: deps.Sources,
		now: now, newID: newID,
	}, nil
}

func defaultUUID() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// ImportAcceptance is one authenticated import submission.
type ImportAcceptance struct {
	TenantID       string
	ActorID        string
	DatasetID      string
	IdempotencyKey string

	Selection            SourceSelection
	SelectionFingerprint string

	// AcceptedLimit is the current runtime.limits.max_content_bytes. It is
	// recorded as the accepted budget and never re-applied to this task.
	AcceptedLimit int64
}

// AcceptImport checks the source, re-resolves it, then delegates the one short
// transaction to the data layer. Everything expensive happens outside it.
func (imports *DatasetImports) AcceptImport(ctx context.Context, acceptance ImportAcceptance) (AcceptanceResult, error) {
	if imports == nil {
		return AcceptanceResult{}, ErrDatasetInvalidMetadata
	}
	if !CanonicalEnvironmentUUID(acceptance.TenantID) || !CanonicalEnvironmentUUID(acceptance.DatasetID) || acceptance.ActorID == "" {
		return AcceptanceResult{}, ErrDatasetInvalidMetadata
	}
	if !ValidIdempotencyKey(acceptance.IdempotencyKey) {
		return AcceptanceResult{}, ErrDatasetInvalidMetadata
	}
	if acceptance.AcceptedLimit <= 0 || acceptance.Selection.ExpectedSizeBytes <= 0 {
		return AcceptanceResult{}, ErrSourceSizeUnknown
	}

	// 1. Idempotent replay is answered before any hub access.
	command := DatasetCommand{
		TenantID: acceptance.TenantID, ActorID: acceptance.ActorID,
		Kind: OpImportVersion, IdempotencyKey: acceptance.IdempotencyKey,
		DatasetID: acceptance.DatasetID,
	}
	if receipt, err := imports.commands.FindReceipt(ctx, acceptance.TenantID, acceptance.ActorID, OpImportVersion, acceptance.IdempotencyKey); err == nil {
		if !receipt.SameIntent(command) {
			return AcceptanceResult{}, ErrIdempotencyConflict
		}
		task, err := imports.tasks.GetImportTask(ctx, acceptance.TenantID, receipt.TaskID)
		if err != nil {
			return AcceptanceResult{}, err
		}
		return AcceptanceResult{Receipt: receipt, Task: task, Replayed: true, ResponseStatus: receipt.ResponseStatus}, nil
	} else if !errors.Is(err, ErrDatasetNotFound) {
		return AcceptanceResult{}, err
	}

	// 2. The dataset must still be ACTIVE for a genuinely new acceptance.
	dataset, err := imports.datasets.GetDataset(ctx, acceptance.TenantID, acceptance.DatasetID)
	if err != nil {
		return AcceptanceResult{}, err
	}
	if dataset.State != DatasetActive {
		return AcceptanceResult{}, ErrDatasetArchived
	}

	// 3. Re-resolve the chosen file and compare with what the user confirmed.
	// A changed file requires a new confirmation, never a silent re-pin.
	fact, err := imports.sources.InspectFile(ctx, acceptance.Selection.Provider, acceptance.Selection.RepoID, acceptance.Selection.RequestedRevision, acceptance.Selection.FilePath)
	if err != nil {
		return AcceptanceResult{}, err
	}
	resolved := SourceSelection{
		Provider: fact.Provider, RepoID: fact.RepoID, RequestedRevision: fact.RequestedRevision,
		FilePath: fact.FilePath, LockKind: fact.LockKind, ResolvedRevision: fact.ResolvedRevision,
		ExpectedSizeBytes: fact.SizeBytes, ExpectedFileSHA256: fact.FileSHA256,
	}
	if resolved.SelectionFingerprint() != acceptance.SelectionFingerprint {
		return AcceptanceResult{}, ErrSourceChanged
	}
	snapshot := SourceSnapshot{SourceSelection: resolved, ResolvedAt: imports.now().UTC().Truncate(time.Microsecond)}
	if err := snapshot.Validate(); err != nil {
		return AcceptanceResult{}, err
	}

	// 4. The trusted target: namespace identity plus the input scope.
	target, err := imports.targets.ReadDatasetImportTarget(ctx, acceptance.TenantID)
	if err != nil {
		return AcceptanceResult{}, err
	}
	targetSnapshot, err := target.Canonical()
	if err != nil {
		return AcceptanceResult{}, err
	}
	targetSHA, err := target.Digest()
	if err != nil {
		return AcceptanceResult{}, err
	}

	// 5. Pre-generate the identities the transaction reserves.
	taskID, err := imports.newID()
	if err != nil {
		return AcceptanceResult{}, ErrPersistence
	}
	receiptID, err := imports.newID()
	if err != nil {
		return AcceptanceResult{}, ErrPersistence
	}
	inputVersionID, err := imports.newID()
	if err != nil {
		return AcceptanceResult{}, ErrPersistence
	}
	inputRequestID, err := imports.newID()
	if err != nil {
		return AcceptanceResult{}, ErrPersistence
	}
	acceptedAt := imports.now().UTC().Truncate(time.Microsecond)
	requestSHA := command.Digest()

	return imports.acceptance.AcceptImport(ctx, AcceptanceRequest{
		TenantID: acceptance.TenantID, ActorID: acceptance.ActorID, DatasetID: acceptance.DatasetID,
		IdempotencyKey: acceptance.IdempotencyKey, RequestSHA256: requestSHA,
		TaskID: taskID, ReceiptID: receiptID,
		ReservedInputVersionID: inputVersionID, InputRequestID: inputRequestID,
		CreatedBy: acceptance.ActorID, AcceptedAt: acceptedAt,
		SourceProvider: string(snapshot.Provider), SourceSnapshot: snapshot.CanonicalJSON(),
		SourceSnapshotSHA256: snapshot.SnapshotSHA256(),
		ExpectedSizeBytes:    snapshot.ExpectedSizeBytes, AcceptedLimitBytes: acceptance.AcceptedLimit,
		Target: target, TargetSnapshot: targetSnapshot, TargetSnapshotSHA256: targetSHA,
		AuthorizationScope:       NewImportAuthorizationScope(acceptance.TenantID, target.InputScopeSHA256),
		AuthorizationScopeSHA256: NewImportAuthorizationScopeSHA256(acceptance.TenantID, target.InputScopeSHA256),
		RecoveryPolicy:           DefaultRecoveryPolicy(),
	})
}

// CancelImport closes a live task. An already finished task is a stable no-op
// reported as a conflict so the caller reads the current state instead.
func (imports *DatasetImports) CancelImport(ctx context.Context, command TaskCommand) (CommandReceipt, error) {
	if imports == nil || !ValidIdempotencyKey(command.IdempotencyKey) {
		return CommandReceipt{}, ErrDatasetInvalidMetadata
	}
	intent := DatasetCommand{TenantID: command.TenantID, ActorID: command.ActorID, Kind: OpCancelImport, IdempotencyKey: command.IdempotencyKey, DatasetID: command.DatasetID, TaskID: command.TaskID}
	if receipt, err := imports.commands.FindReceipt(ctx, command.TenantID, command.ActorID, OpCancelImport, command.IdempotencyKey); err == nil {
		if !receipt.SameIntent(intent) {
			return CommandReceipt{}, ErrIdempotencyConflict
		}
		return receipt, nil
	} else if !errors.Is(err, ErrDatasetNotFound) {
		return CommandReceipt{}, err
	}
	task, err := imports.tasks.GetImportTask(ctx, command.TenantID, command.TaskID)
	if err != nil {
		return CommandReceipt{}, err
	}
	if !task.CanCancel() {
		// Publish already committed or the task is terminal: nothing to undo.
		return CommandReceipt{}, ErrImportStateConflict
	}
	if _, err := imports.transitions.RequestCancel(ctx, TaskCommandRequest{
		TenantID: command.TenantID, ActorID: command.ActorID, DatasetID: command.DatasetID,
		TaskID: command.TaskID, IdempotencyKey: command.IdempotencyKey, RequestSHA256: intent.Digest(),
	}); err != nil {
		return CommandReceipt{}, err
	}
	return imports.recordTransitionReceipt(ctx, command, intent, EventCancelRequest, "ACCEPTED")
}

// RetryImport requeues a retryable FAILED task. Source, size, accepted limit,
// reserved ids, version number and target stay exactly as accepted.
func (imports *DatasetImports) RetryImport(ctx context.Context, command TaskCommand) (CommandReceipt, error) {
	if imports == nil || !ValidIdempotencyKey(command.IdempotencyKey) {
		return CommandReceipt{}, ErrDatasetInvalidMetadata
	}
	intent := DatasetCommand{TenantID: command.TenantID, ActorID: command.ActorID, Kind: OpRetryImport, IdempotencyKey: command.IdempotencyKey, DatasetID: command.DatasetID, TaskID: command.TaskID}
	if receipt, err := imports.commands.FindReceipt(ctx, command.TenantID, command.ActorID, OpRetryImport, command.IdempotencyKey); err == nil {
		if !receipt.SameIntent(intent) {
			return CommandReceipt{}, ErrIdempotencyConflict
		}
		return receipt, nil
	} else if !errors.Is(err, ErrDatasetNotFound) {
		return CommandReceipt{}, err
	}
	dataset, err := imports.datasets.GetDataset(ctx, command.TenantID, command.DatasetID)
	if err != nil {
		return CommandReceipt{}, err
	}
	if dataset.State != DatasetActive {
		return CommandReceipt{}, ErrDatasetArchived
	}
	task, err := imports.tasks.GetImportTask(ctx, command.TenantID, command.TaskID)
	if err != nil {
		return CommandReceipt{}, err
	}
	if !task.CanRetry() {
		return CommandReceipt{}, ErrImportStateConflict
	}
	if _, err := imports.transitions.RetryTask(ctx, TaskCommandRequest{
		TenantID: command.TenantID, ActorID: command.ActorID, DatasetID: command.DatasetID,
		TaskID: command.TaskID, IdempotencyKey: command.IdempotencyKey, RequestSHA256: intent.Digest(),
	}); err != nil {
		return CommandReceipt{}, err
	}
	return imports.recordTransitionReceipt(ctx, command, intent, EventRetryAccepted, "ACCEPTED")
}

// TaskCommand is the common shape of cancel and retry.
type TaskCommand struct {
	TenantID       string
	ActorID        string
	DatasetID      string
	TaskID         string
	IdempotencyKey string
}

func (imports *DatasetImports) recordTransitionReceipt(ctx context.Context, command TaskCommand, intent DatasetCommand, event DatasetEventType, result string) (CommandReceipt, error) {
	receiptID, err := imports.newID()
	if err != nil {
		return CommandReceipt{}, ErrPersistence
	}
	receipt, err := imports.commands.InsertReceipt(ctx, CommandReceipt{
		TenantID: command.TenantID, ReceiptID: receiptID, ActorID: command.ActorID,
		Kind: intent.Kind, IdempotencyKey: command.IdempotencyKey, RequestSHA256: intent.Digest(),
		DatasetID: command.DatasetID, TaskID: command.TaskID,
		ResponseStatus: ReceiptStatusAccepted, AcceptedAt: imports.now().UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		return CommandReceipt{}, err
	}
	operationID, err := imports.newID()
	if err != nil {
		return CommandReceipt{}, ErrPersistence
	}
	if _, err := imports.operations.InsertOperation(ctx, DatasetOperation{
		TenantID: command.TenantID, OperationID: operationID, DatasetID: command.DatasetID,
		TaskID: command.TaskID, ReceiptID: receipt.ReceiptID, Actor: command.ActorID,
		EventType: event, Result: result,
	}); err != nil {
		return CommandReceipt{}, err
	}
	return receipt, nil
}

// PublishImport commits the READY input, the version row and the task success in
// one transaction. It is called by the worker after full verification.
func (imports *DatasetImports) PublishImport(ctx context.Context, request PublicationRequest) (PublicationResult, error) {
	if imports == nil {
		return PublicationResult{}, ErrDatasetInvalidMetadata
	}
	return imports.publication.PublishVersion(ctx, request)
}

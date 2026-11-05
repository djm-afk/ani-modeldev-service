package service

import (
	"context"
	"errors"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Dataset adapts the dataset use cases to the owner's gRPC contract. Every
// method, reads and writes alike, runs under the BFF's per-request current
// authorization and makes no authorization decisions of its own.
type Dataset struct {
	modeldevv1.UnimplementedModelDevDatasetServiceServer
	collections *biz.DatasetCollections
	imports     *biz.DatasetImports
	sources     biz.SourceProviderPort
	limits      func() (int64, error)
}

// NewDataset builds the handler. The content limit is read per acceptance so a
// new task always uses the current budget while old tasks keep theirs.
func NewDataset(collections *biz.DatasetCollections, imports *biz.DatasetImports, sources biz.SourceProviderPort, limits func() (int64, error)) *Dataset {
	return &Dataset{collections: collections, imports: imports, sources: sources, limits: limits}
}

func (service *Dataset) CreateDataset(ctx context.Context, request *modeldevv1.CreateDatasetRequest) (*modeldevv1.CreateDatasetResponse, error) {
	delivery, err := queryScope(ctx, modeldevv1.ModelDevDatasetService_CreateDataset_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil {
		return nil, datasetInvalid(delivery.RequestID)
	}
	dataset, receipt, err := service.collections.CreateDataset(ctx, biz.CreateDatasetInput{
		TenantID: delivery.TenantID, ActorID: delivery.Actor, IdempotencyKey: request.IdempotencyKey,
		Name: request.Name, Description: request.Description,
	})
	if err != nil {
		return nil, datasetError(err, delivery.RequestID)
	}
	return &modeldevv1.CreateDatasetResponse{Dataset: datasetView(dataset), Receipt: receiptView(receipt, dataset, biz.DatasetImportTask{})}, nil
}

func (service *Dataset) GetDataset(ctx context.Context, request *modeldevv1.GetDatasetRequest) (*modeldevv1.GetDatasetResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevDatasetService_GetDataset_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || !queryID(request.DatasetId) {
		return nil, datasetInvalid(scope.RequestID)
	}
	dataset, err := service.collections.GetDataset(ctx, scope.TenantID, request.DatasetId)
	if err != nil {
		return nil, datasetError(err, scope.RequestID)
	}
	return &modeldevv1.GetDatasetResponse{Dataset: datasetView(dataset)}, nil
}

func (service *Dataset) ListDatasets(ctx context.Context, request *modeldevv1.ListDatasetsRequest) (*modeldevv1.ListDatasetsResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevDatasetService_ListDatasets_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil {
		return nil, datasetInvalid(scope.RequestID)
	}
	page, pageSize, err := resolveModelDevPage(request.Page)
	if err != nil {
		return nil, err
	}
	pageValue, pageSizeValue := int32(page), int32(pageSize)
	filter := biz.DatasetListFilter{TenantID: scope.TenantID, Page: pageValue, PageSize: pageSizeValue}
	if request.State != nil {
		state := datasetStateName(*request.State)
		if state == "" {
			return nil, datasetInvalid(scope.RequestID)
		}
		filter.State = state
	}
	if request.Name != nil {
		filter.NameFilter = *request.Name
	}
	result, err := service.collections.ListDatasets(ctx, filter)
	if err != nil {
		return nil, datasetError(err, scope.RequestID)
	}
	response := &modeldevv1.ListDatasetsResponse{Total: result.Total}
	for _, item := range result.Items {
		response.Items = append(response.Items, datasetView(item))
	}
	return response, nil
}

func (service *Dataset) UpdateDataset(ctx context.Context, request *modeldevv1.UpdateDatasetRequest) (*modeldevv1.UpdateDatasetResponse, error) {
	delivery, err := queryScope(ctx, modeldevv1.ModelDevDatasetService_UpdateDataset_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || !queryID(request.DatasetId) || request.ExpectedRevision <= 0 || (request.Name == nil && request.Description == nil) {
		return nil, datasetInvalid(delivery.RequestID)
	}
	input := biz.UpdateDatasetInput{
		TenantID: delivery.TenantID, ActorID: delivery.Actor, DatasetID: request.DatasetId,
		IdempotencyKey: request.IdempotencyKey, ExpectedRevision: request.ExpectedRevision,
	}
	if request.Name != nil {
		input.Name, input.UpdateName = *request.Name, true
	}
	if request.Description != nil {
		input.Description, input.UpdateDescription = *request.Description, true
	}
	dataset, receipt, err := service.collections.UpdateDataset(ctx, input)
	if err != nil {
		return nil, datasetError(err, delivery.RequestID)
	}
	return &modeldevv1.UpdateDatasetResponse{Dataset: datasetView(dataset), Receipt: receiptView(receipt, dataset, biz.DatasetImportTask{})}, nil
}

func (service *Dataset) ArchiveDataset(ctx context.Context, request *modeldevv1.ArchiveDatasetRequest) (*modeldevv1.ArchiveDatasetResponse, error) {
	return service.lifecycle(ctx, request, true)
}

func (service *Dataset) RestoreDataset(ctx context.Context, request *modeldevv1.RestoreDatasetRequest) (*modeldevv1.RestoreDatasetResponse, error) {
	response, err := service.transition(ctx, request.GetDatasetId(), request.GetExpectedRevision(), request.GetIdempotencyKey(), false, modeldevv1.ModelDevDatasetService_RestoreDataset_FullMethodName)
	if err != nil {
		return nil, err
	}
	return &modeldevv1.RestoreDatasetResponse{Dataset: response.dataset, Receipt: response.receipt}, nil
}

// lifecycle and transition share the archive/restore shape but return the two
// distinct response messages the contract requires.
func (service *Dataset) lifecycle(ctx context.Context, request *modeldevv1.ArchiveDatasetRequest, archive bool) (*modeldevv1.ArchiveDatasetResponse, error) {
	response, err := service.transition(ctx, request.GetDatasetId(), request.GetExpectedRevision(), request.GetIdempotencyKey(), archive, modeldevv1.ModelDevDatasetService_ArchiveDataset_FullMethodName)
	if err != nil {
		return nil, err
	}
	return &modeldevv1.ArchiveDatasetResponse{Dataset: response.dataset, Receipt: response.receipt}, nil
}

type lifecycleResult struct {
	dataset *modeldevv1.DatasetView
	receipt *modeldevv1.CommandReceipt
}

func (service *Dataset) transition(ctx context.Context, datasetID string, expected int64, idempotencyKey string, archive bool, method string) (lifecycleResult, error) {
	delivery, err := queryScope(ctx, method)
	if err != nil {
		return lifecycleResult{}, err
	}
	if !queryID(datasetID) || expected <= 0 {
		return lifecycleResult{}, datasetInvalid(delivery.RequestID)
	}
	input := biz.DatasetLifecycleInput{TenantID: delivery.TenantID, ActorID: delivery.Actor, DatasetID: datasetID, IdempotencyKey: idempotencyKey, ExpectedRevision: expected}
	var dataset biz.Dataset
	var receipt biz.CommandReceipt
	if archive {
		dataset, receipt, err = service.collections.ArchiveDataset(ctx, input)
	} else {
		dataset, receipt, err = service.collections.RestoreDataset(ctx, input)
	}
	if err != nil {
		return lifecycleResult{}, datasetError(err, delivery.RequestID)
	}
	return lifecycleResult{dataset: datasetView(dataset), receipt: receiptView(receipt, dataset, biz.DatasetImportTask{})}, nil
}

// InspectDatasetSource answers the read-only source query. It returns bounded
// provider metadata only: a listing page or one resolved file fact plus the
// fingerprint the client must echo on submit. It never promises the content is
// acceptable — full validation happens during the import.
func (service *Dataset) InspectDatasetSource(ctx context.Context, request *modeldevv1.InspectDatasetSourceRequest) (*modeldevv1.InspectDatasetSourceResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevDatasetService_InspectDatasetSource_FullMethodName)
	if err != nil {
		return nil, err
	}
	invalid := datasetInvalid(scope.RequestID)
	if service.sources == nil || request == nil {
		return nil, invalid
	}
	switch query := request.Query.(type) {
	case *modeldevv1.InspectDatasetSourceRequest_ListFiles:
		list := query.ListFiles
		if list == nil || list.PageSize > 100 {
			return nil, invalid
		}
		provider := sourceProviderName(list.Provider)
		if provider == "" {
			return nil, invalid
		}
		page, err := service.sources.ListDirectory(ctx, provider, list.RepoId, list.Revision, list.DirectoryPath, list.PageToken, int32(list.PageSize))
		if err != nil {
			return nil, datasetError(err, scope.RequestID)
		}
		reply := &modeldevv1.InspectDatasetSourceResponse{FilesPage: &modeldevv1.DatasetSourceFilesPage{NextPageToken: page.NextPageToken, HasMore: page.HasMore}}
		for _, entry := range page.Entries {
			reply.FilesPage.Items = append(reply.FilesPage.Items, &modeldevv1.DatasetSourceFileView{Path: entry.Path, SizeBytes: entry.SizeBytes, Directory: entry.IsDirectory})
		}
		return reply, nil
	case *modeldevv1.InspectDatasetSourceRequest_InspectFile:
		file := query.InspectFile
		if file == nil {
			return nil, invalid
		}
		provider := sourceProviderName(file.Provider)
		if provider == "" {
			return nil, invalid
		}
		fact, err := service.sources.InspectFile(ctx, provider, file.RepoId, file.Revision, file.FilePath)
		if err != nil {
			return nil, datasetError(err, scope.RequestID)
		}
		selection := biz.SourceSelection{
			Provider: fact.Provider, RepoID: fact.RepoID, RequestedRevision: fact.RequestedRevision,
			FilePath: fact.FilePath, LockKind: fact.LockKind, ResolvedRevision: fact.ResolvedRevision,
			ExpectedSizeBytes: fact.SizeBytes, ExpectedFileSHA256: fact.FileSHA256,
		}
		return &modeldevv1.InspectDatasetSourceResponse{
			ResolvedRevision:     fact.ResolvedRevision,
			Selection:            sourceSelectionView(selection),
			SelectionFingerprint: selection.SelectionFingerprint(),
		}, nil
	}
	return nil, invalid
}

// sourceProviderName maps the contract enum to the domain value. An unknown or
// unspecified provider is rejected rather than defaulted to a real hub.
func sourceProviderName(provider modeldevv1.DatasetSourceProvider) biz.SourceProvider {
	switch provider {
	case modeldevv1.DatasetSourceProvider_DATASET_SOURCE_PROVIDER_HUGGINGFACE:
		return biz.ProviderHuggingFace
	case modeldevv1.DatasetSourceProvider_DATASET_SOURCE_PROVIDER_MODELSCOPE:
		return biz.ProviderModelScope
	}
	return ""
}

func sourceSelectionView(selection biz.SourceSelection) *modeldevv1.DatasetSourceSelection {
	view := &modeldevv1.DatasetSourceSelection{
		RepoId: selection.RepoID, RequestedRevision: selection.RequestedRevision,
		FilePath: selection.FilePath, ResolvedRevision: selection.ResolvedRevision,
		ExpectedSizeBytes: selection.ExpectedSizeBytes, ExpectedFileSha256: selection.ExpectedFileSHA256,
	}
	switch selection.Provider {
	case biz.ProviderHuggingFace:
		view.Provider = modeldevv1.DatasetSourceProvider_DATASET_SOURCE_PROVIDER_HUGGINGFACE
	case biz.ProviderModelScope:
		view.Provider = modeldevv1.DatasetSourceProvider_DATASET_SOURCE_PROVIDER_MODELSCOPE
	}
	switch selection.LockKind {
	case biz.LockImmutableRevision:
		view.LockKind = modeldevv1.DatasetSourceLockKind_DATASET_SOURCE_LOCK_KIND_IMMUTABLE_REVISION
	case biz.LockFileSHA256:
		view.LockKind = modeldevv1.DatasetSourceLockKind_DATASET_SOURCE_LOCK_KIND_FILE_SHA256
	}
	return view
}

func (service *Dataset) ImportDatasetVersion(ctx context.Context, request *modeldevv1.ImportDatasetVersionRequest) (*modeldevv1.ImportDatasetVersionResponse, error) {
	delivery, err := queryScope(ctx, modeldevv1.ModelDevDatasetService_ImportDatasetVersion_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || !queryID(request.DatasetId) || request.Source == nil || request.SelectionFingerprint == "" {
		return nil, datasetInvalid(delivery.RequestID)
	}
	selection := biz.SourceSelection{
		Provider: sourceProvider(request.Source), RepoID: request.Source.RepoId,
		RequestedRevision: request.Source.RequestedRevision, FilePath: request.Source.FilePath,
		LockKind: sourceLock(request.Source), ResolvedRevision: request.Source.ResolvedRevision,
		ExpectedSizeBytes: request.Source.ExpectedSizeBytes, ExpectedFileSHA256: request.Source.ExpectedFileSha256,
	}
	limit, err := service.limits()
	if err != nil {
		return nil, datasetError(biz.ErrPersistence, delivery.RequestID)
	}
	result, err := service.imports.AcceptImport(ctx, biz.ImportAcceptance{
		TenantID: delivery.TenantID, ActorID: delivery.Actor, DatasetID: request.DatasetId,
		IdempotencyKey: request.IdempotencyKey,
		Selection:      selection, SelectionFingerprint: request.SelectionFingerprint,
		AcceptedLimit: limit,
	})
	if err != nil {
		return nil, datasetError(err, delivery.RequestID)
	}
	return &modeldevv1.ImportDatasetVersionResponse{
		Receipt: receiptView(result.Receipt, biz.Dataset{}, result.Task),
		Task:    importTaskView(result.Task),
	}, nil
}

func (service *Dataset) CancelDatasetImport(ctx context.Context, request *modeldevv1.CancelDatasetImportRequest) (*modeldevv1.CancelDatasetImportResponse, error) {
	delivery, err := queryScope(ctx, modeldevv1.ModelDevDatasetService_CancelDatasetImport_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || !queryID(request.ImportTaskId) {
		return nil, datasetInvalid(delivery.RequestID)
	}
	task, err := service.collections.GetImportTask(ctx, delivery.TenantID, request.ImportTaskId)
	if err != nil {
		return nil, datasetError(err, delivery.RequestID)
	}
	receipt, err := service.imports.CancelImport(ctx, biz.TaskCommand{
		TenantID: delivery.TenantID, ActorID: delivery.Actor, DatasetID: task.DatasetID,
		TaskID: request.ImportTaskId, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return nil, datasetError(err, delivery.RequestID)
	}
	return &modeldevv1.CancelDatasetImportResponse{Receipt: receiptView(receipt, biz.Dataset{}, task)}, nil
}

func (service *Dataset) RetryDatasetImport(ctx context.Context, request *modeldevv1.RetryDatasetImportRequest) (*modeldevv1.RetryDatasetImportResponse, error) {
	delivery, err := queryScope(ctx, modeldevv1.ModelDevDatasetService_RetryDatasetImport_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || !queryID(request.ImportTaskId) {
		return nil, datasetInvalid(delivery.RequestID)
	}
	task, err := service.collections.GetImportTask(ctx, delivery.TenantID, request.ImportTaskId)
	if err != nil {
		return nil, datasetError(err, delivery.RequestID)
	}
	receipt, err := service.imports.RetryImport(ctx, biz.TaskCommand{
		TenantID: delivery.TenantID, ActorID: delivery.Actor, DatasetID: task.DatasetID,
		TaskID: request.ImportTaskId, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return nil, datasetError(err, delivery.RequestID)
	}
	return &modeldevv1.RetryDatasetImportResponse{Receipt: receiptView(receipt, biz.Dataset{}, task)}, nil
}

func (service *Dataset) ListDatasetVersions(ctx context.Context, request *modeldevv1.ListDatasetVersionsRequest) (*modeldevv1.ListDatasetVersionsResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevDatasetService_ListDatasetVersions_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || !queryID(request.DatasetId) {
		return nil, datasetInvalid(scope.RequestID)
	}
	page, pageSize, err := resolveModelDevPage(request.Page)
	if err != nil {
		return nil, err
	}
	pageValue, pageSizeValue := int32(page), int32(pageSize)
	result, err := service.collections.ListVersions(ctx, biz.DatasetVersionFilter{TenantID: scope.TenantID, DatasetID: request.DatasetId, Page: pageValue, PageSize: pageSizeValue})
	if err != nil {
		return nil, datasetError(err, scope.RequestID)
	}
	response := &modeldevv1.ListDatasetVersionsResponse{Total: result.Total}
	for _, item := range result.Items {
		response.Items = append(response.Items, versionView(item))
	}
	return response, nil
}

func (service *Dataset) ListDatasetImportTasks(ctx context.Context, request *modeldevv1.ListDatasetImportTasksRequest) (*modeldevv1.ListDatasetImportTasksResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevDatasetService_ListDatasetImportTasks_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || !queryID(request.DatasetId) {
		return nil, datasetInvalid(scope.RequestID)
	}
	page, pageSize, err := resolveModelDevPage(request.Page)
	if err != nil {
		return nil, err
	}
	pageValue, pageSizeValue := int32(page), int32(pageSize)
	filter := biz.DatasetImportFilter{TenantID: scope.TenantID, DatasetID: request.DatasetId, Page: pageValue, PageSize: pageSizeValue}
	if request.State != nil {
		state := importTaskStateName(*request.State)
		if state == "" {
			return nil, datasetInvalid(scope.RequestID)
		}
		filter.State = state
	}
	result, err := service.collections.ListImportTasks(ctx, filter)
	if err != nil {
		return nil, datasetError(err, scope.RequestID)
	}
	response := &modeldevv1.ListDatasetImportTasksResponse{Total: result.Total}
	for _, item := range result.Items {
		response.Items = append(response.Items, importTaskView(item))
	}
	return response, nil
}

func (service *Dataset) GetDatasetImportTask(ctx context.Context, request *modeldevv1.GetDatasetImportTaskRequest) (*modeldevv1.GetDatasetImportTaskResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevDatasetService_GetDatasetImportTask_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || !queryID(request.ImportTaskId) {
		return nil, datasetInvalid(scope.RequestID)
	}
	task, err := service.collections.GetImportTask(ctx, scope.TenantID, request.ImportTaskId)
	if err != nil {
		return nil, datasetError(err, scope.RequestID)
	}
	return &modeldevv1.GetDatasetImportTaskResponse{Task: importTaskView(task)}, nil
}

func (service *Dataset) ListDatasetOperations(ctx context.Context, request *modeldevv1.ListDatasetOperationsRequest) (*modeldevv1.ListDatasetOperationsResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevDatasetService_ListDatasetOperations_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || !queryID(request.DatasetId) {
		return nil, datasetInvalid(scope.RequestID)
	}
	page, pageSize, err := resolveModelDevPage(request.Page)
	if err != nil {
		return nil, err
	}
	pageValue, pageSizeValue := int32(page), int32(pageSize)
	filter := biz.DatasetOperationFilter{TenantID: scope.TenantID, DatasetID: request.DatasetId, Page: pageValue, PageSize: pageSizeValue}
	if request.EventType != nil {
		eventType := operationEventName(*request.EventType)
		if eventType == "" {
			return nil, datasetInvalid(scope.RequestID)
		}
		filter.EventType = eventType
	}
	if request.ImportTaskId != nil {
		if !queryID(*request.ImportTaskId) {
			return nil, datasetInvalid(scope.RequestID)
		}
		filter.TaskID = *request.ImportTaskId
	}
	result, err := service.collections.ListOperations(ctx, filter)
	if err != nil {
		return nil, datasetError(err, scope.RequestID)
	}
	response := &modeldevv1.ListDatasetOperationsResponse{Total: result.Total}
	for _, item := range result.Items {
		response.Items = append(response.Items, operationView(item))
	}
	return response, nil
}

// Every Dataset method, writes included, arrives as a current per-request
// authorization: the receiver verifies mTLS, the exact authorized method and
// tenant-all scope, then installs that access. The identity therefore comes from
// queryScope, exactly like EnsureTenantExecutionEnvironment, which is also a
// write. commandDelivery would look for a context the receiver never installs
// for these methods and reject every write as unauthenticated.

func datasetInvalid(requestID string) error {
	return commandError(codes.InvalidArgument, modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT, "invalid dataset request", requestID)
}

// datasetError maps the domain errors onto the public reason vocabulary. A
// missing reason here would collapse a precise business answer into the generic
// upstream failure, so every dataset error is listed explicitly.
func datasetError(err error, requestID string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	case errors.Is(err, biz.ErrDatasetNotFound):
		return commandError(codes.NotFound, modeldevv1.ErrorReason_ERROR_REASON_RESOURCE_NOT_FOUND, "dataset unavailable", requestID)
	case errors.Is(err, biz.ErrImportTaskNotFound):
		return commandError(codes.NotFound, modeldevv1.ErrorReason_ERROR_REASON_RESOURCE_NOT_FOUND, "import task unavailable", requestID)
	case errors.Is(err, biz.ErrDatasetNameConflict):
		return commandError(codes.AlreadyExists, modeldevv1.ErrorReason_ERROR_REASON_COMMAND_CONFLICT, "dataset name conflict", requestID)
	case errors.Is(err, biz.ErrRevisionConflict):
		return commandError(codes.Aborted, modeldevv1.ErrorReason_ERROR_REASON_COMMAND_CONFLICT, "dataset revision conflict", requestID)
	case errors.Is(err, biz.ErrIdempotencyConflict):
		return commandError(codes.AlreadyExists, modeldevv1.ErrorReason_ERROR_REASON_IDEMPOTENCY_CONFLICT, "idempotency key reused with a different intent", requestID)
	case errors.Is(err, biz.ErrDatasetArchived), errors.Is(err, biz.ErrImportStateConflict):
		return commandError(codes.FailedPrecondition, modeldevv1.ErrorReason_ERROR_REASON_INPUT_NOT_READY, "dataset lifecycle does not accept this action", requestID)
	case errors.Is(err, biz.ErrSourceNotFound):
		return commandError(codes.NotFound, modeldevv1.ErrorReason_ERROR_REASON_RESOURCE_NOT_FOUND, "source file not found", requestID)
	case errors.Is(err, biz.ErrSourceAuthRequired):
		return commandError(codes.PermissionDenied, modeldevv1.ErrorReason_ERROR_REASON_FORBIDDEN, "source requires authentication", requestID)
	case errors.Is(err, biz.ErrSourceNotPinnable), errors.Is(err, biz.ErrSourceSizeUnknown):
		return commandError(codes.FailedPrecondition, modeldevv1.ErrorReason_ERROR_REASON_INPUT_NOT_READY, "source cannot be pinned", requestID)
	case errors.Is(err, biz.ErrSourceUnavailable):
		return commandError(codes.Unavailable, modeldevv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE, "source hub unavailable", requestID)
	case errors.Is(err, biz.ErrSourceChanged):
		return commandError(codes.FailedPrecondition, modeldevv1.ErrorReason_ERROR_REASON_INPUT_NOT_READY, "source changed since confirmation", requestID)
	case errors.Is(err, biz.ErrImportEnvironmentMissing):
		return commandError(codes.FailedPrecondition, modeldevv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_READY, "import environment unavailable", requestID)
	case errors.Is(err, biz.ErrDatasetInvalidMetadata), errors.Is(err, biz.ErrSourceInvalidInput), errors.Is(err, biz.ErrSourceFileInvalid):
		return datasetInvalid(requestID)
	default:
		return commandError(codes.Unavailable, modeldevv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE, "dataset service unavailable", requestID)
	}
}

// ---- view projections ----

func datasetView(dataset biz.Dataset) *modeldevv1.DatasetView {
	view := &modeldevv1.DatasetView{
		DatasetId: dataset.DatasetID, Name: dataset.Name, Description: dataset.Description,
		Modality: "TEXT", SchemaKind: "ALPACA", State: datasetState(dataset.State),
		Revision: dataset.Revision, CreatedBy: dataset.CreatedBy,
		CreatedAt: timestamppb.New(dataset.CreatedAt), UpdatedAt: timestamppb.New(dataset.UpdatedAt),
	}
	return view
}

func receiptView(receipt biz.CommandReceipt, dataset biz.Dataset, task biz.DatasetImportTask) *modeldevv1.CommandReceipt {
	view := &modeldevv1.CommandReceipt{
		ReceiptId: receipt.ReceiptID, Operation: string(receipt.Kind), Outcome: "ACCEPTED",
		DatasetId: receipt.DatasetID, AcceptedAt: timestamppb.New(receipt.AcceptedAt),
	}
	if receipt.Kind == biz.OpImportVersion {
		view.Outcome = "ACCEPTED"
		view.ImportTaskId = task.TaskID
		view.ReservedInputVersionId = task.ReservedInputVersionID
		view.VersionNumber = task.VersionNumber
	}
	if dataset.Revision > 0 {
		view.Revision = dataset.Revision
	}
	return view
}

func importTaskView(task biz.DatasetImportTask) *modeldevv1.DatasetImportTaskView {
	view := &modeldevv1.DatasetImportTaskView{
		ImportTaskId: task.TaskID, DatasetId: task.DatasetID,
		ReservedInputVersionId: task.ReservedInputVersionID, VersionNumber: task.VersionNumber,
		State: importTaskState(task.State), Phase: importPhase(task.Phase),
		AcceptedAt: timestamppb.New(task.AcceptedAt), RecoveryGeneration: task.RecoveryGeneration,
	}
	if task.State == biz.TaskSucceeded {
		view.InputVersionId = task.ReservedInputVersionID
	}
	if task.FailureCode != "" {
		view.Failure = &modeldevv1.ErrorDetail{SafeMessage: task.FailureCode}
	}
	return view
}

func versionView(version biz.DatasetVersion) *modeldevv1.DatasetVersionView {
	return &modeldevv1.DatasetVersionView{
		InputVersionId: version.InputVersionID, DatasetId: version.DatasetID,
		ImportTaskId: version.ImportTaskID, VersionNumber: version.VersionNumber,
		PublishedAt: timestamppb.New(version.PublishedAt), PreviewAvailable: len(version.Preview) > 0,
	}
}

func operationView(operation biz.DatasetOperation) *modeldevv1.DatasetOperationView {
	return &modeldevv1.DatasetOperationView{
		OperationId: operation.OperationID, EventType: operationEvent(operation.EventType),
		Actor: operation.Actor, OccurredAt: timestamppb.New(operation.OccurredAt), Result: operation.Result,
		ImportTaskId: operation.TaskID, ImportAttemptId: operation.AttemptID, ReceiptId: operation.ReceiptID,
		Revision: operation.AfterRevision, RecoveryGeneration: operation.RecoveryGeneration,
		FailureCode: operation.FailureCode,
	}
}

func datasetState(state biz.DatasetState) modeldevv1.DatasetState {
	if state == biz.DatasetArchived {
		return modeldevv1.DatasetState_DATASET_STATE_ARCHIVED
	}
	return modeldevv1.DatasetState_DATASET_STATE_ACTIVE
}

func datasetStateName(state modeldevv1.DatasetState) string {
	switch state {
	case modeldevv1.DatasetState_DATASET_STATE_ACTIVE:
		return string(biz.DatasetActive)
	case modeldevv1.DatasetState_DATASET_STATE_ARCHIVED:
		return string(biz.DatasetArchived)
	}
	return ""
}

func importTaskState(state biz.ImportTaskState) modeldevv1.DatasetImportTaskState {
	switch state {
	case biz.TaskQueued:
		return modeldevv1.DatasetImportTaskState_DATASET_IMPORT_TASK_STATE_QUEUED
	case biz.TaskRunning:
		return modeldevv1.DatasetImportTaskState_DATASET_IMPORT_TASK_STATE_RUNNING
	case biz.TaskFailed:
		return modeldevv1.DatasetImportTaskState_DATASET_IMPORT_TASK_STATE_FAILED
	case biz.TaskCanceling:
		return modeldevv1.DatasetImportTaskState_DATASET_IMPORT_TASK_STATE_CANCELING
	case biz.TaskCancelled:
		return modeldevv1.DatasetImportTaskState_DATASET_IMPORT_TASK_STATE_CANCELLED
	case biz.TaskSucceeded:
		return modeldevv1.DatasetImportTaskState_DATASET_IMPORT_TASK_STATE_SUCCEEDED
	}
	return modeldevv1.DatasetImportTaskState_DATASET_IMPORT_TASK_STATE_UNSPECIFIED
}

func importTaskStateName(state modeldevv1.DatasetImportTaskState) string {
	switch state {
	case modeldevv1.DatasetImportTaskState_DATASET_IMPORT_TASK_STATE_QUEUED:
		return string(biz.TaskQueued)
	case modeldevv1.DatasetImportTaskState_DATASET_IMPORT_TASK_STATE_RUNNING:
		return string(biz.TaskRunning)
	case modeldevv1.DatasetImportTaskState_DATASET_IMPORT_TASK_STATE_FAILED:
		return string(biz.TaskFailed)
	case modeldevv1.DatasetImportTaskState_DATASET_IMPORT_TASK_STATE_CANCELING:
		return string(biz.TaskCanceling)
	case modeldevv1.DatasetImportTaskState_DATASET_IMPORT_TASK_STATE_CANCELLED:
		return string(biz.TaskCancelled)
	case modeldevv1.DatasetImportTaskState_DATASET_IMPORT_TASK_STATE_SUCCEEDED:
		return string(biz.TaskSucceeded)
	}
	return ""
}

func importPhase(phase biz.ImportPhase) modeldevv1.DatasetImportPhase {
	switch phase {
	case biz.PhaseWaiting:
		return modeldevv1.DatasetImportPhase_DATASET_IMPORT_PHASE_WAITING
	case biz.PhaseDownloading:
		return modeldevv1.DatasetImportPhase_DATASET_IMPORT_PHASE_DOWNLOADING
	case biz.PhaseUploading:
		return modeldevv1.DatasetImportPhase_DATASET_IMPORT_PHASE_UPLOADING
	case biz.PhaseVerifying:
		return modeldevv1.DatasetImportPhase_DATASET_IMPORT_PHASE_VERIFYING
	case biz.PhasePublishing:
		return modeldevv1.DatasetImportPhase_DATASET_IMPORT_PHASE_PUBLISHING
	case biz.PhaseStopping:
		return modeldevv1.DatasetImportPhase_DATASET_IMPORT_PHASE_STOPPING
	}
	return modeldevv1.DatasetImportPhase_DATASET_IMPORT_PHASE_UNSPECIFIED
}

func sourceProvider(selection *modeldevv1.DatasetSourceSelection) biz.SourceProvider {
	if selection == nil {
		return ""
	}
	switch selection.Provider {
	case modeldevv1.DatasetSourceProvider_DATASET_SOURCE_PROVIDER_HUGGINGFACE:
		return biz.ProviderHuggingFace
	case modeldevv1.DatasetSourceProvider_DATASET_SOURCE_PROVIDER_MODELSCOPE:
		return biz.ProviderModelScope
	}
	return ""
}

func sourceLock(selection *modeldevv1.DatasetSourceSelection) biz.SourceLockKind {
	if selection == nil {
		return ""
	}
	switch selection.LockKind {
	case modeldevv1.DatasetSourceLockKind_DATASET_SOURCE_LOCK_KIND_IMMUTABLE_REVISION:
		return biz.LockImmutableRevision
	case modeldevv1.DatasetSourceLockKind_DATASET_SOURCE_LOCK_KIND_FILE_SHA256:
		return biz.LockFileSHA256
	}
	return ""
}

func operationEvent(event biz.DatasetEventType) modeldevv1.DatasetOperationEventType {
	switch event {
	case biz.EventCreated:
		return modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_DATASET_CREATED
	case biz.EventUpdated:
		return modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_DATASET_UPDATED
	case biz.EventArchived:
		return modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_DATASET_ARCHIVED
	case biz.EventRestored:
		return modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_DATASET_RESTORED
	case biz.EventImportAccepted:
		return modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_IMPORT_ACCEPTED
	case biz.EventRetryAccepted:
		return modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_IMPORT_RETRY_ACCEPTED
	case biz.EventCancelRequest:
		return modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_IMPORT_CANCEL_REQUESTED
	case biz.EventImportOK:
		return modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_IMPORT_SUCCEEDED
	case biz.EventImportFailed:
		return modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_IMPORT_FAILED
	case biz.EventImportCanceled:
		return modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_IMPORT_CANCELLED
	}
	return modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_UNSPECIFIED
}

func operationEventName(event modeldevv1.DatasetOperationEventType) string {
	switch event {
	case modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_DATASET_CREATED:
		return string(biz.EventCreated)
	case modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_DATASET_UPDATED:
		return string(biz.EventUpdated)
	case modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_DATASET_ARCHIVED:
		return string(biz.EventArchived)
	case modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_DATASET_RESTORED:
		return string(biz.EventRestored)
	case modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_IMPORT_ACCEPTED:
		return string(biz.EventImportAccepted)
	case modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_IMPORT_RETRY_ACCEPTED:
		return string(biz.EventRetryAccepted)
	case modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_IMPORT_CANCEL_REQUESTED:
		return string(biz.EventCancelRequest)
	case modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_IMPORT_SUCCEEDED:
		return string(biz.EventImportOK)
	case modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_IMPORT_FAILED:
		return string(biz.EventImportFailed)
	case modeldevv1.DatasetOperationEventType_DATASET_OPERATION_EVENT_TYPE_IMPORT_CANCELLED:
		return string(biz.EventImportCanceled)
	}
	return ""
}

var _ = time.Second

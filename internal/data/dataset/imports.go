package dataset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	datasetsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/dataset/sqlc"
)

var (
	_ biz.DatasetImportTaskRepository     = (*Repository)(nil)
	_ biz.DatasetVersionRepository        = (*Repository)(nil)
	_ biz.DatasetAcceptanceRepository     = (*Repository)(nil)
	_ biz.DatasetPublicationRepository    = (*Repository)(nil)
	_ biz.DatasetTaskTransitionRepository = (*Repository)(nil)
)

func taskFromRow(row datasetsql.ModeldevDatasetImportTask) (biz.DatasetImportTask, error) {
	if !row.TenantID.Valid || !row.TaskID.Valid || !row.DatasetID.Valid || !row.AcceptedAt.Valid || !row.UpdatedAt.Valid {
		return biz.DatasetImportTask{}, biz.ErrPersistence
	}
	task := biz.DatasetImportTask{
		TenantID: row.TenantID.String(), TaskID: row.TaskID.String(), DatasetID: row.DatasetID.String(),
		AcceptanceReceiptID:    uuidText(row.AcceptanceReceiptID),
		ReservedInputVersionID: uuidText(row.ReservedInputVersionID), InputRequestID: uuidText(row.InputRequestID),
		VersionNumber: row.VersionNumber, CreatedBy: row.CreatedBy, AcceptedAt: row.AcceptedAt.Time.UTC(),
		SourceProvider: row.SourceProvider, SourceSnapshot: row.SourceSnapshot, SourceSnapshotSHA256: row.SourceSnapshotSha256,
		ExpectedSizeBytes: row.ExpectedSizeBytes, AcceptedLimitBytes: row.AcceptedLimitBytes,
		ClusterID: uuidText(row.ClusterID), NamespaceName: row.NamespaceName, NamespaceUID: uuidText(row.NamespaceUid),
		TargetSnapshot: row.TargetSnapshot, TargetSnapshotSHA256: row.TargetSnapshotSha256,
		StorageConnectionID: row.StorageConnectionID, Bucket: row.Bucket, ApprovedPrefix: row.ApprovedPrefix,
		InputScopeSHA256: row.InputScopeSha256, CredentialReference: row.CredentialReference,
		AuthorizationScope: row.AuthorizationScope, AuthorizationScopeSHA256: row.AuthorizationScopeSha256,
		State: biz.ImportTaskState(row.State), Phase: biz.ImportPhase(row.Phase),
		RecoveryGeneration: row.RecoveryGeneration, RecoveryPolicy: row.RecoveryPolicy,
		Retryable: row.Retryable, UpdatedAt: row.UpdatedAt.Time.UTC(),
		NextRunAt: row.NextRunAt.Time.UTC(), LeaseOwner: row.LeaseOwner.String, LeaseEpoch: row.LeaseEpoch,
		EnvironmentID:      uuidText(row.ObservedEnvironmentID),
		CurrentAttemptID:   uuidText(row.CurrentAttemptID),
		CandidateAttemptID: uuidText(row.CandidateAttemptID), CandidateObjectKey: row.CandidateObjectKey.String,
		CandidateVersionID: row.CandidateVersionID.String, CandidateSHA256: row.CandidateSha256.String,
		CandidateReportVersionID: row.CandidateReportVersionID.String, CandidateReportSHA256: row.CandidateReportSha256.String,
	}
	if row.CandidateSizeBytes.Valid {
		task.CandidateSizeBytes = row.CandidateSizeBytes.Int64
	}
	if row.CancelRequestedAt.Valid {
		at := row.CancelRequestedAt.Time.UTC()
		task.CancelRequestedAt = &at
	}
	if row.FailureCode.Valid {
		task.FailureCode = row.FailureCode.String
	}
	if row.FinishedAt.Valid {
		at := row.FinishedAt.Time.UTC()
		task.FinishedAt = &at
	}
	if row.PublishedAt.Valid {
		at := row.PublishedAt.Time.UTC()
		task.PublishedAt = &at
	}
	return task, nil
}

func uuidText(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return value.String()
}

func versionFromRow(row datasetsql.ModeldevDatasetVersionMetadatum) (biz.DatasetVersion, error) {
	if !row.TenantID.Valid || !row.InputVersionID.Valid || !row.DatasetID.Valid || !row.ImportTaskID.Valid || !row.PublishedAt.Valid {
		return biz.DatasetVersion{}, biz.ErrPersistence
	}
	return biz.DatasetVersion{
		TenantID: row.TenantID.String(), InputVersionID: row.InputVersionID.String(),
		DatasetID: row.DatasetID.String(), ImportTaskID: row.ImportTaskID.String(),
		VersionNumber: row.VersionNumber, Preview: row.Preview, PublishedAt: row.PublishedAt.Time.UTC(),
	}, nil
}

// ---- read paths ----

func (r *Repository) GetImportTask(ctx context.Context, tenantID, taskID string) (biz.DatasetImportTask, error) {
	if r == nil || r.pool == nil {
		return biz.DatasetImportTask{}, biz.ErrPersistence
	}
	tenant, err := databaseID(tenantID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	id, err := databaseID(taskID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	row, err := datasetsql.New(r.pool).GetImportTask(ctx, datasetsql.GetImportTaskParams{TenantID: tenant, TaskID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportTask{}, biz.ErrImportTaskNotFound
	}
	if err != nil {
		return biz.DatasetImportTask{}, biz.ErrPersistence
	}
	task, err := taskFromRow(row)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	if err := r.attachAttemptCounts(ctx, tenant, []biz.DatasetImportTask{task}, func(index int, count int64) {
		task.AttemptCount = count
	}); err != nil {
		return biz.DatasetImportTask{}, err
	}
	return task, nil
}

func (r *Repository) ListImportTasks(ctx context.Context, filter biz.DatasetImportFilter) (biz.DatasetImportTaskPage, error) {
	if r == nil || r.pool == nil {
		return biz.DatasetImportTaskPage{}, biz.ErrPersistence
	}
	tenant, datasetID, err := tenantAndDataset(filter.TenantID, filter.DatasetID)
	if err != nil {
		return biz.DatasetImportTaskPage{}, err
	}
	pageSize, offset := normalizePage(filter.Page, filter.PageSize)
	queries := datasetsql.New(r.pool)
	rows, err := queries.ListImportTasks(ctx, datasetsql.ListImportTasksParams{
		TenantID: tenant, DatasetID: datasetID, State: filter.State, PageSize: pageSize, PageOffset: offset,
	})
	if err != nil {
		return biz.DatasetImportTaskPage{}, biz.ErrPersistence
	}
	total, err := queries.CountImportTasks(ctx, datasetsql.CountImportTasksParams{TenantID: tenant, DatasetID: datasetID, State: filter.State})
	if err != nil {
		return biz.DatasetImportTaskPage{}, biz.ErrPersistence
	}
	page := biz.DatasetImportTaskPage{Total: total, Items: make([]biz.DatasetImportTask, 0, len(rows))}
	for _, row := range rows {
		item, err := taskFromRow(row)
		if err != nil {
			return biz.DatasetImportTaskPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := r.attachAttemptCounts(ctx, tenant, page.Items, func(index int, count int64) {
		page.Items[index].AttemptCount = count
	}); err != nil {
		return biz.DatasetImportTaskPage{}, err
	}
	return page, nil
}

// attachAttemptCounts fills the attempt counter for one page of tasks in a
// single grouped query. A task that never dispatched has no attempt row and
// keeps the zero value.
func (r *Repository) attachAttemptCounts(ctx context.Context, tenant pgtype.UUID, items []biz.DatasetImportTask, apply func(int, int64)) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]pgtype.UUID, 0, len(items))
	for _, item := range items {
		id, err := databaseID(item.TaskID)
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	rows, err := datasetsql.New(r.pool).TaskAttemptCounts(ctx, datasetsql.TaskAttemptCountsParams{TenantID: tenant, TaskIds: ids})
	if err != nil {
		return biz.ErrPersistence
	}
	counts := make(map[string]int64, len(rows))
	for _, row := range rows {
		counts[row.TaskID.String()] = row.AttemptCount
	}
	for position, item := range items {
		apply(position, counts[item.TaskID])
	}
	return nil
}

func (r *Repository) GetVersion(ctx context.Context, tenantID, inputVersionID string) (biz.DatasetVersion, error) {
	if r == nil || r.pool == nil {
		return biz.DatasetVersion{}, biz.ErrPersistence
	}
	tenant, err := databaseID(tenantID)
	if err != nil {
		return biz.DatasetVersion{}, err
	}
	id, err := databaseID(inputVersionID)
	if err != nil {
		return biz.DatasetVersion{}, err
	}
	row, err := datasetsql.New(r.pool).GetVersionMetadata(ctx, datasetsql.GetVersionMetadataParams{TenantID: tenant, InputVersionID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetVersion{}, biz.ErrDatasetNotFound
	}
	if err != nil {
		return biz.DatasetVersion{}, biz.ErrPersistence
	}
	return versionFromRow(row)
}

func (r *Repository) ListVersions(ctx context.Context, filter biz.DatasetVersionFilter) (biz.DatasetVersionPage, error) {
	if r == nil || r.pool == nil {
		return biz.DatasetVersionPage{}, biz.ErrPersistence
	}
	tenant, datasetID, err := tenantAndDataset(filter.TenantID, filter.DatasetID)
	if err != nil {
		return biz.DatasetVersionPage{}, err
	}
	pageSize, offset := normalizePage(filter.Page, filter.PageSize)
	queries := datasetsql.New(r.pool)
	rows, err := queries.ListVersionFacts(ctx, datasetsql.ListVersionFactsParams{TenantID: tenant, DatasetID: datasetID, PageSize: pageSize, PageOffset: offset})
	if err != nil {
		return biz.DatasetVersionPage{}, biz.ErrPersistence
	}
	total, err := queries.CountVersionMetadata(ctx, datasetsql.CountVersionMetadataParams{TenantID: tenant, DatasetID: datasetID})
	if err != nil {
		return biz.DatasetVersionPage{}, biz.ErrPersistence
	}
	page := biz.DatasetVersionPage{Total: total, Items: make([]biz.DatasetVersion, 0, len(rows))}
	for _, row := range rows {
		item, err := versionFactsFromRow(row)
		if err != nil {
			return biz.DatasetVersionPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}

// versionFactsFromRow projects one joined version row. The source snapshot is
// decoded with the same strict decoder the importer used, so a version view can
// never show a selection the acceptance path would have rejected.
func versionFactsFromRow(row datasetsql.ListVersionFactsRow) (biz.DatasetVersion, error) {
	if !row.InputVersionID.Valid || !row.DatasetID.Valid || !row.ImportTaskID.Valid || !row.PublishedAt.Valid {
		return biz.DatasetVersion{}, biz.ErrPersistence
	}
	if !row.InputRequestedAt.Valid || row.InputRequestedAt.InfinityModifier != pgtype.Finite {
		return biz.DatasetVersion{}, biz.ErrPersistence
	}
	snapshot, err := biz.DecodeSourceSnapshot(row.SourceSnapshot)
	if err != nil {
		return biz.DatasetVersion{}, err
	}
	value := biz.DatasetVersion{
		InputVersionID: row.InputVersionID.String(), DatasetID: row.DatasetID.String(),
		ImportTaskID: row.ImportTaskID.String(), VersionNumber: row.VersionNumber,
		PublishedAt: row.PublishedAt.Time.UTC(), Preview: row.Preview,
		Input: &biz.DatasetVersionInput{
			State: row.InputState, Format: row.InputFormat, SizeBytes: row.InputSizeBytes,
			SHA256: row.InputSha256, RequestedAt: row.InputRequestedAt.Time.UTC(),
		},
		Source: &snapshot,
	}
	if row.VerifiedRowCount.Valid {
		value.Input.RowCount = row.VerifiedRowCount.Int64
	}
	if row.VerifiedFeatureCount.Valid {
		value.Input.FeatureCount = int64(row.VerifiedFeatureCount.Int32)
	}
	return value, nil
}

// ---- acceptance ----

// AcceptImport runs the whole acceptance in one short transaction, in the lock
// order of design §5.5: idempotency receipt lookup → target commit lock →
// dataset row → input identity locks. No network call runs inside it.
func (r *Repository) AcceptImport(ctx context.Context, request biz.AcceptanceRequest) (biz.AcceptanceResult, error) {
	if r == nil || r.pool == nil {
		return biz.AcceptanceResult{}, fmt.Errorf("%w: repository not configured", biz.ErrPersistence)
	}
	tenant, datasetID, err := tenantAndDataset(request.TenantID, request.DatasetID)
	if err != nil {
		return biz.AcceptanceResult{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return biz.AcceptanceResult{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
	}
	defer tx.Rollback(ctx)
	queries := datasetsql.New(tx)

	// 1. An existing receipt wins before any other observation.
	existing, err := queries.GetCommandReceipt(ctx, datasetsql.GetCommandReceiptParams{
		TenantID: tenant, ActorID: request.ActorID, OperationKind: string(biz.OpImportVersion), IdempotencyKey: request.IdempotencyKey,
	})
	if err == nil {
		receipt, convErr := receiptFromRow(existing)
		if convErr != nil {
			return biz.AcceptanceResult{}, convErr
		}
		if receipt.RequestSHA256 != request.RequestSHA256 {
			return biz.AcceptanceResult{}, biz.ErrIdempotencyConflict
		}
		task, taskErr := taskForReplay(ctx, queries, tenant, receipt.TaskID)
		if taskErr != nil {
			return biz.AcceptanceResult{}, taskErr
		}
		return biz.AcceptanceResult{Receipt: receipt, Task: task, Replayed: true, ResponseStatus: receipt.ResponseStatus}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return biz.AcceptanceResult{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
	}

	// 2. Target commit lock: registry writes and acceptance share one boundary.
	if request.Target.ClusterID != "" {
		cluster, clusterErr := databaseID(request.Target.ClusterID)
		if clusterErr != nil {
			return biz.AcceptanceResult{}, clusterErr
		}
		if err := queries.LockTargetCommit(ctx, datasetsql.LockTargetCommitParams{TenantID: tenant, ClusterID: cluster}); err != nil {
			return biz.AcceptanceResult{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
		}
	}

	// 3. The dataset row is locked and must still be ACTIVE. The version number
	// is consumed here in the same statement's sequence.
	datasetRow, err := queries.LockDataset(ctx, datasetsql.LockDatasetParams{TenantID: tenant, DatasetID: datasetID})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.AcceptanceResult{}, biz.ErrDatasetNotFound
	}
	if err != nil {
		return biz.AcceptanceResult{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
	}
	if datasetRow.State != string(biz.DatasetActive) {
		return biz.AcceptanceResult{}, biz.ErrDatasetArchived
	}
	versionNumber, err := queries.TakeVersionNumber(ctx, datasetsql.TakeVersionNumberParams{TenantID: tenant, DatasetID: datasetID})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.AcceptanceResult{}, biz.ErrDatasetArchived
	}
	if err != nil {
		return biz.AcceptanceResult{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
	}

	// 4. Reserve the input identity before writing the task that claims it.
	inputVersion, inputErr := databaseID(request.ReservedInputVersionID)
	if inputErr != nil {
		return biz.AcceptanceResult{}, inputErr
	}
	inputRequest, inputErr := databaseID(request.InputRequestID)
	if inputErr != nil {
		return biz.AcceptanceResult{}, inputErr
	}
	if err := queries.LockInputIdentity(ctx, datasetsql.LockInputIdentityParams{TenantID: tenant, InputVersionID: inputVersion, RequestID: inputRequest}); err != nil {
		return biz.AcceptanceResult{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
	}

	// 5. The task, the receipt and the history event commit together. The task
	// is written first: an import receipt must carry task_id (both the table's
	// CHECK and the receipt→task foreign key say so), and the task row has to
	// exist before a receipt can legitimately name it.
	task, err := writeTask(ctx, queries, request, tenant, datasetID, versionNumber)
	if err != nil {
		return biz.AcceptanceResult{}, err
	}
	receipt, err := writeReceipt(ctx, queries, biz.CommandReceipt{
		TenantID: request.TenantID, ReceiptID: request.ReceiptID, ActorID: request.ActorID,
		Kind: biz.OpImportVersion, IdempotencyKey: request.IdempotencyKey,
		RequestSHA256: request.RequestSHA256, DatasetID: request.DatasetID, TaskID: request.TaskID,
		ResponseStatus: biz.ReceiptStatusAccepted, AcceptedAt: request.AcceptedAt,
	})
	if err != nil {
		return biz.AcceptanceResult{}, err
	}
	if err := writeOperation(ctx, queries, biz.DatasetOperation{
		TenantID: request.TenantID, OperationID: request.TaskID, DatasetID: request.DatasetID,
		TaskID: request.TaskID, ReceiptID: request.ReceiptID, Actor: request.ActorID,
		EventType: biz.EventImportAccepted, Result: "ACCEPTED",
		BeforeRevision: datasetRow.Revision, AfterRevision: datasetRow.Revision,
	}); err != nil {
		return biz.AcceptanceResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return biz.AcceptanceResult{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
	}
	return biz.AcceptanceResult{Receipt: receipt, Task: task, ResponseStatus: biz.ReceiptStatusAccepted}, nil
}

func taskForReplay(ctx context.Context, queries *datasetsql.Queries, tenant pgtype.UUID, taskID string) (biz.DatasetImportTask, error) {
	if taskID == "" {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: replay receipt carries no task id", biz.ErrPersistence)
	}
	id, err := databaseID(taskID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	row, err := queries.GetImportTask(ctx, datasetsql.GetImportTaskParams{TenantID: tenant, TaskID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportTask{}, biz.ErrImportTaskNotFound
	}
	if err != nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
	}
	return taskFromRow(row)
}

func writeTask(ctx context.Context, queries *datasetsql.Queries, request biz.AcceptanceRequest, tenant, datasetID pgtype.UUID, versionNumber int64) (biz.DatasetImportTask, error) {
	taskID, err := databaseID(request.TaskID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	receiptID, err := databaseID(request.ReceiptID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	inputVersion, err := databaseID(request.ReservedInputVersionID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	inputRequest, err := databaseID(request.InputRequestID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	namespaceUID, err := databaseID(request.Target.NamespaceUID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	clusterID, err := databaseID(request.Target.ClusterID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	row, err := queries.InsertImportTask(ctx, datasetsql.InsertImportTaskParams{
		TenantID: tenant, TaskID: taskID, DatasetID: datasetID,
		AcceptanceReceiptID: receiptID, ReservedInputVersionID: inputVersion,
		InputRequestID: inputRequest, VersionNumber: versionNumber,
		CreatedBy: request.CreatedBy, AcceptedAt: pgtype.Timestamptz{Time: request.AcceptedAt.UTC(), Valid: true},
		SourceProvider: request.SourceProvider, SourceSnapshot: request.SourceSnapshot,
		SourceSnapshotSha256: request.SourceSnapshotSHA256,
		ExpectedSizeBytes:    request.ExpectedSizeBytes, AcceptedLimitBytes: request.AcceptedLimitBytes,
		ClusterID: clusterID, NamespaceName: request.Target.NamespaceName, NamespaceUid: namespaceUID,
		TargetSnapshot: request.TargetSnapshot, TargetSnapshotSha256: request.TargetSnapshotSHA256,
		StorageConnectionID: request.Target.InputScope.StorageConnectionID,
		Bucket:              request.Target.InputScope.Bucket,
		ApprovedPrefix:      request.Target.InputScope.ApprovedPrefix,
		InputScopeSha256:    request.Target.InputScopeSHA256,
		CredentialReference: request.Target.InputScope.CredentialReference,
		AuthorizationScope:  request.AuthorizationScope, AuthorizationScopeSha256: request.AuthorizationScopeSHA256,
		RecoveryPolicy: request.RecoveryPolicy,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportTask{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: insert import task: %v", biz.ErrPersistence, err)
	}
	return taskFromRow(row)
}

func writeReceipt(ctx context.Context, queries *datasetsql.Queries, receipt biz.CommandReceipt) (biz.CommandReceipt, error) {
	tenant, err := databaseID(receipt.TenantID)
	if err != nil {
		return biz.CommandReceipt{}, err
	}
	receiptID, err := databaseID(receipt.ReceiptID)
	if err != nil {
		return biz.CommandReceipt{}, err
	}
	datasetID, err := databaseID(receipt.DatasetID)
	if err != nil {
		return biz.CommandReceipt{}, err
	}
	var taskID pgtype.UUID
	if receipt.TaskID != "" {
		if taskID, err = databaseID(receipt.TaskID); err != nil {
			return biz.CommandReceipt{}, err
		}
	}
	body, err := json.Marshal(map[string]string{"receipt_id": receipt.ReceiptID, "operation": string(receipt.Kind)})
	if err != nil {
		return biz.CommandReceipt{}, fmt.Errorf("%w: encode receipt body: %v", biz.ErrPersistence, err)
	}
	canonical, err := json.Marshal(map[string]string{"request_sha256": receipt.RequestSHA256})
	if err != nil {
		return biz.CommandReceipt{}, fmt.Errorf("%w: encode canonical request: %v", biz.ErrPersistence, err)
	}
	row, err := queries.InsertCommandReceipt(ctx, datasetsql.InsertCommandReceiptParams{
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
		return biz.CommandReceipt{}, fmt.Errorf("%w: insert command receipt: %v", biz.ErrPersistence, err)
	}
	return receiptFromRow(row)
}

func writeOperation(ctx context.Context, queries *datasetsql.Queries, operation biz.DatasetOperation) error {
	tenant, err := databaseID(operation.TenantID)
	if err != nil {
		return err
	}
	datasetID, err := databaseID(operation.DatasetID)
	if err != nil {
		return err
	}
	operationID, err := databaseID(operation.OperationID)
	if err != nil {
		return err
	}
	params := datasetsql.InsertDatasetOperationParams{
		TenantID: tenant, OperationID: operationID, DatasetID: datasetID,
		Actor: operation.Actor, EventType: string(operation.EventType), Result: operation.Result,
		BeforeState: operation.BeforeState, AfterState: operation.AfterState, Detail: []byte(`{}`),
	}
	if operation.TaskID != "" {
		if params.TaskID, err = databaseID(operation.TaskID); err != nil {
			return err
		}
	}
	if operation.ReceiptID != "" {
		if params.ReceiptID, err = databaseID(operation.ReceiptID); err != nil {
			return err
		}
	}
	if operation.BeforeRevision > 0 {
		params.BeforeRevision = pgtype.Int8{Int64: operation.BeforeRevision, Valid: true}
	}
	if operation.AfterRevision > 0 {
		params.AfterRevision = pgtype.Int8{Int64: operation.AfterRevision, Valid: true}
	}
	if _, err := queries.InsertDatasetOperation(ctx, params); err != nil {
		return fmt.Errorf("%w: insert dataset operation: %v", biz.ErrPersistence, err)
	}
	return nil
}

// ---- publication ----

// PublishVersion writes the READY input proof, the version metadata and the task
// transition in one transaction. It delegates the input write to the caller's
// closure so the input package keeps ownership of its own SQL while the dataset
// package keeps ownership of the boundary.
func (r *Repository) PublishVersion(ctx context.Context, request biz.PublicationRequest) (biz.PublicationResult, error) {
	if r == nil || r.pool == nil || r.inputs == nil {
		return biz.PublicationResult{}, fmt.Errorf("%w: repository or input publisher not configured", biz.ErrPersistence)
	}
	tenant, datasetID, err := tenantAndDataset(request.TenantID, request.DatasetID)
	if err != nil {
		return biz.PublicationResult{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return biz.PublicationResult{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
	}
	defer tx.Rollback(ctx)
	queries := datasetsql.New(tx)

	// Target commit lock first, then the dataset row, then the task.
	cluster, err := databaseID(request.Target.ClusterID)
	if err != nil {
		return biz.PublicationResult{}, err
	}
	if err := queries.LockTargetCommit(ctx, datasetsql.LockTargetCommitParams{TenantID: tenant, ClusterID: cluster}); err != nil {
		return biz.PublicationResult{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
	}
	datasetRow, err := queries.LockDataset(ctx, datasetsql.LockDatasetParams{TenantID: tenant, DatasetID: datasetID})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.PublicationResult{}, biz.ErrDatasetNotFound
	}
	if err != nil {
		return biz.PublicationResult{}, fmt.Errorf("%w: lock dataset: %v", biz.ErrPersistence, err)
	}
	if datasetRow.State != string(biz.DatasetActive) {
		return biz.PublicationResult{}, biz.ErrDatasetArchived
	}
	taskID, err := databaseID(request.TaskID)
	if err != nil {
		return biz.PublicationResult{}, err
	}
	taskRow, err := queries.LockImportTask(ctx, datasetsql.LockImportTaskParams{TenantID: tenant, TaskID: taskID})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.PublicationResult{}, biz.ErrImportTaskNotFound
	}
	if err != nil {
		return biz.PublicationResult{}, fmt.Errorf("%w: lock import task: %v", biz.ErrPersistence, err)
	}
	if taskRow.CancelRequestedAt.Valid {
		return biz.PublicationResult{}, biz.ErrImportStateConflict
	}
	if taskRow.State == string(biz.TaskSucceeded) {
		// Publish already committed: return the original facts.
		return replayPublication(ctx, queries, tenant, datasetID, taskRow)
	}
	if taskRow.State != string(biz.TaskRunning) {
		return biz.PublicationResult{}, biz.ErrImportStateConflict
	}
	inputVersion, err := r.inputs.PublishVerifiedAlpacaInTx(ctx, tx, request.Import, request.Verified)
	if err != nil {
		return biz.PublicationResult{}, err
	}
	versionRow, err := queries.InsertVersionMetadata(ctx, datasetsql.InsertVersionMetadataParams{
		TenantID: tenant, InputVersionID: mustUUID(inputVersion.Import.InputVersionID), DatasetID: datasetID,
		ImportTaskID: taskID, VersionNumber: request.VersionNumber,
		Preview: request.Preview, PublishedAt: pgtype.Timestamptz{Time: request.PublishedAt.UTC(), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.PublicationResult{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.PublicationResult{}, fmt.Errorf("%w: insert version metadata: %v", biz.ErrPersistence, err)
	}
	succeeded, err := queries.MarkTaskSucceeded(ctx, datasetsql.MarkTaskSucceededParams{TenantID: tenant, TaskID: taskID})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.PublicationResult{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.PublicationResult{}, fmt.Errorf("%w: mark task succeeded: %v", biz.ErrPersistence, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return biz.PublicationResult{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
	}
	task, err := taskFromRow(succeeded)
	if err != nil {
		return biz.PublicationResult{}, err
	}
	version, err := versionFromRow(versionRow)
	if err != nil {
		return biz.PublicationResult{}, err
	}
	return biz.PublicationResult{InputVersion: inputVersion, Version: version, Task: task}, nil
}

func replayPublication(ctx context.Context, queries *datasetsql.Queries, tenant, datasetID pgtype.UUID, taskRow datasetsql.ModeldevDatasetImportTask) (biz.PublicationResult, error) {
	row, err := queries.GetVersionMetadata(ctx, datasetsql.GetVersionMetadataParams{
		TenantID: tenant, InputVersionID: mustUUID(uuidText(taskRow.ReservedInputVersionID)),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.PublicationResult{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.PublicationResult{}, fmt.Errorf("%w: read version metadata for replay: %v", biz.ErrPersistence, err)
	}
	version, err := versionFromRow(row)
	if err != nil {
		return biz.PublicationResult{}, err
	}
	task, err := taskFromRow(taskRow)
	if err != nil {
		return biz.PublicationResult{}, err
	}
	return biz.PublicationResult{Version: version, Task: task, Replayed: true}, nil
}

func mustUUID(value string) pgtype.UUID {
	parsed, err := databaseID(value)
	if err != nil {
		return pgtype.UUID{}
	}
	return parsed
}

// ---- task transitions ----

func (r *Repository) RequestCancel(ctx context.Context, request biz.TaskCommandRequest) (biz.DatasetImportTask, error) {
	return r.transitionTask(ctx, request, true)
}

func (r *Repository) RetryTask(ctx context.Context, request biz.TaskCommandRequest) (biz.DatasetImportTask, error) {
	return r.transitionTask(ctx, request, false)
}

func (r *Repository) transitionTask(ctx context.Context, request biz.TaskCommandRequest, cancel bool) (biz.DatasetImportTask, error) {
	if r == nil || r.pool == nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: repository not configured", biz.ErrPersistence)
	}
	tenant, _, err := tenantAndDataset(request.TenantID, request.DatasetID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	taskID, err := databaseID(request.TaskID)
	if err != nil {
		return biz.DatasetImportTask{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
	}
	defer tx.Rollback(ctx)
	queries := datasetsql.New(tx)
	var row datasetsql.ModeldevDatasetImportTask
	if cancel {
		row, err = queries.RequestCancelTask(ctx, datasetsql.RequestCancelTaskParams{TenantID: tenant, TaskID: taskID})
	} else {
		row, err = queries.RequeueRetryableTask(ctx, datasetsql.RequeueRetryableTaskParams{TenantID: tenant, TaskID: taskID})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.DatasetImportTask{}, biz.ErrImportStateConflict
	}
	if err != nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: transition import task: %v", biz.ErrPersistence, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return biz.DatasetImportTask{}, fmt.Errorf("%w: %v", biz.ErrPersistence, err)
	}
	return taskFromRow(row)
}

// ReserveImport writes the frozen input row the publication transaction locks.
func (r *Repository) ReserveImport(ctx context.Context, request biz.InputImport) (biz.InputVersion, error) {
	if r == nil || r.inputs == nil {
		return biz.InputVersion{}, fmt.Errorf("%w: input publisher not configured", biz.ErrPersistence)
	}
	return r.inputs.FreezeImport(ctx, request)
}

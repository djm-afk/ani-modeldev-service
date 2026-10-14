package service

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Query struct {
	modeldevv1.UnimplementedModelDevQueryServiceServer
	repository biz.ArtifactQueryRepository
	signer     biz.ArtifactSigner
	logs       biz.TrainingLogReader
	materials  *biz.ManagedMaterials
	cursors    *biz.CursorCodec
}

func (query *Query) WithMaterials(materials *biz.ManagedMaterials) *Query {
	query.materials = materials
	return query
}

// WithCursorCodec wires the list-cursor signer. List pagination is fail-closed
// without it, so every assembled query listener supplies one.
func (query *Query) WithCursorCodec(codec *biz.CursorCodec) *Query {
	query.cursors = codec
	return query
}

func NewQuery(repository biz.ArtifactQueryRepository, signer biz.ArtifactSigner, logs ...biz.TrainingLogReader) *Query {
	query := &Query{repository: repository, signer: signer}
	if len(logs) == 1 {
		query.logs = logs[0]
	}
	return query
}

// listCursor verifies an inbound cursor against its binding and returns the raw
// boundary key to resume from. An empty token is the first page. A missing
// codec or any integrity/binding failure is rejected rather than treated as a
// fresh first page.
func (query *Query) listCursor(binding biz.CursorBinding, token string) (string, error) {
	if query == nil || query.cursors == nil {
		return "", biz.ErrInvalidCursor
	}
	if token == "" {
		return "", nil
	}
	return query.cursors.Decode(token, binding)
}

// nextListCursor signs the raw boundary key of the next page. An empty key
// yields an empty token (last page).
func (query *Query) nextListCursor(binding biz.CursorBinding, last string) (string, error) {
	if last == "" {
		return "", nil
	}
	if query == nil || query.cursors == nil {
		return "", biz.ErrInvalidCursor
	}
	return query.cursors.Encode(binding, last)
}

type governanceQueryKey struct{}
type governanceQuery struct {
	delivery GovernanceDelivery
	method   string
}

// WithVerifiedGovernanceQuery is called by the mTLS receiver only after checking
// the BFF's per-request current authorization delegation for the exact method.
func WithVerifiedGovernanceQuery(ctx context.Context, delivery GovernanceDelivery, method string) context.Context {
	return context.WithValue(ctx, governanceQueryKey{}, governanceQuery{delivery: delivery, method: method})
}

func queryScope(ctx context.Context, method string) (GovernanceDelivery, error) {
	access, ok := ctx.Value(governanceQueryKey{}).(governanceQuery)
	if !ok || access.method != method {
		return GovernanceDelivery{}, status.Error(codes.Unauthenticated, "current query authorization required")
	}
	if err := ctx.Err(); err != nil {
		return GovernanceDelivery{}, status.FromContextError(err).Err()
	}
	return access.delivery, nil
}

func queryID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func queryError(err error, requestID string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	if errors.Is(err, biz.ErrExecutionNotFound) || errors.Is(err, biz.ErrArtifactNotFound) || errors.Is(err, biz.ErrTrainingLogNotFound) {
		return commandError(codes.NotFound, modeldevv1.ErrorReason_ERROR_REASON_RESOURCE_NOT_FOUND, "resource unavailable", requestID)
	}
	return commandError(codes.Unavailable, modeldevv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE, "query unavailable", requestID)
}

func (query *Query) GetExecutionLogs(ctx context.Context, request *modeldevv1.GetExecutionLogsRequest) (*modeldevv1.GetExecutionLogsResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevQueryService_GetExecutionLogs_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || !queryID(request.ExecutionId) || len(request.ProtoReflect().GetUnknown()) != 0 ||
		(request.LogId != nil && !queryID(*request.LogId)) || request.TailLines > 1000 || request.MaxBytes > 65536 {
		return nil, status.Error(codes.InvalidArgument, "invalid training log query")
	}
	if query == nil || query.repository == nil {
		return nil, queryError(biz.ErrPersistence, scope.RequestID)
	}
	record, err := query.repository.GetQueryRecord(ctx, scope.TenantID, request.ExecutionId)
	if err != nil {
		return nil, queryError(err, scope.RequestID)
	}
	source, err := biz.TrainingLogSourceFor(record, request.GetLogId())
	if err != nil {
		return nil, queryError(err, scope.RequestID)
	}
	if query.logs == nil {
		return nil, queryError(biz.ErrTrainingLogsUnavailable, scope.RequestID)
	}
	options := biz.TrainingLogOptions{TailLines: request.TailLines, MaxBytes: request.MaxBytes}
	if options.TailLines == 0 {
		options.TailLines = 200
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = 16384
	}
	result, err := query.logs.ReadTrainingLogs(ctx, source, options)
	if err != nil {
		return nil, queryError(err, scope.RequestID)
	}
	response := &modeldevv1.GetExecutionLogsResponse{Source: &trainingv1.LogRef{LogId: source.LogID, ResourceUid: source.PodUID, ContainerName: source.ContainerName}, Truncated: result.Truncated, ObservedAt: timestamppb.New(result.ObservedAt)}
	for _, line := range result.Lines {
		response.Lines = append(response.Lines, &modeldevv1.LogLine{Timestamp: timestamppb.New(line.Timestamp), Text: line.Text})
	}
	return response, nil
}

func (query *Query) GetExecution(ctx context.Context, request *modeldevv1.GetExecutionRequest) (*modeldevv1.GetExecutionResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevQueryService_GetExecution_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || !queryID(request.ExecutionId) || len(request.ProtoReflect().GetUnknown()) != 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid execution query")
	}
	if query == nil || query.repository == nil {
		return nil, queryError(biz.ErrDownloadUnavailable, scope.RequestID)
	}
	record, err := query.repository.GetQueryRecord(ctx, scope.TenantID, request.ExecutionId)
	if err != nil {
		return nil, queryError(err, scope.RequestID)
	}
	view, err := executionView(record)
	if err != nil {
		return nil, queryError(err, scope.RequestID)
	}
	return &modeldevv1.GetExecutionResponse{Execution: view}, nil
}

func executionView(record biz.QueryRecord) (*modeldevv1.ExecutionView, error) {
	value := record.Execution
	states, valid := encodeRuntimeStates(value.States)
	if !valid {
		return nil, biz.ErrPersistence
	}
	view := &modeldevv1.ExecutionView{Identity: &trainingv1.ExecutionIdentity{OperationId: value.OperationID, ExecutionId: value.ExecutionID, ExecutionSpecHash: value.SpecHash}, Name: value.Intent.Name, Kind: trainingv1.ExecutionKind_EXECUTION_KIND_GENERAL_TRAINING, PresetId: value.Snapshot.Release.PresetID, ReleaseId: value.Snapshot.Release.ReleaseID, InputVersionId: value.Snapshot.Input.InputVersionID, ImageVersionId: value.Snapshot.Program.ImageVersionID, States: states, CloseGeneration: record.Runtime.CloseGeneration, AcceptedAt: timestamppb.New(value.AcceptedAt), DeadlineAt: timestamppb.New(value.Snapshot.DeadlineAt), ObservedAt: timestamppb.New(value.AcceptedAt)}
	if value.Close != nil {
		view.StopRequested = value.Close.Reason == biz.CloseReasonUserStop
	}
	if record.Runtime.Observation != nil {
		view.ObservedAt = timestamppb.New(record.Runtime.Observation.ObservedAt)
	}
	if record.Runtime.TrainingRejection != nil {
		view.ObservedAt = timestamppb.New(record.Runtime.TrainingRejection.ObservedAt)
	}
	if record.Runtime.Workspace != nil {
		view.CurrentStep = modeldevv1.PipelineStep_PIPELINE_STEP_PREPARE
	}
	if record.Runtime.Training != nil {
		view.CurrentStep = modeldevv1.PipelineStep_PIPELINE_STEP_TRAIN_WAIT
	}
	if record.Runtime.Publication != nil {
		view.CurrentStep = modeldevv1.PipelineStep_PIPELINE_STEP_PUBLISH
		view.ObservedAt = timestamppb.New(record.Runtime.Publication.VerifiedAt)
	}
	if record.Runtime.ClosedAt != nil {
		view.CurrentStep = modeldevv1.PipelineStep_PIPELINE_STEP_CLOSE
		view.ObservedAt = timestamppb.New(*record.Runtime.ClosedAt)
	}
	return view, nil
}

func (query *Query) ListExecutions(ctx context.Context, request *modeldevv1.ListExecutionsRequest) (*modeldevv1.ListExecutionsResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevQueryService_ListExecutions_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || len(request.ProtoReflect().GetUnknown()) != 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid execution query")
	}
	filter := biz.ExecutionQuery{Limit: 20}
	cursorToken := ""
	if page := request.Page; page != nil {
		if len(page.ProtoReflect().GetUnknown()) != 0 || page.Limit > 100 || len(page.Cursor) > 2048 {
			return nil, status.Error(codes.InvalidArgument, "invalid execution page")
		}
		if page.Limit > 0 {
			filter.Limit = int(page.Limit)
		}
		cursorToken = page.Cursor
	}
	if request.ComputeState != nil {
		name, known := modeldevv1.ComputeState_name[int32(*request.ComputeState)]
		if !known || *request.ComputeState == 0 {
			return nil, status.Error(codes.InvalidArgument, "invalid compute filter")
		}
		filter.Compute = biz.ComputeState(strings.TrimPrefix(name, "COMPUTE_STATE_"))
	}
	if request.DeliveryState != nil {
		name, known := modeldevv1.DeliveryState_name[int32(*request.DeliveryState)]
		if !known || *request.DeliveryState == 0 {
			return nil, status.Error(codes.InvalidArgument, "invalid delivery filter")
		}
		filter.Delivery = biz.DeliveryState(strings.TrimPrefix(name, "DELIVERY_STATE_"))
	}
	if request.CloseState != nil {
		name, known := modeldevv1.CloseState_name[int32(*request.CloseState)]
		if !known || *request.CloseState == 0 {
			return nil, status.Error(codes.InvalidArgument, "invalid close filter")
		}
		filter.Close = biz.CloseState(strings.TrimPrefix(name, "CLOSE_STATE_"))
	}
	if query == nil || query.repository == nil {
		return nil, queryError(biz.ErrPersistence, scope.RequestID)
	}
	binding := biz.CursorBinding{Kind: biz.CursorKindExecutions, TenantID: scope.TenantID,
		FilterKey: biz.CursorFilterKey(string(filter.Compute), string(filter.Delivery), string(filter.Close))}
	filter.AfterID, err = query.listCursor(binding, cursorToken)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid execution cursor")
	}
	records, next, err := query.repository.ListQueryRecords(ctx, scope.TenantID, filter)
	if err != nil {
		return nil, queryError(err, scope.RequestID)
	}
	total, err := query.repository.CountQueryRecords(ctx, scope.TenantID, filter)
	if err != nil {
		return nil, queryError(err, scope.RequestID)
	}
	response := &modeldevv1.ListExecutionsResponse{Total: total}
	response.NextCursor, err = query.nextListCursor(binding, next)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid execution cursor")
	}
	for _, record := range records {
		view, err := executionView(record)
		if err != nil {
			return nil, queryError(err, scope.RequestID)
		}
		response.Executions = append(response.Executions, view)
	}
	return response, nil
}

func artifactView(record biz.QueryRecord, file biz.PublishedRuntimeFile) *modeldevv1.ArtifactView {
	return &modeldevv1.ArtifactView{ArtifactId: file.ArtifactID, ExecutionId: record.Execution.ExecutionID, Filename: file.File.RelativePath, Role: trainingv1.FileRole(trainingv1.FileRole_value["FILE_ROLE_"+file.File.Role]), SizeBytes: file.File.SizeBytes, Sha256: file.File.SHA256, DeliveryState: modeldevv1.DeliveryState_DELIVERY_STATE_PUBLISHED, VerifiedAt: timestamppb.New(record.Runtime.Publication.VerifiedAt)}
}

func (query *Query) ListExecutionArtifacts(ctx context.Context, request *modeldevv1.ListExecutionArtifactsRequest) (*modeldevv1.ListExecutionArtifactsResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevQueryService_ListExecutionArtifacts_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || !queryID(request.ExecutionId) || len(request.ProtoReflect().GetUnknown()) != 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid artifact query")
	}
	size := uint32(20)
	cursorToken := ""
	if page := request.Page; page != nil {
		if len(page.ProtoReflect().GetUnknown()) != 0 || page.Limit > 100 || len(page.Cursor) > 2048 {
			return nil, status.Error(codes.InvalidArgument, "invalid artifact page")
		}
		if page.Limit > 0 {
			size = page.Limit
		}
		cursorToken = page.Cursor
	}
	if query == nil || query.repository == nil {
		return nil, queryError(biz.ErrDownloadUnavailable, scope.RequestID)
	}
	binding := biz.CursorBinding{Kind: biz.CursorKindArtifacts, TenantID: scope.TenantID,
		FilterKey: biz.CursorFilterKey(request.ExecutionId)}
	cursor, err := query.listCursor(binding, cursorToken)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid artifact cursor")
	}
	record, err := query.repository.GetQueryRecord(ctx, scope.TenantID, request.ExecutionId)
	if err != nil {
		return nil, queryError(err, scope.RequestID)
	}
	response := &modeldevv1.ListExecutionArtifactsResponse{}
	if record.Runtime.Publication == nil {
		return response, nil
	}
	files := append([]biz.PublishedRuntimeFile(nil), record.Runtime.Publication.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].ArtifactID < files[j].ArtifactID })
	// total is every published artifact of this execution, independent of the
	// pagination window.
	response.Total = uint64(len(files))
	for _, file := range files {
		if file.ArtifactID <= cursor {
			continue
		}
		if len(response.Artifacts) == int(size) {
			response.NextCursor, err = query.nextListCursor(binding, response.Artifacts[len(response.Artifacts)-1].ArtifactId)
			if err != nil {
				return nil, status.Error(codes.InvalidArgument, "invalid artifact cursor")
			}
			break
		}
		response.Artifacts = append(response.Artifacts, artifactView(record, file))
	}
	return response, nil
}

func (query *Query) AuthorizeArtifactDownload(ctx context.Context, request *modeldevv1.AuthorizeArtifactDownloadRequest) (*modeldevv1.AuthorizeArtifactDownloadResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevQueryService_AuthorizeArtifactDownload_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || !queryID(request.ArtifactId) || len(request.ProtoReflect().GetUnknown()) != 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid artifact query")
	}
	if query == nil || query.repository == nil || query.signer == nil {
		return nil, queryError(biz.ErrDownloadUnavailable, scope.RequestID)
	}
	record, file, err := query.repository.FindPublishedArtifact(ctx, scope.TenantID, request.ArtifactId)
	if err != nil {
		return nil, queryError(err, scope.RequestID)
	}
	grant, err := query.signer.SignDownload(ctx, record.Execution.Snapshot.PublicationScope, file.Object)
	if err != nil {
		return nil, queryError(err, scope.RequestID)
	}
	return &modeldevv1.AuthorizeArtifactDownloadResponse{Artifact: artifactView(record, file), DownloadUrl: grant.URL, ExpiresAt: timestamppb.New(grant.ExpiresAt)}, nil
}

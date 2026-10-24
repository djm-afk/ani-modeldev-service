package service

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
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
	models     biz.FinetuningModelFactsReader
}

// WithFinetuningFacts records the actual read dependency assembled by the
// composition. A selected base and target still require a current Model read.
func (query *Query) WithFinetuningFacts(models biz.FinetuningModelFactsReader) *Query {
	query.models = models
	return query
}

func (query *Query) WithMaterials(materials *biz.ManagedMaterials) *Query {
	query.materials = materials
	return query
}

func NewQuery(repository biz.ArtifactQueryRepository, signer biz.ArtifactSigner, logs ...biz.TrainingLogReader) *Query {
	query := &Query{repository: repository, signer: signer}
	if len(logs) == 1 {
		query.logs = logs[0]
	}
	return query
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
	options := biz.TrainingLogOptions{TailLines: request.TailLines, MaxBytes: request.MaxBytes}
	if options.TailLines == 0 {
		options.TailLines = 200
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = 16384
	}
	result, recorded, err := biz.RecordedTrainingLogsFor(record, source, options)
	if err != nil {
		return nil, queryError(err, scope.RequestID)
	}
	if !recorded {
		if query.logs == nil {
			return nil, queryError(biz.ErrTrainingLogsUnavailable, scope.RequestID)
		}
		result, err = query.logs.ReadTrainingLogs(ctx, source, options)
		if err != nil {
			return nil, queryError(err, scope.RequestID)
		}
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
	view := &modeldevv1.ExecutionView{Identity: &trainingv1.ExecutionIdentity{OperationId: value.OperationID, ExecutionId: value.ExecutionID, ExecutionSpecHash: value.SpecHash}, Name: value.Intent.Name, Kind: trainingv1.ExecutionKind_EXECUTION_KIND_GENERAL_TRAINING, ImplementationId: value.Snapshot.Implementation.ImplementationID, ComputePresetId: value.Snapshot.ComputeOrigin.ComputePresetID, InputVersionId: value.Snapshot.Input.InputVersionID, ImageVersionId: value.Snapshot.Program.ImageVersionID, States: states, CloseGeneration: record.Runtime.CloseGeneration, AcceptedAt: timestamppb.New(value.AcceptedAt), DeadlineAt: timestamppb.New(value.Snapshot.DeadlineAt), ObservedAt: timestamppb.New(value.AcceptedAt)}
	view.ParameterContractVersion = value.Snapshot.Implementation.ParameterContractVersion
	encoded, err := contractpb.EncodeSnapshot(value.Snapshot)
	if err != nil {
		return nil, biz.ErrPersistence
	}
	view.ComputeOrigin = encoded.ComputeOrigin
	view.Timing = encoded.Timing
	if value.Intent.MLP != nil {
		typed, err := contractpb.EncodeIntent(value.Intent)
		if err != nil {
			return nil, biz.ErrPersistence
		}
		view.ExplicitMlp = typed.GetMlp()
		for _, p := range value.Snapshot.Program.ResolvedParameters {
			wire := parameterView(p)
			if wire == nil {
				return nil, biz.ErrPersistence
			}
			view.ResolvedParameters = append(view.ResolvedParameters, wire)
		}
	}
	if fine := value.Snapshot.Finetuning; fine != nil {
		if value.Snapshot.Kind != "FINETUNING" || value.Intent.Finetuning == nil {
			return nil, biz.ErrPersistence
		}
		view.Kind = trainingv1.ExecutionKind_EXECUTION_KIND_FINETUNING
		view.ParameterContractVersion = cpup01.FinetuningParameterContractVersion
		typedIntent, err := contractpb.EncodeIntent(value.Intent)
		if err != nil {
			return nil, biz.ErrPersistence
		}
		view.ExplicitFinetuning = typedIntent.GetSftLora()
		for _, p := range value.Snapshot.Program.ResolvedParameters {
			wire := parameterView(p)
			if wire == nil {
				return nil, biz.ErrPersistence
			}
			view.ResolvedParameters = append(view.ResolvedParameters, wire)
		}
		view.ManagedSettings = contractpb.EncodeManagedSettings(fine.ManagedSettings)
		view.ResolvedConfigurationSha256 = fine.ResolvedConfigurationSHA256
	}
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
	for _, step := range record.Runtime.FinetuningSteps {
		if step.Binding.Role == "export" {
			view.CurrentStep = modeldevv1.PipelineStep_PIPELINE_STEP_EXPORT
		}
		if step.Binding.Role == "verify" {
			view.CurrentStep = modeldevv1.PipelineStep_PIPELINE_STEP_VERIFY
		}
	}
	if record.Runtime.Publication != nil {
		view.CurrentStep = modeldevv1.PipelineStep_PIPELINE_STEP_PUBLISH
		view.ObservedAt = timestamppb.New(record.Runtime.Publication.VerifiedAt)
	}
	if registration := record.Runtime.FinetuningRegistration; registration != nil {
		view.ModelRegistration, err = modelRegistrationView(value.Snapshot, registration)
		if err != nil {
			return nil, err
		}
		view.CurrentStep = modeldevv1.PipelineStep_PIPELINE_STEP_REGISTER
		view.ObservedAt = timestamppb.New(registration.RecordedAt)
		if registration.FailureCode != "" {
			view.Failure = &modeldevv1.ErrorDetail{Reason: modeldevv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE, SafeMessage: "model registration dependency unavailable"}
		}
	}
	if record.Runtime.ClosedAt != nil {
		view.CurrentStep = modeldevv1.PipelineStep_PIPELINE_STEP_CLOSE
		view.ObservedAt = timestamppb.New(*record.Runtime.ClosedAt)
	}
	view.History, err = executionHistoryView(record.Runtime.CloseEvidence)
	if err != nil {
		return nil, err
	}
	return view, nil
}

func executionHistoryView(evidence *biz.ManagedCloseEvidence) (*modeldevv1.ExecutionHistoryView, error) {
	if evidence == nil {
		return nil, nil
	}
	view := &modeldevv1.ExecutionHistoryView{RunId: evidence.RunID, WorkflowUid: evidence.WorkflowUID}
	if !evidence.ObservedAt.IsZero() {
		view.ObservedAt = timestamppb.New(evidence.ObservedAt)
		if view.ObservedAt.CheckValid() != nil {
			return nil, biz.ErrPersistence
		}
	}
	owner := evidence.OwnerTermination
	if owner == nil {
		return view, nil
	}
	switch owner.RunState {
	case "SUCCEEDED":
		view.RunState = modeldevv1.KfpRunTerminalState_KFP_RUN_TERMINAL_STATE_SUCCEEDED
	case "FAILED":
		view.RunState = modeldevv1.KfpRunTerminalState_KFP_RUN_TERMINAL_STATE_FAILED
	case "CANCELED":
		view.RunState = modeldevv1.KfpRunTerminalState_KFP_RUN_TERMINAL_STATE_CANCELED
	default:
		return nil, biz.ErrPersistence
	}
	switch owner.WorkflowPhase {
	case "Succeeded":
		view.WorkflowPhase = modeldevv1.WorkflowTerminalPhase_WORKFLOW_TERMINAL_PHASE_SUCCEEDED
	case "Failed":
		view.WorkflowPhase = modeldevv1.WorkflowTerminalPhase_WORKFLOW_TERMINAL_PHASE_FAILED
	case "Error":
		view.WorkflowPhase = modeldevv1.WorkflowTerminalPhase_WORKFLOW_TERMINAL_PHASE_ERROR
	default:
		return nil, biz.ErrPersistence
	}
	if owner.RunFinishedAt.IsZero() || owner.WorkflowFinishedAt.IsZero() || evidence.ObservedAt.IsZero() || owner.RunFinishedAt.After(evidence.ObservedAt) || owner.WorkflowFinishedAt.After(evidence.ObservedAt) || owner.WorkflowResourceVersion == "" || strings.ContainsAny(owner.WorkflowResourceVersion, "\x00\r\n") || (owner.WorkflowNodesSHA256 != "" && !runtimeSHA256(owner.WorkflowNodesSHA256)) {
		return nil, biz.ErrPersistence
	}
	view.RunFinishedAt, view.WorkflowFinishedAt = timestamppb.New(owner.RunFinishedAt), timestamppb.New(owner.WorkflowFinishedAt)
	if view.RunFinishedAt.CheckValid() != nil || view.WorkflowFinishedAt.CheckValid() != nil {
		return nil, biz.ErrPersistence
	}
	view.WorkflowResourceVersion, view.WorkflowNodesSha256 = owner.WorkflowResourceVersion, owner.WorkflowNodesSHA256
	return view, nil
}

// Registration visibility comes exclusively from the original persisted
// provider receipt. Reading a catalogue or retrying a write cannot imply READY.
func modelRegistrationView(snapshot cpup01.Snapshot, registration *biz.FinetuningRegistration) (*modeldevv1.ModelRegistrationView, error) {
	if snapshot.Kind != "FINETUNING" || snapshot.Finetuning == nil {
		return nil, biz.ErrPersistence
	}
	view := &modeldevv1.ModelRegistrationView{State: registration.State, FailureCode: registration.FailureCode}
	switch registration.State {
	case biz.RegistrationStatePending:
		if registration.FailureCode != "" || registration.Receipt != nil {
			return nil, biz.ErrPersistence
		}
	case biz.RegistrationStateUnavailable:
		if registration.FailureCode != biz.RegistrationUnavailableFailureCode || registration.Receipt != nil {
			return nil, biz.ErrPersistence
		}
	case biz.RegistrationStateReady:
		receipt := registration.Receipt
		if registration.FailureCode != "" || receipt == nil || !queryID(receipt.RegistrationID) || !queryID(receipt.ModelID) || !queryID(receipt.ModelVersionID) || receipt.StoragePath == "" || receipt.SizeBytes <= 0 || receipt.SizeBytes != registration.Bundle.SizeBytes || !runtimeSHA256(receipt.SHA256) || receipt.SHA256 != registration.Bundle.SHA256 {
			return nil, biz.ErrPersistence
		}
		if target := snapshot.Finetuning.TargetModelID; target != nil && receipt.ModelID != *target {
			return nil, biz.ErrPersistence
		}
		view.RegistrationId, view.ModelId, view.ModelVersionId = receipt.RegistrationID, receipt.ModelID, receipt.ModelVersionID
	default:
		return nil, biz.ErrPersistence
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
	if page := request.Page; page != nil {
		resolvedPage, pageSize, err := resolveModelDevPage(page)
		if err != nil {
			return nil, err
		}
		filter.Offset, filter.Limit = modelDevPageWindow(resolvedPage, pageSize)
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
	records, err := query.repository.ListQueryRecords(ctx, scope.TenantID, filter)
	if err != nil {
		return nil, queryError(err, scope.RequestID)
	}
	total, err := query.repository.CountQueryRecords(ctx, scope.TenantID, filter)
	if err != nil {
		return nil, queryError(err, scope.RequestID)
	}
	response := &modeldevv1.ListExecutionsResponse{Total: total}
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
	page, pageSize := uint32(1), uint32(20)
	if request.Page != nil {
		resolvedPage, resolvedSize, err := resolveModelDevPage(request.Page)
		if err != nil {
			return nil, err
		}
		page, pageSize = resolvedPage, resolvedSize
	}
	if query == nil || query.repository == nil {
		return nil, queryError(biz.ErrDownloadUnavailable, scope.RequestID)
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
	offset, limit := modelDevPageWindow(page, pageSize)
	if offset < len(files) {
		end := min(offset+limit, len(files))
		files = files[offset:end]
	} else {
		files = nil
	}
	for _, file := range files {
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

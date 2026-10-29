package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

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
	repository           biz.ArtifactQueryRepository
	operationsRepository biz.ExecutionOperationsRepository
	signer               biz.ArtifactSigner
	logs                 biz.TrainingLogReader
	containers           biz.ContainerLogReader
	streams              biz.ContainerLogStreamer
	workers              ExecutionWorkloadReader
	steps                StepWorkloadReader
	materials            *biz.ManagedMaterials
	models               biz.FinetuningModelFactsReader
	metrics              biz.MetricsObjectReader
}

// WithOperations installs the read used by the live workload views, whose
// authorization needs dispatch, authority and rejection evidence together.
func (query *Query) WithOperations(operations biz.ExecutionOperationsRepository) *Query {
	query.operationsRepository = operations
	return query
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

// WithMetrics installs the published-object reader used to derive training
// curves. Without it metrics stay absent; the detail response is unaffected.
func (query *Query) WithMetrics(metrics biz.MetricsObjectReader) *Query {
	query.metrics = metrics
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

// validExecutionNameFilter mirrors the admitted name shape: a non-blank,
// non-control substring of at most 80 runes. Longer or control-bearing input
// cannot match a stored name, so rejecting it keeps an impossible query from
// looking like an empty tenant.
func validExecutionNameFilter(value string) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > 80 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
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
	options := biz.TrainingLogOptions{TailLines: request.TailLines, MaxBytes: request.MaxBytes}
	if options.TailLines == 0 {
		options.TailLines = 200
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = 16384
	}
	selection, err := query.selectLogTarget(ctx, record, request)
	if err != nil {
		return nil, workloadError(err, scope.RequestID)
	}
	var result biz.TrainingLogResult
	if selection.Kind == biz.LogSourceKindTrainer && selection.Target.ContainerName == "node" && !selection.Target.Previous {
		source, err := trainingSource(record, selection)
		if err != nil {
			return nil, queryError(err, scope.RequestID)
		}
		archived, recorded, err := biz.RecordedTrainingLogsFor(record, source, options)
		if err != nil {
			return nil, queryError(err, scope.RequestID)
		}
		if recorded {
			result = archived
		} else {
			if query.logs == nil {
				return nil, queryError(biz.ErrTrainingLogsUnavailable, scope.RequestID)
			}
			result, err = query.logs.ReadTrainingLogs(ctx, source, options)
			if err != nil {
				return nil, queryError(err, scope.RequestID)
			}
		}
	} else {
		if query.containers == nil {
			return nil, workloadError(biz.ErrTrainingLogsUnavailable, scope.RequestID)
		}
		result, err = query.containers.ReadContainerLogs(ctx, selection.Target, options)
		if err != nil {
			return nil, queryLogError(err, scope.RequestID)
		}
	}
	response := &modeldevv1.GetExecutionLogsResponse{
		Source:        &trainingv1.LogRef{LogId: selection.LogID, ResourceUid: selection.Target.PodUID, ContainerName: selection.Target.ContainerName},
		Truncated:     result.Truncated,
		ObservedAt:    timestamppb.New(result.ObservedAt),
		SourceKind:    logSourceKindValue(selection.Kind),
		ContainerName: selection.Target.ContainerName,
		Previous:      selection.Target.Previous,
		Available:     true,
	}
	for _, line := range result.Lines {
		response.Lines = append(response.Lines, &modeldevv1.LogLine{Timestamp: timestamppb.New(line.Timestamp), Text: line.Text})
	}
	return response, nil
}

func logSourceKindValue(kind biz.LogSourceKind) modeldevv1.LogSourceKind {
	if kind == biz.LogSourceKindKFPStep {
		return modeldevv1.LogSourceKind_LOG_SOURCE_KIND_KFP_STEP
	}
	return modeldevv1.LogSourceKind_LOG_SOURCE_KIND_TRAINER
}

// queryLogError distinguishes a missing previous instance from a plain
// unavailable log so the client can offer the right next action.
func queryLogError(err error, requestID string) error {
	if errors.Is(err, biz.ErrLogPreviousUnavailable) {
		return commandError(codes.NotFound, modeldevv1.ErrorReason_ERROR_REASON_RESOURCE_NOT_FOUND, "previous container instance unavailable", requestID)
	}
	return queryError(err, requestID)
}

// selectLogTarget resolves exactly one container instance for a read. The
// worker path stays compatible: no pod_uid and no container keeps the original
// single-worker `node` behaviour, including its archived-tail fallback.
func (query *Query) selectLogTarget(ctx context.Context, record biz.QueryRecord, request *modeldevv1.GetExecutionLogsRequest) (biz.LogSelection, error) {
	container := request.GetContainer()
	podUID := request.GetPodUid()
	previous := request.GetPrevious()
	if podUID == "" && container == "" {
		source, err := biz.TrainingLogSourceFor(record, request.GetLogId())
		if err != nil {
			return biz.LogSelection{}, err
		}
		return biz.LogSelection{Kind: biz.LogSourceKindTrainer, LogID: source.LogID, Target: biz.ContainerLogTarget{
			Namespace: source.Namespace, NamespaceUID: source.NamespaceUID, PodName: source.PodName,
			PodUID: source.PodUID, ContainerName: source.ContainerName, Previous: previous,
		}}, nil
	}
	workload, err := query.logWorkloadScope(ctx, record)
	if err != nil {
		return biz.LogSelection{}, err
	}
	if podUID == "" {
		return biz.LogSelection{}, biz.ErrWorkloadViewUnavailable
	}
	if workload.PipelineReadable() && query.steps != nil {
		if pod, err := query.steps.StepPod(ctx, workload, podUID); err == nil {
			logID := biz.StepLogID(workload.ExecutionID, pod.UID)
			if request.GetLogId() != "" && request.GetLogId() != logID {
				return biz.LogSelection{}, biz.ErrTrainingLogNotFound
			}
			return biz.LogSelection{Kind: biz.LogSourceKindKFPStep, LogID: logID, Target: biz.ContainerLogTarget{
				Namespace: workload.NamespaceName, NamespaceUID: workload.NamespaceUID, PodName: pod.Name,
				PodUID: pod.UID, ContainerName: containerOr(container, biz.DefaultContainerFor(biz.LogSourceKindKFPStep)), Previous: previous,
			}}, nil
		} else if !errors.Is(err, biz.ErrTrainingLogNotFound) {
			return biz.LogSelection{}, err
		} else if request.GetLogId() == biz.StepLogID(workload.ExecutionID, podUID) {
			// The credential proves the caller asked for this step Pod, but the
			// Pod is gone. M1 has no KFP archive reader, so the answer is an
			// explicit unavailable step log rather than a worker fallback.
			return biz.LogSelection{}, biz.ErrStepLogsUnavailable
		}
	}
	if query.workers == nil || !workload.WorkerReadable() {
		return biz.LogSelection{}, biz.ErrTrainingLogNotFound
	}
	pods, err := query.workers.ListWorkerPods(ctx, workload)
	if err != nil {
		return biz.LogSelection{}, err
	}
	for _, pod := range pods {
		if pod.UID != podUID {
			continue
		}
		return biz.LogSelection{Kind: biz.LogSourceKindTrainer, LogID: workerLogID(workload.ExecutionID, pod.UID), Target: biz.ContainerLogTarget{
			Namespace: workload.NamespaceName, NamespaceUID: workload.NamespaceUID, PodName: pod.Name,
			PodUID: pod.UID, ContainerName: containerOr(container, biz.DefaultContainerFor(biz.LogSourceKindTrainer)), Previous: previous,
		}}, nil
	}
	return biz.LogSelection{}, biz.ErrTrainingLogNotFound
}

// logWorkloadScope resolves the read boundary for a container log request. The
// pipeline plane needs the frozen Run authority, which only the operation
// aggregate carries, so a step log can never be addressed from a bare record.
func (query *Query) logWorkloadScope(ctx context.Context, record biz.QueryRecord) (biz.ExecutionWorkloadScope, error) {
	aggregate := biz.OperationRecord{QueryRecord: record}
	if query.operationsRepository != nil {
		committed, err := query.operationsRepository.GetOperationRecord(ctx, record.Execution.TenantID, record.Execution.ExecutionID)
		if err != nil {
			return biz.ExecutionWorkloadScope{}, err
		}
		aggregate = committed
	}
	return biz.ExecutionWorkloadScopeFor(aggregate)
}

func containerOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// trainingSource re-derives the durable worker identity for a selection so an
// archived tail is checked against the same owner chain the live read used.
func trainingSource(record biz.QueryRecord, selection biz.LogSelection) (biz.TrainingLogSource, error) {
	source, err := biz.TrainingLogSourceFor(record, selection.LogID)
	if err != nil {
		return biz.TrainingLogSource{}, err
	}
	if source.Namespace != selection.Target.Namespace || source.PodName != selection.Target.PodName ||
		source.PodUID != selection.Target.PodUID || source.ContainerName != selection.Target.ContainerName {
		return biz.TrainingLogSource{}, biz.ErrTrainingLogsUnavailable
	}
	return source, nil
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
	// The curve needs one object read per execution, so only the single-execution
	// detail fills it. A list keeps deriving everything from committed facts.
	view.Metrics = query.executionMetrics(ctx, record)
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
		// Publish only the base model identity: object key, storage scope and
		// the file list stay owner-internal.
		view.BaseModel = &modeldevv1.BaseModelIdentity{
			ModelId: fine.BaseModel.ModelID, VersionId: fine.BaseModel.VersionID,
			Family: fine.BaseModel.Family, Format: fine.BaseModel.Format,
		}
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
	// A running collect supersedes the step that produced its inputs, but never
	// publish: once publish is admitted or published, the later facts decide.
	// The collect step state uses the same predicate, so the timeline and
	// current_step cannot disagree.
	if step := currentStepFromFacts(record.Runtime); step != modeldevv1.PipelineStep_PIPELINE_STEP_UNSPECIFIED {
		view.CurrentStep = step
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
	view.Steps = executionSteps(value, record.Runtime)
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
	if request.Name != nil {
		if !validExecutionNameFilter(*request.Name) {
			return nil, status.Error(codes.InvalidArgument, "invalid name filter")
		}
		// The name is matched case-insensitively as a substring, so it is
		// folded once here rather than per scanned row.
		filter.Name = strings.ToLower(*request.Name)
	}
	if request.Kind != nil {
		// UNSPECIFIED is the enum placeholder, not a task type; treating it as
		// "no filter" would silently widen the page.
		if *request.Kind == trainingv1.ExecutionKind_EXECUTION_KIND_UNSPECIFIED {
			return nil, status.Error(codes.InvalidArgument, "invalid kind filter")
		}
		kind, known := trainingv1.ExecutionKind_name[int32(*request.Kind)]
		if !known {
			return nil, status.Error(codes.InvalidArgument, "invalid kind filter")
		}
		filter.Kind = strings.TrimPrefix(kind, "EXECUTION_KIND_")
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

package service

import (
	"context"
	"errors"
	"sort"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ExecutionWorkloadReader reads the two live planes of one execution. Both are
// real-time Kubernetes reads; nothing here is persisted into ExecutionView.
type ExecutionWorkloadReader interface {
	ListWorkerPods(context.Context, biz.ExecutionWorkloadScope) ([]biz.ExecutionPod, error)
	ListTrainingWorkloadEvents(context.Context, biz.ExecutionWorkloadScope, []biz.ExecutionPod) ([]biz.ExecutionEvent, error)
}

// StepWorkloadReader reads the KFP orchestration plane of the same execution.
type StepWorkloadReader interface {
	ListStepPods(context.Context, biz.ExecutionWorkloadScope) ([]biz.ExecutionPod, error)
	StepPod(context.Context, biz.ExecutionWorkloadScope, string) (biz.ExecutionPod, error)
	ListWorkflowEvents(context.Context, biz.ExecutionWorkloadScope) ([]biz.ExecutionEvent, error)
}

// LogSources bundles the three log capabilities the query service needs: the
// archived worker tail reader, the explicit container reader, and the follower.
type LogSources struct {
	Archived   biz.TrainingLogReader
	Containers biz.ContainerLogReader
	Streams    biz.ContainerLogStreamer
}

func (query *Query) WithWorkload(workers ExecutionWorkloadReader, steps StepWorkloadReader) *Query {
	query.workers = workers
	query.steps = steps
	return query
}

func (query *Query) WithLogStream(containers biz.ContainerLogReader, streams biz.ContainerLogStreamer) *Query {
	query.containers = containers
	query.streams = streams
	return query
}

func workloadError(err error, requestID string) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	case errors.Is(err, biz.ErrExecutionNotFound), errors.Is(err, biz.ErrTrainingLogNotFound):
		return commandError(codes.NotFound, modeldevv1.ErrorReason_ERROR_REASON_RESOURCE_NOT_FOUND, "resource unavailable", requestID)
	case errors.Is(err, biz.ErrLogPreviousUnavailable):
		return commandError(codes.NotFound, modeldevv1.ErrorReason_ERROR_REASON_RESOURCE_NOT_FOUND, "previous container instance unavailable", requestID)
	case errors.Is(err, biz.ErrStepLogsUnavailable), errors.Is(err, biz.ErrTrainingLogsUnavailable):
		return commandError(codes.FailedPrecondition, modeldevv1.ErrorReason_ERROR_REASON_CAPABILITY_NOT_ENABLED, "logs unavailable", requestID)
	case errors.Is(err, biz.ErrWorkloadViewUnavailable), errors.Is(err, biz.ErrInvalidAdmission):
		return commandError(codes.FailedPrecondition, modeldevv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_READY, "execution workload unavailable", requestID)
	}
	return commandError(codes.Unavailable, modeldevv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE, "query unavailable", requestID)
}

// ListExecutionPods answers the container-group / orchestration-step tab. The
// worker plane and the pipeline plane are queried separately and concatenated
// only when both were asked for; each Pod keeps the layer that produced it.
func (query *Query) ListExecutionPods(ctx context.Context, request *modeldevv1.ListExecutionPodsRequest) (*modeldevv1.ListExecutionPodsResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevQueryService_ListExecutionPods_FullMethodName)
	if err != nil {
		return nil, err
	}
	layer := modeldevv1.PodLayer_POD_LAYER_ALL
	if request != nil && request.Layer != modeldevv1.PodLayer_POD_LAYER_UNSPECIFIED {
		layer = request.Layer
	}
	if request == nil || !queryID(request.ExecutionId) || len(request.ProtoReflect().GetUnknown()) != 0 || layer == modeldevv1.PodLayer_POD_LAYER_UNSPECIFIED && request.Layer != 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid pod query")
	}
	if query == nil || query.repository == nil {
		return nil, queryError(biz.ErrPersistence, scope.RequestID)
	}
	record, err := query.operations(ctx, scope, request.ExecutionId)
	if err != nil {
		return nil, err
	}
	workload, err := biz.ExecutionWorkloadScopeFor(record)
	if err != nil {
		return nil, workloadError(err, scope.RequestID)
	}
	response := &modeldevv1.ListExecutionPodsResponse{ObservedAt: timestamppb.Now()}
	wantTrainer := layer == modeldevv1.PodLayer_POD_LAYER_ALL || layer == modeldevv1.PodLayer_POD_LAYER_TRAINER
	wantPipeline := layer == modeldevv1.PodLayer_POD_LAYER_ALL || layer == modeldevv1.PodLayer_POD_LAYER_PIPELINE
	var gathered []biz.ExecutionPod
	var workerPods []biz.ExecutionPod
	if wantTrainer {
		pods, err := query.workerPods(ctx, workload, record, request.IncludeRecycled)
		if err != nil {
			return nil, workloadError(err, scope.RequestID)
		}
		workerPods = pods
		gathered = append(gathered, pods...)
	}
	if wantPipeline && workload.PipelineReadable() {
		if query.steps == nil {
			return nil, workloadError(biz.ErrWorkloadViewUnavailable, scope.RequestID)
		}
		pods, err := query.steps.ListStepPods(ctx, workload)
		if err != nil {
			return nil, workloadError(err, scope.RequestID)
		}
		gathered = append(gathered, pods...)
	}
	response.Availability = availabilityValue(overallAvailability(workload, workerPods, record))
	for _, pod := range gathered {
		response.Pods = append(response.Pods, podViewMessage(pod))
	}
	return response, nil
}

// overallAvailability reports what the whole response can prove. An execution
// whose worker Pods all come from the durable observation is RECYCLED, never
// LIVE; an execution with no Pods keeps the reason from its create evidence.
// The same holds when the live read finds nothing but the observation recorded
// worker Pods: they were collected, so the honest answer is RECYCLED even
// though the caller did not ask for the recycled rows.
func overallAvailability(workload biz.ExecutionWorkloadScope, workerPods []biz.ExecutionPod, record biz.OperationRecord) biz.PodAvailability {
	if len(workerPods) == 0 {
		if workload.Availability == biz.PodAvailabilityLive && observedWorkerPods(record) != 0 {
			return biz.PodAvailabilityRecycled
		}
		return workload.Availability
	}
	for _, pod := range workerPods {
		if pod.Availability != biz.PodAvailabilityRecycled {
			return workload.Availability
		}
	}
	return biz.PodAvailabilityRecycled
}

// observedWorkerPods counts the worker Pods the durable observation recorded.
func observedWorkerPods(record biz.OperationRecord) int {
	observation := record.Runtime.Observation
	if observation == nil {
		return 0
	}
	count := 0
	for _, resource := range observation.Resources {
		if resource.Kind == "Pod" && resource.UID != "" {
			count++
		}
	}
	return count
}

// workerPods prefers the durable observation when the API objects are gone, so
// a collected execution still reports RECYCLED worker Pods instead of an empty
// list. Only an execution with no persisted worker evidence is really empty.
func (query *Query) workerPods(ctx context.Context, workload biz.ExecutionWorkloadScope, record biz.OperationRecord, includeRecycled bool) ([]biz.ExecutionPod, error) {
	live := make([]biz.ExecutionPod, 0)
	if workload.WorkerReadable() && query.workers != nil {
		pods, err := query.workers.ListWorkerPods(ctx, workload)
		if err != nil {
			return nil, err
		}
		live = pods
	}
	if !includeRecycled {
		return live, nil
	}
	seen := make(map[string]bool, len(live))
	for _, pod := range live {
		seen[pod.UID] = true
	}
	for _, pod := range recycledWorkerPods(record, workload.ExecutionID) {
		if seen[pod.UID] {
			continue
		}
		live = append(live, pod)
	}
	return live, nil
}

// recycledWorkerPods projects the persisted observation as RECYCLED Pods. The
// phase is left empty: a recycled Pod has no current Kubernetes phase.
func recycledWorkerPods(record biz.OperationRecord, executionID string) []biz.ExecutionPod {
	observation := record.Runtime.Observation
	if observation == nil {
		return nil
	}
	result := make([]biz.ExecutionPod, 0, len(observation.Resources))
	for _, resource := range observation.Resources {
		if resource.Kind != "Pod" || resource.UID == "" {
			continue
		}
		pod := biz.ExecutionPod{
			Name: resource.Name, UID: resource.UID, Layer: biz.PodLayerTrainer,
			Availability: biz.PodAvailabilityRecycled, APIPresent: resource.APIObjectPresent,
			ExitCode: resource.ExitCode, LogID: biz.TrainerLogID(executionID, resource.UID),
			Containers: []biz.PodContainer{{Name: "node", Type: biz.ContainerTypeContainer, State: biz.ContainerStateTerminated}},
		}
		if resource.TrainingLog != nil {
			source, err := biz.TrainingLogSourceFor(record.QueryRecord, pod.LogID)
			pod.LogAvailable = err == nil && source == resource.TrainingLog.Source && biz.ValidateRecordedTrainingLogs(record.QueryRecord, []biz.RecordedTrainingLog{*resource.TrainingLog}) == nil
		}
		result = append(result, pod)
	}
	return result
}

func workerLogID(executionID, podUID string) string {
	return biz.TrainerLogID(executionID, podUID)
}

// ListExecutionEvents answers the event tab. Kubernetes Events expire on their
// own schedule, so an empty list after retention is a normal answer and never
// an error.
func (query *Query) ListExecutionEvents(ctx context.Context, request *modeldevv1.ListExecutionEventsRequest) (*modeldevv1.ListExecutionEventsResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevQueryService_ListExecutionEvents_FullMethodName)
	if err != nil {
		return nil, err
	}
	if request == nil || !queryID(request.ExecutionId) || len(request.ProtoReflect().GetUnknown()) != 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid event query")
	}
	eventScope, valid := biz.ParseEventScope(request.Scope)
	if !valid {
		return nil, status.Error(codes.InvalidArgument, "invalid event scope")
	}
	if query == nil || query.repository == nil {
		return nil, queryError(biz.ErrPersistence, scope.RequestID)
	}
	record, err := query.operations(ctx, scope, request.ExecutionId)
	if err != nil {
		return nil, err
	}
	workload, err := biz.ExecutionWorkloadScopeFor(record)
	if err != nil {
		return nil, workloadError(err, scope.RequestID)
	}
	var gathered []biz.ExecutionEvent
	if workload.WorkerReadable() && query.workers != nil {
		pods, podErr := query.workers.ListWorkerPods(ctx, workload)
		if podErr != nil {
			return nil, workloadError(podErr, scope.RequestID)
		}
		events, eventErr := query.workers.ListTrainingWorkloadEvents(ctx, workload, pods)
		if eventErr != nil {
			return nil, workloadError(eventErr, scope.RequestID)
		}
		gathered = append(gathered, events...)
	}
	if workload.PipelineReadable() && query.steps != nil {
		events, eventErr := query.steps.ListWorkflowEvents(ctx, workload)
		if eventErr != nil {
			return nil, workloadError(eventErr, scope.RequestID)
		}
		gathered = append(gathered, events...)
	}
	events := filterEvents(workload, gathered, eventScope)
	response := &modeldevv1.ListExecutionEventsResponse{ObservedAt: timestamppb.Now(), RetentionHint: eventRetentionHint}
	for _, event := range events {
		response.Events = append(response.Events, eventViewMessage(event))
	}
	return response, nil
}

// eventRetentionHint is the Kubernetes Event TTL the cluster currently uses.
const eventRetentionHint = "1h"

func filterEvents(workload biz.ExecutionWorkloadScope, events []biz.ExecutionEvent, scope biz.ExecutionEventScope) []biz.ExecutionEvent {
	selected := make([]biz.ExecutionEvent, 0, len(events))
	for _, event := range events {
		if scope == biz.EventScopeWarning && event.Type != "Warning" {
			continue
		}
		if scope == biz.EventScopeKey && !keyEvent(workload, event) {
			continue
		}
		selected = append(selected, event)
	}
	merged := aggregateEvents(selected)
	sortEventsByRecency(merged)
	return merged
}

// keyEvent keeps every Warning plus the controller failures that explain a
// root cause. A Normal event of a driver Pod would otherwise bury the signal.
func keyEvent(workload biz.ExecutionWorkloadScope, event biz.ExecutionEvent) bool {
	if event.Type == "Warning" {
		return true
	}
	return event.InvolvedKind != "Pod"
}

func availabilityValue(value biz.PodAvailability) modeldevv1.PodAvailability {
	switch value {
	case biz.PodAvailabilityLive:
		return modeldevv1.PodAvailability_POD_AVAILABILITY_LIVE
	case biz.PodAvailabilityRecycled:
		return modeldevv1.PodAvailability_POD_AVAILABILITY_RECYCLED
	case biz.PodAvailabilityNeverCreated:
		return modeldevv1.PodAvailability_POD_AVAILABILITY_NEVER_CREATED
	case biz.PodAvailabilityRejected:
		return modeldevv1.PodAvailability_POD_AVAILABILITY_REJECTED
	case biz.PodAvailabilityOrphaned:
		return modeldevv1.PodAvailability_POD_AVAILABILITY_ORPHANED
	case biz.PodAvailabilityCreationPending:
		return modeldevv1.PodAvailability_POD_AVAILABILITY_CREATION_PENDING
	}
	return modeldevv1.PodAvailability_POD_AVAILABILITY_UNSPECIFIED
}

func podLayerValue(value biz.PodLayer) modeldevv1.PodLayer {
	switch value {
	case biz.PodLayerTrainer:
		return modeldevv1.PodLayer_POD_LAYER_TRAINER
	case biz.PodLayerPipeline:
		return modeldevv1.PodLayer_POD_LAYER_PIPELINE
	}
	return modeldevv1.PodLayer_POD_LAYER_ALL
}

func podViewMessage(pod biz.ExecutionPod) *modeldevv1.PodView {
	view := &modeldevv1.PodView{
		PodName: pod.Name, PodUid: pod.UID, PodIp: pod.IP, Phase: pod.Phase, Ready: pod.Ready,
		NodeName: pod.NodeName, Restarts: pod.Restarts, Layer: podLayerValue(pod.Layer),
		Availability: availabilityValue(pod.Availability), StepName: pod.StepName, Attempt: pod.Attempt,
		JobIndex: pod.JobIndex, LogId: pod.LogID, LogAvailable: pod.LogAvailable, ApiPresent: pod.APIPresent,
		ExitCode: pod.ExitCode, MetricInstanceId: pod.MetricInstanceID,
	}
	view.CreatedAt, view.StartTime, view.DeletionTimestamp = optionalTimestamp(pod.CreatedAt), optionalTimestamp(pod.StartTime), optionalTimestamp(pod.DeletedAt)
	for _, container := range pod.Containers {
		view.Containers = append(view.Containers, containerViewMessage(container))
	}
	for _, container := range pod.InitContainers {
		view.InitContainers = append(view.InitContainers, containerViewMessage(container))
	}
	for _, container := range pod.EphemeralContainers {
		view.EphemeralContainers = append(view.EphemeralContainers, containerViewMessage(container))
	}
	return view
}

func containerViewMessage(container biz.PodContainer) *modeldevv1.ContainerView {
	view := &modeldevv1.ContainerView{
		Name: container.Name, Type: string(container.Type), State: string(container.State),
		Reason: container.Reason, Message: container.Message, RestartCount: container.RestartCount,
		ExitCode: container.ExitCode,
	}
	view.StartedAt, view.FinishedAt = optionalTimestamp(container.StartedAt), optionalTimestamp(container.FinishedAt)
	if container.LastState != nil {
		view.LastState = containerViewMessage(*container.LastState)
	}
	return view
}

func eventViewMessage(event biz.ExecutionEvent) *modeldevv1.EventView {
	return &modeldevv1.EventView{
		Type: event.Type, Reason: event.Reason, Message: event.Message,
		FirstTimestamp: timestamppb.New(event.FirstTimestamp), LastTimestamp: timestamppb.New(event.LastTimestamp),
		Count: event.Count, Source: event.Source, InvolvedKind: event.InvolvedKind,
		InvolvedName: event.InvolvedName, InvolvedUid: event.InvolvedUID, FieldPath: event.FieldPath,
		ReportingInstance: event.ReportingInstance,
	}
}

// optionalTimestamp keeps an absent observation absent instead of emitting the
// protobuf epoch.
func optionalTimestamp(at *time.Time) *timestamppb.Timestamp {
	if at == nil || at.IsZero() {
		return nil
	}
	return timestamppb.New(*at)
}

func aggregateEvents(events []biz.ExecutionEvent) []biz.ExecutionEvent {
	return biz.AggregateExecutionEvents(events)
}

// sortEventsByRecency orders by last occurrence so the most recent failure is
// first; ties keep the collection order stable.
func sortEventsByRecency(events []biz.ExecutionEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].LastTimestamp.After(events[j].LastTimestamp)
	})
}

// operations reads the durable aggregate the workload views are authorized
// against. It is deliberately a single read: a Pod list and its availability
// evidence must come from the same committed revision.
func (query *Query) operations(ctx context.Context, scope GovernanceDelivery, executionID string) (biz.OperationRecord, error) {
	record, err := query.operationsRepository.GetOperationRecord(ctx, scope.TenantID, executionID)
	if err != nil {
		return biz.OperationRecord{}, queryError(err, scope.RequestID)
	}
	return record, nil
}

package biz

import (
	"errors"
	"time"
)

var (
	ErrWorkloadViewUnavailable = errors.New("EXECUTION_WORKLOAD_UNAVAILABLE")
	ErrLogOffsetInvalid        = errors.New("LOG_OFFSET_INVALID")
	ErrStepLogsUnavailable     = errors.New("STEP_LOGS_UNAVAILABLE")
	ErrLogPreviousUnavailable  = errors.New("LOG_PREVIOUS_UNAVAILABLE")
)

// PodLayer separates the Trainer execution plane from the KFP orchestration
// plane. A training-instance list never mixes them.
type PodLayer string

const (
	PodLayerAll      PodLayer = "ALL"
	PodLayerTrainer  PodLayer = "TRAINER"
	PodLayerPipeline PodLayer = "PIPELINE"
)

// MetricInstanceLabel is written by the admission webhook from sources it has
// already verified. The observability training records use the same value, so
// reading it here keeps this service from deriving an identity of its own.
const MetricInstanceLabel = "ani.io/instance-id"

func ParsePodLayer(value string) (PodLayer, bool) {
	switch PodLayer(value) {
	case "", PodLayerAll:
		return PodLayerAll, true
	case PodLayerTrainer:
		return PodLayerTrainer, true
	case PodLayerPipeline:
		return PodLayerPipeline, true
	}
	return "", false
}

// PodAvailability carries evidence a Kubernetes phase cannot express. Synthetic
// values are never written into Phase.
type PodAvailability string

const (
	PodAvailabilityLive            PodAvailability = "LIVE"
	PodAvailabilityRecycled        PodAvailability = "RECYCLED"
	PodAvailabilityNeverCreated    PodAvailability = "NEVER_CREATED"
	PodAvailabilityRejected        PodAvailability = "REJECTED"
	PodAvailabilityOrphaned        PodAvailability = "ORPHANED"
	PodAvailabilityCreationPending PodAvailability = "CREATION_PENDING"
)

type ContainerType string

const (
	ContainerTypeContainer ContainerType = "container"
	ContainerTypeInit      ContainerType = "init_container"
	ContainerTypeEphemeral ContainerType = "ephemeral"
)

type ContainerState string

const (
	ContainerStateRunning    ContainerState = "running"
	ContainerStateWaiting    ContainerState = "waiting"
	ContainerStateTerminated ContainerState = "terminated"
	ContainerStateUnknown    ContainerState = "unknown"
)

// PodContainer is one container observation. ExitCode is present only while
// the container is terminated; a waiting container carries no invented code.
type PodContainer struct {
	Name         string
	Type         ContainerType
	State        ContainerState
	Reason       string
	Message      string
	RestartCount int32
	ExitCode     *int32
	StartedAt    *time.Time
	FinishedAt   *time.Time
	LastState    *PodContainer
}

type ExecutionPod struct {
	Name                string
	UID                 string
	IP                  string
	Phase               string
	Ready               bool
	NodeName            string
	Restarts            int32
	CreatedAt           *time.Time
	StartTime           *time.Time
	DeletedAt           *time.Time
	Layer               PodLayer
	Availability        PodAvailability
	StepName            string
	Attempt             int32
	JobIndex            int32
	LogID               string
	LogAvailable        bool
	Containers          []PodContainer
	InitContainers      []PodContainer
	EphemeralContainers []PodContainer
	APIPresent          bool
	ExitCode            *int32
	// MetricInstanceID is the value of the ani.io/instance-id label the
	// admission webhook writes: the execution ID for a Trainer worker and the
	// KFP run ID for a pipeline step. It names the same instance the
	// observability training records carry, so callers can query one Pod's
	// curve without re-deriving it. Empty on Pods created before labelling.
	MetricInstanceID string
}

type ExecutionEvent struct {
	Type              string
	Reason            string
	Message           string
	FirstTimestamp    time.Time
	LastTimestamp     time.Time
	Count             int32
	Source            string
	InvolvedKind      string
	InvolvedName      string
	InvolvedUID       string
	FieldPath         string
	ReportingInstance string
}

// ExecutionEventScope selects how much of the collected events is returned.
type ExecutionEventScope string

const (
	EventScopeKey     ExecutionEventScope = "key"
	EventScopeAll     ExecutionEventScope = "all"
	EventScopeWarning ExecutionEventScope = "warning"
)

func ParseEventScope(value string, warningOnly bool) (ExecutionEventScope, bool) {
	if warningOnly {
		return EventScopeWarning, true
	}
	switch ExecutionEventScope(value) {
	case "", EventScopeKey:
		return EventScopeKey, true
	case EventScopeAll:
		return EventScopeAll, true
	case EventScopeWarning:
		return EventScopeWarning, true
	}
	return "", false
}

// ExecutionWorkloadScope is the verified identity boundary of one execution's
// live Kubernetes reads. Every field comes from persisted owner facts; no
// caller-supplied namespace or Pod name reaches an adapter.
type ExecutionWorkloadScope struct {
	TenantID       string
	ExecutionID    string
	NamespaceName  string
	NamespaceUID   string
	TrainJobName   string
	TrainJobUID    string
	PodGroupWanted bool
	RunID          string
	WorkflowName   string
	WorkflowUID    string
	Availability   PodAvailability
}

// ExecutionWorkloadScopeFor derives the read boundary and the availability
// evidence of the worker layer. The decision order is deliberate: an explicit
// rejection is stronger than a missing plan, and a missing Handle after a
// consumed permit is creation-unknown rather than never-created.
func ExecutionWorkloadScopeFor(record OperationRecord) (ExecutionWorkloadScope, error) {
	scope := ExecutionWorkloadScope{
		TenantID:      record.Execution.TenantID,
		ExecutionID:   record.Execution.ExecutionID,
		NamespaceName: record.Execution.Snapshot.Environment.NamespaceName,
		NamespaceUID:  record.Execution.Snapshot.Environment.NamespaceUID,
	}
	if scope.TenantID == "" || scope.ExecutionID == "" || scope.NamespaceName == "" || scope.NamespaceUID == "" {
		return ExecutionWorkloadScope{}, ErrWorkloadViewUnavailable
	}
	groupWanted, err := RuntimePlanSchedulesPodGroup(record.Execution.Snapshot.RuntimePlan)
	if err != nil {
		return ExecutionWorkloadScope{}, err
	}
	scope.PodGroupWanted = groupWanted
	if authority := record.Authority; authority != nil {
		if authority.RunID == "" || authority.WorkflowUID == "" || authority.WorkflowName == "" ||
			authority.NamespaceName != scope.NamespaceName || authority.NamespaceUID != scope.NamespaceUID {
			return ExecutionWorkloadScope{}, ErrWorkloadViewUnavailable
		}
		scope.RunID, scope.WorkflowName, scope.WorkflowUID = authority.RunID, authority.WorkflowName, authority.WorkflowUID
	}
	runtime := record.Runtime
	switch {
	case runtime.TrainingRejection != nil:
		scope.Availability = PodAvailabilityRejected
	case runtime.Training == nil:
		// No plan was ever persisted, so nothing was sent for this execution.
		scope.Availability = PodAvailabilityNeverCreated
	case runtime.TrainingHandle == nil:
		// The one-shot creation permit may have been consumed without a
		// persisted Handle: the outcome is unknown, not "never created".
		scope.Availability = PodAvailabilityCreationPending
	default:
		if runtime.Training.Name != scope.trainJobName(record) {
			return ExecutionWorkloadScope{}, ErrWorkloadViewUnavailable
		}
		scope.TrainJobName = runtime.Training.Name
		scope.TrainJobUID = runtime.TrainingHandle.TrainJobUID
		if scope.TrainJobUID == "" {
			return ExecutionWorkloadScope{}, ErrWorkloadViewUnavailable
		}
		scope.Availability = PodAvailabilityLive
	}
	return scope, nil
}

func (scope ExecutionWorkloadScope) trainJobName(record OperationRecord) string {
	return "md-" + record.Execution.ExecutionID
}

// AggregateExecutionEvents merges events sharing type, reason, source,
// involved identity and message. Keeping the message in the key means two
// distinct messages stay two rows instead of one losing the other's text.
func AggregateExecutionEvents(events []ExecutionEvent) []ExecutionEvent {
	type key struct {
		Type, Reason, Source, Kind, UID, Name, Message string
	}
	order := make([]key, 0, len(events))
	merged := make(map[key]ExecutionEvent, len(events))
	for _, event := range events {
		id := key{event.Type, event.Reason, event.Source, event.InvolvedKind, event.InvolvedUID, event.InvolvedName, event.Message}
		existing, seen := merged[id]
		if !seen {
			order = append(order, id)
			merged[id] = event
			continue
		}
		if event.FirstTimestamp.Before(existing.FirstTimestamp) {
			existing.FirstTimestamp = event.FirstTimestamp
		}
		if event.LastTimestamp.After(existing.LastTimestamp) {
			existing.LastTimestamp = event.LastTimestamp
			existing.ReportingInstance = event.ReportingInstance
			existing.FieldPath = event.FieldPath
		}
		existing.Count += event.Count
		merged[id] = existing
	}
	result := make([]ExecutionEvent, 0, len(order))
	for _, id := range order {
		result = append(result, merged[id])
	}
	return result
}

// WorkerReadable reports whether the worker layer can be queried at all.
func (scope ExecutionWorkloadScope) WorkerReadable() bool {
	return scope.TrainJobUID != "" && scope.Availability != PodAvailabilityRejected &&
		scope.Availability != PodAvailabilityNeverCreated && scope.Availability != PodAvailabilityCreationPending
}

// PipelineReadable reports whether a KFP step Pod read can be attempted.
func (scope ExecutionWorkloadScope) PipelineReadable() bool {
	return scope.WorkflowUID != "" && scope.RunID != "" && scope.WorkflowName != ""
}

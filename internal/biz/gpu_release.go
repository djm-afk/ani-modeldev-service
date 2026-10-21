package biz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const GPUOwnerService = "ani-modeldev-service"

// GPUReleaseRef is the original accepted identity; no quota amounts enter it.
type GPUReleaseRef struct {
	TenantID          string `json:"tenant_id"`
	OwnerService      string `json:"owner_service"`
	ResourceID        string `json:"resource_id"`
	CreateOperationID string `json:"create_operation_id"`
}

type GPUReleasePodRef struct {
	ClusterID     string `json:"cluster_id"`
	Namespace     string `json:"namespace"`
	Name          string `json:"name"`
	UID           string `json:"uid"`
	ContainerName string `json:"container_name"`
}

// GPUReleaseScope is frozen once, after creation and required API cleanup are
// durably complete. The close UUID is independently persisted by the aggregate.
type GPUReleaseScope struct {
	Ref              GPUReleaseRef      `json:"ref"`
	CloseOperationID string             `json:"close_operation_id"`
	ClusterID        string             `json:"cluster_id"`
	SealedAt         time.Time          `json:"sealed_at"`
	Pods             []GPUReleasePodRef `json:"pods"`
}

// GPUReleaseScopeSHA256 is an internal immutable request-scope identity. It is
// separate from ACC's public owner signature and physical evidence digest.
func GPUReleaseScopeSHA256(scope GPUReleaseScope) (string, error) {
	raw, err := json.Marshal(scope)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("modeldev-gpu-release-scope-v1\n"), raw...))
	return hex.EncodeToString(sum[:]), nil
}

type GPUReleaseObservation struct {
	Ref               GPUReleaseRef `json:"ref"`
	Finding           string        `json:"finding"`
	Complete          bool          `json:"complete"`
	EvidenceID        string        `json:"evidence_id"`
	EvidenceDigest    string        `json:"evidence_digest"`
	ObservedAt        time.Time     `json:"observed_at"`
	ReceivedAt        time.Time     `json:"received_at"`
	ActivePodUIDs     []string      `json:"active_pod_uids"`
	UncertainNodeUIDs []string      `json:"uncertain_node_uids"`
	Issues            []string      `json:"issues"`
}

// GPUResourceReleasedEvent is the complete four-field Gov release request.
// Tenant, owner and quantities come from its authenticated original ledger.
type GPUResourceReleasedEvent struct {
	ReleaseEventID    string `json:"release_event_id"`
	CreateOperationID string `json:"create_operation_id"`
	ResourceID        string `json:"resource_id"`
	Reason            string `json:"reason"`
}

type GPUReleaseACK struct {
	ReleaseEventID    string `json:"release_event_id"`
	CreateOperationID string `json:"create_operation_id"`
	ResourceID        string `json:"resource_id"`
	Status            string `json:"status"`
}

type GPUReleaseOutbox struct {
	Event       GPUResourceReleasedEvent `json:"event"`
	CreatedAt   time.Time                `json:"created_at"`
	ACK         *GPUReleaseACK           `json:"ack,omitempty"`
	DeliveredAt *time.Time               `json:"delivered_at,omitempty"`
}

type GPUReleaseState struct {
	Scope       GPUReleaseScope        `json:"scope"`
	ScopeSHA256 string                 `json:"scope_sha256"`
	NoDispatch  bool                   `json:"no_dispatch"`
	Observation *GPUReleaseObservation `json:"observation,omitempty"`
	Outbox      *GPUReleaseOutbox      `json:"outbox,omitempty"`
}

type GPUReleaseRepository interface {
	SealGPURelease(context.Context, string, string) (ExecutionRuntime, error)
	RecordGPUReleaseObservation(context.Context, string, string, string, GPUReleaseObservation) (ExecutionRuntime, error)
	RecordGPUReleaseACK(context.Context, string, string, string, GPUReleaseACK) (ExecutionRuntime, error)
}

type GPUReleaseObserver interface {
	ObserveGPURelease(context.Context, GPUReleaseScope) (GPUReleaseObservation, error)
}

type GPUReleaseReporter interface {
	ReportGPUResourceReleased(context.Context, GPUResourceReleasedEvent) (GPUReleaseACK, error)
}

// GPUReleaseReconciler is an extension of the existing owner close loop. Its
// repository commits the seal and outbox before any corresponding external call.
type GPUReleaseReconciler struct {
	repository GPUReleaseRepository
	observer   GPUReleaseObserver
	reporter   GPUReleaseReporter
}

func NewGPUReleaseReconciler(repository GPUReleaseRepository, observer GPUReleaseObserver, reporter GPUReleaseReporter) (*GPUReleaseReconciler, error) {
	if repository == nil || observer == nil || reporter == nil {
		return nil, ErrRuntimeNotReady
	}
	return &GPUReleaseReconciler{repository: repository, observer: observer, reporter: reporter}, nil
}

func (reconciler *GPUReleaseReconciler) Reconcile(ctx context.Context, tenant, execution string) (ExecutionRuntime, error) {
	if ctx == nil || !validAdmissionID(tenant) || !validAdmissionID(execution) || reconciler == nil || reconciler.repository == nil || reconciler.observer == nil || reconciler.reporter == nil {
		return ExecutionRuntime{}, ErrRuntimeNotReady
	}
	state, err := reconciler.repository.SealGPURelease(ctx, tenant, execution)
	if err != nil || state.GPURelease == nil {
		return state, err
	}
	fact := state.GPURelease
	if fact.Outbox == nil {
		observation, err := reconciler.observer.ObserveGPURelease(ctx, fact.Scope)
		if err != nil {
			return state, err
		}
		state, err = reconciler.repository.RecordGPUReleaseObservation(ctx, tenant, execution, fact.ScopeSHA256, observation)
		if err != nil {
			return state, err
		}
		fact = state.GPURelease
	}
	if fact == nil || fact.Outbox == nil || fact.Outbox.ACK != nil {
		return state, nil
	}
	ack, err := reconciler.reporter.ReportGPUResourceReleased(ctx, fact.Outbox.Event)
	if err != nil {
		return state, err
	}
	return reconciler.repository.RecordGPUReleaseACK(ctx, tenant, execution, fact.Outbox.Event.ReleaseEventID, ack)
}

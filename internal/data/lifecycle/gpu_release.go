package lifecycle

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	lifecyclesql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle/sqlc"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/pgvalue"
	"k8s.io/apimachinery/pkg/util/validation"
)

var _ biz.GPUReleaseRepository = (*Repository)(nil)

func gpuReleaseID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func gpuReleaseDigest(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == value
}

func gpuReleaseReady(execution biz.Execution, state biz.ExecutionRuntime) bool {
	if execution.Snapshot.ComputeOrigin.DeviceMode != "gpu" || execution.Snapshot.GPU == nil || execution.Snapshot.GPU.ValidateFor(execution.Snapshot.ComputeOrigin) != nil || state.ClosedAt == nil || state.CloseGeneration == 0 || !gpuReleaseID(state.CloseOperationID) || state.CloseOperationID == execution.OperationID || state.CloseEvidence == nil || state.CloseReviewReason != "" || !runtimeCreationSettled(state) {
		return false
	}
	if state.CloseEvidence.NoDispatch {
		return state.Workspace == nil && state.Training == nil && state.TrainingHandle == nil && state.Observation == nil && state.ComputeCleanup == nil
	}
	// readRuntime independently validates the complete committed close and the
	// original cleanup target/exit graph before this post-close extension runs.
	return state.ComputeCleanup != nil && state.ComputeCleanup.Phase == "APPLIED" && !state.ComputeCleanup.CompletedAt.IsZero() && state.StepExitCleanup != nil && state.StepExitCleanup.CompletedAt != nil
}

func gpuReleaseScope(execution biz.Execution, state biz.ExecutionRuntime, sealed time.Time) (biz.GPUReleaseScope, error) {
	if !gpuReleaseReady(execution, state) || sealed.IsZero() || sealed.Before(*state.ClosedAt) || state.ComputeCleanup != nil && sealed.Before(state.ComputeCleanup.CompletedAt) {
		return biz.GPUReleaseScope{}, biz.ErrRuntimeNotReady
	}
	scope := biz.GPUReleaseScope{Ref: biz.GPUReleaseRef{TenantID: execution.TenantID, OwnerService: biz.GPUOwnerService, ResourceID: execution.ExecutionID, CreateOperationID: execution.OperationID}, CloseOperationID: state.CloseOperationID, ClusterID: execution.Snapshot.Environment.ClusterID, SealedAt: sealed.UTC(), Pods: []biz.GPUReleasePodRef{}}
	if !gpuReleaseID(scope.ClusterID) {
		return biz.GPUReleaseScope{}, biz.ErrRuntimeConflict
	}
	if state.Training != nil && state.TrainingRejection == nil {
		if state.TrainingHandle == nil || state.Observation == nil || !state.Observation.WritersAbsent || state.Observation.Handle != *state.TrainingHandle {
			return biz.GPUReleaseScope{}, biz.ErrRuntimeConflict
		}
		seen := map[string]bool{}
		for _, resource := range state.Observation.Resources {
			if resource.Kind != "Pod" {
				continue
			}
			if resource.APIVersion != "v1" || resource.Namespace != execution.Snapshot.Environment.NamespaceName || len(validation.IsDNS1123Subdomain(resource.Name)) != 0 || !validOpaqueID(resource.UID) || seen[resource.UID] || !resource.Terminal || resource.ExitCode == nil {
				return biz.GPUReleaseScope{}, biz.ErrRuntimeConflict
			}
			seen[resource.UID] = true
			scope.Pods = append(scope.Pods, biz.GPUReleasePodRef{ClusterID: scope.ClusterID, Namespace: resource.Namespace, Name: resource.Name, UID: resource.UID, ContainerName: "node"})
		}
	}
	if len(scope.Pods) > 64 {
		return biz.GPUReleaseScope{}, biz.ErrRuntimeConflict
	}
	sort.Slice(scope.Pods, func(i, j int) bool {
		a, b := scope.Pods[i], scope.Pods[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.UID < b.UID
	})
	return scope, nil
}

func validGPUObservation(scope biz.GPUReleaseScope, observation biz.GPUReleaseObservation, now time.Time) bool {
	if observation.Ref != scope.Ref || !gpuReleaseID(observation.EvidenceID) || !gpuReleaseDigest(observation.EvidenceDigest) || observation.ObservedAt.IsZero() || observation.ReceivedAt.IsZero() || observation.ObservedAt.Year() < 1970 || observation.ReceivedAt.Year() < 1970 || observation.ObservedAt.Year() > 9999 || observation.ReceivedAt.Year() > 9999 || observation.ObservedAt.After(now.Add(5*time.Second)) || observation.ReceivedAt.After(now.Add(5*time.Second)) || observation.ReceivedAt.Before(observation.ObservedAt.Add(-5*time.Second)) {
		return false
	}
	for _, values := range [][]string{observation.ActivePodUIDs, observation.UncertainNodeUIDs, observation.Issues} {
		if len(values) > 4096 {
			return false
		}
		seen := map[string]bool{}
		for _, value := range values {
			if value == "" || len(value) > 256 || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") || seen[value] {
				return false
			}
			seen[value] = true
		}
	}
	if observation.Complete && (len(observation.UncertainNodeUIDs) != 0 || len(observation.Issues) != 0) {
		return false
	}
	switch observation.Finding {
	case "ALLOCATION_RELEASED":
		return observation.Complete && len(observation.ActivePodUIDs) == 0 && len(observation.UncertainNodeUIDs) == 0 && len(observation.Issues) == 0
	case "ALLOCATION_STILL_PRESENT":
		return len(observation.ActivePodUIDs) > 0
	case "RELEASE_UNKNOWN":
		return !observation.Complete
	default:
		return false
	}
}

func gpuReleaseReason(state biz.ExecutionRuntime) string {
	if state.CloseReason == "STEP_FAILED" || state.CloseReason == "DEADLINE" {
		return "ABORTED_CLEANED"
	}
	return "RESOURCE_RELEASED"
}

func validGPUACK(event biz.GPUResourceReleasedEvent, ack biz.GPUReleaseACK) bool {
	return ack.ReleaseEventID == event.ReleaseEventID && ack.CreateOperationID == event.CreateOperationID && ack.ResourceID == event.ResourceID && (ack.Status == "APPLIED" || ack.Status == "ALREADY_APPLIED")
}

// ValidGPUReleaseFacts validates JSON facts against the original accepted and
// closed aggregate. The reader also compares the independent SQL anchor below.
func ValidGPUReleaseFacts(execution biz.Execution, state biz.ExecutionRuntime, now time.Time) error {
	fact := state.GPURelease
	if fact == nil {
		return nil
	}
	if now.IsZero() || fact.Scope.SealedAt.After(now) || fact.NoDispatch != (state.CloseEvidence != nil && state.CloseEvidence.NoDispatch) {
		return biz.ErrPersistence
	}
	expected, err := gpuReleaseScope(execution, state, fact.Scope.SealedAt)
	if err != nil || !reflect.DeepEqual(expected, fact.Scope) {
		return biz.ErrPersistence
	}
	hash, err := biz.GPUReleaseScopeSHA256(expected)
	if err != nil || hash != fact.ScopeSHA256 {
		return biz.ErrPersistence
	}
	if fact.Observation != nil && !validGPUObservation(fact.Scope, *fact.Observation, now) {
		return biz.ErrPersistence
	}
	if fact.Outbox == nil {
		return nil
	}
	outbox := fact.Outbox
	if fact.Observation == nil || fact.Observation.Finding != "ALLOCATION_RELEASED" || !fact.Observation.Complete || !gpuReleaseID(outbox.Event.ReleaseEventID) || outbox.Event.CreateOperationID != execution.OperationID || outbox.Event.ResourceID != execution.ExecutionID || outbox.Event.Reason != gpuReleaseReason(state) || outbox.CreatedAt.Before(fact.Scope.SealedAt) || outbox.CreatedAt.After(now) || (outbox.ACK == nil) != (outbox.DeliveredAt == nil) {
		return biz.ErrPersistence
	}
	if outbox.ACK != nil && (!validGPUACK(outbox.Event, *outbox.ACK) || outbox.DeliveredAt.Before(outbox.CreatedAt) || outbox.DeliveredAt.After(now)) {
		return biz.ErrPersistence
	}
	return nil
}

func validateGPUReleaseAnchor(ctx context.Context, current *runtimeTransaction) error {
	if current.state.GPURelease == nil {
		return nil
	}
	anchor, err := current.queries.GetGPUReleaseAnchor(ctx, lifecyclesql.GetGPUReleaseAnchorParams{TenantID: current.tenantID, ExecutionID: current.executionID})
	if err != nil || !anchor.GpuReleaseSealedAt.Valid || !anchor.GpuReleaseScopeSha256.Valid || !anchor.GpuReleaseSealedAt.Time.Equal(current.state.GPURelease.Scope.SealedAt) || anchor.GpuReleaseScopeSha256.String != current.state.GPURelease.ScopeSHA256 {
		return biz.ErrPersistence
	}
	return nil
}

func (repository *Repository) gpuReleaseMutation(ctx context.Context, tenant, execution string, change func(*runtimeTransaction) (bool, error)) (biz.ExecutionRuntime, error) {
	tenantID, err := databaseID(tenant)
	if err != nil {
		return biz.ExecutionRuntime{}, err
	}
	executionID, err := databaseID(execution)
	if err != nil {
		return biz.ExecutionRuntime{}, err
	}
	if ctx == nil || repository == nil || repository.pool == nil {
		return biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	defer rollback(tx)
	queries := lifecyclesql.New(tx)
	if _, err = queries.LockRuntimeIdentity(ctx, lifecyclesql.LockRuntimeIdentityParams{TenantID: tenantID, ExecutionID: executionID}); err != nil {
		return biz.ExecutionRuntime{}, storageError(err)
	}
	current, err := readRuntime(ctx, tx, tenantID, executionID)
	if err != nil {
		return biz.ExecutionRuntime{}, err
	}
	if ValidGPUReleaseFacts(current.execution, current.state, current.now) != nil || validateGPUReleaseAnchor(ctx, current) != nil {
		return biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	initial := current.state.GPURelease == nil
	changed, err := change(current)
	if err != nil {
		return biz.ExecutionRuntime{}, err
	}
	if changed {
		if ValidGPUReleaseFacts(current.execution, current.state, current.now) != nil {
			return biz.ExecutionRuntime{}, biz.ErrPersistence
		}
		revision, err := queries.AdvanceRuntimeRevision(ctx, lifecyclesql.AdvanceRuntimeRevisionParams{TenantID: tenantID, ExecutionID: executionID})
		if err != nil {
			return biz.ExecutionRuntime{}, biz.ErrPersistence
		}
		value, ok := pgvalue.Uint64(revision)
		if !ok || value == 0 {
			return biz.ExecutionRuntime{}, biz.ErrPersistence
		}
		current.state.OwnerRevision = value
		if initial {
			raw, err := json.Marshal(current.state)
			if err != nil {
				return biz.ExecutionRuntime{}, biz.ErrPersistence
			}
			fact := current.state.GPURelease
			rows, err := queries.SealGPUReleaseFacts(ctx, lifecyclesql.SealGPUReleaseFactsParams{TenantID: tenantID, ExecutionID: executionID, SealedAt: timestamp(fact.Scope.SealedAt), ScopeSha256: fact.ScopeSHA256, Facts: raw})
			if err != nil || rows != 1 {
				return biz.ExecutionRuntime{}, biz.ErrPersistence
			}
		} else if err := saveRuntime(ctx, current); err != nil {
			return biz.ExecutionRuntime{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	return current.state, nil
}

func (repository *Repository) SealGPURelease(ctx context.Context, tenant, execution string) (biz.ExecutionRuntime, error) {
	return repository.gpuReleaseMutation(ctx, tenant, execution, func(current *runtimeTransaction) (bool, error) {
		if current.execution.Snapshot.ComputeOrigin.DeviceMode == "cpu" {
			if current.state.GPURelease != nil {
				return false, biz.ErrPersistence
			}
			return false, nil
		}
		if current.state.GPURelease != nil {
			return false, nil
		}
		scope, err := gpuReleaseScope(current.execution, current.state, current.now)
		if err != nil {
			return false, err
		}
		hash, err := biz.GPUReleaseScopeSHA256(scope)
		if err != nil {
			return false, biz.ErrPersistence
		}
		current.state.GPURelease = &biz.GPUReleaseState{Scope: scope, ScopeSHA256: hash, NoDispatch: current.state.CloseEvidence.NoDispatch}
		return true, nil
	})
}

func (repository *Repository) RecordGPUReleaseObservation(ctx context.Context, tenant, execution, scopeHash string, observation biz.GPUReleaseObservation) (biz.ExecutionRuntime, error) {
	return repository.gpuReleaseMutation(ctx, tenant, execution, func(current *runtimeTransaction) (bool, error) {
		fact := current.state.GPURelease
		if fact == nil || scopeHash != fact.ScopeSHA256 || !validGPUObservation(fact.Scope, observation, current.now) {
			return false, biz.ErrRuntimeConflict
		}
		if fact.Outbox != nil {
			return false, nil
		}
		if fact.Observation != nil {
			same := observation
			same.EvidenceID = fact.Observation.EvidenceID
			if reflect.DeepEqual(same, *fact.Observation) {
				return false, nil
			}
		}
		fact.Observation = &observation
		if observation.Finding == "ALLOCATION_RELEASED" && observation.Complete {
			eventID, err := uuid.NewRandom()
			if err != nil {
				return false, biz.ErrPersistence
			}
			fact.Outbox = &biz.GPUReleaseOutbox{Event: biz.GPUResourceReleasedEvent{ReleaseEventID: eventID.String(), CreateOperationID: current.execution.OperationID, ResourceID: current.execution.ExecutionID, Reason: gpuReleaseReason(current.state)}, CreatedAt: current.now}
		}
		return true, nil
	})
}

func (repository *Repository) RecordGPUReleaseACK(ctx context.Context, tenant, execution, eventID string, ack biz.GPUReleaseACK) (biz.ExecutionRuntime, error) {
	return repository.gpuReleaseMutation(ctx, tenant, execution, func(current *runtimeTransaction) (bool, error) {
		fact := current.state.GPURelease
		if fact == nil || fact.Outbox == nil || eventID != fact.Outbox.Event.ReleaseEventID || !validGPUACK(fact.Outbox.Event, ack) {
			return false, biz.ErrRuntimeConflict
		}
		if fact.Outbox.ACK != nil {
			return false, nil
		}
		fact.Outbox.ACK = &ack
		delivered := current.now
		fact.Outbox.DeliveredAt = &delivered
		return true, nil
	})
}

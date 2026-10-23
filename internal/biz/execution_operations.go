package biz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

// OperationRecord is one consistent durable view of the execution aggregate.
type OperationRecord struct {
	QueryRecord
	Dispatch  *PipelineDispatch
	Authority *RunAuthorityCandidate
	Cleanup   *CleanupReceipt
}

type ExecutionOperationsRepository interface {
	GetOperationRecord(context.Context, string, string) (OperationRecord, error)
	ReserveCleanup(context.Context, CleanupPlan, string) (CleanupReceipt, bool, error)
	ExecuteCleanup(context.Context, string, string, string, func(OperationRecord) (CleanupReceipt, error)) (CleanupReceipt, error)
	ReconcileCleanup(context.Context, string, string, string, func(OperationRecord) (CleanupReceipt, error)) (CleanupReceipt, error)
}

type ExecutionInspect struct {
	TenantID               string                `json:"tenant_id"`
	ExecutionID            string                `json:"execution_id"`
	OperationID            string                `json:"operation_id"`
	SpecHash               string                `json:"execution_spec_hash"`
	OwnerRevision          uint64                `json:"owner_revision"`
	States                 ExecutionStates       `json:"states"`
	NamespaceName          string                `json:"namespace"`
	NamespaceUID           string                `json:"namespace_uid"`
	RunID                  string                `json:"run_id,omitempty"`
	WorkflowUID            string                `json:"workflow_uid,omitempty"`
	PVCUID                 string                `json:"pvc_uid,omitempty"`
	Resources              []RuntimeResource     `json:"resources"`
	DispatchState          PipelineDispatchState `json:"dispatch_state,omitempty"`
	RunCreateInFlight      bool                  `json:"run_create_in_flight"`
	TrainingCreateInFlight bool                  `json:"training_create_in_flight"`
	RuntimeCreateInFlight  bool                  `json:"runtime_create_in_flight"`
	RuntimeReviewReason    string                `json:"runtime_review_reason,omitempty"`
	RuntimeNextAction      string                `json:"runtime_next_action,omitempty"`
	CloseOperationID       string                `json:"close_operation_id,omitempty"`
	CloseGeneration        uint64                `json:"close_generation"`
	CloseReason            string                `json:"close_reason,omitempty"`
	CloseReviewReason      string                `json:"close_review_reason,omitempty"`
	ClosedAt               *time.Time            `json:"closed_at,omitempty"`
	PublicationID          string                `json:"publication_id,omitempty"`
	ObservedAt             time.Time             `json:"observed_at"`
	RetrievedAt            time.Time             `json:"retrieved_at"`
	ObservationStale       bool                  `json:"observation_stale"`
	ComputeOutcome         string                `json:"compute_outcome,omitempty"`
	CleanupPhase           string                `json:"cleanup_phase,omitempty"`
	CleanupPlanHash        string                `json:"cleanup_plan_sha256,omitempty"`
}

type ExecutionOperations struct {
	repository ExecutionOperationsRepository
	closer     *ExecutionCloser
	cleanup    ExecutionCleanupBoundary
}

func NewManagedExecutionOperations(repository ExecutionOperationsRepository, closer *ExecutionCloser, cleanup ExecutionCleanupBoundary) (*ExecutionOperations, error) {
	if repository == nil || closer == nil || cleanup == nil {
		return nil, ErrRuntimeNotReady
	}
	return &ExecutionOperations{repository: repository, closer: closer, cleanup: cleanup}, nil
}

func NewExecutionOperations(repository ExecutionOperationsRepository) *ExecutionOperations {
	return &ExecutionOperations{repository: repository}
}

func (operations *ExecutionOperations) Inspect(ctx context.Context, tenant, execution string) (ExecutionInspect, error) {
	if ctx == nil || !validAdmissionID(tenant) || !validAdmissionID(execution) || strings.ToLower(tenant) != tenant || strings.ToLower(execution) != execution {
		return ExecutionInspect{}, ErrInvalidAdmission
	}
	if operations == nil || operations.repository == nil {
		return ExecutionInspect{}, ErrRuntimeNotReady
	}
	record, err := operations.repository.GetOperationRecord(ctx, tenant, execution)
	if err != nil {
		return ExecutionInspect{}, err
	}
	admitted, runtime := record.Execution, record.Runtime
	if admitted.TenantID != tenant || admitted.ExecutionID != execution || admitted.OwnerRevision != runtime.OwnerRevision {
		return ExecutionInspect{}, ErrRuntimeConflict
	}
	view := ExecutionInspect{TenantID: tenant, ExecutionID: execution, OperationID: admitted.OperationID, SpecHash: admitted.SpecHash,
		OwnerRevision: admitted.OwnerRevision, States: admitted.States, NamespaceName: admitted.Snapshot.Environment.NamespaceName,
		NamespaceUID: admitted.Snapshot.Environment.NamespaceUID, CloseGeneration: runtime.CloseGeneration, CloseReason: runtime.CloseReason,
		CloseReviewReason: runtime.CloseReviewReason, ClosedAt: runtime.ClosedAt, RetrievedAt: time.Now().UTC(), Resources: []RuntimeResource{}}
	if record.Authority != nil {
		view.RunID, view.WorkflowUID = record.Authority.RunID, record.Authority.WorkflowUID
	}
	if record.Dispatch != nil {
		view.DispatchState = record.Dispatch.State
		view.RunCreateInFlight = record.Dispatch.State == PipelineDispatchSubmitting || record.Dispatch.State == PipelineDispatchUncertain
	}
	view.TrainingCreateInFlight = runtime.Training != nil && runtime.TrainingHandle == nil
	view.CloseOperationID = runtime.CloseOperationID
	if fact := runtime.RuntimeTemplate; fact != nil {
		view.RuntimeCreateInFlight = fact.Binding == nil && len(fact.Attempts) > 0 && fact.Attempts[len(fact.Attempts)-1].NotSentAt == nil
		view.RuntimeReviewReason, view.RuntimeNextAction = fact.ReviewReason, fact.NextAction
		if fact.Binding != nil {
			view.Resources = append(view.Resources, RuntimeResource{APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainingRuntime", Namespace: view.NamespaceName, Name: fact.Binding.Name, UID: fact.Binding.UID})
		}
	}
	if runtime.Workspace != nil {
		view.PVCUID = runtime.Workspace.PVCUID
	}
	if runtime.Observation != nil {
		view.Resources = append(view.Resources, runtime.Observation.Resources...)
		view.ComputeOutcome, view.ObservedAt = runtime.Observation.Outcome, runtime.Observation.ObservedAt
	}
	if runtime.Observation == nil && runtime.Training != nil && runtime.TrainingHandle != nil {
		view.Resources = append(view.Resources, RuntimeResource{APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainJob", Namespace: runtime.Training.Workspace.NamespaceName, Name: runtime.Training.Name, UID: runtime.TrainingHandle.TrainJobUID})
	}
	if runtime.CloseEvidence != nil {
		view.Resources = append(view.Resources, runtime.CloseEvidence.Resources...)
		if runtime.CloseEvidence.ObservedAt.After(view.ObservedAt) {
			view.ObservedAt = runtime.CloseEvidence.ObservedAt
		}
	}
	if runtime.Publication != nil {
		view.PublicationID = runtime.Publication.ID
	}
	if record.Cleanup != nil {
		view.CleanupPhase, view.CleanupPlanHash = record.Cleanup.Phase, record.Cleanup.PlanHash
	}
	// This read returns durable observations, not a fresh successful upstream
	// observation. No last-retrieval time can refresh those facts.
	view.ObservationStale = view.ObservedAt.IsZero() || view.RetrievedAt.Sub(view.ObservedAt) > time.Minute
	return view, nil
}

// Reconcile only resumes the existing owner close path; it grants no new
// creation permit and cannot set a caller-selected computation state.
func (operations *ExecutionOperations) Reconcile(ctx context.Context, tenant, execution string) (ExecutionInspect, error) {
	return operations.reconcile(ctx, tenant, execution, "")
}

// ReconcileWithActor is the existing authenticated operator path. Only this
// explicit call may admit a single exhausted pre-DELETE cleanup recovery.
func (operations *ExecutionOperations) ReconcileWithActor(ctx context.Context, tenant, execution, actor string) (ExecutionInspect, error) {
	if !validAuditActor(actor) {
		return ExecutionInspect{}, ErrInvalidAdmission
	}
	return operations.reconcile(ctx, tenant, execution, actor)
}

func (operations *ExecutionOperations) reconcile(ctx context.Context, tenant, execution, actor string) (ExecutionInspect, error) {
	if _, err := operations.Inspect(ctx, tenant, execution); err != nil {
		return ExecutionInspect{}, err
	}
	if operations.closer == nil {
		return ExecutionInspect{}, ErrRuntimeNotReady
	}
	if actor != "" {
		record, err := operations.repository.GetOperationRecord(ctx, tenant, execution)
		if err != nil {
			return ExecutionInspect{}, err
		}
		if record.Authority != nil && ComputeCleanupRetryEligible(record.Execution, *record.Authority, record.Runtime) {
			repository, ok := operations.closer.runtime.repository.(ComputeCleanupOperatorRepository)
			if !ok || operations.closer.compute == nil {
				return ExecutionInspect{}, ErrRuntimeNotReady
			}
			preflight, cancel := context.WithTimeout(ctx, ComputeCleanupOperatorRetryWindow)
			logs, err := operations.closer.compute.CaptureComputeCleanupLogs(preflight, record.Execution, *record.Authority, record.Runtime)
			if err == nil {
				_, err = repository.AdmitComputeCleanupRetry(preflight, *record.Authority, record.Runtime.CloseGeneration, actor, logs)
			}
			cancel()
			if err != nil {
				return ExecutionInspect{}, err
			}
		}
	}
	if _, err := operations.closer.Reconcile(ctx, tenant, execution); err != nil {
		return ExecutionInspect{}, err
	}
	record, err := operations.repository.GetOperationRecord(ctx, tenant, execution)
	if err != nil {
		return ExecutionInspect{}, err
	}
	if record.Cleanup != nil && (record.Cleanup.Phase == "STARTED" || record.Cleanup.Phase == "NEEDS_REVIEW") {
		if operations.cleanup == nil {
			return ExecutionInspect{}, ErrRuntimeNotReady
		}
		// Resolve the original audit by observation only. An interrupted apply
		// never grants another DELETE; exact-UID maintenance is a separate act.
		_, err = operations.repository.ReconcileCleanup(ctx, tenant, execution, record.Cleanup.PlanHash, func(current OperationRecord) (CleanupReceipt, error) {
			receipt := *current.Cleanup
			absent, err := operations.cleanup.VerifyCleanupResolved(ctx, current)
			if err != nil {
				return receipt, err
			}
			receipt.ReconciledFromPhase = receipt.Phase
			receipt.Phase = "RECONCILED"
			receipt.ResolvedAbsent = absent
			return receipt, nil
		})
		if err != nil {
			return ExecutionInspect{}, err
		}
	}
	return operations.Inspect(ctx, tenant, execution)
}

var (
	ErrCleanupBlocked   = errors.New("EXECUTION_CLEANUP_BLOCKED")
	ErrCleanupConflict  = errors.New("EXECUTION_CLEANUP_PLAN_CONFLICT")
	ErrCleanupUncertain = errors.New("EXECUTION_CLEANUP_NEEDS_REVIEW")
)

// Cleanup retains the work volume, all Pods/Workflow, immutable publication
// bytes, and durable execution facts. Only completed controller objects are
// targets; Kubernetes Orphan propagation preserves their dependent evidence.
type CleanupPlan struct {
	TenantID        string            `json:"tenant_id"`
	ExecutionID     string            `json:"execution_id"`
	OperationID     string            `json:"operation_id"`
	SpecHash        string            `json:"execution_spec_hash"`
	OwnerRevision   uint64            `json:"owner_revision"`
	CloseGeneration uint64            `json:"close_generation"`
	NamespaceUID    string            `json:"namespace_uid"`
	PublicationID   string            `json:"publication_id"`
	Targets         []RuntimeResource `json:"targets"`
	PlanHash        string            `json:"plan_sha256"`
	Retained        []string          `json:"retained"`
	ObservedAt      time.Time         `json:"observed_at"`
}

type CleanupReceipt struct {
	ExecutionID     string            `json:"execution_id"`
	PlanHash        string            `json:"plan_sha256"`
	Phase           string            `json:"phase"`
	Requested       []RuntimeResource `json:"requested"`
	ConfirmedAbsent []RuntimeResource `json:"confirmed_absent"`
	Actor           string            `json:"actor"`
	StartedAt       time.Time         `json:"started_at"`
	CompletedAt     time.Time         `json:"completed_at"`
	// Reconciliation preserves the first apply's request/result and timestamps.
	// These fields record a later observation, never a replayed DELETE.
	ResolvedAbsent      []RuntimeResource `json:"resolved_absent,omitempty"`
	ReconciledAt        time.Time         `json:"reconciled_at,omitempty"`
	ReconciledFromPhase string            `json:"reconciled_from_phase,omitempty"`
}

type ExecutionCleanupBoundary interface {
	VerifyCleanupWritersAbsent(context.Context, OperationRecord) error
	DeleteExecutionResource(context.Context, OperationRecord, RuntimeResource) error
	VerifyCleanupResolved(context.Context, OperationRecord) ([]RuntimeResource, error)
}

func CleanupPlanFor(record OperationRecord) (CleanupPlan, error) {
	admitted, runtime := record.Execution, record.Runtime
	automatic := ComputeCleanupApplied(record)
	if admitted.States.Close != CloseStateClosed || runtime.ClosedAt == nil || runtime.CloseGeneration == 0 || runtime.CloseReviewReason != "" || runtime.CloseEvidence == nil || admitted.OwnerRevision != runtime.OwnerRevision {
		return CleanupPlan{}, ErrCleanupBlocked
	}
	if record.Dispatch != nil && (record.Dispatch.State != PipelineDispatchConfirmed || len(record.Dispatch.ConfirmedRuns) != 1) {
		return CleanupPlan{}, ErrCleanupBlocked
	}
	if runtime.Training != nil && !automatic && (runtime.TrainingHandle == nil || runtime.Observation == nil || !runtime.Observation.WritersAbsent || runtime.Publication == nil || admitted.States.Delivery != DeliveryStatePublished) {
		// A stopped execution can still own the only unpublished output. Retain
		// all of its resources until publication/retention is proved separately.
		return CleanupPlan{}, ErrCleanupBlocked
	}
	plan := CleanupPlan{TenantID: admitted.TenantID, ExecutionID: admitted.ExecutionID, OperationID: admitted.OperationID,
		SpecHash: admitted.SpecHash, OwnerRevision: admitted.OwnerRevision, CloseGeneration: runtime.CloseGeneration,
		NamespaceUID: admitted.Snapshot.Environment.NamespaceUID, Targets: []RuntimeResource{}, Retained: []string{"PVC", "Workflow", "Pod", "S3 publication", "execution and audit records"}, ObservedAt: runtime.CloseEvidence.ObservedAt}
	if automatic {
		plan.Retained = []string{"PVC", "S3 publication", "execution and audit records", "recorded training log tails"}
	}
	if runtime.Publication != nil {
		plan.PublicationID = runtime.Publication.ID
	}
	if runtime.Observation != nil && !automatic {
		seen := map[string]bool{}
		for _, resource := range runtime.Observation.Resources {
			if resource.Kind == "Pod" || !resource.APIObjectPresent {
				continue
			}
			valid := resource.Kind == "TrainJob" && resource.APIVersion == "trainer.kubeflow.org/v1alpha1" || resource.Kind == "JobSet" && resource.APIVersion == "jobset.x-k8s.io/v1alpha2" || resource.Kind == "Job" && resource.APIVersion == "batch/v1"
			if !valid || resource.UID == "" || resource.Name == "" || resource.Namespace != admitted.Snapshot.Environment.NamespaceName || (!resource.Terminal && !resource.CreationDisabled) || seen[resource.UID] {
				return CleanupPlan{}, ErrCleanupBlocked
			}
			seen[resource.UID] = true
			plan.Targets = append(plan.Targets, resource)
		}
	}
	if len(plan.Targets) > 64 {
		return CleanupPlan{}, ErrCleanupBlocked
	}
	// Preserve the existing canonical target order for reviewed plan hashes.
	// ApplyCleanup dispatches parent-first; Orphan retains Pod/Workflow evidence
	// and the workspace.
	rank := map[string]int{"Job": 0, "JobSet": 1, "TrainJob": 2}
	sort.Slice(plan.Targets, func(i, j int) bool {
		if rank[plan.Targets[i].Kind] != rank[plan.Targets[j].Kind] {
			return rank[plan.Targets[i].Kind] < rank[plan.Targets[j].Kind]
		}
		return plan.Targets[i].UID < plan.Targets[j].UID
	})
	canonical := plan
	canonical.ObservedAt = time.Time{}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return CleanupPlan{}, ErrCleanupBlocked
	}
	digest := sha256.Sum256(encoded)
	plan.PlanHash = hex.EncodeToString(digest[:])
	return plan, nil
}

func (operations *ExecutionOperations) PlanCleanup(ctx context.Context, tenant, execution string) (CleanupPlan, error) {
	if _, err := operations.Inspect(ctx, tenant, execution); err != nil {
		return CleanupPlan{}, err
	}
	record, err := operations.repository.GetOperationRecord(ctx, tenant, execution)
	if err != nil {
		return CleanupPlan{}, err
	}
	plan, err := CleanupPlanFor(record)
	if err != nil {
		return CleanupPlan{}, err
	}
	if operations.cleanup == nil {
		return CleanupPlan{}, ErrRuntimeNotReady
	}
	if err := operations.cleanup.VerifyCleanupWritersAbsent(ctx, record); err != nil {
		return CleanupPlan{}, err
	}
	return plan, nil
}

func (operations *ExecutionOperations) ApplyCleanup(ctx context.Context, tenant, execution, expectedHash, actor string) (CleanupReceipt, error) {
	if !closeSpecHashPattern.MatchString(expectedHash) || !validAuditActor(actor) {
		return CleanupReceipt{}, ErrInvalidAdmission
	}
	// A previous UNKNOWN outcome is not permission to retry deletion. Inspect
	// exposes its retained receipt; only an already APPLIED receipt is replayed.
	prior, err := operations.repository.GetOperationRecord(ctx, tenant, execution)
	if err != nil {
		return CleanupReceipt{}, err
	}
	if prior.Cleanup != nil {
		if prior.Cleanup.PlanHash != expectedHash {
			return *prior.Cleanup, ErrCleanupConflict
		}
		if prior.Cleanup.Phase == "APPLIED" || prior.Cleanup.Phase == "RECONCILED" {
			return *prior.Cleanup, nil
		}
		return *prior.Cleanup, ErrCleanupUncertain
	}
	plan, err := operations.PlanCleanup(ctx, tenant, execution)
	if err != nil {
		return CleanupReceipt{}, err
	}
	if plan.PlanHash != expectedHash {
		return CleanupReceipt{}, ErrCleanupConflict
	}
	reserved, replay, err := operations.repository.ReserveCleanup(ctx, plan, actor)
	if err != nil {
		return CleanupReceipt{}, err
	}
	if replay {
		if reserved.Phase == "APPLIED" || reserved.Phase == "RECONCILED" {
			return reserved, nil
		}
		return reserved, ErrCleanupUncertain
	}
	return operations.repository.ExecuteCleanup(ctx, tenant, execution, expectedHash, func(record OperationRecord) (CleanupReceipt, error) {
		receipt := reserved
		current, err := CleanupPlanFor(record)
		if err != nil || current.PlanHash != expectedHash {
			receipt.Phase = "NEEDS_REVIEW"
			return receipt, ErrCleanupConflict
		}
		if err := operations.cleanup.VerifyCleanupWritersAbsent(ctx, record); err != nil {
			receipt.Phase = "NEEDS_REVIEW"
			return receipt, err
		}
		// Remove reconciling parents before their dependents so a completed
		// TrainJob cannot recreate a deleted JobSet or Job. Keep the reviewed
		// plan's canonical target order and hash unchanged.
		deleteTargets := append([]RuntimeResource{}, current.Targets...)
		rank := map[string]int{"TrainJob": 0, "JobSet": 1, "Job": 2}
		sort.Slice(deleteTargets, func(i, j int) bool {
			if rank[deleteTargets[i].Kind] != rank[deleteTargets[j].Kind] {
				return rank[deleteTargets[i].Kind] < rank[deleteTargets[j].Kind]
			}
			return deleteTargets[i].UID < deleteTargets[j].UID
		})
		for _, resource := range deleteTargets {
			receipt.Requested = append(receipt.Requested, resource)
			if err := operations.cleanup.DeleteExecutionResource(ctx, record, resource); err != nil {
				receipt.Phase = "NEEDS_REVIEW"
				return receipt, err
			}
			receipt.ConfirmedAbsent = append(receipt.ConfirmedAbsent, resource)
		}
		receipt.Phase = "APPLIED"
		return receipt, nil
	})
}

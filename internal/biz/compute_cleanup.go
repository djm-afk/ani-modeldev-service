package biz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"
	"time"
)

const ComputeCleanupActor = "modeldev:compute-cleanup"
const ComputeCleanupMaximumAttempts = 3

// ComputeCleanupReceipt extends the existing cleanup audit for the automatic
// post-close owner path. Targets and bounded logs commit before any DELETE;
// ConfirmedAbsent never substitutes for the original persisted process exits.
type ComputeCleanupReceipt struct {
	CleanupReceipt
	CloseGeneration uint64                       `json:"close_generation"`
	Targets         []RuntimeResource            `json:"targets"`
	Logs            []RecordedTrainingLog        `json:"logs"`
	LogsReady       bool                         `json:"logs_ready"`
	Attempts        uint32                       `json:"attempts"`
	LastError       string                       `json:"last_error,omitempty"`
	OperatorRetry   *ComputeCleanupOperatorRetry `json:"operator_retry,omitempty"`
}

// ComputeCleanupOperatorRetry preserves the exhausted pre-DELETE audit. It
// authorizes one additional original-scope attempt, never a renewed budget.
type ComputeCleanupOperatorRetry struct {
	Actor            string    `json:"actor"`
	AdmittedAt       time.Time `json:"admitted_at"`
	DeadlineAt       time.Time `json:"deadline_at"`
	OriginalAttempts uint32    `json:"original_attempts"`
	OriginalPhase    string    `json:"original_phase"`
	OriginalError    string    `json:"original_error"`
}

const ComputeCleanupOperatorRetryWindow = 2 * time.Minute

func ComputeCleanupAttemptLimit(receipt *ComputeCleanupReceipt) uint32 {
	if receipt != nil && receipt.OperatorRetry != nil {
		return ComputeCleanupMaximumAttempts + 1
	}
	return ComputeCleanupMaximumAttempts
}

func ComputeCleanupRetryEligible(execution Execution, owner RunAuthorityCandidate, state ExecutionRuntime) bool {
	r := state.ComputeCleanup
	if r == nil || r.OperatorRetry != nil || r.Attempts != ComputeCleanupMaximumAttempts || r.Phase != "NEEDS_REVIEW" || r.LastError != "ATTEMPTS_EXHAUSTED" || r.LogsReady || len(r.Logs) != 0 || len(r.Requested) != 0 || len(r.ConfirmedAbsent) != 0 {
		return false
	}
	targets, hash, err := ComputeCleanupTargets(execution, owner, state)
	return err == nil && r.CloseGeneration == state.CloseGeneration && r.PlanHash == hash && reflect.DeepEqual(r.Targets, targets)
}

type ComputeCleanupOperatorRepository interface {
	AdmitComputeCleanupRetry(context.Context, RunAuthorityCandidate, uint64, string, []RecordedTrainingLog) (ExecutionRuntime, error)
}

type ComputeCleanupRepository interface {
	BeginComputeCleanup(context.Context, RunAuthorityCandidate, uint64) (ExecutionRuntime, error)
	StartComputeCleanupAttempt(context.Context, RunAuthorityCandidate, uint64) (ExecutionRuntime, error)
	RecordComputeCleanupLogs(context.Context, RunAuthorityCandidate, uint64, []RecordedTrainingLog) (ExecutionRuntime, error)
	RecordComputeCleanupResult(context.Context, RunAuthorityCandidate, uint64, uint32, []RuntimeResource, string) (ExecutionRuntime, error)
}

type ComputeCleanupBoundary interface {
	CaptureComputeCleanupLogs(context.Context, Execution, RunAuthorityCandidate, ExecutionRuntime) ([]RecordedTrainingLog, error)
	ReconcileComputeCleanup(context.Context, Execution, RunAuthorityCandidate, ExecutionRuntime) ([]RuntimeResource, error)
}

type RecordedComputeWriterVerifier interface {
	VerifyRecordedComputeWritersAbsent(context.Context, Execution, RunAuthorityCandidate, ExecutionRuntime) error
}

func ComputeCleanupApplied(record OperationRecord) bool {
	receipt := record.Runtime.ComputeCleanup
	if receipt == nil || receipt.Phase != "APPLIED" || !receipt.LogsReady || receipt.CompletedAt.IsZero() || record.Authority == nil {
		return false
	}
	targets, hash, err := ComputeCleanupTargets(record.Execution, *record.Authority, record.Runtime)
	return err == nil && hash == receipt.PlanHash && receipt.CloseGeneration == record.Runtime.CloseGeneration && reflect.DeepEqual(targets, receipt.Targets) && reflect.DeepEqual(targets, receipt.Requested) && ComputeCleanupRemovedValid(targets, receipt.ConfirmedAbsent, true) && ComputeCleanupLogsComplete(record.Execution, record.Runtime, receipt.Logs)
}

// ComputeCleanupTargets freezes only original terminal compute identities. The
// complete Workflow/Pod exit graph and training history stay in runtime facts.
func ComputeCleanupTargets(execution Execution, owner RunAuthorityCandidate, state ExecutionRuntime) ([]RuntimeResource, string, error) {
	if _, _, err := execution.CanonicalPayloads(); err != nil || state.ClosedAt == nil || state.CloseGeneration == 0 || state.CloseReviewReason != "" || state.CloseEvidence == nil || state.CloseEvidence.NoDispatch || state.StepExitCleanup == nil || state.StepExitCleanup.CompletedAt == nil || state.StepExitCleanup.CloseGeneration != state.CloseGeneration || state.StepExitCleanup.Evidence.OwnerTermination == nil || state.StepExitCleanup.Evidence.RunID != owner.RunID || state.StepExitCleanup.Evidence.WorkflowUID != owner.WorkflowUID || owner.WorkflowName == "" || owner.WorkflowUID == "" || owner.TenantID != execution.TenantID || owner.ExecutionID != execution.ExecutionID || owner.OperationID != execution.OperationID || owner.SpecHash != execution.SpecHash || owner.NamespaceName != execution.Snapshot.Environment.NamespaceName || owner.NamespaceUID != execution.Snapshot.Environment.NamespaceUID {
		return nil, "", ErrCleanupBlocked
	}
	targets := []RuntimeResource{}
	if state.Training != nil && state.TrainingRejection == nil {
		if state.Workspace == nil || state.TrainingHandle == nil || state.Observation == nil || !state.Observation.WritersAbsent || state.Observation.Handle != *state.TrainingHandle || len(state.Observation.Resources) > 64 {
			return nil, "", ErrCleanupBlocked
		}
		byUID := map[string]RuntimeResource{}
		for _, resource := range state.Observation.Resources {
			valid := resource.Kind == "Pod" && resource.APIVersion == "v1" && resource.Terminal && resource.ExitCode != nil || resource.Kind == "TrainJob" && resource.APIVersion == "trainer.kubeflow.org/v1alpha1" && (resource.Terminal || resource.CreationDisabled) || resource.Kind == "JobSet" && resource.APIVersion == "jobset.x-k8s.io/v1alpha2" && (resource.Terminal || resource.CreationDisabled) || resource.Kind == "Job" && resource.APIVersion == "batch/v1" && (resource.Terminal || resource.CreationDisabled)
			valid = valid || resource.Kind == "PodGroup" && resource.APIVersion == "scheduling.volcano.sh/v1beta1" && resource.OwnerUID == state.TrainingHandle.TrainJobUID && resource.Name == state.Training.Name && resource.Terminal
			if !valid || resource.Name == "" || resource.UID == "" || resource.Namespace != owner.NamespaceName || byUID[resource.UID].UID != "" {
				return nil, "", ErrCleanupBlocked
			}
			byUID[resource.UID] = resource
			if resource.APIObjectPresent {
				targets = append(targets, resource)
			}
		}
		train := byUID[state.TrainingHandle.TrainJobUID]
		if train.Kind != "TrainJob" || train.Name != state.Training.Name || train.OwnerUID != "" {
			return nil, "", ErrCleanupBlocked
		}
		for _, resource := range byUID {
			parent := byUID[resource.OwnerUID]
			if resource.Kind == "Pod" && parent.Kind != "Job" || resource.Kind == "Job" && parent.Kind != "JobSet" || resource.Kind == "JobSet" && (parent.Kind != "TrainJob" || parent.UID != train.UID) {
				return nil, "", ErrCleanupBlocked
			}
		}
	}
	targets = append(targets, RuntimeResource{APIVersion: "argoproj.io/v1alpha1", Kind: "Workflow", Namespace: owner.NamespaceName, Name: owner.WorkflowName, UID: owner.WorkflowUID, APIObjectPresent: true, CreationDisabled: true, Terminal: true})
	if fact := state.RuntimeTemplate; fact != nil && fact.Binding != nil {
		targets = append(targets, RuntimeResource{APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainingRuntime", Namespace: owner.NamespaceName, Name: fact.Binding.Name, UID: fact.Binding.UID, APIObjectPresent: true, CreationDisabled: true, Terminal: true})
	}
	// Keep Workflow until the other removals finish: its complete original node
	// graph remains available for independent verification throughout cleanup.
	rank := map[string]int{"TrainJob": 0, "JobSet": 1, "Job": 2, "Pod": 3, "PodGroup": 4, "Workflow": 5, "TrainingRuntime": 6}
	sort.Slice(targets, func(i, j int) bool {
		if rank[targets[i].Kind] != rank[targets[j].Kind] {
			return rank[targets[i].Kind] < rank[targets[j].Kind]
		}
		return targets[i].UID < targets[j].UID
	})
	material := struct {
		Owner      RunAuthorityCandidate
		Generation uint64
		Targets    []RuntimeResource
	}{owner, state.CloseGeneration, targets}
	raw, err := json.Marshal(material)
	if err != nil {
		return nil, "", ErrCleanupConflict
	}
	digest := sha256.Sum256(raw)
	return targets, hex.EncodeToString(digest[:]), nil
}

func ComputeCleanupLogsComplete(execution Execution, state ExecutionRuntime, logs []RecordedTrainingLog) bool {
	if ValidateRecordedTrainingLogs(QueryRecord{Execution: execution, Runtime: state}, logs) != nil {
		return false
	}
	pods := 0
	if state.Observation != nil {
		for _, resource := range state.Observation.Resources {
			if resource.Kind == "Pod" && resource.APIObjectPresent {
				pods++
				found := false
				for _, log := range logs {
					found = found || log.Source.PodUID == resource.UID
				}
				if !found {
					return false
				}
			}
		}
	}
	return len(logs) == pods
}

func ComputeCleanupRemovedValid(targets, removed []RuntimeResource, complete bool) bool {
	if complete && len(targets) != len(removed) || len(removed) > len(targets) {
		return false
	}
	seen := map[string]bool{}
	for _, resource := range removed {
		if resource.APIObjectPresent || seen[resource.UID] {
			return false
		}
		seen[resource.UID] = true
		resource.APIObjectPresent = true
		found := false
		for _, target := range targets {
			found = found || reflect.DeepEqual(resource, target)
		}
		if !found {
			return false
		}
	}
	return true
}

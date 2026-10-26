package biz

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

type ManagedRuntime struct {
	steps         *ManagedSteps
	repository    ExecutionRuntimeRepository
	trainer       TrainingRuntime
	workspaces    PreparedWorkspaceVerifier
	publications  PublicationVerifier
	registrations *FinetuningRegistrationOrchestrator
}

func (runtime *ManagedRuntime) WithModelRegistration(registry TrainedArtifactRegistry, copier TrainedArtifactCopier) (*ManagedRuntime, error) {
	if runtime == nil {
		return nil, ErrRuntimeNotReady
	}
	repository, ok := runtime.repository.(FinetuningRegistrationDispatchRepository)
	if !ok {
		return nil, ErrRuntimeNotReady
	}
	writer, err := NewFinetuningRegistrationOrchestrator(repository, registry, copier)
	if err != nil {
		return nil, err
	}
	runtime.registrations = writer
	return runtime, nil
}

func NewManagedRuntime(steps *ManagedSteps, repository ExecutionRuntimeRepository, trainer TrainingRuntime, workspaces PreparedWorkspaceVerifier, publications PublicationVerifier) (*ManagedRuntime, error) {
	if steps == nil || repository == nil || trainer == nil || workspaces == nil || publications == nil {
		return nil, ErrRuntimeNotReady
	}
	return &ManagedRuntime{steps: steps, repository: repository, trainer: trainer, workspaces: workspaces, publications: publications}, nil
}

type ManagedRuntimeResult struct {
	States    ExecutionStates
	Execution Execution
	Authority RunAuthority
	Runtime   ExecutionRuntime
	Replayed  bool
	TaskID    string
}

// Every callback rechecks current workload identity and the KFP-owned task
// association. A previously issued receipt is never a bearer credential.
func (runtime *ManagedRuntime) authenticate(ctx context.Context, token string, request BeginManagedExecutionRequest, task string) (ManagedRuntimeResult, error) {
	return runtime.authenticateOrCloseUnbound(ctx, token, request, task, false)
}

func (runtime *ManagedRuntime) authenticateOrCloseUnbound(ctx context.Context, token string, request BeginManagedExecutionRequest, task string, closeUnbound bool) (ManagedRuntimeResult, error) {
	if ctx == nil || token == "" || len(token) > 16384 {
		return ManagedRuntimeResult{}, ErrManagedStepUnauthorized
	}
	if err := ctx.Err(); err != nil {
		return ManagedRuntimeResult{}, err
	}
	if runtime == nil || runtime.steps == nil {
		return ManagedRuntimeResult{}, ErrManagedStepUnavailable
	}
	identifiers := []string{request.TenantID, request.ExecutionID, request.Association.RunID, request.Association.NamespaceUID, request.Association.PodUID}
	if request.OperationID != "" {
		identifiers = append(identifiers, request.OperationID)
	}
	for _, id := range identifiers {
		if !validAdmissionID(id) || strings.ToLower(id) != id {
			return ManagedRuntimeResult{}, ErrInvalidAdmission
		}
	}
	if !closeSpecHashPattern.MatchString(request.SpecHash) {
		return ManagedRuntimeResult{}, ErrInvalidAdmission
	}
	dispatch, err := runtime.steps.repository.Get(ctx, request.TenantID, request.ExecutionID)
	if errors.Is(err, ErrExecutionNotFound) {
		return ManagedRuntimeResult{}, ErrManagedStepUnauthorized
	}
	if err != nil {
		return ManagedRuntimeResult{}, ErrManagedStepUnavailable
	}
	plan, a := dispatch.Plan, request.Association
	if plan.TenantID != request.TenantID || (request.OperationID != "" && plan.OperationID != request.OperationID) || plan.ExecutionID != request.ExecutionID || plan.SpecHash != request.SpecHash || plan.Environment.NamespaceName != a.NamespaceName || plan.Environment.NamespaceUID != a.NamespaceUID {
		return ManagedRuntimeResult{}, ErrManagedStepUnauthorized
	}
	if err = runtime.steps.workloads.Verify(ctx, token, plan, a); err != nil {
		return ManagedRuntimeResult{}, ErrManagedStepUnauthorized
	}
	if err = runtime.steps.runs.VerifyManagedRun(ctx, plan, a, task); err != nil {
		return ManagedRuntimeResult{}, ErrManagedStepUnauthorized
	}
	expected := RunAuthorityCandidate{TenantID: plan.TenantID, ExecutionID: plan.ExecutionID, OperationID: plan.OperationID, SpecHash: plan.SpecHash, AttemptID: dispatch.AttemptID, PlanHash: dispatch.PlanHash, RunID: a.RunID, NamespaceName: a.NamespaceName, NamespaceUID: a.NamespaceUID, WorkflowName: a.WorkflowName, WorkflowUID: a.WorkflowUID}
	authority, err := runtime.steps.repository.GetRunAuthority(ctx, plan.TenantID, plan.ExecutionID)
	if err != nil {
		if !closeUnbound || task != "close" || !errors.Is(err, ErrExecutionNotFound) {
			return ManagedRuntimeResult{}, ErrManagedStepUnauthorized
		}
		// A failed workspace creation may skip prepare before Begin. Current
		// workload and KFP proof above are still mandatory; only a proven absence
		// of writers permits fixing this association in an atomic CLOSED record.
		execution, err := runtime.steps.executions.Get(ctx, plan.TenantID, plan.ExecutionID)
		if err != nil {
			return ManagedRuntimeResult{}, err
		}
		evidence, err := runtime.publications.VerifyWritersAbsent(ctx, execution, a)
		if err != nil {
			return ManagedRuntimeResult{}, err
		}
		authority, state, err := runtime.repository.CloseUnboundRuntime(ctx, expected, evidence)
		if err != nil {
			return ManagedRuntimeResult{}, err
		}
		if authority.RunAuthorityCandidate != expected || state.CloseGeneration == 0 || state.ClosedAt == nil || state.CloseReason != "STEP_FAILED" {
			return ManagedRuntimeResult{}, ErrRuntimeConflict
		}
		return runtimeResult(ManagedRuntimeResult{Execution: execution, Authority: authority}, state, false)
	}
	if authority.RunAuthorityCandidate != expected {
		return ManagedRuntimeResult{}, ErrManagedStepUnauthorized
	}
	execution, err := runtime.steps.executions.Get(ctx, plan.TenantID, plan.ExecutionID)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	state, err := runtime.repository.GetRuntime(ctx, plan.TenantID, plan.ExecutionID)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	return runtimeResult(ManagedRuntimeResult{Execution: execution, Authority: authority}, state, false)
}
func runtimeResult(result ManagedRuntimeResult, state ExecutionRuntime, replayed bool) (ManagedRuntimeResult, error) {
	states, err := ProjectRuntimeStates(result.Execution.States, state)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	result.Runtime = state
	result.States = states
	result.Replayed = replayed
	result.Execution.OwnerRevision = state.OwnerRevision
	result.Execution.States = states
	result.Authority.OwnerRevision = state.OwnerRevision
	return result, nil
}
func (runtime *ManagedRuntime) Configuration(ctx context.Context, token string, request BeginManagedExecutionRequest, task string) (ManagedRuntimeResult, error) {
	switch task {
	case "prepare", "train-wait", "collect", "publish", "close", "export", "verify", "register":
	default:
		return ManagedRuntimeResult{}, ErrManagedStepUnauthorized
	}
	result, err := runtime.authenticate(ctx, token, request, task)
	if err != nil {
		return result, err
	}
	if (task == "export" || task == "verify" || task == "register") && result.Execution.Snapshot.Kind != "FINETUNING" {
		return ManagedRuntimeResult{}, ErrManagedStepUnauthorized
	}
	resolver, ok := runtime.steps.runs.(ManagedTaskIdentityResolver)
	if !ok {
		return result, nil
	}
	dispatch, err := runtime.steps.repository.Get(ctx, request.TenantID, request.ExecutionID)
	if err != nil {
		return ManagedRuntimeResult{}, ErrManagedStepUnavailable
	}
	plan, a := dispatch.Plan, request.Association
	expected := RunAuthorityCandidate{TenantID: plan.TenantID, ExecutionID: plan.ExecutionID, OperationID: plan.OperationID, SpecHash: plan.SpecHash, AttemptID: dispatch.AttemptID, PlanHash: dispatch.PlanHash, RunID: a.RunID, NamespaceName: plan.Environment.NamespaceName, NamespaceUID: plan.Environment.NamespaceUID, WorkflowName: a.WorkflowName, WorkflowUID: a.WorkflowUID}
	if expected != result.Authority.RunAuthorityCandidate {
		return ManagedRuntimeResult{}, ErrManagedStepUnauthorized
	}
	taskID, err := resolver.ResolveManagedTaskID(ctx, plan, a, task)
	if err != nil || taskID == "" || len(taskID) > 256 || strings.IndexFunc(taskID, func(r rune) bool { return r <= ' ' || r > '~' }) >= 0 {
		return ManagedRuntimeResult{}, ErrManagedStepUnauthorized
	}
	result.TaskID = taskID
	return result, nil
}
func (runtime *ManagedRuntime) Prepared(ctx context.Context, token string, request BeginManagedExecutionRequest, workspace WorkspaceBinding, input cpup01.InputRef) (ManagedRuntimeResult, error) {
	result, err := runtime.authenticate(ctx, token, request, "prepare")
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(input, result.Execution.Snapshot.Input) {
		return ManagedRuntimeResult{}, ErrRuntimeConflict
	}
	if err = runtime.workspaces.VerifyPrepared(ctx, result.Execution, request.Association, workspace); err != nil {
		return ManagedRuntimeResult{}, err
	}
	state, replayed, err := runtime.repository.RecordPrepared(ctx, result.Authority.RunAuthorityCandidate, workspace)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	return runtimeResult(result, state, replayed)
}
func (runtime *ManagedRuntime) Ensure(ctx context.Context, token string, request BeginManagedExecutionRequest) (ManagedRuntimeResult, error) {
	result, err := runtime.authenticate(ctx, token, request, "train-wait")
	if err != nil {
		return result, err
	}
	if result.Runtime.Workspace == nil {
		return ManagedRuntimeResult{}, ErrRuntimeNotReady
	}
	if err := runtime.workspaces.VerifyPreparedCompletion(ctx, result.Execution, request.Association, *result.Runtime.Workspace); err != nil {
		return ManagedRuntimeResult{}, err
	}
	if err := runtime.materializeRuntime(ctx, result); err != nil {
		return ManagedRuntimeResult{}, err
	}
	reservation, err := runtime.repository.ReserveTraining(ctx, result.Authority.RunAuthorityCandidate)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	state := reservation.State
	if state.TrainingRejection != nil {
		return result, ErrTrainingRejected
	}
	if state.TrainingHandle != nil {
		return runtimeResult(result, state, true)
	}
	if state.Training == nil {
		return ManagedRuntimeResult{}, ErrRuntimeNotReady
	}
	var handle TrainingHandle
	if reservation.SendPermit {
		// The only POST permit is returned by the first committed reservation.
		// An unknown outcome leaves that reservation consumed permanently.
		handle, err = runtime.trainer.CreateTraining(ctx, *state.Training)
	} else {
		handle, err = runtime.trainer.FindTraining(ctx, *state.Training)
	}
	if err != nil {
		var rejected *TrainingCreationRejection
		if reservation.SendPermit && errors.As(err, &rejected) {
			// A canceled step cannot erase a complete refusal from its original
			// one-shot request. One finite budget records it, without another POST.
			write, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			state, recordErr := runtime.repository.RecordTrainingRejection(write, result.Authority.RunAuthorityCandidate, *rejected)
			if recordErr != nil {
				return ManagedRuntimeResult{}, recordErr
			}
			result, resultErr := runtimeResult(result, state, false)
			if resultErr != nil {
				return ManagedRuntimeResult{}, resultErr
			}
			return result, ErrTrainingRejected
		}
		return ManagedRuntimeResult{}, err
	}
	state, err = runtime.repository.RecordTrainingHandle(ctx, result.Authority.RunAuthorityCandidate, handle)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	if state.CloseGeneration > 0 {
		if err = runtime.trainer.StopTraining(ctx, *state.Training, handle); err != nil {
			return ManagedRuntimeResult{}, err
		}
	}
	return runtimeResult(result, state, !reservation.SendPermit)
}

// materializeRuntime belongs to ModelDev's authenticated EnsureTraining path.
// KFP never receives Kubernetes authority. Persisted dispatch and bound UID
// facts survive the owner process; neither lease expiry nor 404 recreates them.
func (runtime *ManagedRuntime) materializeRuntime(ctx context.Context, result ManagedRuntimeResult) error {
	authority := result.Authority.RunAuthorityCandidate
	reserved, err := runtime.repository.ReserveRuntime(ctx, authority)
	if err != nil {
		return err
	}
	state := reserved.State
	if state.Workspace == nil || state.TrainingWindow == nil || state.RuntimeTemplate == nil {
		return ErrRuntimeNotReady
	}
	plan, err := FreezeTrainingPlan(result.Execution, *state.Workspace, state.TrainingWindow.AdmittedAt)
	if err != nil {
		return err
	}
	creationContext, cancel := context.WithDeadline(ctx, state.TrainingWindow.DeadlineAt)
	defer cancel()
	var binding RuntimeTemplateBinding
	if reserved.SendPermit {
		binding, err = runtime.trainer.CreateRuntime(creationContext, plan)
	} else {
		binding, err = runtime.trainer.FindRuntime(creationContext, plan)
	}
	if err != nil {
		write, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer done()
		var notSent *RuntimeCreationNotSent
		if reserved.SendPermit && errors.As(err, &notSent) {
			proof := *notSent
			proof.AttemptDispatchedAt = state.RuntimeTemplate.Attempts[len(state.RuntimeTemplate.Attempts)-1].DispatchedAt
			if _, recordErr := runtime.repository.RecordRuntimeNotSent(write, authority, proof); recordErr != nil {
				return recordErr
			}
		} else if state.RuntimeTemplate.Binding == nil && errors.Is(err, ErrTrainingNotFound) {
			if _, recordErr := runtime.repository.RecordRuntimeReview(write, authority); recordErr != nil {
				return recordErr
			}
			return ErrTrainingUncertain
		} else if state.RuntimeTemplate.Binding != nil && (errors.Is(err, ErrRuntimeConflict) || errors.Is(err, ErrTrainingNotFound)) {
			// Drift prevents new computation, but the normal owner closes already
			// bound computation using its original TrainJob UID and writer graph.
			if _, _, closeErr := runtime.repository.RequestRuntimeClose(write, authority, "STEP_FAILED"); closeErr != nil {
				return closeErr
			}
		}
		return err
	}
	if state.RuntimeTemplate.Binding != nil && *state.RuntimeTemplate.Binding != binding {
		write, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer done()
		_, _, _ = runtime.repository.RequestRuntimeClose(write, authority, "STEP_FAILED")
		return ErrRuntimeConflict
	}
	write, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	_, err = runtime.repository.RecordRuntimeBinding(write, authority, binding)
	return err
}

// observe recovers a lost creation response by deterministic lookup only. It
// does not reissue creation and retains all previously observed writer UIDs.
func (runtime *ManagedRuntime) observe(ctx context.Context, result ManagedRuntimeResult) (ExecutionRuntime, error) {
	state := result.Runtime
	if state.TrainingRejection != nil {
		return state, ErrTrainingRejected
	}
	if state.Training == nil {
		return state, ErrRuntimeNotReady
	}
	if state.TrainingHandle == nil {
		handle, err := runtime.trainer.FindTraining(ctx, *state.Training)
		if err != nil {
			return state, err
		}
		state, err = runtime.repository.RecordTrainingHandle(ctx, result.Authority.RunAuthorityCandidate, handle)
		if err != nil {
			return state, err
		}
	}
	history := []RuntimeResource(nil)
	if state.Observation != nil {
		history = state.Observation.Resources
	}
	observation, err := runtime.trainer.ObserveTraining(ctx, *state.Training, *state.TrainingHandle, history)
	if err != nil {
		return state, err
	}
	state, err = runtime.repository.RecordTrainingObservation(ctx, result.Authority.RunAuthorityCandidate, observation)
	if err != nil {
		return state, err
	}
	if observation.SpecificationMismatch {
		state, _, err = runtime.repository.RequestRuntimeClose(ctx, result.Authority.RunAuthorityCandidate, "STEP_FAILED")
		if err == nil {
			err = ErrRuntimeConflict
		}
	}
	return state, err
}
func (runtime *ManagedRuntime) Status(ctx context.Context, token string, request BeginManagedExecutionRequest) (ManagedRuntimeResult, error) {
	result, err := runtime.authenticate(ctx, token, request, "train-wait")
	if err != nil {
		return result, err
	}
	state, err := runtime.observe(ctx, result)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	return runtimeResult(result, state, false)
}
func (runtime *ManagedRuntime) Publish(ctx context.Context, token string, request BeginManagedExecutionRequest, candidate RuntimePublication) (ManagedRuntimeResult, error) {
	if runtime == nil || runtime.steps == nil || runtime.steps.executions == nil {
		return ManagedRuntimeResult{}, ErrManagedStepUnavailable
	}
	execution, err := runtime.steps.executions.Get(ctx, request.TenantID, request.ExecutionID)
	if err != nil {
		return ManagedRuntimeResult{}, ErrManagedStepUnauthorized
	}
	task := "close"
	if execution.Snapshot.Kind == "FINETUNING" {
		task = "register"
	}
	result, err := runtime.authenticate(ctx, token, request, task)
	if err != nil {
		return result, err
	}
	state := result.Runtime
	if state.Observation == nil || state.Observation.Outcome != "SUCCEEDED" || !state.Observation.WritersAbsent {
		return ManagedRuntimeResult{}, ErrRuntimeNotReady
	}
	publication, err := runtime.publications.VerifyPublication(ctx, result.Execution, request.Association, candidate)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	state, replayed, err := runtime.repository.RecordPublication(ctx, result.Authority.RunAuthorityCandidate, publication)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	return runtimeResult(result, state, replayed)
}
func (runtime *ManagedRuntime) Close(ctx context.Context, token string, request BeginManagedExecutionRequest, reason string) (ManagedRuntimeResult, error) {
	result, err := runtime.authenticateOrCloseUnbound(ctx, token, request, "close", reason == "STEP_FAILED")
	if err != nil {
		return result, err
	}
	if reason == "NATURAL_TERMINAL" && result.Execution.Snapshot.Kind == "FINETUNING" && result.Execution.Snapshot.Finetuning.RequiresModelRegistration() && (result.Runtime.FinetuningRegistration == nil || result.Runtime.FinetuningRegistration.State != "READY") {
		return ManagedRuntimeResult{}, ErrModelDependencyUnavailable
	}
	state, replayed, err := runtime.repository.RequestRuntimeClose(ctx, result.Authority.RunAuthorityCandidate, reason)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	// The shared fence commits before any remote stop or observation. A failure
	// below leaves durable CLOSING and cannot authorize another creation.
	result.Runtime = state
	if reason == "NATURAL_TERMINAL" && state.CloseReason != "NATURAL_TERMINAL" {
		return ManagedRuntimeResult{}, ErrRuntimeConflict
	}
	if state.CloseReason == "NATURAL_TERMINAL" {
		if state.Publication == nil || state.Observation == nil || state.Observation.Outcome != "SUCCEEDED" || !state.Observation.WritersAbsent {
			return ManagedRuntimeResult{}, ErrRuntimeNotReady
		}
		// The close component must exit before its Workflow can be terminal.
		// Deliver only the durable fence; the owner supplies full terminal proof.
		return runtimeResult(result, state, replayed || state.ClosedAt != nil)
	}
	if state.ClosedAt != nil {
		return runtimeResult(result, state, true)
	}
	if state.TrainingRejection != nil {
		if err := runtime.verifyRejectedTrainingAbsent(ctx, state); err != nil {
			return runtimeResult(result, state, replayed)
		}
	} else if state.Training != nil {
		state, err = runtime.observe(ctx, result)
		if err != nil {
			return runtimeResult(result, state, replayed)
		}
		if !state.Observation.WritersAbsent {
			if err = runtime.trainer.StopTraining(ctx, *state.Training, *state.TrainingHandle); err != nil {
				return ManagedRuntimeResult{}, err
			}
			result.Runtime = state
			state, err = runtime.observe(ctx, result)
			if err != nil {
				return runtimeResult(result, state, replayed)
			}
		}
		if !state.Observation.WritersAbsent {
			return runtimeResult(result, state, replayed)
		}
	}
	evidence, err := runtime.publications.VerifyWritersAbsent(ctx, result.Execution, request.Association)
	if err != nil {
		return runtimeResult(result, state, replayed)
	}
	observation := TrainingRuntimeObservation{}
	if state.Observation != nil {
		observation = *state.Observation
	}
	state, err = runtime.repository.ConfirmRuntimeClosed(ctx, result.Authority.RunAuthorityCandidate, state.CloseGeneration, observation, evidence)
	if err != nil {
		return ManagedRuntimeResult{}, err
	}
	return runtimeResult(result, state, replayed)
}

// Rejection proves that the original POST cannot produce a late writer. Still
// reject a conflicting current object or unavailable lookup before closure.
func (runtime *ManagedRuntime) verifyRejectedTrainingAbsent(ctx context.Context, state ExecutionRuntime) error {
	if state.Training == nil || state.TrainingRejection == nil {
		return ErrRuntimeNotReady
	}
	_, err := runtime.trainer.FindTraining(ctx, *state.Training)
	if errors.Is(err, ErrTrainingNotFound) {
		return nil
	}
	if err == nil {
		return ErrRuntimeConflict
	}
	return err
}

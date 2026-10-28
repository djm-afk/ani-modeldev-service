package biz

import (
	"context"
	"errors"
	"strings"
)

var (
	ErrManagedStepUnauthorized = errors.New("MANAGED_STEP_UNAUTHORIZED")
	ErrManagedStepUnavailable  = errors.New("MANAGED_STEP_UNAVAILABLE")
)

// ManagedStepAssociation is a claim, never proof. Both the workload identity
// and the KFP control API must independently confirm it on every callback.
type ManagedStepAssociation struct {
	RunID, NamespaceName, NamespaceUID string
	WorkflowName, WorkflowUID          string
	PodName, PodUID                    string
}

type BeginManagedExecutionRequest struct {
	TenantID, OperationID, ExecutionID, SpecHash string
	Association                                  ManagedStepAssociation
}

type BeginManagedExecutionResult struct {
	Authority RunAuthorityReceipt
	States    ExecutionStates
}

type RunAuthorityRepository interface {
	Get(context.Context, string, string) (PipelineDispatch, error)
	BindRunAuthority(context.Context, RunAuthorityCandidate) (RunAuthorityReceipt, error)
	GetRunAuthority(context.Context, string, string) (RunAuthority, error)
}

type ManagedWorkloadVerifier interface {
	Verify(context.Context, string, PipelineDispatchPlan, ManagedStepAssociation) error
}

type ManagedRunVerifier interface {
	VerifyManagedRun(context.Context, PipelineDispatchPlan, ManagedStepAssociation, string) error
}

// ManagedTaskIdentityResolver obtains the real logical task ID from verified
// Run and workload membership. It is required at service construction.
type ManagedTaskIdentityResolver interface {
	ResolveManagedTaskID(context.Context, PipelineDispatchPlan, ManagedStepAssociation, string) (string, error)
}

// ManagedSteps handles KFP callbacks; it never advances the normal workflow.
// The token is used transiently for TokenReview and is never persisted.
type ManagedSteps struct {
	repository RunAuthorityRepository
	executions ExecutionRepository
	workloads  ManagedWorkloadVerifier
	runs       ManagedRunVerifier
	tasks      ManagedTaskIdentityResolver
}

func NewManagedSteps(repository RunAuthorityRepository, executions ExecutionRepository, workloads ManagedWorkloadVerifier, runs ManagedRunVerifier) (*ManagedSteps, error) {
	if repository == nil || executions == nil || workloads == nil || runs == nil {
		return nil, ErrManagedStepUnavailable
	}
	tasks, ok := runs.(ManagedTaskIdentityResolver)
	if !ok {
		return nil, ErrManagedStepUnavailable
	}
	return &ManagedSteps{repository: repository, executions: executions, workloads: workloads, runs: runs, tasks: tasks}, nil
}

func (steps *ManagedSteps) Begin(ctx context.Context, token string, request BeginManagedExecutionRequest) (BeginManagedExecutionResult, error) {
	if ctx == nil || token == "" || len(token) > 16384 {
		return BeginManagedExecutionResult{}, ErrManagedStepUnauthorized
	}
	if err := ctx.Err(); err != nil {
		return BeginManagedExecutionResult{}, err
	}
	if steps == nil || steps.repository == nil || steps.executions == nil || steps.workloads == nil || steps.runs == nil {
		return BeginManagedExecutionResult{}, ErrManagedStepUnavailable
	}
	identifiers := []string{request.TenantID, request.ExecutionID, request.Association.RunID, request.Association.NamespaceUID, request.Association.PodUID}
	if request.OperationID != "" {
		identifiers = append(identifiers, request.OperationID)
	}
	for _, id := range identifiers {
		if !validAdmissionID(id) || strings.ToLower(id) != id {
			return BeginManagedExecutionResult{}, ErrInvalidAdmission
		}
	}
	if !closeSpecHashPattern.MatchString(request.SpecHash) {
		return BeginManagedExecutionResult{}, ErrInvalidAdmission
	}
	// Tenant is an untrusted lookup selector until current workload identity is
	// checked against this immutable plan. Do not expose a plan on a failed call.
	dispatch, err := steps.repository.Get(ctx, request.TenantID, request.ExecutionID)
	if errors.Is(err, ErrExecutionNotFound) {
		return BeginManagedExecutionResult{}, ErrManagedStepUnauthorized
	}
	if err != nil {
		return BeginManagedExecutionResult{}, ErrManagedStepUnavailable
	}
	plan, association := dispatch.Plan, request.Association
	if plan.TenantID != request.TenantID || (request.OperationID != "" && plan.OperationID != request.OperationID) || plan.ExecutionID != request.ExecutionID || plan.SpecHash != request.SpecHash ||
		plan.Environment.NamespaceName != association.NamespaceName || plan.Environment.NamespaceUID != association.NamespaceUID {
		return BeginManagedExecutionResult{}, ErrManagedStepUnauthorized
	}
	if err := steps.workloads.Verify(ctx, token, plan, association); err != nil {
		return BeginManagedExecutionResult{}, ErrManagedStepUnauthorized
	}
	if err := steps.runs.VerifyManagedRun(ctx, plan, association, "prepare"); err != nil {
		return BeginManagedExecutionResult{}, ErrManagedStepUnauthorized
	}
	// SUBMITTING/UNCERTAIN is deliberately allowed: a lost CreateRun response
	// does not erase the actual Run. Independent API proof precedes the same
	// durable CAS used by confirmed submissions. It grants no training permit.
	authority, err := steps.repository.BindRunAuthority(ctx, RunAuthorityCandidate{
		TenantID: plan.TenantID, ExecutionID: plan.ExecutionID, OperationID: plan.OperationID, SpecHash: plan.SpecHash,
		AttemptID: dispatch.AttemptID, PlanHash: dispatch.PlanHash, RunID: association.RunID,
		NamespaceName: association.NamespaceName, NamespaceUID: association.NamespaceUID,
		WorkflowName: association.WorkflowName, WorkflowUID: association.WorkflowUID,
	})
	if err != nil {
		return BeginManagedExecutionResult{}, err
	}
	execution, err := steps.executions.Get(ctx, plan.TenantID, plan.ExecutionID)
	if err != nil {
		return BeginManagedExecutionResult{}, ErrManagedStepUnavailable
	}
	return BeginManagedExecutionResult{Authority: authority, States: execution.States}, nil
}

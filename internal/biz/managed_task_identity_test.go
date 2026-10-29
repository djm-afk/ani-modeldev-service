package biz_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func TestManagedConfigurationResolvesTaskOnlyAfterAuthentication(t *testing.T) {
	for _, test := range []struct {
		name, taskID                    string
		workloadErr, runErr, resolveErr error
		wantErr                         bool
	}{
		{name: "real logical task", taskID: "25f4d3fc-cb4d-476d-becf-2b60bd887c03"},
		{name: "unauthorized workload", taskID: "must-not-return", workloadErr: errors.New("TokenReview denied"), wantErr: true},
		{name: "unowned task", taskID: "must-not-return", runErr: errors.New("current Pod absent from Run"), wantErr: true},
		{name: "resolver denied", resolveErr: errors.New("current membership changed"), wantErr: true},
		{name: "missing task", wantErr: true},
		{name: "oversized task", taskID: strings.Repeat("a", 257), wantErr: true},
		{name: "invalid task", taskID: "task with space", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := []string{}
			runs := &taskIdentityRuns{calls: &calls, taskID: test.taskID, verifyErr: test.runErr, resolveErr: test.resolveErr}
			runtime, request := taskIdentityRuntime(t, &calls, test.workloadErr, runs)
			result, err := runtime.Configuration(context.Background(), "synthetic-current-bound-token", request, "publish")
			if test.wantErr {
				if !errors.Is(err, biz.ErrManagedStepUnauthorized) || result.TaskID != "" {
					t.Fatalf("unverified task identity escaped authentication: %+v %v", result, err)
				}
			} else if err != nil || result.TaskID != test.taskID {
				t.Fatalf("verified backend task was not returned: task=%q err=%v", result.TaskID, err)
			}
			if test.workloadErr != nil && !reflect.DeepEqual(calls, []string{"workload"}) {
				t.Fatalf("resolver was reached before workload authentication: %v", calls)
			}
			if test.runErr != nil && !reflect.DeepEqual(calls, []string{"workload", "run"}) {
				t.Fatalf("resolver was reached before Run membership authentication: %v", calls)
			}
			if test.workloadErr == nil && test.runErr == nil && !reflect.DeepEqual(calls, []string{"workload", "run", "resolve"}) {
				t.Fatalf("task identity did not follow authenticated membership: %v", calls)
			}
		})
	}
}

// Only outbound ports are substituted; Configuration's authentication and
// identity behavior remain the real use case under test.
func taskIdentityRuntime(t *testing.T, calls *[]string, workloadErr error, runs biz.ManagedRunVerifier) (*biz.ManagedRuntime, biz.BeginManagedExecutionRequest) {
	t.Helper()
	snapshot := conformance.SnapshotV1()
	request := biz.BeginManagedExecutionRequest{TenantID: "11111111-1111-4111-8111-111111111111", ExecutionID: "22222222-2222-4222-8222-222222222222", OperationID: "44444444-4444-4444-8444-444444444444", SpecHash: conformance.SnapshotSHA256V1, Association: biz.ManagedStepAssociation{RunID: "55555555-5555-4555-8555-555555555555", NamespaceName: snapshot.Environment.NamespaceName, NamespaceUID: snapshot.Environment.NamespaceUID, WorkflowName: "captured-flow", WorkflowUID: "66666666-6666-4666-8666-666666666666", PodName: "captured-publish", PodUID: "77777777-7777-4777-8777-777777777777"}}
	plan := biz.PipelineDispatchPlan{TenantID: request.TenantID, ExecutionID: request.ExecutionID, OperationID: request.OperationID, SpecHash: request.SpecHash, Environment: snapshot.Environment}
	dispatch := biz.PipelineDispatch{Plan: plan, AttemptID: "synthetic-attempt", PlanHash: strings.Repeat("a", 64)}
	a := request.Association
	authority := biz.RunAuthority{RunAuthorityCandidate: biz.RunAuthorityCandidate{TenantID: plan.TenantID, ExecutionID: plan.ExecutionID, OperationID: plan.OperationID, SpecHash: plan.SpecHash, AttemptID: dispatch.AttemptID, PlanHash: dispatch.PlanHash, RunID: a.RunID, NamespaceName: a.NamespaceName, NamespaceUID: a.NamespaceUID, WorkflowName: a.WorkflowName, WorkflowUID: a.WorkflowUID}}
	steps, err := biz.NewManagedSteps(&taskIdentityAuthority{dispatch: dispatch, authority: authority}, &taskIdentityExecutions{}, &taskIdentityWorkload{calls: calls, err: workloadErr}, runs)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := biz.NewManagedRuntime(steps, &taskIdentityRuntimeStore{}, &taskIdentityUnused{}, &taskIdentityUnused{}, &taskIdentityUnused{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved, ok := runs.(*taskIdentityRuns); ok {
		resolved.plan, resolved.association = plan, a
	}
	return runtime, request
}

type taskIdentityAuthority struct {
	biz.RunAuthorityRepository
	dispatch  biz.PipelineDispatch
	authority biz.RunAuthority
}

func (p *taskIdentityAuthority) Get(context.Context, string, string) (biz.PipelineDispatch, error) {
	return p.dispatch, nil
}
func (p *taskIdentityAuthority) GetRunAuthority(context.Context, string, string) (biz.RunAuthority, error) {
	return p.authority, nil
}

type taskIdentityExecutions struct{ biz.ExecutionRepository }

func (*taskIdentityExecutions) Get(context.Context, string, string) (biz.Execution, error) {
	return biz.Execution{}, nil
}

type taskIdentityRuntimeStore struct{ biz.ExecutionRuntimeRepository }

func (*taskIdentityRuntimeStore) GetRuntime(context.Context, string, string) (biz.ExecutionRuntime, error) {
	return biz.ExecutionRuntime{}, nil
}

type taskIdentityUnused struct {
	biz.TrainingRuntime
	biz.PreparedWorkspaceVerifier
	biz.PublicationVerifier
}
type taskIdentityWorkload struct {
	calls *[]string
	err   error
}

func (p *taskIdentityWorkload) Verify(context.Context, string, biz.PipelineDispatchPlan, biz.ManagedStepAssociation) error {
	*p.calls = append(*p.calls, "workload")
	return p.err
}

type taskIdentityLegacyRuns struct{ calls *[]string }

func (p *taskIdentityLegacyRuns) VerifyManagedRun(context.Context, biz.PipelineDispatchPlan, biz.ManagedStepAssociation, string) error {
	*p.calls = append(*p.calls, "run")
	return nil
}

type taskIdentityRuns struct {
	calls                 *[]string
	taskID                string
	verifyErr, resolveErr error
	plan                  biz.PipelineDispatchPlan
	association           biz.ManagedStepAssociation
}

func (p *taskIdentityRuns) VerifyManagedRun(context.Context, biz.PipelineDispatchPlan, biz.ManagedStepAssociation, string) error {
	*p.calls = append(*p.calls, "run")
	return p.verifyErr
}
func (p *taskIdentityRuns) ResolveManagedTaskID(_ context.Context, plan biz.PipelineDispatchPlan, association biz.ManagedStepAssociation, task string) (string, error) {
	*p.calls = append(*p.calls, "resolve")
	if !reflect.DeepEqual(plan, p.plan) || association != p.association || task != "publish" {
		return "", errors.New("changed frozen plan, association or task")
	}
	return p.taskID, p.resolveErr
}

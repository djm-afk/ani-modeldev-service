package biz_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func TestManagedEnsureRequiresPreparedCompletionBeforeRuntimeReservation(t *testing.T) {
	for _, test := range []struct {
		name                   string
		proofErr               error
		wantProof, wantReserve int
	}{
		{name: "missing durable preparation"},
		{name: "prepare still running", proofErr: biz.ErrRuntimeNotReady, wantProof: 1},
		{name: "prepare binding changed", proofErr: biz.ErrRuntimeConflict, wantProof: 1},
		{name: "original prepare completed", wantProof: 1, wantReserve: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := conformance.SnapshotV1()
			calls := []string{}
			a := biz.ManagedStepAssociation{RunID: "55555555-5555-4555-8555-555555555555", NamespaceName: snapshot.Environment.NamespaceName, NamespaceUID: snapshot.Environment.NamespaceUID, WorkflowName: "original-flow", WorkflowUID: "66666666-6666-4666-8666-666666666666", PodName: "original-train-wait", PodUID: "77777777-7777-4777-8777-777777777777"}
			plan := biz.PipelineDispatchPlan{TenantID: "11111111-1111-4111-8111-111111111111", ExecutionID: "22222222-2222-4222-8222-222222222222", OperationID: "44444444-4444-4444-8444-444444444444", SpecHash: conformance.SnapshotSHA256V1, Environment: snapshot.Environment}
			dispatch := biz.PipelineDispatch{Plan: plan, AttemptID: "attempt", PlanHash: strings.Repeat("a", 64)}
			authority := biz.RunAuthority{RunAuthorityCandidate: biz.RunAuthorityCandidate{TenantID: plan.TenantID, ExecutionID: plan.ExecutionID, OperationID: plan.OperationID, SpecHash: plan.SpecHash, AttemptID: dispatch.AttemptID, PlanHash: dispatch.PlanHash, RunID: a.RunID, NamespaceName: a.NamespaceName, NamespaceUID: a.NamespaceUID, WorkflowName: a.WorkflowName, WorkflowUID: a.WorkflowUID}}
			execution := biz.Execution{Admission: biz.Admission{Snapshot: snapshot}}
			store := &preparedGateStore{}
			if test.name != "missing durable preparation" {
				w := snapshot.Workspace
				store.state.Workspace = &biz.WorkspaceBinding{Mode: w.Mode, NamespaceName: a.NamespaceName, NamespaceUID: a.NamespaceUID, PVCName: "original-pvc", PVCUID: "88888888-8888-4888-8888-888888888888", InputSubpath: w.InputSubpath, TrainingSubpath: w.TrainingSubpath, ReportsSubpath: w.ReportsSubpath, PublicationSubpath: w.PublicationSubpath, PreparedManifestBytes: 1, PreparedManifestSHA256: strings.Repeat("b", 64)}
			}
			steps, err := biz.NewManagedSteps(&taskIdentityAuthority{dispatch: dispatch, authority: authority}, &preparedGateExecutions{execution: execution}, &taskIdentityWorkload{calls: &calls}, &taskIdentityLegacyRuns{calls: &calls})
			if err != nil {
				t.Fatal(err)
			}
			proof := &preparedGateProof{err: test.proofErr}
			runtime, err := biz.NewManagedRuntime(steps, store, &taskIdentityUnused{}, proof, &taskIdentityUnused{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runtime.Ensure(context.Background(), "current-authenticated-token", biz.BeginManagedExecutionRequest{TenantID: plan.TenantID, ExecutionID: plan.ExecutionID, OperationID: plan.OperationID, SpecHash: plan.SpecHash, Association: a})
			if err == nil || proof.calls != test.wantProof || store.reservations != test.wantReserve {
				t.Fatalf("unproven prepare reached training reservation: proof=%d reserve=%d err=%v", proof.calls, store.reservations, err)
			}
			if test.proofErr != nil && !errors.Is(err, test.proofErr) {
				t.Fatal(err)
			}
		})
	}
}

type preparedGateExecutions struct {
	biz.ExecutionRepository
	execution biz.Execution
}

func (p *preparedGateExecutions) Get(context.Context, string, string) (biz.Execution, error) {
	return p.execution, nil
}

type preparedGateStore struct {
	biz.ExecutionRuntimeRepository
	state        biz.ExecutionRuntime
	reservations int
}

func (p *preparedGateStore) GetRuntime(context.Context, string, string) (biz.ExecutionRuntime, error) {
	return p.state, nil
}
func (p *preparedGateStore) ReserveRuntime(context.Context, biz.RunAuthorityCandidate) (biz.TrainingReservation, error) {
	p.reservations++
	return biz.TrainingReservation{}, biz.ErrRuntimeNotReady
}

type preparedGateProof struct {
	biz.PreparedWorkspaceVerifier
	calls int
	err   error
}

func (p *preparedGateProof) VerifyPreparedCompletion(context.Context, biz.Execution, biz.ManagedStepAssociation, biz.WorkspaceBinding) error {
	p.calls++
	return p.err
}

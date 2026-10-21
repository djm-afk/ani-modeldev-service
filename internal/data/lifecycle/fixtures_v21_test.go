package lifecycle_test

import (
	"context"
	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"testing"
)

// These are repository-boundary facts. Full KFP/Pod verification remains in
// runtimeproof and the real authenticated service seam tests.
func startManagedRoleV21(t *testing.T, r *lifecycle.Repository, a biz.RunAuthorityCandidate, role string) {
	t.Helper()
	b := biz.FinetuningStepBinding{Role: role, SpecHash: a.SpecHash, RunID: a.RunID, WorkflowUID: a.WorkflowUID, TaskID: role + "-fixture", PodName: role + "-fixture", PodUID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(role)).String()}
	if _, _, err := r.ReserveManagedStep(context.Background(), a, b); err != nil {
		t.Fatal("original role window", role, err)
	}
}

func reserveTrainingV21(t *testing.T, r *lifecycle.Repository, ctx context.Context, a biz.RunAuthorityCandidate) (biz.TrainingReservation, error) {
	t.Helper()
	reserved, err := r.ReserveRuntime(ctx, a)
	if err != nil {
		return biz.TrainingReservation{}, err
	}
	if reserved.SendPermit {
		fact := reserved.State.RuntimeTemplate
		if _, err := r.RecordRuntimeBinding(ctx, a, biz.RuntimeTemplateBinding{NamespaceUID: fact.NamespaceUID, Name: fact.Name, UID: "runtime-fixture-uid", ObservedSpecSHA256: fact.ExpectedSpecSHA256}); err != nil {
			return biz.TrainingReservation{}, err
		}
	}
	return r.ReserveTraining(ctx, a)
}

func recordPublicationV21(t *testing.T, r *lifecycle.Repository, ctx context.Context, a biz.RunAuthorityCandidate, p biz.RuntimePublication) (biz.ExecutionRuntime, bool, error) {
	t.Helper()
	state, err := r.GetRuntime(ctx, a.TenantID, a.ExecutionID)
	if err != nil {
		return biz.ExecutionRuntime{}, false, err
	}
	if state.Publication == nil && state.CloseGeneration == 0 {
		startManagedRoleV21(t, r, a, "collect")
		startManagedRoleV21(t, r, a, "publish")
		if len(state.FinetuningSteps) > 0 {
			startManagedRoleV21(t, r, a, "register")
		}
	}
	return r.RecordPublication(ctx, a, p)
}

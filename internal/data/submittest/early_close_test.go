//go:build cpu_mainflow

package submittest_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/component"
)

func TestMainFlowEarlyCloseBeforeBeginPersistsClosedWithoutTrainingPermit(t *testing.T) {
	f, client, facts, _, _ := bootstrapFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// CreatePVC failed at the external boundary: KFP created no data-plane Pod
	// and explicitly skipped every data step. No Begin or workspace report ran.
	f.mu.Lock()
	f.skipped = map[string]bool{"prepare": true, "train-wait": true, "collect": true, "publish": true}
	for step := range f.skipped {
		delete(f.objects, "/api/v1/namespaces/"+f.workspace.NamespaceName+"/pods/main-"+step)
	}
	delete(f.objects, "/api/v1/namespaces/"+f.workspace.NamespaceName+"/persistentvolumeclaims/"+f.workspace.PVCName)
	f.mu.Unlock()
	before, err := facts.GetRuntime(ctx, f.request.Admission.TenantID, f.request.Admission.ExecutionID)
	if err != nil || before.CloseGeneration != 0 || before.Workspace != nil || before.Training != nil {
		t.Fatalf("early close preflight must contain only admission/dispatch: %+v %v", before, err)
	}
	claim := f.stepContext("close")
	claim.Identity.OperationId = ""
	if result, err := client.RequestExecutionClose(bootstrapCall(ctx, f, "invalid-workload-token"), &modeldevv1.RequestExecutionCloseRequest{Context: claim, Reason: modeldevv1.CloseReason_CLOSE_REASON_STEP_FAILED}); err == nil || result != nil {
		t.Fatal("unbound close bypassed current workload authentication")
	}
	unchanged, err := facts.GetRuntime(ctx, f.request.Admission.TenantID, f.request.Admission.ExecutionID)
	if err != nil || unchanged.CloseGeneration != 0 || unchanged.ClosedAt != nil {
		t.Fatal("unauthorized early close changed durable execution facts", err)
	}
	tokenFile := filepath.Join(t.TempDir(), "projected-token")
	if err := os.WriteFile(tokenFile, []byte("synthetic-bound-close"), 0600); err != nil {
		t.Fatal(err)
	}
	runner, err := component.New(component.Config{TenantID: f.request.Admission.TenantID, Context: claim, TokenFile: tokenFile}, client, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, "close"); err == nil {
		t.Fatal("early pipeline failure must not become a successful component result")
	}
	closed, err := facts.GetRuntime(ctx, f.request.Admission.TenantID, f.request.Admission.ExecutionID)
	if err != nil || closed.CloseGeneration == 0 || closed.ClosedAt == nil || closed.CloseReason != "STEP_FAILED" || closed.Workspace != nil || closed.Training != nil || closed.Publication != nil || closed.CloseEvidence == nil || len(closed.CloseEvidence.SkippedTasks) != 4 || len(closed.CloseEvidence.Resources) != 0 {
		t.Fatalf("EARLY_CLOSE_NOT_IMPLEMENTED: verified skipped workflow must atomically close its unbound execution: %+v %v", closed, err)
	}
	waitClaim := f.stepContext("train-wait")
	waitClaim.Identity.OperationId = ""
	if result, err := client.EnsureTraining(bootstrapCall(ctx, f, "synthetic-bound-train-wait"), &modeldevv1.EnsureTrainingRequest{Context: waitClaim}); err == nil || result != nil {
		t.Fatal("early close granted a training permit to the skipped step")
	}
	f.mu.Lock()
	creates := f.creates
	f.mu.Unlock()
	if creates != 0 {
		t.Fatal("early close created a TrainJob")
	}
}

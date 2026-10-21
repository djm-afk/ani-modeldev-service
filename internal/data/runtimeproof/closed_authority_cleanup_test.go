package runtimeproof_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/runtimeproof"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/stepidentity"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// This is the real early-close path: a dispatch is confirmed, but no normal
// step ever binds. Closing records its independent owner, exits and Pod removal
// through restricted PostgreSQL adapters before every pool is discarded.
func newClosedRecoveryCleanupFixture(t *testing.T) (*computeCleanupFixture, biz.ExecutionRuntime) {
	t.Helper()
	f := newOwnerCloseFixture(t)
	f.execution.AcceptedAt = time.Now().UTC().Truncate(time.Microsecond)
	f.execution.Snapshot.DeadlineAt = f.execution.AcceptedAt.Add(time.Hour)
	f.execution.Admission = admissionfixture.Bind(t, f.execution.Admission)
	runtimeObject := computeRuntime(t, f)
	var err error
	f.execution.SpecHash, err = f.execution.Snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	open := postgres.Prepare(t)
	pool := open()
	ctx := context.Background()
	commands, dispatches := execution.New(pool), submission.New(pool)
	if _, err := commands.Accept(ctx, f.execution.Admission); err != nil {
		t.Fatal(err)
	}
	reserved, err := dispatches.Reserve(ctx, biz.PipelineDispatchRequest{Admission: f.execution.Admission, Owner: f.dispatch.Plan.Owner})
	if err != nil || reserved.SendPermit == nil {
		t.Fatal("original dispatch permit", err)
	}
	at, err := dispatches.SubmissionObservationTime(ctx, *reserved.SendPermit)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := dispatches.RecordSubmissionConfirmed(ctx, *reserved.SendPermit, biz.PipelineSubmissionObservation{State: biz.PipelineSubmissionConfirmed, RunID: f.runID}, at)
	if err != nil {
		t.Fatal(err)
	}
	f.dispatch = confirmed.Dispatch
	f.run["experiment_id"], f.run["display_name"], f.run["service_account"] = f.dispatch.Plan.Environment.ExperimentID, f.dispatch.Plan.DisplayName, f.dispatch.Plan.Environment.Identities.KFPStepServiceAccount
	f.run["pipeline_version_reference"] = map[string]any{"pipeline_id": f.dispatch.Plan.PipelineID, "pipeline_version_id": f.dispatch.Plan.PipelineVersionID}
	f.run["runtime_config"] = map[string]any{"parameters": map[string]any{"execution_id": f.dispatch.Plan.ExecutionID, "spec_hash": f.dispatch.Plan.SpecHash}, "pipeline_root": f.dispatch.Plan.Owner.PipelineRoot}
	f.terminateExternalRecords(t)
	f.workflow.SetResourceVersion("1")
	step := &stepRetentionFixture{f: f, open: open, pods: map[string]*unstructured.Unstructured{}, workspace: biz.WorkspaceBinding{NamespaceName: f.authority().NamespaceName}}
	for i := range f.pods {
		f.pods[i].SetResourceVersion("1")
		f.pods[i].SetFinalizers([]string{"modeldev.ani.io/step-exit-evidence"})
		step.pods[f.pods[i].GetName()] = f.pods[i].DeepCopy()
	}
	fixture := &computeCleanupFixture{step: step, runtime: runtimeObject, objects: map[string]*unstructured.Unstructured{}, pendingReads: map[string]int{}}
	step.serveExtra = fixture.serve
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { step.serve(t, w, r) }))
	t.Cleanup(peer.Close)
	step.clientConfig = &rest.Config{Host: peer.URL, TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw})}, Timeout: 3 * time.Second, QPS: 100, Burst: 200}
	fixture.kube, err = dynamic.NewForConfig(step.clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	step.proof = runtimeproof.New(fixture.kube, f.runs, nil, dispatches)
	if _, err := commands.ApplyCloseIntent(ctx, biz.CloseIntent{TenantID: f.execution.TenantID, ExecutionID: f.execution.ExecutionID, OperationID: f.execution.OperationID, SpecHash: f.execution.SpecHash, SourceGeneration: 1, Reason: biz.CloseReasonUserStop, RequestedAt: time.Now().UTC().Truncate(time.Microsecond), RequestedActor: f.execution.Actor}); err != nil {
		t.Fatal(err)
	}
	facts := lifecycle.New(pool)
	if _, err := facts.FenceOwnerClose(ctx, f.execution.TenantID, f.execution.ExecutionID); err != nil {
		t.Fatal(err)
	}
	owner, err := step.proof.VerifyClosingRun(ctx, f.execution, f.dispatch, f.runID)
	if err != nil || owner != f.authority() {
		t.Fatal("independent original owner before exits are disposed", err)
	}
	closing, err := facts.RecordClosingRun(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := step.proof.VerifyOwnerWritersAbsent(ctx, f.execution, owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := facts.ConfirmRuntimeClosed(ctx, owner, closing.CloseGeneration, biz.TrainingRuntimeObservation{}, evidence); err != nil {
		t.Fatal(err)
	}
	started, err := facts.BeginStepExitCleanup(ctx, owner, closing.CloseGeneration, evidence)
	if err != nil {
		t.Fatal(err)
	}
	step.receipt = *started.StepExitCleanup
	removed, err := step.proof.ReconcileStepExitCleanup(ctx, f.execution, owner, nil, step.receipt)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := facts.CompleteStepExitCleanup(ctx, owner, closing.CloseGeneration, removed)
	if err != nil || len(step.pods) != 0 || closed.CloseAuthority == nil || closed.StepExitCleanup.CompletedAt == nil {
		t.Fatal("real CLOSED and original step removal did not persist", err)
	}
	if _, err := dispatches.GetRunAuthority(ctx, f.execution.TenantID, f.execution.ExecutionID); !errors.Is(err, biz.ErrExecutionNotFound) {
		t.Fatal("close recovery invented normal step authority", err)
	}
	pool.Close()
	return fixture, closed
}

type countedClosingRecovery struct {
	*runtimeproof.Verifier
	calls int
}

func (proof *countedClosingRecovery) VerifyClosingRun(ctx context.Context, execution biz.Execution, dispatch biz.PipelineDispatch, id string) (biz.RunAuthorityCandidate, error) {
	proof.calls++
	return proof.Verifier.VerifyClosingRun(ctx, execution, dispatch, id)
}

func closedRecoveryCloser(t *testing.T, f *computeCleanupFixture) (*biz.ExecutionCloser, *countedClosingRecovery) {
	t.Helper()
	pool := f.step.open()
	dispatch := submission.New(pool)
	f.step.proof = runtimeproof.New(f.kube, f.step.f.runs, nil, dispatch)
	identity, err := stepidentity.New(f.kube, "managed-test-audience")
	if err != nil {
		t.Fatal(err)
	}
	steps, err := biz.NewManagedSteps(dispatch, execution.New(pool), identity, f.step.proof)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := biz.NewManagedRuntime(steps, lifecycle.New(pool), trainer.New(f.kube), f.step.proof, f.step.proof)
	if err != nil {
		t.Fatal(err)
	}
	counted := &countedClosingRecovery{Verifier: f.step.proof}
	closer, err := biz.NewExecutionCloser(managed, f.step.f.runs, counted, f.step.proof)
	if err != nil {
		t.Fatal(err)
	}
	closer, err = closer.WithComputeCleanup(f.step.proof)
	if err != nil {
		t.Fatal(err)
	}
	return closer, counted
}

func TestClosedRecoveryAuthorityCleanupReplaysWithoutNormalBindingOrPods(t *testing.T) {
	f, before := newClosedRecoveryCleanupFixture(t)
	for attempt := 0; attempt < 2; attempt++ {
		closer, proof := closedRecoveryCloser(t, f)
		result, err := closer.Reconcile(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
		if err != nil || result.Runtime.ComputeCleanup == nil || result.Runtime.ComputeCleanup.Phase != "APPLIED" || !f.workflowDeleted || proof.calls != 0 {
			t.Fatalf("CLOSED_RECOVERY_REPLAY: original persisted exits must reach compute cleanup without rediscovering deleted Pods: result=%+v calls=%d error=%v", result.Runtime.ComputeCleanup, proof.calls, err)
		}
		if result.Runtime.ClosedAt == nil || !result.Runtime.ClosedAt.Equal(*before.ClosedAt) || !reflect.DeepEqual(result.Runtime.CloseAuthority, before.CloseAuthority) || !reflect.DeepEqual(result.Runtime.CloseEvidence, before.CloseEvidence) || !reflect.DeepEqual(result.Runtime.StepExitCleanup, before.StepExitCleanup) || !result.Authority.BoundAt.IsZero() {
			t.Fatal("cleanup changed original close facts or fabricated normal BoundAt")
		}
		pool := f.step.open()
		if _, err := submission.New(pool).GetRunAuthority(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID); !errors.Is(err, biz.ErrExecutionNotFound) {
			t.Fatal("replay inserted a normal authority row", err)
		}
		if _, err := lifecycle.New(pool).ReserveTraining(context.Background(), *before.CloseAuthority); err == nil {
			t.Fatal("cleanup replay permitted new training")
		}
		pool.Close()
	}
	if !reflect.DeepEqual(f.deleted, []string{"Workflow"}) {
		t.Fatal("restart reissued compute deletion", f.deleted)
	}
}

func TestClosedRecoveryAuthorityCleanupRejectsMissingProofTamperingAndReplacement(t *testing.T) {
	for _, change := range []string{"missing-close-proof", "incomplete-step-cleanup", "tampered-plan-hash", "replacement-workflow"} {
		t.Run(change, func(t *testing.T) {
			f, before := newClosedRecoveryCleanupFixture(t)
			pool := f.step.open()
			var expression string
			switch change {
			case "missing-close-proof":
				expression = "facts - 'CloseEvidence'"
			case "incomplete-step-cleanup":
				expression = "facts #- '{StepExitCleanup,CompletedAt}'"
			case "tampered-plan-hash":
				expression = "jsonb_set(facts, '{CloseAuthority,PlanHash}', to_jsonb('" + strings.Repeat("b", 64) + "'::text))"
			case "replacement-workflow":
				f.step.f.workflow.SetUID(types.UID("same-name-replacement"))
			}
			if expression != "" {
				if _, err := pool.Exec(context.Background(), "UPDATE modeldev_execution_runtimes SET facts = "+expression+" WHERE tenant_id=$1::uuid AND execution_id=$2::uuid", f.step.f.execution.TenantID, f.step.f.execution.ExecutionID); err != nil {
					t.Fatal(err)
				}
			}
			pool.Close()
			closer, _ := closedRecoveryCloser(t, f)
			result, err := closer.Reconcile(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
			if err == nil || f.workflowDeleted || len(f.deleted) != 0 || result.Runtime.ComputeCleanup != nil && result.Runtime.ComputeCleanup.Phase == "APPLIED" {
				raw, _ := json.Marshal(result.Runtime.ComputeCleanup)
				t.Fatalf("unproved/tampered/replaced owner authorized deletion: %s %v", raw, err)
			}
			pool = f.step.open()
			if _, err := submission.New(pool).GetRunAuthority(context.Background(), before.CloseAuthority.TenantID, before.CloseAuthority.ExecutionID); !errors.Is(err, biz.ErrExecutionNotFound) {
				t.Fatal("rejected cleanup inserted a normal binding", err)
			}
			pool.Close()
		})
	}
}

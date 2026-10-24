package lifecycle_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
)

// Admission, step permits/results, publication and registration arbitration all
// use the production repositories and the restricted real PostgreSQL fixture.
// Only the external workload/KFP authentication and Model ports are substituted.
func TestFinetuningArtifactsOnlyManagedRegisterSkipsModelWrites(t *testing.T) {
	open, admission, authority, binding := finetuningRuntimeFixture(t, time.Hour, func(a *biz.Admission) {
		a.Intent.TargetModelID, a.Intent.NewModelName = nil, nil
		a.Snapshot.Finetuning.TargetModelID, a.Snapshot.Finetuning.NewModelName = nil, nil
	})
	ctx := context.Background()
	pool := open()
	repository := lifecycle.New(pool)
	for _, role := range []string{"export", "verify"} {
		binding.Role, binding.TaskID = role, role+"-task"
		binding.PodName, binding.PodUID = role+"-pod", role+"-pod-uid"
		start, err := repository.ReserveFinetuningStep(ctx, authority, binding)
		if err != nil || !start.SendPermit {
			t.Fatal("original step reservation", err)
		}
		var now time.Time
		if err := pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
			t.Fatal(err)
		}
		manifest := "reports/export/merged_manifest.json"
		if role == "verify" {
			manifest = "reports/verify/verify.json"
		}
		result := biz.FinetuningStepResult{StartedAt: start.Start.AdmittedAt, FinishedAt: now.UTC(), ExitCode: 0, ManifestRelativePath: manifest, ManifestBytes: 128, ManifestSHA256: strings.Repeat("a", 64)}
		if _, _, err := repository.RecordFinetuningStepResult(ctx, authority, binding, result); err != nil {
			t.Fatal("original step result", err)
		}
	}
	publication := publicationFixture(t, admission, authority)
	before, _, err := recordPublicationV21(t, repository, ctx, authority, publication)
	if err != nil {
		t.Fatal("original publication", err)
	}
	claim := biz.BeginManagedExecutionRequest{
		TenantID: authority.TenantID, OperationID: authority.OperationID, ExecutionID: authority.ExecutionID, SpecHash: authority.SpecHash,
		Association: biz.ManagedStepAssociation{RunID: authority.RunID, NamespaceName: authority.NamespaceName, NamespaceUID: authority.NamespaceUID, WorkflowName: authority.WorkflowName, WorkflowUID: authority.WorkflowUID, PodName: "register-fixture", PodUID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("register")).String()},
	}
	external := &artifactsOnlyExternalBoundary{t: t, authority: authority, association: claim.Association, role: "register"}
	steps, err := biz.NewManagedSteps(submission.New(pool), execution.New(pool), external, external)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := biz.NewManagedRuntime(steps, repository, external, external, external)
	if err != nil {
		t.Fatal(err)
	}
	model := &artifactsOnlyModelBoundary{}
	if _, err := runtime.WithModelRegistration(model, model); err != nil {
		t.Fatal(err)
	}
	result, err := runtime.RegisterModel(ctx, "synthetic-current-bound-token", claim)
	if err != nil {
		t.Fatalf("ARTIFACTS_ONLY_REGISTER_NOT_IMPLEMENTED: published None-target execution must confirm without Model registration: %v", err)
	}
	stored, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || result.Runtime.FinetuningRegistration != nil || stored.FinetuningRegistration != nil || !reflect.DeepEqual(stored.Publication, before.Publication) || !reflect.DeepEqual(stored.StepTimings, before.StepTimings) {
		t.Fatalf("artifacts-only confirmation changed publication, timing or created Model intent: result=%+v stored=%+v err=%v", result.Runtime.FinetuningRegistration, stored.FinetuningRegistration, err)
	}
	if external.workloadChecks != 1 || external.runChecks != 1 || len(model.calls) != 0 {
		t.Fatalf("confirmation bypassed current authentication or called Model: workload=%d run=%d model=%v", external.workloadChecks, external.runChecks, model.calls)
	}
	// The same published choice is valid when no Model client is configured.
	// Confirmation still uses the authenticated original register task/window.
	withoutModel, err := biz.NewManagedRuntime(steps, repository, external, external, external)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := withoutModel.RegisterModel(ctx, "synthetic-current-bound-token", claim); err != nil || replay.Runtime.FinetuningRegistration != nil || !reflect.DeepEqual(replay.Runtime.Publication, before.Publication) {
		t.Fatalf("artifacts-only replay incorrectly required a Model provider: %v", err)
	}
	if external.workloadChecks != 2 || external.runChecks != 2 {
		t.Fatal("artifacts-only replay skipped current workload/Run authentication")
	}

	closeClaim := claim
	closeClaim.Association.PodName = "close-fixture"
	closeClaim.Association.PodUID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("close")).String()
	external.association, external.role = closeClaim.Association, "close"
	closeResult, err := runtime.Close(ctx, "synthetic-current-bound-token", closeClaim, "NATURAL_TERMINAL")
	closing := closeResult.Runtime
	if err != nil || closeResult.Replayed || closing.CloseGeneration == 0 || closing.ClosedAt != nil || closing.Observation == nil {
		t.Fatalf("published artifacts-only execution could not request natural close: %v", err)
	}
	if external.workloadChecks != 3 || external.runChecks != 3 {
		t.Fatal("artifacts-only natural close skipped current workload/Run authentication")
	}
	zero := int32(0)
	evidence := biz.ManagedCloseEvidence{RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, ObservedAt: time.Now().UTC()}
	for _, role := range []string{"prepare", "train-wait", "export", "verify", "collect", "publish", "register"} {
		name, uid := role+"-fixture", uuid.NewSHA1(uuid.NameSpaceOID, []byte(role)).String()
		for _, step := range closing.FinetuningSteps {
			if step.Binding.Role == role {
				name, uid = step.Binding.PodName, step.Binding.PodUID
			}
		}
		evidence.Resources = append(evidence.Resources, biz.RuntimeResource{APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: name, UID: uid, OwnerUID: authority.WorkflowUID, APIObjectPresent: true, Terminal: true, ExitCode: &zero})
	}
	closed, err := repository.ConfirmRuntimeClosed(ctx, authority, closing.CloseGeneration, *closing.Observation, evidence)
	if err != nil || closed.ClosedAt == nil || closed.CloseReason != "NATURAL_TERMINAL" || closed.FinetuningRegistration != nil || !reflect.DeepEqual(closed.Publication, before.Publication) {
		t.Fatalf("artifacts-only natural close did not preserve original publication: %v", err)
	}
	visible, err := execution.New(open()).Get(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || visible.States.Compute != biz.ComputeState("SUCCEEDED") || visible.States.Delivery != biz.DeliveryState("PUBLISHED") || visible.States.Close != biz.CloseState("CLOSED") {
		t.Fatalf("artifacts-only published/closed outcome was not visible through execution repository: %+v %v", visible.States, err)
	}
	for _, candidate := range []*biz.ManagedRuntime{runtime, withoutModel} {
		if _, err := candidate.ResumeModelRegistration(ctx, authority.TenantID, authority.ExecutionID); err == nil || err.Error() != "MODEL_REGISTRATION_NOT_APPLICABLE" {
			t.Fatalf("artifacts-only resume must explicitly reject inapplicable registration: %v", err)
		}
	}
	afterResume, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || !reflect.DeepEqual(afterResume, closed) || len(model.calls) != 0 {
		t.Fatalf("inapplicable resume changed original closed runtime or called Model: calls=%v err=%v", model.calls, err)
	}
}

type artifactsOnlyExternalBoundary struct {
	biz.TrainingRuntime
	biz.PreparedWorkspaceVerifier
	biz.PublicationVerifier
	t                         *testing.T
	authority                 biz.RunAuthorityCandidate
	association               biz.ManagedStepAssociation
	role                      string
	workloadChecks, runChecks int
}

func (b *artifactsOnlyExternalBoundary) Verify(_ context.Context, token string, plan biz.PipelineDispatchPlan, association biz.ManagedStepAssociation) error {
	b.t.Helper()
	if token != "synthetic-current-bound-token" || plan.TenantID != b.authority.TenantID || plan.ExecutionID != b.authority.ExecutionID || plan.SpecHash != b.authority.SpecHash || association != b.association {
		return biz.ErrManagedStepUnauthorized
	}
	b.workloadChecks++
	return nil
}

func (b *artifactsOnlyExternalBoundary) VerifyManagedRun(_ context.Context, plan biz.PipelineDispatchPlan, association biz.ManagedStepAssociation, role string) error {
	b.t.Helper()
	if plan.OperationID != b.authority.OperationID || plan.Environment.NamespaceUID != b.authority.NamespaceUID || association != b.association || role != b.role {
		return biz.ErrManagedStepUnauthorized
	}
	b.runChecks++
	return nil
}

// Every external Model operation is observable, including reads and tickets.
// Any call is a failure: None does not have an original Model delivery intent.
type artifactsOnlyModelBoundary struct{ calls []string }

func (b *artifactsOnlyModelBoundary) PrepareArtifact(context.Context, biz.TrainedArtifactRequest) (biz.TrainedArtifactSession, error) {
	b.calls = append(b.calls, "prepare")
	return biz.TrainedArtifactSession{}, errors.New("unexpected Model prepare")
}
func (b *artifactsOnlyModelBoundary) ReadArtifact(context.Context, biz.TrainedArtifactRequest) (biz.TrainedArtifactObservation, error) {
	b.calls = append(b.calls, "read")
	return biz.TrainedArtifactObservation{}, errors.New("unexpected Model read")
}
func (b *artifactsOnlyModelBoundary) ArtifactUploadGrant(context.Context, biz.TrainedArtifactRequest, biz.TrainedArtifactSession) (biz.TrainedArtifactUploadGrant, error) {
	b.calls = append(b.calls, "grant")
	return biz.TrainedArtifactUploadGrant{}, errors.New("unexpected Model grant")
}
func (b *artifactsOnlyModelBoundary) CompleteArtifact(context.Context, biz.TrainedArtifactRequest, biz.TrainedArtifactSession) (biz.FinetuningModelReceipt, error) {
	b.calls = append(b.calls, "complete")
	return biz.FinetuningModelReceipt{}, errors.New("unexpected Model complete")
}
func (b *artifactsOnlyModelBoundary) CopyTrainedArtifact(context.Context, biz.Execution, cpup01.FixedObjectRef, biz.TrainedArtifactUploadGrant) error {
	b.calls = append(b.calls, "copy")
	return errors.New("unexpected Model copy")
}

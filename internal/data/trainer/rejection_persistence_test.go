package trainer_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

// Only the external Kubernetes responses are fixtures. Admission, the consumed
// creation permit, real adapter decoding, rejection persistence and close use
// their production implementations with restricted PostgreSQL connections.
func TestTrainingActual422PersistsAndClosesWithoutSecondPost(t *testing.T) {
	f := newTrainingFixture(t)
	raw, err := os.ReadFile("testdata/training-create-invalid-422.json")
	if err != nil {
		t.Fatal(err)
	}
	f.createStatusCode, f.createStatusBody = 422, string(raw)
	snapshot := conformance.SnapshotV1()
	snapshot.Release.Runtime = f.plan.Snapshot.Release.Runtime
	snapshot.Environment.NamespaceName = f.plan.Workspace.NamespaceName
	snapshot.Environment.NamespaceUID = f.plan.Workspace.NamespaceUID
	snapshot.Environment.Identities.TrainerServiceAccount = f.plan.Snapshot.Environment.Identities.TrainerServiceAccount
	snapshot.Workspace.StorageClass = f.plan.Snapshot.Workspace.StorageClass
	accepted := time.Now().UTC().Truncate(time.Microsecond)
	snapshot.DeadlineAt = accepted.Add(time.Hour)
	intent := cpup01.Intent{Name: "actual-422-refusal", Kind: "GENERAL_TRAINING", PresetID: snapshot.Release.PresetID, DatasetVersionID: snapshot.Input.InputVersionID}
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	specHash, err := snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	admission := biz.Admission{TenantID: f.plan.TenantID, ExecutionID: f.plan.ExecutionID, OperationID: f.plan.OperationID, Actor: "governance:user:422-fixture", Intent: intent, IntentHash: intentHash, Snapshot: snapshot, SpecHash: specHash, AcceptedAt: accepted}
	open := postgres.Prepare(t)
	pool := open()
	ctx := context.Background()
	if _, err := execution.New(pool).Accept(ctx, admission); err != nil {
		t.Fatal("real admission", err)
	}
	dispatches := submission.New(pool)
	dispatch, err := dispatches.Reserve(ctx, biz.PipelineDispatchRequest{Admission: admission, Owner: biz.PipelineOwnerConfiguration{Reference: "422-fixture-owner", RevisionSHA256: strings.Repeat("a", 64), PipelineRoot: "s3://fixture-kfp-artifacts/managed-root"}})
	if err != nil || dispatch.SendPermit == nil {
		t.Fatal("real dispatch", err)
	}
	authority := biz.RunAuthorityCandidate{TenantID: admission.TenantID, ExecutionID: admission.ExecutionID, OperationID: admission.OperationID, SpecHash: specHash, AttemptID: dispatch.Dispatch.AttemptID, PlanHash: dispatch.Dispatch.PlanHash, RunID: "55555555-6666-4777-8888-999999999999", NamespaceName: snapshot.Environment.NamespaceName, NamespaceUID: snapshot.Environment.NamespaceUID, WorkflowName: "original-refused-workflow", WorkflowUID: "cccccccc-dddd-4eee-8fff-111111111111"}
	if _, err := dispatches.BindRunAuthority(ctx, authority); err != nil {
		t.Fatal(err)
	}
	workspace := f.plan.Workspace
	workspace.PreparedManifestSHA256, workspace.PreparedManifestBytes = strings.Repeat("1", 64), 256
	facts := lifecycle.New(pool)
	if _, _, err := facts.RecordPrepared(ctx, authority, workspace); err != nil {
		t.Fatal(err)
	}
	reserved, err := facts.ReserveTraining(ctx, authority)
	if err != nil || !reserved.SendPermit || reserved.State.Training == nil {
		t.Fatal("original permit", err)
	}
	f.plan = *reserved.State.Training
	_, err = f.adapter(t).CreateTraining(ctx, f.plan)
	var rejected *biz.TrainingCreationRejection
	if !errors.As(err, &rejected) || rejected.StatusCode != 422 || rejected.RequestSHA256 != f.plan.RequestSHA256 || rejected.NamespaceUID != workspace.NamespaceUID {
		t.Fatalf("real decoded 422 was not bound to the original plan: %v", err)
	}
	state, err := facts.RecordTrainingRejection(ctx, authority, *rejected)
	if err != nil || state.TrainingRejection == nil || state.TrainingRejection.ObservedAt.IsZero() {
		t.Fatal("original API rejection was not durable", err)
	}
	original := *state.TrainingRejection
	encoded, err := json.Marshal(state.TrainingRejection)
	if err != nil || strings.Contains(string(encoded), "Use the fixed versioned runtime") || strings.Contains(string(encoded), "ani-kfp-trainjob") {
		t.Fatal("untrusted API message became durable")
	}
	pool.Close()
	facts = lifecycle.New(open())
	state, err = facts.GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || state.TrainingRejection == nil || *state.TrainingRejection != original {
		t.Fatal("reconnect lost the exact refusal", err)
	}
	if retry, err := facts.ReserveTraining(ctx, authority); err != nil || retry.SendPermit {
		t.Fatal("rejection granted another POST permit", err)
	}
	closing, _, err := facts.RequestRuntimeClose(ctx, authority, "STEP_FAILED")
	if err != nil {
		t.Fatal(err)
	}
	zero, failed := int32(0), int32(1)
	evidence := biz.ManagedCloseEvidence{RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, ObservedAt: time.Now().UTC(), Resources: []biz.RuntimeResource{{APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: "prepare", UID: "prepare-original-uid", OwnerUID: authority.WorkflowUID, APIObjectPresent: true, Terminal: true, ExitCode: &zero}, {APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: "train-wait", UID: "wait-original-uid", OwnerUID: authority.WorkflowUID, APIObjectPresent: true, Terminal: true, ExitCode: &failed}}, SkippedTasks: []biz.ManagedSkippedTask{{TaskName: "collect", TaskID: "collect-task"}, {TaskName: "publish", TaskID: "publish-task"}}}
	closed, err := facts.ConfirmRuntimeClosed(ctx, authority, closing.CloseGeneration, biz.TrainingRuntimeObservation{}, evidence)
	if err != nil || closed.ClosedAt == nil || closed.TrainingRejection == nil || *closed.TrainingRejection != original {
		t.Fatal("complete rejection and writer evidence did not close", err)
	}
	readback, err := execution.New(open()).Get(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || readback.States.Compute != biz.ComputeStateFailed || readback.States.Close != biz.CloseState("CLOSED") {
		t.Fatal("422 refusal lost failed/CLOSED projection", err)
	}
	if f.posts != 1 || f.train != nil {
		t.Fatal("rejection issued a second POST or fabricated a training object")
	}
}

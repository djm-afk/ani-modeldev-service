package lifecycle_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
)

func TestTrainingRejectionBindsConsumedPlanAndDatabaseTimeAcrossCloseFence(t *testing.T) {
	open, _, authority, workspace := runtimeFixture(t)
	ctx := context.Background()
	pool := open()
	repository := lifecycle.New(pool)
	if _, err := repository.RecordTrainingRejection(ctx, authority, biz.TrainingCreationRejection{StatusCode: 403}); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("rejection invented a missing creation intent", err)
	}
	if _, _, err := repository.RecordPrepared(ctx, authority, workspace); err != nil {
		t.Fatal(err)
	}
	reserved, err := reserveTrainingV21(t, repository, ctx, authority)
	if err != nil || !reserved.SendPermit {
		t.Fatal("original consumed permit", err)
	}
	rejection := biz.TrainingCreationRejection{RequestSHA256: reserved.State.Training.RequestSHA256, NamespaceUID: workspace.NamespaceUID, StatusCode: 403, ObservedAt: time.Now().Add(24 * time.Hour)}
	for _, invalid := range []biz.TrainingCreationRejection{
		{RequestSHA256: strings.Repeat("f", 64), NamespaceUID: rejection.NamespaceUID, StatusCode: 403},
		{RequestSHA256: rejection.RequestSHA256, NamespaceUID: "different-namespace", StatusCode: 403},
		{RequestSHA256: rejection.RequestSHA256, NamespaceUID: rejection.NamespaceUID, StatusCode: 503},
	} {
		if _, err := repository.RecordTrainingRejection(ctx, authority, invalid); !errors.Is(err, biz.ErrRuntimeConflict) {
			t.Fatal("unbound or unknown refusal became durable", err)
		}
	}
	closing, _, err := repository.RequestRuntimeClose(ctx, authority, "STEP_FAILED")
	if err != nil {
		t.Fatal(err)
	}
	zero := int32(0)
	failed := int32(1)
	evidence := biz.ManagedCloseEvidence{RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, ObservedAt: time.Now().UTC(), Resources: []biz.RuntimeResource{{APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: "prepare", UID: "prepare-proof-uid", OwnerUID: authority.WorkflowUID, APIObjectPresent: true, Terminal: true, ExitCode: &zero}, {APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: "train-wait", UID: "wait-proof-uid", OwnerUID: authority.WorkflowUID, APIObjectPresent: true, Terminal: true, ExitCode: &failed}}, SkippedTasks: []biz.ManagedSkippedTask{{TaskName: "collect", TaskID: "collect-task"}, {TaskName: "publish", TaskID: "publish-task"}}}
	if _, err := repository.ConfirmRuntimeClosed(ctx, authority, closing.CloseGeneration, biz.TrainingRuntimeObservation{}, evidence); !errors.Is(err, biz.ErrTrainingUncertain) {
		t.Fatal("404-equivalent absence closed an unresolved consumed plan", err)
	}
	var before, after time.Time
	if err := pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&before); err != nil {
		t.Fatal(err)
	}
	state, err := repository.RecordTrainingRejection(ctx, authority, rejection)
	if err != nil {
		t.Fatal("a complete original response cannot be erased by a concurrent close fence", err)
	}
	if err := pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if state.TrainingRejection == nil || state.TrainingRejection.ObservedAt.Before(before) || state.TrainingRejection.ObservedAt.After(after) || state.Training.RequestSHA256 != rejection.RequestSHA256 || state.CloseGeneration != closing.CloseGeneration {
		t.Fatal("receipt did not use the database observation time and original fenced plan")
	}
	original := *state.TrainingRejection
	pool.Close()
	repository = lifecycle.New(open())
	replayed, err := repository.RecordTrainingRejection(ctx, authority, rejection)
	if err != nil || replayed.TrainingRejection == nil || *replayed.TrainingRejection != original {
		t.Fatal("reconnect replay changed the immutable refusal", err)
	}
	rejection.StatusCode = 400
	if _, err := repository.RecordTrainingRejection(ctx, authority, rejection); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("conflicting refusal overwrote the original receipt", err)
	}
	if _, err := repository.RecordTrainingHandle(ctx, authority, biz.TrainingHandle{NamespaceUID: workspace.NamespaceUID, PVCUID: workspace.PVCUID, TrainJobUID: "unexpected-trainjob-uid"}); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("rejected creation admitted a late conflicting handle", err)
	}
	if retry, err := reserveTrainingV21(t, repository, ctx, authority); err != nil || retry.SendPermit || retry.State.Training.RequestSHA256 != rejection.RequestSHA256 {
		t.Fatal("refusal regenerated or cleared the consumed plan", err)
	}
	if _, err := repository.ConfirmRuntimeClosed(ctx, authority, closing.CloseGeneration, biz.TrainingRuntimeObservation{}, biz.ManagedCloseEvidence{}); !errors.Is(err, biz.ErrRuntimeNotReady) {
		t.Fatal("refusal bypassed full writer evidence", err)
	}
	if closed, err := repository.ConfirmRuntimeClosed(ctx, authority, closing.CloseGeneration, biz.TrainingRuntimeObservation{}, evidence); err != nil || closed.ClosedAt == nil || closed.TrainingRejection == nil {
		t.Fatal("original refusal plus full writer evidence did not close", err)
	}
}

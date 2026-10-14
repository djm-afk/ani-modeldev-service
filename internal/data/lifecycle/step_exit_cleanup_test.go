package lifecycle_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
)

func stepCleanupEvidence(closed biz.ExecutionRuntime, authority biz.RunAuthorityCandidate) biz.ManagedCloseEvidence {
	evidence := *closed.CloseEvidence
	evidence.Resources = append([]biz.RuntimeResource{}, evidence.Resources...)
	zero := int32(0)
	evidence.Resources = append(evidence.Resources, biz.RuntimeResource{APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: "closed-control-observer", UID: "closed-control-observer-uid", OwnerUID: authority.WorkflowUID, Terminal: true, APIObjectPresent: true, ExitCode: &zero})
	evidence.ObservedAt = time.Now().UTC()
	evidence.OwnerTermination = &biz.ManagedOwnerTermination{RunState: "SUCCEEDED", WorkflowPhase: "Succeeded", RunFinishedAt: evidence.ObservedAt.Add(-time.Second), WorkflowFinishedAt: evidence.ObservedAt.Add(-time.Second), WorkflowResourceVersion: "owner-proof-version", WorkflowNodesSHA256: strings.Repeat("c", 64)}
	return evidence
}

// The API proof is supplied at its typed boundary here; persistence is the real
// restricted PostgreSQL repository, reopened between every irreversible stage.
func TestStepExitCleanupPersistsBeforeReleaseAndResumesClosedScan(t *testing.T) {
	open, admission, authority, workspace := runtimeFixture(t)
	ctx := context.Background()
	repository := lifecycle.New(open())
	closed := naturalClosedFixture(t, repository, admission, authority, workspace)
	ids, err := repository.ListPendingClosures(ctx, biz.PipelineDispatchBinding{TenantID: admission.TenantID, Environment: admission.Snapshot.Environment, Owner: biz.PipelineOwnerConfiguration{Reference: "runtime-fixture-owner", RevisionSHA256: strings.Repeat("a", 64), PipelineRoot: "s3://fixture-kfp-artifacts/managed-root"}}, "", 10)
	if err != nil || len(ids) != 1 || ids[0] != admission.ExecutionID {
		t.Fatalf("CLOSED with unreleased exits vanished from restart scan: %v %v", ids, err)
	}
	evidence := stepCleanupEvidence(closed, authority)
	started, err := repository.BeginStepExitCleanup(ctx, authority, closed.CloseGeneration, evidence)
	if err != nil || started.StepExitCleanup == nil || started.StepExitCleanup.CompletedAt != nil || !reflect.DeepEqual(started.CloseEvidence, closed.CloseEvidence) || !started.ClosedAt.Equal(*closed.ClosedAt) || !reflect.DeepEqual(started.Publication, closed.Publication) {
		t.Fatal("durable cleanup plan changed the original close or publication", err)
	}
	repository = lifecycle.New(open())
	reloaded, err := repository.GetRuntime(ctx, admission.TenantID, admission.ExecutionID)
	if err != nil || !reflect.DeepEqual(reloaded.StepExitCleanup, started.StepExitCleanup) {
		t.Fatal("reconnect lost the committed full step exits", err)
	}
	removed := append([]biz.RuntimeResource{}, evidence.Resources...)
	for i := range removed {
		removed[i].APIObjectPresent = false
	}
	if _, err := repository.CompleteStepExitCleanup(ctx, authority, closed.CloseGeneration, removed[:len(removed)-1]); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("partial external absence became a complete cleanup receipt", err)
	}
	completed, err := repository.CompleteStepExitCleanup(ctx, authority, closed.CloseGeneration, removed)
	if err != nil || completed.StepExitCleanup.CompletedAt == nil {
		t.Fatal("independently confirmed removals were not persisted", err)
	}
	repository = lifecycle.New(open())
	visible, err := repository.GetRuntime(ctx, admission.TenantID, admission.ExecutionID)
	if err != nil || !reflect.DeepEqual(visible.StepExitCleanup, completed.StepExitCleanup) {
		t.Fatal("completed audit lost on process restart", err)
	}
	repeated, err := repository.CompleteStepExitCleanup(ctx, authority, closed.CloseGeneration, removed)
	if err != nil || repeated.OwnerRevision != completed.OwnerRevision || !repeated.StepExitCleanup.CompletedAt.Equal(*completed.StepExitCleanup.CompletedAt) {
		t.Fatal("cleanup replay renewed its original database receipt", err)
	}
	ids, err = repository.ListPendingClosures(ctx, biz.PipelineDispatchBinding{TenantID: admission.TenantID, Environment: admission.Snapshot.Environment, Owner: biz.PipelineOwnerConfiguration{Reference: "runtime-fixture-owner", RevisionSHA256: strings.Repeat("a", 64), PipelineRoot: "s3://fixture-kfp-artifacts/managed-root"}}, "", 10)
	if err != nil || len(ids) != 0 {
		t.Fatal("completed cleanup remained in the restart scan", ids, err)
	}
}

func TestStepExitCleanupRejectsUncommittedChangedGenerationAndForgedCompletion(t *testing.T) {
	open, admission, authority, workspace := runtimeFixture(t)
	ctx := context.Background()
	repository := lifecycle.New(open())
	if _, err := repository.BeginStepExitCleanup(ctx, authority, 1, biz.ManagedCloseEvidence{}); !errors.Is(err, biz.ErrRuntimeNotReady) {
		t.Fatal("unclosed execution supplied cleanup authority", err)
	}
	closed := naturalClosedFixture(t, repository, admission, authority, workspace)
	evidence := stepCleanupEvidence(closed, authority)
	if _, err := repository.BeginStepExitCleanup(ctx, authority, closed.CloseGeneration+1, evidence); !errors.Is(err, biz.ErrRuntimeNotReady) {
		t.Fatal("another close generation supplied cleanup authority", err)
	}
	if _, err := repository.CompleteStepExitCleanup(ctx, authority, closed.CloseGeneration, nil); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("completion without a committed original cleanup plan passed", err)
	}
	changed := evidence
	changed.Resources = append([]biz.RuntimeResource{}, evidence.Resources...)
	changed.Resources[0].UID = "same-name-replacement-uid"
	if _, err := repository.BeginStepExitCleanup(ctx, authority, closed.CloseGeneration, changed); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("changed original exit UID entered the durable cleanup receipt", err)
	}
	if _, err := repository.BeginStepExitCleanup(ctx, authority, closed.CloseGeneration, evidence); err != nil {
		t.Fatal(err)
	}
	removed := append([]biz.RuntimeResource{}, evidence.Resources...)
	for i := range removed {
		removed[i].APIObjectPresent = false
	}
	removed[0].UID = "foreign-removed-uid"
	if _, err := repository.CompleteStepExitCleanup(ctx, authority, closed.CloseGeneration, removed); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("foreign resource absence became an original cleanup completion", err)
	}
}

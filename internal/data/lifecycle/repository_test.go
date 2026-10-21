package lifecycle_test

import (
	"context"
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func runtimeFixture(t *testing.T, lifetime ...time.Duration) (func() *pgxpool.Pool, biz.Admission, biz.RunAuthorityCandidate, biz.WorkspaceBinding) {
	t.Helper()
	open := postgres.Prepare(t)
	snapshot := conformance.SnapshotV1()
	accepted := time.Now().UTC().Truncate(time.Microsecond)
	snapshot.DeadlineAt = accepted.Add(time.Hour)
	if len(lifetime) != 0 {
		snapshot.DeadlineAt = accepted.Add(lifetime[0])
	}
	intent := cpup01.Intent{Name: "main-runtime", Kind: "GENERAL_TRAINING", ImplementationID: snapshot.Implementation.ImplementationID, ComputePresetID: snapshot.ComputeOrigin.ComputePresetID, MLP: &cpup01.MLPParameters{}, DatasetVersionID: snapshot.Input.InputVersionID}
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	specHash := ""
	admission := biz.Admission{TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:user:42", OperationID: "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff", ExecutionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", Intent: intent, IntentHash: intentHash, Snapshot: snapshot, SpecHash: specHash, AcceptedAt: accepted}
	admission = admissionfixture.Bind(t, admission)
	snapshot = admission.Snapshot
	pool := open()
	ctx := context.Background()
	if _, err = execution.New(pool).Accept(ctx, admission); err != nil {
		t.Fatal("runtime preflight admission", err)
	}
	repository := submission.New(pool)
	reserved, err := repository.Reserve(ctx, biz.PipelineDispatchRequest{Admission: admission, Owner: biz.PipelineOwnerConfiguration{Reference: "runtime-fixture-owner", RevisionSHA256: strings.Repeat("a", 64), PipelineRoot: "s3://fixture-kfp-artifacts/managed-root"}})
	if err != nil || reserved.SendPermit == nil {
		t.Fatal("runtime preflight dispatch", err)
	}
	authority := biz.RunAuthorityCandidate{TenantID: admission.TenantID, ExecutionID: admission.ExecutionID, OperationID: admission.OperationID, SpecHash: admission.SpecHash, AttemptID: reserved.Dispatch.AttemptID, PlanHash: reserved.Dispatch.PlanHash, RunID: "55555555-6666-4777-8888-999999999999", NamespaceName: snapshot.Environment.NamespaceName, NamespaceUID: snapshot.Environment.NamespaceUID, WorkflowName: "main-runtime-workflow", WorkflowUID: "cccccccc-dddd-4eee-8fff-111111111111"}
	if _, err = repository.BindRunAuthority(ctx, authority); err != nil {
		t.Fatal("runtime preflight authority", err)
	}
	workspace := biz.WorkspaceBinding{Mode: snapshot.Workspace.Mode, NamespaceName: snapshot.Environment.NamespaceName, NamespaceUID: snapshot.Environment.NamespaceUID, PVCName: biz.ExpectedWorkspacePVCName(admission.ExecutionID), PVCUID: "dddddddd-eeee-4fff-8111-222222222222", InputSubpath: snapshot.Workspace.InputSubpath, TrainingSubpath: snapshot.Workspace.TrainingSubpath, ReportsSubpath: snapshot.Workspace.ReportsSubpath, PublicationSubpath: snapshot.Workspace.PublicationSubpath, PreparedManifestSHA256: strings.Repeat("1", 64), PreparedManifestBytes: 256}
	startManagedRoleV21(t, lifecycle.New(pool), authority, "prepare")
	t.Log("RUNTIME_PREFLIGHT PASS: real restricted PostgreSQL admission, dispatch and Run authority")
	return open, admission, authority, workspace
}

func TestRuntimePersistsPrepareTrainingPublicationAndCloseAcrossReconnect(t *testing.T) {
	open, admission, authority, workspace := runtimeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := open()
	repository := lifecycle.New(pool)
	prepared, replayed, err := repository.RecordPrepared(ctx, authority, workspace)
	if err != nil {
		t.Fatalf("RUNTIME_BEHAVIOR: real prepared workspace was not committed: %v", err)
	}
	if replayed || prepared.Workspace == nil || *prepared.Workspace != workspace {
		t.Fatal("prepared receipt mismatch")
	}
	reserved, err := reserveTrainingV21(t, repository, ctx, authority)
	if err != nil || !reserved.SendPermit || reserved.State.Training == nil || reserved.State.Training.RequestSHA256 == "" {
		t.Fatalf("first training creation did not receive committed intent and unique permit: %v", err)
	}
	pool.Close()
	repository = lifecycle.New(open())
	repeated, err := reserveTrainingV21(t, repository, ctx, authority)
	if err != nil || repeated.SendPermit || repeated.State.Training == nil || repeated.State.Training.RequestSHA256 != reserved.State.Training.RequestSHA256 {
		t.Fatalf("reconnect repeated training permission or changed frozen plan: %v", err)
	}
	handle := biz.TrainingHandle{NamespaceUID: workspace.NamespaceUID, TrainJobUID: "eeeeeeee-ffff-4111-8222-333333333333", PVCUID: workspace.PVCUID}
	if _, err = repository.RecordTrainingHandle(ctx, authority, handle); err != nil {
		t.Fatal(err)
	}
	zero := int32(0)
	observation := biz.TrainingRuntimeObservation{Handle: handle, Resources: []biz.RuntimeResource{{APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainJob", Namespace: workspace.NamespaceName, Name: reserved.State.Training.Name, UID: handle.TrainJobUID, APIObjectPresent: true, Terminal: true}, {APIVersion: "jobset.x-k8s.io/v1alpha2", Kind: "JobSet", Namespace: workspace.NamespaceName, Name: "main-training-jobset", UID: "jobset-runtime-uid", OwnerUID: handle.TrainJobUID, APIObjectPresent: true, Terminal: true}, {APIVersion: "v1", Kind: "Pod", Namespace: workspace.NamespaceName, Name: "main-training-pod", UID: "pod-runtime-uid", OwnerUID: "jobset-runtime-uid", APIObjectPresent: true, Terminal: true, ExitCode: &zero}}, Outcome: "SUCCEEDED", WritersAbsent: true, ObservedAt: time.Now().UTC().Truncate(time.Microsecond)}
	if _, err = repository.RecordTrainingObservation(ctx, authority, observation); err != nil {
		t.Fatal(err)
	}
	publication := publicationFixture(t, admission, authority)
	published, replayed, err := recordPublicationV21(t, repository, ctx, authority, publication)
	if err != nil || replayed || published.Publication == nil {
		t.Fatalf("verified publication was not committed: %v", err)
	}
	if _, replayed, err = recordPublicationV21(t, repository, ctx, authority, publication); err != nil || !replayed {
		t.Fatalf("publication replay changed the main publication: %v", err)
	}
	closing, replayed, err := repository.RequestRuntimeClose(ctx, authority, "NATURAL_TERMINAL")
	if err != nil || replayed || closing.CloseGeneration == 0 || closing.ClosedAt != nil {
		t.Fatalf("natural close intent failed: %v", err)
	}
	evidence := biz.ManagedCloseEvidence{RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, ObservedAt: time.Now().UTC()}
	for _, step := range []string{"prepare", "train-wait", "collect", "publish"} {
		evidence.Resources = append(evidence.Resources, biz.RuntimeResource{APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: "managed-" + step, UID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(step)).String(), OwnerUID: authority.WorkflowUID, APIObjectPresent: true, Terminal: true, ExitCode: &zero})
	}
	closed, err := repository.ConfirmRuntimeClosed(ctx, authority, closing.CloseGeneration, observation, evidence)
	if err != nil || closed.ClosedAt == nil {
		t.Fatalf("verified writer absence did not close execution: %v", err)
	}
	visible, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || visible.ClosedAt == nil || visible.Publication == nil || visible.TrainingHandle == nil || *visible.TrainingHandle != handle {
		t.Fatalf("independent reconnect lost closed runtime facts: %v", err)
	}
	readback, err := execution.New(open()).Get(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || readback.States.Compute != biz.ComputeState("SUCCEEDED") || readback.States.Delivery != biz.DeliveryState("PUBLISHED") || readback.States.Close != biz.CloseState("CLOSED") || readback.States.Resource != biz.ResourceStateNotApplicable || readback.OwnerRevision != visible.OwnerRevision {
		t.Fatalf("RUNTIME_STATE_PROJECTION: committed closed execution still returned stale admission states: %+v; %v", readback.States, err)
	}
	if _, err = lifecycle.New(open()).GetRuntime(ctx, "99999999-2222-4333-8444-555555555555", authority.ExecutionID); !errors.Is(err, biz.ErrExecutionNotFound) {
		t.Fatal("cross-tenant runtime read was not rejected", err)
	}
	t.Log("MAIN_FLOW_RUNTIME PASS: prepared -> unique training permit -> handle -> successful resource history -> verified publication -> natural CLOSED survives reconnect")
}

func TestRuntimeCloseBeforeTrainingRejectsCreationPermit(t *testing.T) {
	open, admission, authority, workspace := runtimeFixture(t)
	ctx := context.Background()
	pool := open()
	repository := lifecycle.New(pool)
	if _, _, err := repository.RecordPrepared(ctx, authority, workspace); err != nil {
		t.Fatalf("RUNTIME_BEHAVIOR: prepared fact failed: %v", err)
	}
	if _, err := execution.New(pool).ApplyCloseIntent(ctx, biz.CloseIntent{TenantID: admission.TenantID, ExecutionID: admission.ExecutionID, OperationID: admission.OperationID, SpecHash: admission.SpecHash, SourceGeneration: 1, Reason: biz.CloseReasonUserStop, RequestedAt: time.Now().UTC().Truncate(time.Microsecond), RequestedActor: admission.Actor}); err != nil {
		t.Fatal(err)
	}
	receipt, err := reserveTrainingV21(t, repository, ctx, authority)
	if !errors.Is(err, biz.ErrPipelineDispatchBlocked) || receipt.SendPermit {
		t.Fatalf("stop-before-create granted a new training permit: %v", err)
	}
	persisted, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || persisted.Training != nil {
		t.Fatalf("rejected creation left a training intent: %v", err)
	}
}

func TestRuntimeFailedCloseAcceptsObservedSkippedStepsAcrossReconnect(t *testing.T) {
	open, _, authority, _ := runtimeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	repository := lifecycle.New(open())
	closing, replayed, err := repository.RequestRuntimeClose(ctx, authority, "STEP_FAILED")
	if err != nil || replayed || closing.CloseGeneration == 0 || closing.ClosedAt != nil {
		t.Fatalf("failed prepare must first commit the close fence: %v", err)
	}
	exit := int32(42)
	evidence := biz.ManagedCloseEvidence{RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, ObservedAt: time.Now().UTC(),
		Resources:    []biz.RuntimeResource{{APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: "managed-prepare", UID: "prepare-failed-pod-uid", OwnerUID: authority.WorkflowUID, APIObjectPresent: true, Terminal: true, ExitCode: &exit}},
		SkippedTasks: []biz.ManagedSkippedTask{{TaskName: "train-wait", TaskID: "skipped-train-wait"}, {TaskName: "collect", TaskID: "skipped-collect"}, {TaskName: "publish", TaskID: "skipped-publish"}},
	}
	closed, err := repository.ConfirmRuntimeClosed(ctx, authority, closing.CloseGeneration, biz.TrainingRuntimeObservation{}, evidence)
	if err != nil || closed.ClosedAt == nil {
		t.Fatalf("FAILED_CLOSE_SKIPPED: stopped prepare plus three explicitly skipped steps must close: %v", err)
	}
	visible, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || visible.ClosedAt == nil || visible.CloseReason != "STEP_FAILED" || visible.CloseEvidence == nil || len(visible.CloseEvidence.SkippedTasks) != 3 || len(visible.CloseEvidence.Resources) != 1 || visible.CloseEvidence.Resources[0].ExitCode == nil || *visible.CloseEvidence.Resources[0].ExitCode != 42 || visible.Publication != nil || visible.Training != nil {
		t.Fatalf("reconnect must preserve the failed writer and skipped task evidence without inventing training or publication: %+v; %v", visible, err)
	}
}

func publicationFixture(t *testing.T, admission biz.Admission, authority biz.RunAuthorityCandidate) biz.RuntimePublication {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	scope := admission.Snapshot.PublicationScope
	version := "runtime-version-1"
	publication := biz.RuntimePublication{ID: "ffffffff-1111-4222-8333-444444444444", LogicalKey: "main", ReceiptID: "runtime-receipt", Upload: biz.UploadCompletion{RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, TaskID: "publish", PodUID: "upload-pod-uid", ContainerName: "main", CompletedAt: now, ObservedAt: now}, VerifiedAt: now}
	var files []cpup01.OutputFile
	for _, required := range admission.Snapshot.OutputContract.RequiredFiles {
		file := cpup01.OutputFile{RelativePath: required.RelativePath, Role: required.Role, SizeBytes: 1, SHA256: strings.Repeat("2", 64)}
		files = append(files, file)
		publication.Files = append(publication.Files, biz.PublishedRuntimeFile{ArtifactID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(file.RelativePath)).String(), File: file, Object: cpup01.FixedObjectRef{StorageConnectionID: scope.StorageConnectionID, Bucket: scope.Bucket, Key: strings.TrimSuffix(scope.ApprovedPrefix, "/") + "/" + admission.ExecutionID + "/" + file.RelativePath, VersionID: &version, SizeBytes: file.SizeBytes, SHA256: file.SHA256}})
	}
	manifest, digest, err := cpup01.OutputManifestBytes(cpup01.AdmissionEnvelope(admission), files)
	if err != nil {
		t.Fatal(err)
	}
	publication.Manifest = cpup01.FixedObjectRef{StorageConnectionID: scope.StorageConnectionID, Bucket: scope.Bucket, Key: strings.TrimSuffix(scope.ApprovedPrefix, "/") + "/" + admission.ExecutionID + "/output-manifest.json", VersionID: &version, SizeBytes: int64(len(manifest)), SHA256: digest}
	if admission.Snapshot.OutputContract.CreateTarBundle {
		bundle := publication.Manifest
		bundle.Key = strings.TrimSuffix(scope.ApprovedPrefix, "/") + "/" + admission.ExecutionID + "/bundle.tar"
		bundle.SHA256 = strings.Repeat("3", 64)
		publication.Bundle = &bundle
	}
	return publication
}

package submittest_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestCleanupRecoveryPreservesOriginalAuditAndDoesNotRepeatBoundary(t *testing.T) {
	for _, phase := range []string{"STARTED", "NEEDS_REVIEW"} {
		t.Run(phase, func(t *testing.T) {
			pool, repository, plan := closedCleanupRecoveryFixture(t)
			ctx := context.Background()
			original, _, err := repository.ReserveCleanup(ctx, plan, "governance:user:9001")
			if err != nil {
				t.Fatal(err)
			}
			if phase == "NEEDS_REVIEW" {
				original, err = repository.ExecuteCleanup(ctx, plan.TenantID, plan.ExecutionID, plan.PlanHash, func(record biz.OperationRecord) (biz.CleanupReceipt, error) {
					receipt := *record.Cleanup
					receipt.Phase = "NEEDS_REVIEW"
					receipt.Requested = append(receipt.Requested, plan.Targets[0])
					return receipt, biz.ErrCleanupUncertain
				})
				if !errors.Is(err, biz.ErrCleanupUncertain) || original.CompletedAt.IsZero() {
					t.Fatal("original uncertain apply audit unavailable", err)
				}
			}
			before := readCleanupRecoveryAudit(t, pool, plan)
			calls := 0
			partial := func(record biz.OperationRecord) (biz.CleanupReceipt, error) {
				calls++
				receipt := cleanupResolution(record, plan)
				receipt.ResolvedAbsent = receipt.ResolvedAbsent[:len(plan.Targets)-1]
				return receipt, nil
			}
			if _, err := repository.ReconcileCleanup(ctx, plan.TenantID, plan.ExecutionID, plan.PlanHash, partial); !errors.Is(err, biz.ErrCleanupConflict) {
				t.Fatal("missing target absence became resolved", err)
			}
			if !reflect.DeepEqual(before, readCleanupRecoveryAudit(t, pool, plan)) {
				t.Fatal("failed absence verification rewrote the original audit")
			}
			if _, err := repository.ReconcileCleanup(ctx, plan.TenantID, plan.ExecutionID, plan.PlanHash, func(record biz.OperationRecord) (biz.CleanupReceipt, error) {
				return cleanupResolution(record, plan), biz.ErrCleanupBlocked
			}); !errors.Is(err, biz.ErrCleanupBlocked) || !reflect.DeepEqual(before, readCleanupRecoveryAudit(t, pool, plan)) {
				t.Fatal("boundary failure changed the audit", err)
			}
			resolved, err := repository.ReconcileCleanup(ctx, plan.TenantID, plan.ExecutionID, plan.PlanHash, func(record biz.OperationRecord) (biz.CleanupReceipt, error) {
				calls++
				// Both restricted pool connections must be free during external
				// reads; the identity row must also have no retained writer lock.
				bounded, cancel := context.WithTimeout(ctx, time.Second)
				defer cancel()
				first, err := pool.Acquire(bounded)
				if err != nil {
					t.Fatal("callback could not acquire first connection")
				}
				defer first.Release()
				second, err := pool.Acquire(bounded)
				if err != nil {
					t.Fatal("callback ran while the repository held a connection")
				}
				defer second.Release()
				tx, err := first.Begin(bounded)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback(ctx) }()
				if _, err := tx.Exec(bounded, "SELECT execution_id FROM modeldev_execution_identities WHERE tenant_id=$1::uuid AND execution_id=$2::uuid FOR UPDATE NOWAIT", plan.TenantID, plan.ExecutionID); err != nil {
					t.Fatal("callback ran while the identity remained locked", err)
				}
				return cleanupResolution(record, plan), nil
			})
			if err != nil || resolved.Phase != "RECONCILED" || resolved.ReconciledFromPhase != phase || resolved.ReconciledAt.Before(original.StartedAt) || !reflect.DeepEqual(resolved.ResolvedAbsent, plan.Targets) {
				t.Fatalf("read-only recovery did not resolve all original targets: %+v %v", resolved, err)
			}
			if resolved.Actor != original.Actor || !resolved.StartedAt.Equal(original.StartedAt) || !resolved.CompletedAt.Equal(original.CompletedAt) || !reflect.DeepEqual(resolved.Requested, original.Requested) || !reflect.DeepEqual(resolved.ConfirmedAbsent, original.ConfirmedAbsent) {
				t.Fatal("reconciliation fabricated or replaced the original apply audit")
			}
			after := readCleanupRecoveryAudit(t, pool, plan)
			if after.actor != before.actor || !after.started.Equal(before.started) || !reflect.DeepEqual(after.completed, before.completed) {
				t.Fatal("reconciliation changed original database actor/timestamps")
			}
			replayed, err := execution.New(pool).ReconcileCleanup(ctx, plan.TenantID, plan.ExecutionID, plan.PlanHash, func(biz.OperationRecord) (biz.CleanupReceipt, error) {
				calls++
				return biz.CleanupReceipt{}, biz.ErrCleanupBlocked
			})
			if err != nil || !reflect.DeepEqual(replayed, resolved) || calls != 2 {
				t.Fatal("resolved replay called the boundary or changed its receipt", err)
			}
		})
	}
}

func TestCleanupRecoveryRejectsChangedAuditAndOwnerPlan(t *testing.T) {
	for _, mutation := range []string{"owner revision", "audit hash", "callback actor", "callback aliased requested"} {
		t.Run(mutation, func(t *testing.T) {
			pool, repository, plan := closedCleanupRecoveryFixture(t)
			ctx := context.Background()
			if _, _, err := repository.ReserveCleanup(ctx, plan, "governance:user:9001"); err != nil {
				t.Fatal(err)
			}
			before := readCleanupRecoveryAudit(t, pool, plan)
			var changed cleanupRecoveryAudit
			_, err := repository.ReconcileCleanup(ctx, plan.TenantID, plan.ExecutionID, plan.PlanHash, func(record biz.OperationRecord) (biz.CleanupReceipt, error) {
				receipt := cleanupResolution(record, plan)
				switch mutation {
				case "owner revision":
					_, err := repository.ApplyCloseIntent(ctx, biz.CloseIntent{TenantID: plan.TenantID, ExecutionID: plan.ExecutionID, OperationID: plan.OperationID, SpecHash: plan.SpecHash, SourceGeneration: 1, Reason: biz.CloseReasonUserStop, RequestedAt: time.Now().UTC().Truncate(time.Microsecond), RequestedActor: "governance:user:9002"})
					if err != nil {
						t.Fatal("concurrent owner fence unavailable", err)
					}
				case "audit hash":
					// Deliberately mutate only this isolated PG fixture to prove
					// the final compare-and-swap cannot overwrite a changed audit.
					if _, err := pool.Exec(ctx, "UPDATE modeldev_execution_cleanup_audits SET receipt=jsonb_set(receipt,'{plan_sha256}',to_jsonb($4::text)) WHERE tenant_id=$1::uuid AND execution_id=$2::uuid AND plan_sha256=$3", plan.TenantID, plan.ExecutionID, plan.PlanHash, strings.Repeat("f", 64)); err != nil {
						t.Fatal(err)
					}
					changed = readCleanupRecoveryAudit(t, pool, plan)
				case "callback actor":
					receipt.Actor = "governance:user:9002"
				case "callback aliased requested":
					record.Cleanup.Requested = append(record.Cleanup.Requested, plan.Targets[0])
					receipt.Requested = record.Cleanup.Requested
				}
				return receipt, nil
			})
			if !errors.Is(err, biz.ErrCleanupConflict) {
				t.Fatal("changed original plan or audit received successful reconciliation", err)
			}
			want := before
			if mutation == "audit hash" {
				want = changed
			}
			if !reflect.DeepEqual(want, readCleanupRecoveryAudit(t, pool, plan)) {
				t.Fatal("failed CAS wrote a reconciliation result")
			}
		})
	}
}

func TestCleanupRecoveryAppliedReplayAndDatabaseResolutionConstraints(t *testing.T) {
	pool, repository, plan := closedCleanupRecoveryFixture(t)
	ctx := context.Background()
	if _, _, err := repository.ReserveCleanup(ctx, plan, "governance:user:9001"); err != nil {
		t.Fatal(err)
	}
	for _, fields := range []map[string]any{
		{"reconciled_from_phase": "STARTED"},
		{"reconciled_from_phase": "APPLIED", "reconciled_at": time.Now().UTC()},
	} {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(fields)
		_, err = tx.Exec(ctx, "UPDATE modeldev_execution_cleanup_audits SET phase='RECONCILED',receipt=receipt || $4::jsonb WHERE tenant_id=$1::uuid AND execution_id=$2::uuid AND plan_sha256=$3", plan.TenantID, plan.ExecutionID, plan.PlanHash, encoded)
		_ = tx.Rollback(ctx)
		var databaseError *pgconn.PgError
		if !errors.As(err, &databaseError) || databaseError.Code != "23514" {
			t.Fatal("database allowed an incomplete reconciliation audit")
		}
	}
	applied, err := repository.ExecuteCleanup(ctx, plan.TenantID, plan.ExecutionID, plan.PlanHash, func(record biz.OperationRecord) (biz.CleanupReceipt, error) {
		receipt := *record.Cleanup
		receipt.Phase, receipt.Requested, receipt.ConfirmedAbsent = "APPLIED", plan.Targets, plan.Targets
		return receipt, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	replayed, err := repository.ReconcileCleanup(ctx, plan.TenantID, plan.ExecutionID, plan.PlanHash, func(biz.OperationRecord) (biz.CleanupReceipt, error) {
		calls++
		return biz.CleanupReceipt{}, biz.ErrCleanupBlocked
	})
	if err != nil || calls != 0 || !reflect.DeepEqual(applied, replayed) {
		t.Fatal("APPLIED reconciliation granted another boundary attempt", err)
	}
}

func TestCleanupRecoveryRejectsAuditMissingCurrentResolutionTimestamp(t *testing.T) {
	pool, repository, plan := closedCleanupRecoveryFixture(t)
	ctx := context.Background()
	if _, _, err := repository.ReserveCleanup(ctx, plan, "governance:user:9001"); err != nil {
		t.Fatal(err)
	}
	// The current writer emits this timestamp even when zero. Omitting it is
	// an obsolete audit shape, distinct from current optional empty slices.
	if _, err := pool.Exec(ctx, "UPDATE modeldev_execution_cleanup_audits SET receipt=receipt-'reconciled_at' WHERE tenant_id=$1::uuid AND execution_id=$2::uuid AND plan_sha256=$3", plan.TenantID, plan.ExecutionID, plan.PlanHash); err != nil {
		t.Fatal(err)
	}
	before := readCleanupRecoveryAudit(t, pool, plan)
	calls := 0
	_, err := repository.ReconcileCleanup(ctx, plan.TenantID, plan.ExecutionID, plan.PlanHash, func(record biz.OperationRecord) (biz.CleanupReceipt, error) {
		calls++
		return cleanupResolution(record, plan), nil
	})
	if !errors.Is(err, biz.ErrPersistence) || calls != 0 {
		t.Fatalf("obsolete audit reached external reconciliation: calls=%d error=%v", calls, err)
	}
	if after := readCleanupRecoveryAudit(t, pool, plan); !reflect.DeepEqual(after, before) {
		t.Fatal("obsolete audit rejection rewrote original evidence")
	}
}

func TestCleanupApplyRemovesParentsBeforeDependentsWithoutChangingReviewedPlan(t *testing.T) {
	for _, scenario := range []struct {
		name, failedKind  string
		requested, absent []string
	}{
		{name: "all original controllers removed", requested: []string{"TrainJob", "JobSet", "Job"}, absent: []string{"TrainJob", "JobSet", "Job"}},
		{name: "unconfirmed TrainJob stops all child deletes", failedKind: "TrainJob", requested: []string{"TrainJob"}, absent: []string{}},
		{name: "unconfirmed JobSet stops Job delete", failedKind: "JobSet", requested: []string{"TrainJob", "JobSet"}, absent: []string{"TrainJob"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			pool, repository, reviewed := closedCleanupRecoveryFixture(t)
			ctx := context.Background()
			before, err := repository.GetOperationRecord(ctx, reviewed.TenantID, reviewed.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			originalPlan, err := json.Marshal(reviewed)
			if err != nil {
				t.Fatal(err)
			}
			// Reviewed plans retain their original child-first canonical order.
			// Dispatch order must not invalidate that existing plan/hash contract.
			if !reflect.DeepEqual(cleanupResourceKinds(reviewed.Targets), []string{"Job", "JobSet", "TrainJob"}) {
				t.Fatal("reviewed plan canonical order changed")
			}
			boundary := &cleanupControllerBoundary{present: map[string]bool{}, failedKind: scenario.failedKind}
			for _, target := range reviewed.Targets {
				boundary.present[target.UID] = true
			}
			operations, err := biz.NewManagedExecutionOperations(repository, &biz.ExecutionCloser{}, boundary)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := operations.ApplyCleanup(ctx, reviewed.TenantID, reviewed.ExecutionID, reviewed.PlanHash, "governance:user:9001")
			if scenario.failedKind == "" {
				if err != nil || receipt.Phase != "APPLIED" || len(boundary.present) != 0 {
					t.Fatalf("PARENT_FIRST_CLEANUP: parent could recreate a deleted dependent: receipt=%+v error=%v remaining=%d", receipt, err, len(boundary.present))
				}
			} else if !errors.Is(err, biz.ErrCleanupUncertain) || receipt.Phase != "NEEDS_REVIEW" {
				t.Fatalf("unconfirmed parent deletion did not retain uncertain audit: %+v %v", receipt, err)
			}
			if !reflect.DeepEqual(cleanupResourceKinds(boundary.attempted), scenario.requested) || !reflect.DeepEqual(cleanupResourceKinds(receipt.Requested), scenario.requested) || !reflect.DeepEqual(cleanupResourceKinds(receipt.ConfirmedAbsent), scenario.absent) {
				t.Fatalf("PARENT_FIRST_CLEANUP: child delete escaped parent confirmation or audit order: attempts=%v requested=%v absent=%v", cleanupResourceKinds(boundary.attempted), cleanupResourceKinds(receipt.Requested), cleanupResourceKinds(receipt.ConfirmedAbsent))
			}
			after, err := execution.New(pool).GetOperationRecord(ctx, reviewed.TenantID, reviewed.ExecutionID)
			if err != nil || after.Cleanup == nil || !reflect.DeepEqual(*after.Cleanup, receipt) {
				t.Fatal("parent-first cleanup receipt was not durably readable", err)
			}
			if !reflect.DeepEqual(before.Execution, after.Execution) || !reflect.DeepEqual(before.Runtime, after.Runtime) || !reflect.DeepEqual(before.Dispatch, after.Dispatch) || !reflect.DeepEqual(before.Authority, after.Authority) {
				t.Fatal("controller cleanup changed original execution, publication, close or run facts")
			}
			recomputed, err := biz.CleanupPlanFor(after)
			encoded, marshalErr := json.Marshal(recomputed)
			if err != nil || marshalErr != nil || string(encoded) != string(originalPlan) {
				t.Fatal("dispatch mutated the reviewed target order or stable plan/hash", err, marshalErr)
			}
		})
	}
}

// Only the Kubernetes delete boundary is supplied by this fixture. A still
// present parent can recreate its dependent, even after that dependent ended.
type cleanupControllerBoundary struct {
	present    map[string]bool
	attempted  []biz.RuntimeResource
	failedKind string
}

func (boundary *cleanupControllerBoundary) VerifyCleanupWritersAbsent(context.Context, biz.OperationRecord) error {
	return nil
}

func (boundary *cleanupControllerBoundary) DeleteExecutionResource(_ context.Context, _ biz.OperationRecord, resource biz.RuntimeResource) error {
	boundary.attempted = append(boundary.attempted, resource)
	if boundary.present[resource.OwnerUID] || resource.Kind == boundary.failedKind {
		return biz.ErrCleanupUncertain
	}
	delete(boundary.present, resource.UID)
	return nil
}

func (boundary *cleanupControllerBoundary) VerifyCleanupResolved(context.Context, biz.OperationRecord) ([]biz.RuntimeResource, error) {
	return nil, biz.ErrCleanupBlocked
}

func cleanupResourceKinds(resources []biz.RuntimeResource) []string {
	kinds := []string{}
	for _, resource := range resources {
		kinds = append(kinds, resource.Kind)
	}
	return kinds
}

func cleanupResolution(record biz.OperationRecord, plan biz.CleanupPlan) biz.CleanupReceipt {
	receipt := *record.Cleanup
	receipt.Phase, receipt.ReconciledFromPhase = "RECONCILED", record.Cleanup.Phase
	receipt.ResolvedAbsent = append([]biz.RuntimeResource{}, plan.Targets...)
	return receipt
}

type cleanupRecoveryAudit struct {
	phase, actor string
	started      time.Time
	completed    *time.Time
	receipt      []byte
}

func readCleanupRecoveryAudit(t *testing.T, pool *pgxpool.Pool, plan biz.CleanupPlan) cleanupRecoveryAudit {
	t.Helper()
	var audit cleanupRecoveryAudit
	if err := pool.QueryRow(context.Background(), "SELECT phase,actor,started_at,completed_at,receipt FROM modeldev_execution_cleanup_audits WHERE tenant_id=$1::uuid AND execution_id=$2::uuid AND plan_sha256=$3", plan.TenantID, plan.ExecutionID, plan.PlanHash).Scan(&audit.phase, &audit.actor, &audit.started, &audit.completed, &audit.receipt); err != nil {
		t.Fatal("isolated cleanup audit read failed")
	}
	return audit
}

// Use the existing runtime/publication ports to produce a genuine closed
// PostgreSQL execution. Only upstream controller/upload observations are facts
// supplied at their external boundaries, as in the lifecycle repository tests.
func closedCleanupRecoveryFixture(t *testing.T) (*pgxpool.Pool, *execution.Repository, biz.CleanupPlan) {
	t.Helper()
	pool := postgres.Prepare(t)()
	ctx := context.Background()
	request := dispatchRequest(t)
	repository := execution.New(pool)
	if _, err := repository.Accept(ctx, request.Admission); err != nil {
		t.Fatal(err)
	}
	dispatch := submission.New(pool)
	reserved, err := dispatch.Reserve(ctx, request)
	if err != nil || reserved.SendPermit == nil {
		t.Fatal("closed cleanup dispatch fixture", err)
	}
	a := request.Admission
	authority := biz.RunAuthorityCandidate{TenantID: a.TenantID, ExecutionID: a.ExecutionID, OperationID: a.OperationID, SpecHash: a.SpecHash, AttemptID: reserved.Dispatch.AttemptID, PlanHash: reserved.Dispatch.PlanHash, RunID: "55555555-6666-4777-8888-999999999999", NamespaceName: a.Snapshot.Environment.NamespaceName, NamespaceUID: a.Snapshot.Environment.NamespaceUID, WorkflowName: "cleanup-workflow", WorkflowUID: "cleanup-workflow-uid"}
	if _, err := dispatch.BindRunAuthority(ctx, authority); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatch.RecordSubmissionConfirmed(ctx, *reserved.SendPermit, biz.PipelineSubmissionObservation{State: biz.PipelineSubmissionConfirmed, RunID: authority.RunID}, time.Now().UTC().Truncate(time.Microsecond)); err != nil {
		t.Fatal(err)
	}
	workspace := biz.WorkspaceBinding{Mode: a.Snapshot.Workspace.Mode, NamespaceName: authority.NamespaceName, NamespaceUID: authority.NamespaceUID, PVCName: biz.ExpectedWorkspacePVCName(a.ExecutionID), PVCUID: "cleanup-workspace-uid", InputSubpath: a.Snapshot.Workspace.InputSubpath, TrainingSubpath: a.Snapshot.Workspace.TrainingSubpath, ReportsSubpath: a.Snapshot.Workspace.ReportsSubpath, PublicationSubpath: a.Snapshot.Workspace.PublicationSubpath, WorkSubpath: a.Snapshot.Workspace.WorkSubpath, PreparedManifestSHA256: strings.Repeat("1", 64), PreparedManifestBytes: 256}
	runtime := lifecycle.New(pool)
	startRole := func(role string) {
		b := biz.FinetuningStepBinding{Role: role, SpecHash: a.SpecHash, RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, TaskID: role + "-cleanup-task", PodName: role + "-cleanup-pod", PodUID: role + "-cleanup-pod-uid"}
		if _, _, err := runtime.ReserveManagedStep(ctx, authority, b); err != nil {
			t.Fatal(err)
		}
	}
	startRole("prepare")
	if _, _, err := runtime.RecordPrepared(ctx, authority, workspace); err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.ReserveRuntime(ctx, authority)
	if err != nil || !rt.SendPermit {
		t.Fatal(err)
	}
	if _, err := runtime.RecordRuntimeBinding(ctx, authority, biz.RuntimeTemplateBinding{NamespaceUID: authority.NamespaceUID, Name: a.Snapshot.RuntimePlan.Name, UID: "cleanup-runtime-uid", ObservedSpecSHA256: a.Snapshot.RuntimePlan.ExpectedSpecSHA256}); err != nil {
		t.Fatal(err)
	}
	training, err := runtime.ReserveTraining(ctx, authority)
	if err != nil || !training.SendPermit {
		t.Fatal(err)
	}
	handle := biz.TrainingHandle{NamespaceUID: workspace.NamespaceUID, PVCUID: workspace.PVCUID, TrainJobUID: "cleanup-train-uid"}
	if _, err := runtime.RecordTrainingHandle(ctx, authority, handle); err != nil {
		t.Fatal(err)
	}
	zero := int32(0)
	observation := biz.TrainingRuntimeObservation{Handle: handle, Outcome: "SUCCEEDED", WritersAbsent: true, ObservedAt: time.Now().UTC(), Resources: []biz.RuntimeResource{
		{APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainJob", Namespace: authority.NamespaceName, Name: training.State.Training.Name, UID: handle.TrainJobUID, APIObjectPresent: true, Terminal: true},
		{APIVersion: "jobset.x-k8s.io/v1alpha2", Kind: "JobSet", Namespace: authority.NamespaceName, Name: "cleanup-set", UID: "cleanup-set-uid", OwnerUID: handle.TrainJobUID, APIObjectPresent: true, Terminal: true},
		{APIVersion: "batch/v1", Kind: "Job", Namespace: authority.NamespaceName, Name: "cleanup-job", UID: "cleanup-job-uid", OwnerUID: "cleanup-set-uid", APIObjectPresent: true, Terminal: true},
		{APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: "cleanup-pod", UID: "cleanup-pod-uid", OwnerUID: "cleanup-job-uid", APIObjectPresent: true, Terminal: true, ExitCode: &zero},
	}}
	if _, err := runtime.RecordTrainingObservation(ctx, authority, observation); err != nil {
		t.Fatal(err)
	}
	publication := biz.RuntimePublication{ID: "ffffffff-1111-4222-8333-444444444444", LogicalKey: "main", ReceiptID: "cleanup-receipt", Upload: biz.UploadCompletion{RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, TaskID: "publish", PodUID: "cleanup-publish-uid", ContainerName: "main", CompletedAt: time.Now().UTC(), ObservedAt: time.Now().UTC()}, VerifiedAt: time.Now().UTC()}
	scope, version := a.Snapshot.PublicationScope, "cleanup-object-version"
	var files []cpup01.OutputFile
	for _, required := range a.Snapshot.OutputContract.RequiredFiles {
		file := cpup01.OutputFile{RelativePath: required.RelativePath, Role: required.Role, SizeBytes: 1, SHA256: strings.Repeat("2", 64), InTar: required.InTar}
		files = append(files, file)
		publication.Files = append(publication.Files, biz.PublishedRuntimeFile{ArtifactID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(file.RelativePath)).String(), File: file, Object: cpup01.FixedObjectRef{StorageConnectionID: scope.StorageConnectionID, Bucket: scope.Bucket, Key: strings.TrimSuffix(scope.ApprovedPrefix, "/") + "/" + a.ExecutionID + "/" + file.RelativePath, VersionID: &version, SizeBytes: file.SizeBytes, SHA256: file.SHA256}})
	}
	manifest, digest, err := cpup01.OutputManifestBytes(cpup01.AdmissionEnvelope(a), files)
	if err != nil {
		t.Fatal(err)
	}
	publication.Manifest = cpup01.FixedObjectRef{StorageConnectionID: scope.StorageConnectionID, Bucket: scope.Bucket, Key: strings.TrimSuffix(scope.ApprovedPrefix, "/") + "/" + a.ExecutionID + "/output-manifest.json", VersionID: &version, SizeBytes: int64(len(manifest)), SHA256: digest}
	if a.Snapshot.OutputContract.CreateTarBundle {
		bundle := publication.Manifest
		bundle.Key, bundle.SHA256 = strings.TrimSuffix(scope.ApprovedPrefix, "/")+"/"+a.ExecutionID+"/bundle.tar", strings.Repeat("3", 64)
		publication.Bundle = &bundle
	}
	startRole("collect")
	startRole("publish")
	if _, _, err := runtime.RecordPublication(ctx, authority, publication); err != nil {
		t.Fatal(err)
	}
	closing, _, err := runtime.RequestRuntimeClose(ctx, authority, "NATURAL_TERMINAL")
	if err != nil {
		t.Fatal(err)
	}
	evidence := biz.ManagedCloseEvidence{RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, ObservedAt: time.Now().UTC()}
	for _, step := range []string{"prepare", "train-wait", "collect", "publish"} {
		evidence.Resources = append(evidence.Resources, biz.RuntimeResource{APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: "cleanup-" + step, UID: "cleanup-" + step + "-uid", OwnerUID: authority.WorkflowUID, APIObjectPresent: true, Terminal: true, ExitCode: &zero})
	}
	if _, err := runtime.ConfirmRuntimeClosed(ctx, authority, closing.CloseGeneration, observation, evidence); err != nil {
		t.Fatal(err)
	}
	record, err := repository.GetOperationRecord(ctx, a.TenantID, a.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := biz.CleanupPlanFor(record)
	if err != nil || len(plan.Targets) != 3 {
		t.Fatal("closed exact three-controller plan unavailable", err)
	}
	return pool, repository, plan
}

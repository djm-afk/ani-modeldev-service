package lifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestRuntimeAuthorityUpgradePreservesFactsAndRejectsUnboundCompute(t *testing.T) {
	open, upgrade := postgres.PrepareRuntimeCloseUpgrade(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := open()
	snapshot := conformance.SnapshotV1()
	accepted := time.Now().UTC().Truncate(time.Microsecond)
	snapshot.DeadlineAt = accepted.Add(time.Hour)
	intent := cpup01.Intent{Name: "upgrade-close", Kind: "GENERAL_TRAINING", ImplementationID: snapshot.Implementation.ImplementationID, ComputePresetID: snapshot.ComputeOrigin.ComputePresetID, MLP: &cpup01.MLPParameters{}, DatasetVersionID: snapshot.Input.InputVersionID}
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	specHash := ""
	admission := biz.Admission{TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:user:42", OperationID: "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff", ExecutionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", Intent: intent, IntentHash: intentHash, Snapshot: snapshot, SpecHash: specHash, AcceptedAt: accepted}
	admission = admissionfixture.Bind(t, admission)
	snapshot = admission.Snapshot
	if _, err := execution.New(pool).Accept(ctx, admission); err != nil {
		t.Fatal(err)
	}
	dispatch, err := submission.New(pool).Reserve(ctx, biz.PipelineDispatchRequest{Admission: admission, Owner: biz.PipelineOwnerConfiguration{Reference: "runtime-fixture-owner", RevisionSHA256: strings.Repeat("a", 64), PipelineRoot: "s3://fixture-kfp-artifacts/managed-root"}})
	if err != nil {
		t.Fatal(err)
	}
	authority := biz.RunAuthorityCandidate{TenantID: admission.TenantID, ExecutionID: admission.ExecutionID, OperationID: admission.OperationID, SpecHash: admission.SpecHash, AttemptID: dispatch.Dispatch.AttemptID, PlanHash: dispatch.Dispatch.PlanHash, RunID: "55555555-6666-4777-8888-999999999999", NamespaceName: snapshot.Environment.NamespaceName, NamespaceUID: snapshot.Environment.NamespaceUID, WorkflowName: "upgrade-workflow", WorkflowUID: "cccccccc-dddd-4eee-8fff-111111111111"}
	if _, err := submission.New(pool).BindRunAuthority(ctx, authority); err != nil {
		t.Fatal(err)
	}
	workspace := biz.WorkspaceBinding{Mode: snapshot.Workspace.Mode, NamespaceName: snapshot.Environment.NamespaceName, NamespaceUID: snapshot.Environment.NamespaceUID, PVCName: "upgrade-workspace", PVCUID: "dddddddd-eeee-4fff-8111-222222222222", InputSubpath: snapshot.Workspace.InputSubpath, TrainingSubpath: snapshot.Workspace.TrainingSubpath, ReportsSubpath: snapshot.Workspace.ReportsSubpath, PublicationSubpath: snapshot.Workspace.PublicationSubpath, PreparedManifestSHA256: strings.Repeat("1", 64), PreparedManifestBytes: 256}
	startManagedRoleV21(t, lifecycle.New(pool), authority, "prepare")
	before, _, err := lifecycle.New(pool).RecordPrepared(ctx, authority, workspace)
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	upgrade()
	pool = open()
	after, err := lifecycle.New(pool).GetRuntime(ctx, admission.TenantID, admission.ExecutionID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed prior resource facts: %+v %v", after, err)
	}
	other := admission
	other.OperationID, other.ExecutionID = "bbbbbbbb-cccc-4ddd-8eee-111111111111", "aaaaaaaa-bbbb-4ccc-8ddd-111111111111"
	other = admissionfixture.Bind(t, other)
	if _, err := execution.New(pool).Accept(ctx, other); err != nil {
		t.Fatal(err)
	}
	// These malformed records cross the persistence boundary directly to prove
	// PostgreSQL, rather than a pre-storage branch, still rejects unbound compute.
	for _, withWorkspace := range []bool{false, true} {
		state := biz.ExecutionRuntime{}
		var reason *string
		var at *time.Time
		if withWorkspace {
			state.Workspace, state.CloseGeneration, state.CloseReason, state.CloseRequestedAt = &workspace, 1, "USER_STOP", accepted
			reason, at = &state.CloseReason, &state.CloseRequestedAt
		}
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(ctx, `INSERT INTO modeldev_execution_runtimes (tenant_id,execution_id,operation_id,spec_hash,facts,close_generation,close_reason,close_requested_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, other.TenantID, other.ExecutionID, other.OperationID, other.SpecHash, raw, state.CloseGeneration, reason, at)
		var rejected *pgconn.PgError
		if !errors.As(err, &rejected) || rejected.Code != "23503" {
			t.Fatalf("unbound compute did not fail FK: workspace=%v err=%v", withWorkspace, err)
		}
	}
	t.Log("RUNTIME_AUTHORITY_UPGRADE: populated resource facts preserved; unbound OPEN and fenced workspace records both rejected by PostgreSQL")
}

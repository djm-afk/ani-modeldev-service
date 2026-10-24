package lifecycle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestRuntimeAuthorityUpgradePreservesFactsAndRejectsUnboundCompute(t *testing.T) {
	open, upgrade := postgres.PrepareRuntimeCloseUpgrade(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := open()
	assertRuntimeAuthorityUpgradeSchema(t, ctx, pool, false)
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
	seedRuntimeAuthorityUpgradeFacts(t, ctx, pool, admission, authority, workspace)
	before, err := lifecycle.New(pool).GetRuntime(ctx, admission.TenantID, admission.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Workspace, &workspace) || len(before.StepTimings) != 1 || before.StepTimings[0].Binding.Role != "prepare" {
		t.Fatal("historical writer did not retain the original prepare/workspace facts")
	}
	oldRows := snapshotRuntimeAuthorityUpgradeRows(t, ctx, pool, admission)
	pool.Close()
	upgrade()
	pool = open()
	assertRuntimeAuthorityUpgradeSchema(t, ctx, pool, true)
	newRows := snapshotRuntimeAuthorityUpgradeRows(t, ctx, pool, admission)
	for table, original := range oldRows {
		if !bytes.Equal(original, newRows[table]) {
			t.Fatalf("migration changed original stored row/JSON bytes: %s", table)
		}
	}
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

// This fixed writer is only a pre-0015 migration fixture, never a production
// compatibility path. Its SaveRuntimeFacts SQL and json.Marshal(state) encoding
// come from released 44188466290fbd0965b21816cdfa39403e6a6b7b. The current writer
// requires 0019's independently anchored GPU scope; it cannot seed old tables.
const runtimeAuthorityUpgradeOldSave = `-- name: SaveRuntimeFacts :execrows
INSERT INTO modeldev_execution_runtimes (
    tenant_id, execution_id, operation_id, spec_hash, facts,
    training_name, training_request_sha256, training_uid, publication_id,
    close_generation, close_reason, close_requested_at, closed_at
)
SELECT i.tenant_id, i.execution_id, i.operation_id, i.spec_hash, $1::jsonb,
    $2::text, $3::text,
    $4::text, $5::uuid,
    $6::numeric, $7::text,
    $8::timestamptz, $9::timestamptz
FROM modeldev_execution_identities AS i
WHERE i.tenant_id = $10::uuid AND i.execution_id = $11::uuid
  AND (NOT $12::boolean OR
       (i.close_generation = 0 AND clock_timestamp() < $13::timestamptz))
ON CONFLICT (tenant_id, execution_id) DO UPDATE SET
    facts = EXCLUDED.facts,
    training_name = EXCLUDED.training_name,
    training_request_sha256 = EXCLUDED.training_request_sha256,
    training_uid = EXCLUDED.training_uid,
    publication_id = EXCLUDED.publication_id,
    close_generation = EXCLUDED.close_generation,
    close_reason = EXCLUDED.close_reason,
    close_requested_at = EXCLUDED.close_requested_at,
    closed_at = EXCLUDED.closed_at
`

func seedRuntimeAuthorityUpgradeFacts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, admission biz.Admission, authority biz.RunAuthorityCandidate, workspace biz.WorkspaceBinding) {
	t.Helper()
	state := biz.ExecutionRuntime{}
	// Retain the original two commits: prepare window, then prepared workspace.
	for _, prepared := range []bool{false, true} {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		var now time.Time
		var generation string
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp(), close_generation::text
FROM modeldev_execution_identities WHERE tenant_id=$1::uuid AND execution_id=$2::uuid FOR UPDATE`, admission.TenantID, admission.ExecutionID).Scan(&now, &generation); err != nil || generation != "0" {
			t.Fatalf("historical fixture must retain the original open identity: %v", err)
		}
		if !prepared {
			deadline, err := admission.Snapshot.Timing.DeadlineFor(now)
			if err != nil || !now.Before(deadline) {
				t.Fatalf("historical prepare window: %v", err)
			}
			binding := biz.FinetuningStepBinding{Role: "prepare", SpecHash: authority.SpecHash, RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, TaskID: "prepare-fixture", PodName: "prepare-fixture", PodUID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("prepare")).String()}
			state.StepTimings = []biz.ManagedStepTiming{{Binding: binding, AdmittedAt: now.UTC(), DeadlineAt: deadline}}
		} else {
			state.Workspace = &workspace
		}
		var revision string
		if err := tx.QueryRow(ctx, `UPDATE modeldev_execution_identities SET owner_revision=owner_revision+1
WHERE tenant_id=$1::uuid AND execution_id=$2::uuid AND owner_revision < 18446744073709551615
RETURNING owner_revision::text`, admission.TenantID, admission.ExecutionID).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		state.OwnerRevision, err = strconv.ParseUint(revision, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		result, err := tx.Exec(ctx, runtimeAuthorityUpgradeOldSave, raw, nil, nil, nil, nil, "0", nil, nil, nil, admission.TenantID, admission.ExecutionID, true, state.StepTimings[0].DeadlineAt)
		if err != nil || result.RowsAffected() != 1 {
			t.Fatalf("historical runtime writer failed: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func assertRuntimeAuthorityUpgradeSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool, upgraded bool) {
	t.Helper()
	var authority, sealed, scope bool
	if err := pool.QueryRow(ctx, `SELECT
EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='modeldev_execution_runtimes' AND column_name='authority_execution_id'),
EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='modeldev_execution_runtimes' AND column_name='gpu_release_sealed_at'),
EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='modeldev_execution_runtimes' AND column_name='gpu_release_scope_sha256')`).Scan(&authority, &sealed, &scope); err != nil || authority != upgraded || sealed || scope {
		t.Fatalf("expected only historical 0014-to-0015 schema, never latest: authority=%v sealed=%v scope=%v err=%v", authority, sealed, scope, err)
	}
}

func snapshotRuntimeAuthorityUpgradeRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, admission biz.Admission) map[string][]byte {
	t.Helper()
	rows := map[string][]byte{}
	for name, query := range map[string]string{
		"identity":  `SELECT row_to_json(original)::text FROM modeldev_execution_identities AS original WHERE tenant_id=$1::uuid AND execution_id=$2::uuid`,
		"admission": `SELECT row_to_json(original)::text FROM modeldev_executions AS original WHERE tenant_id=$1::uuid AND execution_id=$2::uuid`,
		"authority": `SELECT row_to_json(original)::text FROM modeldev_run_authorities AS original WHERE tenant_id=$1::uuid AND execution_id=$2::uuid`,
		"runtime": `SELECT row_to_json(original)::text FROM (SELECT tenant_id,execution_id,operation_id,spec_hash,facts,
training_name,training_request_sha256,training_uid,publication_id,close_generation,close_reason,close_requested_at,closed_at
FROM modeldev_execution_runtimes WHERE tenant_id=$1::uuid AND execution_id=$2::uuid) AS original`,
		"facts": `SELECT facts::text FROM modeldev_execution_runtimes WHERE tenant_id=$1::uuid AND execution_id=$2::uuid`,
	} {
		var raw []byte
		if err := pool.QueryRow(ctx, query, admission.TenantID, admission.ExecutionID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		rows[name] = append([]byte(nil), raw...)
	}
	return rows
}

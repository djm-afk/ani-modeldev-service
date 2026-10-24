//go:build revisionupgrade

package submission_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

// Only the fixed historical process produces this private test protocol. Its
// old receipts are expectations, not inputs to a replacement repository.
type oldRevisionSeedManifest struct {
	WriterBase string                `json:"writer_base"`
	Cases      []oldRevisionSeedCase `json:"cases"`
}

// Raw historical payloads stay private to this test. They are never decoded
// into an active v1 business contract or passed to a replacement writer.
type oldRevisionSeedCase struct {
	Name      string                  `json:"name"`
	Request   json.RawMessage         `json:"request"`
	Closes    []biz.CloseRecord       `json:"closes"`
	Permit    *biz.PipelineSendPermit `json:"permit,omitempty"`
	Execution json.RawMessage         `json:"execution,omitempty"`
	Dispatch  json.RawMessage         `json:"dispatch,omitempty"`
	admission oldRevisionAdmission
	dispatch  *oldRevisionDispatch
}

type oldRevisionAdmission struct {
	TenantID    string
	Actor       string
	OperationID string
	ExecutionID string
	Intent      json.RawMessage
	IntentHash  string
	Snapshot    json.RawMessage
	SpecHash    string
	AcceptedAt  time.Time
}

type oldRevisionDispatch struct {
	OwnerRevision uint64
	AttemptID     string
	Plan          json.RawMessage
	PlanHash      string
	State         biz.PipelineDispatchState
	ReservedAt    time.Time
	UncertainAt   *time.Time
	NotSentAt     *time.Time
	ConfirmedRuns []biz.PipelineConfirmedRun
}

func TestOwnerRevisionUpgradeFromOldWriter(t *testing.T) {
	fixture := postgres.PrepareRevisionUpgrade(t)
	old := decodeOldRevisionSeeds(t, fixture.SeedOldWriter(t))
	beforePool := fixture.OpenRuntimePool(t)
	before := snapshotRevisionOldColumns(t, beforePool)
	beforePool.Close()
	for index, count := range []int{5, 4, 3, 2, 2} {
		if len(before[index].rows) != count {
			t.Fatal("REVISION_UPGRADE_PREFLIGHT: independent old rows do not contain all five cases; behavior NOT_RUN")
		}
	}
	t.Log("REVISION_UPGRADE_PREFLIGHT PASS: exited historical writer, five qualified cases, independent complete old-column byte snapshot")
	fixture.ApplyOwnerRevision(t)
	afterPool := fixture.OpenRuntimePool(t)
	after := snapshotRevisionOldColumns(t, afterPool)
	assertOldRevisionBaseline(t, afterPool)
	afterPool.Close()
	if !reflect.DeepEqual(after, before) {
		t.Fatal("REVISION_UPGRADE_BEHAVIOR: migration changed old column identity, bytes, timestamps, generations or related rows")
	}
	for _, seed := range old.Cases {
		t.Run(seed.Name, func(t *testing.T) {
			pool := fixture.OpenRuntimePool(t)
			defer pool.Close()
			writer, submissions := execution.New(pool), submission.New(pool)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			original := seed.admission
			got, err := writer.Get(ctx, original.TenantID, original.ExecutionID)
			if seed.Name == "close-only" {
				if !errors.Is(err, biz.ErrExecutionNotFound) || !reflect.DeepEqual(got, biz.Execution{}) {
					t.Fatal("REVISION_UPGRADE_BEHAVIOR: migration invented a close-only admission")
				}
			} else {
				// Preserve the exact old bytes while the sole v2.1 codec refuses to
				// turn them into a runnable current admission or a successful receipt.
				var intent, snapshot []byte
				var operation, actor, intentHash, specHash string
				var acceptedAt time.Time
				if queryErr := pool.QueryRow(ctx, `SELECT operation_id::text, actor, intent_canonical,
       intent_hash, snapshot_canonical, spec_hash, accepted_at FROM modeldev_executions
       WHERE tenant_id=$1::uuid AND execution_id=$2::uuid`, original.TenantID, original.ExecutionID).
					Scan(&operation, &actor, &intent, &intentHash, &snapshot, &specHash, &acceptedAt); queryErr != nil ||
					operation != original.OperationID || actor != original.Actor || intentHash != original.IntentHash ||
					specHash != original.SpecHash || !acceptedAt.Equal(original.AcceptedAt) ||
					oldBytesSHA256(intent) != intentHash || oldBytesSHA256(snapshot) != specHash {
					t.Fatal("REVISION_UPGRADE_BEHAVIOR: independent stored legacy admission differs from the real old writer")
				}
				decodedIntent, intentErr := cpup01.DecodeCanonicalIntent(intent)
				decodedSnapshot, snapshotErr := cpup01.DecodeCanonicalSnapshot(snapshot)
				if intentErr == nil || snapshotErr == nil || !reflect.DeepEqual(decodedIntent, cpup01.Intent{}) ||
					!reflect.DeepEqual(decodedSnapshot, cpup01.Snapshot{}) || !errors.Is(err, biz.ErrPersistence) ||
					!reflect.DeepEqual(got, biz.Execution{}) {
					t.Fatal("REVISION_UPGRADE_BEHAVIOR: sole current codec adopted historical v1 admission or returned a receipt")
				}
			}
			dispatch, dispatchErr := submissions.Get(ctx, original.TenantID, original.ExecutionID)
			if seed.dispatch == nil {
				if !errors.Is(dispatchErr, biz.ErrExecutionNotFound) || !reflect.DeepEqual(dispatch, biz.PipelineDispatch{}) {
					t.Fatal("REVISION_UPGRADE_BEHAVIOR: current reader invented a legacy dispatch")
				}
			} else {
				if !errors.Is(dispatchErr, biz.ErrPersistence) || !reflect.DeepEqual(dispatch, biz.PipelineDispatch{}) {
					t.Fatal("REVISION_UPGRADE_BEHAVIOR: current reader adopted a historical v1 dispatch")
				}
				observedAt := seed.dispatch.ReservedAt.Add(3 * time.Microsecond)
				uncertain, uncertainErr := submissions.MarkSubmissionUncertain(ctx, *seed.Permit, observedAt)
				notSent, notSentErr := submissions.MarkSubmissionNotSent(ctx, *seed.Permit, observedAt)
				confirmed, confirmedErr := submissions.RecordSubmissionConfirmed(ctx, *seed.Permit,
					confirmedObservation("10000000-0000-4000-8000-000000000003"), observedAt)
				if !errors.Is(uncertainErr, biz.ErrPersistence) || !reflect.DeepEqual(uncertain, biz.PipelineDispatch{}) ||
					!errors.Is(notSentErr, biz.ErrPersistence) || !reflect.DeepEqual(notSent, biz.PipelineDispatch{}) ||
					!errors.Is(confirmedErr, biz.ErrPersistence) || !reflect.DeepEqual(confirmed, biz.PipelineConfirmationReceipt{}) {
					t.Fatal("REVISION_UPGRADE_BEHAVIOR: historical permit caused current adoption or a successful observation receipt")
				}
			}
		})
	}
	// A separate connection observes the unchanged original fences, payloads and
	// baseline after every rejected read/observation transaction has ended.
	reconnected := fixture.OpenRuntimePool(t)
	defer reconnected.Close()
	if !reflect.DeepEqual(snapshotRevisionOldColumns(t, reconnected), before) {
		t.Fatal("REVISION_UPGRADE_BEHAVIOR: current rejection mutated old columns, close fence or observations")
	}
	assertOldRevisionBaseline(t, reconnected)
	t.Log("REVISION_UPGRADE_BEHAVIOR PASS: old bytes/fences retained across reconnect, baseline=1; sole v2.1 codec rejected legacy takeover and returned zero receipts")
}

func assertOldRevisionBaseline(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var count, baselines int
	if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE owner_revision=1)
   FROM modeldev_execution_identities`).Scan(&count, &baselines); err != nil || count != 5 || baselines != 5 {
		t.Fatal("REVISION_UPGRADE_BEHAVIOR: original five identities do not retain owner revision baseline 1")
	}
}

func oldBytesSHA256(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// These are copies of actual PostgreSQL wire values, including original bytea
// bytes. No JSON round trip, generated model, or repository decoder can hide a
// migration change. Metadata captures the exact old column order/types/formats.
type revisionOldTable struct {
	fields []pgconn.FieldDescription
	rows   [][][]byte
}

func snapshotRevisionOldColumns(t *testing.T, pool *pgxpool.Pool) []revisionOldTable {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transaction, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal("REVISION_UPGRADE_READ: independent raw snapshot transaction unavailable")
	}
	defer func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		_ = transaction.Rollback(cleanup)
	}()
	if _, err := transaction.Exec(ctx, "SET LOCAL TIME ZONE 'UTC'"); err != nil {
		t.Fatal("REVISION_UPGRADE_READ: independent timestamp representation unavailable")
	}
	var tables []revisionOldTable
	for _, query := range []string{
		`SELECT tenant_id, execution_id, operation_id, spec_hash, close_generation FROM modeldev_execution_identities ORDER BY tenant_id, execution_id`,
		`SELECT tenant_id, execution_id, operation_id, actor, intent_canonical, intent_hash, snapshot_canonical, spec_hash, accepted_at FROM modeldev_executions ORDER BY tenant_id, execution_id`,
		`SELECT tenant_id, execution_id, operation_id, spec_hash, source_kind, source_generation, owner_generation, reason, requested_at, requested_actor, close_state FROM modeldev_close_intents ORDER BY tenant_id, execution_id, source_kind, source_generation`,
		`SELECT tenant_id, execution_id, operation_id, spec_hash, attempt_id, plan_canonical, plan_hash, state, reserved_at, uncertain_at, not_sent_at FROM modeldev_pipeline_dispatches ORDER BY tenant_id, execution_id`,
		`SELECT tenant_id, execution_id, attempt_id, plan_hash, run_id, first_observed_at FROM modeldev_pipeline_confirmed_runs ORDER BY tenant_id, execution_id, attempt_id, run_id`,
	} {
		rows, err := transaction.Query(ctx, query)
		if err != nil {
			t.Fatal("REVISION_UPGRADE_READ: complete old-column query failed")
		}
		table := revisionOldTable{fields: append([]pgconn.FieldDescription(nil), rows.FieldDescriptions()...)}
		for rows.Next() {
			var row [][]byte
			for _, value := range rows.RawValues() {
				row = append(row, bytes.Clone(value))
			}
			table.rows = append(table.rows, row)
		}
		rows.Close()
		if rows.Err() != nil {
			t.Fatal("REVISION_UPGRADE_READ: complete old-column rows unavailable")
		}
		tables = append(tables, table)
	}
	if err := transaction.Commit(ctx); err != nil {
		t.Fatal("REVISION_UPGRADE_READ: independent raw snapshot did not complete")
	}
	return tables
}

func decodeOldRevisionSeeds(t *testing.T, raw []byte) oldRevisionSeedManifest {
	t.Helper()
	var manifest oldRevisionSeedManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(new(any)) != io.EOF || manifest.WriterBase != "bbf0ecbda123229a588c29445d132d4ee5c370a6" || len(manifest.Cases) != 5 {
		t.Fatal("REVISION_UPGRADE_PREFLIGHT: historical manifest is not the fixed complete seed; behavior NOT_RUN")
	}
	names := []string{"admission-only", "close-only", "multiple-closes", "uncertain-and-not-sent", "confirmed-multiple-runs"}
	identities := make(map[string]bool)
	for index := range manifest.Cases {
		seed := &manifest.Cases[index]
		var request struct {
			Admission oldRevisionAdmission
			Owner     json.RawMessage
		}
		requestDecoder := json.NewDecoder(bytes.NewReader(seed.Request))
		requestDecoder.DisallowUnknownFields()
		if requestDecoder.Decode(&request) != nil || requestDecoder.Decode(new(any)) != io.EOF {
			t.Fatal("REVISION_UPGRADE_PREFLIGHT: private original envelope unreadable; behavior NOT_RUN")
		}
		seed.admission = request.Admission
		original := seed.admission
		if seed.Name != names[index] || identities[original.ExecutionID] || identities[original.OperationID] ||
			len(original.TenantID) != 36 || len(original.ExecutionID) != 36 || len(original.OperationID) != 36 ||
			original.AcceptedAt.IsZero() || len(original.IntentHash) != 64 || len(original.SpecHash) != 64 ||
			!json.Valid(original.Intent) || !json.Valid(original.Snapshot) || !json.Valid(request.Owner) {
			t.Fatal("REVISION_UPGRADE_PREFLIGHT: old case order/identity incomplete; behavior NOT_RUN")
		}
		identities[original.ExecutionID] = true
		identities[original.OperationID] = true
		wantCloses := 0
		if seed.Name == "close-only" {
			wantCloses = 1
		} else if seed.Name == "multiple-closes" {
			wantCloses = 2
		}
		if len(seed.Closes) != wantCloses || (len(seed.Execution) == 0) != (seed.Name == "close-only") {
			t.Fatal("REVISION_UPGRADE_PREFLIGHT: old close/admission cases incomplete; behavior NOT_RUN")
		}
		if len(seed.Execution) != 0 {
			var admitted map[string]json.RawMessage
			var admittedIdentity oldRevisionAdmission
			var oldRevision uint64
			if json.Unmarshal(seed.Execution, &admitted) != nil || json.Unmarshal(admitted["OwnerRevision"], &oldRevision) != nil || oldRevision != 0 ||
				json.Unmarshal(seed.Execution, &admittedIdentity) != nil ||
				admittedIdentity.TenantID != original.TenantID || admittedIdentity.Actor != original.Actor ||
				admittedIdentity.OperationID != original.OperationID || admittedIdentity.ExecutionID != original.ExecutionID ||
				admittedIdentity.IntentHash != original.IntentHash || admittedIdentity.SpecHash != original.SpecHash ||
				!admittedIdentity.AcceptedAt.Equal(original.AcceptedAt) {
				t.Fatal("REVISION_UPGRADE_PREFLIGHT: seed did not retain the original pre-revision admission; behavior NOT_RUN")
			}
		}
		for closeIndex, close := range seed.Closes {
			if close.Validate() != nil || close.TenantID != original.TenantID || close.ExecutionID != original.ExecutionID ||
				close.OperationID != original.OperationID || close.SpecHash != original.SpecHash || close.RequestedActor != original.Actor ||
				close.SourceGeneration != uint64(41+closeIndex) ||
				!close.RequestedAt.Equal(original.AcceptedAt.Add(time.Duration(closeIndex)*time.Microsecond)) ||
				close.Generation != uint64(closeIndex+1) || close.State != biz.CloseStateClosing {
				t.Fatal("REVISION_UPGRADE_PREFLIGHT: old source/owner close originals invalid; behavior NOT_RUN")
			}
		}
		wantDispatch := index >= 3
		if (len(seed.Dispatch) != 0) != wantDispatch || (seed.Permit != nil) != wantDispatch {
			t.Fatal("REVISION_UPGRADE_PREFLIGHT: old dispatch cases incomplete; behavior NOT_RUN")
		}
		if !wantDispatch {
			continue
		}
		var dispatch oldRevisionDispatch
		dispatchDecoder := json.NewDecoder(bytes.NewReader(seed.Dispatch))
		dispatchDecoder.DisallowUnknownFields()
		if dispatchDecoder.Decode(&dispatch) != nil || dispatchDecoder.Decode(new(any)) != io.EOF ||
			dispatch.OwnerRevision != 0 || dispatch.ReservedAt.IsZero() || !json.Valid(dispatch.Plan) ||
			seed.Permit.TenantID != original.TenantID || seed.Permit.ExecutionID != original.ExecutionID ||
			seed.Permit.AttemptID != dispatch.AttemptID || seed.Permit.PlanHash != dispatch.PlanHash {
			t.Fatal("REVISION_UPGRADE_PREFLIGHT: old reservation/permit binding invalid; behavior NOT_RUN")
		}
		seed.dispatch = &dispatch
		firstTime := dispatch.ReservedAt.Add(time.Microsecond)
		secondTime := dispatch.ReservedAt.Add(2 * time.Microsecond)
		if seed.Name == "uncertain-and-not-sent" {
			if dispatch.State != biz.PipelineDispatchUncertain || dispatch.UncertainAt == nil ||
				!dispatch.UncertainAt.Equal(firstTime) || dispatch.NotSentAt == nil ||
				!dispatch.NotSentAt.Equal(secondTime) || len(dispatch.ConfirmedRuns) != 0 {
				t.Fatal("REVISION_UPGRADE_PREFLIGHT: old strong state/first observations incomplete; behavior NOT_RUN")
			}
		} else {
			wantRuns := []biz.PipelineConfirmedRun{{RunID: "10000000-0000-4000-8000-000000000001", FirstObservedAt: firstTime},
				{RunID: "10000000-0000-4000-8000-000000000002", FirstObservedAt: secondTime}}
			if dispatch.State != biz.PipelineDispatchConfirmed || dispatch.UncertainAt != nil || dispatch.NotSentAt != nil || !reflect.DeepEqual(dispatch.ConfirmedRuns, wantRuns) {
				t.Fatal("REVISION_UPGRADE_PREFLIGHT: old conflicting Run originals incomplete; behavior NOT_RUN")
			}
		}
	}
	return manifest
}

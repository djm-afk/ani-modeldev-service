package execution_test

import (
	"context"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestAcceptPersistsCompleteAdmissionAcrossNewConnections(t *testing.T) {
	openRuntimePool := preparePostgreSQL(t)
	writerPool := openRuntimePool()
	command := validAdmission(t)
	expectedIntent, _, err := cpup01.CanonicalIntent(command.Intent)
	if err != nil {
		t.Fatalf("invalid intent fixture: %v", err)
	}
	expectedSnapshot, err := command.Snapshot.Canonical()
	if err != nil {
		t.Fatalf("invalid snapshot fixture: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := execution.New(writerPool).Accept(ctx, command); err != nil {
		t.Fatalf("Accept rejected valid trusted admission: %v", err)
	}
	writerPool.Close()

	// A fresh repository and pool must recover committed data, not a process map
	// or an open connection's uncommitted transaction.
	readerPool := openRuntimePool()
	readContext, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	got, err := execution.New(readerPool).Get(readContext, command.TenantID, command.ExecutionID)
	if err != nil {
		t.Fatalf("Get after reconnect: %v", err)
	}
	if got.TenantID != command.TenantID || got.Actor != command.Actor || got.OperationID != command.OperationID || got.ExecutionID != command.ExecutionID {
		t.Fatalf("persisted admission identity changed: got tenant=%q actor=%q operation=%q execution=%q", got.TenantID, got.Actor, got.OperationID, got.ExecutionID)
	}
	if got.IntentHash != command.IntentHash || got.SpecHash != command.SpecHash || !got.AcceptedAt.Equal(command.AcceptedAt) {
		t.Fatalf("persisted admission hashes or acceptance time changed")
	}
	actualIntent, _, err := cpup01.CanonicalIntent(got.Intent)
	if err != nil || string(actualIntent) != string(expectedIntent) {
		t.Fatalf("persisted intent is incomplete or changed: %v", err)
	}
	actualSnapshot, err := got.Snapshot.Canonical()
	if err != nil || string(actualSnapshot) != string(expectedSnapshot) {
		t.Fatalf("persisted frozen snapshot is incomplete or changed: %v", err)
	}
}

func TestAcceptDuplicateAfterReconnectReturnsOriginalAdmission(t *testing.T) {
	openRuntimePool := preparePostgreSQL(t)
	writerPool := openRuntimePool()
	command := validAdmission(t)
	// Governance owns actor identity. Its authenticated user IDs need not be
	// UUIDs, and the receiver must retain this immutable audit identity.
	command.Actor = "governance:user:42"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := execution.New(writerPool).Accept(ctx, command); err != nil {
		t.Fatalf("initial Accept rejected valid trusted admission: %v", err)
	}
	writerPool.Close()

	retryPool := openRuntimePool()
	retryRepository := execution.New(retryPool)
	retryContext, retryCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer retryCancel()
	replayed, err := retryRepository.Accept(retryContext, command)
	if err != nil {
		t.Fatalf("duplicate Accept after reconnect must return the original admission: %v", err)
	}
	assertOriginalAdmission(t, replayed.Execution, command)
	persisted, err := retryRepository.Get(retryContext, command.TenantID, command.ExecutionID)
	if err != nil {
		t.Fatalf("Get after duplicate Accept: %v", err)
	}
	assertOriginalAdmission(t, persisted, command)
}

func assertOriginalAdmission(t *testing.T, got biz.Execution, want biz.Admission) {
	t.Helper()
	if got.TenantID != want.TenantID || got.Actor != want.Actor || got.OperationID != want.OperationID || got.ExecutionID != want.ExecutionID {
		t.Fatal("duplicate delivery changed the original admission identity")
	}
	if got.IntentHash != want.IntentHash || got.SpecHash != want.SpecHash || !got.AcceptedAt.Equal(want.AcceptedAt) {
		t.Fatal("duplicate delivery changed the original hashes or accepted_at")
	}
	expectedIntent, _, err := cpup01.CanonicalIntent(want.Intent)
	if err != nil {
		t.Fatalf("invalid expected intent: %v", err)
	}
	actualIntent, _, err := cpup01.CanonicalIntent(got.Intent)
	if err != nil || string(actualIntent) != string(expectedIntent) {
		t.Fatalf("duplicate delivery changed the complete original intent: %v", err)
	}
	expectedSnapshot, err := want.Snapshot.Canonical()
	if err != nil {
		t.Fatalf("invalid expected snapshot: %v", err)
	}
	actualSnapshot, err := got.Snapshot.Canonical()
	if err != nil || string(actualSnapshot) != string(expectedSnapshot) {
		t.Fatalf("duplicate delivery changed the complete original snapshot: %v", err)
	}
}

func validAdmission(t *testing.T) biz.Admission {
	t.Helper()
	snapshot := conformance.SnapshotV1()
	intent := cpup01.Intent{
		Name:             "durable-cpu-execution",
		Kind:             "GENERAL_TRAINING",
		ImplementationID: snapshot.Implementation.ImplementationID, ComputePresetID: snapshot.ComputeOrigin.ComputePresetID, MLP: &cpup01.MLPParameters{},
		DatasetVersionID: snapshot.Input.InputVersionID,
	}
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatalf("intent fixture: %v", err)
	}
	specHash := ""
	return admissionfixture.Bind(t, biz.Admission{
		TenantID:    "11111111-2222-4333-8444-555555555555",
		Actor:       "governance:user:42",
		OperationID: "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff",
		ExecutionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		Intent:      intent,
		IntentHash:  intentHash,
		Snapshot:    snapshot,
		SpecHash:    specHash,
		AcceptedAt:  snapshot.DeadlineAt.Add(-time.Hour).UTC(),
	})
}

func preparePostgreSQL(t *testing.T) func() *pgxpool.Pool {
	t.Helper()
	return postgres.Prepare(t)
}

// Package submission owns the execution's durable pipeline submission facts.
package submission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	submissionsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission/sqlc"
)

type Repository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Reserve serializes first creation with Admission and close on the existing
// identity row. No network operation or current-default lookup occurs here.
func (repository *Repository) Reserve(ctx context.Context, request biz.PipelineDispatchRequest) (biz.PipelineDispatchReservation, error) {
	plan, err := request.Freeze()
	if err != nil {
		return biz.PipelineDispatchReservation{}, err
	}
	canonical, err := plan.Canonical()
	if err != nil {
		return biz.PipelineDispatchReservation{}, err
	}
	planHash, err := plan.Digest()
	if err != nil {
		return biz.PipelineDispatchReservation{}, err
	}
	tenantID, err := databaseID(plan.TenantID)
	if err != nil {
		return biz.PipelineDispatchReservation{}, err
	}
	executionID, err := databaseID(plan.ExecutionID)
	if err != nil {
		return biz.PipelineDispatchReservation{}, err
	}
	operationID, err := databaseID(plan.OperationID)
	if err != nil {
		return biz.PipelineDispatchReservation{}, err
	}
	if repository == nil || repository.pool == nil {
		return biz.PipelineDispatchReservation{}, biz.ErrPersistence
	}
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return biz.PipelineDispatchReservation{}, biz.ErrPersistence
	}
	defer rollback(transaction)
	queries := submissionsql.New(transaction)
	identity, err := queries.LockExecutionIdentity(ctx, submissionsql.LockExecutionIdentityParams{TenantID: tenantID, ExecutionID: executionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.PipelineDispatchReservation{}, biz.ErrExecutionNotFound
	}
	if err != nil {
		return biz.PipelineDispatchReservation{}, biz.ErrPersistence
	}
	if identity.TenantID != tenantID || identity.ExecutionID != executionID || identity.OperationID != operationID || identity.SpecHash != plan.SpecHash {
		return biz.PipelineDispatchReservation{}, biz.ErrAdmissionConflict
	}
	admission, err := queries.GetAdmission(ctx, submissionsql.GetAdmissionParams{TenantID: tenantID, ExecutionID: executionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.PipelineDispatchReservation{}, biz.ErrExecutionNotFound
	}
	if err != nil {
		return biz.PipelineDispatchReservation{}, biz.ErrPersistence
	}
	intent, snapshot, err := request.Admission.CanonicalPayloads()
	if err != nil {
		return biz.PipelineDispatchReservation{}, err
	}
	if admission.TenantID != tenantID || admission.ExecutionID != executionID || admission.OperationID != operationID ||
		admission.Actor != request.Admission.Actor || admission.IntentHash != request.Admission.IntentHash || admission.SpecHash != plan.SpecHash ||
		!bytes.Equal(admission.IntentCanonical, intent) || !bytes.Equal(admission.SnapshotCanonical, snapshot) ||
		!admission.AcceptedAt.Valid || admission.AcceptedAt.InfinityModifier != pgtype.Finite || !admission.AcceptedAt.Time.Equal(request.Admission.AcceptedAt) {
		return biz.PipelineDispatchReservation{}, biz.ErrAdmissionConflict
	}
	row, err := queries.GetPipelineDispatch(ctx, submissionsql.GetPipelineDispatchParams{TenantID: tenantID, ExecutionID: executionID})
	if err == nil {
		dispatch, err := readDispatch(ctx, queries, row, admission)
		if err != nil {
			return biz.PipelineDispatchReservation{}, err
		}
		if !bytes.Equal(row.PlanCanonical, canonical) || row.PlanHash != planHash {
			return biz.PipelineDispatchReservation{}, biz.ErrAdmissionConflict
		}
		// Close/deadline cannot erase a prior fact. Even a successful replay
		// transaction never reconstructs the original sending permission.
		if err := transaction.Commit(ctx); err != nil {
			return biz.PipelineDispatchReservation{}, biz.ErrPersistence
		}
		return biz.PipelineDispatchReservation{Dispatch: dispatch}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return biz.PipelineDispatchReservation{}, biz.ErrPersistence
	}
	if !identity.CreationOpen {
		return biz.PipelineDispatchReservation{}, biz.ErrPipelineDispatchBlocked
	}
	// A committed Admission already has a positive aggregate version. Zero is
	// reserved for an identity's uncommitted first business fact, never this path.
	if _, err := positiveOwnerRevision(identity.OwnerRevision); err != nil {
		return biz.PipelineDispatchReservation{}, err
	}
	attemptID, err := uuid.NewRandom()
	if err != nil {
		return biz.PipelineDispatchReservation{}, biz.ErrPersistence
	}
	row, err = queries.InsertPipelineDispatch(ctx, submissionsql.InsertPipelineDispatchParams{
		TenantID: tenantID, ExecutionID: executionID, OperationID: operationID, SpecHash: plan.SpecHash,
		AttemptID: pgtype.UUID{Bytes: attemptID, Valid: true}, PlanCanonical: canonical, PlanHash: planHash,
		DeadlineAt: pgtype.Timestamptz{Time: plan.DeadlineAt.UTC(), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.PipelineDispatchReservation{}, biz.ErrPipelineDispatchBlocked
	}
	if err != nil {
		return biz.PipelineDispatchReservation{}, biz.ErrPersistence
	}
	dispatch, err := dispatchFromRow(row, admission, nil)
	if err != nil {
		return biz.PipelineDispatchReservation{}, err
	}
	dispatch.OwnerRevision, err = advanceOwnerRevision(ctx, queries, tenantID, executionID)
	if err != nil {
		return biz.PipelineDispatchReservation{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		// An unknown commit outcome must not authorize a send. If it committed,
		// any subsequent Reserve observes the original row without a permit.
		return biz.PipelineDispatchReservation{}, biz.ErrPersistence
	}
	return biz.PipelineDispatchReservation{
		Dispatch:   dispatch,
		SendPermit: &biz.PipelineSendPermit{TenantID: plan.TenantID, ExecutionID: plan.ExecutionID, AttemptID: dispatch.AttemptID, PlanHash: planHash},
	}, nil
}

func (repository *Repository) Get(ctx context.Context, tenant, execution string) (biz.PipelineDispatch, error) {
	tenantID, err := databaseID(tenant)
	if err != nil {
		return biz.PipelineDispatch{}, err
	}
	executionID, err := databaseID(execution)
	if err != nil {
		return biz.PipelineDispatch{}, err
	}
	if repository == nil || repository.pool == nil {
		return biz.PipelineDispatch{}, biz.ErrPersistence
	}
	// State, immutable handles and aggregate revision must share one snapshot.
	// Separate ReadCommitted statements could attach a new version to old facts.
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return biz.PipelineDispatch{}, biz.ErrPersistence
	}
	defer rollback(transaction)
	dispatch, err := readInTransaction(ctx, transaction, tenantID, executionID)
	if err != nil {
		return biz.PipelineDispatch{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return biz.PipelineDispatch{}, biz.ErrPersistence
	}
	return dispatch, nil
}

var _ biz.PipelineDispatchRepository = (*Repository)(nil)

func databaseID(value string) (pgtype.UUID, error) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return pgtype.UUID{}, biz.ErrInvalidAdmission
	}
	var id pgtype.UUID
	if id.Scan(value) != nil || !id.Valid || id.Bytes == [16]byte{} {
		return pgtype.UUID{}, biz.ErrInvalidAdmission
	}
	return id, nil
}

func rollback(transaction pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = transaction.Rollback(ctx)
}

// The caller supplies either a locked writer transaction or a read-only
// RepeatableRead transaction, so revision, parent and child facts cannot be torn.
func readDispatch(ctx context.Context, queries *submissionsql.Queries, row submissionsql.ModeldevPipelineDispatch, original submissionsql.ModeldevExecution) (biz.PipelineDispatch, error) {
	runs, err := queries.ListConfirmedPipelineRuns(ctx, submissionsql.ListConfirmedPipelineRunsParams{
		TenantID: row.TenantID, ExecutionID: row.ExecutionID, AttemptID: row.AttemptID, PlanHash: row.PlanHash,
	})
	if err != nil {
		return biz.PipelineDispatch{}, biz.ErrPersistence
	}
	dispatch, err := dispatchFromRow(row, original, runs)
	if err != nil {
		return biz.PipelineDispatch{}, err
	}
	dispatch.OwnerRevision, err = readOwnerRevision(ctx, queries, row.TenantID, row.ExecutionID)
	if err != nil {
		return biz.PipelineDispatch{}, err
	}
	return dispatch, nil
}

func dispatchFromRow(row submissionsql.ModeldevPipelineDispatch, original submissionsql.ModeldevExecution, runs []submissionsql.ModeldevPipelineConfirmedRun) (biz.PipelineDispatch, error) {
	if !row.TenantID.Valid || !row.ExecutionID.Valid || !row.OperationID.Valid || !row.AttemptID.Valid || row.AttemptID.Bytes == [16]byte{} ||
		!row.ReservedAt.Valid || row.ReservedAt.InfinityModifier != pgtype.Finite || row.ReservedAt.Time.IsZero() ||
		row.TenantID != original.TenantID || row.ExecutionID != original.ExecutionID || row.OperationID != original.OperationID || row.SpecHash != original.SpecHash ||
		!original.AcceptedAt.Valid || original.AcceptedAt.InfinityModifier != pgtype.Finite {
		return biz.PipelineDispatch{}, biz.ErrPersistence
	}
	admission := biz.Admission{
		TenantID: original.TenantID.String(), ExecutionID: original.ExecutionID.String(), OperationID: original.OperationID.String(),
		Actor: original.Actor, IntentHash: original.IntentHash, SpecHash: original.SpecHash, AcceptedAt: original.AcceptedAt.Time.UTC(),
	}
	var storedPlan biz.PipelineDispatchPlan
	var intentErr, snapshotErr error
	admission.Intent, intentErr = cpup01.DecodeCanonicalIntent(original.IntentCanonical)
	admission.Snapshot, snapshotErr = cpup01.DecodeCanonicalSnapshot(original.SnapshotCanonical)
	if intentErr != nil || snapshotErr != nil || json.Unmarshal(row.PlanCanonical, &storedPlan) != nil {
		return biz.PipelineDispatch{}, biz.ErrPersistence
	}
	intent, snapshot, err := admission.CanonicalPayloads()
	if err != nil || !bytes.Equal(intent, original.IntentCanonical) || !bytes.Equal(snapshot, original.SnapshotCanonical) {
		return biz.PipelineDispatch{}, biz.ErrPersistence
	}
	// Re-derive every plan field from the immutable Admission plus its stored
	// owner revision. Canonical comparison rejects unknown or altered fields.
	plan, err := (biz.PipelineDispatchRequest{Admission: admission, Owner: storedPlan.Owner}).Freeze()
	if err != nil {
		return biz.PipelineDispatch{}, biz.ErrPersistence
	}
	canonical, err := plan.Canonical()
	if err != nil || !bytes.Equal(canonical, row.PlanCanonical) {
		return biz.PipelineDispatch{}, biz.ErrPersistence
	}
	hash, err := plan.Digest()
	if err != nil || hash != row.PlanHash {
		return biz.PipelineDispatch{}, biz.ErrPersistence
	}
	dispatch := biz.PipelineDispatch{AttemptID: row.AttemptID.String(), Plan: plan, PlanHash: hash, State: biz.PipelineDispatchState(row.State), ReservedAt: row.ReservedAt.Time.UTC()}
	if row.UncertainAt.Valid {
		if row.UncertainAt.InfinityModifier != pgtype.Finite || !validObservationTime(row.UncertainAt.Time) || row.UncertainAt.Time.Before(dispatch.ReservedAt) {
			return biz.PipelineDispatch{}, biz.ErrPersistence
		}
		observedAt := row.UncertainAt.Time.UTC()
		dispatch.UncertainAt = &observedAt
	}
	if row.NotSentAt.Valid {
		if row.NotSentAt.InfinityModifier != pgtype.Finite || !validObservationTime(row.NotSentAt.Time) || row.NotSentAt.Time.Before(dispatch.ReservedAt) {
			return biz.PipelineDispatch{}, biz.ErrPersistence
		}
		observedAt := row.NotSentAt.Time.UTC()
		dispatch.NotSentAt = &observedAt
	}
	switch dispatch.State {
	case biz.PipelineDispatchSubmitting:
		if row.UncertainAt.Valid || row.NotSentAt.Valid || len(runs) != 0 {
			return biz.PipelineDispatch{}, biz.ErrPersistence
		}
	case biz.PipelineDispatchNotSent:
		if !row.NotSentAt.Valid || row.UncertainAt.Valid || len(runs) != 0 {
			return biz.PipelineDispatch{}, biz.ErrPersistence
		}
	case biz.PipelineDispatchUncertain:
		if !row.UncertainAt.Valid || len(runs) != 0 {
			return biz.PipelineDispatch{}, biz.ErrPersistence
		}
	case biz.PipelineDispatchConfirmed:
		if len(runs) == 0 {
			return biz.PipelineDispatch{}, biz.ErrPersistence
		}
	default:
		return biz.PipelineDispatch{}, biz.ErrPersistence
	}
	previousRunID := ""
	for _, run := range runs {
		if run.TenantID != row.TenantID || run.ExecutionID != row.ExecutionID || run.AttemptID != row.AttemptID || run.PlanHash != row.PlanHash ||
			!run.RunID.Valid || run.RunID.Bytes == [16]byte{} || !run.FirstObservedAt.Valid || run.FirstObservedAt.InfinityModifier != pgtype.Finite ||
			!validObservationTime(run.FirstObservedAt.Time) || run.FirstObservedAt.Time.Before(dispatch.ReservedAt) {
			return biz.PipelineDispatch{}, biz.ErrPersistence
		}
		runID := run.RunID.String()
		if runID <= previousRunID {
			return biz.PipelineDispatch{}, biz.ErrPersistence
		}
		previousRunID = runID
		dispatch.ConfirmedRuns = append(dispatch.ConfirmedRuns, biz.PipelineConfirmedRun{RunID: runID, FirstObservedAt: run.FirstObservedAt.Time.UTC()})
	}
	return dispatch, nil
}

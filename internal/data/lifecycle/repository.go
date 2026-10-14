// Package lifecycle persists execution facts at managed-step boundaries. It
// never advances KFP stages or reconstructs an outbound creation permit.
package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	executionsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution/sqlc"
	lifecyclesql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle/sqlc"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/pgvalue"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	submissionsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission/sqlc"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

var _ biz.ExecutionRuntimeRepository = (*Repository)(nil)

// ReadInTransaction validates runtime facts in the caller's shared identity lock
// or repeatable-read snapshot. It never grants permission or starts a new
// transaction. A pre-runtime schema is detected before issuing runtime SQL so
// the fixed historical migration reader remains usable without swallowing an
// undefined-table error or invalidating its transaction.
func ReadInTransaction(ctx context.Context, transaction pgx.Tx, tenant, execution string) (biz.ExecutionRuntime, error) {
	if ctx == nil || transaction == nil {
		return biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	tenantID, err := databaseID(tenant)
	if err != nil {
		return biz.ExecutionRuntime{}, err
	}
	executionID, err := databaseID(execution)
	if err != nil {
		return biz.ExecutionRuntime{}, err
	}
	availability, err := lifecyclesql.New(transaction).RuntimeSchemaAvailable(ctx)
	if err != nil {
		return biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	available, valid := availability.(bool)
	if !valid {
		return biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	if !available {
		return biz.ExecutionRuntime{}, biz.ErrExecutionNotFound
	}
	current, err := readRuntime(ctx, transaction, tenantID, executionID)
	if err != nil {
		return biz.ExecutionRuntime{}, err
	}
	if current.state.Publication != nil {
		// Query callers reuse the writer's complete publication validation. A
		// stored candidate cannot become downloadable merely by being JSON.
		row, err := submissionsql.New(transaction).GetRunAuthorityRow(ctx, submissionsql.GetRunAuthorityRowParams{TenantID: tenantID, ExecutionID: executionID})
		revision, valid := pgvalue.Uint64(row.OwnerRevision)
		admitted := current.execution
		if err != nil || !valid || revision != current.state.OwnerRevision || row.TenantID.String() != admitted.TenantID || row.ExecutionID.String() != admitted.ExecutionID || row.OperationID.String() != admitted.OperationID || row.SpecHash != admitted.SpecHash || row.NamespaceName != admitted.Snapshot.Environment.NamespaceName || row.NamespaceUid.String() != admitted.Snapshot.Environment.NamespaceUID {
			return biz.ExecutionRuntime{}, biz.ErrPersistence
		}
		authority := biz.RunAuthorityCandidate{RunID: row.RunID.String(), WorkflowUID: row.WorkflowUid}
		if validPublication(admitted, authority, current.state, *current.state.Publication, false) != nil {
			return biz.ExecutionRuntime{}, biz.ErrPersistence
		}
	}
	return current.state, nil
}

type runtimeTransaction struct {
	transaction           pgx.Tx
	queries               *lifecyclesql.Queries
	tenantID, executionID pgtype.UUID
	execution             biz.Execution
	state                 biz.ExecutionRuntime
	now                   time.Time
	requireCreationOpen   bool
}

func (repository *Repository) GetRuntime(ctx context.Context, tenant, execution string) (biz.ExecutionRuntime, error) {
	tenantID, err := databaseID(tenant)
	if err != nil {
		return biz.ExecutionRuntime{}, err
	}
	executionID, err := databaseID(execution)
	if err != nil {
		return biz.ExecutionRuntime{}, err
	}
	if ctx == nil || repository == nil || repository.pool == nil {
		return biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	defer rollback(tx)
	current, err := readRuntime(ctx, tx, tenantID, executionID)
	if err != nil {
		return biz.ExecutionRuntime{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	return current.state, nil
}

func (repository *Repository) mutate(ctx context.Context, authority biz.RunAuthorityCandidate, change func(*runtimeTransaction) (bool, error)) (biz.ExecutionRuntime, bool, error) {
	tenantID, err := databaseID(authority.TenantID)
	if err != nil {
		return biz.ExecutionRuntime{}, false, err
	}
	executionID, err := databaseID(authority.ExecutionID)
	if err != nil {
		return biz.ExecutionRuntime{}, false, err
	}
	if ctx == nil || repository == nil || repository.pool == nil {
		return biz.ExecutionRuntime{}, false, biz.ErrPersistence
	}
	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return biz.ExecutionRuntime{}, false, biz.ErrPersistence
	}
	defer rollback(tx)
	queries := lifecyclesql.New(tx)
	if _, err = queries.LockRuntimeIdentity(ctx, lifecyclesql.LockRuntimeIdentityParams{TenantID: tenantID, ExecutionID: executionID}); err != nil {
		return biz.ExecutionRuntime{}, false, storageError(err)
	}
	current, err := readRuntime(ctx, tx, tenantID, executionID)
	if err != nil {
		return biz.ExecutionRuntime{}, false, err
	}
	original, err := submissionsql.New(tx).GetRunAuthorityRow(ctx, submissionsql.GetRunAuthorityRowParams{TenantID: tenantID, ExecutionID: executionID})
	var observed biz.RunAuthorityCandidate
	if errors.Is(err, pgx.ErrNoRows) && current.state.CloseGeneration > 0 && current.state.CloseAuthority != nil {
		observed = *current.state.CloseAuthority
	} else if err != nil {
		return biz.ExecutionRuntime{}, false, storageError(err)
	} else {
		observed = biz.RunAuthorityCandidate{TenantID: original.TenantID.String(), ExecutionID: original.ExecutionID.String(), OperationID: original.OperationID.String(), SpecHash: original.SpecHash, AttemptID: original.AttemptID.String(), PlanHash: original.PlanHash, RunID: original.RunID.String(), NamespaceName: original.NamespaceName, NamespaceUID: original.NamespaceUid.String(), WorkflowName: original.WorkflowName, WorkflowUID: original.WorkflowUid}
	}
	if observed != authority || observed.OperationID != current.execution.OperationID || observed.SpecHash != current.execution.SpecHash || observed.NamespaceName != current.execution.Snapshot.Environment.NamespaceName || observed.NamespaceUID != current.execution.Snapshot.Environment.NamespaceUID {
		return biz.ExecutionRuntime{}, false, biz.ErrRunAuthorityConflict
	}
	changed, err := change(current)
	if err != nil {
		return biz.ExecutionRuntime{}, false, err
	}
	if changed {
		revision, err := queries.AdvanceRuntimeRevision(ctx, lifecyclesql.AdvanceRuntimeRevisionParams{TenantID: tenantID, ExecutionID: executionID})
		if err != nil {
			return biz.ExecutionRuntime{}, false, biz.ErrPersistence
		}
		value, valid := pgvalue.Uint64(revision)
		if !valid || value == 0 {
			return biz.ExecutionRuntime{}, false, biz.ErrPersistence
		}
		current.state.OwnerRevision = value
		if err = saveRuntime(ctx, current); err != nil {
			return biz.ExecutionRuntime{}, false, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return biz.ExecutionRuntime{}, false, biz.ErrPersistence
	}
	return current.state, !changed, nil
}

func readRuntime(ctx context.Context, tx pgx.Tx, tenantID, executionID pgtype.UUID) (*runtimeTransaction, error) {
	executions := executionsql.New(tx)
	row, err := executions.GetExecution(ctx, executionsql.GetExecutionParams{TenantID: tenantID, ExecutionID: executionID})
	if err != nil {
		return nil, storageError(err)
	}
	admitted := biz.Execution{Admission: biz.Admission{TenantID: row.TenantID.String(), ExecutionID: row.ExecutionID.String(), OperationID: row.OperationID.String(), Actor: row.Actor, SpecHash: row.SpecHash, IntentHash: row.IntentHash, AcceptedAt: row.AcceptedAt.Time.UTC()}}
	if json.Unmarshal(row.IntentCanonical, &admitted.Intent) != nil || json.Unmarshal(row.SnapshotCanonical, &admitted.Snapshot) != nil {
		return nil, biz.ErrPersistence
	}
	intent, snapshot, err := admitted.CanonicalPayloads()
	if err != nil || !bytes.Equal(intent, row.IntentCanonical) || !bytes.Equal(snapshot, row.SnapshotCanonical) {
		return nil, biz.ErrPersistence
	}
	queries := lifecyclesql.New(tx)
	identity, err := queries.GetRuntimeIdentity(ctx, lifecyclesql.GetRuntimeIdentityParams{TenantID: tenantID, ExecutionID: executionID})
	if err != nil {
		return nil, storageError(err)
	}
	revision, valid := pgvalue.Uint64(identity.OwnerRevision)
	if !valid || revision == 0 {
		return nil, biz.ErrPersistence
	}
	generation, valid := pgvalue.Uint64(identity.CloseGeneration)
	if !valid {
		return nil, biz.ErrPersistence
	}
	state := biz.ExecutionRuntime{}
	facts, err := queries.GetRuntimeFacts(ctx, lifecyclesql.GetRuntimeFactsParams{TenantID: tenantID, ExecutionID: executionID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrPersistence
	}
	if err == nil && json.Unmarshal(facts, &state) != nil {
		return nil, biz.ErrPersistence
	}
	if state.Workspace != nil && !validWorkspace(admitted, *state.Workspace) {
		return nil, biz.ErrPersistence
	}
	if state.Training != nil {
		if state.Workspace == nil {
			return nil, biz.ErrPersistence
		}
		var starts []time.Time
		if state.Training.FinetuningTiming != nil {
			starts = []time.Time{state.Training.FinetuningTiming.AdmittedAt}
			if !identity.DatabaseNow.Valid || state.Training.FinetuningTiming.AdmittedAt.After(identity.DatabaseNow.Time) {
				return nil, biz.ErrPersistence
			}
		}
		expected, err := biz.FreezeTrainingPlan(admitted, *state.Workspace, starts...)
		if err != nil || !reflect.DeepEqual(expected, *state.Training) {
			return nil, biz.ErrPersistence
		}
	}
	if state.TrainingHandle != nil && (state.Training == nil || state.TrainingHandle.NamespaceUID != state.Workspace.NamespaceUID || state.TrainingHandle.PVCUID != state.Workspace.PVCUID || !validOpaqueID(state.TrainingHandle.TrainJobUID)) {
		return nil, biz.ErrPersistence
	}
	if rejected := state.TrainingRejection; rejected != nil {
		if state.Training == nil || state.TrainingHandle != nil || state.Observation != nil || state.Publication != nil ||
			rejected.RequestSHA256 != state.Training.RequestSHA256 || rejected.NamespaceUID != state.Workspace.NamespaceUID ||
			(rejected.StatusCode != 400 && rejected.StatusCode != 403) || rejected.ObservedAt.IsZero() || !identity.DatabaseNow.Valid || rejected.ObservedAt.After(identity.DatabaseNow.Time) {
			return nil, biz.ErrPersistence
		}
	}
	if state.OwnerRevision > revision || state.CloseGeneration > generation {
		return nil, biz.ErrPersistence
	}
	if state.ClosedAt != nil && state.CloseEvidence == nil {
		return nil, biz.ErrPersistence
	}
	if generation > state.CloseGeneration {
		close, err := executions.GetCloseIntent(ctx, executionsql.GetCloseIntentParams{TenantID: tenantID, ExecutionID: executionID})
		closeGeneration, valid := pgvalue.Uint64(close.OwnerGeneration)
		if err != nil || !valid || closeGeneration != generation {
			return nil, biz.ErrPersistence
		}
		// A newer command fence still blocks all creation, but cannot replace
		// the generation/reason/time proved by an earlier terminal close.
		if state.ClosedAt == nil {
			state.CloseGeneration = generation
			state.CloseReason = close.Reason
			state.CloseRequestedAt = close.RequestedAt.Time.UTC()
		}
	}
	state.OwnerRevision = revision
	if recovered := state.CloseAuthority; recovered != nil {
		if state.CloseGeneration == 0 || !validEarlyCloseCandidate(*recovered) || recovered.TenantID != admitted.TenantID || recovered.ExecutionID != admitted.ExecutionID || recovered.OperationID != admitted.OperationID || recovered.SpecHash != admitted.SpecHash || recovered.NamespaceName != admitted.Snapshot.Environment.NamespaceName || recovered.NamespaceUID != admitted.Snapshot.Environment.NamespaceUID {
			return nil, biz.ErrPersistence
		}
	}
	admitted.OwnerRevision = revision
	if !identity.DatabaseNow.Valid || identity.DatabaseNow.InfinityModifier != pgtype.Finite {
		return nil, biz.ErrPersistence
	}
	current := &runtimeTransaction{transaction: tx, queries: queries, tenantID: tenantID, executionID: executionID, execution: admitted, state: state, now: identity.DatabaseNow.Time.UTC()}
	if !validFinetuningStarts(current) {
		return nil, biz.ErrPersistence
	}
	if !validRegistrationFacts(current) {
		return nil, biz.ErrPersistence
	}
	if len(state.FinetuningSteps) > 0 {
		authority, err := submissionsql.New(tx).GetRunAuthorityRow(ctx, submissionsql.GetRunAuthorityRowParams{TenantID: tenantID, ExecutionID: executionID})
		if err != nil {
			return nil, biz.ErrPersistence
		}
		for _, start := range state.FinetuningSteps {
			if start.Binding.RunID != authority.RunID.String() || start.Binding.WorkflowUID != authority.WorkflowUid || start.Binding.SpecHash != authority.SpecHash {
				return nil, biz.ErrPersistence
			}
		}
	}
	if state.ClosedAt != nil {
		if err := validateCommittedClose(ctx, current); err != nil {
			return nil, err
		}
	}
	return current, nil
}

func saveRuntime(ctx context.Context, current *runtimeTransaction) error {
	state := current.state
	facts, err := json.Marshal(state)
	if err != nil {
		return biz.ErrPersistence
	}
	command := lifecyclesql.SaveRuntimeFactsParams{TenantID: current.tenantID, ExecutionID: current.executionID, Facts: facts, RequireCreationOpen: current.requireCreationOpen, DeadlineAt: timestamp(current.execution.Snapshot.DeadlineAt), CloseGeneration: pgtype.Numeric{Int: new(big.Int).SetUint64(state.CloseGeneration), Valid: true}}
	if state.Training != nil {
		command.TrainingName = textValue(state.Training.Name)
		command.TrainingRequestSha256 = textValue(state.Training.RequestSHA256)
	}
	if state.TrainingHandle != nil {
		command.TrainingUid = textValue(state.TrainingHandle.TrainJobUID)
	}
	if state.Publication != nil {
		command.PublicationID, err = databaseID(state.Publication.ID)
		if err != nil {
			return biz.ErrRuntimeConflict
		}
	}
	if state.CloseGeneration > 0 {
		command.CloseReason = textValue(state.CloseReason)
		command.CloseRequestedAt = timestamp(state.CloseRequestedAt)
	}
	if state.ClosedAt != nil {
		command.ClosedAt = timestamp(*state.ClosedAt)
	}
	count, err := current.queries.SaveRuntimeFacts(ctx, command)
	if err != nil {
		return biz.ErrPersistence
	}
	if count != 1 {
		return biz.ErrPipelineDispatchBlocked
	}
	return nil
}

func (repository *Repository) RecordPrepared(ctx context.Context, authority biz.RunAuthorityCandidate, workspace biz.WorkspaceBinding) (biz.ExecutionRuntime, bool, error) {
	return repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		if !validWorkspace(current.execution, workspace) {
			return false, biz.ErrRuntimeConflict
		}
		if current.state.Workspace != nil {
			if *current.state.Workspace != workspace {
				return false, biz.ErrRuntimeConflict
			}
			return false, nil
		}
		if err := requireOpen(current); err != nil {
			return false, err
		}
		current.state.Workspace = &workspace
		return true, nil
	})
}

func (repository *Repository) ReserveTraining(ctx context.Context, authority biz.RunAuthorityCandidate) (biz.TrainingReservation, error) {
	send := false
	state, _, err := repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		if current.state.Training != nil {
			return false, nil
		}
		if current.state.Workspace == nil {
			return false, biz.ErrRuntimeNotReady
		}
		if err := requireOpen(current); err != nil {
			return false, err
		}
		plan, err := biz.FreezeTrainingPlan(current.execution, *current.state.Workspace, current.now)
		if err != nil {
			return false, err
		}
		current.state.Training = &plan
		send = true
		return true, nil
	})
	if err != nil {
		return biz.TrainingReservation{}, err
	}
	return biz.TrainingReservation{State: state, SendPermit: send}, nil
}

func (repository *Repository) RecordTrainingHandle(ctx context.Context, authority biz.RunAuthorityCandidate, handle biz.TrainingHandle) (biz.ExecutionRuntime, error) {
	state, _, err := repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		if current.state.TrainingRejection != nil {
			return false, biz.ErrRuntimeConflict
		}
		if current.state.Training == nil || current.state.Workspace == nil {
			return false, biz.ErrRuntimeNotReady
		}
		if handle.NamespaceUID != current.state.Workspace.NamespaceUID || handle.PVCUID != current.state.Workspace.PVCUID || !validOpaqueID(handle.TrainJobUID) {
			return false, biz.ErrRuntimeConflict
		}
		if current.state.TrainingHandle != nil {
			if *current.state.TrainingHandle != handle {
				return false, biz.ErrRuntimeConflict
			}
			return false, nil
		}
		// A late Create response must remain reconcilable after a close fence.
		current.state.TrainingHandle = &handle
		return true, nil
	})
	return state, err
}

func (repository *Repository) RecordTrainingRejection(ctx context.Context, authority biz.RunAuthorityCandidate, rejection biz.TrainingCreationRejection) (biz.ExecutionRuntime, error) {
	state, _, err := repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		state := &current.state
		if state.Training == nil || state.Workspace == nil || state.TrainingHandle != nil || state.Observation != nil || state.Publication != nil ||
			rejection.RequestSHA256 != state.Training.RequestSHA256 || rejection.NamespaceUID != state.Workspace.NamespaceUID ||
			(rejection.StatusCode != 400 && rejection.StatusCode != 403) {
			return false, biz.ErrRuntimeConflict
		}
		if existing := state.TrainingRejection; existing != nil {
			if existing.RequestSHA256 != rejection.RequestSHA256 || existing.NamespaceUID != rejection.NamespaceUID || existing.StatusCode != rejection.StatusCode {
				return false, biz.ErrRuntimeConflict
			}
			return false, nil
		}
		// The response may arrive after a close fence; it does not grant new
		// creation authority. Preserve the plan and the database observation time.
		rejection.ObservedAt = current.now
		state.TrainingRejection = &rejection
		return true, nil
	})
	return state, err
}

func (repository *Repository) RecordTrainingObservation(ctx context.Context, authority biz.RunAuthorityCandidate, observation biz.TrainingRuntimeObservation) (biz.ExecutionRuntime, error) {
	state, _, err := repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		merged, err := mergeObservation(current.state, observation)
		if err != nil {
			return false, err
		}
		if reflect.DeepEqual(current.state.Observation, &merged) {
			return false, nil
		}
		if current.state.ClosedAt != nil {
			if !merged.WritersAbsent || merged.Outcome != current.state.Observation.Outcome {
				return false, biz.ErrRuntimeConflict
			}
			return false, nil
		}
		current.state.Observation = &merged
		return true, nil
	})
	return state, err
}

func (repository *Repository) RecordPublication(ctx context.Context, authority biz.RunAuthorityCandidate, publication biz.RuntimePublication) (biz.ExecutionRuntime, bool, error) {
	return repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		if err := validPublication(current.execution, authority, current.state, publication, true); err != nil {
			return false, err
		}
		if current.state.Publication != nil {
			if !samePublication(*current.state.Publication, publication) {
				return false, biz.ErrRuntimeConflict
			}
			return false, nil
		}
		if err := requireOpen(current); err != nil {
			return false, err
		}
		current.state.Publication = &publication
		return true, nil
	})
}

func (repository *Repository) RequestRuntimeClose(ctx context.Context, authority biz.RunAuthorityCandidate, reason string) (biz.ExecutionRuntime, bool, error) {
	return repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		if reason != "NATURAL_TERMINAL" && reason != "STEP_FAILED" && reason != "DEADLINE" {
			return false, biz.ErrInvalidAdmission
		}
		if current.state.CloseGeneration > 0 {
			if reason == "DEADLINE" && current.state.CloseReason == "NATURAL_TERMINAL" && current.state.ClosedAt == nil && !current.now.Before(current.execution.Snapshot.DeadlineAt) {
				// A natural handoff may outlive its frozen deadline. The database
				// observes expiry without replacing the original fence or receipt.
				current.state.CloseReason = "DEADLINE"
				return true, nil
			}
			return false, nil
		}
		if reason == "NATURAL_TERMINAL" && (current.state.Publication == nil || current.state.Observation == nil || current.state.Observation.Outcome != "SUCCEEDED" || !current.state.Observation.WritersAbsent) {
			return false, biz.ErrRuntimeNotReady
		}
		if reason == "DEADLINE" && current.now.Before(current.execution.Snapshot.DeadlineAt) {
			return false, biz.ErrRuntimeNotReady
		}
		generation, err := current.queries.AdvanceRuntimeClose(ctx, lifecyclesql.AdvanceRuntimeCloseParams{TenantID: current.tenantID, ExecutionID: current.executionID})
		if err != nil {
			return false, biz.ErrPersistence
		}
		value, valid := pgvalue.Uint64(generation)
		if !valid || value == 0 {
			return false, biz.ErrPersistence
		}
		current.state.CloseGeneration = value
		current.state.CloseReason = reason
		current.state.CloseRequestedAt = current.now
		return true, nil
	})
}

func (repository *Repository) ConfirmRuntimeClosed(ctx context.Context, authority biz.RunAuthorityCandidate, generation uint64, observation biz.TrainingRuntimeObservation, evidence biz.ManagedCloseEvidence) (biz.ExecutionRuntime, error) {
	state, _, err := repository.mutate(ctx, authority, func(current *runtimeTransaction) (bool, error) {
		if generation == 0 || current.state.CloseGeneration != generation {
			return false, biz.ErrRuntimeConflict
		}
		if !validExecutionCloseEvidence(current.execution, authority, current.state.CloseReason, evidence) {
			return false, biz.ErrRuntimeNotReady
		}
		if !finetuningStartsTerminated(current.state, authority, evidence) {
			return false, biz.ErrRuntimeNotReady
		}
		// Recheck retained Run identities under the same lock as observations.
		// A second late observation cannot race the earlier external proof.
		dispatch, err := submission.ReadInTransaction(ctx, current.transaction, authority.TenantID, authority.ExecutionID)
		if err != nil {
			return false, err
		}
		if current.state.CloseReviewReason == "MULTIPLE_RUNS" || len(dispatch.ConfirmedRuns) > 1 || (len(dispatch.ConfirmedRuns) == 1 && dispatch.ConfirmedRuns[0].RunID != authority.RunID) || (current.state.CloseAuthority != nil && len(dispatch.ConfirmedRuns) != 1) {
			return false, biz.ErrRunAuthorityConflict
		}
		if current.state.ClosedAt != nil {
			return false, nil
		}
		// A committed creation intent without a UID remains uncertain; absence
		// cannot prove that an in-flight API request will not produce a writer.
		if current.state.Training != nil && current.state.TrainingHandle == nil && current.state.TrainingRejection == nil {
			return false, biz.ErrTrainingUncertain
		}
		if current.state.Training != nil && current.state.TrainingRejection == nil {
			merged, err := mergeObservation(current.state, observation)
			if err != nil {
				return false, err
			}
			if !merged.WritersAbsent {
				return false, biz.ErrRuntimeNotReady
			}
			if current.state.CloseReason == "NATURAL_TERMINAL" && (merged.Outcome != "SUCCEEDED" || current.state.Publication == nil) {
				return false, biz.ErrRuntimeNotReady
			}
			current.state.Observation = &merged
		} else if current.state.CloseReason == "NATURAL_TERMINAL" {
			return false, biz.ErrRuntimeNotReady
		}
		current.state.CloseEvidence = &evidence
		current.state.CloseReviewReason = ""
		closed := current.now
		current.state.ClosedAt = &closed
		return true, nil
	})
	return state, err
}

func requireOpen(current *runtimeTransaction) error {
	if current.state.CloseGeneration != 0 || !current.now.Before(current.execution.Snapshot.DeadlineAt) {
		return biz.ErrPipelineDispatchBlocked
	}
	current.requireCreationOpen = true
	return nil
}
func databaseID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || id.Scan(value) != nil || !id.Valid || id.Bytes == [16]byte{} {
		return id, biz.ErrInvalidAdmission
	}
	return id, nil
}
func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: !value.IsZero()}
}
func textValue(value string) pgtype.Text { return pgtype.Text{String: value, Valid: true} }
func storageError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.ErrExecutionNotFound
	}
	return biz.ErrPersistence
}
func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

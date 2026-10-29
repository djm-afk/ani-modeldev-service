package lifecycle

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	lifecyclesql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle/sqlc"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/pgvalue"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	submissionsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission/sqlc"
)

func (repository *Repository) FenceOwnerClose(ctx context.Context, tenant, execution string) (biz.ExecutionRuntime, error) {
	return repository.mutateOwnerClose(ctx, tenant, execution, nil, false)
}

func (repository *Repository) MarkOwnerCloseReview(ctx context.Context, tenant, execution, reason string) (biz.ExecutionRuntime, error) {
	if reason != "KFP_CREATE_UNRESOLVED" && reason != "TRAINJOB_CREATE_UNRESOLVED" && reason != "MULTIPLE_RUNS" && reason != "TRAINING_LOGS_UNAVAILABLE" {
		return biz.ExecutionRuntime{}, biz.ErrInvalidAdmission
	}
	return repository.mutateOwnerClose(ctx, tenant, execution, func(current *runtimeTransaction, _ pgx.Tx) (bool, error) {
		if current.state.CloseReviewReason == reason || current.state.CloseReviewReason == "MULTIPLE_RUNS" {
			return false, nil
		}
		current.state.CloseReviewReason = reason
		return true, nil
	}, false)
}

// RecordClosingRun keeps a verified original Run after the creation fence. It
// never inserts normal run_authorities or constructs a PipelineSendPermit.
func (repository *Repository) RecordClosingRun(ctx context.Context, candidate biz.RunAuthorityCandidate) (biz.ExecutionRuntime, error) {
	if !validEarlyCloseCandidate(candidate) {
		return biz.ExecutionRuntime{}, biz.ErrInvalidAdmission
	}
	return repository.mutateOwnerClose(ctx, candidate.TenantID, candidate.ExecutionID, recordClosingRun(ctx, candidate), false)
}

// FenceManagedFailureClose is called only after current workload and original
// KFP CLOSE identity have been verified. It revalidates the frozen candidate in
// the same transaction as the fence, without inserting normal run authority.
func (repository *Repository) FenceManagedFailureClose(ctx context.Context, candidate biz.RunAuthorityCandidate) (biz.ExecutionRuntime, bool, error) {
	if !validEarlyCloseCandidate(candidate) {
		return biz.ExecutionRuntime{}, false, biz.ErrInvalidAdmission
	}
	replayed := false
	state, err := repository.mutateOwnerClose(ctx, candidate.TenantID, candidate.ExecutionID, func(current *runtimeTransaction, tx pgx.Tx) (bool, error) {
		dispatch, err := submission.ReadInTransaction(ctx, tx, candidate.TenantID, candidate.ExecutionID)
		if err != nil {
			return false, err
		}
		if dispatch.State != biz.PipelineDispatchConfirmed || len(dispatch.ConfirmedRuns) != 1 || dispatch.ConfirmedRuns[0].RunID != candidate.RunID {
			return false, biz.ErrRunAuthorityConflict
		}
		replayed = current.state.CloseAuthority != nil && *current.state.CloseAuthority == candidate
		return recordClosingRun(ctx, candidate)(current, tx)
	}, true)
	return state, replayed, err
}

func recordClosingRun(ctx context.Context, candidate biz.RunAuthorityCandidate) func(*runtimeTransaction, pgx.Tx) (bool, error) {
	return func(current *runtimeTransaction, tx pgx.Tx) (bool, error) {
		if current.state.CloseReviewReason == "MULTIPLE_RUNS" {
			return false, biz.ErrRunAuthorityConflict
		}
		dispatch, err := submission.ReadInTransaction(ctx, tx, candidate.TenantID, candidate.ExecutionID)
		if err != nil {
			return false, err
		}
		plan := dispatch.Plan
		if plan.OperationID != candidate.OperationID || plan.SpecHash != candidate.SpecHash || dispatch.AttemptID != candidate.AttemptID || dispatch.PlanHash != candidate.PlanHash || plan.Environment.NamespaceName != candidate.NamespaceName || plan.Environment.NamespaceUID != candidate.NamespaceUID || current.execution.OperationID != candidate.OperationID || current.execution.SpecHash != candidate.SpecHash {
			return false, biz.ErrRunAuthorityConflict
		}
		if len(dispatch.ConfirmedRuns) > 1 || (len(dispatch.ConfirmedRuns) == 1 && dispatch.ConfirmedRuns[0].RunID != candidate.RunID) {
			return false, biz.ErrRunAuthorityConflict
		}
		if current.state.CloseAuthority != nil && *current.state.CloseAuthority != candidate {
			return false, biz.ErrRunAuthorityConflict
		}
		queries := submissionsql.New(tx)
		authority, err := queries.GetRunAuthorityRow(ctx, submissionsql.GetRunAuthorityRowParams{TenantID: current.tenantID, ExecutionID: current.executionID})
		if err == nil {
			if earlyCloseAuthority(authority).RunAuthorityCandidate != candidate {
				return false, biz.ErrRunAuthorityConflict
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return false, biz.ErrPersistence
		}
		if len(dispatch.ConfirmedRuns) == 1 && current.state.CloseAuthority != nil {
			return false, nil
		}
		attemptID, _ := databaseID(candidate.AttemptID)
		runID, _ := databaseID(candidate.RunID)
		count, err := queries.InsertConfirmedPipelineRun(ctx, submissionsql.InsertConfirmedPipelineRunParams{TenantID: current.tenantID, ExecutionID: current.executionID, AttemptID: attemptID, PlanHash: candidate.PlanHash, RunID: runID, ObservedAt: timestamp(current.now)})
		if err != nil || count < 0 || count > 1 {
			return false, biz.ErrPersistence
		}
		if dispatch.State != biz.PipelineDispatchConfirmed {
			if _, err := queries.MarkSubmissionConfirmed(ctx, submissionsql.MarkSubmissionConfirmedParams{TenantID: current.tenantID, ExecutionID: current.executionID, AttemptID: attemptID, PlanHash: candidate.PlanHash}); err != nil {
				return false, biz.ErrPersistence
			}
		}
		// Recovering the original owner cannot resolve missing writer evidence.
		// ConfirmRuntimeClosed clears review only with the durable close proof.
		current.state.CloseAuthority = &candidate
		return true, nil
	}
}

// Keep every verified identity in the existing original-attempt history. The
// next shorter API list cannot erase ambiguity or elect a replacement Run.
func (repository *Repository) RecordClosingRunAmbiguity(ctx context.Context, observed biz.PipelineDispatch, runIDs []string) (biz.ExecutionRuntime, error) {
	if len(runIDs) != 2 || runIDs[0] == runIDs[1] {
		return biz.ExecutionRuntime{}, biz.ErrInvalidAdmission
	}
	for _, id := range runIDs {
		value, err := databaseID(id)
		if err != nil || value.String() != id {
			return biz.ExecutionRuntime{}, biz.ErrInvalidAdmission
		}
	}
	return repository.mutateOwnerClose(ctx, observed.Plan.TenantID, observed.Plan.ExecutionID, func(current *runtimeTransaction, tx pgx.Tx) (bool, error) {
		dispatch, err := submission.ReadInTransaction(ctx, tx, observed.Plan.TenantID, observed.Plan.ExecutionID)
		if err != nil {
			return false, err
		}
		if dispatch.AttemptID != observed.AttemptID || dispatch.PlanHash != observed.PlanHash {
			return false, biz.ErrRunAuthorityConflict
		}
		queries := submissionsql.New(tx)
		attemptID, _ := databaseID(dispatch.AttemptID)
		changed := current.state.CloseReviewReason != "MULTIPLE_RUNS"
		for _, id := range runIDs {
			runID, _ := databaseID(id)
			count, err := queries.InsertConfirmedPipelineRun(ctx, submissionsql.InsertConfirmedPipelineRunParams{TenantID: current.tenantID, ExecutionID: current.executionID, AttemptID: attemptID, PlanHash: dispatch.PlanHash, RunID: runID, ObservedAt: timestamp(current.now)})
			if err != nil || count < 0 || count > 1 {
				return false, biz.ErrPersistence
			}
			changed = changed || count == 1
		}
		if dispatch.State != biz.PipelineDispatchConfirmed {
			if _, err := queries.MarkSubmissionConfirmed(ctx, submissionsql.MarkSubmissionConfirmedParams{TenantID: current.tenantID, ExecutionID: current.executionID, AttemptID: attemptID, PlanHash: dispatch.PlanHash}); err != nil {
				return false, biz.ErrPersistence
			}
		}
		current.state.CloseReviewReason = "MULTIPLE_RUNS"
		return changed, nil
	}, false)
}

// The owner may persist a deadline fence without a normal Run binding. All
// close recovery changes share the existing identity lock with every creator.
func (repository *Repository) mutateOwnerClose(ctx context.Context, tenant, execution string, change func(*runtimeTransaction, pgx.Tx) (bool, error), authenticatedFailure bool) (biz.ExecutionRuntime, error) {
	tenantID, tenantErr := databaseID(tenant)
	executionID, executionErr := databaseID(execution)
	if tenantErr != nil || executionErr != nil || ctx == nil {
		return biz.ExecutionRuntime{}, biz.ErrInvalidAdmission
	}
	if repository == nil || repository.pool == nil {
		return biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	defer rollback(tx)
	queries := lifecyclesql.New(tx)
	if _, err := queries.LockRuntimeIdentity(ctx, lifecyclesql.LockRuntimeIdentityParams{TenantID: tenantID, ExecutionID: executionID}); err != nil {
		return biz.ExecutionRuntime{}, storageError(err)
	}
	current, err := readRuntime(ctx, tx, tenantID, executionID)
	if err != nil {
		return biz.ExecutionRuntime{}, err
	}
	if current.state.ClosedAt != nil {
		return current.state, nil
	}
	changed := false
	if current.state.CloseGeneration == 0 {
		if !authenticatedFailure && current.now.Before(current.execution.Snapshot.DeadlineAt) {
			return biz.ExecutionRuntime{}, biz.ErrRuntimeNotReady
		}
		generation, err := queries.AdvanceRuntimeClose(ctx, lifecyclesql.AdvanceRuntimeCloseParams{TenantID: tenantID, ExecutionID: executionID})
		if err != nil {
			return biz.ExecutionRuntime{}, biz.ErrPersistence
		}
		value, valid := pgvalue.Uint64(generation)
		if !valid || value == 0 {
			return biz.ExecutionRuntime{}, biz.ErrPersistence
		}
		current.state.CloseGeneration, current.state.CloseReason, current.state.CloseRequestedAt = value, "DEADLINE", current.now
		if authenticatedFailure {
			current.state.CloseReason = "STEP_FAILED"
		}
		changed = true
	}
	if change != nil {
		updated, err := change(current, tx)
		if err != nil {
			return biz.ExecutionRuntime{}, err
		}
		changed = changed || updated
	}
	if changed {
		revision, err := queries.AdvanceRuntimeRevision(ctx, lifecyclesql.AdvanceRuntimeRevisionParams{TenantID: tenantID, ExecutionID: executionID})
		if err != nil {
			return biz.ExecutionRuntime{}, biz.ErrPersistence
		}
		value, valid := pgvalue.Uint64(revision)
		if !valid || value == 0 {
			return biz.ExecutionRuntime{}, biz.ErrPersistence
		}
		current.state.OwnerRevision = value
		if err := saveRuntime(ctx, current); err != nil {
			return biz.ExecutionRuntime{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	return current.state, nil
}

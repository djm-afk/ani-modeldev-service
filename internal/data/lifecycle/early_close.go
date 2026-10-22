package lifecycle

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	lifecyclesql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle/sqlc"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/pgvalue"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	submissionsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission/sqlc"
)

// CloseUnboundRuntime handles an authenticated KFP close task after all declared
// data-plane tasks were explicitly skipped. Binding and the closed creation
// fence commit together; no other transaction can observe an open authority.
// The caller must have independently verified TokenReview, KFP and writer facts.
func (repository *Repository) CloseUnboundRuntime(ctx context.Context, candidate biz.RunAuthorityCandidate, evidence biz.ManagedCloseEvidence) (biz.RunAuthority, biz.ExecutionRuntime, error) {
	if ctx == nil || !validEarlyCloseCandidate(candidate) || len(evidence.SkippedTasks) < 4 {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrRuntimeConflict
	}
	if repository == nil || repository.pool == nil {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	tenantID, _ := databaseID(candidate.TenantID)
	executionID, _ := databaseID(candidate.ExecutionID)
	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	defer rollback(tx)
	queries := lifecyclesql.New(tx)
	if _, err = queries.LockRuntimeIdentity(ctx, lifecyclesql.LockRuntimeIdentityParams{TenantID: tenantID, ExecutionID: executionID}); err != nil {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, storageError(err)
	}
	current, err := readRuntime(ctx, tx, tenantID, executionID)
	if err != nil {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, err
	}
	if len(evidence.SkippedTasks) != executionDataPlaneTaskCount(current.execution) || !validExecutionCloseEvidence(current.execution, candidate, "STEP_FAILED", evidence) {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrRuntimeConflict
	}
	// ReadInTransaction re-derives the complete frozen dispatch from the original
	// canonical admission and owner configuration, including its plan digest.
	dispatch, err := submission.ReadInTransaction(ctx, tx, candidate.TenantID, candidate.ExecutionID)
	if err != nil {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, err
	}
	plan := dispatch.Plan
	if plan.TenantID != candidate.TenantID || plan.ExecutionID != candidate.ExecutionID || plan.OperationID != candidate.OperationID || plan.SpecHash != candidate.SpecHash || dispatch.AttemptID != candidate.AttemptID || dispatch.PlanHash != candidate.PlanHash || plan.Environment.NamespaceName != candidate.NamespaceName || plan.Environment.NamespaceUID != candidate.NamespaceUID || current.execution.OperationID != candidate.OperationID || current.execution.SpecHash != candidate.SpecHash || current.state.OwnerRevision != dispatch.OwnerRevision {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrRunAuthorityConflict
	}
	state := current.state
	if state.Workspace != nil || state.Training != nil || state.TrainingHandle != nil || state.Observation != nil || state.Publication != nil {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrRuntimeConflict
	}
	authorities := submissionsql.New(tx)
	existing, err := authorities.GetRunAuthorityRow(ctx, submissionsql.GetRunAuthorityRowParams{TenantID: tenantID, ExecutionID: executionID})
	if err == nil {
		authority := earlyCloseAuthority(existing)
		if authority.RunAuthorityCandidate != candidate {
			return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrRunAuthorityConflict
		}
		if state.CloseGeneration == 0 || state.ClosedAt == nil || state.CloseReason != "STEP_FAILED" || state.CloseEvidence == nil || len(state.CloseEvidence.SkippedTasks) != executionDataPlaneTaskCount(current.execution) || !validExecutionCloseEvidence(current.execution, candidate, "STEP_FAILED", *state.CloseEvidence) || !existing.BoundAt.Valid || existing.BoundAt.InfinityModifier != pgtype.Finite || authority.BoundAt.Before(dispatch.ReservedAt) || !authority.BoundAt.Before(plan.DeadlineAt) {
			return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrRuntimeConflict
		}
		authority.OwnerRevision = state.OwnerRevision
		if err = tx.Commit(ctx); err != nil {
			return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrPersistence
		}
		return authority, state, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	if state.CloseGeneration != 0 || state.ClosedAt != nil || !current.now.Before(plan.DeadlineAt) {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrPipelineDispatchBlocked
	}
	operationID, _ := databaseID(candidate.OperationID)
	attemptID, _ := databaseID(candidate.AttemptID)
	runID, _ := databaseID(candidate.RunID)
	namespaceUID, _ := databaseID(candidate.NamespaceUID)
	inserted, err := authorities.InsertRunAuthority(ctx, submissionsql.InsertRunAuthorityParams{TenantID: tenantID, ExecutionID: executionID, OperationID: operationID, SpecHash: candidate.SpecHash, AttemptID: attemptID, PlanHash: candidate.PlanHash, RunID: runID, NamespaceName: candidate.NamespaceName, NamespaceUid: namespaceUID, WorkflowName: candidate.WorkflowName, WorkflowUid: candidate.WorkflowUID, DeadlineAt: timestamp(plan.DeadlineAt)})
	if err != nil {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	if inserted != 1 {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrPipelineDispatchBlocked
	}
	generation, err := queries.AdvanceRuntimeClose(ctx, lifecyclesql.AdvanceRuntimeCloseParams{TenantID: tenantID, ExecutionID: executionID})
	if err != nil {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	closeGeneration, valid := pgvalue.Uint64(generation)
	if !valid || closeGeneration == 0 {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	revision, err := queries.AdvanceRuntimeRevision(ctx, lifecyclesql.AdvanceRuntimeRevisionParams{TenantID: tenantID, ExecutionID: executionID})
	if err != nil {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	ownerRevision, valid := pgvalue.Uint64(revision)
	if !valid || ownerRevision == 0 {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	clock, err := queries.GetRuntimeIdentity(ctx, lifecyclesql.GetRuntimeIdentityParams{TenantID: tenantID, ExecutionID: executionID})
	if err != nil || !clock.DatabaseNow.Valid || clock.DatabaseNow.InfinityModifier != pgtype.Finite {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	closedAt := clock.DatabaseNow.Time.UTC()
	current.state.CloseGeneration = closeGeneration
	current.state.CloseReason = "STEP_FAILED"
	current.state.CloseRequestedAt = closedAt
	current.state.ClosedAt = &closedAt
	current.state.CloseEvidence = &evidence
	current.state.OwnerRevision = ownerRevision
	if err = saveRuntime(ctx, current); err != nil {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, err
	}
	stored, err := authorities.GetRunAuthorityRow(ctx, submissionsql.GetRunAuthorityRowParams{TenantID: tenantID, ExecutionID: executionID})
	if err != nil {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	authority := earlyCloseAuthority(stored)
	authority.OwnerRevision = ownerRevision
	if authority.RunAuthorityCandidate != candidate || !stored.BoundAt.Valid || stored.BoundAt.InfinityModifier != pgtype.Finite || authority.BoundAt.Before(dispatch.ReservedAt) || !authority.BoundAt.Before(plan.DeadlineAt) || authority.BoundAt.After(closedAt) {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	if err = tx.Commit(ctx); err != nil {
		return biz.RunAuthority{}, biz.ExecutionRuntime{}, biz.ErrPersistence
	}
	return authority, current.state, nil
}

func validEarlyCloseCandidate(candidate biz.RunAuthorityCandidate) bool {
	for _, value := range []string{candidate.TenantID, candidate.ExecutionID, candidate.OperationID, candidate.AttemptID, candidate.RunID, candidate.NamespaceUID} {
		id, err := databaseID(value)
		if err != nil || id.String() != value {
			return false
		}
	}
	return validDigest(candidate.SpecHash) && validDigest(candidate.PlanHash) && len(validation.IsDNS1123Label(candidate.NamespaceName)) == 0 && len(validation.IsDNS1123Subdomain(candidate.WorkflowName)) == 0 && validOpaqueID(candidate.WorkflowUID)
}

func earlyCloseAuthority(row submissionsql.GetRunAuthorityRowRow) biz.RunAuthority {
	return biz.RunAuthority{RunAuthorityCandidate: biz.RunAuthorityCandidate{TenantID: row.TenantID.String(), ExecutionID: row.ExecutionID.String(), OperationID: row.OperationID.String(), SpecHash: row.SpecHash, AttemptID: row.AttemptID.String(), PlanHash: row.PlanHash, RunID: row.RunID.String(), NamespaceName: row.NamespaceName, NamespaceUID: row.NamespaceUid.String(), WorkflowName: row.WorkflowName, WorkflowUID: row.WorkflowUid}, BoundAt: row.BoundAt.Time.UTC()}
}

package submission

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	submissionsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission/sqlc"
)

var _ biz.PendingAdmissionRepository = (*Repository)(nil)

// ListPendingAdmissions is a bounded tenant-and-binding-specific discovery
// read, never a lease or permission. Reserve rechecks close/deadline and original
// admission under its existing identity lock immediately before any send.
func (repository *Repository) ListPendingAdmissions(ctx context.Context, binding biz.PipelineDispatchBinding, limit int) ([]biz.Admission, error) {
	if ctx == nil || binding.Validate() != nil || limit < 1 || limit > 100 {
		return nil, biz.ErrInvalidDispatchWorker
	}
	if repository == nil || repository.pool == nil {
		return nil, biz.ErrPersistence
	}
	tenantID, err := databaseID(binding.TenantID)
	if err != nil {
		return nil, err
	}
	environment, err := json.Marshal(binding.Environment)
	if err != nil {
		return nil, biz.ErrInvalidDispatchWorker
	}
	rows, err := submissionsql.New(repository.pool).ListPendingAdmissions(ctx, submissionsql.ListPendingAdmissionsParams{TenantID: tenantID, Environment: environment, BatchSize: int32(limit)})
	if err != nil {
		return nil, biz.ErrPersistence
	}
	admissions := make([]biz.Admission, 0, len(rows))
	for _, row := range rows {
		if row.TenantID != tenantID || !row.AcceptedAt.Valid || row.AcceptedAt.InfinityModifier != pgtype.Finite {
			return nil, biz.ErrPersistence
		}
		admission := biz.Admission{TenantID: row.TenantID.String(), ExecutionID: row.ExecutionID.String(), OperationID: row.OperationID.String(), Actor: row.Actor, IntentHash: row.IntentHash, SpecHash: row.SpecHash, AcceptedAt: row.AcceptedAt.Time.UTC()}
		var intentErr, snapshotErr error
		admission.Intent, intentErr = cpup01.DecodeCanonicalIntent(row.IntentCanonical)
		admission.Snapshot, snapshotErr = cpup01.DecodeCanonicalSnapshot(row.SnapshotCanonical)
		if intentErr != nil || snapshotErr != nil {
			return nil, biz.ErrPersistence
		}
		intent, snapshot, err := admission.CanonicalPayloads()
		if err != nil || !bytes.Equal(intent, row.IntentCanonical) || !bytes.Equal(snapshot, row.SnapshotCanonical) || admission.Snapshot.Environment != binding.Environment {
			return nil, biz.ErrPersistence
		}
		admissions = append(admissions, admission)
	}
	return admissions, nil
}

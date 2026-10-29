package input

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	inputsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input/sqlc"
)

// RecordVerifiedCSV is an internal persistence boundary for a trusted verifier
// observation. It must match the entire frozen import before publishing READY.
// No request transport exposes a state or verification-proof override.
func (r *Repository) RecordVerifiedCSV(ctx context.Context, expected biz.InputImport, verified biz.VerifiedCSV) (biz.InputVersion, error) {
	if err := verified.ValidateFor(expected); err != nil {
		return biz.InputVersion{}, err
	}
	return r.recordVerifiedInput(ctx, expected, verified.VerifiedAt, verified.SchemaVersion, verified.RowCount, verified.FeatureCount, verified.PreparedDataset)
}

func (r *Repository) RecordVerifiedAlpaca(ctx context.Context, expected biz.InputImport, verified biz.VerifiedAlpaca) (biz.InputVersion, error) {
	if err := verified.ValidateFor(expected); err != nil {
		return biz.InputVersion{}, err
	}
	return r.recordVerifiedInput(ctx, expected, verified.VerifiedAt, verified.SchemaVersion, verified.RowCount, 0, verified.PreparedDataset)
}

func (r *Repository) recordVerifiedInput(ctx context.Context, expected biz.InputImport, at time.Time, schema string, rows int64, features uint32, prepared cpup01.PreparedDatasetFacts) (biz.InputVersion, error) {
	if rows <= 0 || prepared.ValidateForSource(expected.Format, expected.Object.SizeBytes, expected.Object.SHA256, rows) != nil {
		return biz.InputVersion{}, biz.ErrInputVerification
	}
	command, err := importCommand(expected)
	if err != nil {
		return biz.InputVersion{}, err
	}
	if r == nil || r.pool == nil {
		return biz.InputVersion{}, biz.ErrPersistence
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return biz.InputVersion{}, biz.ErrPersistence
	}
	defer tx.Rollback(ctx)
	queries := inputsql.New(tx)
	row, err := queries.LockInputVersion(ctx, inputsql.LockInputVersionParams{TenantID: command.TenantID, InputVersionID: command.InputVersionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.InputVersion{}, biz.ErrInputNotFound
	}
	if err != nil {
		return biz.InputVersion{}, biz.ErrPersistence
	}
	if !sameImport(row, command) {
		return biz.InputVersion{}, biz.ErrInputConflict
	}
	version, err := versionFromRow(row)
	if err != nil {
		return biz.InputVersion{}, err
	}
	if version.State == biz.InputStateRejected {
		return version, biz.ErrInputVerification
	}
	if version.State == biz.InputStateReady {
		if expected.InputFormat() == "CSV" {
			proof := version.Verification
			if proof == nil || proof.SchemaVersion != schema || proof.RowCount != rows || proof.FeatureCount != features || proof.PreparedDataset != prepared {
				return biz.InputVersion{}, biz.ErrInputVerification
			}
		} else {
			proof := version.AlpacaVerification
			if proof == nil || proof.SchemaVersion != schema || proof.RowCount != rows || features != 0 || proof.PreparedDataset != prepared {
				return biz.InputVersion{}, biz.ErrInputVerification
			}
		}
	}
	if version.State == biz.InputStateValidating {
		row, err = queries.RecordVerifiedCSV(ctx, inputsql.RecordVerifiedCSVParams{
			TenantID: command.TenantID, InputVersionID: command.InputVersionID, RequestID: command.RequestID,
			VerifiedAt:            pgtype.Timestamptz{Time: at.UTC().Truncate(time.Microsecond), Valid: true},
			VerifiedSchemaVersion: pgtype.Text{String: schema, Valid: true},
			VerifiedRowCount:      pgtype.Int8{Int64: rows, Valid: true},
			VerifiedFeatureCount:  pgtype.Int4{Int32: int32(features), Valid: true},
			PreparedFormat:        pgtype.Text{String: prepared.Format, Valid: true},
			PreparedTransformID:   pgtype.Text{String: prepared.TransformID, Valid: true},
			PreparedSizeBytes:     pgtype.Int8{Int64: prepared.SizeBytes, Valid: true},
			PreparedSha256:        pgtype.Text{String: prepared.SHA256, Valid: true},
			PreparedRowCount:      pgtype.Int8{Int64: prepared.RowCount, Valid: true},
		})
		if err != nil {
			return biz.InputVersion{}, biz.ErrPersistence
		}
		version, err = versionFromRow(row)
		if err != nil {
			return biz.InputVersion{}, err
		}
	}
	// Equivalent concurrent/restarted verification returns the original proof
	// without changing its timestamp or downgrading an already READY version.
	if err := tx.Commit(ctx); err != nil {
		return biz.InputVersion{}, biz.ErrPersistence
	}
	return version, nil
}

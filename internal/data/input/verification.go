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

// PublishVerifiedAlpacaInTx publishes a trusted Alpaca observation inside a
// caller-owned transaction. The online dataset import uses it so the READY
// input, its version metadata and the task transition share one commit. The
// caller owns the transaction, its locks and its rollback; this does not take
// the input identity lock on the caller's behalf.
func (r *Repository) PublishVerifiedAlpacaInTx(ctx context.Context, tx pgx.Tx, expected biz.InputImport, verified biz.VerifiedAlpaca) (biz.InputVersion, error) {
	if err := verified.ValidateFor(expected); err != nil {
		return biz.InputVersion{}, err
	}
	command, err := importCommand(expected)
	if err != nil {
		return biz.InputVersion{}, err
	}
	return publishVerifiedInTx(ctx, tx, command, true, verified.VerifiedAt, verified.SchemaVersion, verified.RowCount, 0, verified.PreparedDataset)
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
	version, err := publishVerifiedInTx(ctx, tx, command, expected.InputFormat() == "JSON", at, schema, rows, features, prepared)
	if err != nil {
		// A rejected row is the caller-visible terminal observation; it must be
		// returned alongside the error, exactly as it is stored.
		return version, err
	}
	if err := tx.Commit(ctx); err != nil {
		return biz.InputVersion{}, biz.ErrPersistence
	}
	return version, nil
}

// publishVerifiedInTx writes the READY proof inside a caller-owned transaction.
// It is the single place that turns a trusted verifier observation into
// durable input facts, so the online import can publish an InputVersion and
// its dataset metadata in one commit instead of chaining two public methods.
// The caller must have taken the input identity lock first.
func publishVerifiedInTx(ctx context.Context, tx pgx.Tx, command inputsql.InsertFrozenImportParams, alpacaProof bool, at time.Time, schema string, rows int64, features uint32, prepared cpup01.PreparedDatasetFacts) (biz.InputVersion, error) {
	if tx == nil || rows <= 0 {
		return biz.InputVersion{}, biz.ErrInputVerification
	}
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
	// A terminal rejection is the first observation: a late proof must return
	// that same row with the verification error, never replace it.
	if version.State == biz.InputStateRejected {
		return version, biz.ErrInputVerification
	}
	// An equivalent concurrent/restarted verification returns the original
	// proof unchanged; it must never rewrite the timestamp or downgrade READY.
	if version.State == biz.InputStateReady {
		if alpacaProof {
			proof := version.AlpacaVerification
			if proof == nil || proof.SchemaVersion != schema || proof.RowCount != rows || features != 0 || proof.PreparedDataset != prepared {
				return biz.InputVersion{}, biz.ErrInputVerification
			}
			return version, nil
		}
		proof := version.Verification
		if proof == nil || proof.SchemaVersion != schema || proof.RowCount != rows || proof.FeatureCount != features || proof.PreparedDataset != prepared {
			return biz.InputVersion{}, biz.ErrInputVerification
		}
		return version, nil
	}
	if version.State != biz.InputStateValidating {
		return biz.InputVersion{}, biz.ErrInputVerification
	}
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
	return versionFromRow(row)
}

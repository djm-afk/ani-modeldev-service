// Package input persists fixed managed input imports. It does not authorize
// callers or inspect remote object bytes.
package input

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	inputsql "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input/sqlc"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// FindImport reads both immutable business keys before applying policy to a
// genuinely new import. A reused request cannot be hidden by a later L change.
func (r *Repository) FindImport(ctx context.Context, request biz.InputImport) (biz.InputVersion, error) {
	command, err := importCommand(request)
	if err != nil {
		return biz.InputVersion{}, err
	}
	if r == nil || r.pool == nil {
		return biz.InputVersion{}, biz.ErrPersistence
	}
	queries := inputsql.New(r.pool)
	row, err := queries.GetInputVersion(ctx, inputsql.GetInputVersionParams{TenantID: command.TenantID, InputVersionID: command.InputVersionID})
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = queries.GetInputVersionByRequestID(ctx, inputsql.GetInputVersionByRequestIDParams{TenantID: command.TenantID, RequestID: command.RequestID})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.InputVersion{}, biz.ErrInputNotFound
	}
	if err != nil {
		return biz.InputVersion{}, biz.ErrPersistence
	}
	if !sameImport(row, command) {
		return biz.InputVersion{}, biz.ErrInputConflict
	}
	return versionFromRow(row)
}

func (r *Repository) FreezeImport(ctx context.Context, request biz.InputImport) (biz.InputVersion, error) {
	command, err := importCommand(request)
	if err != nil {
		return biz.InputVersion{}, err
	}
	if r == nil || r.pool == nil {
		return biz.InputVersion{}, biz.ErrPersistence
	}
	// One immutable row is the complete import command. The autocommit INSERT
	// must finish before this method can acknowledge the VALIDATING receipt.
	queries := inputsql.New(r.pool)
	row, err := queries.InsertFrozenImport(ctx, command)
	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT waits for a competing insertion to finish. Immutable
		// request fields can then be compared through this explicit tenant read.
		row, err = queries.GetInputVersion(ctx, inputsql.GetInputVersionParams{TenantID: command.TenantID, InputVersionID: command.InputVersionID})
		if errors.Is(err, pgx.ErrNoRows) {
			return biz.InputVersion{}, biz.ErrInputConflict
		}
		if err != nil {
			return biz.InputVersion{}, biz.ErrPersistence
		}
		if !sameImport(row, command) {
			return biz.InputVersion{}, biz.ErrInputConflict
		}
	}
	if err != nil {
		return biz.InputVersion{}, biz.ErrPersistence
	}
	return versionFromRow(row)
}

func importCommand(request biz.InputImport) (inputsql.InsertFrozenImportParams, error) {
	if err := request.Validate(); err != nil {
		return inputsql.InsertFrozenImportParams{}, err
	}
	tenant, err := databaseID(request.TenantID)
	if err != nil {
		return inputsql.InsertFrozenImportParams{}, err
	}
	inputID, err := databaseID(request.InputVersionID)
	if err != nil {
		return inputsql.InsertFrozenImportParams{}, err
	}
	requestID, err := databaseID(request.RequestID)
	if err != nil {
		return inputsql.InsertFrozenImportParams{}, err
	}
	return inputsql.InsertFrozenImportParams{
		Format:   request.InputFormat(),
		TenantID: tenant, InputVersionID: inputID, RequestID: requestID,
		Actor: request.Actor, RequestedAt: pgtype.Timestamptz{Time: request.RequestedAt.UTC(), Valid: true},
		StorageConnectionID: request.Scope.StorageConnectionID, Bucket: request.Scope.Bucket, ApprovedPrefix: request.Scope.ApprovedPrefix,
		CredentialReference: request.Scope.CredentialReference,
		ObjectKey:           request.Object.Key, ObjectVersionID: *request.Object.VersionID, SizeBytes: request.Object.SizeBytes, Sha256: request.Object.SHA256,
	}, nil
}

func sameImport(row inputsql.ModeldevInputVersion, command inputsql.InsertFrozenImportParams) bool {
	return row.TenantID == command.TenantID && row.InputVersionID == command.InputVersionID && row.RequestID == command.RequestID &&
		row.Actor == command.Actor && row.RequestedAt.Valid && row.RequestedAt.InfinityModifier == pgtype.Finite && row.RequestedAt.Time.Equal(command.RequestedAt.Time) &&
		row.StorageConnectionID == command.StorageConnectionID && row.Bucket == command.Bucket && row.ApprovedPrefix == command.ApprovedPrefix && row.CredentialReference == command.CredentialReference &&
		row.ObjectKey == command.ObjectKey && row.ObjectVersionID == command.ObjectVersionID && row.SizeBytes == command.SizeBytes && row.Sha256 == command.Sha256 && row.Format == command.Format
}

func (r *Repository) Get(ctx context.Context, tenantID, inputVersionID string) (biz.InputVersion, error) {
	tenant, err := databaseID(tenantID)
	if err != nil {
		return biz.InputVersion{}, err
	}
	inputID, err := databaseID(inputVersionID)
	if err != nil {
		return biz.InputVersion{}, err
	}
	if r == nil || r.pool == nil {
		return biz.InputVersion{}, biz.ErrPersistence
	}
	row, err := inputsql.New(r.pool).GetInputVersion(ctx, inputsql.GetInputVersionParams{TenantID: tenant, InputVersionID: inputID})
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.InputVersion{}, biz.ErrInputNotFound
	}
	if err != nil {
		return biz.InputVersion{}, biz.ErrPersistence
	}
	return versionFromRow(row)
}

func databaseID(value string) (pgtype.UUID, error) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return pgtype.UUID{}, biz.ErrInvalidInput
	}
	var id pgtype.UUID
	if id.Scan(value) != nil || !id.Valid || id.Bytes == [16]byte{} {
		return pgtype.UUID{}, biz.ErrInvalidInput
	}
	return id, nil
}

func versionFromRow(row inputsql.ModeldevInputVersion) (biz.InputVersion, error) {
	if !row.TenantID.Valid || !row.InputVersionID.Valid || !row.RequestID.Valid || !row.RequestedAt.Valid || row.RequestedAt.InfinityModifier != pgtype.Finite {
		return biz.InputVersion{}, biz.ErrPersistence
	}
	request := biz.InputImport{
		Format:   row.Format,
		TenantID: row.TenantID.String(), RequestID: row.RequestID.String(), InputVersionID: row.InputVersionID.String(), Actor: row.Actor, RequestedAt: row.RequestedAt.Time.UTC(),
		Scope:  cpup01.StorageScope{StorageConnectionID: row.StorageConnectionID, Bucket: row.Bucket, ApprovedPrefix: row.ApprovedPrefix, CredentialReference: row.CredentialReference},
		Object: cpup01.FixedObjectRef{StorageConnectionID: row.StorageConnectionID, Bucket: row.Bucket, Key: row.ObjectKey, VersionID: &row.ObjectVersionID, SizeBytes: row.SizeBytes, SHA256: row.Sha256},
	}
	if request.Validate() != nil {
		return biz.InputVersion{}, biz.ErrPersistence
	}
	version := biz.InputVersion{Import: request, State: biz.InputState(row.State)}
	if row.FailureCode.Valid != row.FailureObservedAt.Valid {
		return biz.InputVersion{}, biz.ErrPersistence
	}
	if row.FailureCode.Valid {
		failure := biz.InputValidationFailure{Code: biz.InputFailureCode(row.FailureCode.String), ObservedAt: row.FailureObservedAt.Time.UTC()}
		if row.FailureObservedAt.InfinityModifier != pgtype.Finite || failure.ValidateFor(request) != nil {
			return biz.InputVersion{}, biz.ErrPersistence
		}
		version.Failure = &failure
	}
	switch version.State {
	case biz.InputStateValidating, biz.InputStateRejected:
		if row.VerifiedAt.Valid || row.VerifiedSchemaVersion.Valid || row.VerifiedRowCount.Valid || row.VerifiedFeatureCount.Valid || row.PreparedFormat.Valid || row.PreparedTransformID.Valid || row.PreparedSizeBytes.Valid || row.PreparedSha256.Valid || row.PreparedRowCount.Valid {
			return biz.InputVersion{}, biz.ErrPersistence
		}
		if version.State == biz.InputStateRejected && (version.Failure == nil || version.Failure.Code != biz.InputFailureContentRejected) {
			return biz.InputVersion{}, biz.ErrPersistence
		}
		if version.State == biz.InputStateValidating && version.Failure != nil && version.Failure.Code != biz.InputFailureSourceUnavailable {
			return biz.InputVersion{}, biz.ErrPersistence
		}
	case biz.InputStateReady:
		if version.Failure != nil || !row.VerifiedAt.Valid || row.VerifiedAt.InfinityModifier != pgtype.Finite || !row.VerifiedSchemaVersion.Valid || !row.VerifiedRowCount.Valid || !row.VerifiedFeatureCount.Valid || !row.PreparedFormat.Valid || !row.PreparedTransformID.Valid || !row.PreparedSizeBytes.Valid || !row.PreparedSha256.Valid || !row.PreparedRowCount.Valid {
			return biz.InputVersion{}, biz.ErrPersistence
		}
		prepared := cpup01.PreparedDatasetFacts{Format: row.PreparedFormat.String, TransformID: row.PreparedTransformID.String, SizeBytes: row.PreparedSizeBytes.Int64, SHA256: row.PreparedSha256.String, RowCount: row.PreparedRowCount.Int64}
		if request.InputFormat() == "JSON" {
			proof := biz.VerifiedAlpaca{
				VerifiedObject: biz.VerifiedObject{Object: request.Object, VerifiedAt: row.VerifiedAt.Time.UTC()},
				SchemaVersion:  row.VerifiedSchemaVersion.String, RowCount: row.VerifiedRowCount.Int64, PreparedDataset: prepared,
			}
			if row.VerifiedFeatureCount.Int32 != 0 || row.VerifiedRowCount.Int64 <= 0 || proof.ValidateFor(request) != nil {
				return biz.InputVersion{}, biz.ErrPersistence
			}
			version.AlpacaVerification = &proof
			break
		}
		proof := biz.VerifiedCSV{
			VerifiedObject: biz.VerifiedObject{Object: request.Object, VerifiedAt: row.VerifiedAt.Time.UTC()},
			SchemaVersion:  row.VerifiedSchemaVersion.String, RowCount: row.VerifiedRowCount.Int64, FeatureCount: uint32(row.VerifiedFeatureCount.Int32), PreparedDataset: prepared,
		}
		if proof.ValidateFor(request) != nil {
			return biz.InputVersion{}, biz.ErrPersistence
		}
		version.Verification = &proof
	default:
		return biz.InputVersion{}, biz.ErrPersistence
	}
	return version, nil
}

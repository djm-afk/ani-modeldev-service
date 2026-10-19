package biz

import (
	"context"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

const AlpacaSchemaVersion = "ani.finetune.alpaca.v1"

// VerifiedAlpaca records observations of the exact original JSON bytes. The
// current Release's 64-row constraint belongs to admission, not this importer.
type VerifiedAlpaca struct {
	VerifiedObject
	SchemaVersion string
	RowCount      uint32
}

func (v VerifiedAlpaca) ValidateFor(r InputImport) error {
	o, want := v.Object, r.Object
	t := v.VerifiedAt.UTC()
	if r.Validate() != nil || r.InputFormat() != "JSON" || v.SchemaVersion != AlpacaSchemaVersion || v.RowCount == 0 ||
		t.IsZero() || t.Year() < 1 || t.Year() > 9999 || t.Before(r.RequestedAt) ||
		o.StorageConnectionID != want.StorageConnectionID || o.Bucket != want.Bucket || o.Key != want.Key ||
		o.VersionID == nil || want.VersionID == nil || *o.VersionID != *want.VersionID || o.ImmutableCopy != nil ||
		o.SizeBytes != want.SizeBytes || o.SHA256 != want.SHA256 {
		return ErrInputVerification
	}
	return nil
}

type AlpacaVerifier interface {
	VerifyAlpaca(context.Context, cpup01.StorageScope, cpup01.FixedObjectRef) (VerifiedAlpaca, error)
}

type FinetuningInputRepository interface {
	ManagedInputRepository
	RecordVerifiedAlpaca(context.Context, InputImport, VerifiedAlpaca) (InputVersion, error)
}

// ImportFinetuningDataset uses the same durable freeze/failure/READY protocol
// as CSV. It is called only by an authenticated managed import or the explicitly
// isolated application-layer integration assembly.
func (i *InputImporter) ImportFinetuningDataset(ctx context.Context, r InputImport) (InputVersion, error) {
	if err := ctx.Err(); err != nil {
		return InputVersion{}, err
	}
	if r.Validate() != nil || r.InputFormat() != "JSON" {
		return InputVersion{}, ErrInvalidInput
	}
	if i == nil {
		return InputVersion{}, ErrPersistence
	}
	repo, ok := i.repository.(FinetuningInputRepository)
	if !ok {
		return InputVersion{}, ErrPersistence
	}
	verifier, ok := i.verifier.(AlpacaVerifier)
	if !ok {
		return InputVersion{}, ErrPersistence
	}
	frozen, err := repo.FreezeImport(ctx, r)
	if err != nil {
		return InputVersion{}, err
	}
	switch frozen.State {
	case InputStateReady:
		return frozen, nil
	case InputStateRejected:
		return frozen, ErrInputVerification
	case InputStateValidating:
	default:
		return InputVersion{}, ErrPersistence
	}
	proof, err := verifier.VerifyAlpaca(ctx, frozen.Import.Scope, frozen.Import.Object)
	if err != nil {
		return i.recordFailure(ctx, frozen, err)
	}
	if err := proof.ValidateFor(frozen.Import); err != nil {
		return i.recordFailure(ctx, frozen, err)
	}
	return repo.RecordVerifiedAlpaca(ctx, frozen.Import, proof)
}

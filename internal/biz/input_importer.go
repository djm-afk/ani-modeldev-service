package biz

import (
	"context"
	"errors"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// ManagedInputRepository commits the immutable request and its verified state.
type ManagedInputRepository interface {
	FreezeImport(context.Context, InputImport) (InputVersion, error)
	RecordVerifiedCSV(context.Context, InputImport, VerifiedCSV) (InputVersion, error)
	RecordValidationFailure(context.Context, InputImport, InputValidationFailure) (InputVersion, error)
}

type CSVVerifier interface {
	VerifyCSV(context.Context, cpup01.StorageScope, cpup01.FixedObjectRef) (VerifiedCSV, error)
}

// InputImporter runs only after the managed entry point authenticates the
// current administrator and authorizes the target tenant and source scope.
// The request carries an explicit fixed version, never an arbitrary endpoint.
type InputImporter struct {
	repository ManagedInputRepository
	verifier   CSVVerifier
}

func NewInputImporter(repository ManagedInputRepository, verifier CSVVerifier) *InputImporter {
	return &InputImporter{repository: repository, verifier: verifier}
}

func (importer *InputImporter) ImportCSV(ctx context.Context, request InputImport) (InputVersion, error) {
	if err := ctx.Err(); err != nil {
		return InputVersion{}, err
	}
	if err := request.Validate(); err != nil {
		return InputVersion{}, err
	}
	if request.InputFormat() != "CSV" {
		return InputVersion{}, ErrInvalidInput
	}
	if importer == nil || importer.repository == nil || importer.verifier == nil {
		return InputVersion{}, ErrPersistence
	}
	frozen, err := importer.repository.FreezeImport(ctx, request)
	if err != nil {
		return InputVersion{}, err
	}
	if frozen.State == InputStateReady {
		return frozen, nil
	}
	if frozen.State == InputStateRejected {
		return frozen, ErrInputVerification
	}
	if frozen.State != InputStateValidating {
		return InputVersion{}, ErrPersistence
	}
	// The receipt has committed before this network call. Use its original
	// version and approved scope, including when resuming after a process exit.
	verified, err := importer.verifier.VerifyCSV(ctx, frozen.Import.Scope, frozen.Import.Object)
	if err != nil {
		return importer.recordFailure(ctx, frozen, err)
	}
	if err := verified.ValidateFor(frozen.Import); err != nil {
		return importer.recordFailure(ctx, frozen, err)
	}
	return importer.repository.RecordVerifiedCSV(ctx, frozen.Import, verified)
}

func (importer *InputImporter) recordFailure(ctx context.Context, frozen InputVersion, cause error) (InputVersion, error) {
	if err := ctx.Err(); err != nil {
		return frozen, err
	}
	if errors.Is(cause, context.Canceled) {
		return frozen, context.Canceled
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return frozen, context.DeadlineExceeded
	}
	// Unknown failures establish no bad-content fact. Never retain the external
	// error text, and classify source failures before their compatibility parent.
	code := InputFailureSourceUnavailable
	if errors.Is(cause, ErrInputVerification) && !errors.Is(cause, ErrInputSourceUnavailable) {
		code = InputFailureContentRejected
	}
	version, err := importer.repository.RecordValidationFailure(ctx, frozen.Import, InputValidationFailure{Code: code, ObservedAt: time.Now().UTC()})
	if err != nil {
		return InputVersion{}, err
	}
	switch version.State {
	case InputStateReady:
		// A concurrent successful verifier may have committed first.
		return version, nil
	case InputStateRejected:
		return version, ErrInputVerification
	case InputStateValidating:
		return version, ErrInputSourceUnavailable
	default:
		return InputVersion{}, ErrPersistence
	}
}

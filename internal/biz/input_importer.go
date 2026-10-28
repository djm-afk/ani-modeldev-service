package biz

import (
	"context"
	"errors"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// ManagedInputRepository commits the immutable request and its verified state.
type ManagedInputRepository interface {
	FindImport(context.Context, InputImport) (InputVersion, error)
	FreezeImport(context.Context, InputImport) (InputVersion, error)
	AcquireValidationLock(context.Context, string, string) (func(), error)
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
	limits     cpup01.ContentLimits
	timeout    time.Duration
}

var ErrInputValidationBusy = errors.New("INPUT_VALIDATION_BUSY")

var inputValidationSlot = make(chan struct{}, 1)

func NewInputImporter(repository ManagedInputRepository, verifier CSVVerifier, limits cpup01.ContentLimits, timeout time.Duration) *InputImporter {
	return &InputImporter{repository: repository, verifier: verifier, limits: limits, timeout: timeout}
}

func (importer *InputImporter) beginImport(ctx context.Context, request InputImport) (InputVersion, func(), error) {
	if importer == nil || importer.repository == nil || importer.verifier == nil || importer.limits.Validate() != nil || importer.timeout <= 0 {
		return InputVersion{}, nil, ErrPersistence
	}
	existing, err := importer.repository.FindImport(ctx, request)
	if errors.Is(err, ErrInputNotFound) {
		if err := importer.limits.Check(cpup01.ContentDataset, request.Object.SizeBytes); err != nil {
			return InputVersion{}, nil, err
		}
	} else if err != nil {
		return InputVersion{}, nil, err
	} else if existing.State == InputStateReady || existing.State == InputStateRejected {
		// Compare the exact original intent before a READY replay, without
		// taking content capacity or reapplying the current content policy.
		frozen, err := importer.repository.FreezeImport(ctx, request)
		return frozen, nil, err
	}
	release, err := importer.lockValidation(ctx, request)
	if err != nil {
		return InputVersion{}, nil, err
	}
	// Freeze still compares the complete intent for both existing and new keys.
	frozen, err := importer.repository.FreezeImport(ctx, request)
	if err != nil {
		release()
		return InputVersion{}, nil, err
	}
	return frozen, release, nil
}

func (importer *InputImporter) lockValidation(ctx context.Context, request InputImport) (func(), error) {
	select {
	case inputValidationSlot <- struct{}{}:
	default:
		return nil, ErrInputValidationBusy
	}
	release, err := importer.repository.AcquireValidationLock(ctx, request.TenantID, request.InputVersionID)
	if err != nil || release == nil {
		<-inputValidationSlot
		if err == nil {
			err = ErrPersistence
		}
		return nil, err
	}
	return func() { release(); <-inputValidationSlot }, nil
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
	if importer == nil || importer.timeout <= 0 {
		return InputVersion{}, ErrPersistence
	}
	ctx, cancel := context.WithTimeout(ctx, importer.timeout)
	defer cancel()
	frozen, release, err := importer.beginImport(ctx, request)
	if err != nil {
		return InputVersion{}, err
	}
	if release != nil {
		defer release()
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
	var structure *cpup01.LimitViolation
	if errors.As(cause, &structure) || errors.Is(cause, ErrInputVerification) && !errors.Is(cause, ErrInputSourceUnavailable) {
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
		if structure != nil {
			return version, cause
		}
		return version, ErrInputVerification
	case InputStateValidating:
		return version, ErrInputSourceUnavailable
	default:
		return InputVersion{}, ErrPersistence
	}
}

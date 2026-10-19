package biz

import (
	"errors"
	"strings"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

var ErrInputVerification = errors.New("INPUT_VERIFICATION_FAILED")

var (
	ErrInvalidInput  = errors.New("INVALID_INPUT")
	ErrInputConflict = errors.New("INPUT_IMPORT_CONFLICT")
	ErrInputNotFound = errors.New("INPUT_VERSION_NOT_FOUND")
)

// VerifiedCSV combines actual immutable object bytes with the registered CPU
// input shape. It is an observation, not a persisted READY input or access grant.
type VerifiedCSV struct {
	VerifiedObject
	SchemaVersion string
	RowCount      uint32
	FeatureCount  uint32
}

// ValidateFor binds a trusted byte observation to the already frozen request.
// It does not obtain the observation or establish the caller's authority.
func (verified VerifiedCSV) ValidateFor(request InputImport) error {
	object, expected := verified.Object, request.Object
	when := verified.VerifiedAt.UTC()
	if request.Validate() != nil || request.InputFormat() != "CSV" || when.IsZero() || when.Year() < 1 || when.Year() > 9999 || when.Before(request.RequestedAt) ||
		verified.SchemaVersion != "ani.cpu.csv.v1" || verified.RowCount != 1024 || verified.FeatureCount != 16 ||
		object.StorageConnectionID != expected.StorageConnectionID || object.Bucket != expected.Bucket || object.Key != expected.Key ||
		object.VersionID == nil || expected.VersionID == nil || *object.VersionID != *expected.VersionID || object.ImmutableCopy != nil ||
		object.SizeBytes != expected.SizeBytes || object.SHA256 != expected.SHA256 {
		return ErrInputVerification
	}
	return nil
}

// InputImport is a trusted request fixed before any remote validation. Tenant,
// actor and storage scope must come from the managed import authorization path.
// Neither a request nor its durable receipt establishes a READY input.
type InputImport struct {
	Format         string
	TenantID       string
	RequestID      string
	InputVersionID string
	Actor          string
	RequestedAt    time.Time
	Scope          cpup01.StorageScope
	Object         cpup01.FixedObjectRef
}

type InputState string

const (
	InputStateValidating InputState = "VALIDATING"
	InputStateReady      InputState = "READY"
	InputStateRejected   InputState = "REJECTED"
)

// InputValidationFailure stores only a finite reason for the original fixed
// object. It never includes storage response text, credentials or signed URLs.
type InputValidationFailure struct {
	Code       InputFailureCode
	ObservedAt time.Time
}

type InputFailureCode string

const (
	InputFailureContentRejected   InputFailureCode = "CONTENT_REJECTED"
	InputFailureSourceUnavailable InputFailureCode = "SOURCE_UNAVAILABLE"
)

func (failure InputValidationFailure) ValidateFor(request InputImport) error {
	when := failure.ObservedAt.UTC()
	if request.Validate() != nil || when.IsZero() || when.Year() < 1 || when.Year() > 9999 || when.Before(request.RequestedAt) ||
		(failure.Code != InputFailureContentRejected && failure.Code != InputFailureSourceUnavailable) {
		return ErrInputVerification
	}
	return nil
}

type InputVersion struct {
	Import             InputImport
	State              InputState
	Verification       *VerifiedCSV
	AlpacaVerification *VerifiedAlpaca
	Failure            *InputValidationFailure
}

// Validate checks the immutable request's structure, not source permissions or
// remote content. An explicit object version is required before validation starts.
func (request InputImport) Validate() error {
	if request.InputFormat() != "CSV" && request.InputFormat() != "JSON" {
		return ErrInvalidInput
	}
	if !validAdmissionID(request.TenantID) || !validAdmissionID(request.RequestID) || !validAdmissionID(request.InputVersionID) || !validAuditActor(request.Actor) {
		return ErrInvalidInput
	}
	when := request.RequestedAt.UTC()
	if when.IsZero() || when.Year() < 1 || when.Year() > 9999 || when.Nanosecond()%1000 != 0 {
		return ErrInvalidInput
	}
	object, scope := request.Object, request.Scope
	if scope.CredentialReference != "" && !ValidStorageReference(scope.CredentialReference) {
		return ErrInvalidInput
	}
	if !ValidStorageReference(scope.StorageConnectionID) || !ValidStorageReference(scope.Bucket) || object.StorageConnectionID != scope.StorageConnectionID || object.Bucket != scope.Bucket || !ValidStorageKey(scope.ApprovedPrefix) || !ValidStorageKey(object.Key) || !strings.HasPrefix(object.Key, scope.ApprovedPrefix+"/") {
		return ErrInvalidInput
	}
	if object.VersionID == nil || object.ImmutableCopy != nil || !ValidStorageReference(*object.VersionID) || strings.EqualFold(*object.VersionID, "null") || object.SizeBytes <= 0 || object.SizeBytes > 32*1024*1024 || !closeSpecHashPattern.MatchString(object.SHA256) {
		return ErrInvalidInput
	}
	return nil
}

// Empty format retains the original CSV import contract for existing callers.
func (request InputImport) InputFormat() string {
	if request.Format == "" {
		return "CSV"
	}
	return request.Format
}

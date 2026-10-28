package cpup01

import (
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// AdmissionEnvelope binds authenticated command identities to the immutable
// intent and resolved snapshot. Both producers and consumers validate this
// value; it contains no persistence, transport, or current-default resolution.
type AdmissionEnvelope struct {
	TenantID    string
	Actor       string
	OperationID string
	ExecutionID string
	Intent      Intent
	IntentHash  string
	Snapshot    Snapshot
	SpecHash    string
	AcceptedAt  time.Time
}

// CanonicalPayloads validates identity, hashes, and intent-to-snapshot coherence
// and returns the two exact canonical payloads. It never reads the wall clock.
func (a AdmissionEnvelope) CanonicalPayloads() ([]byte, []byte, error) {
	if !validUUID(a.TenantID) || !validUUID(a.OperationID) || !validUUID(a.ExecutionID) || !ValidAuditActor(a.Actor) {
		return nil, nil, ErrInvalidArgument
	}
	acceptedAt := a.AcceptedAt.UTC()
	if acceptedAt.IsZero() || acceptedAt.Year() < 1 || acceptedAt.Year() > 9999 || acceptedAt.Nanosecond()%1000 != 0 || !acceptedAt.Before(a.Snapshot.DeadlineAt) {
		return nil, nil, ErrInvalidArgument
	}
	intent, intentHash, err := CanonicalIntent(a.Intent)
	if err != nil || intentHash != a.IntentHash {
		return nil, nil, ErrInvalidArgument
	}
	snapshot, err := a.Snapshot.Canonical()
	if err != nil {
		return nil, nil, ErrInvalidArgument
	}
	digest := sha256.Sum256(snapshot)
	if hex.EncodeToString(digest[:]) != a.SpecHash {
		return nil, nil, ErrInvalidArgument
	}
	identity := a.Snapshot.IdentityRef
	if !strings.EqualFold(identity.TenantID, a.TenantID) || identity.Actor != a.Actor || !strings.EqualFold(identity.ExecutionID, a.ExecutionID) || !strings.EqualFold(identity.OperationID, a.OperationID) || identity.IntentHash != a.IntentHash || !a.AcceptedAt.Equal(a.Snapshot.Timing.AcceptedAt) || a.Intent.Kind != a.Snapshot.Kind || a.Intent.ImplementationID != a.Snapshot.Implementation.ImplementationID || !strings.EqualFold(a.Intent.ComputePresetID, a.Snapshot.ComputeOrigin.ComputePresetID) || !strings.EqualFold(a.Intent.DatasetVersionID, a.Snapshot.Input.InputVersionID) {
		return nil, nil, ErrInvalidArgument
	}
	explicit, err := a.Intent.ExplicitParameters()
	if err != nil {
		return nil, nil, err
	}
	for _, p := range explicit {
		matched := false
		for _, r := range a.Snapshot.resolvedParameters() {
			if p.Name == r.Name && p.Type == r.Type {
				left, lok := new(big.Rat).SetString(p.Value)
				right, rok := new(big.Rat).SetString(r.Value)
				matched = lok && rok && left.Cmp(right) == 0
				break
			}
		}
		if !matched {
			return nil, nil, ErrInvalidArgument
		}
	}
	if a.Intent.TimeoutSeconds != nil && *a.Intent.TimeoutSeconds != a.Snapshot.Timing.ExecutionTimeoutSeconds {
		return nil, nil, ErrInvalidArgument
	}
	// The training image selection must reach the snapshot unchanged: an
	// accepted image intent can never execute a different image. An absent user
	// selection is legal and must still be backed by a resolved snapshot image.
	if a.Intent.Kind == "GENERAL_TRAINING" {
		program := a.Snapshot.TrainingProgram
		if program == nil || (a.Intent.TrainingImageID == nil) != (a.Intent.TrainingImageScope == nil) {
			return nil, nil, ErrInvalidArgument
		}
		if a.Intent.TrainingImageID != nil && (!strings.EqualFold(*a.Intent.TrainingImageID, program.Image.ImageID) || !strings.EqualFold(*a.Intent.TrainingImageScope, program.Image.Scope)) {
			return nil, nil, ErrInvalidArgument
		}
	}
	if a.Intent.Kind == "FINETUNING" {
		fine := a.Snapshot.Finetuning
		if fine == nil || a.Intent.Finetuning == nil || a.Intent.BaseModelVersionID == nil || !strings.EqualFold(*a.Intent.BaseModelVersionID, fine.BaseModel.VersionID) || !sameOptionalUUID(a.Intent.TargetModelID, fine.TargetModelID) || !sameOptionalString(a.Intent.NewModelName, fine.NewModelName) {
			return nil, nil, ErrInvalidArgument
		}
	}
	return intent, snapshot, nil
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
func sameOptionalUUID(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return strings.EqualFold(*left, *right)
}

// ValidAuditActor validates an immutable audit label, not current authorization.
// Admission and close commands share this bounded, unambiguous string format.
func ValidAuditActor(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

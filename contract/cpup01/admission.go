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
	if a.Intent.Kind != a.Snapshot.Kind || !strings.EqualFold(a.Intent.PresetID, a.Snapshot.Release.PresetID) || !strings.EqualFold(a.Intent.DatasetVersionID, a.Snapshot.Input.InputVersionID) {
		return nil, nil, ErrInvalidArgument
	}
	if a.Intent.ImageVersionID != nil && !strings.EqualFold(*a.Intent.ImageVersionID, a.Snapshot.Program.ImageVersionID) {
		return nil, nil, ErrInvalidArgument
	}
	if a.Intent.GeneralParameters != nil {
		for _, parameter := range *a.Intent.GeneralParameters {
			matched := false
			for _, resolved := range a.Snapshot.Program.ResolvedParameters {
				if parameter.Name == resolved.Name && parameter.Type == resolved.Type {
					left, leftOK := new(big.Rat).SetString(parameter.Value)
					right, rightOK := new(big.Rat).SetString(resolved.Value)
					matched = leftOK && rightOK && left.Cmp(right) == 0
					break
				}
			}
			if !matched {
				return nil, nil, ErrInvalidArgument
			}
		}
	}
	if a.Intent.Kind == "FINETUNING" {
		fine := a.Snapshot.Finetuning
		if fine == nil || a.Intent.Finetuning == nil || a.Intent.BaseModelVersionID == nil || !strings.EqualFold(*a.Intent.BaseModelVersionID, fine.BaseModel.VersionID) || !sameOptionalUUID(a.Intent.TargetModelID, fine.TargetModelID) || !sameOptionalString(a.Intent.NewModelName, fine.NewModelName) {
			return nil, nil, ErrInvalidArgument
		}
		for _, parameter := range a.Snapshot.Program.ResolvedParameters {
			switch parameter.Name {
			case "learning_rate":
				if a.Intent.Finetuning.LearningRate != nil && canonicalDecimal(*a.Intent.Finetuning.LearningRate) != canonicalDecimal(parameter.Value) {
					return nil, nil, ErrInvalidArgument
				}
			case "max_steps":
				if a.Intent.Finetuning.MaxSteps != nil && new(big.Int).SetInt64(*a.Intent.Finetuning.MaxSteps).String() != parameter.Value {
					return nil, nil, ErrInvalidArgument
				}
			}
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

package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func registrationContent(current *runtimeTransaction) (biz.FinetuningRegistration, error) {
	fine := current.execution.Snapshot.Finetuning
	publication := current.state.Publication
	if current.execution.Snapshot.Kind != "FINETUNING" || fine == nil || publication == nil || publication.Bundle == nil {
		return biz.FinetuningRegistration{}, biz.ErrRuntimeNotReady
	}
	export, verify := stepForRole(current.state, "export"), stepForRole(current.state, "verify")
	if export == nil || verify == nil || export.Result == nil || verify.Result == nil || export.Result.ExitCode != 0 || verify.Result.ExitCode != 0 {
		return biz.FinetuningRegistration{}, biz.ErrRuntimeNotReady
	}
	id := current.execution.ExecutionID
	intent := biz.FinetuningRegistration{PublicationID: publication.ID, ContainerKey: "md-container:" + id, VersionKey: "md-" + id, VersionTag: "exec-" + id, TargetModelID: fine.TargetModelID, NewModelName: fine.NewModelName, BaseModelVersionID: fine.BaseModel.VersionID, Bundle: *publication.Bundle, VerifyManifestSHA256: verify.Result.ManifestSHA256}
	raw, err := json.Marshal(intent)
	if err != nil {
		return biz.FinetuningRegistration{}, biz.ErrPersistence
	}
	h := sha256.Sum256(raw)
	intent.ContentSHA256 = hex.EncodeToString(h[:])
	return intent, nil
}

func (r *Repository) RecordRegistrationUnavailable(ctx context.Context, a biz.RunAuthorityCandidate) (biz.ExecutionRuntime, bool, error) {
	return r.mutate(ctx, a, func(current *runtimeTransaction) (bool, error) {
		content, err := registrationContent(current)
		if err != nil {
			return false, err
		}
		if existing := current.state.FinetuningRegistration; existing != nil {
			// Unavailability is the provider boundary for the whole intent. It
			// cannot be replayed over an intent that already recorded a permit,
			// and it never carries any in-flight dispatch fact.
			if existing.State != biz.RegistrationStateUnavailable || len(existing.Dispatches) != 0 {
				return false, biz.ErrRuntimeConflict
			}
			content.RecordedAt, content.DeadlineAt = existing.RecordedAt, existing.DeadlineAt
			content.State, content.FailureCode = existing.State, existing.FailureCode
			if !registrationIntentUnchanged(content, *existing) {
				return false, biz.ErrRuntimeConflict
			}
			return false, nil
		}
		if err := requireOpen(current); err != nil {
			return false, err
		}
		content.RecordedAt = current.now
		content.DeadlineAt = registrationDeadline(current, current.now)
		content.State = biz.RegistrationStateUnavailable
		content.FailureCode = "MODEL_DEPENDENCY_UNAVAILABLE"
		current.state.FinetuningRegistration = &content
		return true, nil
	})
}

func validRegistrationFacts(current *runtimeTransaction) bool {
	stored := current.state.FinetuningRegistration
	if stored == nil {
		return true
	}
	expected, err := registrationContent(current)
	if err != nil {
		return false
	}
	if stored.RecordedAt.IsZero() || !stored.DeadlineAt.After(stored.RecordedAt) || stored.RecordedAt.After(current.now) ||
		!stored.DeadlineAt.Equal(registrationDeadline(current, stored.RecordedAt)) {
		return false
	}
	return validRegistrationDispatches(current, *stored) && registrationIntentUnchanged(expected, *stored)
}

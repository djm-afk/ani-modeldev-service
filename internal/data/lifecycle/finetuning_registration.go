package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"time"

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
			content.RecordedAt, content.DeadlineAt = existing.RecordedAt, existing.DeadlineAt
			content.State, content.FailureCode = existing.State, existing.FailureCode
			if !reflect.DeepEqual(content, *existing) {
				return false, biz.ErrRuntimeConflict
			}
			return false, nil
		}
		if err := requireOpen(current); err != nil {
			return false, err
		}
		content.RecordedAt = current.now
		content.DeadlineAt = current.now.Add(time.Duration(current.execution.Snapshot.Finetuning.Budgets.RPCTimeoutSeconds) * time.Second)
		if current.execution.Snapshot.DeadlineAt.Before(content.DeadlineAt) {
			content.DeadlineAt = current.execution.Snapshot.DeadlineAt
		}
		content.State = "DEPENDENCY_UNAVAILABLE"
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
	if stored.State != "DEPENDENCY_UNAVAILABLE" || stored.FailureCode != "MODEL_DEPENDENCY_UNAVAILABLE" || stored.RecordedAt.IsZero() || !stored.DeadlineAt.After(stored.RecordedAt) || stored.RecordedAt.After(current.now) {
		return false
	}
	deadline := stored.RecordedAt.Add(time.Duration(current.execution.Snapshot.Finetuning.Budgets.RPCTimeoutSeconds) * time.Second)
	if current.execution.Snapshot.DeadlineAt.Before(deadline) {
		deadline = current.execution.Snapshot.DeadlineAt
	}
	if !stored.DeadlineAt.Equal(deadline) {
		return false
	}
	expected.RecordedAt, expected.DeadlineAt = stored.RecordedAt, stored.DeadlineAt
	expected.State, expected.FailureCode = stored.State, stored.FailureCode
	return reflect.DeepEqual(expected, *stored)
}

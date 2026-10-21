package lifecycle_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
)

// This verifies the real persistence boundary with explicitly synthetic remote
// publication/Pod observations. Real publication proof belongs to CORE_FLOW.
func TestFinetuningMissingProviderRequiresPublicationAndKeepsOriginalIntent(t *testing.T) {
	open, admission, authority, binding := finetuningRuntimeFixture(t, time.Hour)
	ctx := context.Background()
	repository := lifecycle.New(open())
	if _, _, err := repository.RecordRegistrationUnavailable(ctx, authority); !errors.Is(err, biz.ErrRuntimeNotReady) {
		t.Fatal("registration preceded publication", err)
	}
	for _, role := range []string{"export", "verify"} {
		binding.Role = role
		binding.TaskID = role + "-task"
		binding.PodName = role + "-pod"
		binding.PodUID = role + "-pod-uid"
		start, err := repository.ReserveFinetuningStep(ctx, authority, binding)
		if err != nil || !start.SendPermit {
			t.Fatal(err)
		}
		var now time.Time
		if err := open().QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
			t.Fatal(err)
		}
		manifest := "reports/export/merged_manifest.json"
		if role == "verify" {
			manifest = "reports/verify/verify.json"
		}
		result := biz.FinetuningStepResult{StartedAt: start.Start.AdmittedAt, FinishedAt: now.UTC(), ExitCode: 0, ManifestRelativePath: manifest, ManifestBytes: 128, ManifestSHA256: strings.Repeat("a", 64)}
		if _, _, err := repository.RecordFinetuningStepResult(ctx, authority, binding, result); err != nil {
			t.Fatal(err)
		}
	}
	publication := publicationFixture(t, admission, authority)
	if _, _, err := recordPublicationV21(t, repository, ctx, authority, publication); err != nil {
		t.Fatal(err)
	}
	first, replayed, err := repository.RecordRegistrationUnavailable(ctx, authority)
	if err != nil || replayed || first.FinetuningRegistration == nil {
		t.Fatal(err)
	}
	original := *first.FinetuningRegistration
	if original.State != "DEPENDENCY_UNAVAILABLE" || original.FailureCode != "MODEL_DEPENDENCY_UNAVAILABLE" || original.PublicationID != publication.ID || original.VersionKey != "md-"+admission.ExecutionID || original.VersionTag != "exec-"+admission.ExecutionID || original.ContentSHA256 == "" {
		t.Fatal("missing original recoverable content")
	}
	if err := applyFinetuningStop(ctx, open(), admission); err != nil {
		t.Fatal(err)
	}
	replayedState, replayed, err := lifecycle.New(open()).RecordRegistrationUnavailable(ctx, authority)
	if err != nil || !replayed || !reflect.DeepEqual(original, *replayedState.FinetuningRegistration) {
		t.Fatal("reconnect/Stop renewed or rewrote original registration", err)
	}
}

func TestFinetuningStopCannotCreateRegistrationIntent(t *testing.T) {
	open, admission, authority, _ := finetuningRuntimeFixture(t, time.Hour)
	ctx := context.Background()
	if err := applyFinetuningStop(ctx, open(), admission); err != nil {
		t.Fatal(err)
	}
	repository := lifecycle.New(open())
	if _, _, err := repository.RecordRegistrationUnavailable(ctx, authority); err == nil {
		t.Fatal("closing execution acquired registration")
	}
	state, err := repository.GetRuntime(ctx, admission.TenantID, admission.ExecutionID)
	if err != nil || state.FinetuningRegistration != nil {
		t.Fatal("closing execution persisted a new registration", err)
	}
}

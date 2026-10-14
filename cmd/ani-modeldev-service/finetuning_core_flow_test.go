//go:build finetuning_core_flow

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/admissionfacts"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/catalogue"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"google.golang.org/protobuf/encoding/protojson"
)

// This executable exists only in an explicitly tagged test binary. It replaces
// the upstream delivery and read-only Model provider boundaries, while the
// production app owns dispatch, workload authentication, all steps and close.
type coreFlowFixture struct {
	Schema              string              `json:"schema"`
	Bootstrap           json.RawMessage     `json:"bootstrap"`
	Scenario            string              `json:"scenario"`
	TenantID            string              `json:"tenant_id"`
	Actor               string              `json:"actor"`
	OperationID         string              `json:"operation_id"`
	ExecutionID         string              `json:"execution_id"`
	Intent              json.RawMessage     `json:"intent"`
	ReleaseID           string              `json:"release_id"`
	ReleaseDigest       string              `json:"release_digest"`
	CanonicalRelease    json.RawMessage     `json:"canonical_release"`
	Input               biz.InputImport     `json:"input"`
	BaseModel           cpup01.BaseModelRef `json:"base_model"`
	AcceptedAt          time.Time           `json:"accepted_at"`
	StopWhenTraining    bool                `json:"stop_when_training"`
	RestartWhenTraining bool                `json:"restart_when_training"`
	ExpectDeadline      bool                `json:"expect_deadline"`
	EvidenceDirectory   string              `json:"evidence_directory"`
	SourceCommit        string              `json:"source_commit"`
	MaterialSource      string              `json:"material_source"`
}

type coreFlowModelFixture struct {
	tenant string
	base   cpup01.BaseModelRef
}

func (f coreFlowModelFixture) ResolveFinetuningModelFacts(ctx context.Context, tenant string, intent cpup01.Intent, release cpup01.ReleaseDocument) (cpup01.BaseModelRef, error) {
	if err := ctx.Err(); err != nil {
		return cpup01.BaseModelRef{}, err
	}
	if tenant != f.tenant || intent.BaseModelVersionID == nil || *intent.BaseModelVersionID != f.base.VersionID || release.Finetuning == nil || release.Finetuning.BaseModelVersionID != f.base.VersionID {
		return cpup01.BaseModelRef{}, biz.ErrNoCompatibleRelease
	}
	return f.base, nil
}

func TestFinetuningCoreFlow(t *testing.T) {
	fixturePath := os.Getenv("ANI_FINETUNING_CORE_FLOW_FIXTURE")
	if fixturePath == "" {
		t.Skip("explicit task-owned integration fixture required")
	}
	raw, err := readCommandMaterial(fixturePath, 1<<20)
	if err != nil {
		t.Fatal("integration fixture unavailable")
	}
	var f coreFlowFixture
	if decodeRuntimeMaterial(raw, &f) != nil || f.Schema != "ani.finetuning.core-flow-fixture.v1" || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(f.SourceCommit) || f.SourceCommit != Version || !regexp.MustCompile(`^[a-z0-9-]{1,48}$`).MatchString(f.Scenario) || f.Actor != "core-flow:test-driver" || f.EvidenceDirectory != "/evidence" {
		t.Fatal("invalid isolated integration fixture or binary source identity")
	}
	var config conf.Bootstrap
	if protojson.Unmarshal(f.Bootstrap, &config) != nil || config.Validate() != nil || config.Command == nil || config.Runtime == nil {
		t.Fatal("invalid production bootstrap")
	}
	if config.Command.AdmissionResolution == nil {
		t.Fatal("managed facts and immutable catalogue required")
	}
	if err := os.MkdirAll(config.Command.AdmissionResolution.CatalogueDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
	defer cancel()
	if _, err := catalogue.ImportRelease(ctx, config.Command.AdmissionResolution.CatalogueDirectory, f.CanonicalRelease, f.ReleaseDigest); err != nil {
		t.Fatal("formal Release import", err)
	}
	clients, err := loadRuntimeClients(config.Runtime)
	if err != nil {
		t.Fatal("real runtime dependencies unavailable", err)
	}
	defer clients.close()
	pool, err := openCommandPool(config.Command)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	inputs := input.New(pool)
	importer := biz.NewInputImporter(inputs, objectstore.NewVerifier(clients.store, config.Runtime.ObjectStorage.ConnectionId, 32<<20))
	if f.Input.TenantID != f.TenantID || f.Input.Actor != f.Actor || f.Input.InputFormat() != "JSON" {
		t.Fatal("input fixture outside test scope")
	}
	ready, err := importer.ImportFinetuningDataset(ctx, f.Input)
	if err != nil || ready.State != biz.InputStateReady || ready.AlpacaVerification == nil || ready.AlpacaVerification.RowCount != 64 {
		t.Fatal("real JSON import did not persist READY", err)
	}
	intent, err := cpup01.ParseFinetuningIntent(f.Intent)
	if err != nil || intent.DatasetVersionID != ready.Import.InputVersionID {
		t.Fatal("formal codec rejected fixture", err)
	}
	sources := make([]admissionfacts.FileSource, len(config.Command.AdmissionResolution.FactsFiles))
	for i, p := range config.Command.AdmissionResolution.FactsFiles {
		sources[i] = admissionfacts.FileSource{Path: p.Path, SHA256: p.Sha256}
	}
	facts, err := admissionfacts.Load(ctx, sources)
	if err != nil {
		t.Fatal("pinned managed facts unavailable", err)
	}
	resolver := biz.NewFinetuningAdmissionResolver(catalogue.NewReader(config.Command.AdmissionResolution.CatalogueDirectory), inputs, facts, coreFlowModelFixture{tenant: f.TenantID, base: f.BaseModel})
	resolved, err := resolver.ResolveManaged(ctx, biz.AdmissionResolutionRequest{TenantID: f.TenantID, Intent: intent, Release: biz.AdmissionReleaseSelection{ReleaseID: f.ReleaseID, ReleaseDigest: f.ReleaseDigest, BindingGeneration: 1}, AcceptedAt: f.AcceptedAt})
	if err != nil {
		t.Fatal("formal candidate resolution", err)
	}
	if f.ExpectDeadline && (f.StopWhenTraining || f.RestartWhenTraining || resolved.Snapshot.DeadlineAt.Sub(f.AcceptedAt) != 10800*time.Second || !resolved.Snapshot.DeadlineAt.After(time.Now())) {
		t.Fatal("deadline scenario requires the original 10800-second Release and a future remaining window")
	}
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	admission := biz.Admission{TenantID: f.TenantID, Actor: f.Actor, OperationID: f.OperationID, ExecutionID: f.ExecutionID, Intent: intent, IntentHash: intentHash, Snapshot: resolved.Snapshot, SpecHash: resolved.SpecHash, AcceptedAt: f.AcceptedAt}
	if _, _, err := admission.CanonicalPayloads(); err != nil {
		t.Fatal("formal admission envelope", err)
	}
	admissions := execution.New(pool)
	first, err := admissions.Accept(ctx, admission)
	if err != nil {
		t.Fatal("durable application-layer intake", err)
	}
	replay, err := admissions.Accept(ctx, admission)
	if err != nil || !replay.Replayed || replay.ExecutionID != first.ExecutionID || replay.SpecHash != first.SpecHash {
		t.Fatal("original intake replay changed immutable execution", err)
	}
	logger := newRuntimeLogger(io.Discard)
	app, err := buildApp(&config, logger)
	if err != nil {
		t.Fatal("production application wiring", err)
	}
	appDone := make(chan error, 1)
	go func() { appDone <- app.Run() }()
	defer func() { _ = app.Stop() }()
	factsRepo := lifecycle.New(pool)
	stopped, restarted := false, false
	var trainingObservedAt *time.Time
	var final biz.ExecutionRuntime
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		state, err := factsRepo.GetRuntime(ctx, f.TenantID, f.ExecutionID)
		if err != nil && !errors.Is(err, biz.ErrExecutionNotFound) {
			t.Fatal("runtime read", err)
		}
		if err == nil {
			if state.TrainingHandle != nil && state.Observation != nil && state.Observation.Outcome == "RUNNING" {
				if trainingObservedAt == nil {
					observed := time.Now().UTC().Truncate(time.Microsecond)
					trainingObservedAt = &observed
				}
				if f.StopWhenTraining && !stopped {
					stop := biz.CloseIntent{TenantID: f.TenantID, ExecutionID: f.ExecutionID, OperationID: f.OperationID, SpecHash: resolved.SpecHash, SourceGeneration: 1, Reason: biz.CloseReasonUserStop, RequestedAt: time.Now().UTC().Truncate(time.Microsecond), RequestedActor: f.Actor}
					first, err := admissions.ApplyCloseIntent(ctx, stop)
					if err != nil {
						t.Fatal("real Stop usecase", err)
					}
					again, err := admissions.ApplyCloseIntent(ctx, stop)
					if err != nil || !again.Replayed || again.Generation != first.Generation {
						t.Fatal("Stop replay", err)
					}
					stopped = true
				}
				if f.RestartWhenTraining && !restarted {
					err := app.Stop()
					if err != nil {
						t.Fatal("owner stop", err)
					}
					if err := <-appDone; err != nil {
						t.Fatal("original owner shutdown", err)
					}
					app, err = buildApp(&config, logger)
					if err != nil {
						t.Fatal("owner rebuild", err)
					}
					go func() { appDone <- app.Run() }()
					restarted = true
				}
			}
			if state.CloseReviewReason != "" {
				t.Fatal("close remains uncertain", state.CloseReviewReason)
			}
			if state.ClosedAt != nil {
				final = state
				break
			}
		}
		select {
		case err := <-appDone:
			t.Fatal("production owner exited before closure", err)
		case <-ctx.Done():
			t.Fatal("frozen main-flow budget exceeded")
		case <-ticker.C:
		}
	}
	if final.Training == nil || final.TrainingHandle == nil || final.Observation == nil || !final.Observation.WritersAbsent || final.CloseEvidence == nil {
		t.Fatal("CLOSED lacks real computation writer facts")
	}
	if f.ExpectDeadline {
		if trainingObservedAt == nil || !trainingObservedAt.Before(resolved.Snapshot.DeadlineAt) || final.CloseReason != "DEADLINE" || final.CloseRequestedAt.Before(resolved.Snapshot.DeadlineAt) || len(final.FinetuningSteps) != 0 {
			t.Fatal("original deadline did not stop an observed training writer before new export/verify permits")
		}
	} else if f.StopWhenTraining {
		if !stopped || final.CloseReason != "USER_STOP" {
			t.Fatal("actual running Stop was not observed")
		}
	} else {
		if final.Publication == nil || final.Publication.Bundle == nil || final.FinetuningRegistration == nil || final.FinetuningRegistration.State != "DEPENDENCY_UNAVAILABLE" {
			t.Fatal("publication/provider-error closure missing")
		}
	}
	if f.RestartWhenTraining && !restarted {
		t.Fatal("owner restart was not exercised")
	}
	closedReplay, err := admissions.Accept(ctx, admission)
	if err != nil || !closedReplay.Replayed || closedReplay.ExecutionID != first.ExecutionID || closedReplay.SpecHash != first.SpecHash || !closedReplay.Snapshot.DeadlineAt.Equal(resolved.Snapshot.DeadlineAt) {
		t.Fatal("original closed execution replay changed identity or deadline", err)
	}
	replayedState, err := factsRepo.GetRuntime(ctx, f.TenantID, f.ExecutionID)
	if err != nil || replayedState.OwnerRevision != final.OwnerRevision || replayedState.ClosedAt == nil || !replayedState.ClosedAt.Equal(*final.ClosedAt) || replayedState.CloseGeneration != final.CloseGeneration {
		t.Fatal("original closed execution replay changed runtime facts", err)
	}
	if err := os.MkdirAll(f.EvidenceDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{"scope": "CORE_FLOW", "scenario": f.Scenario, "source_commit": f.SourceCommit, "execution_id": f.ExecutionID, "operation_id": f.OperationID, "spec_hash": resolved.SpecHash, "input_version": ready, "snapshot": resolved.Snapshot, "runtime": final, "closed_replay": closedReplay, "training_observed_at": trainingObservedAt, "upstream_boundary": "isolated application-layer fixture; Gov identity and delivery NOT_RUN", "base_model_boundary": "approved fixed S3 object through read-only Model fixture; Model permissions NOT_RUN", "model_registration": "provider unavailable; no READY", "material_source": f.MaterialSource}
	bytes, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	name := strings.ReplaceAll(f.Scenario, "/", "_")
	if err := os.WriteFile(filepath.Join(f.EvidenceDirectory, "core-flow-"+name+".json"), append(bytes, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	// No credentials, signed URL, input bytes or complete private config enters
	// standard output. The independent readback has a separate read-only identity.
	t.Logf("CORE_FLOW_CLOSED scenario=%s execution=%s publication=%t namespace_uid=%s", f.Scenario, f.ExecutionID, final.Publication != nil, resolved.Snapshot.Environment.NamespaceUID)
}

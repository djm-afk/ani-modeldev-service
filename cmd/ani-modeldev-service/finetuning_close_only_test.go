//go:build finetuning_core_flow

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
)

func TestCloseOnlyOriginalNoDispatchClosesWithoutTrainingAndReplaysStop(t *testing.T) {
	f, fixture := closeOnlyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := f.openPool()
	if _, err := execution.New(pool).Accept(ctx, f.admission); err != nil {
		t.Fatal(err)
	}
	result, err := runCoreFlowCloseOnly(ctx, fixture, f.config)
	if err != nil || result.Outcome != "CLOSED" || result.Runtime.ClosedAt == nil || result.Runtime.CloseEvidence == nil || !result.Runtime.CloseEvidence.NoDispatch || result.Runtime.TrainingHandle != nil || result.Runtime.Training != nil || !result.StopReplay.Replayed || result.Stop.Generation != result.StopReplay.Generation {
		t.Fatalf("CLOSE_ONLY_NOT_IMPLEMENTED: actual undispatched closure/replay without training: outcome=%s err=%v", result.Outcome, err)
	}
	if f.posts.Load() != 0 {
		t.Fatal("close-only spawned a new Run")
	}
	// A fresh pool reads the committed Stop and closure; rerunning the original
	// close fixture must preserve both clocks and the original owner generation.
	stored, err := lifecycle.New(f.openPool()).GetRuntime(ctx, fixture.TenantID, fixture.ExecutionID)
	if err != nil || stored.ClosedAt == nil || !stored.ClosedAt.Equal(*result.Runtime.ClosedAt) {
		t.Fatal("closure was not independently durable", err)
	}
	again, err := runCoreFlowCloseOnly(ctx, fixture, f.config)
	if err != nil || !again.Stop.Replayed || !again.Runtime.ClosedAt.Equal(*stored.ClosedAt) || again.Runtime.CloseGeneration != stored.CloseGeneration {
		t.Fatal("original Stop retry changed its persisted receipt", err)
	}
}

func TestCloseOnlyUncertainNoRunKeepsNeedsReviewWithoutResend(t *testing.T) {
	f, fixture := closeOnlyFixture(t)
	var lists atomic.Int32
	f.peer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/apis/v2beta1/runs" && r.Method == http.MethodGet && r.Header.Get("Authorization") == "Bearer synthetic-kfp-owner" {
			lists.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"runs":[],"total_size":0}`)
			return
		}
		if r.Method == http.MethodPost {
			f.posts.Add(1)
		}
		t.Errorf("close-only unexpected boundary: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusForbidden)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := f.openPool()
	if _, err := execution.New(pool).Accept(ctx, f.admission); err != nil {
		t.Fatal(err)
	}
	var binding biz.PipelineDispatchBinding
	raw, err := os.ReadFile(f.config.Runtime.BindingFile)
	if err != nil || json.Unmarshal(raw, &binding) != nil {
		t.Fatal("fixture binding unavailable")
	}
	dispatches := submission.New(pool)
	reserved, err := dispatches.Reserve(ctx, biz.PipelineDispatchRequest{Admission: f.admission, Owner: binding.Owner})
	if err != nil || reserved.SendPermit == nil {
		t.Fatal("actual original send reservation unavailable", err)
	}
	observed, err := dispatches.SubmissionObservationTime(ctx, *reserved.SendPermit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatches.MarkSubmissionUncertain(ctx, *reserved.SendPermit, observed); err != nil {
		t.Fatal(err)
	}
	result, err := runCoreFlowCloseOnly(ctx, fixture, f.config)
	if err != nil || result.Outcome != "NEEDS_REVIEW" || result.Runtime.ClosedAt != nil || result.Runtime.CloseEvidence != nil || result.Runtime.CloseReviewReason != "KFP_CREATE_UNRESOLVED" || result.Runtime.Training != nil || !result.StopReplay.Replayed || lists.Load() == 0 || f.posts.Load() != 0 {
		t.Fatalf("CLOSE_ONLY_NOT_IMPLEMENTED: uncertain original must remain reviewed: outcome=%s err=%v lists=%d posts=%d", result.Outcome, err, lists.Load(), f.posts.Load())
	}
	stored, err := lifecycle.New(f.openPool()).GetRuntime(ctx, fixture.TenantID, fixture.ExecutionID)
	if err != nil || stored.ClosedAt != nil || stored.CloseReviewReason != "KFP_CREATE_UNRESOLVED" {
		t.Fatal("original uncertainty was not durably retained", err)
	}
	retry, err := dispatches.Reserve(ctx, biz.PipelineDispatchRequest{Admission: f.admission, Owner: binding.Owner})
	if err != nil || retry.SendPermit != nil || retry.Dispatch.AttemptID != reserved.Dispatch.AttemptID || retry.Dispatch.State != biz.PipelineDispatchUncertain {
		t.Fatal("closing reconstructed the consumed creation permit", err)
	}
}

func TestCloseOnlyRejectsOriginalIdentityOrSourceDriftBeforeStop(t *testing.T) {
	f, fixture := closeOnlyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := f.openPool()
	if _, err := execution.New(pool).Accept(ctx, f.admission); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"operation", "execution-path", "spec", "source", "proof"} {
		t.Run(kind, func(t *testing.T) {
			candidate := fixture
			copy := *fixture.CloseOnly
			candidate.CloseOnly = &copy
			switch kind {
			case "operation":
				candidate.OperationID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			case "execution-path":
				candidate.ExecutionID = "../../outside-evidence"
			case "spec":
				candidate.CloseOnly.OriginalSpecHash = strings.Repeat("e", 64)
			case "source":
				candidate.CloseOnly.OriginalSourceCommit = strings.Repeat("f", 40)
			case "proof":
				candidate.CloseOnly.OriginalEnvironmentFile = filepath.Join(t.TempDir(), "different-proof.json")
				if err := os.WriteFile(candidate.CloseOnly.OriginalEnvironmentFile, []byte(`{"source_commit":"`+copy.OriginalSourceCommit+`","tampered":true}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := runCoreFlowCloseOnly(ctx, candidate, f.config); err == nil {
				t.Fatal("drifted original fixture accepted")
			}
			stored, err := execution.New(f.openPool()).Get(ctx, fixture.TenantID, fixture.ExecutionID)
			if err != nil || stored.Close != nil {
				t.Fatal("rejected fixture changed original execution", err)
			}
		})
	}
	if f.posts.Load() != 0 {
		t.Fatal("rejected fixture spawned a Run")
	}
}

func closeOnlyFixture(t *testing.T) (configuredRuntimeFixture, coreFlowFixture) {
	t.Helper()
	f := runtimeAppFixture(t)
	originalSource := strings.Repeat("1", 40)
	proof := []byte(`{"source_commit":"` + originalSource + `","scope":"external-boundary-fixture"}`)
	digest := sha256.Sum256(proof)
	f.admission.Actor = "core-flow:test-driver"
	f.admission.Snapshot.Environment.BindingDigest = hex.EncodeToString(digest[:])
	var err error
	f.admission.SpecHash, err = f.admission.Snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	var binding biz.PipelineDispatchBinding
	raw, err := os.ReadFile(f.config.Runtime.BindingFile)
	if err != nil || json.Unmarshal(raw, &binding) != nil {
		t.Fatal("binding fixture unavailable")
	}
	binding.Environment = f.admission.Snapshot.Environment
	raw, err = json.Marshal(binding)
	if err != nil || os.WriteFile(f.config.Runtime.BindingFile, raw, 0600) != nil {
		t.Fatal("binding fixture update failed")
	}
	bindDigest := sha256.Sum256(raw)
	f.config.Runtime.BindingSha256 = hex.EncodeToString(bindDigest[:])
	proofPath := filepath.Join(t.TempDir(), "original-environment.json")
	if err := os.WriteFile(proofPath, append(proof, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	fixture := coreFlowFixture{Schema: "ani.finetuning.core-flow-fixture.v1", Scenario: "close-original", TenantID: f.admission.TenantID, Actor: f.admission.Actor, OperationID: f.admission.OperationID, ExecutionID: f.admission.ExecutionID, AcceptedAt: f.admission.AcceptedAt, SourceCommit: strings.Repeat("2", 40), CloseOnly: &coreFlowCloseOnly{OriginalSpecHash: f.admission.SpecHash, OriginalSourceCommit: originalSource, OriginalEnvironmentFile: proofPath, StopSourceGeneration: 1, StopRequestedAt: time.Now().UTC().Truncate(time.Microsecond)}}
	return f, fixture
}

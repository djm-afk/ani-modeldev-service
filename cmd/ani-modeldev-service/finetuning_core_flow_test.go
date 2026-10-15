//go:build finetuning_core_flow

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
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
	CloseOnly           *coreFlowCloseOnly  `json:"close_only,omitempty"`
}

type coreFlowCloseOnly struct {
	OriginalSpecHash        string    `json:"original_spec_hash"`
	OriginalSourceCommit    string    `json:"original_source_commit"`
	OriginalEnvironmentFile string    `json:"original_environment_file"`
	StopSourceGeneration    uint64    `json:"stop_source_generation"`
	StopRequestedAt         time.Time `json:"stop_requested_at"`
}

type coreFlowCloseResult struct {
	Outcome    string
	Original   biz.Execution
	Stop       biz.CloseReceipt
	StopReplay biz.CloseReceipt
	Runtime    biz.ExecutionRuntime
}

type coreFlowTrainingWriter struct {
	Namespace, NamespaceUID, TrainJobUID, JobSetUID, JobUID, PodUID, PodName, ContainerName, ContainerID string
	StartedAt, ObservedAt                                                                                time.Time
}

func coreFlowRunningTrainingWriter(ctx context.Context, client dynamic.Interface, record biz.QueryRecord, clock func() time.Time) (*coreFlowTrainingWriter, bool, error) {
	state := record.Runtime
	if state.Training == nil || state.TrainingHandle == nil || state.Observation == nil || state.Observation.Outcome != "RUNNING" {
		return nil, false, nil
	}
	// RUNNING includes an unscheduled or ContainerCreating Pod. Resolve the
	// durable original chain, then only GET those exact objects with this
	// existing client; observing a writer must not mutate runtime resources.
	source, err := biz.TrainingLogSourceFor(record, "")
	if errors.Is(err, biz.ErrTrainingLogNotFound) {
		return nil, false, nil
	}
	if err != nil || client == nil || clock == nil {
		return nil, false, biz.ErrRuntimeConflict
	}
	namespace, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, source.Namespace, metav1.GetOptions{})
	if err != nil {
		return nil, false, nil
	}
	if namespace.GetAPIVersion() != "v1" || namespace.GetKind() != "Namespace" || namespace.GetName() != source.Namespace || string(namespace.GetUID()) != source.NamespaceUID {
		return nil, false, biz.ErrRuntimeConflict
	}
	resources := make(map[string]biz.RuntimeResource, len(state.Observation.Resources))
	for _, resource := range state.Observation.Resources {
		resources[resource.UID] = resource
	}
	podFact := resources[source.PodUID]
	job := resources[podFact.OwnerUID]
	set := resources[job.OwnerUID]
	train := resources[set.OwnerUID]
	chain := []biz.RuntimeResource{train, set, job, podFact}
	api := []schema.GroupVersionResource{{Group: "trainer.kubeflow.org", Version: "v1alpha1", Resource: "trainjobs"}, {Group: "jobset.x-k8s.io", Version: "v1alpha2", Resource: "jobsets"}, {Group: "batch", Version: "v1", Resource: "jobs"}, {Version: "v1", Resource: "pods"}}
	var pod corev1.Pod
	for index, expected := range chain {
		actual, err := client.Resource(api[index]).Namespace(source.Namespace).Get(ctx, expected.Name, metav1.GetOptions{})
		if err != nil {
			return nil, false, nil
		}
		if actual.GetAPIVersion() != expected.APIVersion || actual.GetKind() != expected.Kind || actual.GetNamespace() != source.Namespace || actual.GetName() != expected.Name || string(actual.GetUID()) != expected.UID {
			return nil, false, biz.ErrRuntimeConflict
		}
		owner := metav1.GetControllerOf(actual)
		if index == 0 {
			annotations := actual.GetAnnotations()
			if owner != nil || annotations["modeldev.ani.io/tenant-id"] != state.Training.TenantID || annotations["modeldev.ani.io/execution-id"] != state.Training.ExecutionID || annotations["modeldev.ani.io/execution-spec-sha256"] != state.Training.SpecHash {
				return nil, false, biz.ErrRuntimeConflict
			}
		} else {
			parent := chain[index-1]
			if owner == nil || string(owner.UID) != parent.UID || owner.APIVersion != parent.APIVersion || owner.Kind != parent.Kind || owner.Name != parent.Name {
				return nil, false, biz.ErrRuntimeConflict
			}
		}
		if actual.GetDeletionTimestamp() != nil {
			return nil, false, nil
		}
		if index == len(chain)-1 && runtime.DefaultUnstructuredConverter.FromUnstructured(actual.Object, &pod) != nil {
			return nil, false, biz.ErrRuntimeConflict
		}
	}
	if pod.Status.Phase != corev1.PodRunning {
		return nil, false, nil
	}
	nodeCount := 0
	for _, container := range pod.Spec.Containers {
		if container.Name == source.ContainerName {
			nodeCount++
		}
	}
	if nodeCount != 1 {
		return nil, false, biz.ErrRuntimeConflict
	}
	var writer *coreFlowTrainingWriter
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != source.ContainerName {
			continue
		}
		if writer != nil {
			return nil, false, biz.ErrRuntimeConflict
		}
		running := status.State.Running
		if running == nil || running.StartedAt.IsZero() || strings.TrimSpace(status.ContainerID) == "" {
			return nil, false, nil
		}
		// Timestamp the successful Pod read, not the beginning of the GET chain.
		// A deadline crossed during I/O must not become an earlier observation.
		now := clock().UTC().Truncate(time.Microsecond)
		if now.IsZero() || running.StartedAt.After(now) {
			return nil, false, nil
		}
		writer = &coreFlowTrainingWriter{Namespace: source.Namespace, NamespaceUID: source.NamespaceUID, TrainJobUID: state.TrainingHandle.TrainJobUID, JobSetUID: set.UID, JobUID: job.UID, PodUID: source.PodUID, PodName: source.PodName, ContainerName: source.ContainerName, ContainerID: status.ContainerID, StartedAt: running.StartedAt.Time.UTC(), ObservedAt: now}
	}
	return writer, writer != nil, nil
}

func coreFlowOriginalStop(f coreFlowFixture) biz.CloseIntent {
	if f.CloseOnly == nil {
		return biz.CloseIntent{}
	}
	return biz.CloseIntent{TenantID: f.TenantID, ExecutionID: f.ExecutionID, OperationID: f.OperationID, SpecHash: f.CloseOnly.OriginalSpecHash, SourceGeneration: f.CloseOnly.StopSourceGeneration, Reason: biz.CloseReasonUserStop, RequestedAt: f.CloseOnly.StopRequestedAt, RequestedActor: f.Actor}
}

func runCoreFlowCloseOnly(ctx context.Context, f coreFlowFixture, config *conf.Bootstrap) (result coreFlowCloseResult, err error) {
	if ctx == nil || f.CloseOnly == nil || config == nil || config.Command == nil || config.Runtime == nil || f.StopWhenTraining || f.RestartWhenTraining || f.ExpectDeadline {
		return result, biz.ErrInvalidAdmission
	}
	stop := coreFlowOriginalStop(f)
	if err := stop.Validate(); err != nil {
		return result, err
	}
	// This mode is a bounded close delivery for an existing command. It cannot
	// import inputs, resolve a current Release or reconstruct an admission.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	pool, err := openCommandPool(config.Command)
	if err != nil {
		return result, errors.New("close-only database unavailable")
	}
	defer pool.Close()
	admissions := execution.New(pool)
	original, err := admissions.Get(ctx, f.TenantID, f.ExecutionID)
	if err != nil {
		return result, err
	}
	close := f.CloseOnly
	if original.TenantID != f.TenantID || original.ExecutionID != f.ExecutionID || original.OperationID != f.OperationID || original.Actor != f.Actor || !original.AcceptedAt.Equal(f.AcceptedAt) || original.SpecHash != close.OriginalSpecHash || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(close.OriginalSourceCommit) {
		return result, biz.ErrAdmissionConflict
	}
	proof, err := readCommandMaterial(close.OriginalEnvironmentFile, 1<<20)
	if err != nil {
		return result, errors.New("original environment evidence unavailable")
	}
	proof = bytes.TrimSuffix(proof, []byte{'\n'})
	digest := sha256.Sum256(proof)
	var identity struct {
		SourceCommit string `json:"source_commit"`
	}
	if hex.EncodeToString(digest[:]) != original.Snapshot.Environment.BindingDigest || json.Unmarshal(proof, &identity) != nil || identity.SourceCommit != close.OriginalSourceCommit {
		return result, biz.ErrAdmissionConflict
	}
	bindingBytes, err := readCommandMaterial(config.Runtime.BindingFile, 1<<20)
	if err != nil {
		return result, errors.New("original owner binding unavailable")
	}
	bindDigest := sha256.Sum256(bindingBytes)
	var binding biz.PipelineDispatchBinding
	if hex.EncodeToString(bindDigest[:]) != config.Runtime.BindingSha256 || decodeRuntimeMaterial(bindingBytes, &binding) != nil || binding.Validate() != nil || binding.TenantID != original.TenantID || binding.Environment != original.Snapshot.Environment {
		return result, biz.ErrAdmissionConflict
	}
	first, err := admissions.ApplyCloseIntent(ctx, stop)
	if err != nil {
		return result, err
	}
	replay, err := admissions.ApplyCloseIntent(ctx, stop)
	if err != nil || !replay.Replayed || replay.CloseRecord != first.CloseRecord {
		return result, errors.New("original Stop replay mismatch")
	}
	result.Original, result.Stop, result.StopReplay = original, first, replay
	app, err := buildApp(config, newRuntimeLogger(io.Discard))
	if err != nil {
		return result, errors.New("original close owner wiring unavailable")
	}
	done := make(chan error, 1)
	go func() { done <- app.Run() }()
	exited := false
	defer func() {
		stopErr := app.Stop()
		if !exited {
			select {
			case ownerErr := <-done:
				stopErr = errors.Join(stopErr, ownerErr)
			case <-time.After(config.Server.ShutdownTimeout.AsDuration() + time.Second):
				stopErr = errors.Join(stopErr, errors.New("close owner shutdown incomplete"))
			}
		}
		if stopErr != nil {
			err = errors.Join(err, errors.New("close owner shutdown failed"))
		}
	}()
	facts := lifecycle.New(pool)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	// Await the original app's existing readiness endpoint before reading an
	// already-terminal receipt; stopping before Run starts would leak the owner.
	_, adminPort, _ := net.SplitHostPort(config.Server.Admin.Addr)
	adminURL := "http://" + net.JoinHostPort("127.0.0.1", adminPort) + "/readyz"
	localTransport := &http.Transport{}
	defer localTransport.CloseIdleConnections()
	localClient := &http.Client{Transport: localTransport, Timeout: time.Second}
	started := false
	for {
		if !started {
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, adminURL, nil)
			response, readyErr := localClient.Do(request)
			if readyErr == nil {
				started = response.StatusCode == http.StatusOK
				_ = response.Body.Close()
			}
			if !started {
				select {
				case <-tick.C:
					continue
				case ownerErr := <-done:
					exited = true
					return result, errors.Join(errors.New("original close owner failed to start"), ownerErr)
				case <-ctx.Done():
					return result, ctx.Err()
				}
			}
		}
		state, readErr := facts.GetRuntime(ctx, original.TenantID, original.ExecutionID)
		if readErr != nil {
			return result, readErr
		}
		result.Runtime = state
		if coreFlowCleanupComplete(state) {
			result.Outcome = "CLOSED"
			break
		}
		if state.CloseReviewReason != "" && state.ClosedAt == nil {
			result.Outcome = "NEEDS_REVIEW"
			break
		}
		if state.ComputeCleanup != nil && state.ComputeCleanup.Phase == "NEEDS_REVIEW" && state.ComputeCleanup.Attempts >= biz.ComputeCleanupMaximumAttempts {
			result.Outcome = "NEEDS_REVIEW"
			break
		}
		select {
		case ownerErr := <-done:
			exited = true
			return result, errors.Join(errors.New("original close owner exited before verified result"), ownerErr)
		case <-ctx.Done():
			return result, ctx.Err()
		case <-tick.C:
		}
	}
	// The source delivery cannot rewrite either immutable admission payload.
	after, err := admissions.Get(ctx, original.TenantID, original.ExecutionID)
	if err != nil {
		return result, err
	}
	beforeIntent, beforeSnapshot, beforeErr := original.CanonicalPayloads()
	afterIntent, afterSnapshot, afterErr := after.CanonicalPayloads()
	if beforeErr != nil || afterErr != nil || !bytes.Equal(beforeIntent, afterIntent) || !bytes.Equal(beforeSnapshot, afterSnapshot) || after.SpecHash != original.SpecHash || after.OperationID != original.OperationID || !after.AcceptedAt.Equal(original.AcceptedAt) {
		return result, biz.ErrAdmissionConflict
	}
	return result, nil
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
	if f.CloseOnly != nil {
		if f.CloseOnly.OriginalEnvironmentFile != "/config/original-environment.json" || coreFlowOriginalStop(f).Validate() != nil {
			t.Fatal("original close evidence must be a fixed read-only fixture material")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		result, closeErr := runCoreFlowCloseOnly(ctx, f, &config)
		record := map[string]any{"scope": "CORE_FLOW_CLOSE_ONLY", "scenario": f.Scenario, "source_commit": f.SourceCommit, "original_source_commit": f.CloseOnly.OriginalSourceCommit, "execution_id": f.ExecutionID, "operation_id": f.OperationID, "original_spec_hash": f.CloseOnly.OriginalSpecHash, "original_environment_reference": f.CloseOnly.OriginalEnvironmentFile, "outcome": result.Outcome, "original": result.Original, "stop": result.Stop, "stop_replay": result.StopReplay, "runtime": result.Runtime, "upstream_boundary": "isolated USER_STOP application-layer delivery; Gov identity NOT_RUN"}
		if closeErr != nil {
			record["outcome"] = "INCOMPLETE"
		}
		if err := os.MkdirAll(f.EvidenceDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		raw, err := json.MarshalIndent(record, "", "  ")
		if err != nil || os.WriteFile(filepath.Join(f.EvidenceDirectory, "close-only-"+f.ExecutionID+".json"), append(raw, '\n'), 0o600) != nil {
			t.Fatal("close-only evidence write failed")
		}
		if closeErr != nil {
			t.Fatal("original close did not reach a verified result", closeErr)
		}
		t.Logf("CORE_FLOW_CLOSE_ONLY outcome=%s execution=%s training=%t", result.Outcome, f.ExecutionID, result.Runtime.TrainingHandle != nil)
		if result.Outcome != "CLOSED" {
			t.Fatal("original close remains NEEDS_REVIEW")
		}
		return
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
	var trainingWriter *coreFlowTrainingWriter
	var final biz.ExecutionRuntime
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		state, err := factsRepo.GetRuntime(ctx, f.TenantID, f.ExecutionID)
		if err != nil && !errors.Is(err, biz.ErrExecutionNotFound) {
			t.Fatal("runtime read", err)
		}
		if err == nil {
			writer, running, writerErr := coreFlowRunningTrainingWriter(ctx, clients.kube, biz.QueryRecord{Execution: first.Execution, Runtime: state}, time.Now)
			if writerErr != nil {
				t.Fatal("original training writer identity", writerErr)
			}
			if running {
				if trainingObservedAt == nil {
					observed := writer.ObservedAt
					trainingObservedAt = &observed
					trainingWriter = writer
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
			if state.ComputeCleanup != nil && state.ComputeCleanup.Phase == "NEEDS_REVIEW" && state.ComputeCleanup.Attempts >= biz.ComputeCleanupMaximumAttempts {
				t.Fatal("automatic compute cleanup remains NEEDS_REVIEW", state.ComputeCleanup.LastError)
			}
			// CLOSED is committed before the owner's external cleanup I/O. Keep
			// that owner alive until its durable post-close receipt is complete.
			if coreFlowCleanupComplete(state) {
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
	if final.Training == nil || final.TrainingHandle == nil || final.Observation == nil || !final.Observation.WritersAbsent || final.CloseEvidence == nil || final.StepExitCleanup == nil || final.StepExitCleanup.CompletedAt == nil {
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
	record := map[string]any{"scope": "CORE_FLOW", "scenario": f.Scenario, "source_commit": f.SourceCommit, "execution_id": f.ExecutionID, "operation_id": f.OperationID, "spec_hash": resolved.SpecHash, "input_version": ready, "snapshot": resolved.Snapshot, "runtime": final, "closed_replay": closedReplay, "training_observed_at": trainingObservedAt, "training_writer": trainingWriter, "upstream_boundary": "isolated application-layer fixture; Gov identity and delivery NOT_RUN", "base_model_boundary": "approved fixed S3 object through read-only Model fixture; Model permissions NOT_RUN", "model_registration": "provider unavailable; no READY", "material_source": f.MaterialSource}
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

func coreFlowCleanupComplete(state biz.ExecutionRuntime) bool {
	return state.ClosedAt != nil && state.CloseEvidence != nil && (state.CloseEvidence.NoDispatch || state.StepExitCleanup != nil && state.StepExitCleanup.CompletedAt != nil && state.ComputeCleanup != nil && state.ComputeCleanup.Phase == "APPLIED")
}

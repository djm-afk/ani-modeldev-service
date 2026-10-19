package runtimeproof_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/runtimeproof"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// Only KFP and Kubernetes HTTPS endpoints are substituted. Admission,
// dispatch, owner authority, close and release receipts use restricted real
// PostgreSQL pools; each PATCH checks a separately reopened durable receipt.
type stepRetentionFixture struct {
	f                *ownerCloseFixture
	open             func() *pgxpool.Pool
	proof            *runtimeproof.Verifier
	workspace        biz.WorkspaceBinding
	receipt          biz.StepExitCleanupReceipt
	mu               sync.Mutex
	pods             map[string]*unstructured.Unstructured
	patches, deletes int
	losePatch        bool
	replaceOnPatch   bool
	serveExtra       func(*testing.T, http.ResponseWriter, *http.Request) bool
	clientConfig     *rest.Config
}

func newStepRetentionFixture(t *testing.T, abort bool, fine ...bool) *stepRetentionFixture {
	return newConfiguredStepRetentionFixture(t, abort, fine, nil, nil)
}

func newConfiguredStepRetentionFixture(t *testing.T, abort bool, fine []bool, configure func(*ownerCloseFixture), prepared func(*stepRetentionFixture, *lifecycle.Repository, dynamic.Interface), deadline ...time.Duration) *stepRetentionFixture {
	t.Helper()
	f := newOwnerCloseFixture(t)
	if len(fine) == 1 && fine[0] {
		f.execution.Snapshot, f.execution.Intent = conformance.FinetuningSnapshotV1(), conformance.FinetuningIntentV1()
		_, f.execution.IntentHash, _ = cpup01.CanonicalIntent(f.execution.Intent)
	}
	if configure != nil {
		configure(f)
	}
	f.execution.AcceptedAt = time.Now().UTC().Truncate(time.Microsecond)
	f.execution.Snapshot.DeadlineAt = f.execution.AcceptedAt.Add(time.Hour)
	if len(deadline) == 1 {
		f.execution.Snapshot.DeadlineAt = f.execution.AcceptedAt.Add(deadline[0])
	}
	hash, err := f.execution.Snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	f.execution.SpecHash = hash
	open := postgres.Prepare(t)
	pool := open()
	ctx := context.Background()
	if _, err := execution.New(pool).Accept(ctx, f.execution.Admission); err != nil {
		t.Fatal(err)
	}
	dispatches := submission.New(pool)
	reserved, err := dispatches.Reserve(ctx, biz.PipelineDispatchRequest{Admission: f.execution.Admission, Owner: f.dispatch.Plan.Owner})
	if err != nil || reserved.SendPermit == nil {
		t.Fatal("real dispatch permit", err)
	}
	at, err := dispatches.SubmissionObservationTime(ctx, *reserved.SendPermit)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := dispatches.RecordSubmissionConfirmed(ctx, *reserved.SendPermit, biz.PipelineSubmissionObservation{State: biz.PipelineSubmissionConfirmed, RunID: f.runID}, at)
	if err != nil {
		t.Fatal(err)
	}
	f.dispatch = confirmed.Dispatch
	f.run["experiment_id"], f.run["display_name"], f.run["service_account"] = f.dispatch.Plan.Environment.ExperimentID, f.dispatch.Plan.DisplayName, f.dispatch.Plan.Environment.Identities.KFPStepServiceAccount
	f.run["pipeline_version_reference"] = map[string]any{"pipeline_id": f.dispatch.Plan.PipelineID, "pipeline_version_id": f.dispatch.Plan.PipelineVersionID}
	f.run["runtime_config"] = map[string]any{"parameters": map[string]any{"execution_id": f.dispatch.Plan.ExecutionID, "spec_hash": f.dispatch.Plan.SpecHash}, "pipeline_root": f.dispatch.Plan.Owner.PipelineRoot}
	if _, err := dispatches.BindRunAuthority(ctx, f.authority()); err != nil {
		t.Fatal(err)
	}
	f.terminateExternalRecords(t)
	for i := range f.pods {
		f.pods[i].SetResourceVersion("1")
		f.pods[i].SetFinalizers([]string{"modeldev.ani.io/step-exit-evidence"})
	}
	if abort {
		capturedInitializationAbort(t, &f.pods[0])
	}
	w, e := f.execution.Snapshot.Workspace, f.execution.Snapshot.Environment
	fixture := &stepRetentionFixture{f: f, open: open, pods: map[string]*unstructured.Unstructured{}, workspace: biz.WorkspaceBinding{Mode: w.Mode, NamespaceName: e.NamespaceName, NamespaceUID: e.NamespaceUID, PVCName: "retention-workspace", PVCUID: "retention-workspace-uid", InputSubpath: w.InputSubpath, TrainingSubpath: w.TrainingSubpath, ReportsSubpath: w.ReportsSubpath, PublicationSubpath: w.PublicationSubpath, PreparedManifestSHA256: strings.Repeat("1", 64), PreparedManifestBytes: 256}}
	for i := range f.pods {
		fixture.pods[f.pods[i].GetName()] = f.pods[i].DeepCopy()
	}
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serve(t, w, r) }))
	t.Cleanup(peer.Close)
	fixture.clientConfig = &rest.Config{Host: peer.URL, TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw})}, Timeout: 3 * time.Second, QPS: 100, Burst: 200}
	client, err := dynamic.NewForConfig(fixture.clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	fixture.proof = runtimeproof.New(client, f.runs, nil, dispatches)
	repository := lifecycle.New(pool)
	if _, _, err := repository.RecordPrepared(ctx, f.authority(), fixture.workspace); err != nil {
		t.Fatal(err)
	}
	if prepared != nil {
		prepared(fixture, repository, client)
	}
	closing, _, err := repository.RequestRuntimeClose(ctx, f.authority(), "STEP_FAILED")
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := fixture.proof.VerifyOwnerWritersAbsent(ctx, f.execution, f.authority(), &fixture.workspace)
	if err != nil {
		t.Fatal("independent actual HTTPS owner exits", err)
	}
	observation := biz.TrainingRuntimeObservation{}
	if closing.Observation != nil {
		observation = *closing.Observation
	}
	if _, err := repository.ConfirmRuntimeClosed(ctx, f.authority(), closing.CloseGeneration, observation, evidence); err != nil {
		t.Fatal(err)
	}
	started, err := repository.BeginStepExitCleanup(ctx, f.authority(), closing.CloseGeneration, evidence)
	if err != nil {
		t.Fatal(err)
	}
	fixture.receipt = *started.StepExitCleanup
	return fixture
}

func (f *stepRetentionFixture) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if f.serveExtra != nil && f.serveExtra(t, w, r) {
		return
	}
	ns := f.f.authority().NamespaceName
	prefix := "/api/v1/namespaces/" + ns
	if r.Method == http.MethodPatch || r.Method == http.MethodDelete {
		// This pool is independent of the close writer. A PATCH without a
		// committed original exit can never pass the external boundary.
		pool := f.open()
		state, err := lifecycle.New(pool).GetRuntime(r.Context(), f.f.execution.TenantID, f.f.execution.ExecutionID)
		pool.Close()
		if err != nil || state.ClosedAt == nil || state.StepExitCleanup == nil || state.StepExitCleanup.StartedAt.IsZero() || !reflect.DeepEqual(state.StepExitCleanup.Evidence, f.receipt.Evidence) {
			t.Error("external mutation preceded the durable original exit receipt", err)
			w.WriteHeader(500)
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	write := func(value any) {
		if err := json.NewEncoder(w).Encode(value); err != nil {
			t.Error(err)
		}
	}
	status := func(code int, reason string) {
		w.WriteHeader(code)
		write(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Failure", Reason: metav1.StatusReason(reason), Code: int32(code)})
	}
	if r.Method == http.MethodGet {
		switch r.URL.Path {
		case prefix:
			write(&unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": ns, "uid": f.f.authority().NamespaceUID}}})
			return
		case "/apis/argoproj.io/v1alpha1/namespaces/" + ns + "/workflows/" + f.f.workflow.GetName():
			write(f.f.workflow)
			return
		case prefix + "/persistentvolumeclaims/" + f.workspace.PVCName:
			write(&unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": f.workspace.PVCName, "namespace": ns, "uid": f.workspace.PVCUID}, "status": map[string]any{"phase": "Bound"}}})
			return
		case prefix + "/pods":
			items := []any{}
			for _, pod := range f.pods {
				items = append(items, pod.Object)
			}
			write(map[string]any{"apiVersion": "v1", "kind": "PodList", "metadata": map[string]any{}, "items": items})
			return
		}
	}
	if !strings.HasPrefix(r.URL.Path, prefix+"/pods/") {
		t.Errorf("unexpected Kubernetes operation %s %s", r.Method, r.URL.Path)
		status(400, "BadRequest")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, prefix+"/pods/")
	pod := f.pods[name]
	if pod == nil {
		status(404, "NotFound")
		return
	}
	switch r.Method {
	case http.MethodGet:
		write(pod)
	case http.MethodPatch:
		f.patches++
		if f.replaceOnPatch {
			pod.SetUID(types.UID("replaced-at-cas"))
			f.replaceOnPatch = false
		}
		var patch []struct {
			Op, Path string
			Value    json.RawMessage
		}
		if r.Header.Get("Content-Type") != "application/json-patch+json" || json.NewDecoder(r.Body).Decode(&patch) != nil || len(patch) != 5 {
			t.Error("not the bounded finalizer JSON CAS")
			status(400, "BadRequest")
			return
		}
		owners, _, _ := unstructured.NestedSlice(pod.Object, "metadata", "ownerReferences")
		values := []any{string(pod.GetUID()), pod.GetResourceVersion(), owners, pod.GetFinalizers()}
		paths := []string{"/metadata/uid", "/metadata/resourceVersion", "/metadata/ownerReferences", "/metadata/finalizers"}
		for i, value := range values {
			expected, _ := json.Marshal(value)
			var a, b any
			_ = json.Unmarshal(expected, &a)
			_ = json.Unmarshal(patch[i].Value, &b)
			if patch[i].Op != "test" || patch[i].Path != paths[i] || !reflect.DeepEqual(a, b) {
				status(409, "Conflict")
				return
			}
		}
		var remaining []string
		if patch[4].Op != "replace" || patch[4].Path != "/metadata/finalizers" || json.Unmarshal(patch[4].Value, &remaining) != nil {
			status(400, "BadRequest")
			return
		}
		wanted := []string{}
		for _, value := range pod.GetFinalizers() {
			if value != "modeldev.ani.io/step-exit-evidence" {
				wanted = append(wanted, value)
			}
		}
		if !reflect.DeepEqual(remaining, wanted) {
			t.Error("foreign finalizer removed")
			status(400, "BadRequest")
			return
		}
		pod.SetFinalizers(remaining)
		version, _ := strconv.Atoi(pod.GetResourceVersion())
		pod.SetResourceVersion(strconv.Itoa(version + 1))
		if f.losePatch {
			f.losePatch = false
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
			} else {
				_ = connection.Close()
			}
			return
		}
		// Kubernetes removes a deletion-requested Pod after its last finalizer.
		if len(remaining) == 0 && pod.GetDeletionTimestamp() != nil {
			delete(f.pods, name)
		}
		write(pod)
	case http.MethodDelete:
		f.deletes++
		var options metav1.DeleteOptions
		if json.NewDecoder(r.Body).Decode(&options) != nil || options.Preconditions == nil || options.Preconditions.UID == nil || options.Preconditions.ResourceVersion == nil || string(*options.Preconditions.UID) != string(pod.GetUID()) || *options.Preconditions.ResourceVersion != pod.GetResourceVersion() || options.PropagationPolicy == nil || *options.PropagationPolicy != metav1.DeletePropagationOrphan || len(pod.GetFinalizers()) != 0 {
			t.Error("DELETE lacked exact original UID/RV or bypassed a finalizer")
			status(409, "Conflict")
			return
		}
		delete(f.pods, name)
		write(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Success", Code: 200})
	default:
		t.Errorf("unexpected Pod operation %s", r.Method)
		status(400, "BadRequest")
	}
}

func (f *stepRetentionFixture) reconcile(t *testing.T) ([]biz.RuntimeResource, error) {
	t.Helper()
	return f.proof.ReconcileStepExitCleanup(context.Background(), f.f.execution, f.f.authority(), &f.workspace, f.receipt)
}

func TestStepExitCleanupHTTPSLostPatchResponseResumesFromRealDatabase(t *testing.T) {
	f := newStepRetentionFixture(t, false)
	f.losePatch = true
	if _, err := f.reconcile(t); !errors.Is(err, biz.ErrCleanupUncertain) {
		t.Fatal("lost successful PATCH became success", err)
	}
	repository := lifecycle.New(f.open())
	state, err := repository.GetRuntime(context.Background(), f.f.execution.TenantID, f.f.execution.ExecutionID)
	if err != nil || state.StepExitCleanup == nil || state.StepExitCleanup.CompletedAt != nil {
		t.Fatal("restart lost unfinished release", err)
	}
	f.receipt = *state.StepExitCleanup
	removed, err := f.reconcile(t)
	if err != nil || len(removed) != len(f.receipt.Evidence.Resources) {
		t.Fatal("original receipt did not safely recover lost response", err)
	}
	completed, err := repository.CompleteStepExitCleanup(context.Background(), f.f.authority(), f.receipt.CloseGeneration, removed)
	if err != nil || completed.StepExitCleanup.CompletedAt == nil || len(f.pods) != 0 || f.deletes != len(removed) {
		t.Fatal("full exact-UID removal not independently persisted", err)
	}
	visible, err := lifecycle.New(f.open()).GetRuntime(context.Background(), f.f.execution.TenantID, f.f.execution.ExecutionID)
	if err != nil || !reflect.DeepEqual(visible.StepExitCleanup, completed.StepExitCleanup) {
		t.Fatal("completed receipt disappeared after reconnect", err)
	}
	t.Log("HTTPS lost PATCH reply recovered from committed DB receipt; all exact-UID Pods absent, original close preserved")
}

func TestStepExitCleanupHTTPSRejectsReplacedCASForeignFinalizerAndNewPVCWriter(t *testing.T) {
	for _, attack := range []string{"replace-at-cas", "foreign-finalizer", "new-pvc-writer"} {
		t.Run(attack, func(t *testing.T) {
			f := newStepRetentionFixture(t, false)
			switch attack {
			case "replace-at-cas":
				f.replaceOnPatch = true
			case "foreign-finalizer":
				for _, pod := range f.pods {
					pod.SetFinalizers([]string{"modeldev.ani.io/step-exit-evidence", "foreign.example/keep"})
				}
			case "new-pvc-writer":
				f.pods["unknown-writer"] = &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "unknown-writer", "namespace": f.workspace.NamespaceName, "uid": "unknown-writer-uid"}, "spec": map[string]any{"containers": []any{map[string]any{"name": "main"}}, "volumes": []any{map[string]any{"name": "workspace", "persistentVolumeClaim": map[string]any{"claimName": f.workspace.PVCName}}}}, "status": map[string]any{"phase": "Running"}}}
			}
			if _, err := f.reconcile(t); err == nil {
				t.Fatal("unsafe release passed", attack)
			}
			if f.deletes != 0 {
				t.Fatal("unsafe release deleted a Pod", f.deletes)
			}
			for _, pod := range f.pods {
				if attack == "foreign-finalizer" && !reflect.DeepEqual(pod.GetFinalizers(), []string{"foreign.example/keep"}) && !reflect.DeepEqual(pod.GetFinalizers(), []string{"modeldev.ani.io/step-exit-evidence", "foreign.example/keep"}) {
					t.Fatal("foreign finalizer lost")
				}
			}
			state, err := lifecycle.New(f.open()).GetRuntime(context.Background(), f.f.execution.TenantID, f.f.execution.ExecutionID)
			if err != nil || state.StepExitCleanup.CompletedAt != nil {
				t.Fatal("unsafe release produced completed DB audit", err)
			}
		})
	}
}

func TestStepExitCleanupHTTPSRetainsInitializationAbortFactsAfterFinalizerRelease(t *testing.T) {
	f := newStepRetentionFixture(t, true)
	var abort *biz.PodInitializationAbort
	for _, resource := range f.receipt.Evidence.Resources {
		if resource.InitializationAbort != nil {
			abort = resource.InitializationAbort
			if resource.ExitCode != nil {
				t.Fatal("invented main exit")
			}
		}
	}
	if abort == nil {
		t.Fatal("missing original real sandbox/container abort facts")
	}
	removed, err := f.reconcile(t)
	if err != nil {
		t.Fatal("finalizer release wrongly invalidated persisted abort facts", err)
	}
	state, err := lifecycle.New(f.open()).CompleteStepExitCleanup(context.Background(), f.f.authority(), f.receipt.CloseGeneration, removed)
	if err != nil || state.StepExitCleanup.CompletedAt == nil {
		t.Fatal(err)
	}
	for _, resource := range state.StepExitCleanup.RemovedResources {
		if resource.InitializationAbort != nil && !reflect.DeepEqual(resource.InitializationAbort, abort) {
			t.Fatal("release rewrote retained abort proof")
		}
	}
}

func TestStepExitCleanupHTTPSRejectsChangedAbortAfterLostReleaseReply(t *testing.T) {
	f := newStepRetentionFixture(t, true)
	// Simulate a committed own-finalizer release whose reply was lost. Only
	// the persisted original sandbox/container/UID facts may authorize retry.
	for _, pod := range f.pods {
		if pod.GetDeletionTimestamp() == nil {
			continue
		}
		pod.SetFinalizers(nil)
		pod.SetResourceVersion("1170949")
		statuses, _, _ := unstructured.NestedSlice(pod.Object, "status", "containerStatuses")
		statuses[0].(map[string]any)["started"] = true
		_ = unstructured.SetNestedSlice(pod.Object, statuses, "status", "containerStatuses")
	}
	if _, err := f.reconcile(t); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("changed never-started container borrowed the persisted abort", err)
	}
	if f.patches != 0 || f.deletes != 0 {
		t.Fatal("changed abort facts reached an API mutation")
	}
}

func TestFinetuningWorkspaceOwnerProofDoesNotCreateTrainingBudget(t *testing.T) {
	// The real HTTPS owner verifier and PG close writer have already accepted
	// this FINETUNING workspace before any TRAIN reservation or budget exists.
	f := newStepRetentionFixture(t, false, true)
	if _, err := biz.FreezeTrainingPlan(f.f.execution, f.workspace); err == nil {
		t.Fatal("workspace validation weakened first TRAIN timing")
	}
	workspace := f.workspace
	workspace.InputSubpath = "other-input"
	if _, err := f.proof.VerifyOwnerWritersAbsent(context.Background(), f.f.execution, f.f.authority(), &workspace); err == nil {
		t.Fatal("fine workspace path drift accepted")
	}
}

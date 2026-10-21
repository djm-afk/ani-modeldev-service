package cleanup_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/cleanup"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/runtimeproof"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestCleanupResolvedConfirmsOnlyOriginalAbsentControllersWithRetainedEvidence(t *testing.T) {
	f := newCleanupResolutionFixture(t)
	resolver, ok := any(cleanup.New(f.kube, f.proof)).(interface {
		VerifyCleanupResolved(context.Context, biz.OperationRecord) ([]biz.RuntimeResource, error)
	})
	if !ok {
		t.Fatal("CLEANUP_RESOLUTION_NOT_IMPLEMENTED: an uncertain original cleanup cannot be reconciled through read-only exact-UID proof")
	}
	confirmed, err := resolver.VerifyCleanupResolved(context.Background(), f.record)
	if err != nil || len(confirmed) != 3 {
		t.Fatalf("absent original controllers with retained original Pods/PVC must reconcile: %+v %v", confirmed, err)
	}
	for i, resource := range confirmed {
		if resource.UID != []string{"job-uid", "set-uid", "train-uid"}[i] {
			t.Fatalf("confirmed absence escaped original plan: %+v", confirmed)
		}
	}
	f.assertReadOnly(t)
}

func TestCleanupResolvedAllowsOnlyProvenOrphanRemovalOfTheOriginalJobOwner(t *testing.T) {
	f := newCleanupResolutionFixture(t)
	f.changePod(t, "original-training-pod", func(pod *unstructured.Unstructured) { pod.SetOwnerReferences(nil) })
	confirmed, err := cleanup.New(f.kube, f.proof).VerifyCleanupResolved(context.Background(), f.record)
	if err != nil || len(confirmed) != 3 {
		t.Fatalf("Orphan deletion may remove only the controller from an exact retained Pod whose complete original Job/JobSet/TrainJob chain is confirmed absent: %+v %v", confirmed, err)
	}
	f.assertReadOnly(t)
}

func TestCleanupResolvedPreservesUncertaintyWithoutAllAbsentTargetsAndRetainedEvidence(t *testing.T) {
	for _, name := range []string{"partial-present", "replacement", "unreadable", "live-writer", "running-init", "missing-close-evidence", "missing-training-history", "missing-pod", "replaced-pod", "deleting-pod", "changed-owner", "orphan-parent-outside-plan", "orphan-job-present", "orphan-workflow-pod", "changed-pvc", "unbound-pvc", "missing-workflow", "changed-namespace"} {
		t.Run(name, func(t *testing.T) {
			f := newCleanupResolutionFixture(t)
			want := error(nil)
			switch name {
			case "partial-present", "replacement", "orphan-job-present":
				uid := "job-uid"
				want = biz.ErrCleanupUncertain
				if name == "replacement" {
					uid = "replacement-job"
					want = biz.ErrCleanupConflict
				}
				object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"namespace": f.namespace(), "name": "original-job", "uid": uid, "ownerReferences": []any{map[string]any{"apiVersion": "jobset.x-k8s.io/v1alpha2", "kind": "JobSet", "name": "original-set", "uid": "set-uid", "controller": true}}}}}
				if err := f.kube.Tracker().Add(object); err != nil {
					t.Fatal(err)
				}
				if name == "orphan-job-present" {
					f.changePod(t, "original-training-pod", func(pod *unstructured.Unstructured) { pod.SetOwnerReferences(nil) })
				}
			case "unreadable":
				want = biz.ErrCleanupUncertain
				f.kube.PrependReactor("get", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "batch", Resource: "jobs"}, "original-job", errors.New("fixture API denied"))
				})
			case "live-writer":
				f.changePod(t, "original-training-pod", func(pod *unstructured.Unstructured) {
					pod.Object["status"] = map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"name": "node", "state": map[string]any{"running": map[string]any{}}}}}
				})
			case "running-init":
				f.changePod(t, "original-training-pod", func(pod *unstructured.Unstructured) {
					_ = unstructured.SetNestedSlice(pod.Object, []any{map[string]any{"name": "init"}}, "spec", "initContainers")
					_ = unstructured.SetNestedSlice(pod.Object, []any{map[string]any{"name": "init", "state": map[string]any{"running": map[string]any{}}}}, "status", "initContainerStatuses")
				})
			case "missing-close-evidence":
				f.record.Runtime.CloseEvidence.Resources = nil
			case "missing-training-history":
				f.record.Runtime.Observation.Resources = f.record.Runtime.Observation.Resources[:3]
			case "missing-pod":
				if err := f.kube.Tracker().Delete(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, f.namespace(), "original-training-pod"); err != nil {
					t.Fatal(err)
				}
			case "replaced-pod":
				f.changePod(t, "original-training-pod", func(pod *unstructured.Unstructured) {
					pod.Object["metadata"].(map[string]any)["uid"] = "replacement-pod"
				})
			case "deleting-pod":
				f.changePod(t, "original-training-pod", func(pod *unstructured.Unstructured) {
					pod.Object["metadata"].(map[string]any)["deletionTimestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
				})
			case "changed-owner":
				f.changePod(t, "original-training-pod", func(pod *unstructured.Unstructured) {
					pod.Object["metadata"].(map[string]any)["ownerReferences"].([]any)[0].(map[string]any)["uid"] = "other-job"
				})
			case "orphan-parent-outside-plan":
				f.changePod(t, "original-training-pod", func(pod *unstructured.Unstructured) { pod.SetOwnerReferences(nil) })
				f.record.Runtime.Observation.Resources[3].OwnerUID = "unrecorded-job"
			case "orphan-workflow-pod":
				f.changePod(t, f.record.Runtime.CloseEvidence.Resources[0].Name, func(pod *unstructured.Unstructured) { pod.SetOwnerReferences(nil) })
			case "changed-pvc", "unbound-pvc":
				resource := schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
				object, err := f.kube.Tracker().Get(resource, f.namespace(), f.record.Runtime.Workspace.PVCName)
				if err != nil {
					t.Fatal(err)
				}
				pvc := object.(*unstructured.Unstructured).DeepCopy()
				if name == "changed-pvc" {
					pvc.Object["metadata"].(map[string]any)["uid"] = "replacement-pvc"
				} else {
					_ = unstructured.SetNestedField(pvc.Object, "Pending", "status", "phase")
				}
				if err := f.kube.Tracker().Update(resource, pvc, f.namespace()); err != nil {
					t.Fatal(err)
				}
			case "missing-workflow":
				if err := f.kube.Tracker().Delete(schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}, f.namespace(), f.record.Authority.WorkflowName); err != nil {
					t.Fatal(err)
				}
			case "changed-namespace":
				resource := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
				object, err := f.kube.Tracker().Get(resource, "", f.namespace())
				if err != nil {
					t.Fatal(err)
				}
				ns := object.(*unstructured.Unstructured).DeepCopy()
				ns.Object["metadata"].(map[string]any)["uid"] = "replacement-namespace"
				if err := f.kube.Tracker().Update(resource, ns, ""); err != nil {
					t.Fatal(err)
				}
			}
			confirmed, err := cleanup.New(f.kube, f.proof).VerifyCleanupResolved(context.Background(), f.record)
			if err == nil || len(confirmed) != 0 || (want != nil && !errors.Is(err, want)) {
				t.Fatalf("unresolved original cleanup was accepted: %+v %v (want %v)", confirmed, err, want)
			}
			f.assertReadOnly(t)
		})
	}
}

func TestCleanupResolvedNoDispatchRequiresNoRuntimeTargetsOrExternalAuthority(t *testing.T) {
	for _, contaminated := range []bool{false, true} {
		f := newCleanupResolutionFixture(t)
		f.record.Runtime.CloseEvidence = &biz.ManagedCloseEvidence{NoDispatch: true, ObservedAt: *f.record.Runtime.ClosedAt}
		f.record.Runtime.Workspace, f.record.Runtime.Training, f.record.Runtime.TrainingHandle, f.record.Runtime.Observation, f.record.Runtime.Publication = nil, nil, nil, nil, nil
		f.record.Dispatch = nil
		if !contaminated {
			f.record.Authority = nil
		}
		confirmed, err := cleanup.New(f.kube, nil).VerifyCleanupResolved(context.Background(), f.record)
		if contaminated {
			if !errors.Is(err, biz.ErrCleanupBlocked) || confirmed != nil {
				t.Fatalf("NoDispatch borrowed foreign authority: %+v %v", confirmed, err)
			}
		} else if err != nil || confirmed == nil || len(confirmed) != 0 {
			t.Fatalf("durable NoDispatch should resolve an empty target set with namespace preserved: %+v %v", confirmed, err)
		}
		f.assertReadOnly(t)
	}
}

func TestCleanupResolvedRejectsNilSuccessfulAPIBodiesWithoutPanicking(t *testing.T) {
	for _, name := range []string{"namespaces", "jobs", "persistentvolumeclaims", "pods"} {
		t.Run(name, func(t *testing.T) {
			f := newCleanupResolutionFixture(t)
			calls := 0
			f.kube.PrependReactor("get", name, func(ktesting.Action) (bool, runtime.Object, error) {
				calls++
				// The real owner verifier consumes the first PVC read. The nil
				// success body here reaches this adapter's independent retained read.
				if name == "persistentvolumeclaims" && calls == 1 {
					return false, nil, nil
				}
				return true, nil, nil
			})
			confirmed, err := cleanup.New(f.kube, f.proof).VerifyCleanupResolved(context.Background(), f.record)
			if err == nil || len(confirmed) != 0 {
				t.Fatalf("empty successful API body proved cleanup: %+v %v", confirmed, err)
			}
			f.assertReadOnly(t)
		})
	}
}

func (f cleanupResolutionFixture) namespace() string {
	return f.record.Execution.Snapshot.Environment.NamespaceName
}
func (f cleanupResolutionFixture) changePod(t *testing.T, name string, change func(*unstructured.Unstructured)) {
	t.Helper()
	resource := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	object, err := f.kube.Tracker().Get(resource, f.namespace(), name)
	if err != nil {
		t.Fatal(err)
	}
	pod := object.(*unstructured.Unstructured).DeepCopy()
	change(pod)
	if err := f.kube.Tracker().Update(resource, pod, f.namespace()); err != nil {
		t.Fatal(err)
	}
}

type cleanupResolutionFixture struct {
	record biz.OperationRecord
	kube   *dynamicfake.FakeDynamicClient
	proof  *runtimeproof.Verifier
}

// The real owner verifier reads the frozen Run through HTTPS and independently
// validates the captured Workflow graph. Only Kubernetes/KFP/storage boundaries
// are substituted; neither close proof nor cleanup behavior is mocked.
func newCleanupResolutionFixture(t *testing.T) cleanupResolutionFixture {
	t.Helper()
	raw, err := os.ReadFile("../runtimeproof/testdata/kfp-2.16-failed-before-prepare.json")
	if err != nil {
		t.Fatal(err)
	}
	var external struct {
		Workflow map[string]any
		Pods     []map[string]any
		Run      map[string]any
	}
	if err := json.Unmarshal(raw, &external); err != nil {
		t.Fatal(err)
	}
	snapshot := conformance.SnapshotV1()
	intent := cpup01.Intent{Name: "cleanup-resolution", Kind: "GENERAL_TRAINING", ImplementationID: snapshot.Implementation.ImplementationID, ComputePresetID: snapshot.ComputeOrigin.ComputePresetID, MLP: &cpup01.MLPParameters{}, DatasetVersionID: snapshot.Input.InputVersionID}
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	execution := biz.Execution{Admission: biz.Admission{TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:synthetic-fixture", OperationID: "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff", ExecutionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", Intent: intent, IntentHash: intentHash, Snapshot: snapshot, SpecHash: conformance.SnapshotSHA256V1, AcceptedAt: snapshot.DeadlineAt.Add(-time.Hour).UTC()}, OwnerRevision: 9, States: biz.ExecutionStates{Close: biz.CloseStateClosed, Delivery: biz.DeliveryStatePublished}}
	execution.Admission = admissionfixture.Bind(t, execution.Admission)
	snapshot = execution.Snapshot
	plan, err := (biz.PipelineDispatchRequest{Admission: execution.Admission, Owner: biz.PipelineOwnerConfiguration{Reference: "cleanup-fixture", RevisionSHA256: strings.Repeat("a", 64), PipelineRoot: "s3://fixture-kfp-artifacts/managed-root"}}).Freeze()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	runID := external.Run["run_id"].(string)
	dispatch := biz.PipelineDispatch{Plan: plan, PlanHash: hash, AttemptID: "66666666-7777-4888-8999-aaaaaaaaaaaa", State: biz.PipelineDispatchConfirmed, ConfirmedRuns: []biz.PipelineConfirmedRun{{RunID: runID}}}
	workflow := &unstructured.Unstructured{Object: external.Workflow}
	workflow.SetNamespace(snapshot.Environment.NamespaceName)
	workflow.SetLabels(map[string]string{"workflows.argoproj.io/completed": "true"})
	at := time.Now().UTC().Add(-time.Minute)
	finished := at.Format(time.RFC3339Nano)
	_ = unstructured.SetNestedField(workflow.Object, finished, "status", "finishedAt")
	nodes, _, _ := unstructured.NestedMap(workflow.Object, "status", "nodes")
	for _, raw := range nodes {
		raw.(map[string]any)["finishedAt"] = finished
	}
	_ = unstructured.SetNestedMap(workflow.Object, nodes, "status", "nodes")
	external.Run["finished_at"] = finished
	external.Run["experiment_id"], external.Run["display_name"], external.Run["service_account"] = plan.Environment.ExperimentID, plan.DisplayName, plan.Environment.Identities.KFPStepServiceAccount
	external.Run["pipeline_version_reference"] = map[string]any{"pipeline_id": plan.PipelineID, "pipeline_version_id": plan.PipelineVersionID}
	external.Run["runtime_config"] = map[string]any{"parameters": map[string]any{"execution_id": plan.ExecutionID, "spec_hash": plan.SpecHash}, "pipeline_root": plan.Owner.PipelineRoot}
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/apis/v2beta1/runs/"+runID || r.Header.Get("Authorization") != "Bearer synthetic-cleanup-token" {
			t.Error("proof escaped frozen original Run")
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(external.Run)
	}))
	t.Cleanup(peer.Close)
	roots := x509.NewCertPool()
	roots.AddCert(peer.Certificate())
	runs, err := kfp.New(kfp.Config{ConnectionRef: plan.Environment.KFPConnectionRef, Endpoint: peer.URL, RootCAs: roots, Timeout: time.Second}, cleanupToken{})
	if err != nil {
		t.Fatal(err)
	}
	workspace := biz.WorkspaceBinding{Mode: snapshot.Workspace.Mode, NamespaceName: snapshot.Environment.NamespaceName, NamespaceUID: snapshot.Environment.NamespaceUID, PVCName: "original-workspace", PVCUID: "pvc-uid", InputSubpath: snapshot.Workspace.InputSubpath, TrainingSubpath: snapshot.Workspace.TrainingSubpath, ReportsSubpath: snapshot.Workspace.ReportsSubpath, PublicationSubpath: snapshot.Workspace.PublicationSubpath, PreparedManifestBytes: 1, PreparedManifestSHA256: strings.Repeat("b", 64)}
	training, err := biz.FreezeTrainingPlan(execution, workspace, execution.AcceptedAt)
	if err != nil {
		t.Fatal(err)
	}
	training, err = biz.BindTrainingRuntime(training, biz.RuntimeTemplateBinding{NamespaceUID: workspace.NamespaceUID, Name: snapshot.RuntimePlan.Name, UID: "runtime-cleanup-original", ObservedSpecSHA256: snapshot.RuntimePlan.ExpectedSpecSHA256})
	if err != nil {
		t.Fatal(err)
	}
	ns := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": workspace.NamespaceName, "uid": workspace.NamespaceUID}}}
	pvc := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"namespace": workspace.NamespaceName, "name": workspace.PVCName, "uid": workspace.PVCUID}, "status": map[string]any{"phase": "Bound"}}}
	objects := []runtime.Object{ns, pvc, workflow}
	for _, raw := range external.Pods {
		pod := &unstructured.Unstructured{Object: raw}
		pod.SetNamespace(workspace.NamespaceName)
		pod.Object["spec"] = map[string]any{"serviceAccountName": snapshot.Environment.Identities.KFPStepServiceAccount, "containers": []any{map[string]any{"name": "main"}}}
		pod.Object["status"] = cleanupTerminatedStatus("main", finished)
		objects = append(objects, pod)
	}
	trainingPod := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"namespace": workspace.NamespaceName, "name": "original-training-pod", "uid": "training-pod-uid", "ownerReferences": []any{map[string]any{"apiVersion": "batch/v1", "kind": "Job", "name": "original-job", "uid": "job-uid", "controller": true}}}, "spec": map[string]any{"containers": []any{map[string]any{"name": "node"}}, "volumes": []any{map[string]any{"name": "workspace", "persistentVolumeClaim": map[string]any{"claimName": workspace.PVCName}}}}, "status": cleanupTerminatedStatus("node", finished)}}
	objects = append(objects, trainingPod)
	kube := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Version: "v1", Resource: "pods"}: "PodList"}, objects...)
	proof := runtimeproof.New(kube, runs, nil, cleanupPlans{dispatch})
	authority := biz.RunAuthorityCandidate{TenantID: execution.TenantID, ExecutionID: execution.ExecutionID, OperationID: execution.OperationID, SpecHash: execution.SpecHash, AttemptID: dispatch.AttemptID, PlanHash: hash, RunID: runID, NamespaceName: workspace.NamespaceName, NamespaceUID: workspace.NamespaceUID, WorkflowName: workflow.GetName(), WorkflowUID: string(workflow.GetUID())}
	evidence, err := proof.VerifyOwnerWritersAbsent(context.Background(), execution, authority, &workspace)
	if err != nil {
		t.Fatalf("real owner proof fixture: %v", err)
	}
	zero := int32(0)
	resources := []biz.RuntimeResource{
		{APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainJob", Namespace: workspace.NamespaceName, Name: training.Name, UID: "train-uid", APIObjectPresent: true, Terminal: true},
		{APIVersion: "jobset.x-k8s.io/v1alpha2", Kind: "JobSet", Namespace: workspace.NamespaceName, Name: "original-set", UID: "set-uid", OwnerUID: "train-uid", APIObjectPresent: true, Terminal: true},
		{APIVersion: "batch/v1", Kind: "Job", Namespace: workspace.NamespaceName, Name: "original-job", UID: "job-uid", OwnerUID: "set-uid", APIObjectPresent: true, Terminal: true},
		{APIVersion: "v1", Kind: "Pod", Namespace: workspace.NamespaceName, Name: trainingPod.GetName(), UID: string(trainingPod.GetUID()), OwnerUID: "job-uid", APIObjectPresent: true, Terminal: true, ExitCode: &zero},
	}
	handle := biz.TrainingHandle{NamespaceUID: workspace.NamespaceUID, TrainJobUID: "train-uid", PVCUID: workspace.PVCUID}
	return cleanupResolutionFixture{record: biz.OperationRecord{QueryRecord: biz.QueryRecord{Execution: execution, Runtime: biz.ExecutionRuntime{OwnerRevision: 9, ClosedAt: &at, CloseGeneration: 1, CloseEvidence: &evidence, Workspace: &workspace, Training: &training, TrainingHandle: &handle, Observation: &biz.TrainingRuntimeObservation{Handle: handle, Resources: resources, WritersAbsent: true, Outcome: "SUCCEEDED", ObservedAt: at}, Publication: &biz.RuntimePublication{ID: "original-publication"}}}, Dispatch: &dispatch, Authority: &authority}, kube: kube, proof: proof}
}

func cleanupTerminatedStatus(name, finished string) map[string]any {
	return map[string]any{"phase": "Succeeded", "containerStatuses": []any{map[string]any{"name": name, "state": map[string]any{"terminated": map[string]any{"exitCode": int64(0), "finishedAt": finished}}}}}
}

func (f cleanupResolutionFixture) assertReadOnly(t *testing.T) {
	t.Helper()
	for _, action := range f.kube.Actions() {
		if action.GetVerb() != "get" && action.GetVerb() != "list" {
			t.Fatalf("resolution mutated retained execution: %s", action.GetVerb())
		}
	}
}

type cleanupToken struct{}

func (cleanupToken) BearerToken(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
	return "synthetic-cleanup-token", nil
}

type cleanupPlans struct{ dispatch biz.PipelineDispatch }

func (p cleanupPlans) Get(context.Context, string, string) (biz.PipelineDispatch, error) {
	return p.dispatch, nil
}

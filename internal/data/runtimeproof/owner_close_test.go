package runtimeproof_test

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
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/runtimeproof"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestClosingRunRecoversActualWorkflowFromKFPChildNodeReferences(t *testing.T) {
	f := newOwnerCloseFixture(t)
	candidate, err := f.verifier().VerifyClosingRun(context.Background(), f.execution, f.dispatch, f.runID)
	if err != nil || candidate.WorkflowName != f.workflow.GetName() || candidate.WorkflowUID != string(f.workflow.GetUID()) || candidate.RunID != f.runID {
		t.Fatalf("closing Run must recover the actual owner of long-named Pods, without treating a child node ID as a Pod name: %+v %v", candidate, err)
	}
}

func TestClosingRunRecoversConfirmedOwnerWithoutElectingAMissingWriter(t *testing.T) {
	f := newOwnerCloseFixture(t)
	f.terminateExternalRecords(t)
	for i := range f.pods {
		if f.pods[i].GetName() == "general-cpu-rnggt-retry-system-container-impl-1467617326" {
			f.pods = append(f.pods[:i], f.pods[i+1:]...)
			break
		}
	}
	candidate, err := f.verifier().VerifyClosingRun(context.Background(), f.execution, f.dispatch, f.runID)
	if err != nil || candidate != f.authority() {
		t.Fatalf("the original confirmed Run must retain its independently observed Workflow owner before writer completion proof: %+v %v", candidate, err)
	}
	if evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, candidate, nil); !errors.Is(err, biz.ErrRuntimeNotReady) || evidence.OwnerTermination != nil {
		t.Fatalf("recovering an owner cannot replace the missing historical Pod exit: %+v %v", evidence, err)
	}
	// A consumed but unresolved send permit has no durable sole confirmed Run.
	// Its discovery must retain the complete independent task/Pod proof.
	f.dispatch.State, f.dispatch.ConfirmedRuns = biz.PipelineDispatchUncertain, nil
	if candidate, err := f.verifier().VerifyClosingRun(context.Background(), f.execution, f.dispatch, f.runID); err == nil {
		t.Fatalf("unknown create borrowed confirmed-owner recovery: %+v", candidate)
	}
}

func TestOwnerCloseRetainsDeletingPodExitsWithoutGrantingCurrentCallerIdentity(t *testing.T) {
	f := newOwnerCloseFixture(t)
	f.terminateExternalRecords(t)
	for i := range f.pods {
		at := metav1.NewTime(time.Now().UTC().Add(-30 * time.Second))
		f.pods[i].SetDeletionTimestamp(&at)
		f.pods[i].SetFinalizers([]string{"modeldev.ani.io/step-exit-evidence"})
	}
	candidate, err := f.verifier().VerifyClosingRun(context.Background(), f.execution, f.dispatch, f.runID)
	if err != nil || candidate != f.authority() {
		t.Fatalf("retained terminal Pods must remain usable by the independent owner: %+v %v", candidate, err)
	}
	evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, candidate, nil)
	if err != nil || evidence.OwnerTermination == nil || len(evidence.Resources) != len(f.pods) {
		t.Fatalf("retention must preserve every actual independently observed exit: %+v %v", evidence, err)
	}
	for _, resource := range evidence.Resources {
		if !resource.APIObjectPresent || !resource.Terminal || resource.ExitCode == nil {
			t.Fatal("retention fabricated or lost an actual Pod exit")
		}
	}
	association := biz.ManagedStepAssociation{RunID: f.runID, NamespaceName: candidate.NamespaceName, NamespaceUID: candidate.NamespaceUID, WorkflowName: candidate.WorkflowName, WorkflowUID: candidate.WorkflowUID}
	for i := range f.pods {
		if f.pods[i].GetName() == "general-cpu-rnggt-retry-system-container-impl-1467617326" {
			association.PodName, association.PodUID = f.pods[i].GetName(), string(f.pods[i].GetUID())
		}
	}
	if _, err := f.verifier().ResolveManagedTaskID(context.Background(), f.dispatch.Plan, association, "workspace-name"); !errors.Is(err, biz.ErrRuntimeNotReady) {
		t.Fatalf("a deleting Pod became a current authenticated step caller: %v", err)
	}
}

func TestOwnerCloseAcceptsControllerProvenOmissionsAfterEveryActualPodTerminates(t *testing.T) {
	f := newOwnerCloseFixture(t)
	f.terminateExternalRecords(t)
	evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil)
	if err != nil || evidence.OwnerTermination == nil || len(evidence.Resources) != len(f.pods) || evidence.RunID != f.runID {
		t.Fatalf("owner close must use completed Workflow omissions and independently terminated actual Pods: %+v %v", evidence, err)
	}
}

func TestOwnerClosePreservesUncertaintyWithoutCompleteWriterAndControllerProof(t *testing.T) {
	for _, missing := range []string{"historical-pod", "running-sidecar", "unknown-node-phase", "unknown-task-state", "contradictory-omitted-state"} {
		t.Run(missing, func(t *testing.T) {
			f := newOwnerCloseFixture(t)
			f.terminateExternalRecords(t)
			switch missing {
			case "historical-pod":
				f.pods = f.pods[1:]
			case "running-sidecar":
				pod := &f.pods[0]
				pod.Object["spec"].(map[string]any)["containers"] = []any{map[string]any{"name": "main"}, map[string]any{"name": "sidecar"}}
				status := pod.Object["status"].(map[string]any)
				status["containerStatuses"] = append(status["containerStatuses"].([]any), map[string]any{"name": "sidecar", "state": map[string]any{"running": map[string]any{"startedAt": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)}}})
			case "unknown-node-phase":
				nodes := f.workflow.Object["status"].(map[string]any)["nodes"].(map[string]any)
				for _, raw := range nodes {
					node := raw.(map[string]any)
					if node["phase"] == "Omitted" {
						node["phase"] = "Unknown"
						break
					}
				}
			case "unknown-task-state", "contradictory-omitted-state":
				for _, raw := range f.run["run_details"].(map[string]any)["task_details"].([]any) {
					task := raw.(map[string]any)
					if task["display_name"] == "prepare" {
						if missing == "unknown-task-state" {
							task["state"] = "STATE_UNSPECIFIED"
						} else {
							task["state"] = "SUCCEEDED"
						}
						break
					}
				}
			}
			if evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil); err == nil || !errors.Is(err, biz.ErrRuntimeNotReady) || evidence.OwnerTermination != nil {
				t.Fatalf("%s must retain unresolved close evidence: %+v %v", missing, evidence, err)
			}
		})
	}
}

func TestClosingRunRejectsNonUniqueCurrentWorkflowAndMainPod(t *testing.T) {
	for _, attack := range []string{"other-workflow", "duplicate-main-pod"} {
		t.Run(attack, func(t *testing.T) {
			f := newOwnerCloseFixture(t)
			for i := range f.pods {
				if f.pods[i].GetName() != "general-cpu-rnggt-retry-system-container-impl-1467617326" {
					continue
				}
				switch attack {
				case "other-workflow":
					refs := f.pods[i].GetOwnerReferences()
					refs[0].Name = "other-workflow"
					refs[0].UID = "other-workflow-uid"
					f.pods[i].SetOwnerReferences(refs)
				case "duplicate-main-pod":
					pod := f.pods[i].DeepCopy()
					pod.SetName("duplicate-main-pod")
					pod.SetUID("duplicate-main-uid")
					f.pods = append(f.pods, *pod)
				}
				break
			}
			if candidate, err := f.verifier().VerifyClosingRun(context.Background(), f.execution, f.dispatch, f.runID); err == nil {
				t.Fatalf("%s must not authorize a frozen Run: %+v", attack, candidate)
			}
		})
	}
}

type ownerCloseFixture struct {
	execution biz.Execution
	dispatch  biz.PipelineDispatch
	runID     string
	run       map[string]any
	workflow  *unstructured.Unstructured
	pods      []unstructured.Unstructured
	runs      *kfp.Client
}

// Only external KFP/Kubernetes records are substituted. The captured Argo
// graph and child references retain their real structure; the frozen envelope
// and namespace are adapted to the existing synthetic conformance admission.
func newOwnerCloseFixture(t *testing.T) *ownerCloseFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/kfp-2.16-failed-before-prepare.json")
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Workflow map[string]any
		Pods     []map[string]any
		Run      map[string]any
	}
	if err = json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	snapshot := conformance.SnapshotV1()
	intent := cpup01.Intent{Name: "owner-close-fixture", Kind: "GENERAL_TRAINING", ImplementationID: snapshot.Implementation.ImplementationID, ComputePresetID: snapshot.ComputeOrigin.ComputePresetID, MLP: &cpup01.MLPParameters{}, DatasetVersionID: snapshot.Input.InputVersionID}
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	admission := biz.Admission{TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:synthetic-fixture", OperationID: "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff", ExecutionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", Intent: intent, IntentHash: intentHash, Snapshot: snapshot, SpecHash: conformance.SnapshotSHA256V1, AcceptedAt: snapshot.DeadlineAt.Add(-time.Hour).UTC()}
	admission = admissionfixture.Bind(t, admission)
	snapshot = admission.Snapshot
	plan, err := (biz.PipelineDispatchRequest{Admission: admission, Owner: biz.PipelineOwnerConfiguration{Reference: "owner-close-fixture", RevisionSHA256: strings.Repeat("a", 64), PipelineRoot: "s3://fixture-kfp-artifacts/managed-root"}}).Freeze()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	f := &ownerCloseFixture{execution: biz.Execution{Admission: admission}, run: record.Run, workflow: &unstructured.Unstructured{Object: record.Workflow}, runID: record.Run["run_id"].(string)}
	f.dispatch = biz.PipelineDispatch{Plan: plan, PlanHash: hash, AttemptID: "66666666-7777-4888-8999-aaaaaaaaaaaa", State: biz.PipelineDispatchConfirmed, ConfirmedRuns: []biz.PipelineConfirmedRun{{RunID: f.runID}}}
	f.workflow.SetNamespace(snapshot.Environment.NamespaceName)
	f.run["experiment_id"], f.run["display_name"], f.run["service_account"] = plan.Environment.ExperimentID, plan.DisplayName, plan.Environment.Identities.KFPStepServiceAccount
	f.run["pipeline_version_reference"] = map[string]any{"pipeline_id": plan.PipelineID, "pipeline_version_id": plan.PipelineVersionID}
	f.run["runtime_config"] = map[string]any{"parameters": map[string]any{"execution_id": plan.ExecutionID, "spec_hash": plan.SpecHash}, "pipeline_root": plan.Owner.PipelineRoot}
	for _, raw := range record.Pods {
		pod := unstructured.Unstructured{Object: raw}
		pod.SetNamespace(snapshot.Environment.NamespaceName)
		if err := unstructured.SetNestedField(pod.Object, snapshot.Environment.Identities.KFPStepServiceAccount, "spec", "serviceAccountName"); err != nil {
			t.Fatal(err)
		}
		f.pods = append(f.pods, pod)
	}
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/apis/v2beta1/runs/"+f.runID || r.Header.Get("Authorization") != "Bearer synthetic-owner-close-token" {
			t.Error("proof did not read the authenticated frozen Run")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(f.run); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(peer.Close)
	roots := x509.NewCertPool()
	roots.AddCert(peer.Certificate())
	f.runs, err = kfp.New(kfp.Config{ConnectionRef: plan.Environment.KFPConnectionRef, Endpoint: peer.URL, RootCAs: roots, Timeout: 3 * time.Second}, ownerCloseToken{})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *ownerCloseFixture) verifier() *runtimeproof.Verifier {
	namespace := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": f.execution.Snapshot.Environment.NamespaceName, "uid": f.execution.Snapshot.Environment.NamespaceUID}}}
	objects := []runtime.Object{namespace, f.workflow}
	for i := range f.pods {
		objects = append(objects, &f.pods[i])
	}
	kube := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Version: "v1", Resource: "pods"}: "PodList"}, objects...)
	return runtimeproof.New(kube, f.runs, nil, ownerClosePlans{f.dispatch})
}

func (f *ownerCloseFixture) authority() biz.RunAuthorityCandidate {
	return biz.RunAuthorityCandidate{TenantID: f.execution.TenantID, ExecutionID: f.execution.ExecutionID, OperationID: f.execution.OperationID, SpecHash: f.execution.SpecHash, AttemptID: f.dispatch.AttemptID, PlanHash: f.dispatch.PlanHash, RunID: f.runID, NamespaceName: f.execution.Snapshot.Environment.NamespaceName, NamespaceUID: f.execution.Snapshot.Environment.NamespaceUID, WorkflowName: f.workflow.GetName(), WorkflowUID: string(f.workflow.GetUID())}
}

// The source capture intentionally excludes full Pod status. This synthetic
// Kubernetes-boundary completion record tests owner proof, not live completion.
func (f *ownerCloseFixture) terminateExternalRecords(t *testing.T) {
	t.Helper()
	at := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	f.workflow.SetLabels(map[string]string{"workflows.argoproj.io/completed": "true"})
	if err := unstructured.SetNestedField(f.workflow.Object, at, "status", "finishedAt"); err != nil {
		t.Fatal(err)
	}
	nodes, _, err := unstructured.NestedMap(f.workflow.Object, "status", "nodes")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range nodes {
		node := raw.(map[string]any)
		node["finishedAt"] = at
	}
	if err := unstructured.SetNestedMap(f.workflow.Object, nodes, "status", "nodes"); err != nil {
		t.Fatal(err)
	}
	f.run["finished_at"] = at
	for i := range f.pods {
		pod := &f.pods[i]
		pod.Object["spec"] = map[string]any{"serviceAccountName": f.execution.Snapshot.Environment.Identities.KFPStepServiceAccount, "containers": []any{map[string]any{"name": "main"}}}
		pod.Object["status"] = map[string]any{"phase": "Succeeded", "containerStatuses": []any{map[string]any{"name": "main", "state": map[string]any{"terminated": map[string]any{"exitCode": int64(0), "finishedAt": at}}}}}
	}
}

type ownerCloseToken struct{}

func (ownerCloseToken) BearerToken(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
	return "synthetic-owner-close-token", nil
}

type ownerClosePlans struct{ dispatch biz.PipelineDispatch }

func (p ownerClosePlans) Get(context.Context, string, string) (biz.PipelineDispatch, error) {
	return p.dispatch, nil
}

package runtimeproof_test

import (
	"context"
	"encoding/json"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/runtimeproof"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"os"
	"strings"
	"testing"
	"time"
)

// Preserve the original task graph, Pod/PVC UIDs, timestamps and status. Only
// immutable boundary admission values follow the existing conformance fixture.
func capturedUnpreparedPVC(t *testing.T) (*ownerCloseFixture, *unstructured.Unstructured) {
	t.Helper()
	f, _ := capturedFinalizerAuth(t)
	raw, e := os.ReadFile("testdata/kfp-2.16-unprepared-pvc.json")
	if e != nil {
		t.Fatal(e)
	}
	var object map[string]any
	if e = json.Unmarshal(raw, &object); e != nil {
		t.Fatal(e)
	}
	pvc := &unstructured.Unstructured{Object: object["pvc"].(map[string]any)}
	oldID := "5f0b866e-31d1-4b44-8a7c-5cc900767e00"
	oldSHA := "bd1fe7a7524ca89f61702eeb28a4ed3b74a8e39b0da30512dfeb3b80013eb12c"
	adaptUnpreparedCapture(t, f, pvc, oldID, oldSHA)
	return f, pvc
}

func adaptUnpreparedCapture(t *testing.T, f *ownerCloseFixture, pvc *unstructured.Unstructured, oldID, oldSHA string) {
	t.Helper()
	adapt := func(object *unstructured.Unstructured) {
		raw, _ := json.Marshal(object.Object)
		s := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(string(raw), oldID, f.execution.ExecutionID), oldSHA, f.execution.SpecHash), "ani-cephfs", f.execution.Snapshot.Workspace.StorageClass), "4Gi", "2Gi")
		s = strings.ReplaceAll(s, "ani-modeldev-tenant-a-v21", f.execution.Snapshot.Environment.NamespaceName)
		if e := json.Unmarshal([]byte(s), object); e != nil {
			t.Fatal(e)
		}
	}
	adapt(f.workflow)
	for i := range f.pods {
		adapt(&f.pods[i])
	}
	adapt(pvc)
	pvc.SetNamespace(f.execution.Snapshot.Environment.NamespaceName)
}

func capturedLauncherFailedPVC(t *testing.T) (*ownerCloseFixture, *unstructured.Unstructured, biz.ManagedStepAssociation) {
	t.Helper()
	f := newOwnerCloseFixture(t)
	raw, err := os.ReadFile("testdata/kfp-2.16-prepare-launcher-failed-before-begin.json")
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Workflow map[string]any
		Pods     []map[string]any
		PVC      map[string]any
		Run      map[string]any
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	f.workflow = &unstructured.Unstructured{Object: record.Workflow}
	f.run, f.runID = record.Run, record.Run["run_id"].(string)
	f.dispatch.ConfirmedRuns[0].RunID = f.runID
	f.pods = nil
	for _, pod := range record.Pods {
		value := unstructured.Unstructured{Object: pod}
		_ = unstructured.SetNestedField(value.Object, f.dispatch.Plan.Environment.Identities.KFPStepServiceAccount, "spec", "serviceAccountName")
		f.pods = append(f.pods, value)
	}
	pvc := &unstructured.Unstructured{Object: record.PVC}
	adaptUnpreparedCapture(t, f, pvc, "ec57bafc-56bd-4f7e-8f65-45527aa4d0e3", "2472153d80484cd6ee777afa9aebdc5f1b5275d861ef294b6e1fca3bb822938d")
	plan := f.dispatch.Plan
	f.run["experiment_id"], f.run["display_name"], f.run["service_account"] = plan.Environment.ExperimentID, plan.DisplayName, plan.Environment.Identities.KFPStepServiceAccount
	f.run["pipeline_version_reference"] = map[string]any{"pipeline_id": plan.PipelineID, "pipeline_version_id": plan.PipelineVersionID}
	f.run["runtime_config"] = map[string]any{"parameters": map[string]any{"execution_id": plan.ExecutionID, "spec_hash": plan.SpecHash}, "pipeline_root": plan.Owner.PipelineRoot}
	owner := f.authority()
	return f, pvc, biz.ManagedStepAssociation{RunID: f.runID, NamespaceName: owner.NamespaceName, NamespaceUID: owner.NamespaceUID, WorkflowName: owner.WorkflowName, WorkflowUID: owner.WorkflowUID, PodName: "md-glh4c-2627411080", PodUID: "59c354aa-a46a-438e-bfea-7f6619a2c368"}
}

func TestPrepareLauncherFailureBeforeBeginClosesAndReclaimsExactPVC(t *testing.T) {
	f, pvc, a := capturedLauncherFailedPVC(t)
	if len(f.pods) != 9 {
		t.Fatal("original ec57 nine-Pod graph changed")
	}
	assertUnpreparedOwnerCloseVertical(t, f, pvc, a)
}

func unpreparedBoundary(f *ownerCloseFixture, pvc *unstructured.Unstructured, extra ...runtime.Object) (*runtimeproof.Verifier, *dynamicfake.FakeDynamicClient) {
	ns := f.execution.Snapshot.Environment
	objects := []runtime.Object{&unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": ns.NamespaceName, "uid": ns.NamespaceUID}}}, f.workflow, pvc}
	for i := range f.pods {
		objects = append(objects, &f.pods[i])
	}
	objects = append(objects, extra...)
	kinds := map[schema.GroupVersionResource]string{{Version: "v1", Resource: "pods"}: "PodList", {Group: "batch", Version: "v1", Resource: "jobs"}: "JobList", {Group: "jobset.x-k8s.io", Version: "v1alpha2", Resource: "jobsets"}: "JobSetList", {Group: "scheduling.volcano.sh", Version: "v1beta1", Resource: "podgroups"}: "PodGroupList"}
	kube := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, objects...)
	return runtimeproof.New(kube, f.runs, nil, ownerClosePlans{f.dispatch}), kube
}

func TestOwnerCloseFreezesCreatedButUnpreparedPVCWithoutInventingPreparedAuthority(t *testing.T) {
	f, pvc := capturedUnpreparedPVC(t)
	proof, _ := unpreparedBoundary(f, pvc)
	evidence, err := proof.VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil)
	if err != nil || evidence.UnpreparedWorkspace == nil || evidence.UnpreparedWorkspace.PVCUID != string(pvc.GetUID()) || evidence.UnpreparedWorkspace.PVCName != pvc.GetName() || len(evidence.Resources) != len(f.pods) {
		t.Fatalf("UNPREPARED_PVC_CLOSE_EVIDENCE_MISSING: %+v %v", evidence, err)
	}
	if f.execution.Snapshot.Workspace.Mode == "" {
		t.Fatal("missing real workspace contract")
	}
	// A foreign running writer is not made harmless by the absence of a durable
	// prepared Workspace binding. Name alone never elects that Pod's owner.
	writer := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"namespace": pvc.GetNamespace(), "name": "foreign-writer", "uid": "foreign-writer-uid"}, "spec": map[string]any{"volumes": []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": pvc.GetName()}}}}, "status": map[string]any{"phase": "Running"}}}
	proof, _ = unpreparedBoundary(f, pvc, writer)
	if _, err := proof.VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil); err == nil {
		t.Fatal("foreign PVC writer accepted")
	}
}

func TestOwnerUnpreparedPVCPreservesOriginalRequestAndObservedUIDFences(t *testing.T) {
	for _, attack := range []string{"foreign-owner", "created-outside-driver", "wrong-storage", "driver-request-disagrees", "wrong-prepare-hash", "missing-created-pvc", "replacement-after-proof"} {
		t.Run(attack, func(t *testing.T) {
			f, pvc := capturedUnpreparedPVC(t)
			switch attack {
			case "foreign-owner":
				pvc.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "foreign", UID: "foreign-owner"}})
			case "created-outside-driver":
				pvc.SetCreationTimestamp(metav1.NewTime(time.Now().UTC()))
			case "wrong-storage":
				_ = unstructured.SetNestedField(pvc.Object, "1Gi", "spec", "resources", "requests", "storage")
			case "driver-request-disagrees":
				for i := range f.pods {
					if f.pods[i].GetName() == "md-58kt8-1728572933" {
						raw, _ := json.Marshal(f.pods[i].Object)
						raw = []byte(strings.ReplaceAll(string(raw), "comp-createpvc", "comp-foreign"))
						_ = json.Unmarshal(raw, &f.pods[i])
					}
				}
			case "wrong-prepare-hash":
				nodes, _, _ := unstructured.NestedMap(f.workflow.Object, "status", "nodes")
				node := nodes["md-58kt8-3210927505"].(map[string]any)
				raw, _ := json.Marshal(node)
				raw = []byte(strings.ReplaceAll(string(raw), f.execution.SpecHash, strings.Repeat("f", 64)))
				_ = json.Unmarshal(raw, &node)
				nodes["md-58kt8-3210927505"] = node
				_ = unstructured.SetNestedMap(f.workflow.Object, nodes, "status", "nodes")
			}
			proof, kube := unpreparedBoundary(f, pvc)
			if attack == "missing-created-pvc" {
				_ = kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(pvc.GetNamespace()).Delete(context.Background(), pvc.GetName(), metav1.DeleteOptions{})
			}
			evidence, err := proof.VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil)
			if attack != "replacement-after-proof" {
				if err == nil {
					t.Fatalf("%s accepted", attack)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			replacement := pvc.DeepCopy()
			replacement.SetUID("aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa")
			_, err = kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(pvc.GetNamespace()).Update(context.Background(), replacement, metav1.UpdateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err = proof.VerifyRecordedOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil, evidence); err == nil {
				t.Fatal("same-name replacement accepted by durable owner proof")
			}
		})
	}
}

func TestPrepareLauncherFailureRetainsRealExitAndWriterFences(t *testing.T) {
	for _, attack := range []string{"running-main", "missing-prepare-pod", "foreign-native-node", "zero-main", "unrelated-live-writer", "different-error-message"} {
		t.Run(attack, func(t *testing.T) {
			f, pvc, _ := capturedLauncherFailedPVC(t)
			var extra []runtime.Object
			switch attack {
			case "running-main", "missing-prepare-pod", "foreign-native-node", "zero-main":
				for i := range f.pods {
					pod := &f.pods[i]
					if pod.GetName() != "md-glh4c-489730569" {
						continue
					}
					if attack == "missing-prepare-pod" {
						f.pods = append(f.pods[:i], f.pods[i+1:]...)
						break
					}
					if attack == "foreign-native-node" {
						a := pod.GetAnnotations()
						a["workflows.argoproj.io/node-id"] = "unrelated-executor"
						pod.SetAnnotations(a)
						break
					}
					statuses, _, _ := unstructured.NestedSlice(pod.Object, "status", "containerStatuses")
					for j, raw := range statuses {
						status := raw.(map[string]any)
						if status["name"] != "main" {
							continue
						}
						if attack == "running-main" {
							status["state"] = map[string]any{"running": map[string]any{"startedAt": "2026-10-29T01:09:00Z"}}
						} else {
							_ = unstructured.SetNestedField(status, int64(0), "state", "terminated", "exitCode")
						}
						statuses[j] = status
					}
					_ = unstructured.SetNestedSlice(pod.Object, statuses, "status", "containerStatuses")
					break
				}
			case "unrelated-live-writer":
				extra = append(extra, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"namespace": pvc.GetNamespace(), "name": "foreign-live-writer", "uid": "foreign-live-writer-uid"}, "spec": map[string]any{"volumes": []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": pvc.GetName()}}}}, "status": map[string]any{"phase": "Running"}}})
			case "different-error-message":
				nodes, _, _ := unstructured.NestedMap(f.workflow.Object, "status", "nodes")
				for id, raw := range nodes {
					node := raw.(map[string]any)
					if node["displayName"] == "prepare" || node["id"] == "md-glh4c-489730569" {
						node["message"] = "arbitrary controller description"
						nodes[id] = node
					}
				}
				_ = unstructured.SetNestedMap(f.workflow.Object, nodes, "status", "nodes")
			}
			proof, _ := unpreparedBoundary(f, pvc, extra...)
			evidence, err := proof.VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil)
			if attack == "different-error-message" {
				if err != nil || evidence.UnpreparedWorkspace == nil || len(evidence.Resources) != 9 {
					t.Fatalf("real exits must not depend on log wording: %+v %v", evidence, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s lost actual process/owner/PVC writer fence", attack)
			}
		})
	}
}

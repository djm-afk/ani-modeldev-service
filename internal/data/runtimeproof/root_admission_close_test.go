package runtimeproof_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/runtimeproof"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestClosingRunClosesCapturedRootAdmissionRejectionWithoutInventingAPod(t *testing.T) {
	f := capturedRootAdmissionFixture(t)
	verifier := rootAdmissionVerifier(f)
	original := f.workflow.DeepCopy()
	authority, err := verifier.VerifyClosingRun(context.Background(), f.execution, f.dispatch, f.runID)
	if err != nil || authority != f.authority() {
		t.Fatalf("the sole confirmed Run lost its actual zero-Pod Workflow owner: %+v %v", authority, err)
	}
	evidence, err := verifier.VerifyOwnerWritersAbsent(context.Background(), f.execution, authority, nil)
	if err != nil || evidence.OwnerTermination == nil || len(evidence.Resources) != 0 || len(evidence.SkippedTasks) != 0 || evidence.OwnerTermination.RunState != "FAILED" || evidence.OwnerTermination.WorkflowPhase != "Error" {
		t.Fatalf("server-rejected root driver must preserve the completed graph without inventing process exits: %+v %v", evidence, err)
	}
	if !reflect.DeepEqual(original.Object, f.workflow.Object) {
		t.Fatal("close-only proof changed the captured Workflow")
	}
	association := biz.ManagedStepAssociation{RunID: f.runID, NamespaceName: authority.NamespaceName, NamespaceUID: authority.NamespaceUID, WorkflowName: authority.WorkflowName, WorkflowUID: authority.WorkflowUID}
	if _, err := verifier.ResolveManagedTaskID(context.Background(), f.dispatch.Plan, association, "workspace-name"); !errors.Is(err, biz.ErrRuntimeNotReady) {
		t.Fatalf("zero-Pod close-only evidence granted a normal step caller: %v", err)
	}
}

func TestClosingRootAdmissionRejectsUnprovedIdentityOrWriters(t *testing.T) {
	for _, attack := range []string{"run-envelope", "tenant", "workflow-execution", "workflow-spec", "pipeline-root", "native-run-id", "native-display-name", "service-account", "multiple-workflows", "real-pod", "process-trace", "missing-completion", "client-timeout", "wrong-hook", "retry", "old-template", "task-membership", "unconfirmed"} {
		t.Run(attack, func(t *testing.T) {
			f := capturedRootAdmissionFixture(t)
			var driver map[string]any
			for _, raw := range f.workflow.Object["status"].(map[string]any)["nodes"].(map[string]any) {
				if raw.(map[string]any)["displayName"] == "root-driver" {
					driver = raw.(map[string]any)
				}
			}
			var extra []*unstructured.Unstructured
			switch attack {
			case "run-envelope":
				f.run["runtime_config"].(map[string]any)["parameters"].(map[string]any)["execution_id"] = "99999999-2222-4333-8444-555555555555"
			case "tenant":
				f.execution.TenantID = "99999999-2222-4333-8444-555555555555"
			case "workflow-execution", "workflow-spec", "pipeline-root":
				value := map[string]any{"gcsOutputDirectory": f.dispatch.Plan.Owner.PipelineRoot, "parameterValues": map[string]any{"execution_id": f.execution.ExecutionID, "spec_hash": f.execution.SpecHash}}
				switch attack {
				case "workflow-execution":
					value["parameterValues"].(map[string]any)["execution_id"] = "99999999-2222-4333-8444-555555555555"
				case "workflow-spec":
					value["parameterValues"].(map[string]any)["spec_hash"] = strings.Repeat("f", 64)
				case "pipeline-root":
					value["gcsOutputDirectory"] = "s3://other-tenant-artifacts/root"
				}
				raw, _ := json.Marshal(value)
				setRootRuntimeConfig(driver["inputs"], string(raw))
				for _, task := range capturedRootEntrypoint(f)["dag"].(map[string]any)["tasks"].([]any) {
					if task.(map[string]any)["name"] == "root-driver" {
						setRootRuntimeConfig(task.(map[string]any)["arguments"], string(raw))
					}
				}
			case "native-run-id", "native-display-name":
				flag := "--run_id"
				if attack == "native-display-name" {
					flag = "--run_display_name"
				}
				for _, raw := range f.workflow.Object["spec"].(map[string]any)["templates"].([]any) {
					template := raw.(map[string]any)
					if template["name"] == "system-dag-driver" {
						args := template["container"].(map[string]any)["args"].([]any)
						for i, value := range args {
							if value == flag {
								args[i+1] = "99999999-2222-4333-8444-555555555555"
							}
						}
					}
				}
			case "service-account":
				f.workflow.Object["spec"].(map[string]any)["serviceAccountName"] = "another-service-account"
			case "multiple-workflows":
				other := f.workflow.DeepCopy()
				other.SetName("another-workflow")
				other.SetUID("99999999-2222-4333-8444-555555555555")
				extra = append(extra, other)
			case "real-pod":
				controller := true
				pod := unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "spec": map[string]any{"serviceAccountName": f.dispatch.Plan.Environment.Identities.KFPStepServiceAccount}, "status": map[string]any{"phase": "Running"}}}
				pod.SetName("actual-root-driver")
				pod.SetUID("99999999-2222-4333-8444-555555555555")
				pod.SetNamespace(f.workflow.GetNamespace())
				pod.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "argoproj.io/v1alpha1", Kind: "Workflow", Name: f.workflow.GetName(), UID: f.workflow.GetUID(), Controller: &controller}})
				pod.SetAnnotations(map[string]string{"workflows.argoproj.io/node-id": driver["id"].(string), "workflows.argoproj.io/node-name": driver["name"].(string)})
				f.pods = append(f.pods, pod)
			case "process-trace":
				driver["hostNodeName"] = "actual-worker"
			case "missing-completion":
				labels := f.workflow.GetLabels()
				labels["workflows.argoproj.io/completed"] = "false"
				f.workflow.SetLabels(labels)
			case "client-timeout":
				driver["message"] = "task '" + driver["name"].(string) + "' errored: context deadline exceeded"
			case "wrong-hook":
				driver["message"] = strings.ReplaceAll(driver["message"].(string), f.workflow.GetNamespace()+".modeldev.ani.io", "another.modeldev.ani.io")
			case "retry":
				capturedRootEntrypoint(f)["retryStrategy"] = map[string]any{"limit": "1"}
			case "old-template":
				driver["templateName"] = "system-container-driver"
			case "task-membership":
				for _, raw := range f.run["run_details"].(map[string]any)["task_details"].([]any) {
					task := raw.(map[string]any)
					if task["display_name"] == f.workflow.GetName() {
						task["child_tasks"] = []any{map[string]any{"pod_name": "another-workflow-node"}}
					}
				}
			case "unconfirmed":
				f.dispatch.State, f.dispatch.ConfirmedRuns = biz.PipelineDispatchUncertain, nil
			}
			verifier := rootAdmissionVerifier(f, extra...)
			authority, ownerErr := verifier.VerifyClosingRun(context.Background(), f.execution, f.dispatch, f.runID)
			if ownerErr == nil && attack != "real-pod" {
				t.Fatalf("%s granted zero-Pod close authority: %+v", attack, authority)
			}
			// Independently recheck the previously observed original owner. A
			// second Workflow or new writer must also block a later close proof.
			evidence, err := verifier.VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil)
			if err == nil || evidence.OwnerTermination != nil {
				t.Fatalf("%s fabricated complete owner termination: %+v %v", attack, evidence, err)
			}
		})
	}
}

func capturedRootEntrypoint(f *ownerCloseFixture) map[string]any {
	for _, raw := range f.workflow.Object["spec"].(map[string]any)["templates"].([]any) {
		if raw.(map[string]any)["name"] == "entrypoint" {
			return raw.(map[string]any)
		}
	}
	panic("captured native entrypoint missing")
}

// The complete original Run and Workflow are captured external boundary
// records. Adapt only their frozen admission envelope to the existing public
// conformance admission. No node phase, child reference, Pod or exit is added.
func capturedRootAdmissionFixture(t *testing.T) *ownerCloseFixture {
	t.Helper()
	f := newOwnerCloseFixture(t)
	raw, err := os.ReadFile("testdata/kfp-2.16-root-driver-admission-timeout.json")
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Workflow map[string]any
		Run      map[string]any
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	f.run, f.workflow, f.pods = record.Run, &unstructured.Unstructured{Object: record.Workflow}, nil
	f.runID = f.run["run_id"].(string)
	plan := f.dispatch.Plan
	f.dispatch.ConfirmedRuns = []biz.PipelineConfirmedRun{{RunID: f.runID}}
	f.run["experiment_id"], f.run["display_name"], f.run["service_account"] = plan.Environment.ExperimentID, plan.DisplayName, plan.Environment.Identities.KFPStepServiceAccount
	f.run["pipeline_version_reference"] = map[string]any{"pipeline_id": plan.PipelineID, "pipeline_version_id": plan.PipelineVersionID}
	f.run["runtime_config"] = map[string]any{"parameters": map[string]any{"execution_id": plan.ExecutionID, "spec_hash": plan.SpecHash}, "pipeline_root": plan.Owner.PipelineRoot}
	oldNamespace := f.workflow.GetNamespace()
	f.workflow.SetNamespace(plan.Environment.NamespaceName)
	f.workflow.Object["spec"].(map[string]any)["serviceAccountName"] = plan.Environment.Identities.KFPStepServiceAccount
	nativeConfig, err := json.Marshal(map[string]any{"gcsOutputDirectory": plan.Owner.PipelineRoot, "parameterValues": map[string]any{"execution_id": plan.ExecutionID, "spec_hash": plan.SpecHash}})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range f.workflow.Object["status"].(map[string]any)["nodes"].(map[string]any) {
		node := raw.(map[string]any)
		if node["displayName"] == "root-driver" {
			node["message"] = strings.ReplaceAll(node["message"].(string), oldNamespace+".modeldev.ani.io", plan.Environment.NamespaceName+".modeldev.ani.io")
			setRootRuntimeConfig(node["inputs"], string(nativeConfig))
		}
	}
	for _, raw := range f.workflow.Object["spec"].(map[string]any)["templates"].([]any) {
		template := raw.(map[string]any)
		if template["name"] == "entrypoint" {
			for _, raw := range template["dag"].(map[string]any)["tasks"].([]any) {
				task := raw.(map[string]any)
				if task["name"] == "root-driver" {
					setRootRuntimeConfig(task["arguments"], string(nativeConfig))
				}
			}
		}
		if template["name"] == "system-dag-driver" {
			args := template["container"].(map[string]any)["args"].([]any)
			for i := range args {
				if args[i] == "--run_display_name" {
					args[i+1] = plan.DisplayName
				}
			}
		}
	}
	return f
}

func setRootRuntimeConfig(value any, config string) {
	for _, raw := range value.(map[string]any)["parameters"].([]any) {
		parameter := raw.(map[string]any)
		if parameter["name"] == "runtime-config" {
			parameter["value"] = config
		}
	}
}

func rootAdmissionVerifier(f *ownerCloseFixture, extra ...*unstructured.Unstructured) *runtimeproof.Verifier {
	namespace := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": f.execution.Snapshot.Environment.NamespaceName, "uid": f.execution.Snapshot.Environment.NamespaceUID}}}
	objects := []runtime.Object{namespace, f.workflow}
	for i := range f.pods {
		objects = append(objects, &f.pods[i])
	}
	for _, object := range extra {
		objects = append(objects, object)
	}
	kube := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}:                                  "PodList",
		{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}: "WorkflowList",
	}, objects...)
	return runtimeproof.New(kube, f.runs, nil, ownerClosePlans{f.dispatch})
}

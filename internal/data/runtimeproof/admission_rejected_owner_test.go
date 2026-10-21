package runtimeproof_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// These are the two server-returned admission failure shapes captured from
// md-7lr59 on 2026-10-21. Only Workflow identity and completed fixture time are
// adapted. No Pod, process exit, or business repository result is fabricated.
func addServerRejectedAdmissionNodes(t *testing.T, f *ownerCloseFixture) (string, string, string) {
	t.Helper()
	finished, _, _ := unstructured.NestedString(f.workflow.Object, "status", "finishedAt")
	nodes := f.workflow.Object["status"].(map[string]any)["nodes"].(map[string]any)
	prefix := f.workflow.GetName()
	driverID, executorID, retryID := prefix+"-337419028", prefix+"-3431297996", prefix+"-791900861"
	message := "Internal error occurred: failed calling webhook \"" + f.workflow.GetNamespace() + ".modeldev.ani.io\": failed to call webhook: Post \"https://ani-modeldev-webhook-v21.ani-system.svc:443/mutate?timeout=8s\": context deadline exceeded"
	driverName := prefix + ".root.exit-handler-1.onExit.finalize-close-driver"
	nodes[driverID] = map[string]any{"id": driverID, "name": driverName, "displayName": "finalize-close-driver", "type": "Pod", "phase": "Error", "templateName": "system-container-driver", "templateScope": "local/" + prefix, "startedAt": finished, "finishedAt": finished, "progress": "0/1", "taskResultSynced": true, "message": "task '" + driverName + "' errored: " + message}
	retryName := prefix + ".root.exit-handler-1.prepare.executor"
	parameters := map[string]any{"parameters": []any{map[string]any{"name": "retry-max-count", "value": "0"}}}
	nodes[executorID] = map[string]any{"id": executorID, "name": retryName + "(0)", "displayName": "executor(0)", "boundaryID": prefix + "-735612318", "type": "Pod", "phase": "Error", "templateName": "metadata-0-2-retry-system-container-impl", "templateScope": "local/" + prefix, "startedAt": finished, "finishedAt": finished, "progress": "0/1", "taskResultSynced": true, "nodeFlag": map[string]any{"retried": true}, "inputs": parameters, "message": message}
	nodes[retryID] = map[string]any{"id": retryID, "name": retryName, "displayName": "executor", "boundaryID": prefix + "-735612318", "type": "Retry", "phase": "Error", "templateName": "metadata-0-2-retry-system-container-impl", "templateScope": "local/" + prefix, "startedAt": finished, "finishedAt": finished, "progress": "0/1", "children": []any{executorID}, "inputs": map[string]any{"parameters": []any{map[string]any{"name": "retry-max-count", "value": "0"}}}, "message": "task '" + retryName + "' errored: " + message}
	// Preserve existing templates and ensure the captured retry contract is
	// explicit rather than inferring zero retries from a child name alone.
	templates, _, err := unstructured.NestedSlice(f.workflow.Object, "spec", "templates")
	if err != nil {
		t.Fatal(err)
	}
	var kept []any
	for _, raw := range templates {
		template := raw.(map[string]any)
		if template["name"] != "system-container-driver" && template["name"] != "metadata-0-2-retry-system-container-impl" {
			kept = append(kept, raw)
		}
	}
	kept = append(kept, map[string]any{"name": "system-container-driver"}, map[string]any{"name": "metadata-0-2-retry-system-container-impl", "retryStrategy": map[string]any{"limit": "{{inputs.parameters.retry-max-count}}"}})
	if err := unstructured.SetNestedSlice(f.workflow.Object, kept, "spec", "templates"); err != nil {
		t.Fatal(err)
	}
	f.workflow.Object["status"].(map[string]any)["phase"] = "Error"
	return driverID, executorID, retryID
}

// KFP 2.16 reports prepare FAILED and preserves its Retry/executor child node
// references even when the Pod Create was rejected by the API server. Keep that
// failed task, rather than making it SKIPPED to bypass normal Pod resolution.
func bindRejectedPrepareTask(t *testing.T, f *ownerCloseFixture, executor, retry string) {
	t.Helper()
	nodes := f.workflow.Object["status"].(map[string]any)["nodes"].(map[string]any)
	var prepareID string
	for id, raw := range nodes {
		if raw.(map[string]any)["displayName"] == "prepare" {
			prepareID = id
		}
	}
	if prepareID == "" {
		t.Fatal("captured prepare controller absent")
	}
	finished, _, _ := unstructured.NestedString(f.workflow.Object, "status", "finishedAt")
	nodes[prepareID] = map[string]any{"id": prepareID, "name": f.workflow.GetName() + ".root.exit-handler-1.prepare", "displayName": "prepare", "type": "DAG", "phase": "Error", "templateName": "metadata-0-2-retry-system-container-executor", "templateScope": "local/" + f.workflow.GetName(), "startedAt": finished, "finishedAt": finished, "progress": "0/1", "children": []any{retry}, "outboundNodes": []any{executor}}
	nodes[executor].(map[string]any)["boundaryID"] = prepareID
	nodes[retry].(map[string]any)["boundaryID"] = prepareID
	details := f.run["run_details"].(map[string]any)
	tasks := details["task_details"].([]any)
	for _, raw := range tasks {
		task := raw.(map[string]any)
		if task["display_name"] == "prepare" {
			task["state"] = "FAILED"
			task["child_tasks"] = []any{map[string]any{"pod_name": retry}}
		}
	}
	details["task_details"] = append(tasks, map[string]any{"run_id": f.runID, "task_id": "a2cf78e8-480c-48b6-9528-bf725e3edaba", "display_name": "executor", "state": "FAILED", "child_tasks": []any{map[string]any{"pod_name": executor}}})
}

func TestOwnerCloseProvesServerRejectedAdmissionBeforeStorage(t *testing.T) {
	for _, shape := range []string{"driver", "executor-zero-retry", "captured-both"} {
		t.Run(shape, func(t *testing.T) {
			f := newOwnerCloseFixture(t)
			f.terminateExternalRecords(t)
			driver, executor, retry := addServerRejectedAdmissionNodes(t, f)
			nodes := f.workflow.Object["status"].(map[string]any)["nodes"].(map[string]any)
			if shape == "driver" {
				delete(nodes, executor)
				delete(nodes, retry)
			} else {
				bindRejectedPrepareTask(t, f, executor, retry)
				if shape == "executor-zero-retry" {
					delete(nodes, driver)
				}
			}
			original := f.workflow.DeepCopy()
			evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil)
			if err != nil || evidence.OwnerTermination == nil || len(evidence.Resources) != len(f.pods) {
				t.Fatalf("server admission rejected the Create before storage; only actual Pods need exits: resources=%d actual=%d err=%v", len(evidence.Resources), len(f.pods), err)
			}
			if !reflect.DeepEqual(original.Object, f.workflow.Object) || evidence.OwnerTermination.WorkflowPhase != "Error" || evidence.OwnerTermination.WorkflowNodesSHA256 == "" {
				t.Fatal("original failed graph was rewritten or omitted")
			}
			for _, r := range evidence.Resources {
				if r.Name == driver || r.Name == executor || r.Name == retry || r.ExitCode == nil {
					t.Fatal("noncreation acquired an invented process exit", r)
				}
			}
		})
	}
}

func TestOwnerCloseServerRejectedAdmissionRetainsUncertainty(t *testing.T) {
	for _, attack := range []string{"client-timeout", "bare-timeout", "other-hook", "other-task-prefix", "http-url", "non-service-host", "wrong-port", "wrong-path", "duplicate-timeout", "unbounded-timeout", "userinfo", "fragment", "process-host", "outputs", "resources", "wrong-template", "wrong-scope", "nonterminal", "incomplete-graph", "missing-historical-pod", "same-node-pod", "foreign-same-node-pod", "retry-one", "retry-input-missing", "retry-template-one", "retry-second-child", "retry-wrong-boundary", "retry-wrong-message", "retry-parent-missing", "driver-retry", "workflow-default-retry", "kfp-executor-reference-missing", "kfp-controller-reference-wrong", "duplicate-kfp-executor-reference"} {
		t.Run(attack, func(t *testing.T) {
			f := newOwnerCloseFixture(t)
			f.terminateExternalRecords(t)
			driver, executor, retry := addServerRejectedAdmissionNodes(t, f)
			bindRejectedPrepareTask(t, f, executor, retry)
			nodes := f.workflow.Object["status"].(map[string]any)["nodes"].(map[string]any)
			node := nodes[executor].(map[string]any)
			parent := nodes[retry].(map[string]any)
			switch attack {
			case "client-timeout":
				node["message"] = "Post \"https://kubernetes.default.svc/api/v1/namespaces/tenant/pods\": context deadline exceeded"
			case "bare-timeout":
				node["message"] = "failed calling webhook: context deadline exceeded"
			case "other-hook":
				node["message"] = strings.ReplaceAll(node["message"].(string), f.workflow.GetNamespace()+".modeldev.ani.io", "other.modeldev.ani.io")
			case "other-task-prefix":
				nodes[driver].(map[string]any)["message"] = "task 'other-node' errored: " + node["message"].(string)
			case "http-url":
				node["message"] = strings.ReplaceAll(node["message"].(string), "https://", "http://")
			case "non-service-host":
				node["message"] = strings.ReplaceAll(node["message"].(string), "ani-modeldev-webhook-v21.ani-system.svc", "example.com")
			case "wrong-port":
				node["message"] = strings.ReplaceAll(node["message"].(string), ":443/", ":8443/")
			case "wrong-path":
				node["message"] = strings.ReplaceAll(node["message"].(string), "/mutate?", "/other?")
			case "duplicate-timeout":
				node["message"] = strings.ReplaceAll(node["message"].(string), "timeout=8s", "timeout=8s&timeout=8s")
			case "unbounded-timeout":
				node["message"] = strings.ReplaceAll(node["message"].(string), "timeout=8s", "timeout=31s")
			case "userinfo":
				node["message"] = strings.ReplaceAll(node["message"].(string), "https://", "https://user@")
			case "fragment":
				node["message"] = strings.ReplaceAll(node["message"].(string), "timeout=8s", "timeout=8s#fragment")
			case "process-host":
				node["hostNodeName"] = "actual-worker"
			case "outputs":
				node["outputs"] = map[string]any{}
			case "resources":
				node["resourcesDuration"] = map[string]any{"cpu": int64(1)}
			case "wrong-template":
				node["templateName"] = "other-template"
			case "wrong-scope":
				node["templateScope"] = "local/other-workflow"
			case "nonterminal":
				node["phase"] = "Running"
			case "incomplete-graph":
				f.workflow.SetLabels(map[string]string{"workflows.argoproj.io/completed": "false"})
			case "missing-historical-pod":
				f.pods = f.pods[1:]
			case "same-node-pod", "foreign-same-node-pod":
				p := f.pods[0].DeepCopy()
				p.SetName("replacement-for-rejected-node")
				p.SetUID("replacement-for-rejected-node-uid")
				p.SetAnnotations(map[string]string{"workflows.argoproj.io/node-id": executor, "workflows.argoproj.io/node-name": node["name"].(string)})
				if attack == "foreign-same-node-pod" {
					p.SetOwnerReferences(nil)
				}
				f.pods = append(f.pods, *p)
			case "retry-one":
				node["name"], node["displayName"] = strings.TrimSuffix(node["name"].(string), "(0)")+"(1)", "executor(1)"
			case "retry-input-missing":
				delete(node, "inputs")
			case "retry-template-one":
				templates := f.workflow.Object["spec"].(map[string]any)["templates"].([]any)
				for _, raw := range templates {
					if raw.(map[string]any)["name"] == node["templateName"] {
						raw.(map[string]any)["retryStrategy"] = map[string]any{"limit": "1"}
					}
				}
			case "retry-second-child":
				parent["children"] = []any{executor, "another-attempt"}
			case "retry-wrong-boundary":
				parent["boundaryID"] = "another-boundary"
			case "retry-wrong-message":
				parent["message"] = "pod deleted"
			case "retry-parent-missing":
				delete(nodes, retry)
			case "driver-retry":
				for _, raw := range f.workflow.Object["spec"].(map[string]any)["templates"].([]any) {
					if raw.(map[string]any)["name"] == "system-container-driver" {
						raw.(map[string]any)["retryStrategy"] = map[string]any{"limit": "1"}
					}
				}
			case "workflow-default-retry":
				f.workflow.Object["spec"].(map[string]any)["templateDefaults"] = map[string]any{"retryStrategy": map[string]any{"limit": "1"}}
			case "kfp-executor-reference-missing", "kfp-controller-reference-wrong":
				for _, raw := range f.run["run_details"].(map[string]any)["task_details"].([]any) {
					task := raw.(map[string]any)
					if attack == "kfp-executor-reference-missing" && task["task_id"] == "a2cf78e8-480c-48b6-9528-bf725e3edaba" {
						task["child_tasks"] = []any{}
					}
					if attack == "kfp-controller-reference-wrong" && task["display_name"] == "prepare" {
						task["child_tasks"] = []any{map[string]any{"pod_name": driver}}
					}
				}
			case "duplicate-kfp-executor-reference":
				details := f.run["run_details"].(map[string]any)
				details["task_details"] = append(details["task_details"].([]any), map[string]any{"run_id": f.runID, "task_id": "44faf2c2-2576-443b-819a-fb2f34d990cd", "display_name": "another-task", "state": "FAILED", "child_tasks": []any{map[string]any{"pod_name": executor}}})
			}
			if evidence, err := f.verifier().VerifyOwnerWritersAbsent(context.Background(), f.execution, f.authority(), nil); err == nil || evidence.OwnerTermination != nil {
				t.Fatalf("%s fabricated writer absence: %+v %v", attack, evidence, err)
			}
		})
	}
}

func TestServerRejectedAdmissionCleanupPersistsOnlyRealExits(t *testing.T) {
	var compute *computeCleanupFixture
	var runtimeObject *unstructured.Unstructured
	step := newConfiguredStepRetentionFixture(t, false, nil, func(owner *ownerCloseFixture) { runtimeObject = computeRuntime(t, owner) }, func(step *stepRetentionFixture, _ *lifecycle.Repository, kube dynamic.Interface) {
		_, executor, retry := addServerRejectedAdmissionNodes(t, step.f)
		bindRejectedPrepareTask(t, step.f, executor, retry)
		compute = &computeCleanupFixture{step: step, kube: kube, runtime: runtimeObject, objects: map[string]*unstructured.Unstructured{}, pendingReads: map[string]int{}}
		step.serveExtra = compute.serve
	})
	ctx := context.Background()
	pool := step.open()
	before, err := lifecycle.New(pool).GetRuntime(ctx, step.f.execution.TenantID, step.f.execution.ExecutionID)
	pool.Close()
	if err != nil || before.ClosedAt == nil || before.Training != nil || before.TrainingHandle != nil || before.StepExitCleanup == nil || len(before.StepExitCleanup.Evidence.Resources) != len(step.f.pods) {
		t.Fatal("server rejection invented training or lost actual exits", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		closer, _ := closedRecoveryCloser(t, compute)
		result, err := closer.Reconcile(ctx, step.f.execution.TenantID, step.f.execution.ExecutionID)
		if err != nil || result.Runtime.ComputeCleanup == nil || result.Runtime.ComputeCleanup.Phase != "APPLIED" || len(step.pods) != 0 {
			t.Fatal("original rejected-create cleanup did not complete", err)
		}
		if !reflect.DeepEqual(result.Runtime.CloseEvidence, before.CloseEvidence) || !result.Runtime.ClosedAt.Equal(*before.ClosedAt) || result.Runtime.TrainingHandle != nil || result.Runtime.Training != nil {
			t.Fatal("cleanup changed failed history or invented training")
		}
	}
	if !reflect.DeepEqual(compute.deleted, []string{"Workflow"}) || step.deletes != len(step.f.pods) || step.patches != len(step.f.pods) {
		t.Fatal("cleanup mutated noncreated Pods or repeated original mutations", compute.deleted, step.deletes, step.patches)
	}
}

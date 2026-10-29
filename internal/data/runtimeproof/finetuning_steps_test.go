package runtimeproof_test

import (
	"context"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Only KFP/Kubernetes external responses are fixtures. The execution uses the
// shared FINETUNING codec, and real proof code checks each original task/Pod.
func finetuningPredecessorFixture(t *testing.T) (*ownerCloseFixture, biz.ManagedStepAssociation, biz.ExecutionRuntime) {
	t.Helper()
	f := newOwnerCloseFixture(t)
	snapshot := conformance.FinetuningSnapshotV1()
	intent := conformance.FinetuningIntentV1()
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	specHash, err := snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	f.execution.Snapshot, f.execution.Intent = snapshot, intent
	f.execution.SpecHash, f.execution.IntentHash = specHash, intentHash
	f.execution.Admission = admissionfixture.Bind(t, f.execution.Admission)
	snapshot, specHash = f.execution.Snapshot, f.execution.SpecHash
	plan, err := (biz.PipelineDispatchRequest{Admission: f.execution.Admission, Owner: f.dispatch.Plan.Owner}).Freeze()
	if err != nil {
		t.Fatal(err)
	}
	f.dispatch.Plan = plan
	f.dispatch.PlanHash, err = plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	f.run["runtime_config"] = map[string]any{"parameters": map[string]any{"execution_id": plan.ExecutionID, "spec_hash": plan.SpecHash}, "pipeline_root": plan.Owner.PipelineRoot}
	f.run["state"] = "RUNNING"
	association := biz.ManagedStepAssociation{RunID: f.runID, NamespaceName: snapshot.Environment.NamespaceName, NamespaceUID: snapshot.Environment.NamespaceUID, WorkflowName: f.workflow.GetName(), WorkflowUID: string(f.workflow.GetUID()), PodName: "verify-original-pod", PodUID: "88888888-aaaa-4bbb-8ccc-dddddddddddd"}
	completed := time.Now().UTC().Add(-time.Second)
	f.pods = nil
	tasks := []any{}
	for _, role := range []string{"train-wait", "export", "verify"} {
		name, uid := role+"-original-pod", "77777777-aaaa-4bbb-8ccc-dddddddddddd"
		if role == "export" {
			uid = "99999999-aaaa-4bbb-8ccc-dddddddddddd"
		}
		if role == "verify" {
			uid = association.PodUID
		}
		pod := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": name, "uid": uid, "namespace": association.NamespaceName, "ownerReferences": []any{map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "name": association.WorkflowName, "uid": association.WorkflowUID, "controller": true}}}, "spec": map[string]any{"serviceAccountName": snapshot.Environment.Identities.KFPStepServiceAccount, "containers": []any{map[string]any{"name": "main"}}}, "status": map[string]any{"phase": "Succeeded", "containerStatuses": []any{map[string]any{"name": "main", "state": map[string]any{"terminated": map[string]any{"exitCode": int64(0), "finishedAt": completed.Format(time.RFC3339Nano)}}}}}}}
		state := "SUCCEEDED"
		if role == "verify" {
			state = "RUNNING"
		}
		tasks = append(tasks, nativeTaskFixture(t, f.workflow, pod, f.runID, role+"-task-id", role, state))
		f.pods = append(f.pods, *pod)
	}
	f.run["run_details"] = map[string]any{"task_details": tasks}
	handle := biz.TrainingHandle{TrainJobUID: "original-training-uid", NamespaceUID: association.NamespaceUID, PVCUID: "original-workspace-uid"}
	state := biz.ExecutionRuntime{TrainingHandle: &handle, Observation: &biz.TrainingRuntimeObservation{Handle: handle, Outcome: "SUCCEEDED", WritersAbsent: true}, FinetuningSteps: []biz.FinetuningStepStart{{Binding: biz.FinetuningStepBinding{Role: "export", SpecHash: specHash, RunID: f.runID, WorkflowUID: association.WorkflowUID, TaskID: "export-task-id", PodName: "export-original-pod", PodUID: "99999999-aaaa-4bbb-8ccc-dddddddddddd"}, Result: &biz.FinetuningStepResult{StartedAt: completed.Add(-time.Second), FinishedAt: completed.Add(-time.Millisecond), ExitCode: 0}}}}
	return f, association, state
}

func TestFinetuningPredecessorUsesTaskExitWhileRunStillRunning(t *testing.T) {
	f, association, state := finetuningPredecessorFixture(t)
	for _, role := range []string{"export", "verify"} {
		if err := f.verifier().VerifyFinetuningStepPredecessor(context.Background(), f.execution, association, role, state); err != nil {
			t.Fatalf("%s incorrectly requires terminal Run: %v", role, err)
		}
	}
}

func TestFinetuningPredecessorRejectsSelfReportWithoutOriginalPodProof(t *testing.T) {
	for _, missing := range []string{"pod-uid", "task-id", "failed-main", "running-sidecar", "training-writer"} {
		t.Run(missing, func(t *testing.T) {
			f, association, state := finetuningPredecessorFixture(t)
			switch missing {
			case "pod-uid":
				state.FinetuningSteps[0].Binding.PodUID = "other-pod-uid"
			case "task-id":
				state.FinetuningSteps[0].Binding.TaskID = "other-task-id"
			case "training-writer":
				state.Observation.WritersAbsent = false
			case "failed-main":
				if err := unstructured.SetNestedSlice(f.pods[1].Object, []any{map[string]any{"name": "main", "state": map[string]any{"terminated": map[string]any{"exitCode": int64(1), "finishedAt": time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)}}}}, "status", "containerStatuses"); err != nil {
					t.Fatal(err)
				}
			case "running-sidecar":
				if err := unstructured.SetNestedSlice(f.pods[1].Object, []any{map[string]any{"name": "main"}, map[string]any{"name": "sidecar"}}, "spec", "containers"); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.verifier().VerifyFinetuningStepPredecessor(context.Background(), f.execution, association, "verify", state); err == nil {
				t.Fatal("unproven predecessor admitted compute")
			}
		})
	}
}

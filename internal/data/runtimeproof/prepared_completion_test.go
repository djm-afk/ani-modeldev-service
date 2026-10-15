package runtimeproof_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/runtimeproof"
	workspaceio "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/workspace"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestPreparedCompletionChecksOriginalTaskPodAndFrozenWorkspace(t *testing.T) {
	for _, mode := range []string{"CPU success", "FINETUNING success", "running prepare", "failed main", "running sidecar", "future main", "future sidecar", "wrong PVC UID", "wrong mount", "wrong manifest", "foreign workflow"} {
		t.Run(mode, func(t *testing.T) {
			f := newOwnerCloseFixture(t)
			if mode != "CPU success" {
				f, _, _ = finetuningPredecessorFixture(t)
			}
			snapshot := f.execution.Snapshot
			association := biz.ManagedStepAssociation{RunID: f.runID, NamespaceName: snapshot.Environment.NamespaceName, NamespaceUID: snapshot.Environment.NamespaceUID, WorkflowName: f.workflow.GetName(), WorkflowUID: string(f.workflow.GetUID()), PodName: "original-train-wait", PodUID: "99999999-9999-4999-8999-999999999999"}
			w := snapshot.Workspace
			workspace := biz.WorkspaceBinding{Mode: w.Mode, NamespaceName: association.NamespaceName, NamespaceUID: association.NamespaceUID, PVCName: "original-workspace", PVCUID: "88888888-8888-4888-8888-888888888888", InputSubpath: w.InputSubpath, TrainingSubpath: w.TrainingSubpath, ReportsSubpath: w.ReportsSubpath, PublicationSubpath: w.PublicationSubpath}
			manifest, err := workspaceio.PreparedManifestBytes(f.execution, workspace)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(manifest)
			workspace.PreparedManifestBytes, workspace.PreparedManifestSHA256 = int64(len(manifest)), hex.EncodeToString(digest[:])
			completed := time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
			prepare := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "original-prepare", "uid": "77777777-7777-4777-8777-777777777777", "namespace": association.NamespaceName, "ownerReferences": []any{map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "name": association.WorkflowName, "uid": association.WorkflowUID, "controller": true}}}, "spec": map[string]any{"serviceAccountName": snapshot.Environment.Identities.KFPStepServiceAccount, "volumes": []any{map[string]any{"name": "workspace", "persistentVolumeClaim": map[string]any{"claimName": workspace.PVCName}}}, "containers": []any{map[string]any{"name": "main", "volumeMounts": []any{map[string]any{"name": "workspace", "mountPath": "/workspace"}}}}}, "status": map[string]any{"phase": "Succeeded", "containerStatuses": []any{map[string]any{"name": "main", "state": map[string]any{"terminated": map[string]any{"exitCode": int64(0), "finishedAt": completed}}}}}}}
			pvc := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": workspace.PVCName, "namespace": workspace.NamespaceName, "uid": workspace.PVCUID}, "spec": map[string]any{"storageClassName": w.StorageClass, "resources": map[string]any{"requests": map[string]any{"storage": strconv.FormatInt(w.CapacityBytes, 10)}}}, "status": map[string]any{"phase": "Bound"}}}
			task := map[string]any{"run_id": f.runID, "task_id": "original-prepare-task", "display_name": "prepare", "pod_name": prepare.GetName(), "state": "SUCCEEDED"}
			set := func(value any, fields ...string) {
				t.Helper()
				if err := unstructured.SetNestedField(prepare.Object, value, fields...); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "running prepare":
				task["state"] = "RUNNING"
			case "failed main":
				set("Failed", "status", "phase")
				set([]any{map[string]any{"name": "main", "state": map[string]any{"terminated": map[string]any{"exitCode": int64(1), "finishedAt": completed}}}}, "status", "containerStatuses")
			case "future main":
				set([]any{map[string]any{"name": "main", "state": map[string]any{"terminated": map[string]any{"exitCode": int64(0), "finishedAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}}}}, "status", "containerStatuses")
			case "running sidecar", "future sidecar":
				containers, _, _ := unstructured.NestedSlice(prepare.Object, "spec", "containers")
				set(append(containers, map[string]any{"name": "sidecar"}), "spec", "containers")
				statuses, _, _ := unstructured.NestedSlice(prepare.Object, "status", "containerStatuses")
				state := map[string]any{"running": map[string]any{}}
				if mode == "future sidecar" {
					state = map[string]any{"terminated": map[string]any{"exitCode": int64(0), "finishedAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}}
				}
				set(append(statuses, map[string]any{"name": "sidecar", "state": state}), "status", "containerStatuses")
			case "wrong PVC UID":
				workspace.PVCUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			case "wrong mount":
				set([]any{map[string]any{"name": "main", "volumeMounts": []any{map[string]any{"name": "workspace", "mountPath": "/other"}}}}, "spec", "containers")
			case "wrong manifest":
				workspace.PreparedManifestSHA256 = strings.Repeat("0", 64)
			case "foreign workflow":
				set([]any{map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "name": association.WorkflowName, "uid": "other-workflow-uid", "controller": true}}, "metadata", "ownerReferences")
			}
			f.run["state"] = "RUNNING"
			f.run["run_details"] = map[string]any{"task_details": []any{task}}
			namespace := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": association.NamespaceName, "uid": association.NamespaceUID}}}
			kube := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Version: "v1", Resource: "pods"}: "PodList"}, namespace, f.workflow, prepare, pvc)
			proof := runtimeproof.New(kube, f.runs, nil, ownerClosePlans{f.dispatch})
			err = proof.VerifyPreparedCompletion(context.Background(), f.execution, association, workspace)
			wantSuccess := mode == "CPU success" || mode == "FINETUNING success"
			if (err == nil) != wantSuccess {
				t.Fatalf("prepare completion gate mode=%s err=%v", mode, err)
			}
		})
	}
}

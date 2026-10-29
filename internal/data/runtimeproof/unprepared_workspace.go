package runtimeproof

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// This is a close-only observation of the official KFP CreatePVC primitive.
// It neither substitutes for prepare nor grants normal workspace authority.
func (v *Verifier) observeUnpreparedWorkspace(ctx context.Context, execution biz.Execution, owner biz.RunAuthorityCandidate, workflow *unstructured.Unstructured, run kfp.ManagedRun, pods []unstructured.Unstructured) (*biz.UnpreparedWorkspace, error) {
	name := biz.ExpectedWorkspacePVCName(execution.ExecutionID)
	client := v.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(owner.NamespaceName)
	pvc, err := client.Get(ctx, name, metav1.GetOptions{})
	var create, prepare kfp.ManagedTask
	creates, prepares := 0, 0
	for _, task := range run.Tasks {
		if task.Name == "createpvc" {
			create = task
			creates++
		}
		if task.Name == "prepare" {
			prepare = task
			prepares++
		}
	}
	if apierrors.IsNotFound(err) && creates <= 1 && create.State != "SUCCEEDED" {
		return nil, nil
	}
	if err != nil || pvc == nil || creates != 1 || prepares != 1 || create.State != "SUCCEEDED" || prepare.State != "FAILED" || (!serverRejectedManagedTask(workflow, prepare, run.Tasks) && !failedPrepareExited(workflow, prepare, run.Tasks, pods, execution.Snapshot.Kind)) {
		return nil, biz.ErrRuntimeNotReady
	}
	nodes, _, _ := unstructured.NestedMap(workflow.Object, "status", "nodes")
	id, node, err := taskControllerNode(create, nodes)
	if err != nil || node["type"] != "Pod" || node["templateName"] != "system-container-driver" || node["phase"] != "Succeeded" {
		return nil, biz.ErrRuntimeConflict
	}
	var driver *unstructured.Unstructured
	for i := range pods {
		if pods[i].GetAnnotations()["workflows.argoproj.io/node-id"] == id {
			if driver != nil {
				return nil, biz.ErrRuntimeConflict
			}
			driver = &pods[i]
		}
	}
	a := biz.ManagedStepAssociation{WorkflowName: owner.WorkflowName, WorkflowUID: owner.WorkflowUID}
	if driver == nil || !workflowOwner(driver, a) || driver.GetAnnotations()["workflows.argoproj.io/node-name"] != node["name"] {
		return nil, biz.ErrRuntimeConflict
	}
	terminal, _, _, code := terminatedPod(driver)
	account, _ := textAt(driver, "spec", "serviceAccountName")
	if !terminal || code == nil || *code != 0 || account != execution.Snapshot.Environment.Identities.KFPStepServiceAccount {
		return nil, biz.ErrRuntimeNotReady
	}
	taskJSON, ok := rootNamedParameter(node, "inputs", "task")
	componentJSON, componentOK := rootNamedParameter(node, "inputs", "component")
	var task, component map[string]any
	if !ok || !componentOK || json.Unmarshal([]byte(taskJSON), &task) != nil || json.Unmarshal([]byte(componentJSON), &component) != nil || component["executorLabel"] != "exec-createpvc" {
		return nil, biz.ErrRuntimeConflict
	}
	if got, _, _ := unstructured.NestedString(task, "componentRef", "name"); got != "comp-createpvc" {
		return nil, biz.ErrRuntimeConflict
	}
	if got, _, _ := unstructured.NestedString(task, "taskInfo", "name"); got != "createpvc" {
		return nil, biz.ErrRuntimeConflict
	}
	deps, _, _ := unstructured.NestedStringSlice(task, "dependentTasks")
	if !reflect.DeepEqual(deps, []string{"workspace-name"}) {
		return nil, biz.ErrRuntimeConflict
	}
	params, _, _ := unstructured.NestedMap(task, "inputs", "parameters")
	if len(params) != 4 {
		return nil, biz.ErrRuntimeConflict
	}
	expectedSource := map[string]any{"taskOutputParameter": map[string]any{"outputParameterKey": "name", "producerTask": "workspace-name"}}
	if !reflect.DeepEqual(params["pvc_name"], expectedSource) {
		return nil, biz.ErrRuntimeConflict
	}
	size, _, _ := unstructured.NestedString(params, "size", "runtimeValue", "constant")
	quantity, qerr := resource.ParseQuantity(size)
	storage, _, _ := unstructured.NestedString(params, "storage_class_name", "runtimeValue", "constant")
	modes, _, _ := unstructured.NestedStringSlice(params, "access_modes", "runtimeValue", "constant")
	if qerr != nil || quantity.Value() != execution.Snapshot.Workspace.CapacityBytes || storage != execution.Snapshot.Workspace.StorageClass || !reflect.DeepEqual(modes, []string{"ReadWriteMany"}) {
		return nil, biz.ErrRuntimeConflict
	}
	// Match the actual driver's request, not just its descriptive Workflow node.
	containers, _, _ := unstructured.NestedSlice(driver.Object, "spec", "containers")
	matched := 0
	for _, raw := range containers {
		c, ok := raw.(map[string]any)
		if !ok {
			return nil, biz.ErrRuntimeConflict
		}
		if c["name"] != "main" {
			continue
		}
		args, _, _ := unstructured.NestedSlice(c, "args")
		if !rootDriverFlag(args, "--task", taskJSON) || !rootDriverFlag(args, "--component", componentJSON) {
			return nil, biz.ErrRuntimeConflict
		}
		matched++
	}
	if matched != 1 {
		return nil, biz.ErrRuntimeConflict
	}
	// The failed prepare's original launcher input is the independently consumed
	// CreatePVC output. Do not derive a cleanup identity from a label or name.
	_, prepareNode, err := taskControllerNode(prepare, nodes)
	patchJSON, patchOK := rootNamedParameter(prepareNode, "inputs", "pod-spec-patch")
	var patch map[string]any
	if err != nil || !patchOK || json.Unmarshal([]byte(patchJSON), &patch) != nil {
		return nil, biz.ErrRuntimeConflict
	}
	volumes, _, _ := unstructured.NestedSlice(patch, "volumes")
	claims := 0
	for _, raw := range volumes {
		vol, ok := raw.(map[string]any)
		if !ok {
			return nil, biz.ErrRuntimeConflict
		}
		claim, found, _ := unstructured.NestedString(vol, "persistentVolumeClaim", "claimName")
		if found {
			if claim != name {
				return nil, biz.ErrRuntimeConflict
			}
			claims++
		}
	}
	if claims != 1 {
		return nil, biz.ErrRuntimeConflict
	}
	launchers, _, _ := unstructured.NestedSlice(patch, "containers")
	inputs := 0
	for _, raw := range launchers {
		c, ok := raw.(map[string]any)
		if !ok {
			return nil, biz.ErrRuntimeConflict
		}
		if c["name"] != "main" {
			continue
		}
		args, _, _ := unstructured.NestedStringSlice(c, "command")
		for i, arg := range args {
			if arg != "--executor_input" {
				continue
			}
			if i+1 >= len(args) {
				return nil, biz.ErrRuntimeConflict
			}
			var input map[string]any
			if json.Unmarshal([]byte(args[i+1]), &input) != nil {
				return nil, biz.ErrRuntimeConflict
			}
			values, _, _ := unstructured.NestedMap(input, "inputs", "parameterValues")
			if values["execution_id"] != execution.ExecutionID || values["spec_hash"] != execution.SpecHash || values["run_id"] != owner.RunID || values["pvc_name"] != name {
				return nil, biz.ErrRuntimeConflict
			}
			inputs++
		}
	}
	if inputs != 1 {
		return nil, biz.ErrRuntimeConflict
	}
	start, _ := node["startedAt"].(string)
	finish, _ := node["finishedAt"].(string)
	started, se := time.Parse(time.RFC3339Nano, start)
	finished, fe := time.Parse(time.RFC3339Nano, finish)
	created := pvc.GetCreationTimestamp().Time
	if se != nil || fe != nil || created.Before(started) || created.After(finished) || pvc.GetUID() == "" || pvc.GetResourceVersion() == "" || !sameObject(pvc, "v1", "PersistentVolumeClaim", owner.NamespaceName, name, string(pvc.GetUID())) || pvc.GetDeletionTimestamp() != nil || len(pvc.GetOwnerReferences()) != 0 {
		return nil, biz.ErrRuntimeConflict
	}
	actualStorage, _, _ := unstructured.NestedString(pvc.Object, "spec", "storageClassName")
	actualModes, _, _ := unstructured.NestedStringSlice(pvc.Object, "spec", "accessModes")
	actualSize, _, _ := unstructured.NestedString(pvc.Object, "spec", "resources", "requests", "storage")
	actualQuantity, err := resource.ParseQuantity(actualSize)
	if err != nil || actualQuantity.Cmp(quantity) != 0 || actualStorage != storage || !reflect.DeepEqual(actualModes, modes) {
		return nil, biz.ErrRuntimeConflict
	}
	spec, _, _ := unstructured.NestedMap(pvc.Object, "spec")
	fact := &biz.UnpreparedWorkspace{NamespaceName: owner.NamespaceName, NamespaceUID: owner.NamespaceUID, PVCName: name, PVCUID: string(pvc.GetUID()), PVCResourceVersion: pvc.GetResourceVersion(), SpecSHA256: unpreparedDigest(spec), CreateTaskID: create.ID, CreatePodUID: string(driver.GetUID()), RequestSHA256: unpreparedDigest(task), CreatedAt: created}
	if err := v.verifyUnpreparedWorkspace(ctx, execution, owner, fact, false); err != nil {
		return nil, err
	}
	return fact, nil
}

// A launcher may exit before Begin records a prepared workspace. The failed
// native task must resolve to its independently referenced real Pod, whose
// main and every sidecar/init process have actually terminated. Error text is
// not evidence. The caller still proves the complete owner graph and writers.
func failedPrepareExited(workflow *unstructured.Unstructured, prepare kfp.ManagedTask, tasks []kfp.ManagedTask, pods []unstructured.Unstructured, kind string) bool {
	resolved, err := resolvedManagedTasksForRole(workflow, tasks, pods, kind, "prepare")
	if err != nil || len(resolved) != 1 || resolved[0].ID != prepare.ID || resolved[0].RunID != prepare.RunID || resolved[0].State != "FAILED" {
		return false
	}
	for i := range pods {
		pod := &pods[i]
		if pod.GetName() != resolved[0].PodName {
			continue
		}
		phase, _ := textAt(pod, "status", "phase")
		terminal, _, _, code := terminatedPod(pod)
		return phase == "Failed" && terminal && code != nil && *code != 0
	}
	return false
}

func unpreparedDigest(value any) string {
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func (v *Verifier) verifyUnpreparedWorkspace(ctx context.Context, execution biz.Execution, owner biz.RunAuthorityCandidate, fact *biz.UnpreparedWorkspace, removalIntent bool) error {
	if !biz.ValidUnpreparedWorkspace(execution, owner, fact) {
		return biz.ErrRuntimeConflict
	}
	pvc, err := v.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(fact.NamespaceName).Get(ctx, fact.PVCName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && removalIntent {
		return nil
	}
	if err != nil {
		return biz.ErrRuntimeNotReady
	}
	spec, _, _ := unstructured.NestedMap(pvc.Object, "spec")
	if !sameObject(pvc, "v1", "PersistentVolumeClaim", fact.NamespaceName, fact.PVCName, fact.PVCUID) || len(pvc.GetOwnerReferences()) != 0 || unpreparedDigest(spec) != fact.SpecSHA256 || !pvc.GetCreationTimestamp().Time.Equal(fact.CreatedAt) || (pvc.GetDeletionTimestamp() != nil && !removalIntent) {
		return biz.ErrRuntimeConflict
	}
	return nil
}

func ownerWorkspaceForEvidence(workspace *biz.WorkspaceBinding, fact *biz.UnpreparedWorkspace) *biz.WorkspaceBinding {
	if workspace != nil {
		return workspace
	}
	if fact == nil {
		return nil
	}
	return &biz.WorkspaceBinding{PVCName: fact.PVCName, PVCUID: fact.PVCUID, NamespaceName: fact.NamespaceName, NamespaceUID: fact.NamespaceUID}
}

package runtimeproof

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// VerifyOwnerWritersAbsent observes owner-initiated close independently of a
// managed close Pod and its token.
func (verifier *Verifier) VerifyOwnerWritersAbsent(ctx context.Context, execution biz.Execution, authority biz.RunAuthorityCandidate, workspace *biz.WorkspaceBinding) (biz.ManagedCloseEvidence, error) {
	if verifier == nil || verifier.kube == nil || verifier.runs == nil || verifier.plans == nil || ctx == nil || ctx.Err() != nil {
		return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
	}
	if _, _, err := execution.CanonicalPayloads(); err != nil {
		return biz.ManagedCloseEvidence{}, biz.ErrRuntimeConflict
	}
	environment := execution.Snapshot.Environment
	if authority.TenantID != execution.TenantID || authority.ExecutionID != execution.ExecutionID || authority.OperationID != execution.OperationID || authority.SpecHash != execution.SpecHash ||
		authority.NamespaceName != environment.NamespaceName || authority.NamespaceUID != environment.NamespaceUID || authority.WorkflowName == "" || authority.WorkflowUID == "" {
		return biz.ManagedCloseEvidence{}, biz.ErrRuntimeConflict
	}
	dispatch, err := verifier.plans.Get(ctx, execution.TenantID, execution.ExecutionID)
	if err != nil {
		return biz.ManagedCloseEvidence{}, err
	}
	frozen, err := (biz.PipelineDispatchRequest{Admission: execution.Admission, Owner: dispatch.Plan.Owner}).Freeze()
	if err != nil {
		return biz.ManagedCloseEvidence{}, biz.ErrRuntimeConflict
	}
	digest, err := frozen.Digest()
	actualDigest, actualErr := dispatch.Plan.Digest()
	if err != nil || actualErr != nil || digest != actualDigest || dispatch.PlanHash != digest || authority.PlanHash != digest || authority.AttemptID != dispatch.AttemptID ||
		dispatch.State != biz.PipelineDispatchConfirmed || len(dispatch.ConfirmedRuns) != 1 || dispatch.ConfirmedRuns[0].RunID != authority.RunID {
		return biz.ManagedCloseEvidence{}, biz.ErrRuntimeConflict
	}
	run, err := verifier.runs.GetManagedRun(ctx, dispatch.Plan, authority.RunID)
	if err != nil {
		return biz.ManagedCloseEvidence{}, err
	}
	observedAt := time.Now().UTC()
	if !terminalTask(run.State) || run.FinishedAt.IsZero() || run.FinishedAt.After(observedAt) {
		return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
	}
	namespace, err := verifier.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, environment.NamespaceName, metav1.GetOptions{})
	if err != nil || !sameObject(namespace, "v1", "Namespace", "", environment.NamespaceName, environment.NamespaceUID) || namespace.GetDeletionTimestamp() != nil {
		return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
	}
	workflows := verifier.kube.Resource(schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}).Namespace(environment.NamespaceName)
	workflow, err := workflows.Get(ctx, authority.WorkflowName, metav1.GetOptions{})
	if err != nil || !sameObject(workflow, "argoproj.io/v1alpha1", "Workflow", environment.NamespaceName, authority.WorkflowName, authority.WorkflowUID) || workflow.GetDeletionTimestamp() != nil {
		return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
	}
	proof, nodes, err := ownerWorkflowCompletion(workflow, observedAt)
	if err != nil {
		return biz.ManagedCloseEvidence{}, err
	}
	proof.RunState, proof.RunFinishedAt = run.State, run.FinishedAt
	if workspace != nil {
		if err := biz.ValidateWorkspaceBinding(execution, *workspace); err != nil {
			return biz.ManagedCloseEvidence{}, biz.ErrRuntimeConflict
		}
		pvc, err := verifier.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(environment.NamespaceName).Get(ctx, workspace.PVCName, metav1.GetOptions{})
		if err != nil || !sameObject(pvc, "v1", "PersistentVolumeClaim", environment.NamespaceName, workspace.PVCName, workspace.PVCUID) || pvc.GetDeletionTimestamp() != nil {
			return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
		}
	}
	// No selector is used: labels and KFP's latest task list cannot hide older
	// attempts, the close Pod, or another controller's writer on the same PVC.
	pods, err := verifier.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(environment.NamespaceName).List(ctx, metav1.ListOptions{})
	if err != nil || pods == nil || pods.GetContinue() != "" {
		return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
	}
	association := biz.ManagedStepAssociation{RunID: authority.RunID, NamespaceName: authority.NamespaceName, NamespaceUID: authority.NamespaceUID, WorkflowName: authority.WorkflowName, WorkflowUID: authority.WorkflowUID}
	evidence := biz.ManagedCloseEvidence{RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, OwnerTermination: &proof}
	seenNames, seenUIDs, seenNodes := make(map[string]bool), make(map[string]bool), make(map[string]bool)
	abortedPods := make(map[string]string)
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.GetAPIVersion() != "v1" || pod.GetKind() != "Pod" || pod.GetNamespace() != environment.NamespaceName {
			return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
		}
		owned := workflowOwner(pod, association)
		for _, owner := range pod.GetOwnerReferences() {
			if string(owner.UID) == authority.WorkflowUID && !owned {
				return biz.ManagedCloseEvidence{}, biz.ErrRuntimeConflict
			}
		}
		usesPVC, valid := ownerWorkspaceReference(pod, workspace)
		if !valid {
			return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
		}
		if !owned && !usesPVC {
			continue
		}
		terminal, _, completed, code := terminatedPod(pod)
		var abort *biz.PodInitializationAbort
		if !terminal && owned {
			abort = ownerInitializationAbort(pod, observedAt)
			if abort != nil {
				terminal, completed = true, abort.DeletedAt
			}
		}
		if !terminal || pod.GetUID() == "" || pod.GetName() == "" || completed.After(observedAt) {
			return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
		}
		if !owned {
			continue
		}
		account, _ := textAt(pod, "spec", "serviceAccountName")
		if (code == nil && abort == nil) || account != environment.Identities.KFPStepServiceAccount || seenNames[pod.GetName()] || seenUIDs[string(pod.GetUID())] {
			return biz.ManagedCloseEvidence{}, biz.ErrRuntimeConflict
		}
		// Argo 3.7.8 common.AnnotationKeyNodeID defines the Pod-name fallback
		// for older Pod naming. Ownership is checked before using the annotation.
		nodeID := pod.GetAnnotations()["workflows.argoproj.io/node-id"]
		if nodeID == "" {
			nodeID = pod.GetName()
		}
		if !nodes[nodeID] || seenNodes[nodeID] {
			return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
		}
		seenNames[pod.GetName()], seenUIDs[string(pod.GetUID())], seenNodes[nodeID] = true, true, true
		if abort != nil {
			evidence.Resources = append(evidence.Resources, biz.RuntimeResource{APIVersion: "v1", Kind: "Pod", Namespace: pod.GetNamespace(), Name: pod.GetName(), UID: string(pod.GetUID()), OwnerUID: authority.WorkflowUID, APIObjectPresent: true, CreationDisabled: true, Terminal: true, InitializationAbort: abort})
			abortedPods[pod.GetName()] = string(pod.GetUID())
		} else {
			evidence.Resources = append(evidence.Resources, podFact(pod, authority.WorkflowUID, *code))
		}
	}
	if len(seenNodes) != len(nodes) {
		// A disappeared historical Pod is missing evidence, not a stopped writer.
		return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
	}
	seenTasks := make(map[string]bool)
	for _, task := range run.Tasks {
		if task.RunID != authority.RunID || task.ID == "" || task.Name == "" || seenTasks[task.ID] || (task.State != "" && !terminalTask(task.State) && task.State != "SKIPPED") {
			return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
		}
		seenTasks[task.ID] = true
		// Retain the top-level Pod contract for older adapters. Child node
		// references are resolved against the independently completed graph.
		if task.PodName != "" && (task.State == "SKIPPED" || !seenNames[task.PodName]) {
			return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
		}
	}
	tasks, err := resolvedManagedTasksForKind(workflow, run.Tasks, pods.Items, execution.Snapshot.Kind)
	if err != nil {
		return biz.ManagedCloseEvidence{}, err
	}
	for _, task := range tasks {
		if !terminalTask(task.State) && task.State != "SKIPPED" {
			return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
		}
		if task.State == "SKIPPED" {
			if task.PodName != "" {
				return biz.ManagedCloseEvidence{}, biz.ErrRuntimeConflict
			}
		} else if task.PodName == "" || !seenNames[task.PodName] {
			return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
		}
	}
	// A controller mutation/retry during enumeration invalidates this observation.
	for _, resource := range evidence.Resources {
		if resource.InitializationAbort == nil {
			continue
		}
		current, err := verifier.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(environment.NamespaceName).Get(ctx, resource.Name, metav1.GetOptions{})
		if err != nil || !sameObject(current, "v1", "Pod", environment.NamespaceName, resource.Name, abortedPods[resource.Name]) || current.GetResourceVersion() != resource.InitializationAbort.PodResourceVersion {
			return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
		}
	}
	current, err := workflows.Get(ctx, authority.WorkflowName, metav1.GetOptions{})
	if err != nil || !sameObject(current, "argoproj.io/v1alpha1", "Workflow", environment.NamespaceName, authority.WorkflowName, authority.WorkflowUID) || current.GetDeletionTimestamp() != nil || current.GetResourceVersion() != proof.WorkflowResourceVersion {
		return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
	}
	sort.Slice(evidence.Resources, func(i, j int) bool { return evidence.Resources[i].UID < evidence.Resources[j].UID })
	evidence.ObservedAt = time.Now().UTC()
	return evidence, nil
}

// ownerWorkflowCompletion requires the controller's completion marker and the
// full historical node set, not merely a terminal phase or a stop request.
func ownerWorkflowCompletion(workflow *unstructured.Unstructured, now time.Time) (biz.ManagedOwnerTermination, map[string]bool, error) {
	phase, _ := textAt(workflow, "status", "phase")
	finished, _ := textAt(workflow, "status", "finishedAt")
	at, err := time.Parse(time.RFC3339Nano, finished)
	if err != nil || at.IsZero() || at.After(now) || (phase != "Succeeded" && phase != "Failed" && phase != "Error") || workflow.GetResourceVersion() == "" || workflow.GetLabels()["workflows.argoproj.io/completed"] != "true" {
		return biz.ManagedOwnerTermination{}, nil, biz.ErrRuntimeNotReady
	}
	conditions, found, err := unstructured.NestedSlice(workflow.Object, "status", "conditions")
	if err != nil || !found {
		return biz.ManagedOwnerTermination{}, nil, biz.ErrRuntimeNotReady
	}
	completed := 0
	for _, item := range conditions {
		condition, ok := item.(map[string]any)
		if !ok {
			return biz.ManagedOwnerTermination{}, nil, biz.ErrRuntimeNotReady
		}
		if condition["type"] == "Completed" {
			if condition["status"] != "True" {
				return biz.ManagedOwnerTermination{}, nil, biz.ErrRuntimeNotReady
			}
			completed++
		}
	}
	if completed != 1 {
		return biz.ManagedOwnerTermination{}, nil, biz.ErrRuntimeNotReady
	}
	for _, field := range []string{"compressedNodes", "offloadNodeStatusVersion"} {
		value, _, err := unstructured.NestedString(workflow.Object, "status", field)
		if err != nil || value != "" {
			return biz.ManagedOwnerTermination{}, nil, biz.ErrRuntimeNotReady
		}
	}
	nodes, found, err := unstructured.NestedMap(workflow.Object, "status", "nodes")
	if err != nil || !found || len(nodes) == 0 {
		return biz.ManagedOwnerTermination{}, nil, biz.ErrRuntimeNotReady
	}
	fulfillablePods := make(map[string]bool)
	for id, raw := range nodes {
		node, ok := raw.(map[string]any)
		if !ok {
			return biz.ManagedOwnerTermination{}, nil, biz.ErrRuntimeNotReady
		}
		kind, kindOK := node["type"].(string)
		name, nameOK := node["name"].(string)
		if id == "" || node["id"] != id || !kindOK || kind == "" || !nameOK || name == "" {
			return biz.ManagedOwnerTermination{}, nil, biz.ErrRuntimeNotReady
		}
		switch node["phase"] {
		case "Succeeded", "Failed", "Error":
		case "Skipped", "Omitted":
			continue
		default:
			return biz.ManagedOwnerTermination{}, nil, biz.ErrRuntimeNotReady
		}
		if kind != "Pod" {
			continue
		}
		finished, ok := node["finishedAt"].(string)
		nodeFinished, err := time.Parse(time.RFC3339Nano, finished)
		if !ok || err != nil || nodeFinished.IsZero() || nodeFinished.After(at) {
			return biz.ManagedOwnerTermination{}, nil, biz.ErrRuntimeNotReady
		}
		fulfillablePods[id] = true
	}
	encoded, err := json.Marshal(nodes)
	if err != nil {
		return biz.ManagedOwnerTermination{}, nil, biz.ErrRuntimeNotReady
	}
	return biz.ManagedOwnerTermination{WorkflowPhase: phase, WorkflowFinishedAt: at.UTC(), WorkflowResourceVersion: workflow.GetResourceVersion(), WorkflowNodesSHA256: contentSHA(encoded)}, fulfillablePods, nil
}

func ownerWorkspaceReference(pod *unstructured.Unstructured, workspace *biz.WorkspaceBinding) (bool, bool) {
	if workspace == nil {
		return false, true
	}
	volumes, _, err := unstructured.NestedSlice(pod.Object, "spec", "volumes")
	if err != nil {
		return false, false
	}
	uses := false
	for _, raw := range volumes {
		volume, ok := raw.(map[string]any)
		if !ok {
			return false, false
		}
		name, _, err := unstructured.NestedString(volume, "persistentVolumeClaim", "claimName")
		if err != nil {
			return false, false
		}
		uses = uses || name == workspace.PVCName
	}
	return uses, true
}

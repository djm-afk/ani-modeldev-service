package runtimeproof

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
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
	if workspace == nil && rootAdmissionRejected(workflow, dispatch.Plan, authority.RunID, run, pods.Items) {
		unique, err := verifier.rootAdmissionWorkflow(ctx, environment.NamespaceName, authority.RunID)
		if err != nil {
			return biz.ManagedCloseEvidence{}, err
		}
		if unique.GetName() != authority.WorkflowName || string(unique.GetUID()) != authority.WorkflowUID {
			return biz.ManagedCloseEvidence{}, biz.ErrRunAuthorityConflict
		}
		current, err := workflows.Get(ctx, authority.WorkflowName, metav1.GetOptions{})
		if err != nil || !sameObject(current, "argoproj.io/v1alpha1", "Workflow", environment.NamespaceName, authority.WorkflowName, authority.WorkflowUID) || current.GetDeletionTimestamp() != nil || current.GetResourceVersion() != proof.WorkflowResourceVersion {
			return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
		}
		return biz.ManagedCloseEvidence{RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, OwnerTermination: &proof, ObservedAt: time.Now().UTC()}, nil
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
		if podMatchesServerDeniedNode(workflow, pod) {
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
		// The current native node ID is mandatory independently of Pod name.
		nodeID := pod.GetAnnotations()["workflows.argoproj.io/node-id"]
		if nodeID == "" || !nodes[nodeID] || seenNodes[nodeID] {
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
	terminatedStructuralTasks := make(map[string]bool)
	for _, task := range run.Tasks {
		structuralTermination := terminatedOwnerStructuralTask(workflow, task)
		if task.RunID != authority.RunID || task.ID == "" || task.Name == "" || seenTasks[task.ID] || (task.State != "" && !terminalTask(task.State) && task.State != "SKIPPED" && !structuralTermination) {
			return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
		}
		seenTasks[task.ID] = true
		terminatedStructuralTasks[task.ID] = structuralTermination
		// Retain the top-level Pod contract for older adapters. Child node
		// references are resolved against the independently completed graph.
		if task.PodName != "" && (task.State == "SKIPPED" || !seenNames[task.PodName]) {
			return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
		}
	}
	// The original failed KFP task and complete graph remain in the Run/history
	// proof. Only a separately proved, zero-retry, rejected-before-storage
	// executor is excluded from this projection of tasks that require Pod exits.
	// This owner-only exception must not grant a current step caller identity.
	ownerTasks := make([]kfp.ManagedTask, 0, len(run.Tasks))
	for _, task := range run.Tasks {
		if !serverRejectedManagedTask(workflow, task, run.Tasks) {
			ownerTasks = append(ownerTasks, task)
		}
	}
	tasks, err := resolvedManagedTasksForKind(workflow, ownerTasks, pods.Items, execution.Snapshot.Kind)
	if err != nil {
		return biz.ManagedCloseEvidence{}, err
	}
	for _, task := range tasks {
		if !terminalTask(task.State) && task.State != "SKIPPED" && !(task.State == "RUNNING" && terminatedStructuralTasks[task.ID]) {
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

// KFP faithfully projects the native Argo structural Running residue into
// task details. Match the complete original child-reference set to one actual
// structural node; do not rewrite that task or the original Workflow graph.
// This is used only after completed owner/Run and every actual Pod are proved.
func terminatedOwnerStructuralTask(workflow *unstructured.Unstructured, task kfp.ManagedTask) bool {
	shutdown, _ := textAt(workflow, "spec", "shutdown")
	phase, _ := textAt(workflow, "status", "phase")
	if shutdown != "Terminate" || phase != "Failed" || task.State != "RUNNING" || task.PodName != "" || len(task.ChildTasks) == 0 {
		return false
	}
	nodes, found, err := unstructured.NestedMap(workflow.Object, "status", "nodes")
	if err != nil || !found {
		return false
	}
	_, node, err := taskControllerNode(task, nodes)
	return err == nil && node["phase"] == "Running" && (node["type"] == "DAG" || node["type"] == "Retry")
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
	shutdown, _ := textAt(workflow, "spec", "shutdown")
	// Argo 3.7.3 native Terminate completes the Workflow and every actual Pod
	// while retaining onExit DAG/Retry nodes as Running. Preserve these nodes
	// in the full graph hash; never extend this exception to a real Pod.
	nativeTerminate := shutdown == "Terminate" && phase == "Failed"
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
		case "Running":
			if nativeTerminate && (kind == "DAG" || kind == "Retry") {
				continue
			}
			return biz.ManagedOwnerTermination{}, nil, biz.ErrRuntimeNotReady
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
		if serverDeniedAdmissionNode(workflow, node) {
			continue
		}
		fulfillablePods[id] = true
	}
	encoded, err := json.Marshal(nodes)
	if err != nil {
		return biz.ManagedOwnerTermination{}, nil, biz.ErrRuntimeNotReady
	}
	return biz.ManagedOwnerTermination{WorkflowPhase: phase, WorkflowFinishedAt: at.UTC(), WorkflowResourceVersion: workflow.GetResourceVersion(), WorkflowNodesSHA256: contentSHA(encoded)}, fulfillablePods, nil
}

// Argo records a Pod node before the API create call. The formal ModelDev
// CREATE-only hook can reject that create; node.startedAt is then
// controller initialization, not a process exit. Preserve the unmodified node
// in WorkflowNodesSHA256 while requiring exits only for actually created Pods.
// Kubernetes 1.35 endpoints/handlers/create.go calls mutating Admit before the
// storage Create; mutating/dispatcher.go returns InternalError on a failed-closed
// webhook call. The complete server-produced error is thus a rejected Create,
// unlike a client Create timeout whose persistence outcome remains unknown.
// An arbitrary Error, bare timeout or disappeared historical Pod proves nothing.
func serverDeniedAdmissionNode(workflow *unstructured.Unstructured, node map[string]any) bool {
	if node["type"] != "Pod" || node["phase"] != "Error" || node["templateScope"] != "local/"+workflow.GetName() || node["progress"] != "0/1" || node["taskResultSynced"] != true || !admissionRejectionWithoutProcess(node) {
		return false
	}
	name, ok := node["name"].(string)
	id, idOK := node["id"].(string)
	if !ok || !idOK || !strings.HasPrefix(name, workflow.GetName()+".") || !strings.HasPrefix(id, workflow.GetName()+"-") || workflow.GetNamespace() == "" {
		return false
	}
	expected := "task '" + name + "' errored: admission webhook \"" + workflow.GetNamespace() + ".modeldev.ani.io\" denied the request: ModelDev Pod admission refused: dependency_unavailable"
	if node["templateName"] == "system-container-driver" && node["message"] == expected {
		return true
	}
	message, ok := node["message"].(string)
	if !ok {
		return false
	}
	if node["templateName"] == "system-container-driver" || node["templateName"] == "system-dag-driver" && node["displayName"] == "root-driver" && name == workflow.GetName()+".root-driver" {
		prefix := "task '" + name + "' errored: "
		if !strings.HasPrefix(message, prefix) || !rejectedAdmissionTemplate(workflow, node["templateName"].(string), false) {
			return false
		}
		return serverWebhookAdmissionTimeout(workflow.GetNamespace(), strings.TrimPrefix(message, prefix))
	}
	return node["templateName"] == "metadata-0-2-retry-system-container-impl" && serverWebhookAdmissionTimeout(workflow.GetNamespace(), message) && rejectedExecutorHasZeroRetries(workflow, node)
}

func admissionRejectionWithoutProcess(node map[string]any) bool {
	for _, field := range []string{"hostNodeName", "podIP", "podName", "podUID", "containerID", "containerStatuses", "outputs", "resourcesDuration", "exitCode", "daemoned", "memoizationStatus"} {
		if _, found := node[field]; found {
			return false
		}
	}
	started, startedOK := node["startedAt"].(string)
	finished, finishedOK := node["finishedAt"].(string)
	start, startErr := time.Parse(time.RFC3339Nano, started)
	end, endErr := time.Parse(time.RFC3339Nano, finished)
	return startedOK && finishedOK && startErr == nil && endErr == nil && !start.IsZero() && !start.After(end)
}

func serverWebhookAdmissionTimeout(namespace, message string) bool {
	prefix := "Internal error occurred: failed calling webhook \"" + namespace + ".modeldev.ani.io\": failed to call webhook: Post \""
	const suffix = "\": context deadline exceeded"
	if namespace == "" || !strings.HasPrefix(message, prefix) || !strings.HasSuffix(message, suffix) {
		return false
	}
	rawURL := strings.TrimSuffix(strings.TrimPrefix(message, prefix), suffix)
	endpoint, err := url.Parse(rawURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Opaque != "" || endpoint.Fragment != "" || endpoint.RawFragment != "" || endpoint.Path != "/mutate" || endpoint.RawPath != "" || endpoint.Host != endpoint.Hostname()+":443" {
		return false
	}
	labels := strings.Split(endpoint.Hostname(), ".")
	if len(labels) != 3 || labels[2] != "svc" || len(validation.IsDNS1123Label(labels[0])) != 0 || len(validation.IsDNS1123Label(labels[1])) != 0 {
		return false
	}
	seconds, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(endpoint.RawQuery, "timeout="), "s"))
	// Kubernetes admissionregistration/v1 bounds timeoutSeconds to [1,30].
	return err == nil && seconds >= 1 && seconds <= 30 && endpoint.RawQuery == "timeout="+strconv.Itoa(seconds)+"s"
}

func rejectedAdmissionTemplate(workflow *unstructured.Unstructured, name string, executor bool) bool {
	for _, path := range [][]string{{"spec", "retryStrategy"}, {"spec", "templateDefaults", "retryStrategy"}} {
		if _, found, err := unstructured.NestedFieldNoCopy(workflow.Object, path...); err != nil || found {
			return false
		}
	}
	templates, found, err := unstructured.NestedSlice(workflow.Object, "spec", "templates")
	if err != nil || !found {
		return false
	}
	matches := 0
	for _, raw := range templates {
		template, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		if template["name"] != name {
			continue
		}
		matches++
		if !executor {
			if _, found := template["retryStrategy"]; found {
				return false
			}
		} else {
			strategy, ok := template["retryStrategy"].(map[string]any)
			if !ok || strategy["limit"] != "{{inputs.parameters.retry-max-count}}" {
				return false
			}
		}
	}
	return matches == 1
}

func rejectedExecutorHasZeroRetries(workflow *unstructured.Unstructured, node map[string]any) bool {
	name, _ := node["name"].(string)
	flags, ok := node["nodeFlag"].(map[string]any)
	if !strings.HasSuffix(name, ".executor(0)") || node["displayName"] != "executor(0)" || !ok || len(flags) != 1 || flags["retried"] != true || !zeroRetryInput(node) || !rejectedAdmissionTemplate(workflow, "metadata-0-2-retry-system-container-impl", true) {
		return false
	}
	nodes, found, err := unstructured.NestedMap(workflow.Object, "status", "nodes")
	if err != nil || !found {
		return false
	}
	matches := 0
	for id, raw := range nodes {
		parent, ok := raw.(map[string]any)
		if !ok || parent["name"] != strings.TrimSuffix(name, "(0)") {
			continue
		}
		matches++
		children, ok := parent["children"].([]any)
		boundary, boundaryOK := parent["boundaryID"].(string)
		if parent["id"] != id || parent["type"] != "Retry" || parent["phase"] != "Error" || parent["progress"] != "0/1" || parent["templateName"] != node["templateName"] || parent["templateScope"] != node["templateScope"] || !boundaryOK || boundary == "" || node["boundaryID"] != boundary || !ok || len(children) != 1 || children[0] != node["id"] || !zeroRetryInput(parent) || !admissionRejectionWithoutProcess(parent) || parent["startedAt"] != node["startedAt"] || parent["finishedAt"] != node["finishedAt"] || parent["message"] != "task '"+parent["name"].(string)+"' errored: "+node["message"].(string) {
			return false
		}
	}
	return matches == 1
}

func zeroRetryInput(node map[string]any) bool {
	parameters, found, err := unstructured.NestedSlice(node, "inputs", "parameters")
	if err != nil || !found {
		return false
	}
	matches := 0
	for _, raw := range parameters {
		parameter, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		if parameter["name"] == "retry-max-count" {
			matches++
			if parameter["value"] != "0" {
				return false
			}
		}
	}
	return matches == 1
}

func serverRejectedManagedTask(workflow *unstructured.Unstructured, task kfp.ManagedTask, tasks []kfp.ManagedTask) bool {
	if task.State != "FAILED" || task.PodName != "" {
		return false
	}
	nodes, found, err := unstructured.NestedMap(workflow.Object, "status", "nodes")
	if err != nil || !found {
		return false
	}
	id, controller, err := taskControllerNode(task, nodes)
	if err != nil || controller["type"] != "DAG" || controller["phase"] != "Error" || controller["templateName"] != "metadata-0-2-retry-system-container-executor" || controller["templateScope"] != "local/"+workflow.GetName() || controller["progress"] != "0/1" || !admissionRejectionWithoutProcess(controller) {
		return false
	}
	children, ok := controller["children"].([]any)
	outbound, outboundOK := controller["outboundNodes"].([]any)
	if !ok || len(children) != 1 || !outboundOK || len(outbound) != 1 {
		return false
	}
	matches := 0
	for executorID, raw := range nodes {
		node := raw.(map[string]any)
		if node["boundaryID"] != id || node["type"] != "Pod" {
			continue
		}
		matches++
		if !serverDeniedAdmissionNode(workflow, node) || node["templateName"] != "metadata-0-2-retry-system-container-impl" || node["name"] != controller["name"].(string)+".executor(0)" || outbound[0] != executorID {
			return false
		}
		members := 0
		for _, original := range tasks {
			for _, ref := range original.ChildTasks {
				if ref.PodName != executorID {
					continue
				}
				members++
				if ref.TaskID != "" || original.RunID != task.RunID || original.Name != "executor" || original.State != "FAILED" || original.PodName != "" || len(original.ChildTasks) != 1 {
					return false
				}
				parentID, _, err := taskControllerNode(original, nodes)
				if err != nil || children[0] != parentID {
					return false
				}
			}
		}
		if members != 1 {
			return false
		}
	}
	return matches == 1
}

func podMatchesServerDeniedNode(workflow *unstructured.Unstructured, pod *unstructured.Unstructured) bool {
	nodes, found, err := unstructured.NestedMap(workflow.Object, "status", "nodes")
	if err != nil || !found {
		return false
	}
	annotations := pod.GetAnnotations()
	for id, raw := range nodes {
		node, ok := raw.(map[string]any)
		if !ok || !serverDeniedAdmissionNode(workflow, node) {
			continue
		}
		if pod.GetName() == id || annotations["workflows.argoproj.io/node-id"] == id || annotations["workflows.argoproj.io/node-name"] == node["name"] {
			return true
		}
	}
	return false
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

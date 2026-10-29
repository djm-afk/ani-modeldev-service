package runtimeproof

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

const stepExitEvidenceFinalizer = "modeldev.ani.io/step-exit-evidence"

var _ biz.StepExitCleanupBoundary = (*Verifier)(nil)
var _ biz.RecordedOwnerWriterVerifier = (*Verifier)(nil)

// VerifyRecordedOwnerWritersAbsent retains the full historical graph while
// independently enumerating current PVC writers. Only exact already-proved Pod
// UIDs may be absent; a changed name/UID, graph or live writer is still blocked.
func (verifier *Verifier) VerifyRecordedOwnerWritersAbsent(ctx context.Context, execution biz.Execution, authority biz.RunAuthorityCandidate, workspace *biz.WorkspaceBinding, evidence biz.ManagedCloseEvidence) error {
	return verifier.verifyRecordedOwnerWritersAbsent(ctx, execution, authority, workspace, evidence, false)
}

func (verifier *Verifier) verifyRecordedOwnerWritersAbsent(ctx context.Context, execution biz.Execution, authority biz.RunAuthorityCandidate, workspace *biz.WorkspaceBinding, evidence biz.ManagedCloseEvidence, workflowRemovalRecorded bool) error {
	if verifier == nil || verifier.kube == nil || verifier.runs == nil || verifier.plans == nil || ctx == nil || ctx.Err() != nil || evidence.OwnerTermination == nil || len(evidence.OwnerTermination.WorkflowNodesSHA256) != 64 || evidence.RunID != authority.RunID || evidence.WorkflowUID != authority.WorkflowUID {
		return biz.ErrRuntimeNotReady
	}
	if _, _, err := execution.CanonicalPayloads(); err != nil {
		return biz.ErrRuntimeConflict
	}
	environment := execution.Snapshot.Environment
	if authority.TenantID != execution.TenantID || authority.ExecutionID != execution.ExecutionID || authority.OperationID != execution.OperationID || authority.SpecHash != execution.SpecHash || authority.NamespaceName != environment.NamespaceName || authority.NamespaceUID != environment.NamespaceUID {
		return biz.ErrRuntimeConflict
	}
	dispatch, err := verifier.plans.Get(ctx, execution.TenantID, execution.ExecutionID)
	if err != nil {
		return err
	}
	frozen, err := (biz.PipelineDispatchRequest{Admission: execution.Admission, Owner: dispatch.Plan.Owner}).Freeze()
	if err != nil {
		return biz.ErrRuntimeConflict
	}
	wanted, err := frozen.Digest()
	actual, actualErr := dispatch.Plan.Digest()
	if err != nil || actualErr != nil || wanted != actual || actual != authority.PlanHash || actual != dispatch.PlanHash || dispatch.AttemptID != authority.AttemptID || dispatch.State != biz.PipelineDispatchConfirmed || len(dispatch.ConfirmedRuns) != 1 || dispatch.ConfirmedRuns[0].RunID != authority.RunID {
		return biz.ErrRuntimeConflict
	}
	run, err := verifier.runs.GetManagedRun(ctx, dispatch.Plan, authority.RunID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if !terminalTask(run.State) || run.State != evidence.OwnerTermination.RunState || !run.FinishedAt.Equal(evidence.OwnerTermination.RunFinishedAt) || run.FinishedAt.After(now) {
		return biz.ErrRuntimeNotReady
	}
	namespace, err := verifier.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, environment.NamespaceName, metav1.GetOptions{})
	if err != nil || !sameObject(namespace, "v1", "Namespace", "", environment.NamespaceName, environment.NamespaceUID) || namespace.GetDeletionTimestamp() != nil {
		return biz.ErrRuntimeNotReady
	}
	workflows := verifier.kube.Resource(schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}).Namespace(environment.NamespaceName)
	workflow, err := workflows.Get(ctx, authority.WorkflowName, metav1.GetOptions{})
	if !(apierrors.IsNotFound(err) && workflowRemovalRecorded) {
		if err != nil || !sameObject(workflow, "argoproj.io/v1alpha1", "Workflow", environment.NamespaceName, authority.WorkflowName, authority.WorkflowUID) || workflow.GetDeletionTimestamp() != nil {
			return biz.ErrRuntimeNotReady
		}
		completion, nodes, err := ownerWorkflowCompletion(workflow, now)
		if err != nil || completion.WorkflowNodesSHA256 != evidence.OwnerTermination.WorkflowNodesSHA256 || completion.WorkflowPhase != evidence.OwnerTermination.WorkflowPhase || !completion.WorkflowFinishedAt.Equal(evidence.OwnerTermination.WorkflowFinishedAt) || len(nodes) != len(evidence.Resources) {
			return biz.ErrRuntimeNotReady
		}
	}
	if workspace != nil {
		if err := biz.ValidateWorkspaceBinding(execution, *workspace); err != nil {
			return err
		}
		pvc, err := verifier.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(environment.NamespaceName).Get(ctx, workspace.PVCName, metav1.GetOptions{})
		if err != nil || !sameObject(pvc, "v1", "PersistentVolumeClaim", environment.NamespaceName, workspace.PVCName, workspace.PVCUID) || pvc.GetDeletionTimestamp() != nil {
			return biz.ErrRuntimeNotReady
		}
	}
	if evidence.UnpreparedWorkspace != nil {
		if workspace != nil {
			return biz.ErrRuntimeConflict
		}
		if err := verifier.verifyUnpreparedWorkspace(ctx, execution, authority, evidence.UnpreparedWorkspace, workflowRemovalRecorded); err != nil {
			return err
		}
	}
	writerWorkspace := ownerWorkspaceForEvidence(workspace, evidence.UnpreparedWorkspace)
	byName, byUID := map[string]biz.RuntimeResource{}, map[string]bool{}
	for _, resource := range evidence.Resources {
		if resource.APIVersion != "v1" || resource.Kind != "Pod" || resource.Namespace != environment.NamespaceName || resource.Name == "" || resource.UID == "" || resource.OwnerUID != authority.WorkflowUID || !resource.Terminal || !resource.APIObjectPresent || byName[resource.Name].UID != "" || byUID[resource.UID] || resource.ExitCode == nil && resource.InitializationAbort == nil && resource.SandboxTermination == nil {
			return biz.ErrRuntimeConflict
		}
		if resource.SandboxTermination != nil && (writerWorkspace != nil || resource.ExitCode != nil || resource.InitializationAbort != nil || !resource.CreationDisabled || !resource.SandboxTermination.ValidAt(evidence.ObservedAt)) {
			return biz.ErrRuntimeConflict
		}
		byName[resource.Name], byUID[resource.UID] = resource, true
	}
	pods, err := verifier.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(environment.NamespaceName).List(ctx, metav1.ListOptions{})
	if err != nil || pods == nil || pods.GetContinue() != "" {
		return biz.ErrRuntimeNotReady
	}
	association := biz.ManagedStepAssociation{NamespaceName: authority.NamespaceName, NamespaceUID: authority.NamespaceUID, WorkflowName: authority.WorkflowName, WorkflowUID: authority.WorkflowUID}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.GetAPIVersion() != "v1" || pod.GetKind() != "Pod" || pod.GetNamespace() != environment.NamespaceName {
			return biz.ErrRuntimeConflict
		}
		if expected, found := byName[pod.GetName()]; found {
			if string(pod.GetUID()) != expected.UID || !workflowOwner(pod, association) || !recordedStepTerminated(pod, expected, now) {
				return biz.ErrRuntimeConflict
			}
			account, _ := textAt(pod, "spec", "serviceAccountName")
			if account != environment.Identities.KFPStepServiceAccount {
				return biz.ErrRuntimeConflict
			}
			continue
		}
		uses, valid := ownerWorkspaceReference(pod, writerWorkspace)
		if !valid || workflowOwner(pod, association) {
			return biz.ErrRuntimeConflict
		}
		for _, owner := range pod.GetOwnerReferences() {
			if string(owner.UID) == authority.WorkflowUID {
				return biz.ErrRuntimeConflict
			}
		}
		if uses {
			terminal, _, completed, _ := terminatedPod(pod)
			if !terminal || completed.After(now) {
				return biz.ErrRuntimeNotReady
			}
		}
	}
	return nil
}

func recordedStepTerminated(pod *unstructured.Unstructured, expected biz.RuntimeResource, now time.Time) bool {
	if expected.SandboxTermination != nil {
		actual := observeSandboxTermination(pod, now, false)
		if actual == nil || expected.ExitCode != nil || expected.InitializationAbort != nil {
			return false
		}
		original := *expected.SandboxTermination
		original.PodResourceVersion, actual.PodResourceVersion = "", ""
		return reflect.DeepEqual(original, *actual)
	}
	if expected.InitializationAbort != nil {
		actual := observeInitializationAbort(pod, now, false)
		if actual == nil {
			return false
		}
		original := *expected.InitializationAbort
		original.PodResourceVersion, actual.PodResourceVersion = "", ""
		return reflect.DeepEqual(original, *actual)
	}
	terminal, _, completed, code := terminatedPod(pod)
	return terminal && code != nil && expected.ExitCode != nil && *code == *expected.ExitCode && !completed.After(now)
}

// ReconcileStepExitCleanup is invoked only after BeginStepExitCleanup committed.
// A retried request uses the same UID, finalizer list and resourceVersion. The
// complete durable exit receipt survives a successful request with a lost reply.
func (verifier *Verifier) ReconcileStepExitCleanup(ctx context.Context, execution biz.Execution, authority biz.RunAuthorityCandidate, workspace *biz.WorkspaceBinding, receipt biz.StepExitCleanupReceipt) ([]biz.RuntimeResource, error) {
	if receipt.StartedAt.IsZero() || receipt.CloseGeneration == 0 || receipt.CompletedAt != nil {
		return nil, biz.ErrRuntimeConflict
	}
	if err := verifier.VerifyRecordedOwnerWritersAbsent(ctx, execution, authority, workspace, receipt.Evidence); err != nil {
		return nil, err
	}
	client := verifier.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(authority.NamespaceName)
	removed := []biz.RuntimeResource{}
	association := biz.ManagedStepAssociation{NamespaceName: authority.NamespaceName, NamespaceUID: authority.NamespaceUID, WorkflowName: authority.WorkflowName, WorkflowUID: authority.WorkflowUID}
	for _, expected := range receipt.Evidence.Resources {
		pod, err := client.Get(ctx, expected.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			fact := expected
			fact.APIObjectPresent = false
			removed = append(removed, fact)
			continue
		}
		if err != nil || !sameObject(pod, "v1", "Pod", authority.NamespaceName, expected.Name, expected.UID) || !workflowOwner(pod, association) || !recordedStepTerminated(pod, expected, time.Now().UTC()) || pod.GetResourceVersion() == "" {
			return nil, biz.ErrRuntimeConflict
		}
		finalizers := pod.GetFinalizers()
		remaining := []string{}
		retained := false
		for _, finalizer := range finalizers {
			if finalizer == stepExitEvidenceFinalizer {
				retained = true
			} else {
				remaining = append(remaining, finalizer)
			}
		}
		if retained {
			owners, _, _ := unstructured.NestedSlice(pod.Object, "metadata", "ownerReferences")
			patch, err := json.Marshal([]map[string]any{{"op": "test", "path": "/metadata/uid", "value": expected.UID}, {"op": "test", "path": "/metadata/resourceVersion", "value": pod.GetResourceVersion()}, {"op": "test", "path": "/metadata/ownerReferences", "value": owners}, {"op": "test", "path": "/metadata/finalizers", "value": finalizers}, {"op": "replace", "path": "/metadata/finalizers", "value": remaining}})
			if err != nil {
				return nil, biz.ErrRuntimeNotReady
			}
			pod, err = client.Patch(ctx, expected.Name, types.JSONPatchType, patch, metav1.PatchOptions{})
			if err != nil {
				return nil, biz.ErrCleanupUncertain
			}
			if !sameObject(pod, "v1", "Pod", authority.NamespaceName, expected.Name, expected.UID) || !workflowOwner(pod, association) || !slices.Equal(pod.GetFinalizers(), remaining) || !recordedStepTerminated(pod, expected, time.Now().UTC()) {
				return nil, biz.ErrRuntimeConflict
			}
		}
		// Never remove a foreign controller's finalizer or delete around it.
		if len(pod.GetFinalizers()) != 0 {
			return nil, biz.ErrCleanupBlocked
		}
		if pod.GetDeletionTimestamp() == nil {
			uid, version := types.UID(expected.UID), pod.GetResourceVersion()
			orphan := metav1.DeletePropagationOrphan
			if err := client.Delete(ctx, expected.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}, PropagationPolicy: &orphan}); err != nil {
				return nil, biz.ErrCleanupUncertain
			}
		}
		current, err := client.Get(ctx, expected.Name, metav1.GetOptions{})
		if !apierrors.IsNotFound(err) {
			if err == nil && current != nil && string(current.GetUID()) != expected.UID {
				return nil, biz.ErrRuntimeConflict
			}
			return nil, biz.ErrCleanupUncertain
		}
		fact := expected
		fact.APIObjectPresent = false
		removed = append(removed, fact)
	}
	if err := verifier.VerifyRecordedOwnerWritersAbsent(ctx, execution, authority, workspace, receipt.Evidence); err != nil {
		return nil, err
	}
	return removed, nil
}

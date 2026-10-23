package runtimeproof

import (
	"context"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/cleanup"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

var _ biz.ComputeCleanupBoundary = (*Verifier)(nil)
var _ biz.RecordedComputeWriterVerifier = (*Verifier)(nil)

func (verifier *Verifier) VerifyRecordedComputeWritersAbsent(ctx context.Context, execution biz.Execution, owner biz.RunAuthorityCandidate, state biz.ExecutionRuntime) error {
	if !biz.ComputeCleanupApplied(biz.OperationRecord{QueryRecord: biz.QueryRecord{Execution: execution, Runtime: state}, Authority: &owner}) {
		return biz.ErrCleanupBlocked
	}
	return verifier.verifyComputeCleanupGraph(ctx, execution, owner, state, true)
}

func (verifier *Verifier) WithTrainingLogs(reader biz.TrainingLogReader) *Verifier {
	verifier.logs = reader
	return verifier
}

func (verifier *Verifier) CaptureComputeCleanupLogs(ctx context.Context, execution biz.Execution, owner biz.RunAuthorityCandidate, state biz.ExecutionRuntime) ([]biz.RecordedTrainingLog, error) {
	if verifier == nil || state.ComputeCleanup == nil || state.ComputeCleanup.LogsReady || state.ComputeCleanup.Attempts == 0 {
		return nil, biz.ErrCleanupBlocked
	}
	if err := verifier.verifyComputeCleanupGraph(ctx, execution, owner, state, false); err != nil {
		return nil, err
	}
	logs := []biz.RecordedTrainingLog{}
	if state.Training == nil || state.TrainingRejection != nil {
		return logs, nil
	}
	// Reuse the real training observer before disposing any parent. Its full
	// original owner chain, historical exits and creation stop remain required.
	if verifier.training == nil {
		return nil, biz.ErrRuntimeNotReady
	}
	fresh, err := verifier.training.ObserveTraining(ctx, *state.Training, *state.TrainingHandle, state.Observation.Resources)
	if err != nil || !fresh.WritersAbsent || !reflect.DeepEqual(fresh.Resources, state.Observation.Resources) {
		return nil, biz.ErrCleanupBlocked
	}
	for _, pod := range state.Observation.Resources {
		if pod.Kind != "Pod" || !pod.APIObjectPresent {
			continue
		}
		if verifier.logs == nil {
			return nil, biz.ErrTrainingLogsUnavailable
		}
		logID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(execution.ExecutionID+":"+pod.UID+":node")).String()
		source, err := biz.TrainingLogSourceFor(biz.QueryRecord{Execution: execution, Runtime: state}, logID)
		if err != nil {
			return nil, err
		}
		result, err := verifier.logs.ReadTrainingLogs(ctx, source, biz.TrainingLogOptions{TailLines: 1000, MaxBytes: 65536})
		if err != nil {
			return nil, err
		}
		logs = append(logs, biz.RecordedTrainingLog{Source: source, Result: result})
	}
	if !biz.ComputeCleanupLogsComplete(execution, state, logs) {
		return nil, biz.ErrTrainingLogsUnavailable
	}
	return logs, nil
}

func (verifier *Verifier) ReconcileComputeCleanup(ctx context.Context, execution biz.Execution, owner biz.RunAuthorityCandidate, state biz.ExecutionRuntime) ([]biz.RuntimeResource, error) {
	removed := []biz.RuntimeResource{}
	receipt := state.ComputeCleanup
	if receipt == nil || !receipt.LogsReady || receipt.Attempts == 0 || receipt.Attempts > biz.ComputeCleanupAttemptLimit(receipt) || receipt.Phase != "STARTED" || !biz.ComputeCleanupLogsComplete(execution, state, receipt.Logs) {
		return removed, biz.ErrCleanupBlocked
	}
	if retry := receipt.OperatorRetry; retry != nil {
		if !time.Now().Before(retry.DeadlineAt) {
			return removed, biz.ErrCleanupBlocked
		}
		bounded, cancel := context.WithDeadline(ctx, retry.DeadlineAt)
		defer cancel()
		ctx = bounded
	}
	targets, hash, err := biz.ComputeCleanupTargets(execution, owner, state)
	if err != nil || hash != receipt.PlanHash || !reflect.DeepEqual(targets, receipt.Targets) || !reflect.DeepEqual(targets, receipt.Requested) {
		return removed, biz.ErrCleanupConflict
	}
	dispatch, err := verifier.plans.Get(ctx, execution.TenantID, execution.ExecutionID)
	if err != nil {
		return removed, err
	}
	record := biz.OperationRecord{QueryRecord: biz.QueryRecord{Execution: execution, Runtime: state}, Dispatch: &dispatch, Authority: &owner}
	controllers := cleanup.New(verifier.kube, verifier)
	for _, target := range receipt.Targets {
		if retry := receipt.OperatorRetry; retry != nil && !time.Now().Before(retry.DeadlineAt) {
			return removed, biz.ErrCleanupBlocked
		}
		if err := verifier.verifyComputeCleanupGraph(ctx, execution, owner, state, true); err != nil {
			return removed, err
		}
		resource, _ := computeResource(target)
		client := verifier.kube.Resource(resource).Namespace(owner.NamespaceName)
		current, err := client.Get(ctx, target.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			fact := target
			fact.APIObjectPresent = false
			removed = append(removed, fact)
			continue
		}
		if err != nil || !sameObject(current, target.APIVersion, target.Kind, target.Namespace, target.Name, target.UID) {
			return removed, biz.ErrCleanupConflict
		}
		if target.Kind != "Workflow" && target.Kind != "Pod" && target.Kind != "PodGroup" && target.Kind != "TrainingRuntime" {
			if err := controllers.DeleteRecordedComputeController(ctx, record, target); err != nil {
				return removed, err
			}
		} else {
			if target.Kind == "TrainingRuntime" {
				jobs, listErr := verifier.kube.Resource(schema.GroupVersionResource{Group: "trainer.kubeflow.org", Version: "v1alpha1", Resource: "trainjobs"}).Namespace(owner.NamespaceName).List(ctx, metav1.ListOptions{})
				if listErr != nil || jobs == nil || jobs.GetContinue() != "" {
					return removed, biz.ErrCleanupUncertain
				}
				for _, job := range jobs.Items {
					name, _, _ := unstructured.NestedString(job.Object, "spec", "runtimeRef", "name")
					kind, _, _ := unstructured.NestedString(job.Object, "spec", "runtimeRef", "kind")
					if name == target.Name && kind == "TrainingRuntime" {
						return removed, biz.ErrCleanupBlocked
					}
				}
			}
			if len(current.GetFinalizers()) != 0 || current.GetResourceVersion() == "" {
				return removed, biz.ErrCleanupBlocked
			}
			if target.Kind == "Pod" || target.Kind == "PodGroup" {
				if err := verifier.computeParentsAbsent(ctx, receipt.Targets, target); err != nil {
					return removed, err
				}
			}
			uid, version := types.UID(target.UID), current.GetResourceVersion()
			orphan := metav1.DeletePropagationOrphan
			if err := client.Delete(ctx, target.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}, PropagationPolicy: &orphan}); err != nil {
				return removed, biz.ErrCleanupUncertain
			}
			if _, err := client.Get(ctx, target.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				return removed, biz.ErrCleanupUncertain
			}
		}
		fact := target
		fact.APIObjectPresent = false
		removed = append(removed, fact)
	}
	if err := verifier.verifyComputeCleanupGraph(ctx, execution, owner, state, true); err != nil {
		return removed, err
	}
	return removed, nil
}

func (verifier *Verifier) verifyComputeCleanupGraph(ctx context.Context, execution biz.Execution, owner biz.RunAuthorityCandidate, state biz.ExecutionRuntime, removalIntent bool) error {
	if verifier == nil || verifier.kube == nil || verifier.plans == nil || ctx == nil || ctx.Err() != nil || state.ComputeCleanup == nil {
		return biz.ErrRuntimeNotReady
	}
	targets, hash, err := biz.ComputeCleanupTargets(execution, owner, state)
	if err != nil || state.ComputeCleanup.PlanHash != hash || !reflect.DeepEqual(state.ComputeCleanup.Targets, targets) {
		return biz.ErrCleanupConflict
	}
	if removalIntent && (!state.ComputeCleanup.LogsReady || !reflect.DeepEqual(targets, state.ComputeCleanup.Requested)) {
		return biz.ErrCleanupBlocked
	}
	if err := verifier.verifyRecordedOwnerWritersAbsent(ctx, execution, owner, state.Workspace, state.StepExitCleanup.Evidence, removalIntent); err != nil {
		return err
	}
	byUID := map[string]biz.RuntimeResource{}
	for _, target := range targets {
		byUID[target.UID] = target
	}
	// Background GC follows every ownerReference, including non-controller
	// references. The protected work volume must never belong to that closure.
	if state.Workspace != nil {
		pvc, err := verifier.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(owner.NamespaceName).Get(ctx, state.Workspace.PVCName, metav1.GetOptions{})
		if err != nil || pvc == nil || string(pvc.GetUID()) != state.Workspace.PVCUID || pvc.GetDeletionTimestamp() != nil {
			return biz.ErrCleanupBlocked
		}
		for _, reference := range pvc.GetOwnerReferences() {
			if _, managed := byUID[string(reference.UID)]; managed {
				return biz.ErrCleanupBlocked
			}
		}
	}
	for _, target := range targets {
		resource, valid := computeResource(target)
		if !valid {
			return biz.ErrCleanupConflict
		}
		object, err := verifier.kube.Resource(resource).Namespace(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) && (removalIntent || target.Kind == "TrainingRuntime") {
			continue
		}
		if err != nil || !sameObject(object, target.APIVersion, target.Kind, target.Namespace, target.Name, target.UID) || object.GetResourceVersion() == "" {
			return biz.ErrCleanupConflict
		}
		if object.GetDeletionTimestamp() != nil {
			if !removalIntent {
				return biz.ErrCleanupBlocked
			}
			if len(object.GetFinalizers()) != 0 {
				return biz.ErrCleanupBlocked
			}
			if err := verifier.waitComputeRemoval(ctx, target); err != nil {
				return err
			}
			continue
		}
		if target.Kind == "Workflow" {
			continue // Complete node hash/state/UID checked independently above.
		}
		if target.Kind == "TrainingRuntime" {
			if len(object.GetOwnerReferences()) != 0 || object.GetAnnotations()["modeldev.ani.io/execution-id"] != execution.ExecutionID || object.GetAnnotations()["modeldev.ani.io/tenant-id"] != execution.TenantID || object.GetAnnotations()["modeldev.ani.io/execution-spec-sha256"] != execution.SpecHash {
				return biz.ErrCleanupConflict
			}
			continue
		}
		ownerUID := ""
		for _, reference := range object.GetOwnerReferences() {
			if reference.Controller != nil && *reference.Controller {
				if ownerUID != "" {
					return biz.ErrCleanupConflict
				}
				ownerUID = string(reference.UID)
			}
		}
		if ownerUID != target.OwnerUID {
			if !removalIntent || ownerUID != "" || target.OwnerUID == "" || verifier.computeParentsAbsent(ctx, targets, target) != nil {
				return biz.ErrCleanupConflict
			}
		}
		if target.Kind == "Pod" {
			if !recordedTrainingTerminated(object, target, time.Now().UTC()) {
				return biz.ErrCleanupBlocked
			}
			account, _ := textAt(object, "spec", "serviceAccountName")
			if account != execution.Snapshot.Environment.Identities.TrainerServiceAccount || len(object.GetFinalizers()) != 0 {
				return biz.ErrCleanupBlocked
			}
		} else if target.Kind != "PodGroup" && !computeControllerStopped(object) {
			return biz.ErrCleanupBlocked
		}
	}
	// Lists cannot hide an additional historical child or unrelated PVC writer.
	for _, resource := range []schema.GroupVersionResource{{Version: "v1", Resource: "pods"}, {Group: "batch", Version: "v1", Resource: "jobs"}, {Group: "jobset.x-k8s.io", Version: "v1alpha2", Resource: "jobsets"}, {Group: "scheduling.volcano.sh", Version: "v1beta1", Resource: "podgroups"}} {
		objects, err := verifier.kube.Resource(resource).Namespace(owner.NamespaceName).List(ctx, metav1.ListOptions{})
		if err != nil || objects == nil || objects.GetContinue() != "" {
			return biz.ErrRuntimeNotReady
		}
		for i := range objects.Items {
			object := &objects.Items[i]
			if _, known := byUID[string(object.GetUID())]; known {
				continue
			}
			for _, reference := range object.GetOwnerReferences() {
				if _, known := byUID[string(reference.UID)]; known {
					return biz.ErrCleanupConflict
				}
			}
			if resource.Resource == "pods" {
				uses, valid := ownerWorkspaceReference(object, state.Workspace)
				if !valid || uses {
					return biz.ErrCleanupBlocked
				}
			}
		}
	}
	return nil
}

// A committed original deletion may still be undergoing ordinary API/GC work.
// Wait within the existing owner reconciliation budget; never issue another
// DELETE or record absence from deletionTimestamp alone.
func (verifier *Verifier) waitComputeRemoval(ctx context.Context, target biz.RuntimeResource) error {
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resource, valid := computeResource(target)
	if !valid {
		return biz.ErrCleanupConflict
	}
	for {
		current, err := verifier.kube.Resource(resource).Namespace(target.Namespace).Get(wait, target.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return biz.ErrCleanupUncertain
		}
		if !sameObject(current, target.APIVersion, target.Kind, target.Namespace, target.Name, target.UID) {
			return biz.ErrCleanupConflict
		}
		if len(current.GetFinalizers()) != 0 {
			return biz.ErrCleanupBlocked
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-wait.Done():
			timer.Stop()
			return biz.ErrCleanupUncertain
		case <-timer.C:
		}
	}
}

func (verifier *Verifier) computeParentsAbsent(ctx context.Context, targets []biz.RuntimeResource, child biz.RuntimeResource) error {
	for child.OwnerUID != "" {
		var parent biz.RuntimeResource
		for _, target := range targets {
			if target.UID == child.OwnerUID {
				parent = target
			}
		}
		resource, valid := computeResource(parent)
		if !valid || parent.Kind == "Pod" || parent.Kind == "Workflow" {
			return biz.ErrCleanupConflict
		}
		if _, err := verifier.kube.Resource(resource).Namespace(parent.Namespace).Get(ctx, parent.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			return biz.ErrCleanupConflict
		}
		child = parent
	}
	return nil
}

func recordedTrainingTerminated(pod *unstructured.Unstructured, expected biz.RuntimeResource, now time.Time) bool {
	terminal, _, _, _ := terminatedPod(pod)
	if !terminal || expected.ExitCode == nil {
		return false
	}
	node := false
	for _, field := range []string{"containerStatuses", "initContainerStatuses", "ephemeralContainerStatuses"} {
		statuses, _, _ := unstructured.NestedSlice(pod.Object, "status", field)
		for _, item := range statuses {
			status, ok := item.(map[string]any)
			if !ok {
				return false
			}
			when, _, _ := unstructured.NestedString(status, "state", "terminated", "finishedAt")
			at, err := time.Parse(time.RFC3339Nano, when)
			if err != nil || at.IsZero() || at.After(now) {
				return false
			}
			if status["name"] == "node" && field == "containerStatuses" {
				code, found, _ := unstructured.NestedInt64(status, "state", "terminated", "exitCode")
				if node || !found || code != int64(*expected.ExitCode) {
					return false
				}
				node = true
			}
		}
	}
	return node
}

func computeControllerStopped(object *unstructured.Unstructured) bool {
	if suspended, _, err := unstructured.NestedBool(object.Object, "spec", "suspend"); err == nil && suspended {
		return true
	}
	wanted := "Complete"
	if object.GetKind() == "JobSet" {
		wanted = "Completed"
	}
	conditions, _, err := unstructured.NestedSlice(object.Object, "status", "conditions")
	if err != nil {
		return false
	}
	seen, count := map[string]bool{}, 0
	for _, item := range conditions {
		condition, ok := item.(map[string]any)
		if !ok {
			return false
		}
		kind, _ := condition["type"].(string)
		if kind == wanted || kind == "Failed" {
			if seen[kind] {
				return false
			}
			seen[kind] = true
			if condition["status"] == "True" {
				count++
			}
		}
	}
	return count == 1
}

func computeResource(target biz.RuntimeResource) (schema.GroupVersionResource, bool) {
	switch target.Kind {
	case "Workflow":
		return schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}, target.APIVersion == "argoproj.io/v1alpha1"
	case "TrainJob":
		return schema.GroupVersionResource{Group: "trainer.kubeflow.org", Version: "v1alpha1", Resource: "trainjobs"}, target.APIVersion == "trainer.kubeflow.org/v1alpha1"
	case "TrainingRuntime":
		return schema.GroupVersionResource{Group: "trainer.kubeflow.org", Version: "v1alpha1", Resource: "trainingruntimes"}, target.APIVersion == "trainer.kubeflow.org/v1alpha1"
	case "JobSet":
		return schema.GroupVersionResource{Group: "jobset.x-k8s.io", Version: "v1alpha2", Resource: "jobsets"}, target.APIVersion == "jobset.x-k8s.io/v1alpha2"
	case "PodGroup":
		return schema.GroupVersionResource{Group: "scheduling.volcano.sh", Version: "v1beta1", Resource: "podgroups"}, target.APIVersion == "scheduling.volcano.sh/v1beta1"
	case "Job":
		return schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, target.APIVersion == "batch/v1"
	case "Pod":
		return schema.GroupVersionResource{Version: "v1", Resource: "pods"}, target.APIVersion == "v1"
	}
	return schema.GroupVersionResource{}, false
}

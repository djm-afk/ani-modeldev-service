// Package cleanup implements bounded exact-UID disposal of closed CPU-P01
// controller objects. It never deletes Pods, work volumes, or publications.
package cleanup

import (
	"context"
	"reflect"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

type Adapter struct {
	kube    dynamic.Interface
	writers biz.OwnerWriterVerifier
}

func New(kube dynamic.Interface, writers biz.OwnerWriterVerifier) *Adapter {
	return &Adapter{kube: kube, writers: writers}
}

// VerifyCleanupResolved reconciles an existing cleanup audit through reads only.
// A prior DELETE outcome is never inferred from that audit or retried here.
func (adapter *Adapter) VerifyCleanupResolved(ctx context.Context, record biz.OperationRecord) ([]biz.RuntimeResource, error) {
	if ctx == nil || ctx.Err() != nil || adapter == nil || adapter.kube == nil {
		return nil, biz.ErrRuntimeNotReady
	}
	plan, err := biz.CleanupPlanFor(record)
	if err != nil {
		return nil, err
	}
	namespace := record.Execution.Snapshot.Environment.NamespaceName
	ns, err := adapter.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, namespace, metav1.GetOptions{})
	if err != nil || ns == nil || namespace == "" || plan.NamespaceUID == "" || ns.GetAPIVersion() != "v1" || ns.GetKind() != "Namespace" || ns.GetNamespace() != "" || ns.GetName() != namespace || string(ns.GetUID()) != plan.NamespaceUID || ns.GetDeletionTimestamp() != nil {
		return nil, biz.ErrCleanupBlocked
	}
	if record.Runtime.CloseEvidence.NoDispatch {
		if record.Dispatch != nil || record.Authority != nil || record.Runtime.Training != nil || record.Runtime.TrainingHandle != nil || record.Runtime.Workspace != nil || record.Runtime.Observation != nil || record.Runtime.Publication != nil || len(plan.Targets) != 0 || len(record.Runtime.CloseEvidence.Resources) != 0 || record.Runtime.CloseEvidence.RunID != "" || record.Runtime.CloseEvidence.WorkflowUID != "" {
			return nil, biz.ErrCleanupBlocked
		}
		return []biz.RuntimeResource{}, nil
	}
	if record.Authority == nil || adapter.writers == nil || record.Runtime.CloseEvidence.RunID != record.Authority.RunID || record.Runtime.CloseEvidence.WorkflowUID != record.Authority.WorkflowUID || len(record.Runtime.CloseEvidence.Resources) == 0 {
		return nil, biz.ErrCleanupBlocked
	}
	// Unlike the pre-delete check, this proof does not require disposed training
	// controllers to remain present. The frozen original Run/Workflow and every
	// current PVC writer are still independently checked by the owner verifier.
	proof, err := adapter.ownerWriterProof(ctx, record)
	if err != nil {
		return nil, err
	}
	for _, expected := range record.Runtime.CloseEvidence.Resources {
		matched := false
		for _, current := range proof.Resources {
			matched = matched || expected.OwnerUID == record.Authority.WorkflowUID && sameRetainedStepExit(expected, current)
		}
		if !matched {
			return nil, biz.ErrCleanupBlocked
		}
	}
	absent := make(map[string]biz.RuntimeResource, len(plan.Targets))
	for _, target := range plan.Targets {
		resource, valid := targetResource(target)
		if !valid || target.Namespace != namespace {
			return nil, biz.ErrCleanupConflict
		}
		current, err := adapter.kube.Resource(resource).Namespace(namespace).Get(ctx, target.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			absent[target.UID] = target
			continue
		}
		if err != nil || current == nil {
			return nil, biz.ErrCleanupUncertain
		}
		if !exactObject(current, target) {
			return nil, biz.ErrCleanupConflict
		}
		return nil, biz.ErrCleanupUncertain
	}
	if workspace := record.Runtime.Workspace; workspace != nil {
		pvc, err := adapter.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(namespace).Get(ctx, workspace.PVCName, metav1.GetOptions{})
		if err != nil || pvc == nil || workspace.NamespaceName != namespace || workspace.NamespaceUID != plan.NamespaceUID || pvc.GetAPIVersion() != "v1" || pvc.GetKind() != "PersistentVolumeClaim" || pvc.GetNamespace() != namespace || pvc.GetName() != workspace.PVCName || workspace.PVCUID == "" || string(pvc.GetUID()) != workspace.PVCUID || pvc.GetDeletionTimestamp() != nil {
			return nil, biz.ErrCleanupBlocked
		}
		phase, _, err := unstructured.NestedString(pvc.Object, "status", "phase")
		if err != nil || phase != "Bound" {
			return nil, biz.ErrCleanupBlocked
		}
	}
	retained := append([]biz.RuntimeResource(nil), record.Runtime.CloseEvidence.Resources...)
	if record.Runtime.Observation != nil {
		trainingPods := 0
		for _, resource := range record.Runtime.Observation.Resources {
			if resource.Kind == "Pod" {
				if !retainedTrainingChain(resource, absent, record.Runtime.TrainingHandle) {
					return nil, biz.ErrCleanupBlocked
				}
				retained = append(retained, resource)
				trainingPods++
			}
		}
		if record.Runtime.Training != nil && trainingPods == 0 {
			return nil, biz.ErrCleanupBlocked
		}
	}
	for _, expected := range retained {
		if recordedStepRemoval(record, expected) {
			pod, err := adapter.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(namespace).Get(ctx, expected.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil || pod == nil || string(pod.GetUID()) != expected.UID {
				return nil, biz.ErrCleanupConflict
			}
			return nil, biz.ErrCleanupBlocked
		}
		if expected.APIVersion != "v1" || expected.Kind != "Pod" || expected.Namespace != namespace || expected.Name == "" || expected.UID == "" || expected.OwnerUID == "" || !expected.APIObjectPresent || !expected.Terminal || expected.ExitCode == nil || expected.InitializationAbort != nil {
			return nil, biz.ErrCleanupBlocked
		}
		pod, err := adapter.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(namespace).Get(ctx, expected.Name, metav1.GetOptions{})
		if err != nil || pod == nil || pod.GetAPIVersion() != "v1" || pod.GetKind() != "Pod" || pod.GetNamespace() != namespace || pod.GetName() != expected.Name || string(pod.GetUID()) != expected.UID || pod.GetDeletionTimestamp() != nil || !retainedPodTerminated(pod, *expected.ExitCode) {
			return nil, biz.ErrCleanupBlocked
		}
		if exactObject(pod, expected) {
			continue
		}
		// Orphan deletion removes the deleted Job's controller reference from
		// its surviving Pod. Only that complete persisted training chain may
		// lose an owner; the retained Workflow's Pods cannot use this exception.
		if !orphanedTrainingPod(pod, expected, absent, record.Runtime.TrainingHandle) {
			return nil, biz.ErrCleanupConflict
		}
	}
	return append([]biz.RuntimeResource{}, plan.Targets...), nil
}

func orphanedTrainingPod(pod *unstructured.Unstructured, expected biz.RuntimeResource, absent map[string]biz.RuntimeResource, handle *biz.TrainingHandle) bool {
	for _, owner := range pod.GetOwnerReferences() {
		if owner.Controller != nil && *owner.Controller {
			return false
		}
	}
	return retainedTrainingChain(expected, absent, handle)
}

func retainedTrainingChain(expected biz.RuntimeResource, absent map[string]biz.RuntimeResource, handle *biz.TrainingHandle) bool {
	job, jobAbsent := absent[expected.OwnerUID]
	set, setAbsent := absent[job.OwnerUID]
	train, trainAbsent := absent[set.OwnerUID]
	return handle != nil && jobAbsent && setAbsent && trainAbsent && job.Kind == "Job" && set.Kind == "JobSet" && train.Kind == "TrainJob" && train.UID == handle.TrainJobUID && train.OwnerUID == ""
}

func retainedPodTerminated(pod *unstructured.Unstructured, expectedExit int32) bool {
	phase, _, err := unstructured.NestedString(pod.Object, "status", "phase")
	if err != nil || (phase != "Succeeded" && phase != "Failed") {
		return false
	}
	main := false
	now := time.Now().UTC()
	for _, kind := range []struct{ spec, status string }{{"containers", "containerStatuses"}, {"initContainers", "initContainerStatuses"}, {"ephemeralContainers", "ephemeralContainerStatuses"}} {
		containers, _, err := unstructured.NestedSlice(pod.Object, "spec", kind.spec)
		if err != nil || (kind.spec == "containers" && len(containers) == 0) {
			return false
		}
		statuses, _, err := unstructured.NestedSlice(pod.Object, "status", kind.status)
		if err != nil || len(statuses) != len(containers) {
			return false
		}
		declared := make(map[string]bool, len(containers))
		for _, raw := range containers {
			container, ok := raw.(map[string]any)
			name, valid := container["name"].(string)
			if !ok || !valid || name == "" || declared[name] {
				return false
			}
			declared[name] = true
		}
		for _, raw := range statuses {
			status, ok := raw.(map[string]any)
			name, valid := status["name"].(string)
			if !ok || !valid || !declared[name] {
				return false
			}
			delete(declared, name)
			state, found, err := unstructured.NestedMap(status, "state")
			if err != nil || !found || len(state) != 1 {
				return false
			}
			code, found, err := unstructured.NestedInt64(state, "terminated", "exitCode")
			finished, _, timeErr := unstructured.NestedString(state, "terminated", "finishedAt")
			at, parseErr := time.Parse(time.RFC3339Nano, finished)
			if err != nil || !found || code < -2147483648 || code > 2147483647 || timeErr != nil || parseErr != nil || at.IsZero() || at.After(now) {
				return false
			}
			if kind.spec == "containers" && (name == "main" || name == "node") {
				if int32(code) != expectedExit || main {
					return false
				}
				main = true
			}
		}
	}
	return main
}

func (adapter *Adapter) VerifyCleanupWritersAbsent(ctx context.Context, record biz.OperationRecord) error {
	if ctx == nil || adapter == nil || adapter.kube == nil {
		return biz.ErrRuntimeNotReady
	}
	plan, err := biz.CleanupPlanFor(record)
	if err != nil {
		return err
	}
	namespace := record.Execution.Snapshot.Environment.NamespaceName
	ns, err := adapter.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, namespace, metav1.GetOptions{})
	if err != nil || ns.GetAPIVersion() != "v1" || ns.GetKind() != "Namespace" || ns.GetName() != namespace || string(ns.GetUID()) != plan.NamespaceUID || ns.GetDeletionTimestamp() != nil {
		return biz.ErrCleanupBlocked
	}
	if record.Runtime.CloseEvidence.NoDispatch {
		if record.Dispatch != nil || record.Runtime.Training != nil || record.Runtime.Workspace != nil || len(plan.Targets) != 0 {
			return biz.ErrCleanupBlocked
		}
		return nil
	}
	if record.Authority == nil || adapter.writers == nil {
		return biz.ErrCleanupBlocked
	}
	if record.Runtime.Training != nil {
		trains := 0
		for _, target := range plan.Targets {
			if target.Kind != "TrainJob" {
				continue
			}
			resource, valid := targetResource(target)
			if !valid {
				return biz.ErrCleanupConflict
			}
			train, err := adapter.kube.Resource(resource).Namespace(namespace).Get(ctx, target.Name, metav1.GetOptions{})
			if err != nil || train == nil {
				return biz.ErrCleanupUncertain
			}
			if !exactObject(train, target) || train.GetDeletionTimestamp() != nil || !creationStopped(train) {
				return biz.ErrCleanupConflict
			}
			if err := adapter.verifyOriginalParentsAbsent(ctx, record, plan, target); err != nil {
				return err
			}
			if err := adapter.verifyOriginalUnsuspendedSets(ctx, plan, target, train); err != nil {
				return err
			}
			trains++
		}
		if trains != 1 {
			return biz.ErrCleanupConflict
		}
	}
	if _, err := adapter.ownerWriterProof(ctx, record); err != nil {
		return err
	}
	if record.Runtime.Training == nil {
		return nil
	}
	observation, err := trainer.New(adapter.kube).ObserveTraining(ctx, *record.Runtime.Training, *record.Runtime.TrainingHandle, record.Runtime.Observation.Resources)
	if err != nil {
		return err
	}
	if !observation.WritersAbsent {
		return biz.ErrCleanupBlocked
	}
	fresh := record
	fresh.Runtime.Observation = &observation
	actual, err := biz.CleanupPlanFor(fresh)
	if err != nil || len(actual.Targets) != len(plan.Targets) {
		return biz.ErrCleanupConflict
	}
	for i, resource := range actual.Targets {
		expected := plan.Targets[i]
		if resource.APIVersion != expected.APIVersion || resource.Kind != expected.Kind || resource.Namespace != expected.Namespace || resource.Name != expected.Name || resource.UID != expected.UID || resource.OwnerUID != expected.OwnerUID {
			return biz.ErrCleanupConflict
		}
	}
	return nil
}

// A committed full exit receipt replaces only the requirement for the original
// step Pod to remain present. Current unrelated PVC writers are still read and
// checked; training Pod/log retention and exact controller cleanup stay intact.
func (adapter *Adapter) ownerWriterProof(ctx context.Context, record biz.OperationRecord) (biz.ManagedCloseEvidence, error) {
	if receipt := record.Runtime.StepExitCleanup; receipt != nil {
		verifier, ok := adapter.writers.(biz.RecordedOwnerWriterVerifier)
		if !ok || receipt.CloseGeneration != record.Runtime.CloseGeneration || receipt.Evidence.OwnerTermination == nil {
			return biz.ManagedCloseEvidence{}, biz.ErrCleanupBlocked
		}
		if err := verifier.VerifyRecordedOwnerWritersAbsent(ctx, record.Execution, *record.Authority, record.Runtime.Workspace, receipt.Evidence); err != nil {
			return biz.ManagedCloseEvidence{}, err
		}
		return receipt.Evidence, nil
	}
	return adapter.writers.VerifyOwnerWritersAbsent(ctx, record.Execution, *record.Authority, record.Runtime.Workspace)
}

func recordedStepRemoval(record biz.OperationRecord, expected biz.RuntimeResource) bool {
	receipt := record.Runtime.StepExitCleanup
	if receipt == nil || receipt.CompletedAt == nil || expected.OwnerUID != receipt.Evidence.WorkflowUID {
		return false
	}
	for _, removed := range receipt.RemovedResources {
		removed.APIObjectPresent = true
		if sameRetainedStepExit(expected, removed) {
			return true
		}
	}
	return false
}

func sameRetainedStepExit(left, right biz.RuntimeResource) bool {
	if left.InitializationAbort != nil && right.InitializationAbort != nil {
		l, r := *left.InitializationAbort, *right.InitializationAbort
		l.PodResourceVersion, r.PodResourceVersion = "", ""
		left.InitializationAbort, right.InitializationAbort = &l, &r
	}
	return reflect.DeepEqual(left, right)
}

func (adapter *Adapter) DeleteExecutionResource(ctx context.Context, record biz.OperationRecord, target biz.RuntimeResource) error {
	plan, err := biz.CleanupPlanFor(record)
	if err != nil || adapter == nil || adapter.kube == nil || ctx == nil {
		return biz.ErrCleanupBlocked
	}
	known := false
	for _, expected := range plan.Targets {
		known = known || reflect.DeepEqual(expected, target)
	}
	resource, valid := targetResource(target)
	if !known || !valid {
		return biz.ErrCleanupConflict
	}
	client := adapter.kube.Resource(resource).Namespace(target.Namespace)
	object, err := client.Get(ctx, target.Name, metav1.GetOptions{})
	// A missing object before our first exact delete is unresolved rather than
	// an inferred success. STARTED audit prevents retrying an unknown result.
	if err != nil || object == nil || object.GetDeletionTimestamp() != nil || object.GetResourceVersion() == "" || !creationStopped(object) {
		return biz.ErrCleanupConflict
	}
	if !originalOrOrphanObject(object, target) {
		return biz.ErrCleanupConflict
	}
	// A terminal parent can recreate a missing child. Prove the complete
	// original parent chain absent before deleting any child, including one
	// whose owner reference has already been removed by Orphan propagation.
	if err := adapter.verifyOriginalParentsAbsent(ctx, record, plan, target); err != nil {
		return err
	}
	if target.Kind == "TrainJob" {
		if err := adapter.verifyOriginalUnsuspendedSets(ctx, plan, target, object); err != nil {
			return err
		}
	}
	uid, version := types.UID(target.UID), object.GetResourceVersion()
	orphan := metav1.DeletePropagationOrphan
	if err := client.Delete(ctx, target.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}, PropagationPolicy: &orphan}); err != nil {
		return biz.ErrCleanupUncertain
	}
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		current, err := client.Get(wait, target.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		// A UID replacement or unreadable read cannot confirm removal of the
		// original target. DeletionTimestamp still needs a confirmed absence.
		if err != nil || !originalOrOrphanObject(current, target) {
			return biz.ErrCleanupUncertain
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-wait.Done():
			timer.Stop()
			return biz.ErrCleanupUncertain
		case <-timer.C:
		}
	}
}

func originalOrOrphanObject(object *unstructured.Unstructured, target biz.RuntimeResource) bool {
	if exactObject(object, target) {
		return true
	}
	if object == nil || target.OwnerUID == "" || len(object.GetOwnerReferences()) != 0 {
		return false
	}
	orphaned := target
	orphaned.OwnerUID = ""
	return exactObject(object, orphaned)
}

// Kubeflow Trainer v2.1.0 skips JobSet SSA only while both existing parents
// remain unsuspended. A previously modified spec may have left a cached SSA
// which reattaches an owner during Orphan GC; that case requires maintenance.
func (adapter *Adapter) verifyOriginalUnsuspendedSets(ctx context.Context, plan biz.CleanupPlan, target biz.RuntimeResource, train *unstructured.Unstructured) error {
	if !originalUnsuspendedGeneration(train) {
		return biz.ErrCleanupBlocked
	}
	sets := 0
	for _, expected := range plan.Targets {
		if expected.Kind != "JobSet" {
			continue
		}
		resource, valid := targetResource(expected)
		if !valid || expected.OwnerUID != target.UID || expected.Namespace != target.Namespace {
			return biz.ErrCleanupConflict
		}
		set, err := adapter.kube.Resource(resource).Namespace(expected.Namespace).Get(ctx, expected.Name, metav1.GetOptions{})
		if err != nil || set == nil {
			return biz.ErrCleanupUncertain
		}
		if !exactObject(set, expected) || set.GetDeletionTimestamp() != nil || !creationStopped(set) {
			return biz.ErrCleanupConflict
		}
		if !originalUnsuspendedGeneration(set) {
			return biz.ErrCleanupBlocked
		}
		sets++
	}
	if sets == 0 {
		return biz.ErrCleanupBlocked
	}
	return nil
}

func originalUnsuspendedGeneration(object *unstructured.Unstructured) bool {
	suspend, found, err := unstructured.NestedBool(object.Object, "spec", "suspend")
	return err == nil && found && !suspend && object.GetGeneration() == 1
}

func (adapter *Adapter) verifyOriginalParentsAbsent(ctx context.Context, record biz.OperationRecord, plan biz.CleanupPlan, target biz.RuntimeResource) error {
	parents := []biz.RuntimeResource{}
	current := target
	for current.Kind != "TrainJob" {
		kind := "TrainJob"
		if current.Kind == "Job" {
			kind = "JobSet"
		} else if current.Kind != "JobSet" {
			return biz.ErrCleanupConflict
		}
		matches := 0
		var parent biz.RuntimeResource
		for _, candidate := range plan.Targets {
			if candidate.UID == current.OwnerUID && candidate.Kind == kind && candidate.Namespace == target.Namespace {
				parent = candidate
				matches++
			}
		}
		if matches != 1 {
			return biz.ErrCleanupConflict
		}
		current = parent
		parents = append(parents, current)
	}
	if record.Runtime.TrainingHandle == nil || current.OwnerUID != "" || current.UID != record.Runtime.TrainingHandle.TrainJobUID || plan.NamespaceUID != record.Runtime.TrainingHandle.NamespaceUID {
		return biz.ErrCleanupConflict
	}
	for _, parent := range parents {
		resource, valid := targetResource(parent)
		if !valid {
			return biz.ErrCleanupConflict
		}
		object, err := adapter.kube.Resource(resource).Namespace(parent.Namespace).Get(ctx, parent.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil || object == nil {
			return biz.ErrCleanupUncertain
		}
		return biz.ErrCleanupConflict
	}
	return nil
}

func targetResource(target biz.RuntimeResource) (schema.GroupVersionResource, bool) {
	switch {
	case target.APIVersion == "trainer.kubeflow.org/v1alpha1" && target.Kind == "TrainJob":
		return schema.GroupVersionResource{Group: "trainer.kubeflow.org", Version: "v1alpha1", Resource: "trainjobs"}, true
	case target.APIVersion == "jobset.x-k8s.io/v1alpha2" && target.Kind == "JobSet":
		return schema.GroupVersionResource{Group: "jobset.x-k8s.io", Version: "v1alpha2", Resource: "jobsets"}, true
	case target.APIVersion == "batch/v1" && target.Kind == "Job":
		return schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, true
	default:
		return schema.GroupVersionResource{}, false
	}
}

func exactObject(object *unstructured.Unstructured, target biz.RuntimeResource) bool {
	if object == nil || object.GetAPIVersion() != target.APIVersion || object.GetKind() != target.Kind || object.GetNamespace() != target.Namespace || object.GetName() != target.Name || string(object.GetUID()) != target.UID {
		return false
	}
	owner := ""
	for _, reference := range object.GetOwnerReferences() {
		if reference.Controller != nil && *reference.Controller {
			if owner != "" {
				return false
			}
			owner = string(reference.UID)
		}
	}
	return owner == target.OwnerUID
}

func creationStopped(object *unstructured.Unstructured) bool {
	suspend, _, err := unstructured.NestedBool(object.Object, "spec", "suspend")
	if err != nil {
		return false
	}
	if suspend {
		return true
	}
	conditions, _, err := unstructured.NestedSlice(object.Object, "status", "conditions")
	if err != nil {
		return false
	}
	complete := "Complete"
	if object.GetKind() == "JobSet" {
		complete = "Completed"
	}
	seen, completeTrue, failedTrue := map[string]bool{}, false, false
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		kind, ok := condition["type"].(string)
		if !ok {
			return false
		}
		if kind != complete && kind != "Failed" {
			continue
		}
		if seen[kind] {
			return false
		}
		seen[kind] = true
		if kind == complete {
			completeTrue = condition["status"] == "True"
		} else {
			failedTrue = condition["status"] == "True"
		}
	}
	return completeTrue != failedTrue
}

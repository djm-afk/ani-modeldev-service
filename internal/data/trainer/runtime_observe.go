package trainer

import (
	"context"
	"sort"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (a *Adapter) ObserveTraining(ctx context.Context, plan biz.TrainingPlan, handle biz.TrainingHandle, history []biz.RuntimeResource) (biz.TrainingRuntimeObservation, error) {
	observation := biz.TrainingRuntimeObservation{Handle: handle, Outcome: "UNKNOWN", ObservedAt: time.Now().UTC()}
	train, current, err := a.readBoundTrainingForClose(ctx, plan, handle)
	if err != nil {
		return observation, err
	}
	if current != handle {
		return observation, biz.ErrRuntimeConflict
	}
	namespace := plan.Workspace.NamespaceName
	resources := make(map[string]biz.RuntimeResource)
	objects := map[string]*unstructured.Unstructured{string(train.GetUID()): train}
	trainTerminal, trainSuccess, trainFailure := terminalConditions(train, "Complete")
	resources[string(train.GetUID())] = resourceFact(train, "", trainTerminal, nil)
	jobset, err := a.client.Resource(schema.GroupVersionResource{Group: "jobset.x-k8s.io", Version: "v1alpha2", Resource: "jobsets"}).Namespace(namespace).Get(ctx, plan.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// A disappeared controller cannot erase old workers. Revisit their exact
		// names/UIDs and keep this observation unresolved regardless of Pod phase.
		for _, prior := range history {
			if prior.UID == "" || prior.Namespace != namespace {
				return observation, biz.ErrRuntimeConflict
			}
			if current, ok := resources[prior.UID]; ok {
				if current.APIVersion != prior.APIVersion || current.Kind != prior.Kind || current.Name != prior.Name || current.OwnerUID != prior.OwnerUID {
					return observation, biz.ErrRuntimeConflict
				}
				continue
			}
			resource, ok := historyResource(prior)
			if !ok {
				return observation, biz.ErrRuntimeConflict
			}
			actual, getErr := a.client.Resource(resource).Namespace(namespace).Get(ctx, prior.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(getErr) {
				prior.APIObjectPresent, prior.CreationDisabled = false, false
				if prior.Kind != "Pod" || !prior.Terminal || prior.ExitCode == nil {
					prior.Terminal, prior.ExitCode = false, nil
				}
				resources[prior.UID] = prior
				continue
			}
			if getErr != nil {
				return observation, biz.ErrTrainingUnavailable
			}
			if actual.GetAPIVersion() != prior.APIVersion || actual.GetKind() != prior.Kind || actual.GetNamespace() != namespace || actual.GetName() != prior.Name || string(actual.GetUID()) != prior.UID || controllerUID(actual) != prior.OwnerUID {
				return observation, biz.ErrRuntimeConflict
			}
			terminal, code := false, (*int32)(nil)
			if prior.Kind == "Pod" {
				terminal, code = podTermination(actual)
			}
			resources[prior.UID] = resourceFact(actual, prior.OwnerUID, terminal, code)
		}
		observation.Resources = orderedResources(resources)
		return observation, nil
	}
	if err != nil {
		return observation, biz.ErrTrainingUnavailable
	}
	if jobset.GetAPIVersion() != "jobset.x-k8s.io/v1alpha2" || jobset.GetKind() != "JobSet" || jobset.GetNamespace() != namespace || jobset.GetName() != plan.Name || jobset.GetUID() == "" || !controllerIs(jobset, "trainer.kubeflow.org/v1alpha1", "TrainJob", plan.Name, handle.TrainJobUID) {
		return observation, biz.ErrRuntimeConflict
	}
	setTerminal, setSuccess, setFailure := terminalConditions(jobset, "Completed")
	resources[string(jobset.GetUID())] = resourceFact(jobset, handle.TrainJobUID, setTerminal, nil)
	objects[string(jobset.GetUID())] = jobset
	// Whether a PodGroup exists is decided by the Runtime plan this execution
	// froze. A plan compiled with podGroupPolicy.volcano must have the exact
	// original group's complete vector; an older deployment plan without that
	// policy never creates one, so the group is neither queried nor treated as
	// a missing/specification mismatch.
	groupScheduled, err := biz.RuntimePlanSchedulesPodGroup(plan.Snapshot.RuntimePlan)
	if err != nil {
		return observation, biz.ErrRuntimeConflict
	}
	groupMatches := true
	if groupScheduled {
		group, groupErr := a.client.Resource(schema.GroupVersionResource{Group: "scheduling.volcano.sh", Version: "v1beta1", Resource: "podgroups"}).Namespace(namespace).Get(ctx, plan.Name, metav1.GetOptions{})
		if groupErr != nil && !apierrors.IsNotFound(groupErr) {
			return observation, biz.ErrTrainingUnavailable
		}
		// A missing group while a controller is starting is unresolved. Once a
		// worker exists, it must have the exact original group's complete vector.
		groupMatches = groupErr == nil && validatePodGroup(plan, handle, group) == nil
		if groupErr == nil {
			if group == nil || group.GetUID() == "" || !controllerIs(group, "trainer.kubeflow.org/v1alpha1", "TrainJob", plan.Name, handle.TrainJobUID) {
				return observation, biz.ErrRuntimeConflict
			}
			if !groupMatches {
				observation.SpecificationMismatch = true
			}
			resources[string(group.GetUID())] = resourceFact(group, handle.TrainJobUID, true, nil)
		}
	}
	jobs, err := a.client.Resource(schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil || jobs.GetContinue() != "" {
		return observation, biz.ErrTrainingUnavailable
	}
	jobUIDs := make(map[string]string)
	jobsStopped := true
	for i := range jobs.Items {
		job := &jobs.Items[i]
		if !controllerIs(job, "jobset.x-k8s.io/v1alpha2", "JobSet", plan.Name, string(jobset.GetUID())) {
			continue
		}
		if job.GetAPIVersion() != "batch/v1" || job.GetKind() != "Job" || job.GetNamespace() != namespace || job.GetUID() == "" {
			return observation, biz.ErrRuntimeConflict
		}
		terminal, _, _ := terminalConditions(job, "Complete")
		suspended, _, _ := unstructured.NestedBool(job.Object, "spec", "suspend")
		if !terminal && !suspended {
			jobsStopped = false
		}
		jobUIDs[string(job.GetUID())] = job.GetName()
		resources[string(job.GetUID())] = resourceFact(job, string(jobset.GetUID()), terminal, nil)
		objects[string(job.GetUID())] = job
	}
	pods, err := a.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil || pods.GetContinue() != "" {
		return observation, biz.ErrTrainingUnavailable
	}
	allPodsTerminal, allZero, anyNonzero, podCount := true, true, false, 0
	for i := range pods.Items {
		pod := &pods.Items[i]
		ownerUID := ""
		for uid, name := range jobUIDs {
			if controllerIs(pod, "batch/v1", "Job", name, uid) {
				ownerUID = uid
				break
			}
		}
		if ownerUID == "" {
			continue
		}
		if pod.GetAPIVersion() != "v1" || pod.GetKind() != "Pod" || pod.GetNamespace() != namespace || pod.GetUID() == "" {
			return observation, biz.ErrRuntimeConflict
		}
		vector, vectorErr := biz.TrainingResourceVector(plan.Snapshot)
		if vectorErr != nil || ValidatePodResources(vector, pod) != nil || validateEffectiveTrainingPod(plan, pod) != nil || !groupMatches {
			observation.SpecificationMismatch = true
		}
		terminal, code := podTermination(pod)
		// Owner assertions identify live GPU usage. They must not gate observing
		// an original stopped/deleting worker or retaining its actual exit/logs.
		if !observation.SpecificationMismatch && !terminal && pod.GetDeletionTimestamp() == nil && plan.Snapshot.ComputeOrigin.DeviceMode == "gpu" {
			if a.ownerSigner == nil {
				return observation, biz.ErrRuntimeNotReady
			}
			if err := a.ownerSigner.SignPod(ctx, plan, handle, pod); err != nil {
				return observation, err
			}
		}
		resources[string(pod.GetUID())] = resourceFact(pod, ownerUID, terminal, code)
		objects[string(pod.GetUID())] = pod
		podCount++
		allPodsTerminal = allPodsTerminal && terminal
		allZero = allZero && terminal && code != nil && *code == 0
		anyNonzero = anyNonzero || (terminal && code != nil && *code != 0)
	}

	// Lists cannot erase previously observed resources. Explicitly revisit every
	// historical identity, including Pods from old Jobs/JobSets no longer listed.
	for _, prior := range history {
		if prior.UID == "" || prior.Namespace != namespace {
			return observation, biz.ErrRuntimeConflict
		}
		if fact, ok := resources[prior.UID]; ok {
			if fact.Name != prior.Name || fact.Kind != prior.Kind || fact.APIVersion != prior.APIVersion || fact.OwnerUID != prior.OwnerUID {
				return observation, biz.ErrRuntimeConflict
			}
			continue
		}
		resource, ok := historyResource(prior)
		if !ok {
			return observation, biz.ErrRuntimeConflict
		}
		actual, getErr := a.client.Resource(resource).Namespace(namespace).Get(ctx, prior.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(getErr) {
			prior.APIObjectPresent, prior.CreationDisabled = false, false
			if prior.Kind == "Pod" && prior.Terminal && prior.ExitCode != nil {
				// A persisted exit belongs to this exact Pod UID. Garbage
				// collection changes API presence, not that completed process.
				podCount++
				allZero = allZero && *prior.ExitCode == 0
				anyNonzero = anyNonzero || *prior.ExitCode != 0
			} else {
				prior.Terminal, prior.ExitCode = false, nil
				allPodsTerminal, jobsStopped = false, false
			}
			resources[prior.UID] = prior
			continue
		}
		if getErr != nil {
			return observation, biz.ErrTrainingUnavailable
		}
		if actual.GetAPIVersion() != prior.APIVersion || actual.GetKind() != prior.Kind || actual.GetNamespace() != namespace || actual.GetName() != prior.Name || string(actual.GetUID()) != prior.UID || controllerUID(actual) != prior.OwnerUID {
			return observation, biz.ErrRuntimeConflict
		}
		objects[prior.UID] = actual
		if prior.Kind == "Pod" {
			terminal, code := podTermination(actual)
			resources[prior.UID] = resourceFact(actual, prior.OwnerUID, terminal, code)
			podCount++
			allPodsTerminal = allPodsTerminal && terminal
			allZero = allZero && terminal && code != nil && *code == 0
			anyNonzero = anyNonzero || (terminal && code != nil && *code != 0)
		} else if prior.Kind == "PodGroup" {
			if !controllerIs(actual, "trainer.kubeflow.org/v1alpha1", "TrainJob", plan.Name, handle.TrainJobUID) {
				return observation, biz.ErrRuntimeConflict
			}
			resources[prior.UID] = resourceFact(actual, handle.TrainJobUID, true, nil)
		} else {
			successType := "Complete"
			if prior.Kind == "JobSet" {
				successType = "Completed"
			}
			terminal, _, _ := terminalConditions(actual, successType)
			suspended, _, _ := unstructured.NestedBool(actual.Object, "spec", "suspend")
			resources[prior.UID] = resourceFact(actual, prior.OwnerUID, terminal, nil)
			jobsStopped = jobsStopped && (terminal || suspended)
		}
	}
	trainSuspended, _, _ := unstructured.NestedBool(train.Object, "spec", "suspend")
	setSuspended, _, _ := unstructured.NestedBool(jobset.Object, "spec", "suspend")
	creationStopped := (trainTerminal && setTerminal) || (trainSuspended && setSuspended && jobsStopped)
	if err := a.retainPodLogs(ctx, plan, handle, resources, objects, history); err != nil {
		return observation, err
	}
	exitPending, err := a.releaseRecordedPodExits(ctx, plan, handle, resources, objects, history)
	if err != nil {
		return observation, err
	}
	// There is no exit code to invent when every original controller has
	// acknowledged stopping before a worker existed. The immutable Job's exit
	// protection retains any real Pod, including one whose creation raced Stop.
	noWorker := podCount == 0 && allPodsTerminal && noWorkerControllersStopped(resources, objects) && pods.GetResourceVersion() != ""
	if noWorker {
		observation.NoWorkerPodListResourceVersion = pods.GetResourceVersion()
	}
	observation.WritersAbsent = creationStopped && jobsStopped && (podCount > 0 || noWorker) && allPodsTerminal && !exitPending
	switch {
	case podCount > 0 && trainSuccess && setSuccess && allZero && observation.WritersAbsent:
		observation.Outcome = "SUCCEEDED"
	case trainFailure && setFailure && anyNonzero && observation.WritersAbsent:
		observation.Outcome = "FAILED"
	case !observation.WritersAbsent && (!allPodsTerminal || (!trainTerminal && !setTerminal)):
		observation.Outcome = "RUNNING"
	}
	observation.Resources = orderedResources(resources)
	return observation, nil
}

func resourceFact(object *unstructured.Unstructured, owner string, terminal bool, code *int32) biz.RuntimeResource {
	creationDisabled := false
	if object.GetKind() == "TrainJob" || object.GetKind() == "JobSet" || object.GetKind() == "Job" {
		creationDisabled, _, _ = unstructured.NestedBool(object.Object, "spec", "suspend")
	}
	return biz.RuntimeResource{APIVersion: object.GetAPIVersion(), Kind: object.GetKind(), Namespace: object.GetNamespace(), Name: object.GetName(), UID: string(object.GetUID()), OwnerUID: owner, Terminal: terminal, ExitCode: code, APIObjectPresent: true, CreationDisabled: creationDisabled, Suspension: trainingSuspension(object, creationDisabled)}
}

func orderedResources(resources map[string]biz.RuntimeResource) []biz.RuntimeResource {
	result := make([]biz.RuntimeResource, 0, len(resources))
	for _, resource := range resources {
		result = append(result, resource)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UID < result[j].UID })
	return result
}

func controllerUID(object *unstructured.Unstructured) string {
	uid := ""
	for _, owner := range object.GetOwnerReferences() {
		if owner.Controller != nil && *owner.Controller {
			if uid != "" {
				return ""
			}
			uid = string(owner.UID)
		}
	}
	return uid
}

func controllerIs(object *unstructured.Unstructured, apiVersion, kind, name, uid string) bool {
	if controllerUID(object) != uid || uid == "" {
		return false
	}
	for _, owner := range object.GetOwnerReferences() {
		if owner.Controller != nil && *owner.Controller {
			return owner.APIVersion == apiVersion && owner.Kind == kind && owner.Name == name && string(owner.UID) == uid
		}
	}
	return false
}

func terminalConditions(object *unstructured.Unstructured, completeType string) (bool, bool, bool) {
	conditions, _, err := unstructured.NestedSlice(object.Object, "status", "conditions")
	if err != nil {
		return false, false, false
	}
	seen := make(map[string]bool)
	complete, failed := false, false
	for _, value := range conditions {
		condition, ok := value.(map[string]any)
		if !ok {
			return false, false, false
		}
		kind, _ := condition["type"].(string)
		if kind != completeType && kind != "Failed" {
			continue
		}
		if seen[kind] {
			return false, false, false
		}
		seen[kind] = true
		if condition["status"] != "True" {
			continue
		}
		if kind == completeType {
			complete = true
		} else {
			failed = true
		}
	}
	if complete && failed {
		return false, false, false
	}
	return complete || failed, complete, failed
}

func podTermination(pod *unstructured.Unstructured) (bool, *int32) {
	phase, _, _ := unstructured.NestedString(pod.Object, "status", "phase")
	if phase != "Succeeded" && phase != "Failed" {
		return false, nil
	}
	var nodeCode *int32
	for _, kind := range []struct{ spec, status string }{{"containers", "containerStatuses"}, {"initContainers", "initContainerStatuses"}, {"ephemeralContainers", "ephemeralContainerStatuses"}} {
		containers, _, err := unstructured.NestedSlice(pod.Object, "spec", kind.spec)
		if err != nil {
			return false, nil
		}
		statuses, _, err := unstructured.NestedSlice(pod.Object, "status", kind.status)
		if err != nil || len(statuses) != len(containers) {
			return false, nil
		}
		seen := make(map[string]bool)
		for _, value := range statuses {
			status, ok := value.(map[string]any)
			if !ok {
				return false, nil
			}
			name, ok := status["name"].(string)
			if !ok || seen[name] {
				return false, nil
			}
			seen[name] = true
			known := false
			for _, value := range containers {
				container, ok := value.(map[string]any)
				if ok && container["name"] == name {
					known = true
				}
			}
			if !known {
				return false, nil
			}
			state, found, err := unstructured.NestedMap(status, "state")
			if err != nil || !found || len(state) != 1 {
				return false, nil
			}
			code, found, err := unstructured.NestedInt64(state, "terminated", "exitCode")
			if err != nil || !found || code < -2147483648 || code > 2147483647 {
				return false, nil
			}
			finished, _, _ := unstructured.NestedString(state, "terminated", "finishedAt")
			if when, err := time.Parse(time.RFC3339, finished); err != nil || when.IsZero() {
				return false, nil
			}
			if kind.spec == "containers" && name == "node" {
				result := int32(code)
				nodeCode = &result
			}
		}
	}
	return nodeCode != nil, nodeCode
}

func historyResource(resource biz.RuntimeResource) (schema.GroupVersionResource, bool) {
	switch {
	case resource.APIVersion == "v1" && resource.Kind == "Pod":
		return schema.GroupVersionResource{Version: "v1", Resource: "pods"}, true
	case resource.APIVersion == "batch/v1" && resource.Kind == "Job":
		return schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, true
	case resource.APIVersion == "jobset.x-k8s.io/v1alpha2" && resource.Kind == "JobSet":
		return schema.GroupVersionResource{Group: "jobset.x-k8s.io", Version: "v1alpha2", Resource: "jobsets"}, true
	case resource.APIVersion == "trainer.kubeflow.org/v1alpha1" && resource.Kind == "TrainJob":
		return trainJobs, true
	case resource.APIVersion == "scheduling.volcano.sh/v1beta1" && resource.Kind == "PodGroup":
		return schema.GroupVersionResource{Group: "scheduling.volcano.sh", Version: "v1beta1", Resource: "podgroups"}, true
	default:
		return schema.GroupVersionResource{}, false
	}
}

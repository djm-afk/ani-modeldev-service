package trainer

import (
	"slices"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func trainingSuspension(object *unstructured.Unstructured, suspended bool) *biz.TrainingControllerSuspension {
	reasons := map[string]string{"TrainJob": "Suspended", "JobSet": "SuspendedJobs", "Job": "JobSuspended"}
	reason, known := reasons[object.GetKind()]
	if !known || !suspended || object.GetGeneration() < 2 || object.GetResourceVersion() == "" || object.GetDeletionTimestamp() != nil {
		return nil
	}
	conditions, found, err := unstructured.NestedSlice(object.Object, "status", "conditions")
	if err != nil || !found {
		return nil
	}
	var result *biz.TrainingControllerSuspension
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			return nil
		}
		if condition["type"] != "Suspended" {
			continue
		}
		if result != nil || condition["status"] != "True" || condition["reason"] != reason {
			return nil
		}
		created := object.GetCreationTimestamp()
		stamp, ok := condition["lastTransitionTime"].(string)
		at, err := time.Parse(time.RFC3339Nano, stamp)
		if !ok || err != nil || at.IsZero() || created.IsZero() || at.Before(created.Time) || at.After(time.Now().Add(5*time.Second)) {
			return nil
		}
		observed, hasGeneration, err := unstructured.NestedInt64(condition, "observedGeneration")
		if err != nil || hasGeneration && observed != object.GetGeneration() || !hasGeneration && object.GetGeneration() != 2 {
			return nil
		}
		// These controllers do not all emit observedGeneration. Without it,
		// accept only the first transition from the compiled unsuspended gen1.
		result = &biz.TrainingControllerSuspension{Generation: object.GetGeneration(), ObservedGeneration: observed, AcknowledgedAt: at.UTC()}
	}
	if result != nil && object.GetKind() == "Job" {
		finalizers, _, err := unstructured.NestedStringSlice(object.Object, "spec", "template", "metadata", "finalizers")
		result.WorkerExitRetention = err == nil && slices.Contains(finalizers, "modeldev.ani.io/training-exit-evidence")
	}
	return result
}

func noWorkerControllersStopped(resources map[string]biz.RuntimeResource, objects map[string]*unstructured.Unstructured) bool {
	counts := map[string]int{}
	for uid, resource := range resources {
		if resource.Kind == "PodGroup" {
			continue
		}
		if resource.Kind != "TrainJob" && resource.Kind != "JobSet" && resource.Kind != "Job" || !resource.APIObjectPresent || !resource.CreationDisabled || resource.Suspension == nil {
			return false
		}
		counts[resource.Kind]++
		if resource.Kind != "Job" {
			continue
		}
		job := objects[uid]
		if job == nil || !resource.Suspension.WorkerExitRetention {
			return false
		}
		for _, key := range []string{"active", "ready", "succeeded", "failed", "terminating"} {
			value, _, err := unstructured.NestedInt64(job.Object, "status", key)
			if err != nil || value != 0 {
				return false
			}
		}
		for _, key := range []string{"completedIndexes", "failedIndexes"} {
			value, _, err := unstructured.NestedString(job.Object, "status", key)
			if err != nil || value != "" {
				return false
			}
		}
		uncounted, _, err := unstructured.NestedMap(job.Object, "status", "uncountedTerminatedPods")
		if err != nil {
			return false
		}
		for key := range uncounted {
			if key != "succeeded" && key != "failed" {
				return false
			}
			values, _, err := unstructured.NestedSlice(uncounted, key)
			if err != nil || len(values) != 0 {
				return false
			}
		}
	}
	return counts["TrainJob"] == 1 && counts["JobSet"] == 1 && counts["Job"] == 1
}

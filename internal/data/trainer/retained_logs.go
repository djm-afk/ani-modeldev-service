package trainer

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Save real bounded output while the original worker is still running: Pod
// finalizers retain API objects, not the kubelet's terminated container logs.
// A terminal observation freezes its final readable tail (or an explicitly
// truncated earlier tail) before a later observation may release protection.
func (a *Adapter) retainPodLogs(ctx context.Context, plan biz.TrainingPlan, handle biz.TrainingHandle, resources map[string]biz.RuntimeResource, objects map[string]*unstructured.Unstructured, history []biz.RuntimeResource) error {
	var execution biz.Execution
	execution.TenantID, execution.ExecutionID = plan.TenantID, plan.ExecutionID
	execution.Snapshot = plan.Snapshot
	record := biz.QueryRecord{Execution: execution, Runtime: biz.ExecutionRuntime{Training: &plan, TrainingHandle: &handle,
		Observation: &biz.TrainingRuntimeObservation{Handle: handle, ObservedAt: time.Now().UTC(), Resources: orderedResources(resources)}}}
	for uid, fact := range resources {
		if fact.Kind != "Pod" {
			continue
		}
		var retained *biz.RecordedTrainingLog
		frozen := false
		for _, prior := range history {
			if prior.UID == uid && prior.TrainingLog != nil {
				if retained != nil {
					return biz.ErrRuntimeConflict
				}
				retained = prior.TrainingLog
				frozen = prior.Terminal && prior.ExitCode != nil
			}
		}
		pod := objects[uid]
		protected := pod != nil && slices.Contains(pod.GetFinalizers(), trainingExitEvidenceFinalizer)
		if retained == nil && !protected {
			return biz.ErrTrainingLogsUnavailable
		}
		logID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(plan.ExecutionID+":"+uid+":node")).String()
		source, err := biz.TrainingLogSourceFor(record, logID)
		if err != nil {
			return biz.ErrRuntimeConflict
		}
		if retained != nil && (retained.Source != source || biz.ValidateRecordedTrainingLogs(record, []biz.RecordedTrainingLog{*retained}) != nil) {
			return biz.ErrTrainingLogsUnavailable
		}
		started, valid := originalWorkerStarted(pod)
		if pod != nil && !valid {
			return biz.ErrTrainingLogsUnavailable
		}
		if !frozen && (started || fact.Terminal && fact.ExitCode != nil) {
			var result biz.TrainingLogResult
			readErr := biz.ErrTrainingLogsUnavailable
			if a.logs != nil {
				result, readErr = a.logs.ReadTrainingLogs(ctx, source, biz.TrainingLogOptions{TailLines: 1000, MaxBytes: 65536})
			}
			if readErr == nil {
				retained = &biz.RecordedTrainingLog{Source: source, Result: result}
			} else if retained != nil {
				copy := *retained
				copy.Result.Truncated = true
				retained = &copy // Preserve the actual earlier capture timestamp.
			} else if fact.Terminal {
				return readErr
			}
		}
		if retained == nil {
			continue // Running workers may not have emitted their first line yet.
		}
		if biz.ValidateRecordedTrainingLogs(record, []biz.RecordedTrainingLog{*retained}) != nil {
			return biz.ErrTrainingLogsUnavailable
		}
		fact.TrainingLog = retained
		resources[uid] = fact
	}
	return nil
}

func originalWorkerStarted(pod *unstructured.Unstructured) (bool, bool) {
	if pod == nil {
		return false, true
	}
	statuses, _, err := unstructured.NestedSlice(pod.Object, "status", "containerStatuses")
	if err != nil {
		return false, false
	}
	matched, started := 0, false
	for _, value := range statuses {
		status, ok := value.(map[string]any)
		if !ok {
			return false, false
		}
		if status["name"] != "node" {
			continue
		}
		matched++
		restarts, _, err := unstructured.NestedInt64(status, "restartCount")
		if err != nil || restarts != 0 {
			return false, false
		}
		_, running, _ := unstructured.NestedMap(status, "state", "running")
		_, terminal, _ := unstructured.NestedMap(status, "state", "terminated")
		started = running || terminal
	}
	return started, matched <= 1
}

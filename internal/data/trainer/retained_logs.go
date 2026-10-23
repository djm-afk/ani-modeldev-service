package trainer

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Capture only after the original node container has exited. The next observer
// can release its retention finalizer only from the same durably saved tail.
func (a *Adapter) retainTerminalPodLogs(ctx context.Context, plan biz.TrainingPlan, handle biz.TrainingHandle, resources map[string]biz.RuntimeResource, objects map[string]*unstructured.Unstructured, history []biz.RuntimeResource) error {
	var execution biz.Execution
	execution.TenantID, execution.ExecutionID = plan.TenantID, plan.ExecutionID
	execution.Snapshot = plan.Snapshot
	record := biz.QueryRecord{Execution: execution, Runtime: biz.ExecutionRuntime{Training: &plan, TrainingHandle: &handle,
		Observation: &biz.TrainingRuntimeObservation{Handle: handle, ObservedAt: time.Now().UTC(), Resources: orderedResources(resources)}}}
	for uid, fact := range resources {
		if fact.Kind != "Pod" || !fact.Terminal || fact.ExitCode == nil {
			continue
		}
		var retained *biz.RecordedTrainingLog
		for _, prior := range history {
			if prior.UID == uid && prior.TrainingLog != nil {
				retained = prior.TrainingLog
			}
		}
		pod := objects[uid]
		protected := pod != nil && slices.Contains(pod.GetFinalizers(), trainingExitEvidenceFinalizer)
		if retained == nil && !protected {
			continue // Legacy objects still require capture before compute cleanup.
		}
		logID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(plan.ExecutionID+":"+uid+":node")).String()
		source, err := biz.TrainingLogSourceFor(record, logID)
		if err != nil {
			return biz.ErrRuntimeConflict
		}
		if retained == nil {
			if a.logs == nil {
				return biz.ErrTrainingLogsUnavailable
			}
			result, err := a.logs.ReadTrainingLogs(ctx, source, biz.TrainingLogOptions{TailLines: 1000, MaxBytes: 65536})
			if err != nil {
				return err
			}
			retained = &biz.RecordedTrainingLog{Source: source, Result: result}
		}
		if retained.Source != source || biz.ValidateRecordedTrainingLogs(record, []biz.RecordedTrainingLog{*retained}) != nil {
			return biz.ErrTrainingLogsUnavailable
		}
		fact.TrainingLog = retained
		resources[uid] = fact
	}
	return nil
}

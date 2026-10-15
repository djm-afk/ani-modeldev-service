package runtimeproof

import (
	"context"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var _ biz.PreparedWorkspaceVerifier = (*Verifier)(nil)

// VerifyPreparedCompletion gates the first training reservation on the original
// prepare's independent KFP/Pod completion, not its earlier Prepared callback.
func (verifier *Verifier) VerifyPreparedCompletion(ctx context.Context, execution biz.Execution, association biz.ManagedStepAssociation, workspace biz.WorkspaceBinding) error {
	if err := biz.ValidateWorkspaceBinding(execution, workspace); err != nil {
		return err
	}
	tasks, err := verifier.currentTasks(ctx, execution, association)
	if err != nil {
		return err
	}
	prepare, err := oneTask(tasks, "prepare")
	if err != nil || prepare.RunID != association.RunID || prepare.State != "SUCCEEDED" {
		return biz.ErrRuntimeNotReady
	}
	pod, err := verifier.currentTaskPod(ctx, execution, association, prepare, "")
	if err != nil {
		return err
	}
	terminal, zero, completed, code := terminatedPod(pod)
	phase, _ := textAt(pod, "status", "phase")
	now := time.Now().UTC()
	if !terminal || !zero || code == nil || *code != 0 || phase != "Succeeded" || pod.GetDeletionTimestamp() != nil || completed.IsZero() || completed.After(now) {
		return biz.ErrRuntimeNotReady
	}
	// terminatedPod checks every declared container's exit status; its returned
	// timestamp is main's, so also reject a future sidecar/init completion.
	for _, kind := range []string{"containerStatuses", "initContainerStatuses", "ephemeralContainerStatuses"} {
		statuses, _, err := unstructured.NestedSlice(pod.Object, "status", kind)
		if err != nil {
			return biz.ErrRuntimeNotReady
		}
		for _, raw := range statuses {
			status, ok := raw.(map[string]any)
			if !ok {
				return biz.ErrRuntimeNotReady
			}
			text, _, _ := unstructured.NestedString(status, "state", "terminated", "finishedAt")
			at, err := time.Parse(time.RFC3339Nano, text)
			if err != nil || at.IsZero() || at.After(now) {
				return biz.ErrRuntimeNotReady
			}
		}
	}
	// Reuse the exact manifest/PVC/mount checks with prepare's independently
	// observed identity. It is not the train-wait caller's Pod association.
	original := association
	original.PodName, original.PodUID = pod.GetName(), string(pod.GetUID())
	return verifier.VerifyPrepared(ctx, execution, original, workspace)
}

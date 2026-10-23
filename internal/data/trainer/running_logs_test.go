package trainer_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func originalWorkerLog(observation biz.TrainingRuntimeObservation) *biz.RecordedTrainingLog {
	for _, fact := range observation.Resources {
		if fact.UID == childPodUID {
			return fact.TrainingLog
		}
	}
	return nil
}

func TestRunningTailSurvivesStoppedContainerLogLoss(t *testing.T) {
	f := newTrainingFixture(t)
	f.pod["metadata"].(map[string]any)["resourceVersion"] = "1"
	f.pod["metadata"].(map[string]any)["finalizers"] = []any{exitEvidenceFinalizer}
	a, ctx := f.adapter(t), context.Background()
	handle, err := a.CreateTraining(ctx, f.plan)
	if err != nil {
		t.Fatal(err)
	}
	running, err := a.ObserveTraining(ctx, f.plan, handle, nil)
	prior := originalWorkerLog(running)
	if err != nil || prior == nil || len(prior.Result.Lines) == 0 || running.WritersAbsent {
		t.Fatalf("RUNNING_TAIL_NOT_OFFERED_FOR_PERSISTENCE: log=%+v err=%v", prior, err)
	}
	if err := a.StopTraining(ctx, f.plan, handle); err != nil {
		t.Fatal(err)
	}
	f.setTerminal(t, 143)
	// Real Stop03 boundary: HTTP200 is not a valid timestamped container tail.
	f.logReadBody = []byte("unable to retrieve container logs for containerd://original-container")
	terminal, err := a.ObserveTraining(ctx, f.plan, handle, running.Resources)
	retained := originalWorkerLog(terminal)
	if err != nil || retained == nil || !retained.Result.Truncated || retained.Source != prior.Source || !retained.Result.ObservedAt.Equal(prior.Result.ObservedAt) || !reflect.DeepEqual(retained.Result.Lines, prior.Result.Lines) || terminal.WritersAbsent || f.podPatches != 0 {
		t.Fatalf("lost terminal refresh erased real running tail or fabricated a new observation: %+v %v", retained, err)
	}
	reads := f.logReads
	confirmed, err := a.ObserveTraining(ctx, f.plan, handle, terminal.Resources)
	if err != nil || !confirmed.WritersAbsent || f.podPatches != 1 || f.logReads != reads || !reflect.DeepEqual(originalWorkerLog(confirmed), retained) {
		t.Fatalf("frozen durable terminal tail did not release original evidence: %+v reads=%d err=%v", confirmed, f.logReads, err)
	}
	f.pod = nil
	collected, err := a.ObserveTraining(ctx, f.plan, handle, confirmed.Resources)
	if err != nil || !collected.WritersAbsent || !reflect.DeepEqual(originalWorkerLog(collected), retained) {
		t.Fatalf("GC erased original bounded tail: %+v %v", collected, err)
	}
}

func TestMissingRunningLogsDoNotBlockStopOrAuthorizeEmptyExit(t *testing.T) {
	f := newTrainingFixture(t)
	f.pod["metadata"].(map[string]any)["resourceVersion"] = "1"
	f.pod["metadata"].(map[string]any)["finalizers"] = []any{exitEvidenceFinalizer}
	f.logReadBody = []byte("unable to retrieve container logs for containerd://original-container")
	a, ctx := f.adapter(t), context.Background()
	handle, err := a.CreateTraining(ctx, f.plan)
	if err != nil {
		t.Fatal(err)
	}
	running, err := a.ObserveTraining(ctx, f.plan, handle, nil)
	if err != nil || originalWorkerLog(running) != nil {
		t.Fatalf("transient running capture fabricated a log or prevented observing a live worker: %v", err)
	}
	if err := a.StopTraining(ctx, f.plan, handle); err != nil {
		t.Fatalf("missing stdout prevented legal Stop: %v", err)
	}
	f.setTerminal(t, 143)
	if _, err := a.ObserveTraining(ctx, f.plan, handle, running.Resources); !errors.Is(err, biz.ErrTrainingLogsUnavailable) {
		t.Fatalf("missing all original logs authorized terminal evidence: %v", err)
	}
	finalizers, _, _ := unstructured.NestedStringSlice(f.pod, "metadata", "finalizers")
	if !reflect.DeepEqual(finalizers, []string{exitEvidenceFinalizer}) || f.podPatches != 0 {
		t.Fatal("no-log exit released protection")
	}
}

func TestRunningTailFallbackRejectsForeignOrInvalidHistory(t *testing.T) {
	for _, attack := range []string{"foreign-source", "empty-tail", "restart"} {
		t.Run(attack, func(t *testing.T) {
			f := newTrainingFixture(t)
			f.pod["metadata"].(map[string]any)["resourceVersion"] = "1"
			f.pod["metadata"].(map[string]any)["finalizers"] = []any{exitEvidenceFinalizer}
			a, ctx := f.adapter(t), context.Background()
			handle, err := a.CreateTraining(ctx, f.plan)
			if err != nil {
				t.Fatal(err)
			}
			running, err := a.ObserveTraining(ctx, f.plan, handle, nil)
			log := originalWorkerLog(running)
			if err != nil || log == nil {
				t.Fatalf("real running tail unavailable: %v", err)
			}
			f.setTerminal(t, 143)
			f.logReadBody = []byte("unable to retrieve container logs")
			switch attack {
			case "foreign-source":
				log.Source.PodUID = "replacement"
			case "empty-tail":
				log.Result.Lines = nil
			}
			if attack == "restart" {
				statuses, _, _ := unstructured.NestedSlice(f.pod, "status", "containerStatuses")
				statuses[0].(map[string]any)["restartCount"] = int64(1)
				_ = unstructured.SetNestedSlice(f.pod, statuses, "status", "containerStatuses")
			}
			if _, err := a.ObserveTraining(ctx, f.plan, handle, running.Resources); err == nil || f.podPatches != 0 {
				t.Fatalf("invalid prior segment borrowed as original exit tail: %v", err)
			}
		})
	}
}

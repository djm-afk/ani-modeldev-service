package lifecycle_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
)

func TestRunningLogUpdatesKeepOriginalSourceTimeAndTerminalFreeze(t *testing.T) {
	open, admission, authority, workspace := runtimeFixture(t)
	pool := open()
	defer pool.Close()
	r, ctx := lifecycle.New(pool), context.Background()
	if _, _, err := r.RecordPrepared(ctx, authority, workspace); err != nil {
		t.Fatal(err)
	}
	reserved, err := reserveTrainingV21(t, r, ctx, authority)
	if err != nil {
		t.Fatal(err)
	}
	handle := biz.TrainingHandle{NamespaceUID: workspace.NamespaceUID, PVCUID: workspace.PVCUID, TrainJobUID: "running-log-training"}
	if _, err := r.RecordTrainingHandle(ctx, authority, handle); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	observation := biz.TrainingRuntimeObservation{Handle: handle, Outcome: "RUNNING", ObservedAt: at, Resources: []biz.RuntimeResource{
		{APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainJob", Namespace: workspace.NamespaceName, Name: reserved.State.Training.Name, UID: handle.TrainJobUID, APIObjectPresent: true},
		{APIVersion: "jobset.x-k8s.io/v1alpha2", Kind: "JobSet", Namespace: workspace.NamespaceName, Name: "running-log-set", UID: "running-log-set", OwnerUID: handle.TrainJobUID, APIObjectPresent: true},
		{APIVersion: "batch/v1", Kind: "Job", Namespace: workspace.NamespaceName, Name: "running-log-job", UID: "running-log-job", OwnerUID: "running-log-set", APIObjectPresent: true},
		{APIVersion: "v1", Kind: "Pod", Namespace: workspace.NamespaceName, Name: "running-log-pod", UID: "running-log-pod", OwnerUID: "running-log-job", APIObjectPresent: true},
	}}
	record := biz.QueryRecord{Execution: biz.Execution{Admission: admission}, Runtime: biz.ExecutionRuntime{Training: reserved.State.Training, TrainingHandle: &handle, Observation: &observation}}
	source, err := biz.TrainingLogSourceFor(record, "")
	if err != nil {
		t.Fatal(err)
	}
	observation.Resources[3].TrainingLog = &biz.RecordedTrainingLog{Source: source, Result: biz.TrainingLogResult{ObservedAt: at, Lines: []biz.TrainingLogLine{{Timestamp: at.Add(-time.Second), Text: "original running output"}}}}
	if _, err := r.RecordTrainingObservation(ctx, authority, observation); err != nil {
		t.Fatal("valid running log was not committed", err)
	}
	encoded, _ := json.Marshal(observation)
	for _, attack := range []string{"erase", "foreign-source", "time-backwards", "same-time-changed-bytes"} {
		t.Run(attack, func(t *testing.T) {
			var changed biz.TrainingRuntimeObservation
			if err := json.Unmarshal(encoded, &changed); err != nil {
				t.Fatal(err)
			}
			changed.ObservedAt = time.Now().UTC()
			log := changed.Resources[3].TrainingLog
			switch attack {
			case "erase":
				changed.Resources[3].TrainingLog = nil
			case "foreign-source":
				log.Source.PodUID = "foreign-pod"
			case "time-backwards":
				log.Result.ObservedAt = at.Add(-time.Nanosecond)
			case "same-time-changed-bytes":
				log.Result.Lines[0].Text = "changed original capture"
			}
			if _, err := r.RecordTrainingObservation(ctx, authority, changed); err == nil {
				t.Fatal("invalid running tail evolution accepted")
			}
		})
	}
	observation.ObservedAt = time.Now().UTC()
	observation.Resources[3].TrainingLog.Result.ObservedAt = observation.ObservedAt
	observation.Resources[3].TrainingLog.Result.Lines[0].Text = "later real owner capture"
	if _, err := r.RecordTrainingObservation(ctx, authority, observation); err != nil {
		t.Fatal("new same-source running capture rejected", err)
	}
	code := int32(143)
	observation.ObservedAt = time.Now().UTC()
	observation.Resources[3].Terminal = true
	observation.Resources[3].ExitCode = &code
	observation.Resources[3].TrainingLog.Result.Truncated = true
	if _, err := r.RecordTrainingObservation(ctx, authority, observation); err != nil {
		t.Fatal("same capture could not freeze with original terminal exit", err)
	}
	observation.ObservedAt = time.Now().UTC()
	observation.Resources[3].TrainingLog.Result.ObservedAt = observation.ObservedAt
	if _, err := r.RecordTrainingObservation(ctx, authority, observation); err == nil {
		t.Fatal("terminal durable tail was replaced")
	}
}

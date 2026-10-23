package biz_test

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func recordedLogFixture(t *testing.T) (biz.QueryRecord, biz.TrainingLogSource) {
	t.Helper()
	observed := time.Date(2026, 10, 14, 0, 0, 10, 0, time.UTC)
	handle := biz.TrainingHandle{NamespaceUID: "namespace-original", TrainJobUID: "train-original", PVCUID: "pvc-original"}
	record := biz.QueryRecord{
		Execution: biz.Execution{Admission: biz.Admission{ExecutionID: "608aa6bf-4dac-4989-bd22-9b0ef6f1ce68", Snapshot: cpup01.Snapshot{Environment: cpup01.EnvironmentBindingSnapshot{NamespaceName: "tenant-training", NamespaceUID: handle.NamespaceUID}}}},
		Runtime: biz.ExecutionRuntime{Training: &biz.TrainingPlan{Name: "trainer-original"}, TrainingHandle: &handle,
			Observation: &biz.TrainingRuntimeObservation{Handle: handle, ObservedAt: observed, Resources: []biz.RuntimeResource{
				{APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainJob", Namespace: "tenant-training", Name: "trainer-original", UID: handle.TrainJobUID, APIObjectPresent: true},
				{APIVersion: "jobset.x-k8s.io/v1alpha2", Kind: "JobSet", Namespace: "tenant-training", Name: "set-original", UID: "set-original", OwnerUID: handle.TrainJobUID, APIObjectPresent: true},
				{APIVersion: "batch/v1", Kind: "Job", Namespace: "tenant-training", Name: "job-original", UID: "job-original", OwnerUID: "set-original", APIObjectPresent: true},
				{APIVersion: "v1", Kind: "Pod", Namespace: "tenant-training", Name: "pod-original", UID: "pod-original", OwnerUID: "job-original", APIObjectPresent: true},
			}}},
	}
	source, err := biz.TrainingLogSourceFor(record, "")
	if err != nil {
		t.Fatal(err)
	}
	record.Runtime.ComputeCleanup = &biz.ComputeCleanupReceipt{LogsReady: true, Logs: []biz.RecordedTrainingLog{{Source: source, Result: biz.TrainingLogResult{
		ObservedAt: observed, Lines: []biz.TrainingLogLine{
			{Timestamp: observed.Add(-3 * time.Second), Text: "first"},
			{Timestamp: observed.Add(-2 * time.Second), Text: "second"},
			{Timestamp: observed.Add(-time.Second), Text: "last"},
		},
	}}}}
	return record, source
}

func TestRecordedTrainingLogsRetainOriginalTailWithoutLiveObjects(t *testing.T) {
	record, source := recordedLogFixture(t)
	result, found, err := biz.RecordedTrainingLogsFor(record, source, biz.TrainingLogOptions{TailLines: 2, MaxBytes: 65536})
	if err != nil || !found || len(result.Lines) != 2 || result.Lines[0].Text != "second" || result.Lines[1].Text != "last" || !result.Truncated || !result.ObservedAt.Equal(record.Runtime.ComputeCleanup.Logs[0].Result.ObservedAt) {
		t.Fatalf("original archived tail unavailable: found=%t result=%+v error=%v", found, result, err)
	}
	result.Lines[0].Text = "changed by caller"
	if record.Runtime.ComputeCleanup.Logs[0].Result.Lines[1].Text != "second" {
		t.Fatal("query modified the durable log tail")
	}
}

func TestRecordedTrainingLogsKeepLegacyTailAlongsideNewPodEvidence(t *testing.T) {
	record, source := recordedLogFixture(t)
	newPod := record.Runtime.Observation.Resources[3]
	newPod.Name, newPod.UID = "later-pod", "later-pod"
	record.Runtime.Observation.Resources = append(record.Runtime.Observation.Resources, newPod)
	newSource, err := biz.TrainingLogSourceFor(record, uuid.NewSHA1(uuid.NameSpaceOID, []byte(record.Execution.ExecutionID+":"+newPod.UID+":node")).String())
	if err != nil {
		t.Fatal(err)
	}
	newLog := record.Runtime.ComputeCleanup.Logs[0]
	newLog.Source = newSource
	record.Runtime.Observation.Resources[4].TrainingLog = &newLog
	result, found, err := biz.RecordedTrainingLogsFor(record, source, biz.TrainingLogOptions{TailLines: 1, MaxBytes: 65536})
	if err != nil || !found || len(result.Lines) != 1 || result.Lines[0].Text != "last" {
		t.Fatalf("a new Pod tail hid the original committed cleanup archive: found=%t result=%+v error=%v", found, result, err)
	}
}

func TestRecordedTrainingLogsRespectByteLimitWithoutBreakingUTF8(t *testing.T) {
	record, source := recordedLogFixture(t)
	log := &record.Runtime.ComputeCleanup.Logs[0].Result
	log.Lines = []biz.TrainingLogLine{{Timestamp: log.ObservedAt.Add(-time.Second), Text: "训练结束"}}
	result, found, err := biz.RecordedTrainingLogsFor(record, source, biz.TrainingLogOptions{TailLines: 1, MaxBytes: 25})
	if err != nil || !found || len(result.Lines) != 1 || result.Lines[0].Text != "束" || !utf8.ValidString(result.Lines[0].Text) || !result.Truncated {
		t.Fatalf("bounded UTF8 tail unavailable: found=%t result=%+v error=%v", found, result, err)
	}
	if log.Lines[0].Text != "训练结束" {
		t.Fatal("byte trimming changed the recorded log")
	}
}

func TestRecordedTrainingLogsAcceptOriginalWireBudgetWithoutFinalLF(t *testing.T) {
	record, _ := recordedLogFixture(t)
	log := &record.Runtime.ComputeCleanup.Logs[0].Result
	// A 20-byte RFC3339 timestamp, one separator and 65515 ASCII bytes are
	// exactly the original reader's 65536-byte cap without a final newline.
	log.Lines = []biz.TrainingLogLine{{Timestamp: log.ObservedAt.Add(-time.Second), Text: strings.Repeat("x", 65515)}}
	if err := biz.ValidateRecordedTrainingLogs(record, record.Runtime.ComputeCleanup.Logs); err != nil {
		t.Fatalf("valid original wire-limit tail was rejected: %v", err)
	}
}

func TestRecordedTrainingLogValidationRejectsForeignIdentityAndUnboundedPayload(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*biz.QueryRecord)
	}{
		{"same-name replacement Pod UID", func(r *biz.QueryRecord) { r.Runtime.ComputeCleanup.Logs[0].Source.PodUID = "replacement" }},
		{"foreign Job UID", func(r *biz.QueryRecord) { r.Runtime.ComputeCleanup.Logs[0].Source.OwnerUID = "foreign-job" }},
		{"foreign namespace UID", func(r *biz.QueryRecord) { r.Runtime.ComputeCleanup.Logs[0].Source.NamespaceUID = "foreign-namespace" }},
		{"foreign source log ID", func(r *biz.QueryRecord) {
			r.Runtime.ComputeCleanup.Logs[0].Source.LogID = "5f3f767d-590a-49bd-850d-5c3595781173"
		}},
		{"duplicate original segment", func(r *biz.QueryRecord) {
			r.Runtime.ComputeCleanup.Logs = append(r.Runtime.ComputeCleanup.Logs, r.Runtime.ComputeCleanup.Logs[0])
		}},
		{"unobserved log", func(r *biz.QueryRecord) { r.Runtime.ComputeCleanup.Logs[0].Result.ObservedAt = time.Time{} }},
		{"invalid line timestamp", func(r *biz.QueryRecord) { r.Runtime.ComputeCleanup.Logs[0].Result.Lines[0].Timestamp = time.Time{} }},
		{"invalid UTF8", func(r *biz.QueryRecord) { r.Runtime.ComputeCleanup.Logs[0].Result.Lines[0].Text = string([]byte{0xff}) }},
		{"embedded uncounted lines", func(r *biz.QueryRecord) {
			r.Runtime.ComputeCleanup.Logs[0].Result.Lines[0].Text = strings.Repeat("\n", 1001)
		}},
		{"over byte budget", func(r *biz.QueryRecord) {
			r.Runtime.ComputeCleanup.Logs[0].Result.Lines[0].Text = strings.Repeat("x", 65536)
		}},
		{"over line budget", func(r *biz.QueryRecord) {
			for len(r.Runtime.ComputeCleanup.Logs[0].Result.Lines) <= 1000 {
				r.Runtime.ComputeCleanup.Logs[0].Result.Lines = append(r.Runtime.ComputeCleanup.Logs[0].Result.Lines, r.Runtime.ComputeCleanup.Logs[0].Result.Lines[0])
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			record, source := recordedLogFixture(t)
			test.change(&record)
			if err := biz.ValidateRecordedTrainingLogs(record, record.Runtime.ComputeCleanup.Logs); err == nil {
				t.Fatal("invalid durable archive accepted")
			}
			if _, _, err := biz.RecordedTrainingLogsFor(record, source, biz.TrainingLogOptions{TailLines: 200, MaxBytes: 16384}); err == nil {
				t.Fatal("corrupt archive became a live log fallback")
			}
		})
	}
	if err := biz.ValidateRecordedTrainingLogs(biz.QueryRecord{}, nil); err != nil {
		t.Fatalf("an execution with no captured training logs was rejected: %v", err)
	}
}

func TestRecordedTrainingLogsWaitForCommittedCapture(t *testing.T) {
	record, source := recordedLogFixture(t)
	record.Runtime.ComputeCleanup.LogsReady = false
	record.Runtime.ComputeCleanup.Logs[0].Result.Lines = nil
	if _, found, err := biz.RecordedTrainingLogsFor(record, source, biz.TrainingLogOptions{TailLines: 200, MaxBytes: 16384}); err != nil || found {
		t.Fatalf("capture in progress blocked live fallback: found=%t error=%v", found, err)
	}
}

func TestCleanupCannotTreatGoneStartedPodAsNoWorker(t *testing.T) {
	record, _ := recordedLogFixture(t)
	code := int32(143)
	pod := &record.Runtime.Observation.Resources[3]
	pod.APIObjectPresent, pod.Terminal, pod.ExitCode = false, true, &code
	if biz.ComputeCleanupLogsComplete(record.Execution, record.Runtime, nil) {
		t.Fatal("a previously started worker disappeared; empty logs cannot become logs_ready")
	}
	if !biz.ComputeCleanupLogsComplete(record.Execution, record.Runtime, record.Runtime.ComputeCleanup.Logs) {
		t.Fatal("the original identity-bound log tail was lost when its Pod disappeared")
	}
}

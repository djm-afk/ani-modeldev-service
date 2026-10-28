package biz_test

import (
	"errors"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

const (
	workloadTenant    = "9086ce3b-bd19-47c3-8467-190607f633db"
	workloadExecution = "608aa6bf-4dac-4989-bd22-9b0ef6f1ce68"
)

// workloadPlan mirrors a frozen Runtime plan that asks Trainer for a PodGroup.
func workloadPlan() cpup01.RuntimePlan {
	return cpup01.RuntimePlan{ExpectedSpec: []byte(`{"podGroupPolicy":{"volcano":{}}}`)}
}

func workloadExecutionFixture(runtimePlan cpup01.RuntimePlan) biz.OperationRecord {
	handle := biz.TrainingHandle{NamespaceUID: "namespace-uid", TrainJobUID: "train-uid"}
	return biz.OperationRecord{
		QueryRecord: biz.QueryRecord{
			Execution: biz.Execution{Admission: biz.Admission{
				TenantID: workloadTenant, ExecutionID: workloadExecution,
				Snapshot: cpup01.Snapshot{Environment: cpup01.EnvironmentBindingSnapshot{NamespaceName: "tenant-training", NamespaceUID: "namespace-uid"}, RuntimePlan: runtimePlan},
			}},
			Runtime: biz.ExecutionRuntime{Training: &biz.TrainingPlan{Name: "md-" + workloadExecution}, TrainingHandle: &handle},
		},
	}
}

// availability is decided in one fixed order so a detail page never claims
// "never created" for an outcome that is merely unknown.
func TestExecutionWorkloadAvailabilityOrder(t *testing.T) {
	rejected := workloadExecutionFixture(workloadPlan())
	rejected.Runtime.TrainingRejection = &biz.TrainingCreationRejection{}

	neverCreated := workloadExecutionFixture(workloadPlan())
	neverCreated.Runtime.Training, neverCreated.Runtime.TrainingHandle = nil, nil

	pending := workloadExecutionFixture(workloadPlan())
	pending.Runtime.TrainingHandle = nil

	for _, test := range []struct {
		name  string
		scope biz.ExecutionWorkloadScope
		want  biz.PodAvailability
	}{
		{"rejected", mustScope(t, rejected), biz.PodAvailabilityRejected},
		{"never created", mustScope(t, neverCreated), biz.PodAvailabilityNeverCreated},
		{"creation pending", mustScope(t, pending), biz.PodAvailabilityCreationPending},
		{"live", mustScope(t, workloadExecutionFixture(workloadPlan())), biz.PodAvailabilityLive},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.scope.Availability != test.want {
				t.Fatalf("availability=%s want=%s", test.scope.Availability, test.want)
			}
			wantReadable := test.want == biz.PodAvailabilityLive
			if test.scope.WorkerReadable() != wantReadable {
				t.Fatalf("WorkerReadable()=%v for %s", test.scope.WorkerReadable(), test.want)
			}
		})
	}
}

// A rejected or never-created execution exposes no TrainJob identity, so no
// adapter call can be built from it.
func TestExecutionWorkloadScopeRequiresNamespaceIdentity(t *testing.T) {
	record := workloadExecutionFixture(workloadPlan())
	record.Execution.Snapshot.Environment.NamespaceUID = ""
	if _, err := biz.ExecutionWorkloadScopeFor(record); !errors.Is(err, biz.ErrWorkloadViewUnavailable) {
		t.Fatalf("missing namespace UID accepted: %v", err)
	}
}

// Pipeline reads need the frozen Run/Workflow identity as well as the namespace.
func TestExecutionWorkloadScopePipelineRequiresAuthority(t *testing.T) {
	record := workloadExecutionFixture(workloadPlan())
	scope := mustScope(t, record)
	if scope.PipelineReadable() {
		t.Fatal("pipeline readable without a bound Workflow")
	}
	record.Authority = &biz.RunAuthorityCandidate{RunID: "run-1", WorkflowName: "run-1", WorkflowUID: "workflow-uid", NamespaceName: "tenant-training", NamespaceUID: "namespace-uid"}
	scope = mustScope(t, record)
	if !scope.PipelineReadable() {
		t.Fatal("pipeline not readable with a bound Workflow")
	}
	record.Authority.WorkflowUID = ""
	if _, err := biz.ExecutionWorkloadScopeFor(record); !errors.Is(err, biz.ErrWorkloadViewUnavailable) {
		t.Fatalf("authority without Workflow UID accepted: %v", err)
	}
}

// Events merge only when every displayable field matches; two different
// messages stay two rows so neither root cause is lost.
func TestAggregateExecutionEventsKeepsDistinctMessages(t *testing.T) {
	at := time.Date(2026, 10, 23, 1, 0, 0, 0, time.UTC)
	base := biz.ExecutionEvent{Type: "Warning", Reason: "FailedScheduling", Source: "kube-scheduler",
		InvolvedKind: "Pod", InvolvedName: "md-pod", InvolvedUID: "pod-uid", Count: 1, FirstTimestamp: at, LastTimestamp: at}
	second := base
	second.Count = 4
	second.FirstTimestamp = at.Add(-time.Minute)
	second.LastTimestamp = at.Add(time.Minute)
	second.ReportingInstance = "scheduler-2"
	otherMessage := base
	otherMessage.Message = "a different failure"
	otherMessage.Count = 2

	merged := biz.AggregateExecutionEvents([]biz.ExecutionEvent{base, second, otherMessage})
	if len(merged) != 2 {
		t.Fatalf("merged=%d want 2", len(merged))
	}
	if merged[0].Count != 5 || !merged[0].FirstTimestamp.Equal(at.Add(-time.Minute)) || !merged[0].LastTimestamp.Equal(at.Add(time.Minute)) || merged[0].ReportingInstance != "scheduler-2" {
		t.Fatalf("aggregation: %+v", merged[0])
	}
	if merged[1].Message != "a different failure" || merged[1].Count != 2 {
		t.Fatalf("second message: %+v", merged[1])
	}
}

func mustScope(t *testing.T, record biz.OperationRecord) biz.ExecutionWorkloadScope {
	t.Helper()
	scope, err := biz.ExecutionWorkloadScopeFor(record)
	if err != nil {
		t.Fatalf("ExecutionWorkloadScopeFor: %v", err)
	}
	return scope
}

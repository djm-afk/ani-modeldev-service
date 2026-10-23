package service

import (
	"context"
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// workloadQueryRepository serves one committed aggregate; every other
// execution or tenant is absent, exactly like the real tenant-scoped read.
type workloadQueryRepository struct{ record biz.OperationRecord }

func (r workloadQueryRepository) GetOperationRecord(_ context.Context, tenant, execution string) (biz.OperationRecord, error) {
	if tenant != r.record.Execution.TenantID || execution != r.record.Execution.ExecutionID {
		return biz.OperationRecord{}, biz.ErrExecutionNotFound
	}
	return r.record, nil
}

func (workloadQueryRepository) ReserveCleanup(context.Context, biz.CleanupPlan, string) (biz.CleanupReceipt, bool, error) {
	return biz.CleanupReceipt{}, false, biz.ErrPersistence
}

func (workloadQueryRepository) ExecuteCleanup(context.Context, string, string, string, func(biz.OperationRecord) (biz.CleanupReceipt, error)) (biz.CleanupReceipt, error) {
	return biz.CleanupReceipt{}, biz.ErrPersistence
}

func (workloadQueryRepository) ReconcileCleanup(context.Context, string, string, string, func(biz.OperationRecord) (biz.CleanupReceipt, error)) (biz.CleanupReceipt, error) {
	return biz.CleanupReceipt{}, biz.ErrPersistence
}

// noWorkerPods is a live adapter that finds nothing: the API objects are gone.
type noWorkerPods struct{}

func (noWorkerPods) ListWorkerPods(context.Context, biz.ExecutionWorkloadScope) ([]biz.ExecutionPod, error) {
	return nil, nil
}

func (noWorkerPods) ListTrainingWorkloadEvents(context.Context, biz.ExecutionWorkloadScope, []biz.ExecutionPod) ([]biz.ExecutionEvent, error) {
	return nil, nil
}

// collectedExecutionFixture is an execution that finished and whose Pods have
// been garbage collected; only the durable observation still names them.
func collectedExecutionFixture(t *testing.T) (biz.OperationRecord, context.Context) {
	t.Helper()
	executionID := "608aa6bf-4dac-4989-bd22-9b0ef6f1ce68"
	observed := time.Date(2026, 10, 14, 1, 0, 10, 0, time.UTC)
	handle := biz.TrainingHandle{NamespaceUID: "namespace-original", TrainJobUID: "train-original", PVCUID: "pvc-original"}
	record := biz.OperationRecord{
		QueryRecord: biz.QueryRecord{
			Execution: biz.Execution{Admission: biz.Admission{
				TenantID: workloadTenantForTest, ExecutionID: executionID,
				Snapshot: cpup01.Snapshot{
					Environment: cpup01.EnvironmentBindingSnapshot{NamespaceName: "tenant-training", NamespaceUID: "namespace-original"},
					RuntimePlan: cpup01.RuntimePlan{ExpectedSpec: []byte(`{"podGroupPolicy":{"volcano":{}}}`)},
				},
			}},
			Runtime: biz.ExecutionRuntime{
				Training:       &biz.TrainingPlan{Name: "md-" + executionID},
				TrainingHandle: &handle,
				Observation: &biz.TrainingRuntimeObservation{Handle: handle, ObservedAt: observed, Resources: []biz.RuntimeResource{
					{APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainJob", Namespace: "tenant-training", Name: "md-" + executionID, UID: handle.TrainJobUID, APIObjectPresent: false},
					{APIVersion: "v1", Kind: "Pod", Namespace: "tenant-training", Name: "md-pod-0", UID: "pod-uid-0", OwnerUID: "job-original", APIObjectPresent: false, ExitCode: exitCodePointer(0)},
				}},
				ComputeCleanup: &biz.ComputeCleanupReceipt{LogsReady: true},
			},
		},
	}
	ctx := WithVerifiedGovernanceQuery(context.Background(), GovernanceDelivery{TenantID: workloadTenantForTest, RequestID: "request-pods"}, modeldevv1.ModelDevQueryService_ListExecutionPods_FullMethodName)
	return record, ctx
}

func exitCodePointer(value int32) *int32 { return &value }

// A collected execution must report RECYCLED at the response level as well as
// per Pod. Reporting LIVE would claim a Kubernetes object that no longer exists,
// and RECYCLED must never be smuggled into the phase field.
func TestListExecutionPodsReportsRecycledAfterCollection(t *testing.T) {
	record, ctx := collectedExecutionFixture(t)
	query := NewQuery(recordedLogQueryRepository{}, nil).WithOperations(workloadQueryRepository{record}).WithWorkload(noWorkerPods{}, nil)
	response, err := query.ListExecutionPods(ctx, &modeldevv1.ListExecutionPodsRequest{ExecutionId: record.Execution.ExecutionID, IncludeRecycled: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Pods) != 1 {
		t.Fatalf("pods=%d, want the durable observation", len(response.Pods))
	}
	pod := response.Pods[0]
	if response.Availability != modeldevv1.PodAvailability_POD_AVAILABILITY_RECYCLED {
		t.Fatalf("response availability=%v, want RECYCLED", response.Availability)
	}
	if pod.Availability != modeldevv1.PodAvailability_POD_AVAILABILITY_RECYCLED || pod.Phase != "" {
		t.Fatalf("pod availability=%v phase=%q", pod.Availability, pod.Phase)
	}
	if pod.LogId == "" || !pod.LogAvailable || pod.ExitCode == nil || *pod.ExitCode != 0 {
		t.Fatalf("recycled pod lost its log receipt or exit code: %+v", pod)
	}
}

// Without the recycled opt-in the list is empty, but the response must not
// claim the execution is alive: there is no live Pod to prove it.
func TestListExecutionPodsOmitsRecycledUnlessAsked(t *testing.T) {
	record, ctx := collectedExecutionFixture(t)
	query := NewQuery(recordedLogQueryRepository{}, nil).WithOperations(workloadQueryRepository{record}).WithWorkload(noWorkerPods{}, nil)
	response, err := query.ListExecutionPods(ctx, &modeldevv1.ListExecutionPodsRequest{ExecutionId: record.Execution.ExecutionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Pods) != 0 {
		t.Fatalf("pods=%d, recycled rows must be opt-in", len(response.Pods))
	}
	if response.Availability != modeldevv1.PodAvailability_POD_AVAILABILITY_LIVE {
		t.Fatalf("response availability=%v, want the create evidence LIVE", response.Availability)
	}
}

// A live worker Pod keeps the response LIVE even when a durable row is merged in.
func TestOverallAvailabilityPrefersLiveWorkerPods(t *testing.T) {
	workload := biz.ExecutionWorkloadScope{Availability: biz.PodAvailabilityLive}
	pods := []biz.ExecutionPod{{Availability: biz.PodAvailabilityRecycled}, {Availability: biz.PodAvailabilityLive}}
	if got := overallAvailability(workload, pods); got != biz.PodAvailabilityLive {
		t.Fatalf("availability=%s, want LIVE", got)
	}
	if got := overallAvailability(workload, []biz.ExecutionPod{{Availability: biz.PodAvailabilityRecycled}}); got != biz.PodAvailabilityRecycled {
		t.Fatalf("availability=%s, want RECYCLED", got)
	}
	if got := overallAvailability(workload, nil); got != biz.PodAvailabilityLive {
		t.Fatalf("availability=%s, want the create evidence LIVE", got)
	}
}

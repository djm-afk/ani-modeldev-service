package service

import (
	"context"
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
					{APIVersion: "v1", Kind: "Pod", Namespace: "tenant-training", Name: "md-pod-0", UID: "pod-uid-0", OwnerUID: "job-original", APIObjectPresent: false, Terminal: true, ExitCode: exitCodePointer(0)},
					{APIVersion: "jobset.x-k8s.io/v1alpha2", Kind: "JobSet", Namespace: "tenant-training", Name: "set-original", UID: "set-original", OwnerUID: handle.TrainJobUID, APIObjectPresent: false},
					{APIVersion: "batch/v1", Kind: "Job", Namespace: "tenant-training", Name: "job-original", UID: "job-original", OwnerUID: "set-original", APIObjectPresent: false},
				}},
			},
		},
	}
	source, err := biz.TrainingLogSourceFor(record.QueryRecord, biz.TrainerLogID(executionID, "pod-uid-0"))
	if err != nil {
		t.Fatal("original collected worker source", err)
	}
	record.Runtime.Observation.Resources[1].TrainingLog = &biz.RecordedTrainingLog{Source: source, Result: biz.TrainingLogResult{ObservedAt: observed, Lines: []biz.TrainingLogLine{{Timestamp: observed, Text: "retained original worker"}}}}
	ctx := WithVerifiedGovernanceQuery(context.Background(), GovernanceDelivery{TenantID: workloadTenantForTest, RequestID: "request-pods"}, modeldevv1.ModelDevQueryService_ListExecutionPods_FullMethodName)
	return record, ctx
}

func TestRecycledPodDoesNotAdvertiseCleanupOnlyLog(t *testing.T) {
	record, ctx := collectedExecutionFixture(t)
	record.Runtime.Observation.Resources[1].TrainingLog = nil
	record.Runtime.ComputeCleanup = &biz.ComputeCleanupReceipt{LogsReady: true}
	query := NewQuery(recordedLogQueryRepository{}, nil).WithOperations(workloadQueryRepository{record}).WithWorkload(noWorkerPods{}, nil)
	response, err := query.ListExecutionPods(ctx, &modeldevv1.ListExecutionPodsRequest{ExecutionId: record.Execution.ExecutionID, IncludeRecycled: true})
	if err != nil || len(response.Pods) != 1 || response.Pods[0].LogAvailable {
		t.Fatalf("cleanup-only flag advertised a readable original Pod log: %+v %v", response, err)
	}
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
// claim the execution is alive: the live read proved nothing and the durable
// observation says the Pods existed.
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
	if response.Availability != modeldevv1.PodAvailability_POD_AVAILABILITY_RECYCLED {
		t.Fatalf("response availability=%v, want RECYCLED: the Pods were observed and are gone", response.Availability)
	}
}

// An execution that never got a Pod must not be reported as recycled; only a
// recorded worker Pod makes an empty live read mean "collected".
func TestOverallAvailabilityKeepsCreateEvidenceWithoutObservedPods(t *testing.T) {
	workload := biz.ExecutionWorkloadScope{Availability: biz.PodAvailabilityLive}
	if got := overallAvailability(workload, nil, biz.OperationRecord{}); got != biz.PodAvailabilityLive {
		t.Fatalf("availability=%s, want LIVE with no observation", got)
	}
	observed := biz.OperationRecord{QueryRecord: biz.QueryRecord{Runtime: biz.ExecutionRuntime{Observation: &biz.TrainingRuntimeObservation{Resources: []biz.RuntimeResource{
		{Kind: "TrainJob", UID: "train"}, {Kind: "Pod", UID: "pod-uid"},
	}}}}}
	if got := overallAvailability(workload, nil, observed); got != biz.PodAvailabilityRecycled {
		t.Fatalf("availability=%s, want RECYCLED", got)
	}
}

// A live worker Pod keeps the response LIVE even when a durable row is merged in.
func TestOverallAvailabilityPrefersLiveWorkerPods(t *testing.T) {
	workload := biz.ExecutionWorkloadScope{Availability: biz.PodAvailabilityLive}
	pods := []biz.ExecutionPod{{Availability: biz.PodAvailabilityRecycled}, {Availability: biz.PodAvailabilityLive}}
	if got := overallAvailability(workload, pods, biz.OperationRecord{}); got != biz.PodAvailabilityLive {
		t.Fatalf("availability=%s, want LIVE", got)
	}
	if got := overallAvailability(workload, []biz.ExecutionPod{{Availability: biz.PodAvailabilityRecycled}}, biz.OperationRecord{}); got != biz.PodAvailabilityRecycled {
		t.Fatalf("availability=%s, want RECYCLED", got)
	}
	if got := overallAvailability(workload, nil, biz.OperationRecord{}); got != biz.PodAvailabilityLive {
		t.Fatalf("availability=%s, want the create evidence LIVE", got)
	}
}

// A collected execution's events are already expired by retention: the answer is
// a hint plus an empty list, never an error.
func TestListExecutionEventsExpiredKeepsTheRetentionHint(t *testing.T) {
	record, ctx := collectedExecutionFixture(t)
	query := NewQuery(recordedLogQueryRepository{}, nil).WithOperations(workloadQueryRepository{record}).WithWorkload(noWorkerPods{}, nil)
	response, err := query.ListExecutionEvents(withQueryMethod(ctx, modeldevv1.ModelDevQueryService_ListExecutionEvents_FullMethodName), &modeldevv1.ListExecutionEventsRequest{ExecutionId: record.Execution.ExecutionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Events) != 0 || response.RetentionHint != eventRetentionHint || response.ObservedAt == nil {
		t.Fatalf("expired events: %+v", response)
	}
}

// A tenant that does not own the execution sees no Pod, no event and no detail.
func TestWorkloadViewsAreTenantScoped(t *testing.T) {
	record, ctx := collectedExecutionFixture(t)
	foreign := WithVerifiedGovernanceQuery(context.Background(), GovernanceDelivery{TenantID: "00000000-0000-4000-8000-000000000000", RequestID: "request-foreign"},
		modeldevv1.ModelDevQueryService_ListExecutionPods_FullMethodName)
	query := NewQuery(recordedLogQueryRepository{}, nil).WithOperations(workloadQueryRepository{record}).WithWorkload(noWorkerPods{}, nil)
	if _, err := query.ListExecutionPods(foreign, &modeldevv1.ListExecutionPodsRequest{ExecutionId: record.Execution.ExecutionID}); status.Code(err) != codes.NotFound {
		t.Fatalf("cross-tenant pods: %v", err)
	}
	foreignEvents := WithVerifiedGovernanceQuery(context.Background(), GovernanceDelivery{TenantID: "00000000-0000-4000-8000-000000000000", RequestID: "request-foreign"},
		modeldevv1.ModelDevQueryService_ListExecutionEvents_FullMethodName)
	if _, err := query.ListExecutionEvents(foreignEvents, &modeldevv1.ListExecutionEventsRequest{ExecutionId: record.Execution.ExecutionID}); status.Code(err) != codes.NotFound {
		t.Fatalf("cross-tenant events: %v", err)
	}
	if _, err := query.ListExecutionPods(ctx, &modeldevv1.ListExecutionPodsRequest{ExecutionId: record.Execution.ExecutionID}); err != nil {
		t.Fatalf("owning tenant lost its view: %v", err)
	}
}

// A step Pod that is already gone has no archive reader in M1, so a request that
// proves it addresses that step Pod gets STEP_LOGS_UNAVAILABLE rather than a
// worker log or an empty success.
func TestStepLogRequestForAGonePodIsUnavailable(t *testing.T) {
	record, _ := collectedExecutionFixture(t)
	record.Authority = &biz.RunAuthorityCandidate{RunID: "run-1", WorkflowName: "run-1", WorkflowUID: "workflow-uid", NamespaceName: "tenant-training", NamespaceUID: "namespace-original"}
	ctx := WithVerifiedGovernanceQuery(context.Background(), GovernanceDelivery{TenantID: workloadTenantForTest, RequestID: "request-step"},
		modeldevv1.ModelDevQueryService_GetExecutionLogs_FullMethodName)
	stepPodUID := "step-pod-uid"
	query := NewQuery(recordedLogQueryRepository{record.QueryRecord}, nil).WithOperations(workloadQueryRepository{record}).WithWorkload(noWorkerPods{}, &goneStepPods{})
	logID := biz.StepLogID(record.Execution.ExecutionID, stepPodUID)
	_, err := query.GetExecutionLogs(ctx, &modeldevv1.GetExecutionLogsRequest{ExecutionId: record.Execution.ExecutionID, PodUid: &stepPodUID, LogId: &logID})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("gone step Pod: %v", err)
	}
	// Without the step credential the same Pod UID is just an unknown worker Pod.
	foreignLogID := "5f3f767d-590a-49bd-850d-5c3595781173"
	if _, err = query.GetExecutionLogs(ctx, &modeldevv1.GetExecutionLogsRequest{ExecutionId: record.Execution.ExecutionID, PodUid: &stepPodUID, LogId: &foreignLogID}); status.Code(err) != codes.NotFound {
		t.Fatalf("unproven step Pod: %v", err)
	}
}

// goneStepPods is a verified pipeline whose step Pods are all garbage collected.
type goneStepPods struct{}

func (goneStepPods) ListStepPods(context.Context, biz.ExecutionWorkloadScope) ([]biz.ExecutionPod, error) {
	return nil, nil
}

func (goneStepPods) StepPod(context.Context, biz.ExecutionWorkloadScope, string) (biz.ExecutionPod, error) {
	return biz.ExecutionPod{}, biz.ErrTrainingLogNotFound
}

func (goneStepPods) ListWorkflowEvents(context.Context, biz.ExecutionWorkloadScope) ([]biz.ExecutionEvent, error) {
	return nil, nil
}

func withQueryMethod(ctx context.Context, method string) context.Context {
	delivery, _ := ctx.Value(governanceQueryKey{}).(governanceQuery)
	return WithVerifiedGovernanceQuery(ctx, delivery.delivery, method)
}

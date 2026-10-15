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

type recordedLogQueryRepository struct{ record biz.QueryRecord }

func (r recordedLogQueryRepository) GetQueryRecord(_ context.Context, tenant, execution string) (biz.QueryRecord, error) {
	if tenant != r.record.Execution.TenantID || execution != r.record.Execution.ExecutionID {
		return biz.QueryRecord{}, biz.ErrExecutionNotFound
	}
	return r.record, nil
}

func (recordedLogQueryRepository) ListQueryRecords(context.Context, string, biz.ExecutionQuery) ([]biz.QueryRecord, string, error) {
	return nil, "", biz.ErrPersistence
}

func (recordedLogQueryRepository) CountQueryRecords(context.Context, string, biz.ExecutionQuery) (uint64, error) {
	return 0, biz.ErrPersistence
}

func (recordedLogQueryRepository) FindPublishedArtifact(context.Context, string, string) (biz.QueryRecord, biz.PublishedRuntimeFile, error) {
	return biz.QueryRecord{}, biz.PublishedRuntimeFile{}, biz.ErrArtifactNotFound
}

type recordedLogLiveReader struct{ result biz.TrainingLogResult }

func (r recordedLogLiveReader) ReadTrainingLogs(context.Context, biz.TrainingLogSource, biz.TrainingLogOptions) (biz.TrainingLogResult, error) {
	return r.result, nil
}

func recordedLogQueryFixture(t *testing.T) (biz.QueryRecord, biz.TrainingLogSource, context.Context) {
	t.Helper()
	observed := time.Date(2026, 10, 14, 1, 0, 10, 0, time.UTC)
	handle := biz.TrainingHandle{NamespaceUID: "namespace-original", TrainJobUID: "train-original", PVCUID: "pvc-original"}
	record := biz.QueryRecord{
		Execution: biz.Execution{Admission: biz.Admission{TenantID: "9086ce3b-bd19-47c3-8467-190607f633db", ExecutionID: "608aa6bf-4dac-4989-bd22-9b0ef6f1ce68", Snapshot: cpup01.Snapshot{Environment: cpup01.EnvironmentBindingSnapshot{NamespaceName: "tenant-training", NamespaceUID: handle.NamespaceUID}}}},
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
	record.Runtime.ComputeCleanup = &biz.ComputeCleanupReceipt{LogsReady: true, Logs: []biz.RecordedTrainingLog{{Source: source, Result: biz.TrainingLogResult{ObservedAt: observed, Lines: []biz.TrainingLogLine{
		{Timestamp: observed.Add(-2 * time.Second), Text: "archived first"},
		{Timestamp: observed.Add(-time.Second), Text: "archived final"},
	}}}}}
	ctx := WithVerifiedGovernanceQuery(context.Background(), GovernanceDelivery{TenantID: record.Execution.TenantID, RequestID: "request-archive"}, modeldevv1.ModelDevQueryService_GetExecutionLogs_FullMethodName)
	return record, source, ctx
}

func TestExecutionLogsUseOriginalArchiveWithoutLiveReader(t *testing.T) {
	record, source, ctx := recordedLogQueryFixture(t)
	query := NewQuery(recordedLogQueryRepository{record}, nil)
	response, err := query.GetExecutionLogs(ctx, &modeldevv1.GetExecutionLogsRequest{ExecutionId: record.Execution.ExecutionID, TailLines: 1})
	if err != nil || len(response.GetLines()) != 1 || response.Lines[0].Text != "archived final" || !response.Truncated || response.Source.ResourceUid != "pod-original" || response.Source.LogId != source.LogID || response.Source.ContainerName != "node" || !response.ObservedAt.AsTime().Equal(record.Runtime.ComputeCleanup.Logs[0].Result.ObservedAt) {
		t.Fatalf("original archived log unavailable after Pod removal: response=%v error=%v", response, err)
	}
}

func TestExecutionLogsPreferArchiveAndUseLiveOnlyBeforeCaptureCommits(t *testing.T) {
	for _, ready := range []bool{true, false} {
		t.Run(map[bool]string{true: "committed archive", false: "capture pending"}[ready], func(t *testing.T) {
			record, _, ctx := recordedLogQueryFixture(t)
			record.Runtime.ComputeCleanup.LogsReady = ready
			live := recordedLogLiveReader{biz.TrainingLogResult{ObservedAt: record.Runtime.Observation.ObservedAt, Lines: []biz.TrainingLogLine{{Timestamp: record.Runtime.Observation.ObservedAt, Text: "original live"}}}}
			query := NewQuery(recordedLogQueryRepository{record}, nil, live)
			response, err := query.GetExecutionLogs(ctx, &modeldevv1.GetExecutionLogsRequest{ExecutionId: record.Execution.ExecutionID, TailLines: 1})
			want := "original live"
			if ready {
				want = "archived final"
			}
			if err != nil || len(response.GetLines()) != 1 || response.Lines[0].Text != want {
				t.Fatalf("archive/live selection changed: response=%v error=%v want=%q", response, err, want)
			}
		})
	}
}

func TestExecutionLogsRejectForeignArchiveInsteadOfLiveFallback(t *testing.T) {
	record, _, ctx := recordedLogQueryFixture(t)
	record.Runtime.ComputeCleanup.Logs[0].Source.PodUID = "same-name-replacement"
	live := recordedLogLiveReader{biz.TrainingLogResult{ObservedAt: record.Runtime.Observation.ObservedAt, Lines: []biz.TrainingLogLine{{Timestamp: record.Runtime.Observation.ObservedAt, Text: "live must not hide corrupt archive"}}}}
	query := NewQuery(recordedLogQueryRepository{record}, nil, live)
	if _, err := query.GetExecutionLogs(ctx, &modeldevv1.GetExecutionLogsRequest{ExecutionId: record.Execution.ExecutionID}); status.Code(err) != codes.Unavailable {
		t.Fatalf("foreign archive escaped identity validation: %v", err)
	}
}

func TestExecutionLogArchiveStillRequiresCurrentExactQueryAuthorization(t *testing.T) {
	record, _, ctx := recordedLogQueryFixture(t)
	query := NewQuery(recordedLogQueryRepository{record}, nil)
	request := &modeldevv1.GetExecutionLogsRequest{ExecutionId: record.Execution.ExecutionID}
	if _, err := query.GetExecutionLogs(context.Background(), request); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("archive bypassed current authorization: %v", err)
	}
	if _, err := query.GetExecutionLogs(WithVerifiedGovernanceQuery(ctx, GovernanceDelivery{TenantID: record.Execution.TenantID}, modeldevv1.ModelDevQueryService_GetExecution_FullMethodName), request); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("archive reused another method authorization: %v", err)
	}
	if _, err := query.GetExecutionLogs(WithVerifiedGovernanceQuery(ctx, GovernanceDelivery{TenantID: "foreign-tenant"}, modeldevv1.ModelDevQueryService_GetExecutionLogs_FullMethodName), request); status.Code(err) != codes.NotFound {
		t.Fatalf("archive crossed tenant boundary: %v", err)
	}
	unknown := "5f3f767d-590a-49bd-850d-5c3595781173"
	request.LogId = &unknown
	if _, err := query.GetExecutionLogs(ctx, request); status.Code(err) != codes.NotFound {
		t.Fatalf("archive accepted foreign source log ID: %v", err)
	}
}

//go:build cpu_mainflow

package submittest_test

import (
	"context"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/traininglogs"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

func newMainFlowTrainingLogReader(t *testing.T, f *completeFixture) biz.TrainingLogReader {
	t.Helper()
	client, err := coreclient.NewForConfig(&rest.Config{Host: f.kube.URL, BearerToken: "synthetic-kube-owner", TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.kube.Certificate().Raw})}})
	if err != nil {
		t.Fatal(err)
	}
	return traininglogs.New(client)
}

func assertActualTrainingLogs(t *testing.T, ctx context.Context, client modeldevv1.ModelDevQueryServiceClient, f *completeFixture) {
	t.Helper()
	admission := f.request.Admission
	method := modeldevv1.ModelDevQueryService_GetExecutionLogs_FullMethodName
	originalPodUID := completePodUID
	request := &modeldevv1.GetExecutionLogsRequest{ExecutionId: admission.ExecutionID, PodUid: &originalPodUID, TailLines: 1000, MaxBytes: 65536}
	logs, err := client.GetExecutionLogs(queryCall(ctx, admission, method), request)
	if err != nil || logs.GetSource().GetResourceUid() != completePodUID || logs.GetSource().GetContainerName() != "node" || len(logs.GetLines()) == 0 {
		t.Fatalf("ACTUAL_TRAINING_LOGS_NOT_IMPLEMENTED: completed actual MLP cannot be read through current query authorization: %v", err)
	}
	if id, err := uuid.Parse(logs.Source.LogId); err != nil || id == uuid.Nil || logs.ObservedAt == nil || logs.ObservedAt.CheckValid() != nil {
		t.Fatal("training log has no stable owner reference or observation time")
	}
	text := make([]string, 0, len(logs.Lines))
	for _, line := range logs.Lines {
		if line.Timestamp == nil || line.Timestamp.CheckValid() != nil {
			t.Fatal("training line missing actual timestamp")
		}
		text = append(text, line.Text)
	}
	f.mu.Lock()
	actual := strings.TrimSuffix(string(f.trainingLog), "\n")
	f.mu.Unlock()
	if strings.Join(text, "\n") != actual || !strings.Contains(actual, `"step": 48`) || logs.Truncated {
		t.Fatal("query returned waiting-step logs or changed actual MLP stdout")
	}
	tail, err := client.GetExecutionLogs(queryCall(ctx, admission, method), &modeldevv1.GetExecutionLogsRequest{ExecutionId: admission.ExecutionID, PodUid: &originalPodUID, TailLines: 2, MaxBytes: 65536})
	if err != nil || len(tail.GetLines()) != 2 || !tail.Truncated || tail.Lines[1].Text != text[len(text)-1] {
		t.Fatalf("actual training tail is unbounded or lost its final line: %v", err)
	}
	bad := "eeeeeeee-1111-4222-8333-444444444444"
	request.LogId = &bad
	if _, err := client.GetExecutionLogs(queryCall(ctx, admission, method), request); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown owner log ID did not fail closed: %v", err)
	}
	request.LogId = nil
	other := admission
	other.TenantID = "99999999-aaaa-4bbb-8ccc-111111111111"
	if _, err := client.GetExecutionLogs(queryCall(ctx, other, method), request); status.Code(err) != codes.NotFound {
		t.Fatalf("training logs crossed tenant boundary: %v", err)
	}
	if _, err := client.GetExecutionLogs(queryCall(ctx, admission, modeldevv1.ModelDevQueryService_GetExecution_FullMethodName), request); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("training logs reused another method permission: %v", err)
	}
	t.Log("MAIN_FLOW: actual 48-step MLP node stdout queried through mTLS after reconnect; bounded tail and current tenant authorization verified")
}

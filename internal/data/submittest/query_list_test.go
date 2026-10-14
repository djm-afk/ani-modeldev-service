//go:build cpu_mainflow

package submittest_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMainFlowQueryListFiltersCurrentTenantBeforePagination(t *testing.T) {
	open := postgres.Prepare(t)
	pool := open()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	base := dispatchRequest(t).Admission
	base.Actor = "governance:user:42"
	first := base
	first.ExecutionID = "10000000-1111-4222-8333-444444444444"
	first.OperationID = "10000000-1111-4222-8333-555555555555"
	foreign := base
	foreign.TenantID = "99999999-aaaa-4bbb-8ccc-111111111111"
	foreign.ExecutionID = "20000000-1111-4222-8333-444444444444"
	foreign.OperationID = "20000000-1111-4222-8333-555555555555"
	last := base
	last.ExecutionID = "30000000-1111-4222-8333-444444444444"
	last.OperationID = "30000000-1111-4222-8333-555555555555"
	repository := execution.New(pool)
	acceptThroughCommandRPC(t, ctx, repository, first)
	acceptThroughCommandRPC(t, ctx, repository, foreign)
	acceptThroughCommandRPC(t, ctx, repository, last)
	client := startMainFlowQuery(t, pool, nil, base)
	method := modeldevv1.ModelDevQueryService_ListExecutions_FullMethodName
	request := &modeldevv1.ListExecutionsRequest{Page: &modeldevv1.PageRequest{Limit: 1}}
	page, err := client.ListExecutions(queryCall(ctx, base, method), request)
	if err != nil || len(page.GetExecutions()) != 1 || page.Executions[0].Identity.ExecutionId != first.ExecutionID || page.NextCursor == "" || page.Total != 2 {
		t.Fatalf("QUERY_LIST_NOT_IMPLEMENTED: current tenant cannot list its first execution: %v %v", page, err)
	}
	request.Page.Cursor = page.NextCursor
	page, err = client.ListExecutions(queryCall(ctx, base, method), request)
	if err != nil || len(page.GetExecutions()) != 1 || page.Executions[0].Identity.ExecutionId != last.ExecutionID || page.NextCursor != "" || page.Total != 2 {
		t.Fatalf("tenant filter was applied after pagination: %v %v", page, err)
	}
	published := modeldevv1.DeliveryState_DELIVERY_STATE_PUBLISHED
	page, err = client.ListExecutions(queryCall(ctx, base, method), &modeldevv1.ListExecutionsRequest{DeliveryState: &published})
	if err != nil || len(page.GetExecutions()) != 0 || page.NextCursor != "" || page.Total != 0 {
		t.Fatalf("list state filter did not use durable execution facts: %v %v", page, err)
	}
	for _, denied := range []context.Context{ctx, queryCall(ctx, base, modeldevv1.ModelDevQueryService_GetExecution_FullMethodName)} {
		if _, err := client.ListExecutions(denied, request); status.Code(err) != codes.Unauthenticated && status.Code(err) != codes.PermissionDenied {
			t.Fatalf("list bypassed current method permission: %v", err)
		}
	}
	page, err = client.ListExecutions(queryCall(ctx, foreign, method), &modeldevv1.ListExecutionsRequest{})
	if err != nil || len(page.GetExecutions()) != 1 || page.Executions[0].Identity.ExecutionId != foreign.ExecutionID {
		t.Fatalf("list disclosed another tenant: %v %v", page, err)
	}
}

func assertQueryCloseState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, admission biz.Admission, want modeldevv1.CloseState) {
	t.Helper()
	client := startMainFlowQuery(t, pool, nil, admission)
	detail, err := client.GetExecution(queryCall(ctx, admission, modeldevv1.ModelDevQueryService_GetExecution_FullMethodName), &modeldevv1.GetExecutionRequest{ExecutionId: admission.ExecutionID})
	if err != nil || detail.GetExecution().GetStates().GetCloseState() != want {
		t.Errorf("QUERY_CLOSE_REVIEW_NOT_IMPLEMENTED: detail close=%v want=%v err=%v", detail.GetExecution().GetStates().GetCloseState(), want, err)
	}
	page, err := client.ListExecutions(queryCall(ctx, admission, modeldevv1.ModelDevQueryService_ListExecutions_FullMethodName), &modeldevv1.ListExecutionsRequest{CloseState: &want})
	if err != nil || len(page.GetExecutions()) != 1 || page.Executions[0].GetIdentity().GetExecutionId() != admission.ExecutionID || page.Executions[0].GetStates().GetCloseState() != want {
		t.Errorf("QUERY_CLOSE_REVIEW_NOT_IMPLEMENTED: current tenant list lost close-state projection/filter: want=%v count=%d err=%v", want, len(page.GetExecutions()), err)
	}
}

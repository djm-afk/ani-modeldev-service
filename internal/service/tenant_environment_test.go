package service

import (
	"context"
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTenantEnvironmentRequiresExactVerifiedMethodAndNoUnknownFields(t *testing.T) {
	s := NewTenantEnvironment(nil)
	scope := GovernanceDelivery{TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:user:42", RequestID: "request-42"}
	valid := &modeldevv1.EnsureTenantExecutionEnvironmentRequest{OperationId: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}
	for _, method := range []string{"", modeldevv1.ModelDevTenantEnvironmentService_GetTenantExecutionEnvironment_FullMethodName} {
		ctx := WithVerifiedGovernanceQuery(context.Background(), scope, method)
		if _, e := s.EnsureTenantExecutionEnvironment(ctx, valid); status.Code(e) != codes.Unauthenticated {
			t.Fatalf("wrong method accepted: %v", e)
		}
	}
	ctx := WithVerifiedGovernanceQuery(context.Background(), scope, modeldevv1.ModelDevTenantEnvironmentService_EnsureTenantExecutionEnvironment_FullMethodName)
	unknown := &modeldevv1.EnsureTenantExecutionEnvironmentRequest{OperationId: valid.OperationId}
	unknown.ProtoReflect().SetUnknown([]byte{0x10, 0x01})
	for _, request := range []*modeldevv1.EnsureTenantExecutionEnvironmentRequest{nil, {}, unknown, {OperationId: "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE"}} {
		if _, e := s.EnsureTenantExecutionEnvironment(ctx, request); status.Code(e) != codes.InvalidArgument {
			t.Fatalf("malformed request reached usecase: %v", e)
		}
	}
	getCtx := WithVerifiedGovernanceQuery(context.Background(), scope, modeldevv1.ModelDevTenantEnvironmentService_GetTenantExecutionEnvironment_FullMethodName)
	get := &modeldevv1.GetTenantExecutionEnvironmentRequest{}
	get.ProtoReflect().SetUnknown([]byte{0x08, 0x01})
	if _, e := s.GetTenantExecutionEnvironment(getCtx, get); status.Code(e) != codes.InvalidArgument {
		t.Fatal(e)
	}
}
func TestTenantEnvironmentViewPreservesBusyPersistentStateAndRejectsIncompleteReady(t *testing.T) {
	row := biz.TenantExecutionEnvironment{ID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", TenantID: "11111111-2222-4333-8444-555555555555", ClusterID: "22222222-3333-4444-8555-666666666666", NamespaceName: "tenant-11111111-2222-4333-8444-555555555555", State: biz.EnvironmentBlocked, Phase: biz.EnvironmentResources, ReasonCode: "KFP_ARTIFACT_ACCESS_NOT_ISOLATED", UpdatedAt: time.Now().UTC()}
	view, e := environmentView(row)
	if e != nil || view.State != modeldevv1.TenantExecutionEnvironmentState_TENANT_EXECUTION_ENVIRONMENT_STATE_BLOCKED || view.Retryable {
		t.Fatal("blocked observation changed", e)
	}
	row.State = biz.EnvironmentReady
	row.Phase = biz.EnvironmentComplete
	row.ReasonCode = ""
	if _, e = environmentView(row); e == nil {
		t.Fatal("incomplete READY returned")
	}
	row.State = biz.EnvironmentProvisioning
	row.Phase = biz.EnvironmentNamespace
	view, e = environmentView(row)
	if e != nil || !view.Retryable || view.RetryAfterSeconds != 2 || view.NamespaceUid != nil || view.BindingDigest != nil || view.ExperimentId != nil {
		t.Fatal("unobserved values fabricated", e)
	}
}

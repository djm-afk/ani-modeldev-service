package server_test

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/durationpb"
)

type modelBudgetBoundary struct {
	modeldevv1.UnimplementedModelDevQueryServiceServer
	modeldevv1.UnimplementedModelDevAdmissionServiceServer
	modeldevv1.UnimplementedModelDevManagementServiceServer
	remaining chan time.Duration
}

func (h *modelBudgetBoundary) budget(ctx context.Context) {
	deadline, ok := ctx.Deadline()
	if !ok {
		h.remaining <- 0
		return
	}
	h.remaining <- time.Until(deadline)
}
func (h *modelBudgetBoundary) CheckFinetuningReadiness(ctx context.Context, _ *modeldevv1.CheckFinetuningReadinessRequest) (*modeldevv1.CheckFinetuningReadinessResponse, error) {
	h.budget(ctx)
	return &modeldevv1.CheckFinetuningReadinessResponse{}, nil
}
func (h *modelBudgetBoundary) ListImplementations(ctx context.Context, _ *modeldevv1.ListImplementationsRequest) (*modeldevv1.ListImplementationsResponse, error) {
	h.budget(ctx)
	return &modeldevv1.ListImplementationsResponse{}, nil
}
func (h *modelBudgetBoundary) PrepareAdmission(ctx context.Context, _ *modeldevv1.PrepareAdmissionRequest) (*modeldevv1.PrepareAdmissionResponse, error) {
	h.budget(ctx)
	return &modeldevv1.PrepareAdmissionResponse{}, nil
}
func (h *modelBudgetBoundary) FinalizeAdmission(ctx context.Context, _ *modeldevv1.FinalizeAdmissionRequest) (*modeldevv1.FinalizeAdmissionResponse, error) {
	h.budget(ctx)
	return &modeldevv1.FinalizeAdmissionResponse{}, nil
}
func (h *modelBudgetBoundary) ImportCSV(ctx context.Context, _ *modeldevv1.ImportCSVRequest) (*modeldevv1.ImportCSVResponse, error) {
	h.budget(ctx)
	return &modeldevv1.ImportCSVResponse{}, nil
}

// The real listener must preserve each exact method's budget through both
// Kratos' envelope and the authenticated receiver, including an earlier caller.
func TestModelResolutionTransportUsesOnlyItsMethodBudget(t *testing.T) {
	certificates := commandtls.New(t)
	handler := &modelBudgetBoundary{remaining: make(chan time.Duration, 1)}
	s, err := server.NewGovernanceServicesServer(&conf.Server_GRPC{Network: "tcp", Addr: "127.0.0.1:0", Timeout: durationpb.New(200 * time.Millisecond)}, server.CommandTLS{Certificate: certificates.Server, ClientCAs: certificates.Roots, GovernanceDNSName: commandtls.GovernanceDNSName}, &modeldevv1.UnimplementedModelDevCommandServiceServer{}, handler, handler, server.GovernanceServices{Management: handler})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := s.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Stop(ctx); err != nil {
			t.Error(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-ctx.Done():
			t.Error("listener did not stop")
		}
	})
	connection, err := grpc.NewClient(endpoint.Host, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: certificates.Roots, ServerName: commandtls.ServerDNSName, Certificates: []tls.Certificate{certificates.Governance}})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	query, admission, management := modeldevv1.NewModelDevQueryServiceClient(connection), modeldevv1.NewModelDevAdmissionServiceClient(connection), modeldevv1.NewModelDevManagementServiceClient(connection)
	for _, tc := range []struct {
		name, method         string
		caller, lower, upper time.Duration
	}{
		{"readiness R", modeldevv1.ModelDevQueryService_CheckFinetuningReadiness_FullMethodName, 0, 299 * time.Second, 300 * time.Second},
		{"prepare R", modeldevv1.ModelDevAdmissionService_PrepareAdmission_FullMethodName, 0, 299 * time.Second, 300 * time.Second},
		{"short query B", modeldevv1.ModelDevQueryService_ListImplementations_FullMethodName, 0, 100 * time.Millisecond, 200 * time.Millisecond},
		{"finalize B", modeldevv1.ModelDevAdmissionService_FinalizeAdmission_FullMethodName, 0, 100 * time.Millisecond, 200 * time.Millisecond},
		{"import independent", modeldevv1.ModelDevManagementService_ImportCSV_FullMethodName, 0, 1799 * time.Second, 1800 * time.Second},
		{"earlier caller", modeldevv1.ModelDevQueryService_CheckFinetuningReadiness_FullMethodName, time.Second, 500 * time.Millisecond, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.caller > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.caller)
				defer cancel()
			}
			ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", uuid.NewString(), "x-ani-actor", "governance:user:3", "x-ani-request-id", uuid.NewString(), "x-ani-authorized-method", tc.method, "x-ani-data-scope", "tenant-all"))
			switch tc.method {
			case modeldevv1.ModelDevQueryService_CheckFinetuningReadiness_FullMethodName:
				_, err = query.CheckFinetuningReadiness(ctx, &modeldevv1.CheckFinetuningReadinessRequest{})
			case modeldevv1.ModelDevQueryService_ListImplementations_FullMethodName:
				_, err = query.ListImplementations(ctx, &modeldevv1.ListImplementationsRequest{})
			case modeldevv1.ModelDevAdmissionService_PrepareAdmission_FullMethodName:
				_, err = admission.PrepareAdmission(ctx, &modeldevv1.PrepareAdmissionRequest{})
			case modeldevv1.ModelDevAdmissionService_FinalizeAdmission_FullMethodName:
				_, err = admission.FinalizeAdmission(ctx, &modeldevv1.FinalizeAdmissionRequest{})
			case modeldevv1.ModelDevManagementService_ImportCSV_FullMethodName:
				_, err = management.ImportCSV(ctx, &modeldevv1.ImportCSVRequest{})
			}
			if err != nil {
				t.Fatal(err)
			}
			remaining := <-handler.remaining
			if remaining < tc.lower || remaining > tc.upper {
				t.Fatalf("method deadline remaining=%v want %v..%v", remaining, tc.lower, tc.upper)
			}
		})
	}
}

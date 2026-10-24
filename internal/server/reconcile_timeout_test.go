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

type reconcileBudgetBoundary struct {
	modeldevv1.UnimplementedModelDevOperationsServiceServer
	remaining chan time.Duration
}

func (h *reconcileBudgetBoundary) budget(ctx context.Context) {
	deadline, ok := ctx.Deadline()
	if !ok {
		h.remaining <- 0
		return
	}
	h.remaining <- time.Until(deadline)
}
func (h *reconcileBudgetBoundary) ReconcileExecution(ctx context.Context, _ *modeldevv1.ReconcileExecutionRequest) (*modeldevv1.ReconcileExecutionResponse, error) {
	h.budget(ctx)
	return &modeldevv1.ReconcileExecutionResponse{}, nil
}
func (h *reconcileBudgetBoundary) InspectExecution(ctx context.Context, _ *modeldevv1.InspectExecutionRequest) (*modeldevv1.InspectExecutionResponse, error) {
	h.budget(ctx)
	return &modeldevv1.InspectExecutionResponse{}, nil
}

// Exercise the actual mTLS transport, including Kratos' outer context cap.
func TestReconcileTransportBudgetPreservesOtherMethodsAndCallerDeadline(t *testing.T) {
	certificates := commandtls.New(t)
	handler := &reconcileBudgetBoundary{remaining: make(chan time.Duration, 1)}
	s, err := server.NewGovernanceServicesServer(&conf.Server_GRPC{Network: "tcp", Addr: "127.0.0.1:0", Timeout: durationpb.New(200 * time.Millisecond)}, server.CommandTLS{Certificate: certificates.Server, ClientCAs: certificates.Roots, GovernanceDNSName: commandtls.GovernanceDNSName}, &modeldevv1.UnimplementedModelDevCommandServiceServer{}, nil, nil, server.GovernanceServices{Operations: handler})
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
	client := modeldevv1.NewModelDevOperationsServiceClient(connection)
	for _, tc := range []struct {
		name, method         string
		caller, lower, upper time.Duration
	}{
		{"reconcile finite budget", modeldevv1.ModelDevOperationsService_ReconcileExecution_FullMethodName, 0, 4 * time.Minute, 5 * time.Minute},
		{"other operation unchanged", modeldevv1.ModelDevOperationsService_InspectExecution_FullMethodName, 0, 100 * time.Millisecond, 200 * time.Millisecond},
		{"shorter caller wins", modeldevv1.ModelDevOperationsService_ReconcileExecution_FullMethodName, time.Second, 500 * time.Millisecond, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.caller > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.caller)
				defer cancel()
			}
			ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", uuid.NewString(), "x-ani-actor", "governance:user:3", "x-ani-request-id", uuid.NewString(), "x-ani-authorized-method", tc.method, "x-ani-data-scope", "tenant-all"))
			if tc.method == modeldevv1.ModelDevOperationsService_ReconcileExecution_FullMethodName {
				_, err = client.ReconcileExecution(ctx, &modeldevv1.ReconcileExecutionRequest{})
			} else {
				_, err = client.InspectExecution(ctx, &modeldevv1.InspectExecutionRequest{})
			}
			if err != nil {
				t.Fatal(err)
			}
			remaining := <-handler.remaining
			if remaining < tc.lower || remaining > tc.upper {
				t.Fatalf("METHOD_TIME_BUDGET: remaining=%v want %v..%v", remaining, tc.lower, tc.upper)
			}
		})
	}
}

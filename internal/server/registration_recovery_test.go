package server_test

import (
	"context"
	"crypto/tls"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

type recoveryBoundary struct {
	modeldevv1.UnimplementedModelDevOperationsServiceServer
	calls atomic.Int64
}

func (h *recoveryBoundary) ResumeModelRegistration(ctx context.Context, request *modeldevv1.ResumeModelRegistrationRequest) (*modeldevv1.GetExecutionResponse, error) {
	h.calls.Add(1)
	// The actual service must see the trusted exact-method scope. Its absent
	// dependencies then fail closed without initiating any registration write.
	return service.NewOperations(nil).ResumeModelRegistration(ctx, request)
}

func TestResumeRegistrationUsesExistingMTLSExactMethodDelegation(t *testing.T) {
	certificates := commandtls.New(t)
	handler := &recoveryBoundary{}
	s, err := server.NewGovernanceServicesServer(&conf.Server_GRPC{Network: "tcp", Addr: "127.0.0.1:0", Timeout: durationpb.New(5 * time.Second)}, server.CommandTLS{Certificate: certificates.Server, ClientCAs: certificates.Roots, GovernanceDNSName: commandtls.GovernanceDNSName}, &modeldevv1.UnimplementedModelDevCommandServiceServer{}, nil, nil, server.GovernanceServices{Operations: handler})
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
			t.Error("recovery listener did not stop")
		}
	})
	connection, err := grpc.NewClient(endpoint.Host, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: certificates.Roots, ServerName: commandtls.ServerDNSName, Certificates: []tls.Certificate{certificates.Governance}})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := modeldevv1.NewModelDevOperationsServiceClient(connection)
	for _, test := range []struct {
		name, method, scope string
		code                codes.Code
		calls               int64
	}{
		{"original method", modeldevv1.ModelDevOperationsService_ResumeModelRegistration_FullMethodName, "tenant-all", codes.Unavailable, 1},
		{"other operation", modeldevv1.ModelDevOperationsService_ReconcileExecution_FullMethodName, "tenant-all", codes.PermissionDenied, 0},
		{"missing current delegation", "", "tenant-all", codes.PermissionDenied, 0},
		{"insufficient tenant scope", modeldevv1.ModelDevOperationsService_ResumeModelRegistration_FullMethodName, "self", codes.PermissionDenied, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", uuid.NewString(), "x-ani-actor", "governance:user:42", "x-ani-request-id", uuid.NewString(), "x-ani-authorized-method", test.method, "x-ani-data-scope", test.scope))
			before := handler.calls.Load()
			if _, err := client.ResumeModelRegistration(ctx, &modeldevv1.ResumeModelRegistrationRequest{ExecutionId: uuid.NewString()}); status.Code(err) != test.code || handler.calls.Load()-before != test.calls {
				t.Fatal("registration recovery escaped exact current delegation", status.Code(err))
			}
		})
	}
}

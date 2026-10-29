package server_test

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// This boundary delays the handler only. The actual managed TLS listener and
// method-specific transport deadlines remain under test.
type managedRegistrationDelay struct {
	modeldevv1.UnimplementedModelDevStepServiceServer
	delay time.Duration
}

func (h *managedRegistrationDelay) BeginExecution(context.Context, *modeldevv1.BeginExecutionRequest) (*modeldevv1.BeginExecutionResponse, error) {
	return &modeldevv1.BeginExecutionResponse{}, nil
}

func (h *managedRegistrationDelay) RegisterModel(ctx context.Context, _ *modeldevv1.RegisterModelRequest) (*modeldevv1.RegisterModelResponse, error) {
	if err := h.wait(ctx); err != nil {
		return nil, err
	}
	return &modeldevv1.RegisterModelResponse{}, nil
}

func (h *managedRegistrationDelay) GetTrainingStatus(ctx context.Context, _ *modeldevv1.GetTrainingStatusRequest) (*modeldevv1.GetTrainingStatusResponse, error) {
	if err := h.wait(ctx); err != nil {
		return nil, err
	}
	return &modeldevv1.GetTrainingStatusResponse{}, nil
}

func (h *managedRegistrationDelay) wait(ctx context.Context) error {
	select {
	case <-time.After(h.delay):
		return nil
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	}
}

func managedRegistrationTLSClient(t *testing.T, delay, shortBudget time.Duration) modeldevv1.ModelDevStepServiceClient {
	t.Helper()
	certificates := commandtls.New(t)
	s, err := server.NewManagedStepServer(&conf.Server_GRPC{Network: "tcp", Addr: "127.0.0.1:0", Timeout: durationpb.New(shortBudget)}, certificates.Server, &managedRegistrationDelay{delay: delay})
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
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
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
			t.Error("managed listener did not stop")
		}
	})
	connection, err := grpc.NewClient(endpoint.Host, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: certificates.Roots, ServerName: commandtls.ServerDNSName})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := modeldevv1.NewModelDevStepServiceClient(connection)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.BeginExecution(ctx, &modeldevv1.BeginExecutionRequest{}); err != nil {
		t.Fatal(err)
	}
	return client
}

func TestManagedRegisterTLSPreservesFiniteCallerDeadline(t *testing.T) {
	client := managedRegistrationTLSClient(t, 400*time.Millisecond, 150*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.RegisterModel(ctx, &modeldevv1.RegisterModelRequest{}); err != nil {
		t.Fatalf("REGISTER caller deadline was clipped by the ordinary managed Step timeout: %v", err)
	}
}

func TestManagedRegisterTLSRequiresExistingDeadline(t *testing.T) {
	client := managedRegistrationTLSClient(t, 400*time.Millisecond, 150*time.Millisecond)
	if _, err := client.RegisterModel(context.Background(), &modeldevv1.RegisterModelRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unbounded REGISTER reached the handler: %v", err)
	}
}

func TestManagedRegisterTLSHonorsEarlierCallerTermination(t *testing.T) {
	for _, end := range []string{"deadline", "cancel"} {
		t.Run(end, func(t *testing.T) {
			client := managedRegistrationTLSClient(t, 400*time.Millisecond, 150*time.Millisecond)
			budget := 60 * time.Millisecond
			if end == "cancel" {
				budget = 2 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			want := codes.DeadlineExceeded
			if end == "cancel" {
				want = codes.Canceled
				timer := time.AfterFunc(60*time.Millisecond, cancel)
				defer timer.Stop()
			}
			if _, err := client.RegisterModel(ctx, &modeldevv1.RegisterModelRequest{}); status.Code(err) != want {
				t.Fatalf("caller termination changed: got %v want %v", err, want)
			}
		})
	}
}

func TestManagedRegisterTLSKeepsOtherStepMethodsShort(t *testing.T) {
	client := managedRegistrationTLSClient(t, 400*time.Millisecond, 150*time.Millisecond)
	for _, withDeadline := range []bool{false, true} {
		ctx := context.Background()
		if withDeadline {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
		}
		if _, err := client.GetTrainingStatus(ctx, &modeldevv1.GetTrainingStatusRequest{}); status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("ordinary Step query lost its short timeout: %v", err)
		}
	}
}

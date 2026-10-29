package modelclient

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Delay only the external Model boundary. The real adapter, TLS, identity,
// request projection and original receipt validation remain in the call path.
type registrationDeadlineBoundary struct {
	modelv1.UnimplementedModelServiceServer
	t         *testing.T
	delay     time.Duration
	slowShort atomic.Bool
}

func (h *registrationDeadlineBoundary) wait(ctx context.Context) error {
	h.t.Helper()
	p, ok := peer.FromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing TLS peer")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || tlsInfo.State.Version != tls.VersionTLS13 || len(tlsInfo.State.PeerCertificates) == 0 || !exactClientIdentity(tlsInfo.State.PeerCertificates[0], ClientDNSName) {
		return status.Error(codes.Unauthenticated, "wrong ModelDev identity")
	}
	in, _, _ := artifactFixture()
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md.Get("x-ani-tenant-id")) != 1 || md.Get("x-ani-tenant-id")[0] != in.TenantID || len(md.Get("x-ani-actor")) != 1 || md.Get("x-ani-actor")[0] != "modeldev:service" || len(md.Get("x-ani-request-id")) != 1 || len(md.Get("authorization")) != 0 || len(md.Get("cookie")) != 0 {
		h.t.Error("caller metadata crossed the Model mTLS boundary")
	}
	select {
	case <-time.After(h.delay):
		return nil
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	}
}

func (h *registrationDeadlineBoundary) PrepareTrainedArtifact(ctx context.Context, _ *modelv1.PrepareTrainedArtifactRequest) (*modelv1.PrepareTrainedArtifactResponse, error) {
	if h.slowShort.Load() {
		if err := h.wait(ctx); err != nil {
			return nil, err
		}
	}
	_, s, _ := artifactFixture()
	return &modelv1.PrepareTrainedArtifactResponse{RegistrationId: s.RegistrationID, StoragePath: s.StoragePath}, nil
}

func (h *registrationDeadlineBoundary) GetTrainedArtifactUploadURL(ctx context.Context, _ *modelv1.GetTrainedArtifactUploadURLRequest) (*modelv1.GetTrainedArtifactUploadURLResponse, error) {
	if err := h.wait(ctx); err != nil {
		return nil, err
	}
	_, s, _ := artifactFixture()
	return &modelv1.GetTrainedArtifactUploadURLResponse{RegistrationId: s.RegistrationID, StoragePath: s.StoragePath, UploadUrl: "https://storage.example.test/original", ExpiresAt: timestamppb.New(time.Now().Add(time.Minute))}, nil
}

func (h *registrationDeadlineBoundary) GetModel(ctx context.Context, _ *modelv1.GetModelRequest) (*modelv1.Model, error) {
	if err := h.wait(ctx); err != nil {
		return nil, err
	}
	return &modelv1.Model{}, nil
}

func (h *registrationDeadlineBoundary) GetTrainedArtifactRegistration(ctx context.Context, in *modelv1.GetTrainedArtifactRegistrationRequest) (*modelv1.GetTrainedArtifactRegistrationResponse, error) {
	if err := h.wait(ctx); err != nil {
		return nil, err
	}
	_, _, r := artifactFixture()
	if in.TenantId == "" || in.IdempotencyKey != r.IdempotencyKey || in.ExpectedExecutionId != r.ExecutionId || !in.VerifyUploadedObject {
		return nil, status.Error(codes.InvalidArgument, "original registration changed")
	}
	return &modelv1.GetTrainedArtifactRegistrationResponse{Registration: proto.Clone(r).(*modelv1.TrainedArtifactRegistration), UploadedObject: &modelv1.TrainedArtifactObjectObservation{State: modelv1.TrainedArtifactObjectObservation_STATE_VERIFIED, SizeBytes: r.SizeBytes, Sha256: r.ChecksumSha256}}, nil
}

func (h *registrationDeadlineBoundary) CompleteTrainedArtifact(ctx context.Context, in *modelv1.CompleteTrainedArtifactRequest) (*modelv1.CompleteTrainedArtifactResponse, error) {
	if err := h.wait(ctx); err != nil {
		return nil, err
	}
	_, s, r := artifactFixture()
	if in.TenantId == "" || in.RegistrationId != s.RegistrationID || in.IdempotencyKey != r.IdempotencyKey {
		return nil, status.Error(codes.InvalidArgument, "original completion changed")
	}
	return &modelv1.CompleteTrainedArtifactResponse{RegistrationId: s.RegistrationID, StoragePath: s.StoragePath, ModelId: "55555555-5555-4555-8555-555555555555", ModelVersionId: "66666666-6666-4666-8666-666666666666", ModelVersionStatus: "ready", SizeBytes: r.SizeBytes, ChecksumSha256: r.ChecksumSha256}, nil
}

func registrationTLSClient(t *testing.T, delay, timeout time.Duration) (*Client, *registrationDeadlineBoundary) {
	t.Helper()
	config, certs := modelClientTestSetup(t, nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certs.Server}, ClientCAs: certs.Roots, ClientAuth: tls.RequireAndVerifyClientCert})))
	boundary := &registrationDeadlineBoundary{t: t, delay: delay}
	modelv1.RegisterModelServiceServer(s, boundary)
	go func() { _ = s.Serve(listener) }()
	t.Cleanup(func() { s.Stop(); _ = listener.Close() })
	config.Endpoint, config.Timeout = listener.Addr().String(), timeout
	client, closeClient, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeClient)
	// Warm the actual TLS connection with the unchanged short Prepare method.
	in, _, _ := artifactFixture()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.PrepareArtifact(ctx, in); err != nil {
		t.Fatal(err)
	}
	return client, boundary
}

func TestRegistrationArtifactMTLSUsesFiniteCallerDeadline(t *testing.T) {
	for _, method := range []string{"verify original object", "complete original object"} {
		t.Run(method, func(t *testing.T) {
			client, _ := registrationTLSClient(t, 400*time.Millisecond, 150*time.Millisecond)
			in, session, _ := artifactFixture()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", "foreign", "x-ani-actor", "borrowed", "authorization", "must-not-forward", "cookie", "must-not-forward"))
			if method == "verify original object" {
				got, err := client.ReadArtifact(ctx, in)
				if err != nil || !got.ObjectVerified || got.Session != session {
					t.Fatalf("original REGISTER deadline was clipped by short RPC budget: %#v %v", got, err)
				}
			} else {
				got, err := client.CompleteArtifact(ctx, in, session)
				if err != nil || got.RegistrationID != session.RegistrationID || got.SHA256 != in.Registration.Bundle.SHA256 {
					t.Fatalf("original REGISTER deadline was clipped by short RPC budget: %#v %v", got, err)
				}
			}
		})
	}
}

func TestRegistrationArtifactMTLSRequiresExistingDeadline(t *testing.T) {
	client, _ := registrationTLSClient(t, 400*time.Millisecond, 150*time.Millisecond)
	in, session, _ := artifactFixture()
	if _, err := client.ReadArtifact(context.Background(), in); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("object verification accepted an unbounded caller: %v", err)
	}
	if _, err := client.CompleteArtifact(context.Background(), in, session); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("completion accepted an unbounded caller: %v", err)
	}
}

func TestRegistrationArtifactMTLSHonorsEarlierDeadlineAndCancellation(t *testing.T) {
	for _, method := range []string{"read", "complete"} {
		for _, end := range []string{"deadline", "cancel"} {
			t.Run(method+"/"+end, func(t *testing.T) {
				client, _ := registrationTLSClient(t, 400*time.Millisecond, 150*time.Millisecond)
				in, session, _ := artifactFixture()
				budget := 60 * time.Millisecond
				if end == "cancel" {
					budget = 2 * time.Second
				}
				ctx, cancel := context.WithTimeout(context.Background(), budget)
				defer cancel()
				want := context.DeadlineExceeded
				if end == "cancel" {
					want = context.Canceled
					timer := time.AfterFunc(60*time.Millisecond, cancel)
					defer timer.Stop()
				}
				var err error
				if method == "read" {
					_, err = client.ReadArtifact(ctx, in)
				} else {
					_, err = client.CompleteArtifact(ctx, in, session)
				}
				if !errors.Is(err, want) {
					t.Fatalf("caller termination replaced: got %v want %v", err, want)
				}
			})
		}
	}
}

func TestRegistrationArtifactMTLSKeepsOtherMethodsShort(t *testing.T) {
	for _, method := range []string{"prepare", "upload URL", "ordinary query"} {
		t.Run(method, func(t *testing.T) {
			client, boundary := registrationTLSClient(t, 400*time.Millisecond, 150*time.Millisecond)
			boundary.slowShort.Store(true)
			in, session, _ := artifactFixture()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var err error
			switch method {
			case "prepare":
				_, err = client.PrepareArtifact(ctx, in)
			case "upload URL":
				_, err = client.ArtifactUploadGrant(ctx, in, session)
			case "ordinary query":
				_, err = client.GetModel(ctx, in.TenantID, &modelv1.GetModelRequest{ModelId: in.Registration.BaseModelVersionID})
			}
			if !errors.Is(err, context.DeadlineExceeded) && status.Code(err) != codes.DeadlineExceeded {
				t.Fatalf("short method inherited registration budget: %v", err)
			}
		})
	}
}

package server

import (
	"context"
	"crypto/tls"
	"testing"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type stubQueryStreamServer struct {
	modeldevv1.UnimplementedModelDevQueryServiceServer
}

// peerlessStream is a real grpc.ServerStream with no peer in its context, which
// is exactly what an unauthenticated caller produces.
type peerlessStream struct{ ctx context.Context }

func (s peerlessStream) SetHeader(metadata.MD) error  { return nil }
func (s peerlessStream) SendHeader(metadata.MD) error { return nil }
func (s peerlessStream) SetTrailer(metadata.MD)       {}
func (s peerlessStream) Context() context.Context     { return s.ctx }
func (s peerlessStream) SendMsg(any) error            { return nil }
func (s peerlessStream) RecvMsg(any) error            { return nil }

// The streaming interceptor admits exactly one method. Everything else is
// refused before any TLS or handler work, so a future stream cannot inherit an
// allow-list it was never reviewed for.
func TestStreamInterceptorAdmitsOnlyTheExecutionLogStream(t *testing.T) {
	interceptor := authenticateStream(CommandTLS{}, nil, func(tls.ConnectionState) error { return nil }, &stubQueryStreamServer{})
	for _, method := range []string{
		"/modeldev.service.v1.ModelDevQueryService/OtherStream",
		"/modeldev.service.v1.ModelDevCommandService/ApplyCloseIntent",
		"/modeldev.service.v1.ModelDevQueryService/GetExecutionLogs",
	} {
		err := interceptor(nil, nil, &grpc.StreamServerInfo{FullMethod: method, IsServerStream: true}, nil)
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%s admitted: %v", method, err)
		}
	}
}

// The one admitted method still requires an authenticated Governance peer; the
// method allow-list never substitutes for mTLS.
func TestStreamInterceptorRejectsUnauthenticatedPeer(t *testing.T) {
	interceptor := authenticateStream(CommandTLS{}, nil, func(tls.ConnectionState) error { return nil }, &stubQueryStreamServer{})
	err := interceptor(nil, peerlessStream{ctx: context.Background()}, &grpc.StreamServerInfo{FullMethod: modeldevv1.ModelDevQueryService_GetExecutionLogStream_FullMethodName, IsServerStream: true}, nil)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("peerless stream admitted: %v", err)
	}
}

// With no query service registered, the stream is refused rather than exposing
// an unimplemented handler.
func TestStreamInterceptorRequiresRegisteredQueryService(t *testing.T) {
	interceptor := authenticateStream(CommandTLS{}, nil, func(tls.ConnectionState) error { return nil }, nil)
	err := interceptor(nil, nil, &grpc.StreamServerInfo{FullMethod: modeldevv1.ModelDevQueryService_GetExecutionLogStream_FullMethodName, IsServerStream: true}, nil)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unregistered query service admitted a stream: %v", err)
	}
}

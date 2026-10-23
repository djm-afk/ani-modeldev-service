package service

import (
	"context"
	"io"
	"strings"
	"testing"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// fakeStreamServer records what the query service emits. It is a transport
// double only: the frame protocol, offsets and reasons are the real code.
type fakeStreamServer struct {
	ctx    context.Context
	frames []*modeldevv1.GetExecutionLogStreamResponse
}

func (s *fakeStreamServer) Send(frame *modeldevv1.GetExecutionLogStreamResponse) error {
	s.frames = append(s.frames, frame)
	return nil
}
func (s *fakeStreamServer) SetHeader(metadata.MD) error  { return nil }
func (s *fakeStreamServer) SendHeader(metadata.MD) error { return nil }
func (s *fakeStreamServer) SetTrailer(metadata.MD)       {}
func (s *fakeStreamServer) Context() context.Context     { return s.ctx }
func (s *fakeStreamServer) SendMsg(any) error            { return nil }
func (s *fakeStreamServer) RecvMsg(any) error            { return nil }

type streamFixture struct {
	data []byte
	err  error
	got  int64
	read bool
}

func (f *streamFixture) StreamContainerLogs(_ context.Context, _ biz.ContainerLogTarget, since int64) (io.ReadCloser, error) {
	f.read, f.got = true, since
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(string(f.data))), nil
}

func streamQuery(t *testing.T, fixture *streamFixture) (*Query, biz.QueryRecord, context.Context) {
	t.Helper()
	record, _, ctx := recordedLogQueryFixture(t)
	query := NewQuery(recordedLogQueryRepository{record}, nil)
	query.WithLogStream(nil, fixture)
	return query, record, ctx
}

func streamContextFor(ctx context.Context) context.Context {
	return WithVerifiedGovernanceQuery(ctx, GovernanceDelivery{TenantID: workloadTenantForTest, RequestID: "request-stream"},
		modeldevv1.ModelDevQueryService_GetExecutionLogStream_FullMethodName)
}

const workloadTenantForTest = "9086ce3b-bd19-47c3-8467-190607f633db"

// The first frame is always READY, every DATA frame advances a server-owned
// offset, and a log end is a CLOSED control frame rather than a silent stop.
func TestExecutionLogStreamFramesReadyDataThenClosed(t *testing.T) {
	fixture := &streamFixture{data: []byte("line one\nline two\n")}
	query, record, ctx := streamQuery(t, fixture)
	server := &fakeStreamServer{ctx: streamContextFor(ctx)}
	err := query.GetExecutionLogStream(&modeldevv1.GetExecutionLogStreamRequest{ExecutionId: record.Execution.ExecutionID}, server)
	if err != nil {
		t.Fatal(err)
	}
	if len(server.frames) != 3 {
		t.Fatalf("frames=%d want READY+2", len(server.frames))
	}
	if server.frames[0].Kind != modeldevv1.FrameKind_FRAME_KIND_READY || server.frames[0].Offset != 0 {
		t.Fatalf("first frame must be READY at offset 0: %+v", server.frames[0])
	}
	if server.frames[1].Kind != modeldevv1.FrameKind_FRAME_KIND_DATA || string(server.frames[1].Data) != "line one\nline two\n" || server.frames[1].Offset != 18 {
		t.Fatalf("data frame: %+v", server.frames[1])
	}
	closed := server.frames[2]
	if closed.Kind != modeldevv1.FrameKind_FRAME_KIND_CLOSED || closed.Reason != "LOG_ENDED" || closed.Offset != 18 {
		t.Fatalf("close frame: %+v", closed)
	}
	if !fixture.read || fixture.got != 0 {
		t.Fatalf("stream opened with since=%d", fixture.got)
	}
}

// A reconnect offset is forwarded as-is and reported back in READY.
func TestExecutionLogStreamForwardsResumeOffset(t *testing.T) {
	fixture := &streamFixture{data: []byte("tail\n")}
	query, record, ctx := streamQuery(t, fixture)
	server := &fakeStreamServer{ctx: streamContextFor(ctx)}
	since := int64(120)
	err := query.GetExecutionLogStream(&modeldevv1.GetExecutionLogStreamRequest{ExecutionId: record.Execution.ExecutionID, SinceBytes: &since}, server)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.got != since {
		t.Fatalf("offset %d not forwarded", fixture.got)
	}
	if server.frames[0].Kind != modeldevv1.FrameKind_FRAME_KIND_READY || server.frames[0].Offset != since {
		t.Fatalf("READY must echo the resume offset: %+v", server.frames[0])
	}
	if server.frames[1].Offset != since+5 {
		t.Fatalf("DATA offset must accumulate from since_bytes: %+v", server.frames[1])
	}
}

// A stale offset, a missing previous instance and a vanished Pod each close with
// their own REJECTED reason so the client knows which recovery to offer.
func TestExecutionLogStreamRejectsWithDistinctReasons(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		reason string
	}{
		{"stale offset", biz.ErrLogOffsetInvalid, "OFFSET_INVALID"},
		{"previous unavailable", biz.ErrLogPreviousUnavailable, "PREVIOUS_UNAVAILABLE"},
		{"pod gone", biz.ErrTrainingLogNotFound, "POD_GONE"},
		{"log unavailable", biz.ErrTrainingLogsUnavailable, "LOG_UNAVAILABLE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := &streamFixture{err: test.err}
			query, record, ctx := streamQuery(t, fixture)
			server := &fakeStreamServer{ctx: streamContextFor(ctx)}
			if err := query.GetExecutionLogStream(&modeldevv1.GetExecutionLogStreamRequest{ExecutionId: record.Execution.ExecutionID}, server); err != nil {
				t.Fatal(err)
			}
			if len(server.frames) != 1 || server.frames[0].Kind != modeldevv1.FrameKind_FRAME_KIND_REJECTED || server.frames[0].Reason != test.reason {
				t.Fatalf("frames=%+v want REJECTED(%s)", server.frames, test.reason)
			}
		})
	}
}

// The stream is reachable only with the exact per-method query verification; a
// request authorized for another method cannot open it.
func TestExecutionLogStreamRequiresItsOwnAuthorization(t *testing.T) {
	fixture := &streamFixture{data: []byte("line\n")}
	query, record, ctx := streamQuery(t, fixture)
	server := &fakeStreamServer{ctx: WithVerifiedGovernanceQuery(ctx, GovernanceDelivery{TenantID: workloadTenantForTest},
		modeldevv1.ModelDevQueryService_GetExecutionLogs_FullMethodName)}
	err := query.GetExecutionLogStream(&modeldevv1.GetExecutionLogStreamRequest{ExecutionId: record.Execution.ExecutionID}, server)
	if status.Code(err) != codes.Unauthenticated || len(server.frames) != 0 {
		t.Fatalf("cross-method stream authorization: %v frames=%d", err, len(server.frames))
	}
	if err := query.GetExecutionLogStream(&modeldevv1.GetExecutionLogStreamRequest{ExecutionId: record.Execution.ExecutionID}, &fakeStreamServer{ctx: context.Background()}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated stream accepted: %v", err)
	}
}

// A follower that fails after READY closes the stream with a control frame
// instead of returning a transport error.
func TestExecutionLogStreamFollowerFailureCloses(t *testing.T) {
	query, record, ctx := streamQuery(t, &streamFixture{})
	query.streams = &failingStreamer{}
	server := &fakeStreamServer{ctx: streamContextFor(ctx)}
	if err := query.GetExecutionLogStream(&modeldevv1.GetExecutionLogStreamRequest{ExecutionId: record.Execution.ExecutionID}, server); err != nil {
		t.Fatal(err)
	}
	last := server.frames[len(server.frames)-1]
	if server.frames[0].Kind != modeldevv1.FrameKind_FRAME_KIND_READY || last.Kind != modeldevv1.FrameKind_FRAME_KIND_CLOSED || last.Reason != "POD_GONE" {
		t.Fatalf("unexpected frames: %+v", server.frames)
	}
}

type failingStreamer struct{}

func (failingStreamer) StreamContainerLogs(context.Context, biz.ContainerLogTarget, int64) (io.ReadCloser, error) {
	return io.NopCloser(&failingReader{}), nil
}

// failingReader emits one bounded chunk and then reports a broken follower.
type failingReader struct{ read bool }

func (r *failingReader) Read(buffer []byte) (int, error) {
	if !r.read {
		r.read = true
		return copy(buffer, "partial line\n"), nil
	}
	return 0, io.ErrUnexpectedEOF
}

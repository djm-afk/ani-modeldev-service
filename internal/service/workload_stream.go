package service

import (
	"bufio"
	"context"
	"errors"
	"io"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// streamReadyLimit bounds how long a stream may wait before it reports
	// readiness; a slow API is a rejection, not a silent open connection.
	streamFrameBytes = 16 * 1024
	// streamMaxLifetime bounds one server-stream lifetime. The client resumes
	// with since_bytes, so a long training run never depends on one connection.
	streamMaxLifetime = 10 * time.Minute
)

// GetExecutionLogStream follows one already-authorized container instance. The
// first frame is always a control frame; every DATA frame carries the
// server-owned byte offset a reconnect must send back as since_bytes.
func (query *Query) GetExecutionLogStream(request *modeldevv1.GetExecutionLogStreamRequest, stream modeldevv1.ModelDevQueryService_GetExecutionLogStreamServer) error {
	ctx := stream.Context()
	scope, err := queryScope(ctx, modeldevv1.ModelDevQueryService_GetExecutionLogStream_FullMethodName)
	if err != nil {
		return err
	}
	if request == nil || !queryID(request.ExecutionId) || len(request.ProtoReflect().GetUnknown()) != 0 ||
		request.GetSinceBytes() < 0 || request.GetSinceBytes() > 1<<30 {
		return status.Error(codes.InvalidArgument, "invalid log stream query")
	}
	if query == nil || query.repository == nil || query.streams == nil {
		return unavailableStream(stream, scope.RequestID)
	}
	record, err := query.repository.GetQueryRecord(ctx, scope.TenantID, request.ExecutionId)
	if err != nil {
		return queryError(err, scope.RequestID)
	}
	selection, err := query.selectLogTarget(ctx, record, &modeldevv1.GetExecutionLogsRequest{
		ExecutionId: request.ExecutionId, PodUid: request.PodUid, Container: request.Container, Previous: request.Previous,
	})
	if err != nil {
		return rejectedStream(stream, rejectReason(err))
	}
	if selection.Kind != biz.LogSourceKindTrainer && selection.Kind != biz.LogSourceKindKFPStep {
		return rejectedStream(stream, "LOG_UNAVAILABLE")
	}
	lifetime, cancel := context.WithTimeout(ctx, streamMaxLifetime)
	defer cancel()
	follower, err := query.streams.StreamContainerLogs(lifetime, selection.Target, request.GetSinceBytes())
	if err != nil {
		return rejectedStream(stream, rejectReason(err))
	}
	defer follower.Close()
	if err := stream.Send(&modeldevv1.GetExecutionLogStreamResponse{Kind: modeldevv1.FrameKind_FRAME_KIND_READY, Offset: request.GetSinceBytes()}); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(follower, streamFrameBytes)
	var offset = request.GetSinceBytes()
	for {
		chunk := make([]byte, streamFrameBytes)
		read, readErr := reader.Read(chunk)
		if read > 0 {
			offset += int64(read)
			if err := stream.Send(&modeldevv1.GetExecutionLogStreamResponse{Kind: modeldevv1.FrameKind_FRAME_KIND_DATA, Data: chunk[:read], Offset: offset}); err != nil {
				return err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				// The container stopped writing and its log ended: this is a
				// normal close, not a failure of the query.
				return stream.Send(&modeldevv1.GetExecutionLogStreamResponse{Kind: modeldevv1.FrameKind_FRAME_KIND_CLOSED, Reason: "LOG_ENDED", Offset: offset})
			}
			if lifetime.Err() != nil {
				return stream.Send(&modeldevv1.GetExecutionLogStreamResponse{Kind: modeldevv1.FrameKind_FRAME_KIND_CLOSED, Reason: "LIFETIME_EXCEEDED", Offset: offset})
			}
			// A Pod disappearing under a follower is a close, never a silent stop.
			return stream.Send(&modeldevv1.GetExecutionLogStreamResponse{Kind: modeldevv1.FrameKind_FRAME_KIND_CLOSED, Reason: "POD_GONE", Offset: offset})
		}
	}
}

// rejectReason maps a read failure to the control-frame reason the client acts
// on. It is the only place those literals exist.
func rejectReason(err error) string {
	switch {
	case errors.Is(err, biz.ErrLogOffsetInvalid):
		return "OFFSET_INVALID"
	case errors.Is(err, biz.ErrLogPreviousUnavailable):
		return "PREVIOUS_UNAVAILABLE"
	case errors.Is(err, biz.ErrTrainingLogNotFound):
		return "POD_GONE"
	case errors.Is(err, biz.ErrStepLogsUnavailable):
		return "STEP_LOGS_UNAVAILABLE"
	case errors.Is(err, biz.ErrWorkloadViewUnavailable):
		return "POD_GONE"
	}
	return "LOG_UNAVAILABLE"
}

func rejectedStream(stream modeldevv1.ModelDevQueryService_GetExecutionLogStreamServer, reason string) error {
	return stream.Send(&modeldevv1.GetExecutionLogStreamResponse{Kind: modeldevv1.FrameKind_FRAME_KIND_REJECTED, Reason: reason})
}

func unavailableStream(stream modeldevv1.ModelDevQueryService_GetExecutionLogStreamServer, requestID string) error {
	return rejectedStream(stream, "LOG_UNAVAILABLE")
}

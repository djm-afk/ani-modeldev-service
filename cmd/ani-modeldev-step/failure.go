package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/component"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/workspace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type componentFailure struct {
	stage string
	cause error
}

func (*componentFailure) Error() string         { return "managed component failed" }
func (failure *componentFailure) Unwrap() error { return failure.cause }

func stageFailure(stage string, cause error) error {
	if cause == nil {
		return nil
	}
	switch stage {
	case "bootstrap", "grpc-ca", "storage-client", "runner", "run":
	default:
		stage = "unknown"
	}
	return &componentFailure{stage: stage, cause: cause}
}

// This line is a bounded diagnostic vocabulary, never a rendering of an error
// body. Remote responses may contain credentials, signed URLs or configuration.
func failureDiagnostic(err error) string {
	stage, class, code := "unknown", "local", codes.Unknown
	var failure *componentFailure
	if errors.As(err, &failure) {
		stage = failure.stage
	}
	switch {
	case errors.Is(err, component.ErrConfiguration):
		class = "configuration"
	case errors.Is(err, context.Canceled):
		class = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		class = "deadline"
	default:
		if remote, ok := status.FromError(err); ok {
			class, code = "grpc", remote.Code()
			if code > codes.Unauthenticated {
				code = codes.Unknown
			}
		} else {
			var publication *workspace.PublicationFailure
			if errors.As(err, &publication) {
				phase, reason, httpStatus, storageCode := publication.PublicationDiagnostic()
				return fmt.Sprintf("managed component failed stage=%s class=publication code=Unknown phase=%s reason=%s http_status=%d storage_code=%s", stage, phase, reason, httpStatus, storageCode)
			}
		}
	}
	return fmt.Sprintf("managed component failed stage=%s class=%s code=%s", stage, class, code)
}

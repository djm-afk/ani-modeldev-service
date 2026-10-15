package workspace

import (
	"context"
	"errors"

	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// PublicationFailure retains only bounded diagnostic facts and a known error
// identity. In particular it never retains an SDK error or remote response body.
type PublicationFailure struct {
	phase, reason, storageCode string
	httpStatus                 int
	cause                      error
}

func (*PublicationFailure) Error() string { return "workspace publication failed" }
func (failure *PublicationFailure) Unwrap() error {
	return failure.cause
}

func (failure *PublicationFailure) PublicationDiagnostic() (phase, reason string, httpStatus int, storageCode string) {
	phase, reason, storageCode = "unknown", "unknown", "none"
	if failure == nil {
		return
	}
	switch failure.phase {
	case "validate", "open", "spool", "capture", "bundle", "inventory", "put", "put-manifest", "put-bundle":
		phase = failure.phase
	}
	switch failure.reason {
	case "client", "context", "manifest", "scope", "directory", "filesystem", "budget", "content", "archive", "seek", "storage", "version", "head-existing", "verify-existing":
		reason = failure.reason
	}
	if failure.httpStatus >= 100 && failure.httpStatus <= 599 {
		httpStatus = failure.httpStatus
	}
	storageCode = publicationStorageCode(failure.storageCode)
	return
}

func publicationFailure(phase, reason string, cause error) *PublicationFailure {
	// Keep only identities already exposed by the publisher. Path errors and
	// transport errors may contain private paths, signed URLs or credentials.
	safeCause := biz.ErrInvalidWorkspaceOutput
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, biz.ErrInvalidWorkspaceOutput, biz.ErrObjectSourceUnavailable, biz.ErrObjectVerification} {
		if errors.Is(cause, known) {
			safeCause = known
			break
		}
	}
	return &PublicationFailure{phase: phase, reason: reason, cause: safeCause, storageCode: "none"}
}

func publicationStorageFailure(ctx context.Context, phase, reason string, sdkError error) error {
	failure := publicationFailure(phase, reason, pipelineIOError(ctx, biz.ErrObjectSourceUnavailable))
	var response *smithyhttp.ResponseError
	if errors.As(sdkError, &response) {
		failure.httpStatus = response.HTTPStatusCode()
	}
	var api smithy.APIError
	if errors.As(sdkError, &api) {
		failure.storageCode = publicationStorageCode(api.ErrorCode())
	}
	return failure
}

func publicationStorageCode(code string) string {
	switch code {
	case "none", "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch", "ExpiredToken", "PreconditionFailed", "NoSuchBucket", "NoSuchKey", "RequestTimeout", "SlowDown", "InternalError", "ServiceUnavailable":
		return code
	default:
		return "unknown"
	}
}

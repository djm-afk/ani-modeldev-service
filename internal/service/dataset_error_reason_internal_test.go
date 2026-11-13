package service

import (
	"fmt"
	"testing"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A reason must carry one gRPC code across the whole service. The Gov BFF
// validates (reason, code) pairs against a whitelist and degrades any mismatch
// to a generic 503, so two paths reporting the same reason with different codes
// would make one of them unreachable.
func TestContentLimitReasonCarriesOneCodeOnEveryPath(t *testing.T) {
	// The import path reports the reason through datasetError.
	imported := status.Convert(datasetError(fmt.Errorf("accept: %w", biz.ErrSourceTooLarge), "request-123"))
	// The material path reports it through materialsError.
	material := status.Convert(materialsError(fmt.Errorf("validation: %w", &cpup01.LimitViolation{
		Reason: "CONTENT_LIMIT_EXCEEDED", Kind: "dataset", LimitName: "max_content_bytes", Actual: 9, Limit: 8,
	}), "request-123"))

	if imported.Code() != material.Code() {
		t.Fatalf("the same reason carries %v on the import path and %v on the material path", imported.Code(), material.Code())
	}
	if imported.Code() != codes.FailedPrecondition {
		t.Fatalf("code = %v, want FailedPrecondition so the public answer is 422", imported.Code())
	}
	for name, failure := range map[string]*status.Status{"import": imported, "material": material} {
		details := failure.Details()
		if len(details) != 1 {
			t.Fatalf("%s: details = %+v", name, details)
		}
		detail, ok := details[0].(*modeldevv1.ErrorDetail)
		if !ok || detail.Reason != modeldevv1.ErrorReason_ERROR_REASON_CONTENT_LIMIT_EXCEEDED {
			t.Fatalf("%s: reason = %+v", name, detail)
		}
	}
}

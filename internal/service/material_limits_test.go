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

func TestMaterialLimitsAndBusyPreserveTypedPublicFailure(t *testing.T) {
	for _, row := range []struct {
		err    error
		code   codes.Code
		reason modeldevv1.ErrorReason
		limit  *cpup01.LimitViolation
	}{
		{err: fmt.Errorf("content: %w", biz.ErrInputVerification), code: codes.FailedPrecondition, reason: modeldevv1.ErrorReason_ERROR_REASON_DATASET_INVALID},
		{err: fmt.Errorf("source: %w", biz.ErrInputSourceUnavailable), code: codes.Unavailable, reason: modeldevv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE},
		{err: fmt.Errorf("owner: %w", biz.ErrInputValidationBusy), code: codes.ResourceExhausted, reason: modeldevv1.ErrorReason_ERROR_REASON_INPUT_VALIDATION_BUSY},
		{code: codes.FailedPrecondition, reason: modeldevv1.ErrorReason_ERROR_REASON_CONTENT_LIMIT_EXCEEDED, limit: &cpup01.LimitViolation{Reason: "CONTENT_LIMIT_EXCEEDED", Kind: "dataset", LimitName: "max_content_bytes", Actual: 9007199254740993, Limit: 9007199254740992}},
		{code: codes.FailedPrecondition, reason: modeldevv1.ErrorReason_ERROR_REASON_STRUCTURE_LIMIT_EXCEEDED, limit: &cpup01.LimitViolation{Reason: "STRUCTURE_LIMIT_EXCEEDED", Kind: "base_model", LimitName: "MaxModelFiles", Actual: 1025, Limit: 1024}},
		{code: codes.FailedPrecondition, reason: modeldevv1.ErrorReason_ERROR_REASON_DATASET_RECORD_TOO_LARGE, limit: &cpup01.LimitViolation{Reason: "DATASET_RECORD_TOO_LARGE", Kind: "dataset", LimitName: "MaxRecordBytes", Actual: 33554433, Limit: 33554432}},
	} {
		if row.limit != nil {
			row.err = fmt.Errorf("validation: %w", row.limit)
		}
		actual := status.Convert(materialsError(row.err, "request-123"))
		if actual.Code() != row.code || len(actual.Details()) != 1 {
			t.Fatalf("public failure lost typed code/detail: %v %+v", actual.Code(), actual.Details())
		}
		detail, ok := actual.Details()[0].(*modeldevv1.ErrorDetail)
		if !ok || detail.Reason != row.reason || detail.CorrelationId != "request-123" {
			t.Fatalf("public reason missing: %+v", detail)
		}
		if row.limit != nil && (detail.Kind != row.limit.Kind || detail.LimitName != row.limit.LimitName || detail.Actual == nil || detail.Limit == nil || *detail.Actual != row.limit.Actual || *detail.Limit != row.limit.Limit) {
			t.Fatalf("limit metadata changed: %+v", detail)
		}
	}
}

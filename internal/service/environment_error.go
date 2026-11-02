package service

import (
	"errors"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func admissionEnvironmentError(err error, correlation string) (error, bool) {
	var failure *biz.EnvironmentFailure
	reason := ""
	if errors.As(err, &failure) && failure != nil && biz.ValidEnvironmentReason(failure.Reason) {
		reason = failure.Reason
	}
	if errors.Is(err, biz.ErrTenantEnvironmentNotRegistered) {
		reason = "ENVIRONMENT_NOT_REGISTERED"
	}
	if reason == "" {
		return nil, false
	}
	result := status.New(codes.Unavailable, "managed environment is not ready")
	detailed, e := result.WithDetails(&modeldevv1.ErrorDetail{Reason: modeldevv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_READY, SafeMessage: "managed environment is not ready", CorrelationId: correlation, Kind: "tenant_environment", Violations: []*modeldevv1.FieldViolation{{Field: "environment", Reason: reason}}})
	if e != nil {
		return result.Err(), true
	}
	return detailed.Err(), true
}

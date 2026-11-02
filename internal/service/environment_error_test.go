package service

import (
	"errors"
	"fmt"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"testing"
)

func TestAdmissionEnvironmentErrorPreservesOnlyFiniteCause(t *testing.T) {
	for _, reason := range []string{"ENVIRONMENT_NOT_REGISTERED", "ENVIRONMENT_PROVISIONING", "COMPUTE_CLUSTER_MISMATCH", "TENANT_NAMESPACE_UID_CHANGED", "ENVIRONMENT_CONFIG_DRIFT", "ENVIRONMENT_DEPENDENCY_UNAVAILABLE"} {
		err, handled := admissionEnvironmentError(fmt.Errorf("private-sentinel: %w", biz.EnvironmentError(reason)), "request-123")
		failure := status.Convert(err)
		if !handled || failure.Code() != codes.Unavailable || len(failure.Details()) != 1 || strings.Contains(err.Error(), "private-sentinel") {
			t.Fatalf("cause %s: %v %v", reason, handled, err)
		}
		detail, ok := failure.Details()[0].(*modeldevv1.ErrorDetail)
		if !ok || detail.Reason != modeldevv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_READY || detail.Kind != "tenant_environment" || detail.CorrelationId != "request-123" || len(detail.Violations) != 1 || detail.Violations[0].Field != "environment" || detail.Violations[0].Reason != reason || detail.LimitName != "" || detail.Actual != nil || detail.Limit != nil {
			t.Fatalf("cause %s leaked or changed: %+v", reason, detail)
		}
	}
	for _, err := range []error{errors.New("private-sentinel"), biz.EnvironmentError("UNKNOWN_ENVIRONMENT_REASON")} {
		if got, handled := admissionEnvironmentError(err, "request-123"); handled || got != nil {
			t.Fatal("arbitrary cause entered typed environment contract", got)
		}
	}
}

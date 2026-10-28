package modelclient

import (
	"context"
	"errors"
	"strconv"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// ResolveFinetuningFacts is a single read under the independent model-resolution
// budget. It inherits cancellation and never changes a registration outcome.
func (c *Client) ResolveFinetuningFacts(ctx context.Context, tenant string, request *modelv1.ResolveFinetuningFactsRequest) (*modelv1.ResolveFinetuningFactsResponse, error) {
	if c == nil || c.client == nil || request == nil {
		return nil, biz.ErrModelDependencyUnavailable
	}
	in := proto.Clone(request).(*modelv1.ResolveFinetuningFactsRequest)
	in.TenantId = tenant
	call, cancel, err := c.callContextBudget(ctx, tenant, c.resolutionTimeout)
	if err != nil {
		return nil, sourceRPCError(err)
	}
	defer cancel()
	out, err := c.client.ResolveFinetuningFacts(call, in)
	if err != nil {
		return nil, sourceRPCError(err)
	}
	if out.ValidateFor(in) != nil {
		return nil, biz.ErrModelDependencyUnavailable
	}
	return out, nil
}
func sourceRPCError(err error) error {
	if errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded {
		return context.DeadlineExceeded
	}
	// The owner's finite eligibility reason identifies a non-READY source.
	upstream := kratoserrors.FromError(err)
	if upstream.Reason == "MODEL_NOT_READY" {
		return biz.ErrBaseModelInvalid
	}
	switch status.Code(err) {
	case codes.AlreadyExists:
		return errors.Join(biz.ErrModelTargetConflict, status.Error(codes.AlreadyExists, "MODEL_NAME_CONFLICT"))
	case codes.InvalidArgument:
		return biz.ErrInvalidAdmission
	case codes.NotFound:
		return errors.Join(biz.ErrModelSourceNotFound, status.Error(codes.NotFound, "MODEL_SOURCE_NOT_FOUND"))
	case codes.PermissionDenied, codes.Unauthenticated:
		return errors.Join(biz.ErrModelSourceForbidden, status.Error(status.Code(err), "MODEL_SOURCE_FORBIDDEN"))
	}
	switch upstream.Reason {
	case "MODEL_NOT_READY", "BASE_MODEL_INVALID":
		return biz.ErrBaseModelInvalid
	case "BASE_MODEL_UNSUPPORTED":
		return biz.ErrBaseModelUnsupported
	case "CONTEXT_LIMIT_UNKNOWN":
		return biz.ErrContextLimitUnknown
	case "CONTENT_LIMIT_EXCEEDED", "STRUCTURE_LIMIT_EXCEEDED":
		actual, aerr := strconv.ParseInt(upstream.Metadata["actual"], 10, 64)
		limit, lerr := strconv.ParseInt(upstream.Metadata["limit"], 10, 64)
		if aerr == nil && lerr == nil && actual > limit && limit > 0 {
			return &cpup01.LimitViolation{Reason: upstream.Reason, Kind: upstream.Metadata["kind"], LimitName: upstream.Metadata["limit_name"], Actual: actual, Limit: limit}
		}
	}
	return biz.ErrModelDependencyUnavailable
}

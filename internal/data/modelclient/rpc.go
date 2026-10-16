package modelclient

import (
	"context"

	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type requestMessage interface {
	proto.Message
}

type responseMessage interface {
	proto.Message
}

// invoke is a single unary call. It always replaces the caller's metadata and
// applies the configured per-RPC deadline.
func invoke[Req requestMessage, Res responseMessage](c *Client, ctx context.Context, tenant string, request Req, call func(context.Context, Req, ...grpc.CallOption) (Res, error)) (Res, error) {
	var zero Res
	if any(request) == nil || !request.ProtoReflect().IsValid() {
		return zero, status.Error(codes.InvalidArgument, "MODEL_REQUEST_REQUIRED")
	}
	cloned := proto.Clone(request).(Req)
	callCtx, cancel, err := c.callContext(ctx, tenant)
	if err != nil {
		return zero, err
	}
	defer cancel()
	return call(callCtx, cloned)
}

// GetModel resolves one target container. A UUID model_id selects the internal
// primary key; any other value is treated as the external identifier by Model.
func (c *Client) GetModel(ctx context.Context, tenant string, request *modelv1.GetModelRequest) (*modelv1.Model, error) {
	return invoke(c, ctx, tenant, request, func(ctx context.Context, in *modelv1.GetModelRequest, opts ...grpc.CallOption) (*modelv1.Model, error) {
		in.TenantId = tenant
		return c.client.GetModel(ctx, in, opts...)
	})
}

// GetModelVersion reads one exact approved version. It answers READY or miss
// only; callers must not read its absence as proof that no write happened.
func (c *Client) GetModelVersion(ctx context.Context, tenant string, request *modelv1.GetModelVersionRequest) (*modelv1.GetModelVersionResponse, error) {
	return invoke(c, ctx, tenant, request, func(ctx context.Context, in *modelv1.GetModelVersionRequest, opts ...grpc.CallOption) (*modelv1.GetModelVersionResponse, error) {
		in.TenantId = tenant
		return c.client.GetModelVersion(ctx, in, opts...)
	})
}

// ListModelVersions lists a container's versions, including pending ones. It is
// the only read that can distinguish "not created" from "in progress".
func (c *Client) ListModelVersions(ctx context.Context, tenant string, request *modelv1.ListModelVersionsRequest) (*modelv1.ListModelVersionsResponse, error) {
	return invoke(c, ctx, tenant, request, func(ctx context.Context, in *modelv1.ListModelVersionsRequest, opts ...grpc.CallOption) (*modelv1.ListModelVersionsResponse, error) {
		in.TenantId = tenant
		return c.client.ListModelVersions(ctx, in, opts...)
	})
}

// CreateModel creates the new-branch container. It is a write: callers must
// persist the dispatch permit before invoking it and must replay under the
// original idempotency key, never by name.
func (c *Client) CreateModel(ctx context.Context, tenant string, request *modelv1.CreateModelRequest) (*modelv1.Model, error) {
	return invoke(c, ctx, tenant, request, func(ctx context.Context, in *modelv1.CreateModelRequest, opts ...grpc.CallOption) (*modelv1.Model, error) {
		in.TenantId = tenant
		return c.client.CreateModel(ctx, in, opts...)
	})
}

// CreateModelVersion registers the output version. It is a write with the same
// permit and original-key requirements as CreateModel.
func (c *Client) CreateModelVersion(ctx context.Context, tenant string, request *modelv1.CreateModelVersionRequest) (*modelv1.ModelVersion, error) {
	return invoke(c, ctx, tenant, request, func(ctx context.Context, in *modelv1.CreateModelVersionRequest, opts ...grpc.CallOption) (*modelv1.ModelVersion, error) {
		in.TenantId = tenant
		return c.client.CreateModelVersion(ctx, in, opts...)
	})
}

// GetUploadURL issues the controlled-copy ticket. The ticket transports bytes
// only and must not create another business version.
func (c *Client) GetUploadURL(ctx context.Context, tenant string, request *modelv1.GetUploadURLRequest) (*modelv1.GetUploadURLResponse, error) {
	return invoke(c, ctx, tenant, request, func(ctx context.Context, in *modelv1.GetUploadURLRequest, opts ...grpc.CallOption) (*modelv1.GetUploadURLResponse, error) {
		in.TenantId = tenant
		return c.client.GetUploadURL(ctx, in, opts...)
	})
}

// GetModelDownloadURL returns a short-lived download for an already-READY
// version. The caller must not downgrade a versioned object to latest.
func (c *Client) GetModelDownloadURL(ctx context.Context, tenant string, request *modelv1.GetModelDownloadURLRequest) (*modelv1.GetModelDownloadURLResponse, error) {
	return invoke(c, ctx, tenant, request, func(ctx context.Context, in *modelv1.GetModelDownloadURLRequest, opts ...grpc.CallOption) (*modelv1.GetModelDownloadURLResponse, error) {
		in.TenantId = tenant
		return c.client.GetModelDownloadURL(ctx, in, opts...)
	})
}

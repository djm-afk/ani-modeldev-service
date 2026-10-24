package modelclient

import (
	"context"

	modelv1 "github.com/zhangzhe-ctrl/ani-model-service/api/model/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/protobuf/proto"
)

// ResolveFinetuningFacts is one read against the formal Model owner. The
// original caller deadline still bounds the configured finite RPC budget.
func (c *Client) ResolveFinetuningFacts(ctx context.Context, tenant string, request *modelv1.ResolveFinetuningFactsRequest) (*modelv1.ResolveFinetuningFactsResponse, error) {
	if c == nil || c.client == nil || request == nil {
		return nil, biz.ErrModelDependencyUnavailable
	}
	in := proto.Clone(request).(*modelv1.ResolveFinetuningFactsRequest)
	in.TenantId = tenant
	out, err := invoke(c, ctx, tenant, in, c.client.ResolveFinetuningFacts)
	if err != nil {
		return nil, artifactRPCError(err)
	}
	if out.ValidateFor(in) != nil {
		return nil, biz.ErrModelDependencyUnavailable
	}
	return out, nil
}

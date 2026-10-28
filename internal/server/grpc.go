package server

import (
	"github.com/go-kratos/kratos/v3/middleware"
	kratosgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"google.golang.org/grpc"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
)

func NewGRPCServer(c *conf.Server_GRPC, middlewares ...middleware.Middleware) *kratosgrpc.Server {
	return kratosgrpc.NewServer(
		kratosgrpc.Options(grpc.MaxRecvMsgSize(cpup01.MaxControlRPCBytes), grpc.MaxSendMsgSize(cpup01.MaxControlRPCBytes)),
		kratosgrpc.Network(c.Network),
		kratosgrpc.Address(c.Addr),
		kratosgrpc.Timeout(c.Timeout.AsDuration()),
		kratosgrpc.Middleware(middlewares...),
		kratosgrpc.DisableReflection(),
	)
}

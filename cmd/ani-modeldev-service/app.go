package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	kratos "github.com/go-kratos/kratos/v3"
	kratosgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	kratoshttp "github.com/go-kratos/kratos/v3/transport/http"

	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server"
)

// application owns dependencies outside the transport lifecycle. Kratos may
// return before AfterStop when listener binding fails, so Run also releases
// them. Both paths share one cleanup and preserve any shutdown failure.
type application struct {
	*kratos.App
	release func() error
}

func (app *application) Run() (err error) {
	defer func() { err = errors.Join(err, app.release()) }()
	return app.App.Run()
}

func buildApp(bc *conf.Bootstrap, logger *slog.Logger) (*application, error) {
	if err := bc.Validate(); err != nil {
		return nil, err
	}
	if bc.Runtime != nil {
		return buildRuntimeApp(bc, logger)
	}
	// The shell and command-only configuration lack the managed runtime.
	// Durable command delivery alone cannot grant runtime readiness.
	readiness := server.NewReadiness()
	observability, err := server.NewObservability(Name, Version, readiness)
	if err != nil {
		return nil, err
	}
	middlewares := observability.ServerMiddleware(logger)
	var grpcServer *kratosgrpc.Server
	// Shell/composition mode registers no business RPC. It must never expose a
	// plaintext business listener, so the shell transport is not started; only a
	// configured mTLS command listener serves business traffic.
	serveBusinessGRPC := bc.Command != nil
	cleanup := func() {}
	if bc.Command == nil {
		grpcServer = server.NewGRPCServer(bc.Server.Grpc, middlewares...)
	} else {
		codec, codecErr := newCursorCodec(bc.CursorSigningKey)
		if codecErr != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = observability.Shutdown(ctx)
			return nil, codecErr
		}
		grpcServer, cleanup, err = buildCommandServer(bc.Server.Grpc, bc.Command, codec, middlewares...)
		if err != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = observability.Shutdown(ctx)
			return nil, err
		}
	}
	adminServer := server.NewAdminServer(bc.Server.Admin, readiness, observability.Gatherer(), middlewares...)
	release := sync.OnceValue(func() error {
		cleanup()
		ctx, cancel := context.WithTimeout(context.Background(), bc.Server.ShutdownTimeout.AsDuration())
		defer cancel()
		return observability.Shutdown(ctx)
	})
	return &application{App: newApp(logger, grpcServer, serveBusinessGRPC, adminServer, readiness, bc.Server.ShutdownTimeout.AsDuration(), release), release: release}, nil
}

func newApp(
	logger *slog.Logger,
	grpcServer *kratosgrpc.Server,
	serveBusinessGRPC bool,
	adminServer *kratoshttp.Server,
	readiness *server.Readiness,
	stopTimeout time.Duration,
	release func() error,
) *kratos.App {
	options := []kratos.Option{
		kratos.ID(id), kratos.Name(Name), kratos.Version(Version), kratos.Logger(logger),
	}
	if serveBusinessGRPC {
		options = append(options, kratos.Server(grpcServer, adminServer))
	} else {
		options = append(options, kratos.Server(adminServer))
	}
	options = append(options,
		kratos.BeforeStop(func(context.Context) error {
			readiness.Set(false)
			return nil
		}),
		kratos.AfterStop(func(context.Context) error { return release() }),
		kratos.StopTimeout(stopTimeout),
	)
	return kratos.New(options...)
}

package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	kratos "github.com/go-kratos/kratos/v3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/runtimeproof"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/stepidentity"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
)

func buildRuntimeApp(config *conf.Bootstrap, logger *slog.Logger) (*application, error) {
	clients, err := loadRuntimeClients(config.Runtime)
	if err != nil {
		return nil, err
	}
	pool, err := openCommandPool(config.Command)
	if err != nil {
		clients.close()
		return nil, err
	}
	ready := server.NewReadiness()
	observability, err := server.NewObservability(Name, Version, ready)
	if err != nil {
		pool.Close()
		clients.close()
		return nil, err
	}
	failed := func(message string) (*application, error) {
		pool.Close()
		clients.close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = observability.Shutdown(ctx)
		return nil, errors.New(message)
	}
	middlewares := observability.ServerMiddleware(logger)
	query := service.NewQuery(execution.New(pool), objectstore.NewDownloadSigner(clients.store, config.Runtime.ObjectStorage.ConnectionId), clients.logs)
	admissions, dispatch, facts := execution.New(pool), submission.New(pool), lifecycle.New(pool)
	identity, err := stepidentity.New(clients.kube, config.Runtime.TokenAudience)
	if err != nil {
		return failed("managed workload verifier configuration invalid")
	}
	proof := runtimeproof.New(clients.kube, clients.runs, objectstore.NewVerifier(clients.store, config.Runtime.ObjectStorage.ConnectionId, config.Runtime.ObjectStorage.MaxObjectBytes), dispatch)
	steps, err := biz.NewManagedSteps(dispatch, admissions, identity, proof)
	if err != nil {
		return failed("managed step configuration invalid")
	}
	runtime, err := biz.NewManagedRuntime(steps, facts, trainer.New(clients.kube), proof, proof)
	if err != nil {
		return failed("managed runtime configuration invalid")
	}
	step, err := server.NewManagedStepServer(config.Runtime.Step, clients.certificate, service.NewRuntimeStep(steps, runtime, clients.storageIssuer), middlewares...)
	if err != nil {
		return failed("managed step listener configuration invalid")
	}
	submitter, err := biz.NewPipelineSubmitter(dispatch, clients.runs, min(config.Runtime.ApiTimeout.AsDuration(), 10*time.Second))
	if err != nil {
		return failed("managed submitter configuration invalid")
	}
	dispatcher, err := biz.NewDispatchWorker(dispatch, submitter, clients.binding, int(config.Runtime.DispatchBatchSize), config.Runtime.DispatchInterval.AsDuration())
	if err != nil {
		return failed("managed dispatch configuration invalid")
	}
	closer, err := biz.NewExecutionCloser(runtime, clients.runs, proof)
	if err != nil {
		return failed("managed execution close configuration invalid")
	}
	operations, err := buildExecutionOperations(pool, clients, closer, proof)
	if err != nil {
		return failed("managed execution operations configuration invalid")
	}
	codec, err := newCursorCodec(config.CursorSigningKey)
	if err != nil {
		return failed("managed command cursor signing key invalid")
	}
	command, err := buildCommandServerWithCapabilities(config.Server.Grpc, config.Command, pool, query, objectstore.NewVerifier(clients.store, config.Runtime.ObjectStorage.ConnectionId, config.Runtime.ObjectStorage.MaxObjectBytes), operations, codec, middlewares...)
	if err != nil {
		return failed("managed command listener configuration invalid")
	}
	closeWorker, err := biz.NewCloseWorker(facts, closer, clients.binding, int(config.Runtime.DispatchBatchSize), config.Runtime.DispatchInterval.AsDuration())
	if err != nil {
		return failed("managed close worker configuration invalid")
	}
	workerContext, cancelWorker := context.WithCancel(context.Background())
	worker := &runtimeWorker{dispatcher: dispatcher, closer: closeWorker, ctx: workerContext, cancel: cancelWorker, done: make(chan struct{}), ready: ready}
	admin := server.NewAdminServer(config.Server.Admin, ready, observability.Gatherer(), middlewares...)
	release := sync.OnceValue(func() error {
		worker.unavailable()
		cancelWorker()
		// Run has stopped all started servers before release. Stop also closes
		// listeners allocated by Endpoint if a later listener failed to bind.
		ctx, cancel := context.WithTimeout(context.Background(), config.Server.ShutdownTimeout.AsDuration())
		defer cancel()
		_ = step.Stop(ctx)
		_ = command.Stop(ctx)
		_ = admin.Stop(ctx)
		pool.Close()
		clients.close()
		return observability.Shutdown(ctx)
	})
	app := kratos.New(
		kratos.ID(id), kratos.Name(Name), kratos.Version(Version), kratos.Logger(logger),
		kratos.Server(command, step, admin, worker),
		kratos.AfterStart(func(context.Context) error { worker.available(); return nil }),
		kratos.BeforeStop(func(context.Context) error { worker.unavailable(); return nil }),
		kratos.AfterStop(func(context.Context) error { return release() }),
		kratos.StopTimeout(config.Server.ShutdownTimeout.AsDuration()),
	)
	return &application{App: app, release: release}, nil
}

// This lifecycle adapter delivers admissions and resumes durable closing.
// KFP remains responsible for advancing every normal pipeline step.
type runtimeWorker struct {
	dispatcher *biz.DispatchWorker
	closer     *biz.CloseWorker
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	ready      *server.Readiness
	mu         sync.Mutex
	stopped    bool
}

func (worker *runtimeWorker) Start(ctx context.Context) error {
	stopPropagation := context.AfterFunc(ctx, worker.cancel)
	defer stopPropagation()
	defer close(worker.done)
	loops := []func(context.Context) error{worker.dispatcher.Run}
	if worker.closer != nil {
		loops = append(loops, worker.closer.Run)
	}
	results := make(chan error, len(loops))
	for _, loop := range loops {
		go func(run func(context.Context) error) { results <- run(worker.ctx) }(loop)
	}
	var err error
	for range loops {
		err = errors.Join(err, <-results)
		worker.cancel()
	}
	worker.unavailable()
	if err != nil {
		return errors.New("managed execution worker failed")
	}
	return nil
}

func (worker *runtimeWorker) Stop(ctx context.Context) error {
	worker.unavailable()
	worker.cancel()
	select {
	case <-worker.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (worker *runtimeWorker) available() {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	// This means the configured entry points can serve requests, not that a
	// training run or target-environment business acceptance has succeeded.
	if !worker.stopped {
		worker.ready.Set(true)
	}
}

func (worker *runtimeWorker) unavailable() {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	worker.stopped = true
	worker.ready.Set(false)
}

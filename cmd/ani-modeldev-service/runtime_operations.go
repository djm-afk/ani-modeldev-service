package main

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/cleanup"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
)

// buildExecutionOperations reuses the application's configured owner, clients,
// and private database pool. It introduces no second credential or runtime.
func buildExecutionOperations(pool *pgxpool.Pool, clients runtimeClients, closer *biz.ExecutionCloser, proof biz.OwnerWriterVerifier, training biz.TrainingRuntime) (*service.Operations, error) {
	operations, err := biz.NewManagedExecutionOperations(execution.New(pool), closer, cleanup.New(clients.kube, proof).WithTrainingRuntime(training))
	if err != nil {
		return nil, err
	}
	return service.NewOperations(operations), nil
}

package biz

import (
	"context"
	"strings"
	"time"
)

type PendingCloseRepository interface {
	ListPendingClosures(context.Context, PipelineDispatchBinding, string, int) ([]string, error)
}

// CloseWorker discovers persisted fences and expired original deadlines. It
// observes/stops resources only; no normal pipeline step is driven here.
type CloseWorker struct {
	repository PendingCloseRepository
	closer     *ExecutionCloser
	binding    PipelineDispatchBinding
	batchSize  int
	interval   time.Duration
	gpuRelease *GPUReleaseReconciler
	after      string
}

type CloseBatchResult struct {
	Examined, Closed, Unresolved int
	LastError                    error
}

func NewCloseWorker(repository PendingCloseRepository, closer *ExecutionCloser, binding PipelineDispatchBinding, batchSize int, interval time.Duration, gpuRelease ...*GPUReleaseReconciler) (*CloseWorker, error) {
	if repository == nil || closer == nil || binding.Validate() != nil || batchSize < 1 || batchSize > 100 || interval < 100*time.Millisecond || interval > time.Minute || len(gpuRelease) > 1 {
		return nil, ErrInvalidDispatchWorker
	}
	worker := &CloseWorker{repository: repository, closer: closer, binding: binding, batchSize: batchSize, interval: interval}
	if len(gpuRelease) == 1 {
		if gpuRelease[0] == nil {
			return nil, ErrInvalidDispatchWorker
		}
		worker.gpuRelease = gpuRelease[0]
	}
	return worker, nil
}

// ReconcileOnce is owned by one worker loop. A bounded cursor prevents an
// unresolved early execution from starving later requests; restart scans from
// the beginning and recovers entirely from durable facts.
func (worker *CloseWorker) ReconcileOnce(ctx context.Context) (CloseBatchResult, error) {
	result := CloseBatchResult{}
	if ctx == nil || worker == nil || worker.repository == nil || worker.closer == nil {
		return result, ErrInvalidDispatchWorker
	}
	ids, err := worker.repository.ListPendingClosures(ctx, worker.binding, worker.after, worker.batchSize)
	if err != nil {
		return result, err
	}
	if len(ids) > worker.batchSize {
		return result, ErrPersistence
	}
	for _, id := range ids {
		if !validAdmissionID(id) || strings.ToLower(id) != id || id <= worker.after {
			return result, ErrPersistence
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		closed, err := worker.closer.Reconcile(ctx, worker.binding.TenantID, id)
		if err == nil && closed.Runtime.ClosedAt != nil && closed.Execution.Snapshot.GPU != nil {
			if worker.gpuRelease == nil {
				err = ErrRuntimeNotReady
			} else {
				state, releaseErr := worker.gpuRelease.Reconcile(ctx, worker.binding.TenantID, id)
				err = releaseErr
				if err == nil && (state.GPURelease == nil || state.GPURelease.Outbox == nil || state.GPURelease.Outbox.ACK == nil || state.GPURelease.Outbox.DeliveredAt == nil) {
					err = ErrRuntimeNotReady
				}
			}
		}
		result.Examined++
		if err == nil && closed.Runtime.ClosedAt != nil {
			result.Closed++
		} else {
			// Failed observation leaves the existing fence and CLOSING facts.
			// One unavailable controller must not stop other executions closing.
			result.Unresolved++
			result.LastError = err
		}
		worker.after = id
	}
	if len(ids) < worker.batchSize {
		worker.after = ""
	}
	return result, nil
}

func (worker *CloseWorker) Run(ctx context.Context) error {
	if ctx == nil || worker == nil || worker.interval <= 0 {
		return ErrInvalidDispatchWorker
	}
	ticker := time.NewTicker(worker.interval)
	defer ticker.Stop()
	for {
		if _, err := worker.ReconcileOnce(ctx); err != nil && ctx.Err() == nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

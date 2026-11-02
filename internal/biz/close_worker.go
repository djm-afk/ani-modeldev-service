package biz

import (
	"context"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"sync"
	"time"
)

type CloseReasonSet uint8

const (
	NeedsClose CloseReasonSet = 1 << iota
	NeedsStepOrComputeCleanup
	PendingGPUOutbox
	GPUReleaseNotSealed
)

type PendingCloseRow struct {
	TenantID, ExecutionID string
	AcceptedAt            time.Time
	Reasons               CloseReasonSet
	DecodeProblem         string
}
type PendingClosePage struct {
	ExaminedCount int
	Rows          []PendingCloseRow
}
type PendingCloseRepository interface {
	ListPendingCloseTenants(context.Context, string, string, int) ([]string, error)
	CloseDiscoveryTime(context.Context) (time.Time, error)
	ListPendingClosures(context.Context, string, string, DiscoveryCursor, int) (PendingClosePage, error)
}
type CloseWorker struct {
	repository  PendingCloseRepository
	closer      *ExecutionCloser
	clusterID   string
	interval    time.Duration
	coordinator *WorkerCoordinator
	gpuRelease  *GPUReleaseReconciler
	afterTenant string
	cursors     map[string]DiscoveryCursor
	seenTenants map[string]bool
}
type CloseBatchResult struct {
	Examined, Closed, Unresolved int
	LastError                    error
}

func NewCloseWorker(repository PendingCloseRepository, closer *ExecutionCloser, clusterID string, interval time.Duration, gpuRelease ...*GPUReleaseReconciler) (*CloseWorker, error) {
	if repository == nil || closer == nil || !cpup01.CanonicalTenantID(clusterID) || interval < time.Second || interval > time.Minute || len(gpuRelease) > 1 {
		return nil, ErrInvalidDispatchWorker
	}
	w := &CloseWorker{repository: repository, closer: closer, clusterID: clusterID, interval: interval, coordinator: NewWorkerCoordinator(), cursors: map[string]DiscoveryCursor{}, seenTenants: map[string]bool{}}
	if len(gpuRelease) == 1 {
		if gpuRelease[0] == nil {
			return nil, ErrInvalidDispatchWorker
		}
		w.gpuRelease = gpuRelease[0]
	}
	return w, nil
}
func (w *CloseWorker) ReconcileOnce(ctx context.Context) (CloseBatchResult, error) {
	result := CloseBatchResult{}
	if ctx == nil || w == nil {
		return result, ErrInvalidDispatchWorker
	}
	q, c := context.WithTimeout(ctx, 3*time.Second)
	tenants, err := w.repository.ListPendingCloseTenants(q, w.clusterID, w.afterTenant, 16)
	c()
	if err != nil {
		return result, err
	}
	if len(tenants) > 16 {
		return result, ErrPersistence
	}
	type tenantResult struct {
		cursor DiscoveryCursor
		end    bool
		result CloseBatchResult
		err    error
	}
	results := make([]tenantResult, len(tenants))
	var tasks sync.WaitGroup
	for index, tenant := range tenants {
		if !cpup01.CanonicalTenantID(tenant) || (index > 0 && tenant <= tenants[index-1]) || tenant <= w.afterTenant {
			return result, ErrPersistence
		}
		cursor := w.cursors[tenant]
		tasks.Add(1)
		go func(index int, tenant string, cursor DiscoveryCursor) {
			defer tasks.Done()
			release, err := w.coordinator.acquire(ctx, tenant)
			if err != nil {
				results[index].err = err
				return
			}
			defer release()
			current := tenantResult{cursor: cursor}
			defer func() { results[index] = current }()
			query, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			if cursor.SweepBefore.IsZero() {
				cursor.SweepBefore, current.err = w.repository.CloseDiscoveryTime(query)
				if current.err != nil {
					return
				}
			}
			page, e := w.repository.ListPendingClosures(query, w.clusterID, tenant, cursor, 4)
			if e != nil {
				current.err = e
				return
			}
			if page.ExaminedCount != len(page.Rows) || len(page.Rows) > 4 {
				current.err = ErrPersistence
				return
			}
			current.end = len(page.Rows) < 4
			for _, row := range page.Rows {
				current.result.Examined++
				cursor.AfterAcceptedAt = row.AcceptedAt
				cursor.AfterExecutionID = row.ExecutionID
				if row.DecodeProblem != "" {
					current.result.Unresolved++
					continue
				}
				if row.Reasons == 0 {
					continue
				}
				completed := false
				if row.Reasons&PendingGPUOutbox != 0 {
					completed = true
					if w.gpuRelease == nil {
						e = ErrRuntimeNotReady
					} else {
						bounded, stop := context.WithTimeout(ctx, 10*time.Second)
						_, e = w.gpuRelease.Reconcile(bounded, tenant, row.ExecutionID)
						stop()
					}
				} else {
					bounded, stop := context.WithTimeout(ctx, 30*time.Second)
					closed, closeErr := w.closer.Reconcile(bounded, tenant, row.ExecutionID)
					stop()
					e = closeErr
					completed = closed.Runtime.ClosedAt != nil
					if e == nil && closed.Runtime.ClosedAt != nil && closed.Execution.Snapshot.GPU != nil {
						if w.gpuRelease == nil {
							e = ErrRuntimeNotReady
						} else {
							bounded, stop := context.WithTimeout(ctx, 10*time.Second)
							_, e = w.gpuRelease.Reconcile(bounded, tenant, row.ExecutionID)
							stop()
						}
					}
				}
				if e == nil && completed {
					current.result.Closed++
				} else {
					current.result.Unresolved++
					current.result.LastError = e
				}
			}
			current.cursor = cursor
		}(index, tenant, cursor)
	}
	tasks.Wait()
	for index, tenant := range tenants {
		r := results[index]
		if r.err != nil {
			return result, r.err
		}
		if r.end {
			delete(w.cursors, tenant)
		} else {
			w.cursors[tenant] = r.cursor
		}
		result.Examined += r.result.Examined
		result.Closed += r.result.Closed
		result.Unresolved += r.result.Unresolved
		if r.result.LastError != nil {
			result.LastError = r.result.LastError
		}
		w.afterTenant = tenant
		w.seenTenants[tenant] = true
	}
	if len(tenants) < 16 {
		w.afterTenant = ""
		for tenant := range w.cursors {
			if !w.seenTenants[tenant] {
				delete(w.cursors, tenant)
			}
		}
		clear(w.seenTenants)
	}
	return result, nil
}
func (w *CloseWorker) Run(ctx context.Context) error {
	if ctx == nil || w == nil {
		return ErrInvalidDispatchWorker
	}
	backoff := time.Second
	for {
		_, err := w.ReconcileOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		delay := w.interval
		if err != nil {
			delay = backoff
			backoff = min(backoff*2, 30*time.Second)
		} else {
			backoff = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

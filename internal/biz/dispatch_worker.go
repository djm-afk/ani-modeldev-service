package biz

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

var ErrInvalidDispatchWorker = errors.New("INVALID_DISPATCH_WORKER_CONFIGURATION")

type DiscoveryCursor struct {
	SweepBefore      time.Time
	AfterAcceptedAt  time.Time
	AfterExecutionID string
}
type PendingAdmissionRow struct {
	TenantID, ExecutionID string
	AcceptedAt            time.Time
	Admission             Admission
	DecodeProblem         string
}
type PendingAdmissionPage struct {
	ExaminedCount int
	Rows          []PendingAdmissionRow
}
type PendingAdmissionRepository interface {
	ListPendingDispatchTenants(context.Context, string, string, int) ([]string, error)
	DispatchDiscoveryTime(context.Context) (time.Time, error)
	ListPendingAdmissions(context.Context, string, string, DiscoveryCursor, int) (PendingAdmissionPage, error)
}
type DispatchEnvironmentVerifier interface {
	VerifyDispatchAdmission(context.Context, Admission) error
}

type DispatchWorker struct {
	repository   PendingAdmissionRepository
	submitter    *PipelineSubmitter
	environments DispatchEnvironmentVerifier
	clusterID    string
	interval     time.Duration
	coordinator  *WorkerCoordinator
	afterTenant  string
	cursors      map[string]DiscoveryCursor
	seenTenants  map[string]bool
}
type DispatchBatchResult struct {
	Examined, Succeeded, Unresolved int
	LastError                       error
}

func NewDispatchWorker(repository PendingAdmissionRepository, submitter *PipelineSubmitter, clusterID string, environments DispatchEnvironmentVerifier, interval time.Duration) (*DispatchWorker, error) {
	if repository == nil || submitter == nil || environments == nil || !cpup01.CanonicalTenantID(clusterID) || interval < time.Second || interval > time.Minute {
		return nil, ErrInvalidDispatchWorker
	}
	return &DispatchWorker{repository: repository, submitter: submitter, environments: environments, clusterID: clusterID, interval: interval, coordinator: NewWorkerCoordinator(), cursors: map[string]DiscoveryCursor{}, seenTenants: map[string]bool{}}, nil
}
func (w *DispatchWorker) DispatchOnce(ctx context.Context) (DispatchBatchResult, error) {
	result := DispatchBatchResult{}
	if ctx == nil || w == nil {
		return result, ErrInvalidDispatchWorker
	}
	query, cancel := context.WithTimeout(ctx, 3*time.Second)
	tenants, err := w.repository.ListPendingDispatchTenants(query, w.clusterID, w.afterTenant, 16)
	cancel()
	if err != nil {
		return result, err
	}
	if len(tenants) > 16 {
		return result, ErrPersistence
	}
	type tenantResult struct {
		cursor DiscoveryCursor
		end    bool
		result DispatchBatchResult
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
			q, c := context.WithTimeout(ctx, 3*time.Second)
			defer c()
			if cursor.SweepBefore.IsZero() {
				cursor.SweepBefore, current.err = w.repository.DispatchDiscoveryTime(q)
				if current.err != nil {
					return
				}
			}
			page, e := w.repository.ListPendingAdmissions(q, w.clusterID, tenant, cursor, 4)
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
				bounded, stop := context.WithTimeout(ctx, 30*time.Second)
				e = w.environments.VerifyDispatchAdmission(bounded, row.Admission)
				if e == nil {
					_, e = w.submitter.Submit(bounded, PipelineDispatchRequest{Admission: row.Admission, Owner: row.Admission.Snapshot.DispatchOwner})
				}
				stop()
				if e == nil || errors.Is(e, ErrPipelineSubmissionUncertain) || errors.Is(e, ErrPipelineSubmissionNotSent) {
					current.result.Succeeded++
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
		result.Succeeded += r.result.Succeeded
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
func (w *DispatchWorker) Run(ctx context.Context) error {
	if ctx == nil || w == nil {
		return ErrInvalidDispatchWorker
	}
	backoff := time.Second
	for {
		_, err := w.DispatchOnce(ctx)
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

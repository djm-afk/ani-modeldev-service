package submittest_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

type transientDiscovery struct {
	biz.PendingAdmissionRepository
	mu        sync.Mutex
	calls     []time.Time
	recovered chan struct{}
}

func (r *transientDiscovery) ListPendingDispatchTenants(ctx context.Context, cluster, after string, limit int) ([]string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, time.Now())
	n := len(r.calls)
	r.mu.Unlock()
	if n <= 3 {
		return nil, biz.ErrPersistence
	}
	tenants, err := r.PendingAdmissionRepository.ListPendingDispatchTenants(ctx, cluster, after, limit)
	if err == nil && n == 4 {
		close(r.recovered)
	}
	return tenants, err
}

func TestDispatchLoopRecoversFromDatabaseFailureWithBoundedBackoff(t *testing.T) {
	open := postgres.Prepare(t)
	repo := &transientDiscovery{PendingAdmissionRepository: submission.New(open()), recovered: make(chan struct{})}
	// The empty real database has nothing to send. Inject only the external
	// database outage; the worker's loop, timers and recovery query are real.
	worker, err := biz.NewDispatchWorker(repo, &biz.PipelineSubmitter{}, dispatchRequest(t).Admission.Snapshot.Environment.ClusterID, testEnvironmentGate{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case <-repo.recovered:
		cancel()
	case <-ctx.Done():
		t.Fatal("database recovery permanently stopped discovery")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.calls) != 4 {
		t.Fatal("unexpected retry backlog", len(repo.calls))
	}
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		if elapsed := repo.calls[i+1].Sub(repo.calls[i]); elapsed < want-100*time.Millisecond || elapsed > want+3*time.Second {
			t.Fatalf("failure retry %d interval %s, expected bounded %s", i+1, elapsed, want)
		}
	}
}

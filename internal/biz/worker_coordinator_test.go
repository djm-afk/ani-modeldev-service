package biz

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestWorkerCoordinatorCancellationDoesNotConsumeCapacityOrTenant(t *testing.T) {
	c := NewWorkerCoordinator()
	release, err := c.acquire(context.Background(), "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan error, 1)
	go func() { _, err := c.acquire(ctx, "tenant-a"); waiting <- err }()
	cancel()
	if err := <-waiting; !errors.Is(err, context.Canceled) {
		t.Fatalf("same-tenant waiter: %v", err)
	}
	release()
	release() // A repeated deferred release cannot free another holder's slot.
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err = c.acquire(ctx, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for i := 0; i < 3; i++ {
		r, err := c.acquire(ctx, fmt.Sprint(i))
		if err != nil {
			t.Fatal(err)
		}
		defer r()
	}
	full, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	defer stop()
	if _, err := c.acquire(full, "fifth"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fifth tenant must wait for shared capacity: %v", err)
	}
}

type coordinatorDiscovery struct {
	tenants []string
	visit   func(context.Context, string) error
}

func (r coordinatorDiscovery) ListPendingDispatchTenants(context.Context, string, string, int) ([]string, error) {
	return r.tenants, nil
}
func (r coordinatorDiscovery) ListPendingCloseTenants(context.Context, string, string, int) ([]string, error) {
	return r.tenants, nil
}
func (r coordinatorDiscovery) DispatchDiscoveryTime(context.Context) (time.Time, error) {
	return time.Unix(1, 0), nil
}
func (r coordinatorDiscovery) CloseDiscoveryTime(context.Context) (time.Time, error) {
	return time.Unix(1, 0), nil
}
func (r coordinatorDiscovery) ListPendingAdmissions(ctx context.Context, _, tenant string, _ DiscoveryCursor, _ int) (PendingAdmissionPage, error) {
	return PendingAdmissionPage{}, r.visit(ctx, tenant)
}
func (r coordinatorDiscovery) ListPendingClosures(ctx context.Context, _, tenant string, _ DiscoveryCursor, _ int) (PendingClosePage, error) {
	return PendingClosePage{}, r.visit(ctx, tenant)
}

type coordinatorEnvironment struct{}

func (coordinatorEnvironment) VerifyDispatchAdmission(context.Context, Admission) error { return nil }

func coordinatorWorkers(t *testing.T, dispatchRepo, closeRepo coordinatorDiscovery) (*DispatchWorker, *CloseWorker) {
	t.Helper()
	const cluster = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	dispatch, err := NewDispatchWorker(dispatchRepo, &PipelineSubmitter{}, cluster, coordinatorEnvironment{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	close, err := NewCloseWorker(closeRepo, &ExecutionCloser{}, cluster, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.coordinator == close.coordinator {
		t.Fatal("default constructors must remain independently usable")
	}
	shared := NewWorkerCoordinator()
	if dispatch.SetCoordinator(shared) != nil || close.SetCoordinator(shared) != nil {
		t.Fatal("share coordinator")
	}
	return dispatch, close
}

func TestWorkerCoordinatorBoundsDispatchAndCloseTogether(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entered := make(chan string, 8)
	finish := make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(finish) })
	var mu sync.Mutex
	active, peak := 0, 0
	visit := func(ctx context.Context, tenant string) error {
		mu.Lock()
		active++
		peak = max(peak, active)
		mu.Unlock()
		entered <- tenant
		select {
		case <-finish:
		case <-ctx.Done():
		}
		mu.Lock()
		active--
		mu.Unlock()
		return ctx.Err()
	}
	var dispatchTenants, closeTenants []string
	for i := 1; i <= 8; i++ {
		tenant := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		if i <= 4 {
			dispatchTenants = append(dispatchTenants, tenant)
		} else {
			closeTenants = append(closeTenants, tenant)
		}
	}
	dispatch, closer := coordinatorWorkers(t, coordinatorDiscovery{dispatchTenants, visit}, coordinatorDiscovery{closeTenants, visit})
	done := make(chan error, 2)
	go func() { _, err := dispatch.DispatchOnce(ctx); done <- err }()
	go func() { _, err := closer.ReconcileOnce(ctx); done <- err }()
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("workers did not use all four shared slots")
		}
	}
	select {
	case tenant := <-entered:
		t.Fatalf("fifth simultaneous tenant entered: %s", tenant)
	case <-time.After(40 * time.Millisecond):
	}
	unblock.Do(func() { close(finish) })
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if active != 0 || peak != 4 {
		t.Fatalf("active=%d peak=%d, want 0 and 4 across both loops", active, peak)
	}
}

func TestWorkerCoordinatorSameTenantWaitCanCancelAcrossLoops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	const tenant = "00000000-0000-4000-8000-000000000001"
	dispatchEntered, closeEntered := make(chan struct{}, 1), make(chan struct{}, 1)
	finish := make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(finish) })
	dispatch, closer := coordinatorWorkers(t,
		coordinatorDiscovery{[]string{tenant}, func(ctx context.Context, _ string) error {
			dispatchEntered <- struct{}{}
			select {
			case <-finish:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}},
		coordinatorDiscovery{[]string{tenant}, func(context.Context, string) error { closeEntered <- struct{}{}; return nil }})
	dispatched := make(chan error, 1)
	go func() { _, err := dispatch.DispatchOnce(ctx); dispatched <- err }()
	select {
	case <-dispatchEntered:
	case <-ctx.Done():
		t.Fatal("dispatch did not enter")
	}
	closing, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	defer stop()
	if _, err := closer.ReconcileOnce(closing); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same tenant must wait and obey cancellation: %v", err)
	}
	select {
	case <-closeEntered:
		t.Fatal("close overlapped dispatch for the same tenant")
	default:
	}
	unblock.Do(func() { close(finish) })
	if err := <-dispatched; err != nil {
		t.Fatal(err)
	}
	if _, err := closer.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closeEntered:
	default:
		t.Fatal("canceled waiter prevented subsequent tenant processing")
	}
}

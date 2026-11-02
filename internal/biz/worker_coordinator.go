package biz

import (
	"context"
	"sync"
)

// WorkerCoordinator bounds the dispatch and close loops together. It holds no
// queue, lease or execution state: each active tenant occupies one of four
// shared slots until its current finite page has finished.
type WorkerCoordinator struct {
	mu      sync.Mutex
	active  map[string]bool
	changed chan struct{}
}

func NewWorkerCoordinator() *WorkerCoordinator {
	return &WorkerCoordinator{active: make(map[string]bool), changed: make(chan struct{})}
}

func (c *WorkerCoordinator) acquire(ctx context.Context, tenant string) (func(), error) {
	for {
		c.mu.Lock()
		if err := ctx.Err(); err != nil {
			c.mu.Unlock()
			return nil, err
		}
		if len(c.active) < 4 && !c.active[tenant] {
			c.active[tenant] = true
			c.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					c.mu.Lock()
					delete(c.active, tenant)
					close(c.changed)
					c.changed = make(chan struct{})
					c.mu.Unlock()
				})
			}, nil
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

// SetCoordinator is composition-only wiring before either worker is started.
func (w *DispatchWorker) SetCoordinator(c *WorkerCoordinator) error {
	if w == nil || c == nil {
		return ErrInvalidDispatchWorker
	}
	w.coordinator = c
	return nil
}

// SetCoordinator is composition-only wiring before either worker is started.
func (w *CloseWorker) SetCoordinator(c *WorkerCoordinator) error {
	if w == nil || c == nil {
		return ErrInvalidDispatchWorker
	}
	w.coordinator = c
	return nil
}

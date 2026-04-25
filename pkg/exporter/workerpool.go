package exporter

import (
	"context"

	"golang.org/x/sync/errgroup"
)

// workerPool is a bounded errgroup. Submit blocks when n workers are
// already running; Wait returns the first error any submitted job
// produced and cancels the derived context so still-running jobs can
// exit promptly.
type workerPool struct {
	sem chan struct{}
	eg  *errgroup.Group
	ctx context.Context
}

// newWorkerPool returns a worker pool of capacity n along with a
// context derived from ctx that workers should observe for
// cancellation. n is clamped to 1 if non-positive.
func newWorkerPool(ctx context.Context, n int) (*workerPool, context.Context) {
	if n < 1 {
		n = 1
	}
	eg, gctx := errgroup.WithContext(ctx)
	return &workerPool{
		sem: make(chan struct{}, n),
		eg:  eg,
		ctx: gctx,
	}, gctx
}

// Submit enqueues fn. Blocks if the pool is full. If the pool's
// context is already cancelled, Submit returns immediately without
// running fn (the cancel error is observed by Wait).
func (p *workerPool) Submit(fn func() error) {
	select {
	case <-p.ctx.Done():
		return
	case p.sem <- struct{}{}:
	}
	p.eg.Go(func() error {
		defer func() { <-p.sem }()
		return fn()
	})
}

// Wait blocks until every submitted job has returned, then returns
// the first error encountered (if any).
func (p *workerPool) Wait() error {
	return p.eg.Wait()
}

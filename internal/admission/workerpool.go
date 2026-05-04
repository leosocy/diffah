// Package admission provides shared worker-pool and admission-controller
// primitives for diffah's exporter and importer streaming pipelines.
//
// Two pool types coexist:
//   - WorkerPool: bounded errgroup, no per-task RSS estimate. Used for
//     priming/fan-out work where the per-task footprint is uniform.
//   - AdmissionPool: WorkerPool semantics plus a memory-budget semaphore
//     and singleflight dedup. Used for encode/apply where per-task RSS
//     varies with the item being processed.
//
// Both pools recover panics from submitted closures and translate them
// into errgroup errors so a runtime panic can never skip parent
// `defer cleanup()` blocks.
package admission

import (
	"context"
	"fmt"
	"runtime/debug"

	"golang.org/x/sync/errgroup"
)

// WorkerPool is a bounded errgroup. Submit blocks when n workers are
// already running; Wait returns the first error any submitted job
// produced and cancels the derived context so still-running jobs can
// exit promptly.
type WorkerPool struct {
	sem chan struct{}
	eg  *errgroup.Group
	ctx context.Context
}

// NewWorkerPool returns a worker pool of capacity n along with a
// context derived from ctx that workers should observe for
// cancellation. n is clamped to 1 if non-positive.
func NewWorkerPool(ctx context.Context, n int) (*WorkerPool, context.Context) {
	if n < 1 {
		n = 1
	}
	eg, gctx := errgroup.WithContext(ctx)
	return &WorkerPool{
		sem: make(chan struct{}, n),
		eg:  eg,
		ctx: gctx,
	}, gctx
}

// Submit enqueues fn. Blocks if the pool is full. If the pool's
// context is already cancelled, Submit returns immediately without
// running fn but records ctx.Err() on the errgroup so callers see
// the cancellation in Wait() at the same point they would have seen
// any other failure. errgroup collapses multiple ctx.Err submissions
// to the first error, so repeated post-cancel Submits are harmless.
//
// The ctx.Err() guard before the select gives cancel deterministic
// priority — without it, Go's select would pick randomly between
// `<-p.ctx.Done()` and an empty `p.sem <- struct{}{}` when both are
// ready, and ~half of post-cancel Submits would slip through.
func (p *WorkerPool) Submit(fn func() error) {
	if err := p.ctx.Err(); err != nil {
		p.eg.Go(func() error { return err })
		return
	}
	select {
	case <-p.ctx.Done():
		p.eg.Go(func() error { return p.ctx.Err() })
		return
	case p.sem <- struct{}{}:
	}
	p.eg.Go(func() (err error) {
		defer func() {
			<-p.sem
			if r := recover(); r != nil {
				err = fmt.Errorf("worker panic: %v\n%s", r, debug.Stack())
			}
		}()
		return fn()
	})
}

// Wait blocks until every submitted job has returned, then returns
// the first error encountered (if any).
func (p *WorkerPool) Wait() error {
	return p.eg.Wait()
}

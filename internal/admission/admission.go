package admission

import (
	"context"
	"fmt"
	"runtime/debug"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"
)

// AdmissionPool runs submitted work under three gates:
//
//   - singleflight collapses concurrent submissions on the same key
//     so duplicate work for identical content never runs twice.
//   - workerSem (capacity = goroutine count) bounds CPU parallelism.
//   - memSem (capacity = memBudget bytes; nil = disabled) bounds
//     concurrent in-flight RSS by requiring each Submit to acquire its
//     estimated bytes before starting.
//
// Acquisition order is: singleflight → workerSem → memSem. Reverse
// order would deadlock under exhaustion. The recover boundary at the
// goroutine entry converts panics in fn to errgroup errors so caller
// `defer cleanup()` always fires.
//
// The "AdmissionPool" name pairs with WorkerPool to convey two distinct
// pool flavors at call sites; the package-name stutter is intentional.
//
//nolint:revive // paired naming with WorkerPool, see doc comment.
type AdmissionPool struct {
	g         *errgroup.Group
	gctx      context.Context
	workerSem *semaphore.Weighted
	memSem    *semaphore.Weighted
	memBudget int64
	sf        singleflight.Group
}

// NewAdmissionPool returns a pool with `workers` parallelism and
// `memoryBudget` bytes of admission. memoryBudget==0 disables memSem
// (operator opt-out, e.g. benchmarking).
func NewAdmissionPool(ctx context.Context, workers int, memoryBudget int64) *AdmissionPool {
	g, gctx := errgroup.WithContext(ctx)
	if workers < 1 {
		workers = 1
	}
	p := &AdmissionPool{
		g:         g,
		gctx:      gctx,
		workerSem: semaphore.NewWeighted(int64(workers)),
		memBudget: memoryBudget,
	}
	if memoryBudget > 0 {
		p.memSem = semaphore.NewWeighted(memoryBudget)
	}
	return p
}

// Submit runs fn under all three gates, dedup'd by key. estimate is
// the predicted per-task RSS in bytes.
//
// Admission gates only change WHEN tasks run, not WHAT they produce;
// output remains deterministic across (workers, memBudget) combinations.
func (p *AdmissionPool) Submit(key string, estimate int64, fn func() error) {
	if estimate < 1 {
		estimate = 1
	}
	if p.memBudget > 0 && estimate > p.memBudget {
		estimate = p.memBudget
	}
	p.g.Go(func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("admission task panic: %v\n%s", r, debug.Stack())
			}
		}()
		_, sferr, _ := p.sf.Do(key, func() (any, error) {
			return nil, p.runWithGates(estimate, fn)
		})
		return sferr
	})
}

func (p *AdmissionPool) runWithGates(estimate int64, fn func() error) error {
	if err := p.workerSem.Acquire(p.gctx, 1); err != nil {
		return err
	}
	defer p.workerSem.Release(1)
	if p.memSem != nil {
		if err := p.memSem.Acquire(p.gctx, estimate); err != nil {
			return err
		}
		defer p.memSem.Release(estimate)
	}
	return fn()
}

// Wait blocks until every submitted job has returned, then returns the
// first error encountered (if any). ctx cancellation is surfaced here.
func (p *AdmissionPool) Wait() error { return p.g.Wait() }

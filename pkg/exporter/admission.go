package exporter

import (
	"context"

	"github.com/opencontainers/go-digest"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"
)

// rssEstimateByWindowLog is the windowLog → estimated peak RSS table.
// Values are deliberately conservative — see spec §4.3 risks. The
// admission controller blocks new encodes from being admitted unless
// (sum of in-flight estimates) + new_estimate ≤ budget.
var rssEstimateByWindowLog = map[int]int64{
	27: 256 << 20,
	28: 512 << 20,
	29: 1 << 30,
	30: 2 << 30,
	31: 4 << 30,
}

// estimateRSSForWindowLog returns the conservative peak RSS estimate for the
// given windowLog. Unknown values fall back to the largest table entry so that
// out-of-range inputs never under-count memory.
func estimateRSSForWindowLog(wl int) int64 {
	if v, ok := rssEstimateByWindowLog[wl]; ok {
		return v
	}
	return rssEstimateByWindowLog[31]
}

// encodePool runs encode functions under two gates:
//
//   - workerSem (capacity = goroutine count) — bounds CPU parallelism.
//   - memSem (capacity = memBudget bytes; nil = disabled) — bounds concurrent
//     encoder RSS by requiring each encode to acquire its estimated bytes before
//     starting. See spec §5.3 and §4.3.
//
// singleflight collapses concurrent submissions on the same digest so
// duplicate encodes of identical content never start. sf.Do is entered before
// workerSem acquisition so concurrent submitters for the same digest are
// deduplicated before they consume a worker slot.
//
// Priming uses workerPool (no per-task RSS estimate); encodes use encodePool
// with admission gates — the two pool types coexist deliberately.
type encodePool struct {
	g         *errgroup.Group
	gctx      context.Context
	workerSem *semaphore.Weighted
	memSem    *semaphore.Weighted
	memBudget int64
	sf        singleflight.Group
}

func newEncodePool(ctx context.Context, workers int, memoryBudget int64) *encodePool {
	g, gctx := errgroup.WithContext(ctx)
	if workers < 1 {
		workers = 1
	}
	p := &encodePool{
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

// Submit runs fn under both gates, dedup'd by d. estimate is the predicted
// per-encode RSS. Admission only changes WHEN encodes run, not WHAT they
// produce — output is deterministic.
func (p *encodePool) Submit(d digest.Digest, estimate int64, fn func() error) {
	if estimate < 1 {
		estimate = 1
	}
	// Defense-in-depth: clamp estimate to the budget so a too-large estimate
	// can never deadlock memSem.Acquire. In practice this branch is
	// unreachable because checkSingleLayerFitsInBudget rejects too-large
	// estimates upstream — if it fires, the upstream guard was bypassed.
	if p.memBudget > 0 && estimate > p.memBudget {
		log().Warn("encode estimate exceeds memory budget; clamping",
			"estimate", estimate, "budget", p.memBudget,
			"hint", "checkSingleLayerFitsInBudget should have caught this upstream")
		estimate = p.memBudget
	}
	p.g.Go(func() error {
		// singleflight entered before workerSem so concurrent submitters for the
		// same digest are deduplicated before consuming a worker slot.
		_, err, _ := p.sf.Do(string(d), func() (any, error) {
			return nil, p.runWithGates(estimate, fn)
		})
		return err
	})
}

func (p *encodePool) runWithGates(estimate int64, fn func() error) error {
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

// Wait blocks until every submitted job has returned, then returns the first
// error encountered (if any). ctx cancellation is surfaced here.
func (p *encodePool) Wait() error { return p.g.Wait() }

package exporter

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerPool_BoundsConcurrency(t *testing.T) {
	const n = 4
	const jobs = 32
	pool, _ := newWorkerPool(context.Background(), n)

	var inflight, peak atomic.Int64
	for i := 0; i < jobs; i++ {
		pool.Submit(func() error {
			cur := inflight.Add(1)
			for {
				old := peak.Load()
				if cur <= old || peak.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			inflight.Add(-1)
			return nil
		})
	}
	if err := pool.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if peak.Load() > int64(n) {
		t.Fatalf("peak inflight = %d > workers = %d", peak.Load(), n)
	}
}

func TestWorkerPool_PropagatesError(t *testing.T) {
	pool, _ := newWorkerPool(context.Background(), 4)
	want := errors.New("boom")
	pool.Submit(func() error { return want })
	pool.Submit(func() error { time.Sleep(10 * time.Millisecond); return nil })
	got := pool.Wait()
	if !errors.Is(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestWorkerPool_CtxCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pool, poolCtx := newWorkerPool(ctx, 4)

	var ran atomic.Int64
	for i := 0; i < 16; i++ {
		pool.Submit(func() error {
			select {
			case <-poolCtx.Done():
				return poolCtx.Err()
			case <-time.After(50 * time.Millisecond):
				ran.Add(1)
				return nil
			}
		})
	}
	time.Sleep(5 * time.Millisecond)
	cancel()
	err := pool.Wait()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if ran.Load() == 16 {
		t.Fatalf("all 16 jobs ran despite cancellation; expected fewer")
	}
}

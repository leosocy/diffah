package exporter

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
)

func TestEstimateRSSForWindowLog_TableIsConservative(t *testing.T) {
	cases := []struct {
		windowLog int
		min       int64
	}{
		{27, 256 << 20},
		{30, 2 << 30},
		{31, 4 << 30},
	}
	for _, c := range cases {
		got := estimateRSSForWindowLog(c.windowLog)
		if got < c.min {
			t.Errorf("windowLog=%d: estimate %d < min %d", c.windowLog, got, c.min)
		}
	}
	// Out-of-table values fall back to the largest entry.
	if got := estimateRSSForWindowLog(99); got < (4 << 30) {
		t.Errorf("out-of-table fallback too small: %d", got)
	}
}

func TestAdmission_SerializeWhenSumExceedsBudget(t *testing.T) {
	// Budget 4 GiB. Three "encodes" of 2 GiB each must run two-then-one,
	// not all three concurrently.
	budget := int64(4 << 30)
	estimate := int64(2 << 30)
	pool := newEncodePool(context.Background(), 8, budget) // 8 worker slots, 4 GiB budget

	const n = 3
	var concurrent atomic.Int32
	var peakConcurrent atomic.Int32
	hold := 50 * time.Millisecond

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		pool.Submit(testDigest(i), estimate, func() error {
			cur := concurrent.Add(1)
			defer concurrent.Add(-1)
			for {
				peak := peakConcurrent.Load()
				if cur <= peak || peakConcurrent.CompareAndSwap(peak, cur) {
					break
				}
			}
			time.Sleep(hold)
			wg.Done()
			return nil
		})
	}
	if err := pool.Wait(); err != nil {
		t.Fatalf("pool wait: %v", err)
	}
	wg.Wait()
	peak := peakConcurrent.Load()
	if peak > 2 {
		t.Errorf("expected ≤2 concurrent (4 GiB / 2 GiB), got peak %d (admission too lax)", peak)
	}
	// Correction D: also assert admission not too tight — if all encodes
	// serialize despite budget allowing two concurrent, the gate is broken.
	if peak < 2 {
		t.Errorf("expected ≥2 concurrent under legal admission, got %d (admission too tight)", peak)
	}
}

func TestAdmission_DisabledWhenBudgetIsZero(t *testing.T) {
	pool := newEncodePool(context.Background(), 4, 0) // 0 = admission disabled
	estimate := int64(1 << 50)                        // absurdly large; would block forever if gate active
	done := make(chan struct{})
	pool.Submit(testDigest(0), estimate, func() error { close(done); return nil })
	if err := pool.Wait(); err != nil {
		t.Fatalf("pool wait: %v", err)
	}
	select {
	case <-done:
	default:
		t.Fatal("encode never ran")
	}
}

func TestAdmission_SingleflightCollapsesSameDigest(t *testing.T) {
	// Submit all 3 after the winner is already in-flight so they're concurrent
	// at sf.Do and deduplicated. We use a gate channel: submit the winner
	// with a slow fn, wait for it to signal it's running, then submit the
	// duplicates. singleflight collapses them.
	ctx := context.Background()
	pool := newEncodePool(ctx, 4, 0)
	d := testDigest(0)
	var calls atomic.Int32

	running := make(chan struct{})
	unblock := make(chan struct{})
	// Submit winner first with a gated fn.
	pool.Submit(d, 1, func() error {
		close(running)
		<-unblock
		calls.Add(1)
		return nil
	})
	// Ensure winner is inside sf.Do before we submit duplicates.
	<-running
	pool.Submit(d, 1, func() error { calls.Add(1); return nil })
	pool.Submit(d, 1, func() error { calls.Add(1); return nil })
	// Give the duplicate goroutines time to be scheduled and enter sf.Do
	// before the winner returns. Without this, the Go runtime may not
	// schedule them until after close(unblock).
	time.Sleep(5 * time.Millisecond)
	close(unblock)

	if err := pool.Wait(); err != nil {
		t.Fatalf("pool wait: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 call under singleflight, got %d", got)
	}
}

func TestAdmission_PropagatesError(t *testing.T) {
	pool := newEncodePool(context.Background(), 4, 0)
	pool.Submit(testDigest(0), 1, func() error { return errors.New("boom") })
	if err := pool.Wait(); err == nil || err.Error() != "boom" {
		t.Fatalf("expected boom, got %v", err)
	}
}

// TestAdmission_CtxCancelPropagatesToWait verifies that ctx cancellation while
// an encode is blocked waiting for the memory semaphore propagates through
// pool.Wait(). Correction E: workers=4 so workerSem doesn't block; budget set
// so that one encode holds the full budget and the second blocks on memSem.
func TestAdmission_CtxCancelPropagatesToWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// budget = 2 GiB; estimate = 2 GiB per encode.
	// First encode acquires memSem, holds it until ctx is cancelled.
	// Second encode acquires workerSem but blocks on memSem.
	budget := int64(2 << 30)
	estimate := int64(2 << 30)
	pool := newEncodePool(ctx, 4, budget)

	firstRunning := make(chan struct{})
	pool.Submit(testDigest(0), estimate, func() error {
		close(firstRunning)
		// Hold until context is cancelled.
		<-ctx.Done()
		return ctx.Err()
	})

	// Wait for first encode to be running before submitting the blocker.
	<-firstRunning

	pool.Submit(testDigest(1), estimate, func() error {
		// This should never run — it blocks on memSem after the first encode
		// holds the full budget.
		return nil
	})

	cancel()

	err := pool.Wait()
	if err == nil {
		t.Fatal("expected error after ctx cancel, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// testDigest builds a digest.Digest from an integer index for use in tests.
// Avoids importing additional packages by using digest.FromString directly.
func testDigest(i int) digest.Digest {
	return digest.FromString(fmt.Sprintf("test-%d", i))
}

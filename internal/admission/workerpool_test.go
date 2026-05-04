package admission

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerPool_FirstErrorWins(t *testing.T) {
	p, _ := NewWorkerPool(context.Background(), 4)
	want := errors.New("boom")
	p.Submit(func() error { return want })
	p.Submit(func() error { time.Sleep(20 * time.Millisecond); return errors.New("late") })
	if err := p.Wait(); !errors.Is(err, want) {
		t.Fatalf("got %v want %v", err, want)
	}
}

func TestWorkerPool_RecoversPanicAsError(t *testing.T) {
	p, _ := NewWorkerPool(context.Background(), 2)
	p.Submit(func() error { panic("kaboom") })
	err := p.Wait()
	if err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("expected panic surfaced as error, got %v", err)
	}
	if !strings.Contains(err.Error(), "worker panic") {
		t.Fatalf("expected wrapper prefix, got %v", err)
	}
}

func TestWorkerPool_BoundedConcurrency(t *testing.T) {
	const n = 3
	p, _ := NewWorkerPool(context.Background(), n)
	var inFlight, peak int32
	for i := 0; i < 16; i++ {
		p.Submit(func() error {
			cur := atomic.AddInt32(&inFlight, 1)
			for {
				prev := atomic.LoadInt32(&peak)
				if cur <= prev || atomic.CompareAndSwapInt32(&peak, prev, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
			return nil
		})
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if peak > n {
		t.Fatalf("peak %d exceeded n=%d", peak, n)
	}
}

func TestWorkerPool_CtxCancelDropsSubmits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, _ := NewWorkerPool(ctx, 1)
	cancel()
	var ran int32
	p.Submit(func() error { atomic.StoreInt32(&ran, 1); return nil })
	if err := p.Wait(); err == nil {
		t.Fatalf("expected ctx err, got nil")
	}
	if atomic.LoadInt32(&ran) == 1 {
		t.Fatalf("submitted fn ran despite ctx cancel")
	}
}

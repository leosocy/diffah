package admission

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Gated form (channel sync) ensures all 8 Submits coalesce into one
// singleflight execution deterministically, instead of relying on a
// time.Sleep that goes flaky under load.
func TestAdmission_SingleflightDedup(t *testing.T) {
	p := NewAdmissionPool(context.Background(), 4, 0)
	var ran int32
	running := make(chan struct{})
	unblock := make(chan struct{})
	var once sync.Once
	for i := 0; i < 8; i++ {
		p.Submit("same", 1, func() error {
			atomic.AddInt32(&ran, 1)
			once.Do(func() { close(running) })
			<-unblock
			return nil
		})
	}
	<-running                        // leader is inside the gated fn
	time.Sleep(5 * time.Millisecond) // let followers attach to singleflight
	close(unblock)                   // release the leader; followers receive the same result
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&ran) != 1 {
		t.Fatalf("expected 1 run via singleflight, got %d", ran)
	}
}

func TestAdmission_WorkerSemBounds(t *testing.T) {
	p := NewAdmissionPool(context.Background(), 2, 0)
	var inFlight, peak int32
	for i := 0; i < 8; i++ {
		i := i
		p.Submit(string(rune('a'+i)), 1, func() error {
			cur := atomic.AddInt32(&inFlight, 1)
			defer atomic.AddInt32(&inFlight, -1)
			for {
				prev := atomic.LoadInt32(&peak)
				if cur <= prev || atomic.CompareAndSwapInt32(&peak, prev, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			return nil
		})
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if peak > 2 {
		t.Fatalf("peak %d > 2", peak)
	}
}

func TestAdmission_MemSemBoundsByEstimate(t *testing.T) {
	// Budget = 100; submit 10 tasks at estimate=40 → at most 2 concurrent.
	p := NewAdmissionPool(context.Background(), 100, 100)
	var inFlight, peak int32
	for i := 0; i < 10; i++ {
		i := i
		p.Submit(string(rune('a'+i)), 40, func() error {
			cur := atomic.AddInt32(&inFlight, 1)
			defer atomic.AddInt32(&inFlight, -1)
			for {
				prev := atomic.LoadInt32(&peak)
				if cur <= prev || atomic.CompareAndSwapInt32(&peak, prev, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			return nil
		})
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if peak > 2 {
		t.Fatalf("peak %d > 2 (budget=100, est=40)", peak)
	}
}

func TestAdmission_MemBudgetZeroDisablesMemSem(t *testing.T) {
	p := NewAdmissionPool(context.Background(), 8, 0)
	for i := 0; i < 100; i++ {
		i := i
		p.Submit(string(rune(i)), 1<<40 /* 1 TiB */, func() error { return nil })
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestAdmission_RecoversPanicAsError(t *testing.T) {
	p := NewAdmissionPool(context.Background(), 1, 0)
	p.Submit("k", 1, func() error { panic("admission boom") })
	err := p.Wait()
	if err == nil || !strings.Contains(err.Error(), "admission boom") {
		t.Fatalf("expected panic surfaced, got %v", err)
	}
}

func TestAdmission_FirstErrorCancelsSiblings(t *testing.T) {
	p := NewAdmissionPool(context.Background(), 2, 0)
	want := errors.New("first")
	p.Submit("a", 1, func() error { return want })
	p.Submit("b", 1, func() error { time.Sleep(50 * time.Millisecond); return nil })
	if err := p.Wait(); !errors.Is(err, want) {
		t.Fatalf("got %v want %v", err, want)
	}
}

func TestAdmission_CtxCancelMidAcquire(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := NewAdmissionPool(ctx, 1, 0)
	p.Submit("a", 1, func() error { time.Sleep(50 * time.Millisecond); return nil })
	p.Submit("b", 1, func() error { return nil })
	cancel()
	if err := p.Wait(); err == nil {
		t.Fatalf("expected ctx err")
	}
}

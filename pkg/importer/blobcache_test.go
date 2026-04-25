package importer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/opencontainers/go-digest"
)

func TestBlobCache_FirstFetchMisses_SecondHits(t *testing.T) {
	c := newBaselineBlobCache()
	d := digest.Digest("sha256:" + strings.Repeat("a", 64))
	want := []byte("hello")

	var calls atomic.Int64
	fetch := func() ([]byte, error) {
		calls.Add(1)
		return want, nil
	}

	got1, err := c.GetOrLoad(context.Background(), d, fetch)
	if err != nil {
		t.Fatalf("first GetOrLoad: %v", err)
	}
	if string(got1) != "hello" {
		t.Fatalf("first GetOrLoad bytes: got %q want %q", got1, "hello")
	}
	got2, err := c.GetOrLoad(context.Background(), d, fetch)
	if err != nil {
		t.Fatalf("second GetOrLoad: %v", err)
	}
	if string(got2) != "hello" {
		t.Fatalf("second GetOrLoad bytes: got %q want %q", got2, "hello")
	}
	if calls.Load() != 1 {
		t.Fatalf("fetch invoked %d times across two calls, want 1", calls.Load())
	}
}

func TestBlobCache_ConcurrentMissesCollapseToOneFetch(t *testing.T) {
	c := newBaselineBlobCache()
	d := digest.Digest("sha256:" + strings.Repeat("b", 64))

	var calls atomic.Int64
	gate := make(chan struct{})
	fetch := func() ([]byte, error) {
		<-gate
		calls.Add(1)
		return []byte("x"), nil
	}

	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = c.GetOrLoad(context.Background(), d, fetch)
		}()
	}
	close(gate)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("fetch invoked %d times under singleflight, want 1", calls.Load())
	}
}

func TestBlobCache_ConcurrentDistinctDigests(t *testing.T) {
	c := newBaselineBlobCache()
	const N = 10
	digests := make([]digest.Digest, N)
	for i := 0; i < N; i++ {
		digests[i] = digest.Digest("sha256:" + strings.Repeat("0123456789abcdef"[i:i+1], 64))
	}

	var calls atomic.Int64
	fetch := func(d digest.Digest) func() ([]byte, error) {
		return func() ([]byte, error) {
			calls.Add(1)
			return []byte(d.Encoded()[:8]), nil
		}
	}

	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			b, err := c.GetOrLoad(context.Background(), digests[i], fetch(digests[i]))
			if err != nil {
				t.Errorf("digest %d: %v", i, err)
				return
			}
			if string(b) != digests[i].Encoded()[:8] {
				t.Errorf("digest %d: bytes %q, want %q", i, b, digests[i].Encoded()[:8])
			}
		}()
	}
	wg.Wait()
	if calls.Load() != N {
		t.Fatalf("fetch invoked %d times for %d distinct digests, want %d", calls.Load(), N, N)
	}
}

func TestBlobCache_FetchErrorNotCached(t *testing.T) {
	c := newBaselineBlobCache()
	d := digest.Digest("sha256:" + strings.Repeat("c", 64))
	want := errors.New("transient")

	var calls atomic.Int64
	fetch := func() ([]byte, error) {
		n := calls.Add(1)
		if n == 1 {
			return nil, want
		}
		return []byte("ok"), nil
	}

	if _, err := c.GetOrLoad(context.Background(), d, fetch); !errors.Is(err, want) {
		t.Fatalf("first call: got %v, want %v", err, want)
	}
	got, err := c.GetOrLoad(context.Background(), d, fetch)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if string(got) != "ok" {
		t.Fatalf("second call bytes: got %q, want %q", got, "ok")
	}
	if calls.Load() != 2 {
		t.Fatalf("fetch invoked %d times, want 2 (no error caching)", calls.Load())
	}
}

func TestBlobCache_FetchErrorOnConcurrentMissReturnsToAllWaiters(t *testing.T) {
	// Property under test: when concurrent callers miss the cache and
	// the fetch errors, every caller sees the error. We deliberately do
	// NOT assert on the underlying fetch invocation count: errors are
	// not cached (per spec), so callers arriving after a failed flight
	// returns correctly start a fresh flight, and there is no
	// observable hook to prove all N goroutines reached sf.Do before
	// the first flight completes. Concurrent collapse on the success
	// path is covered by ConcurrentMissesCollapseToOneFetch.
	c := newBaselineBlobCache()
	d := digest.Digest("sha256:" + strings.Repeat("d", 64))
	want := errors.New("upstream-down")

	failingFetch := func() ([]byte, error) { return nil, want }

	const N = 32
	errs := make([]error, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, errs[i] = c.GetOrLoad(context.Background(), d, failingFetch)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if !errors.Is(err, want) {
			t.Fatalf("waiter %d got err %v, want %v", i, err, want)
		}
	}

	// After concurrent failures resolve, a subsequent call with a
	// working fetch must succeed — proving errors aren't cached even
	// when many callers raced through them.
	got, err := c.GetOrLoad(context.Background(), d, func() ([]byte, error) {
		return []byte("recovered"), nil
	})
	if err != nil {
		t.Fatalf("post-failure call: %v", err)
	}
	if string(got) != "recovered" {
		t.Fatalf("post-failure bytes: got %q, want %q", got, "recovered")
	}
}

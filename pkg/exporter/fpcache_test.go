package exporter

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/opencontainers/go-digest"
)

// stubFP is a deterministic Fingerprinter for fpCache tests. It returns
// the embedded Fingerprint regardless of input and never errors, so the
// cache's behavior — not the fingerprinter's — drives the assertions.
type stubFP struct{ d Fingerprint }

func (s stubFP) Fingerprint(_ context.Context, _ string, _ []byte) (Fingerprint, error) {
	return s.d, nil
}

func TestFpCache_HitReturnsBytesAndFingerprint(t *testing.T) {
	c := newFpCache()
	d := digest.Digest("sha256:" + strings.Repeat("a", 64))
	want := []byte("hello")

	var calls atomic.Int64
	fetch := func(_ digest.Digest) ([]byte, error) {
		calls.Add(1)
		return want, nil
	}
	fp := stubFP{d: Fingerprint{"k": 5}}

	gotFp, gotBytes, err := c.GetOrLoad(context.Background(),
		BaselineLayerMeta{Digest: d, Size: 5, MediaType: "x"}, fetch, fp)
	if err != nil {
		t.Fatalf("get1: %v", err)
	}
	if string(gotBytes) != "hello" || gotFp == nil {
		t.Fatalf("first call wrong: bytes=%q fp=%v", gotBytes, gotFp)
	}
	// Second call should not invoke fetch.
	_, _, err = c.GetOrLoad(context.Background(),
		BaselineLayerMeta{Digest: d, Size: 5, MediaType: "x"}, fetch, fp)
	if err != nil {
		t.Fatalf("get2: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("fetch called %d times, want 1", calls.Load())
	}
}

func TestFpCache_ConcurrentMissCollapses(t *testing.T) {
	c := newFpCache()
	d := digest.Digest("sha256:" + strings.Repeat("b", 64))

	var calls atomic.Int64
	fetch := func(_ digest.Digest) ([]byte, error) {
		calls.Add(1)
		return []byte("x"), nil
	}
	fp := stubFP{d: Fingerprint{"k": 1}}

	const N = 16
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = c.GetOrLoad(context.Background(),
				BaselineLayerMeta{Digest: d, Size: 1, MediaType: "x"}, fetch, fp)
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("fetch called %d times under singleflight, want 1", calls.Load())
	}
}

func TestFpCache_FetchErrorDoesNotPoison(t *testing.T) {
	c := newFpCache()
	d := digest.Digest("sha256:" + strings.Repeat("c", 64))
	want := errors.New("transient")

	var calls atomic.Int64
	fetch := func(_ digest.Digest) ([]byte, error) {
		n := calls.Add(1)
		if n == 1 {
			return nil, want
		}
		return []byte("ok"), nil
	}
	fp := stubFP{d: Fingerprint{"k": 1}}

	if _, _, err := c.GetOrLoad(context.Background(),
		BaselineLayerMeta{Digest: d, Size: 1, MediaType: "x"}, fetch, fp); !errors.Is(err, want) {
		t.Fatalf("first call: got %v, want %v", err, want)
	}
	// Retry should re-call fetch.
	if _, _, err := c.GetOrLoad(context.Background(),
		BaselineLayerMeta{Digest: d, Size: 1, MediaType: "x"}, fetch, fp); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("fetch called %d times, want 2 (no poisoning)", calls.Load())
	}
}

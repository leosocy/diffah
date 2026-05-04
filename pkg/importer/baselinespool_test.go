package importer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

// errAfterNReader returns the first n bytes of src then errors. Used to
// simulate a mid-stream fetch failure so we can verify partial-spool
// cleanup.
type errAfterNReader struct {
	src []byte
	n   int
	pos int
}

func (r *errAfterNReader) Read(p []byte) (int, error) {
	if r.pos >= r.n {
		return 0, errors.New("simulated mid-stream failure")
	}
	remaining := r.n - r.pos
	if len(p) > remaining {
		p = p[:remaining]
	}
	if r.pos+len(p) > len(r.src) {
		p = p[:len(r.src)-r.pos]
	}
	copy(p, r.src[r.pos:r.pos+len(p)])
	r.pos += len(p)
	return len(p), nil
}

func newSpool(t *testing.T) *BaselineSpool {
	t.Helper()
	dir := t.TempDir()
	return NewBaselineSpool(dir)
}

// TestBaselineSpool_GetOrSpool_StoresFullPayload verifies the basic
// round-trip: a fetched payload is written to <dir>/<digest> and the
// path is returned for subsequent lookups.
func TestBaselineSpool_GetOrSpool_StoresFullPayload(t *testing.T) {
	s := newSpool(t)
	payload := []byte("hello-baseline-blob-payload")
	d := digest.FromBytes(payload)

	path, err := s.GetOrSpool(context.Background(), d, func() (io.ReadCloser, error) {
		return io.NopCloser(bytesReader(payload)), nil
	})
	if err != nil {
		t.Fatalf("GetOrSpool: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spooled file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("spooled bytes mismatch: got %q want %q", got, payload)
	}
	// Second lookup must hit the in-memory entries map without re-fetching.
	var calls atomic.Int32
	path2, err := s.GetOrSpool(context.Background(), d, func() (io.ReadCloser, error) {
		calls.Add(1)
		return nil, errors.New("should not be called")
	})
	if err != nil {
		t.Fatalf("second GetOrSpool: %v", err)
	}
	if path2 != path {
		t.Fatalf("path changed across lookups: got %q want %q", path2, path)
	}
	if calls.Load() != 0 {
		t.Fatalf("fetch invoked on cache hit: %d", calls.Load())
	}
}

// TestBaselineSpool_DrainsAfterPartialConsumer ensures spoolOnce drains
// the TeeReader even when the verifier consumes only a prefix — the
// on-disk file must still be byte-identical to the source payload.
func TestBaselineSpool_DrainsAfterPartialConsumer(t *testing.T) {
	s := newSpool(t)
	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	d := digest.FromBytes(payload)

	verifier := func(r io.Reader) error {
		buf := make([]byte, 16)
		_, err := io.ReadFull(r, buf)
		return err
	}
	path, err := s.getOrSpoolWithVerifier(context.Background(), d, func() (io.ReadCloser, error) {
		return io.NopCloser(bytesReader(payload)), nil
	}, verifier)
	if err != nil {
		t.Fatalf("getOrSpoolWithVerifier: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spool: %v", err)
	}
	if len(got) != len(payload) {
		t.Fatalf("spool size mismatch: got %d want %d", len(got), len(payload))
	}
	for i := range payload {
		if got[i] != payload[i] {
			t.Fatalf("spool byte %d mismatch: got %d want %d", i, got[i], payload[i])
		}
	}
}

// TestBaselineSpool_FetchErrorRemovesPartialFile drives an
// errAfterNReader so the fetch produces partial content, then errors;
// the spool must remove the partial file rather than leave it behind.
func TestBaselineSpool_FetchErrorRemovesPartialFile(t *testing.T) {
	s := newSpool(t)
	payload := make([]byte, 1024)
	for i := range payload {
		payload[i] = byte(i)
	}
	d := digest.FromBytes(payload)

	_, err := s.GetOrSpool(context.Background(), d, func() (io.ReadCloser, error) {
		return io.NopCloser(&errAfterNReader{src: payload, n: 256}), nil
	})
	if err == nil {
		t.Fatal("expected error from mid-stream fetch failure")
	}
	// The spool file must NOT exist after the failed flight.
	expected := s.pathFor(d)
	if _, statErr := os.Stat(expected); !os.IsNotExist(statErr) {
		t.Fatalf("partial spool file should have been removed, stat err=%v", statErr)
	}
	// The entry map must NOT have recorded it.
	if p, ok := s.Path(d); ok {
		t.Fatalf("entries map should not contain failed digest, got path %q", p)
	}
}

// TestBaselineSpool_ConcurrentSameDigestDistinctPayloadAtomicRename
// drives 8 goroutines DIRECTLY into spoolOnce (bypassing the fast-path
// lookup AND singleflight) so they all race on the same dst path.
// 7 carry payloads whose contents do NOT match the claimed digest and
// must be rejected at the digest check (committed=false → tmp removed).
// 1 carries the matching payload and is published via tmp+rename.
//
// The on-disk dst MUST equal that one matching payload byte-for-byte.
// If spoolOnce is mutated to stream directly into dst (skipping
// CreateTemp+rename), bytes from the 8 concurrent streams interleave on
// dst and even the matching goroutine's digest check fails — every
// goroutine errors and dst holds garbage (or doesn't exist).
//
// This test exercises the rename-atomicity contract; see Step 3.9.
func TestBaselineSpool_ConcurrentSameDigestDistinctPayloadAtomicRename(t *testing.T) {
	s := newSpool(t)
	const N = 8
	const sz = 1 << 20

	// We synthesize the digest from the FIRST payload so exactly one
	// goroutine (index 0) passes the digest gate; the other 7 are
	// rejected. The mutation breaks atomicity for both groups.
	first := make([]byte, sz)
	for i := range first {
		first[i] = byte(i % 251)
	}
	d := digest.FromBytes(first)

	payloads := make([][]byte, N)
	payloads[0] = first
	for i := 1; i < N; i++ {
		payloads[i] = make([]byte, sz)
		for j := range payloads[i] {
			payloads[i][j] = byte(i)
		}
	}

	gate := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-gate
			// Bypass singleflight: spoolOnce is the under-singleflight worker;
			// calling it directly produces N concurrent writers competing on
			// the same dst path, which is the only way to observe rename
			// atomicity in a unit test.
			_, _ = s.spoolOnce(context.Background(), d, func() (io.ReadCloser, error) {
				return io.NopCloser(bytesReader(payloads[i])), nil
			}, nil)
		}()
	}
	close(gate)
	wg.Wait()

	// On-disk dst must match exactly the one matching payload byte-for-byte.
	got, err := os.ReadFile(s.pathFor(d))
	if err != nil {
		t.Fatalf("post-race spool readback: %v (atomicity violated — dst absent/incomplete)", err)
	}
	if !bytes.Equal(got, first) {
		t.Fatalf("on-disk spool is not byte-identical to the matching payload (interleaved write?)")
	}
}

// TestBaselineSpool_SingleflightDedupsSameDigest verifies the
// singleflight collapse: 16 concurrent GetOrSpool calls for the same
// digest must result in exactly one underlying fetch invocation.
func TestBaselineSpool_SingleflightDedupsSameDigest(t *testing.T) {
	s := newSpool(t)
	payload := make([]byte, 64<<10)
	for i := range payload {
		payload[i] = byte(i % 13)
	}
	d := digest.FromBytes(payload)

	var calls atomic.Int32
	gate := make(chan struct{})
	const N = 16
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			<-gate
			_, _ = s.GetOrSpool(context.Background(), d, func() (io.ReadCloser, error) {
				calls.Add(1)
				return io.NopCloser(bytesReader(payload)), nil
			})
		}()
	}
	close(gate)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("fetch invoked %d times under singleflight, want 1", calls.Load())
	}
}

// bytesReader is a tiny helper to avoid pulling bytes.Reader into every
// fetch closure.
func bytesReader(b []byte) io.Reader { return &slowBytesReader{b: b} }

// slowBytesReader is a one-byte-per-Read wrapper that exposes interleave
// risk: a buggy spool that writes directly to dst instead of via tmp+rename
// would race per-byte across goroutines and produce a mixed file.
type slowBytesReader struct {
	b   []byte
	pos int
}

func (r *slowBytesReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.b[r.pos]
	r.pos++
	return 1, nil
}

// Compile-time guard that the test file uses the diff sentinel through
// at least one path. Keeps the import live even if we restructure tests.
var _ = (*diff.ErrBaselineBlobDigestMismatch)(nil)

// Package importer — disk-backed baseline spool.
//
// BaselineSpool replaces baselineBlobCache: instead of pinning every
// baseline blob as []byte for the full Import() lifetime, it streams
// each blob to <dir>/<digest> on first touch via TeeReader while
// digest-verifying it on the same pass. Subsequent calls for the same
// digest hit the in-memory entries map (RLock fast path) and return
// the already-written path.
//
// Concurrent first-touches for the same digest are collapsed to a
// single underlying fetch via singleflight.Group (mirrors the old
// blob cache's behaviour). Failed fetches do not cache: the next
// caller retries.
//
// Mirrors pkg/exporter/baselinespool.go in shape and the singleflight /
// drain / committed-sentinel pattern. Importer additionally publishes via
// tmp + atomic rename (CreateTemp → Sync → Close → Rename) for defense-
// in-depth: even if a future caller bypasses singleflight (as the atomic-
// rename test does directly), concurrent writers race on distinct tmp
// files and only the digest-matching writer's payload reaches dst. The
// exporter does not need this because all of its writers go through the
// singleflight gate.
package importer

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/opencontainers/go-digest"
	"golang.org/x/sync/singleflight"

	"github.com/leosocy/diffah/pkg/diff"
)

// BaselineSpool manages spooled baseline blobs under dir. Exported so
// importer.Import() can construct one inside the workdir and pass it
// to the per-image bundleImageSource.
type BaselineSpool struct {
	dir     string
	mu      sync.RWMutex
	entries map[digest.Digest]string
	sf      singleflight.Group
}

// NewBaselineSpool creates a spool backed by dir. The directory must
// already exist; importer.Import() creates it via internal/workdir.Ensure
// before constructing the spool.
func NewBaselineSpool(dir string) *BaselineSpool {
	return &BaselineSpool{
		dir:     dir,
		entries: make(map[digest.Digest]string),
	}
}

// pathFor returns the canonical on-disk path for digest d. Lowercase so
// only the package and tests can call it directly.
func (s *BaselineSpool) pathFor(d digest.Digest) string {
	return filepath.Join(s.dir, d.Encoded())
}

// Path returns the spooled file path for d if a previous GetOrSpool
// has already committed it.
func (s *BaselineSpool) Path(d digest.Digest) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.entries[d]
	return p, ok
}

// GetOrSpool returns the on-disk path for d, spooling the blob on the
// first call. On cache hit (fast path) it returns immediately under
// RLock. On cache miss, one goroutine wins the singleflight and streams
// the blob to disk while digest-verifying it; all other concurrent
// callers wait and share the result. Fetch or verify errors are not
// cached — the next caller retries.
func (s *BaselineSpool) GetOrSpool(
	ctx context.Context,
	d digest.Digest,
	fetch func() (io.ReadCloser, error),
) (string, error) {
	return s.getOrSpoolWithVerifier(ctx, d, fetch, nil)
}

// getOrSpoolWithVerifier is GetOrSpool with an optional verifier hook
// invoked on the streaming TeeReader before the rest of the payload is
// drained. Used by tests to drive the partial-consumer drain path; the
// production callers pass nil.
func (s *BaselineSpool) getOrSpoolWithVerifier(
	ctx context.Context,
	d digest.Digest,
	fetch func() (io.ReadCloser, error),
	verifier func(io.Reader) error,
) (string, error) {
	if p, ok := s.Path(d); ok {
		return p, nil
	}
	v, err, _ := s.sf.Do(string(d), func() (any, error) {
		// Re-check inside the singleflight: another goroutine may have
		// just committed under a previously-finished flight.
		if p, ok := s.Path(d); ok {
			return p, nil
		}
		return s.spoolOnce(ctx, d, fetch, verifier)
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// spoolOnce performs a single fetch+stream+verify+rename cycle for
// digest d. Called at most once per digest per Import() run for any
// successful flight; failed flights leave nothing behind and let the
// next caller retry.
func (s *BaselineSpool) spoolOnce(
	ctx context.Context,
	d digest.Digest,
	fetch func() (io.ReadCloser, error),
	verifier func(io.Reader) error,
) (string, error) {
	rc, err := fetch()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	dst := s.pathFor(d)
	tmp, err := os.CreateTemp(s.dir, d.Encoded()+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create spool tmp for %s: %w", d, err)
	}
	tmpPath := tmp.Name()

	// committed gates the deferred cleanup so any failure before atomic
	// rename + entry recording leaves no partial file behind.
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	tee := io.TeeReader(rc, tmp)
	if verifier != nil {
		if vErr := verifier(tee); vErr != nil {
			return "", vErr
		}
	}
	// Drain whatever the verifier (or any future inline consumer) left
	// unconsumed so the on-disk file is byte-identical to rc — mirrors
	// the exporter spool's contract.
	if _, drainErr := io.Copy(io.Discard, tee); drainErr != nil {
		return "", fmt.Errorf("drain spool tmp for %s: %w", d, drainErr)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("sync spool tmp for %s: %w", d, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close spool tmp for %s: %w", d, err)
	}

	got, err := digestFile(tmpPath)
	if err != nil {
		return "", fmt.Errorf("digest spool tmp for %s: %w", d, err)
	}
	if got != d {
		return "", &diff.ErrBaselineBlobDigestMismatch{Digest: d.String(), Got: got.String()}
	}

	// Atomic publish: same-directory rename is atomic on POSIX, so a
	// concurrent reader observing dst will always see a fully-formed
	// payload, never a half-written tmp.
	if err := os.Rename(tmpPath, dst); err != nil {
		return "", fmt.Errorf("rename spool tmp for %s: %w", d, err)
	}

	s.mu.Lock()
	s.entries[d] = dst
	s.mu.Unlock()
	committed = true

	_ = ctx // ctx kept on the signature for future cancellation propagation
	return dst, nil
}

// digestFile re-reads p and returns its digest. Used to verify the
// on-disk spool after streaming finishes.
func digestFile(p string) (digest.Digest, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return digest.FromReader(f)
}

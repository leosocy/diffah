// Package exporter — disk-backed baseline spool.
//
// baselineSpool replaces fpCache: instead of pinning every baseline layer
// as []byte for the full Export() lifetime, it streams each layer to
// <dir>/<digest> on first touch via TeeReader while fingerprinting it on
// the same pass. Subsequent calls for the same digest hit the in-memory
// entries map (RLock fast path) and return the already-written path.
//
// Concurrent first-touches for the same digest are collapsed to a single
// underlying fetch via singleflight.Group — mirrors fpCache's behaviour.
// Failed fetches do not cache (spec §5.2 resilience contract): the next
// caller retries.
package exporter

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/opencontainers/go-digest"
	"golang.org/x/sync/singleflight"
)

// baselineEntry holds the on-disk path and (possibly nil) fingerprint for
// one baseline layer digest.
type baselineEntry struct {
	// Path is the absolute path of the spool file — byte-identical to the
	// source blob as fetched from the registry.
	Path string
	// Fingerprint is nil when fingerprinting failed (sentinel). Planners
	// fall back to size-only ranking when it is nil.
	Fingerprint Fingerprint
}

// baselineSpool manages spooled baseline blobs under dir.
type baselineSpool struct {
	dir     string
	mu      sync.RWMutex
	entries map[digest.Digest]baselineEntry
	sf      singleflight.Group
}

// newBaselineSpool creates a spool backed by dir. The directory must already
// exist (ensureWorkdir creates it before Export reaches encodeShipped).
func newBaselineSpool(dir string) *baselineSpool {
	return &baselineSpool{
		dir:     dir,
		entries: make(map[digest.Digest]baselineEntry),
	}
}

// Path returns the expected spool file path for digest d.
func (s *baselineSpool) Path(d digest.Digest) string {
	return filepath.Join(s.dir, d.Encoded())
}

// GetOrSpool returns the baselineEntry for meta.Digest, spooling the blob
// on the first call. On cache hit (fast path) it returns immediately under
// RLock. On cache miss, one goroutine wins the singleflight and streams the
// blob to disk via TeeReader; all other concurrent callers wait and share
// the result. Fetch errors are not cached — the next caller retries.
func (s *baselineSpool) GetOrSpool(
	ctx context.Context,
	meta BaselineLayerMeta,
	fetch func(digest.Digest) (io.ReadCloser, error),
	fp Fingerprinter,
) (baselineEntry, error) {
	if e, ok := s.lookup(meta.Digest); ok {
		return e, nil
	}
	v, err, _ := s.sf.Do(string(meta.Digest), func() (any, error) {
		// Re-check after winning the singleflight: another goroutine may
		// have just committed the entry under a previously-finished flight.
		if e, ok := s.lookup(meta.Digest); ok {
			return e, nil
		}
		return s.spoolOnce(ctx, meta, fetch, fp)
	})
	if err != nil {
		return baselineEntry{}, err
	}
	return v.(baselineEntry), nil
}

// lookup is the hot-path RLock read against the entries map.
func (s *baselineSpool) lookup(d digest.Digest) (baselineEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[d]
	return e, ok
}

// spoolOnce fetches the blob and streams it to <dir>/<digest> while
// fingerprinting it on the same pass via TeeReader. It is called at most
// once per digest per Export() run.
func (s *baselineSpool) spoolOnce(
	ctx context.Context,
	meta BaselineLayerMeta,
	fetch func(digest.Digest) (io.ReadCloser, error),
	fp Fingerprinter,
) (baselineEntry, error) {
	path := s.Path(meta.Digest)

	rc, err := fetch(meta.Digest)
	if err != nil {
		return baselineEntry{}, fmt.Errorf("fetch baseline %s: %w", meta.Digest, err)
	}
	defer rc.Close()

	return s.streamToSpool(ctx, path, meta, rc, fp)
}

// streamToSpool creates the spool file at path, copies rc through a
// TeeReader into it while running FingerprintReader on the tee, then
// drains any remaining bytes the fingerprinter left unconsumed.
// Partial files are removed on any error path.
func (s *baselineSpool) streamToSpool(
	ctx context.Context,
	path string,
	meta BaselineLayerMeta,
	rc io.Reader,
	fp Fingerprinter,
) (baselineEntry, error) {
	f, err := os.Create(path)
	if err != nil {
		return baselineEntry{}, fmt.Errorf("create spool file %s: %w", path, err)
	}

	// committed gates the deferred cleanup: only set true after the entry
	// is safely stored in s.entries so a partial file is always removed on
	// any failure before that point.
	committed := false
	defer func() {
		if !committed {
			_ = f.Close()
			_ = os.Remove(path)
		}
	}()

	tee := io.TeeReader(rc, f)

	fpVal, fpErr := fp.FingerprintReader(ctx, meta.MediaType, tee)
	// Always drain the tee so the spool file ends up byte-identical to rc,
	// regardless of whether the fingerprinter consumed the entire stream.
	// Decompressors stop at logical EOF (gzip first member, tar EOF marker)
	// and leave trailing bytes that TeeReader would otherwise miss.
	if _, drainErr := io.Copy(io.Discard, tee); drainErr != nil {
		return baselineEntry{}, fmt.Errorf("drain spool %s: %w", path, drainErr)
	}
	if fpErr != nil {
		// Fingerprint failures are non-fatal (mirrors fpCache contract):
		// the planner falls back to size-only ranking when entry.Fingerprint
		// is nil.
		fpVal = nil
	}

	if err := f.Sync(); err != nil {
		return baselineEntry{}, fmt.Errorf("sync spool file %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return baselineEntry{}, fmt.Errorf("close spool file %s: %w", path, err)
	}

	entry := baselineEntry{Path: path, Fingerprint: fpVal}
	s.mu.Lock()
	s.entries[meta.Digest] = entry
	s.mu.Unlock()
	committed = true

	return entry, nil
}

// SnapshotFingerprints returns a copy of the digest → Fingerprint map for
// every baseline GetOrSpool has fingerprinted so far. Encoders use this to
// seed each per-pair Planner so the same digest is not re-fingerprinted
// across pairs (spec §4.2). A nil-valued entry means "fingerprint failed
// for this baseline"; callers preserve that sentinel rather than retry.
func (s *baselineSpool) SnapshotFingerprints() map[digest.Digest]Fingerprint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[digest.Digest]Fingerprint, len(s.entries))
	for d, e := range s.entries {
		out[d] = e.Fingerprint
	}
	return out
}

package exporter

import (
	"context"
	"sync"

	"github.com/opencontainers/go-digest"
	"golang.org/x/sync/singleflight"
)

// fpCache memoizes baseline layer fingerprints AND raw bytes across
// pairs in a single Export() call. Concurrent misses on the same
// digest collapse to one underlying fetch via singleflight.Group;
// fetch errors are returned to all waiters but do NOT store a cache
// entry — the next caller retries.
type fpCache struct {
	mu    sync.RWMutex
	fps   map[digest.Digest]Fingerprint // nil entry = "fingerprint failed but bytes loaded"
	bytes map[digest.Digest][]byte
	sf    singleflight.Group
}

func newFpCache() *fpCache {
	return &fpCache{
		fps:   make(map[digest.Digest]Fingerprint),
		bytes: make(map[digest.Digest][]byte),
	}
}

// GetOrLoad returns the fingerprint and raw bytes for meta.Digest. On
// cache miss it invokes fetch exactly once even under concurrent
// callers; on fetch error nothing is cached and err is returned.
func (c *fpCache) GetOrLoad(
	ctx context.Context,
	meta BaselineLayerMeta,
	fetch func(digest.Digest) ([]byte, error),
	fp Fingerprinter,
) (Fingerprint, []byte, error) {
	if b, ok := c.lookupBytes(meta.Digest); ok {
		return c.lookupFp(meta.Digest), b, nil
	}
	v, err, _ := c.sf.Do(string(meta.Digest), func() (any, error) {
		// Re-check after winning the singleflight (another caller may
		// have populated under a previously-finished singleflight).
		if b, ok := c.lookupBytes(meta.Digest); ok {
			return cacheValue{fp: c.lookupFp(meta.Digest), bytes: b}, nil
		}
		blob, err := fetch(meta.Digest)
		if err != nil {
			return nil, err
		}
		f, _ := fp.Fingerprint(ctx, meta.MediaType, blob) // err -> nil fp (sentinel)
		c.mu.Lock()
		c.bytes[meta.Digest] = blob
		c.fps[meta.Digest] = f
		c.mu.Unlock()
		return cacheValue{fp: f, bytes: blob}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	cv := v.(cacheValue)
	return cv.fp, cv.bytes, nil
}

type cacheValue struct {
	fp    Fingerprint
	bytes []byte
}

func (c *fpCache) lookupBytes(d digest.Digest) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	b, ok := c.bytes[d]
	return b, ok
}

func (c *fpCache) lookupFp(d digest.Digest) Fingerprint {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.fps[d]
}

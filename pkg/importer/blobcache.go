package importer

import (
	"context"
	"sync"

	"github.com/opencontainers/go-digest"
	"golang.org/x/sync/singleflight"
)

// baselineBlobCache memoizes verified baseline blob bytes across all
// images in a single Import() call. Concurrent misses on the same
// digest collapse to one underlying fetch via singleflight; fetch or
// verify errors are NOT cached — the next caller retries.
type baselineBlobCache struct {
	mu    sync.RWMutex
	bytes map[digest.Digest][]byte
	sf    singleflight.Group
}

func newBaselineBlobCache() *baselineBlobCache {
	return &baselineBlobCache{bytes: make(map[digest.Digest][]byte)}
}

// GetOrLoad returns verified bytes for d. On cache miss it calls fetch
// exactly once even under concurrent callers; on fetch error nothing
// is cached.
//
// The context parameter is kept on the signature for symmetry with
// pkg/exporter/fpCache.GetOrLoad and to allow future cancellation
// propagation into fetch; today the singleflight closure does not
// consult it, hence the blank name.
func (c *baselineBlobCache) GetOrLoad(
	_ context.Context, d digest.Digest, fetch func() ([]byte, error),
) ([]byte, error) {
	if b, ok := c.lookup(d); ok {
		return b, nil
	}
	v, err, _ := c.sf.Do(string(d), func() (any, error) {
		if b, ok := c.lookup(d); ok {
			return b, nil
		}
		data, err := fetch()
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.bytes[d] = data
		c.mu.Unlock()
		return data, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

func (c *baselineBlobCache) lookup(d digest.Digest) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	b, ok := c.bytes[d]
	return b, ok
}

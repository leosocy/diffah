package exporter

// blobPool tracks digest → on-disk spill path + sidecar metadata. It is
// content-addressed and first-write-wins on collision. Each blob is written
// to <dir>/<digest.Encoded()> at addEntryIfAbsent time; the writer streams
// them into the bundle tar via io.Copy. Spec §5.6 §5.7.
//
// Concurrency contract: all mutations and concurrent reads use p.mu.
// Writer / assembler / sidecar phases read spills and entries WITHOUT
// the lock — this is safe because Export() serializes phases via the
// encoder worker pool's Wait() in encode.go, which establishes a
// happens-before edge for every prior map mutation. Anyone adding a
// background goroutine that touches the pool after the writer phase
// starts must take p.mu.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

type blobPool struct {
	mu       sync.RWMutex
	dir      string
	spills   map[digest.Digest]string // digest → absolute spill file path
	entries  map[digest.Digest]diff.BlobEntry
	shipRefs map[digest.Digest]int
}

func newBlobPool(dir string) *blobPool {
	return &blobPool{
		dir:      dir,
		spills:   make(map[digest.Digest]string),
		entries:  make(map[digest.Digest]diff.BlobEntry),
		shipRefs: make(map[digest.Digest]int),
	}
}

// addEntryIfAbsent spills payload to disk and registers d in the pool.
// Writes happen outside the mutex; a second writer for the same digest
// is a no-op (first-write-wins). The function is safe to call concurrently.
// Callers MUST guarantee that all calls with the same d pass byte-identical
// payload — the pool is content-addressed, so this is trivially satisfied
// when d = digest.FromBytes(payload). Without this guarantee, the lost-race
// branch could leave a non-canonical file at <dir>/<d.Encoded()>.
func (p *blobPool) addEntryIfAbsent(d digest.Digest, payload []byte, e diff.BlobEntry) error {
	p.mu.RLock()
	_, exists := p.spills[d]
	p.mu.RUnlock()
	if exists {
		return nil
	}

	path := filepath.Join(p.dir, d.Encoded())
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return fmt.Errorf("spill blob %s: %w", d, err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.spills[d]; exists {
		// Lost the race; another goroutine wrote a byte-identical file
		// (content-addressed). Our spill file remains on disk, harmless.
		// The first writer's path stays canonical.
		return nil
	}
	p.spills[d] = path
	p.entries[d] = e
	return nil
}

func (p *blobPool) has(d digest.Digest) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.spills[d]
	return ok
}

func (p *blobPool) countShipped(d digest.Digest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shipRefs[d]++
}

func (p *blobPool) refCount(d digest.Digest) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.shipRefs[d]
}

func seedManifestAndConfig(p *blobPool, plan *pairPlan) error {
	mfDigest := digest.FromBytes(plan.TargetManifest)
	if err := p.addEntryIfAbsent(mfDigest, plan.TargetManifest, diff.BlobEntry{
		Size: int64(len(plan.TargetManifest)), MediaType: plan.TargetMediaType,
		Encoding: diff.EncodingFull, ArchiveSize: int64(len(plan.TargetManifest)),
	}); err != nil {
		return err
	}
	return p.addEntryIfAbsent(plan.TargetConfigDesc.Digest, plan.TargetConfigRaw, diff.BlobEntry{
		Size: plan.TargetConfigDesc.Size, MediaType: plan.TargetConfigDesc.MediaType,
		Encoding: diff.EncodingFull, ArchiveSize: plan.TargetConfigDesc.Size,
	})
}

func (p *blobPool) sortedDigests() []digest.Digest {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]digest.Digest, 0, len(p.spills))
	for d := range p.spills {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

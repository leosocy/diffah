package exporter

import (
	"context"
	"fmt"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/internal/zstdpatch"
	"github.com/leosocy/diffah/pkg/diff"
)

// CodecZstdPatch is the canonical codec tag persisted in sidecar entries
// whose encoding is patch.
const CodecZstdPatch = "zstd-patch"

// BaselineLayerMeta is the minimum descriptor the planner needs for each
// baseline layer: a digest to key on, a size to match against, and a media
// type for sanity.
type BaselineLayerMeta struct {
	Digest    digest.Digest
	Size      int64
	MediaType string
}

// Planner computes per-layer encoding decisions for ShippedInDelta. It
// owns no I/O state directly — readBlob is injected so tests can avoid
// the real container-image stack.
type Planner struct {
	baseline []BaselineLayerMeta
	readBlob func(digest.Digest) ([]byte, error)
}

// NewPlanner builds a planner that reads blobs via readBlob, keyed by
// digest. The function must handle both target and baseline digests.
func NewPlanner(baseline []BaselineLayerMeta, readBlob func(digest.Digest) ([]byte, error)) *Planner {
	return &Planner{baseline: baseline, readBlob: readBlob}
}

// Run returns the BlobRef entries to drop into Sidecar.ShippedInDelta and
// the on-disk payload map. The payload under each digest is the bytes the
// exporter should persist at `<deltaDir>/<digest.Encoded()>` before packing.
func (p *Planner) Run(
	ctx context.Context, shipped []diff.BlobRef,
) ([]diff.BlobRef, map[digest.Digest][]byte, error) {
	_ = ctx // reserved for future cancellation plumbing
	entries := make([]diff.BlobRef, 0, len(shipped))
	payloads := make(map[digest.Digest][]byte, len(shipped))

	for _, l := range shipped {
		target, err := p.readBlob(l.Digest)
		if err != nil {
			return nil, nil, fmt.Errorf("read target blob %s: %w", l.Digest, err)
		}

		bestRef, ok := p.pickClosest(l.Size)
		if !ok {
			// No baseline layers to diff against → always full.
			entries = append(entries, fullEntry(l))
			payloads[l.Digest] = target
			continue
		}

		refBytes, err := p.readBlob(bestRef.Digest)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"read baseline reference %s: %w", bestRef.Digest, err)
		}

		patch, err := zstdpatch.Encode(refBytes, target)
		if err != nil {
			return nil, nil, fmt.Errorf("encode patch %s: %w", l.Digest, err)
		}
		fullZst, err := zstdpatch.EncodeFull(target)
		if err != nil {
			return nil, nil, fmt.Errorf("encode full %s: %w", l.Digest, err)
		}

		if len(patch) < len(fullZst) && int64(len(patch)) < l.Size {
			entries = append(entries, diff.BlobRef{
				Digest:          l.Digest,
				Size:            l.Size,
				MediaType:       l.MediaType,
				Encoding:        diff.EncodingPatch,
				Codec:           CodecZstdPatch,
				PatchFromDigest: bestRef.Digest,
				ArchiveSize:     int64(len(patch)),
			})
			payloads[l.Digest] = patch
			continue
		}
		entries = append(entries, fullEntry(l))
		payloads[l.Digest] = target
	}
	return entries, payloads, nil
}

// pickClosest returns the baseline layer whose size is closest to want,
// with ties broken by first-seen index (deterministic for a given input).
func (p *Planner) pickClosest(want int64) (BaselineLayerMeta, bool) {
	if len(p.baseline) == 0 {
		return BaselineLayerMeta{}, false
	}
	best := p.baseline[0]
	bestDelta := absDelta(best.Size, want)
	for _, b := range p.baseline[1:] {
		d := absDelta(b.Size, want)
		if d < bestDelta {
			best, bestDelta = b, d
		}
	}
	return best, true
}

func absDelta(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}

// fullEntry builds a sidecar entry describing encoding=full for layer l.
func fullEntry(l diff.BlobRef) diff.BlobRef {
	return diff.BlobRef{
		Digest:      l.Digest,
		Size:        l.Size,
		MediaType:   l.MediaType,
		Encoding:    diff.EncodingFull,
		ArchiveSize: l.Size,
	}
}

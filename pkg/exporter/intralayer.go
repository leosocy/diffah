package exporter

import (
	"context"
	"fmt"
	"sort"
	"sync"

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
// the real container-image stack. The fingerprinter is used to build a
// byte-weighted content overlap score per baseline; a nil fingerprinter
// falls back to DefaultFingerprinter{}.
type Planner struct {
	baseline    []BaselineLayerMeta
	readBlob    func(digest.Digest) ([]byte, error)
	fingerprint Fingerprinter

	// Lazily populated on first Run via ensureBaselineFP. A nil entry
	// means "fingerprint failed for this baseline"; the planner treats
	// that baseline as a size-only candidate.
	fpOnce     sync.Once
	baselineFP map[digest.Digest]Fingerprint
}

// NewPlanner builds a planner that reads blobs via readBlob, keyed by
// digest. The function must handle both target and baseline digests.
// A nil Fingerprinter defaults to DefaultFingerprinter{}. Baselines are
// sorted by Digest at construction time for deterministic tie-breaks.
func NewPlanner(
	baseline []BaselineLayerMeta,
	readBlob func(digest.Digest) ([]byte, error),
	fp Fingerprinter,
) *Planner {
	sorted := make([]BaselineLayerMeta, len(baseline))
	copy(sorted, baseline)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Digest < sorted[j].Digest
	})
	return &Planner{
		baseline:    sorted,
		readBlob:    readBlob,
		fingerprint: fp,
	}
}

// Run returns the BlobRef entries to drop into the sidecar's shipped blobs and
// the on-disk payload map. The payload under each digest is the bytes the
// exporter should persist at `<deltaDir>/<digest.Encoded()>` before packing.
//
// Run loads each shipped target via readBlob before delegating to PlanShipped.
// Encoder paths that already have target bytes in hand should call PlanShipped
// directly — that way a single Planner instance fingerprints the baseline set
// once across the whole pair instead of once per shipped layer.
func (p *Planner) Run(
	ctx context.Context, shipped []diff.BlobRef,
) ([]diff.BlobRef, map[digest.Digest][]byte, error) {
	// Prime the baseline fingerprint cache unconditionally so callers that
	// inspect baselineFP after an empty-shipped Run (existing contract) still
	// see it populated.
	p.ensureBaselineFP(ctx)
	entries := make([]diff.BlobRef, 0, len(shipped))
	payloads := make(map[digest.Digest][]byte, len(shipped))
	for _, l := range shipped {
		target, err := p.readBlob(l.Digest)
		if err != nil {
			return nil, nil, fmt.Errorf("read target blob %s: %w", l.Digest, err)
		}
		entry, payload, err := p.PlanShipped(ctx, l, target)
		if err != nil {
			return nil, nil, err
		}
		entries = append(entries, entry)
		payloads[l.Digest] = payload
	}
	return entries, payloads, nil
}

// PlanShipped decides the encoding (full vs patch) for a single shipped layer
// whose target bytes are already in memory. The Planner's baseline fingerprint
// cache is shared across calls, so an encoder loop can reuse one Planner per
// pair and pay the baseline-fingerprinting cost only once.
func (p *Planner) PlanShipped(
	ctx context.Context, s diff.BlobRef, target []byte,
) (diff.BlobRef, []byte, error) {
	p.ensureBaselineFP(ctx)
	fp := p.fingerprint
	if fp == nil {
		fp = DefaultFingerprinter{}
	}
	// Target fingerprint failure is non-fatal; pickSimilar degrades to
	// pickClosest when the first argument is nil.
	targetFP, _ := fp.Fingerprint(ctx, s.MediaType, target)
	bestRef, ok := p.pickSimilar(targetFP, s.Size)
	if !ok {
		return fullEntry(s), target, nil
	}
	refBytes, err := p.readBlob(bestRef.Digest)
	if err != nil {
		return diff.BlobRef{}, nil, fmt.Errorf(
			"read baseline reference %s: %w", bestRef.Digest, err)
	}
	patch, err := zstdpatch.Encode(ctx, refBytes, target)
	if err != nil {
		return diff.BlobRef{}, nil, fmt.Errorf("encode patch %s: %w", s.Digest, err)
	}
	fullZst, err := zstdpatch.EncodeFull(target)
	if err != nil {
		return diff.BlobRef{}, nil, fmt.Errorf("encode full %s: %w", s.Digest, err)
	}
	if len(patch) < len(fullZst) && int64(len(patch)) < s.Size {
		return diff.BlobRef{
			Digest:          s.Digest,
			Size:            s.Size,
			MediaType:       s.MediaType,
			Encoding:        diff.EncodingPatch,
			Codec:           CodecZstdPatch,
			PatchFromDigest: bestRef.Digest,
			ArchiveSize:     int64(len(patch)),
		}, patch, nil
	}
	return fullEntry(s), target, nil
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

// ensureBaselineFP fingerprints every baseline layer exactly once per
// Planner instance. Failures are recorded as nil entries in baselineFP
// so callers can tell "no fingerprint available" from "empty
// fingerprint" without a separate presence check. This is why
// pickSimilar skips entries by nil-check rather than by
// errors.Is(err, ErrFingerprintFailed) — the error never crosses the
// boundary; the nil-entry sentinel carries the same information.
func (p *Planner) ensureBaselineFP(ctx context.Context) {
	p.fpOnce.Do(func() {
		fp := p.fingerprint
		if fp == nil {
			fp = DefaultFingerprinter{}
		}
		p.baselineFP = make(map[digest.Digest]Fingerprint, len(p.baseline))
		for _, b := range p.baseline {
			blob, err := p.readBlob(b.Digest)
			if err != nil {
				p.baselineFP[b.Digest] = nil
				continue
			}
			f, err := fp.Fingerprint(ctx, b.MediaType, blob)
			if err != nil {
				p.baselineFP[b.Digest] = nil
				continue
			}
			p.baselineFP[b.Digest] = f
		}
	})
}

// pickSimilar chooses the baseline most content-similar to the target
// (byte-weighted tar-entry digest intersection), falling back to
// pickClosest on any of three unrecoverable conditions:
//
//  1. targetFP is nil (target fingerprinting failed)
//  2. no baseline has a non-nil fingerprint
//  3. every candidate's score is 0 (no shared content)
//
// Ties on score break by size-closest; further ties break by the
// baseline's index in the sorted-by-digest p.baseline slice.
func (p *Planner) pickSimilar(
	targetFP Fingerprint, targetSize int64,
) (BaselineLayerMeta, bool) {
	if len(p.baseline) == 0 {
		return BaselineLayerMeta{}, false
	}
	if targetFP == nil {
		return p.pickClosest(targetSize)
	}

	// Collect candidates that actually have fingerprints and score them.
	type scored struct {
		meta  BaselineLayerMeta
		score int64
	}
	cands := make([]scored, 0, len(p.baseline))
	for _, b := range p.baseline {
		fp := p.baselineFP[b.Digest]
		if fp == nil {
			continue
		}
		cands = append(cands, scored{meta: b, score: score(targetFP, fp)})
	}
	if len(cands) == 0 {
		return p.pickClosest(targetSize)
	}

	// Determine max score.
	var maxScore int64
	for _, c := range cands {
		if c.score > maxScore {
			maxScore = c.score
		}
	}
	if maxScore == 0 {
		return p.pickClosest(targetSize)
	}

	// Narrow to winners. cands[:0] reuses the same backing array — safe
	// because range copies each element into c before we write back,
	// so no iteration-vs-append aliasing.
	winners := cands[:0]
	for _, c := range cands {
		if c.score == maxScore {
			winners = append(winners, c)
		}
	}
	if len(winners) == 1 {
		return winners[0].meta, true
	}

	// Tie-break on size-closest, then first in sorted-by-digest order.
	best := winners[0].meta
	bestDelta := absDelta(best.Size, targetSize)
	for _, w := range winners[1:] {
		d := absDelta(w.meta.Size, targetSize)
		if d < bestDelta {
			best, bestDelta = w.meta, d
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

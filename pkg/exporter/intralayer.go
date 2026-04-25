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

// ResolveWindowLog converts the user-facing 0 sentinel into a concrete
// log2 window size based on the layer's declared size. The bands
// (≤128 MiB→27, ≤1 GiB→30, >1 GiB→31) match Phase-4 spec §3.4 and trade
// encoder memory (≈ 2 × 2^N bytes per running encode) against
// long-range match opportunity. A non-zero userWindowLog is honored
// verbatim so explicit operator overrides bypass the per-layer pick.
func ResolveWindowLog(userWindowLog int, layerSize int64) int {
	if userWindowLog != 0 {
		return userWindowLog
	}
	switch {
	case layerSize <= 128<<20:
		return 27
	case layerSize <= 1<<30:
		return 30
	default:
		return 31
	}
}

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

	// Stage 1 of Phase 4: tunables threaded into every Encode call.
	// Zero values reproduce historical Phase-3 behavior (level 3,
	// windowLog 27).
	level     int
	windowLog int

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
//
// level and windowLog are forwarded to every zstd Encode/EncodeFull
// call; zero values reproduce Phase-3 byte-identical defaults.
func NewPlanner(
	baseline []BaselineLayerMeta,
	readBlob func(digest.Digest) ([]byte, error),
	fp Fingerprinter,
	level, windowLog int,
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
		level:       level,
		windowLog:   windowLog,
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
	wl := ResolveWindowLog(p.windowLog, s.Size)
	patch, err := zstdpatch.Encode(ctx, refBytes, target,
		zstdpatch.EncodeOpts{Level: p.level, WindowLog: wl})
	if err != nil {
		return diff.BlobRef{}, nil, fmt.Errorf("encode patch %s: %w", s.Digest, err)
	}
	fullZst, err := zstdpatch.EncodeFull(target,
		zstdpatch.EncodeOpts{Level: p.level, WindowLog: wl})
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

// PickTopK returns up to k candidates ordered by content-similarity
// score (descending), with size-closest as the tie-break inside an
// equal-score band, and digest-asc as the final tiebreak. Falls back
// to size-closest top-k when targetFP is nil or no baseline has a
// fingerprint. Result length is min(k, len(p.baseline)). Deterministic
// for fixed inputs.
//
// PickTopK auto-primes the baseline fingerprint cache so callers may
// invoke it before any other Planner method has run; ctx is propagated
// into that priming pass so caller cancellation flows through.
func (p *Planner) PickTopK(ctx context.Context, targetFP Fingerprint, targetSize int64, k int) []BaselineLayerMeta {
	if k <= 0 || len(p.baseline) == 0 {
		return nil
	}
	p.ensureBaselineFP(ctx)
	type scored struct {
		meta  BaselineLayerMeta
		score int64
	}
	cands := make([]scored, 0, len(p.baseline))
	for _, b := range p.baseline {
		var s int64
		if targetFP != nil {
			s = score(targetFP, p.baselineFP[b.Digest])
		}
		cands = append(cands, scored{meta: b, score: s})
	}
	// Sort by score desc, size-closeness asc, then digest asc for stable order.
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		di := absDelta(cands[i].meta.Size, targetSize)
		dj := absDelta(cands[j].meta.Size, targetSize)
		if di != dj {
			return di < dj
		}
		return cands[i].meta.Digest < cands[j].meta.Digest
	})
	if k > len(cands) {
		k = len(cands)
	}
	out := make([]BaselineLayerMeta, k)
	for i := 0; i < k; i++ {
		out[i] = cands[i].meta
	}
	return out
}

// PlanShippedTopK encodes the target up to k+1 ways — once per top-k
// baseline candidate plus one "full" encode — and returns whichever
// produces the smallest emitted bytes. A target fingerprint is computed
// inside this call (failure is non-fatal; PickTopK degrades to size-only
// ranking).
//
// k=1 is observably equivalent to PlanShipped: same candidate selection,
// same Encode/EncodeFull comparison, same gating (patch must strictly
// beat both the full-zstd ceiling and the raw target bytes).
//
// PlanShipped stays in the file as the legacy K=1 fast path; this
// method is the K-aware variant wired by encodeShipped behind the
// --candidates flag.
func (p *Planner) PlanShippedTopK(
	ctx context.Context, s diff.BlobRef, target []byte, k int,
) (diff.BlobRef, []byte, error) {
	p.ensureBaselineFP(ctx)
	fp := p.fingerprint
	if fp == nil {
		fp = DefaultFingerprinter{}
	}
	// Target fingerprint failure is non-fatal; PickTopK degrades to
	// size-closest ranking when targetFP is nil.
	targetFP, _ := fp.Fingerprint(ctx, s.MediaType, target)
	cands := p.PickTopK(ctx, targetFP, s.Size, k)
	if len(cands) == 0 {
		return fullEntry(s), target, nil
	}

	// Hoisted: compute the full-zstd ceiling once. target is
	// loop-invariant, so leaving EncodeFull inside the loop would
	// trigger K identical compressions of the same bytes — visible cost
	// once PR-4 raises --candidates default above 1.
	wl := ResolveWindowLog(p.windowLog, s.Size)
	fullZst, err := zstdpatch.EncodeFull(target,
		zstdpatch.EncodeOpts{Level: p.level, WindowLog: wl})
	if err != nil {
		return diff.BlobRef{}, nil, fmt.Errorf(
			"encode full %s: %w", s.Digest, err)
	}

	// "full" is always the safety floor — never inflate beyond raw
	// target bytes.
	bestEntry := fullEntry(s)
	bestPayload := target

	for _, c := range cands {
		refBytes, err := p.readBlob(c.Digest)
		if err != nil {
			return diff.BlobRef{}, nil, fmt.Errorf(
				"read baseline reference %s: %w", c.Digest, err)
		}
		patch, err := zstdpatch.Encode(ctx, refBytes, target,
			zstdpatch.EncodeOpts{Level: p.level, WindowLog: wl})
		if err != nil {
			return diff.BlobRef{}, nil, fmt.Errorf(
				"encode patch %s vs %s: %w", s.Digest, c.Digest, err)
		}
		// Same gating as PlanShipped: only emit a patch if it strictly
		// beats both the full-zstd ceiling AND the raw target bytes.
		// Additionally must beat the running best.
		if len(patch) < len(fullZst) &&
			int64(len(patch)) < s.Size &&
			len(patch) < len(bestPayload) {
			bestEntry = diff.BlobRef{
				Digest:          s.Digest,
				Size:            s.Size,
				MediaType:       s.MediaType,
				Encoding:        diff.EncodingPatch,
				Codec:           CodecZstdPatch,
				PatchFromDigest: c.Digest,
				ArchiveSize:     int64(len(patch)),
			}
			bestPayload = patch
		}
	}
	return bestEntry, bestPayload, nil
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

// SeedBaselineFingerprints pre-populates the planner's baseline
// fingerprint map for digests in fps that match a baseline this planner
// owns. Used by the encoder to wire fpCache results into each per-pair
// Planner so a baseline shared across N pairs is fingerprinted once,
// not N times. Must be called before any concurrent PlanShipped /
// PlanShippedTopK invocation; the encoder pool calls this during
// per-pair planner setup, before submitting any encode job.
//
// A nil-valued entry in fps is treated as "fingerprint failed during
// priming" and preserved verbatim — the planner falls back to size-only
// matching for those baselines without retrying the fingerprint pass.
func (p *Planner) SeedBaselineFingerprints(fps map[digest.Digest]Fingerprint) {
	if len(fps) == 0 {
		return
	}
	if p.baselineFP == nil {
		p.baselineFP = make(map[digest.Digest]Fingerprint, len(p.baseline))
	}
	for _, b := range p.baseline {
		if f, ok := fps[b.Digest]; ok {
			p.baselineFP[b.Digest] = f
		}
	}
}

// ensureBaselineFP fingerprints every baseline layer exactly once per
// Planner instance. Failures are recorded as nil entries in baselineFP
// so callers can tell "no fingerprint available" from "empty
// fingerprint" without a separate presence check. This is why
// pickSimilar skips entries by nil-check rather than by
// errors.Is(err, ErrFingerprintFailed) — the error never crosses the
// boundary; the nil-entry sentinel carries the same information.
//
// Pre-seeded entries (via SeedBaselineFingerprints) are honored as-is,
// including nil sentinels. Only baselines without a cached fingerprint
// trigger a fresh readBlob + Fingerprint call.
func (p *Planner) ensureBaselineFP(ctx context.Context) {
	p.fpOnce.Do(func() {
		fp := p.fingerprint
		if fp == nil {
			fp = DefaultFingerprinter{}
		}
		if p.baselineFP == nil {
			p.baselineFP = make(map[digest.Digest]Fingerprint, len(p.baseline))
		}
		for _, b := range p.baseline {
			if _, seeded := p.baselineFP[b.Digest]; seeded {
				continue
			}
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

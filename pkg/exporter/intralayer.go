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

// PlanShipped decides the encoding (full vs patch) for a single shipped
// layer with target bytes already in memory. K=1 shorthand over
// PlanShippedTopK; behavior is byte-identical (pinned by
// TestPlanShippedTopK_K1MatchesPlanShipped).
func (p *Planner) PlanShipped(
	ctx context.Context, s diff.BlobRef, target []byte,
) (diff.BlobRef, []byte, error) {
	return p.PlanShippedTopK(ctx, s, target, 1)
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
// ranking). PlanShipped is the k=1 shorthand wrapper.
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
	fullZst, err := zstdpatch.EncodeFull(target, //nolint:staticcheck // intentional: see EncodeFull deprecation comment
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
		//nolint:staticcheck // intentional: this caller is migrated to EncodeStream in PR 5 of the streaming I/O series.
		patch, err := zstdpatch.Encode(ctx, refBytes, target,
			zstdpatch.EncodeOpts{Level: p.level, WindowLog: wl})
		if err != nil {
			return diff.BlobRef{}, nil, fmt.Errorf(
				"encode patch %s vs %s: %w", s.Digest, c.Digest, err)
		}
		// Patch must strictly beat the full-zstd ceiling, the raw target
		// bytes, and the running best — otherwise full-encode wins.
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
// so PickTopK can distinguish "fingerprint failed" from "no shared
// content" without a separate presence check — score(target, nil)
// returns 0 and the layer falls through to size-only ranking.
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

// pickSimilar is a k=1 shorthand over PickTopK retained as the entry
// point for the legacy TestPickSimilar_* assertion style. Production
// code routes through PlanShippedTopK → PickTopK directly. Behavior
// is observably equivalent — see PickTopK's tie-break order.
func (p *Planner) pickSimilar(
	targetFP Fingerprint, targetSize int64,
) (BaselineLayerMeta, bool) {
	cands := p.PickTopK(context.Background(), targetFP, targetSize, 1)
	if len(cands) == 0 {
		return BaselineLayerMeta{}, false
	}
	return cands[0], true
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

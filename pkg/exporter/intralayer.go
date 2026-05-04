package exporter

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
// owns no I/O state directly — readBlobPath is injected so tests can avoid
// the real container-image stack. The fingerprinter is used to build a
// byte-weighted content overlap score per baseline; a nil fingerprinter
// falls back to DefaultFingerprinter{}.
type Planner struct {
	baseline     []BaselineLayerMeta
	readBlobPath func(digest.Digest) (string, error)
	fingerprint  Fingerprinter

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

// NewPlanner builds a planner that reads blobs via readBlobPath, keyed by
// digest. The function must handle baseline digests and return the path to
// the spooled blob file on disk. A nil Fingerprinter defaults to
// DefaultFingerprinter{}. Baselines are sorted by Digest at construction
// time for deterministic tie-breaks.
//
// level and windowLog are forwarded to every zstd Encode/EncodeFullStream
// call; zero values reproduce Phase-3 byte-identical defaults.
func NewPlanner(
	baseline []BaselineLayerMeta,
	readBlobPath func(digest.Digest) (string, error),
	fp Fingerprinter,
	level, windowLog int,
) *Planner {
	sorted := make([]BaselineLayerMeta, len(baseline))
	copy(sorted, baseline)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Digest < sorted[j].Digest
	})
	return &Planner{
		baseline:     sorted,
		readBlobPath: readBlobPath,
		fingerprint:  fp,
		level:        level,
		windowLog:    windowLog,
	}
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
// baseline candidate (via zstdpatch.EncodeStream into per-candidate spill
// files) plus one "full" size measurement (via zstdpatch.EncodeFullStream
// into io.Discard) — and returns whichever produces the smallest emitted
// bytes along with the path to the winning payload file.
//
// If len(cands) == 0 after PickTopK (no viable baseline), the function
// returns immediately with (fullEntry(s), targetPath, nil) without performing
// any encoding work.
//
// targetPath is the path to the on-disk target spool written by spoolBlob.
// blobsDir is where candidate spill files are written (auto-cleaned on loss).
// A target fingerprint is computed inside this call from disk (failure is
// non-fatal; PickTopK degrades to size-only ranking).
//
// Ownership: on return the caller owns payloadPath (rename or delete it).
// If payloadPath != targetPath, targetPath is also caller-owned and must be
// cleaned up (encode.go's encodeOneShipped removes it when a patch wins).
func (p *Planner) PlanShippedTopK(
	ctx context.Context, s diff.BlobRef, targetPath string, blobsDir string, k int,
) (diff.BlobRef, string, error) {
	p.ensureBaselineFP(ctx)
	fp := p.fingerprint
	if fp == nil {
		fp = DefaultFingerprinter{}
	}

	// Re-fingerprint the target by re-reading from disk. The target file is
	// OS-cached (just written by spoolBlob), so the cost is one sequential
	// read of cached bytes; no RAM retention.
	tf, err := os.Open(targetPath)
	if err != nil {
		return diff.BlobRef{}, "", fmt.Errorf("open target spool %s: %w", targetPath, err)
	}
	targetFP, _ := fp.FingerprintReader(ctx, s.MediaType, tf) // fp failure → nil → size-only ranking
	_ = tf.Close()                                            // read-only file; close error has no observable consequence

	cands := p.PickTopK(ctx, targetFP, s.Size, k)
	if len(cands) == 0 {
		return fullEntry(s), targetPath, nil
	}

	wl := ResolveWindowLog(p.windowLog, s.Size)
	opts := zstdpatch.EncodeOpts{Level: p.level, WindowLog: wl}

	// Full-zstd ceiling: streaming, size only, no spill (correction E).
	// EncodeFullStream already counts compressed bytes internally and returns
	// the count — no need for a duplicate writeCounter in this package.
	fullSize, err := zstdpatch.EncodeFullStream(ctx, targetPath, io.Discard, opts)
	if err != nil {
		return diff.BlobRef{}, "", fmt.Errorf("size-only full encode %s: %w", s.Digest, err)
	}

	bestEntry := fullEntry(s)
	bestPath := targetPath // raw-target / encoding=full default
	bestSize := s.Size

	for _, c := range cands {
		refPath, err := p.readBlobPath(c.Digest)
		if err != nil {
			return diff.BlobRef{}, "", fmt.Errorf("baseline path %s: %w", c.Digest, err)
		}
		candPath := filepath.Join(blobsDir,
			fmt.Sprintf("%s.cand-%s", s.Digest.Encoded(), c.Digest.Encoded()[:8]))
		patchSize, err := zstdpatch.EncodeStream(ctx, refPath, targetPath, candPath, opts)
		if err != nil {
			_ = os.Remove(candPath)
			return diff.BlobRef{}, "", fmt.Errorf("encode patch %s vs %s: %w", s.Digest, c.Digest, err)
		}
		// Patch must strictly beat full-zstd, raw target, AND running best.
		if patchSize < fullSize && patchSize < s.Size && patchSize < bestSize {
			// Discard previous winner if it was a candidate spill (not the target).
			if bestPath != targetPath {
				_ = os.Remove(bestPath)
			}
			bestEntry = diff.BlobRef{
				Digest: s.Digest, Size: s.Size, MediaType: s.MediaType,
				Encoding: diff.EncodingPatch, Codec: CodecZstdPatch,
				PatchFromDigest: c.Digest, ArchiveSize: patchSize,
			}
			bestPath = candPath
			bestSize = patchSize
		} else {
			_ = os.Remove(candPath)
		}
	}
	return bestEntry, bestPath, nil
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
// trigger a fresh readBlobPath + FingerprintReader call.
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
			path, err := p.readBlobPath(b.Digest)
			if err != nil {
				p.baselineFP[b.Digest] = nil
				continue
			}
			f, err := os.Open(path)
			if err != nil {
				p.baselineFP[b.Digest] = nil
				continue
			}
			fingerprint, err := fp.FingerprintReader(ctx, b.MediaType, f)
			_ = f.Close() // read-only file; close error has no observable consequence
			if err != nil {
				p.baselineFP[b.Digest] = nil
				continue
			}
			p.baselineFP[b.Digest] = fingerprint
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

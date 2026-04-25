package exporter

import (
	"context"
	"fmt"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/progress"
)

// encodeShipped streams every shipped target layer through the encoder
// pipeline and writes the result into pool. PR-3 splits the work into
// two phases driven by a bounded worker pool:
//
//   - E1: prime fpCache for the union of distinct baseline layers across
//     all pairs (in parallel; singleflight collapses duplicate fetches).
//   - E2: per (pair, shipped) tuple, fetch the target bytes and run the
//     planner against the primed cache (also in parallel).
//
// Output bytes are byte-identical across worker counts because:
//   - blobPool is content-addressed and first-write-wins
//   - fullBlobEntry only depends on s, not on iteration order
//   - PlanShippedTopK is deterministic for fixed inputs
//
// workers<1 collapses to serial; candidates<1 collapses to single-best.
func encodeShipped(
	ctx context.Context, pool *blobPool, pairs []*pairPlan,
	mode string, fp Fingerprinter, rep progress.Reporter,
	level, windowLog, candidates, workers int,
) error {
	if rep == nil {
		rep = progress.NewDiscard()
	}
	if workers < 1 {
		workers = 1
	}
	if candidates < 1 {
		candidates = 1
	}
	if fp == nil {
		fp = DefaultFingerprinter{}
	}

	cache := newFpCache()
	if err := primeBaselineCache(ctx, pairs, cache, fp, workers); err != nil {
		return fmt.Errorf("prime baseline fingerprints: %w", err)
	}
	return encodeTargets(ctx, pool, pairs, mode, fp, rep, cache,
		level, windowLog, candidates, workers)
}

// primeBaselineCache pre-fetches every distinct baseline layer digest
// referenced by pairs into cache, in parallel up to `workers`. Per-
// baseline fetch failures are swallowed (logged, not propagated) to
// mirror Planner.ensureBaselineFP's fail-soft contract: a missing or
// unreadable baseline layer must NOT abort encoding — the planner
// degrades that baseline to size-only/full fallback. Returning the error
// here would convert "one bad baseline" into "whole encode fails,"
// breaking TestEncodeShipped_WarningOnError_FallbackToFull and the
// matching real-world resilience guarantee. ctx-cancellation errors
// still propagate so cancellation flows.
func primeBaselineCache(
	ctx context.Context, pairs []*pairPlan,
	cache *fpCache, fp Fingerprinter, workers int,
) error {
	pool, poolCtx := newWorkerPool(ctx, workers)
	seen := make(map[digest.Digest]struct{})
	for _, p := range pairs {
		ref := p.BaselineImageRef
		sys := p.SystemContext
		for _, b := range p.BaselineLayerMeta {
			if _, ok := seen[b.Digest]; ok {
				continue
			}
			seen[b.Digest] = struct{}{}
			fetch := func(d digest.Digest) ([]byte, error) {
				return readBlobBytes(ctx, ref, sys, d)
			}
			pool.Submit(func() error {
				if _, _, err := cache.GetOrLoad(ctx, b, fetch, fp); err != nil {
					if cerr := poolCtx.Err(); cerr != nil {
						return cerr
					}
					log().Warn("baseline fingerprint priming failed; planner will fall back",
						"digest", b.Digest, "err", err)
				}
				return nil
			})
		}
	}
	return pool.Wait()
}

// encodeTargets runs phase E2: for every (pair, shipped) tuple it
// fetches the target bytes and resolves the layer's encoding through
// PlanShippedTopK against the primed cache, in parallel up to `workers`.
func encodeTargets(
	ctx context.Context, pool *blobPool, pairs []*pairPlan,
	mode string, fp Fingerprinter, rep progress.Reporter, cache *fpCache,
	level, windowLog, candidates, workers int,
) error {
	// fingerprint snapshot taken once after Phase E1: every per-pair
	// Planner is seeded with the same map so a baseline shared across N
	// pairs is fingerprinted once (during E1), not N times (once per
	// Planner.ensureBaselineFP). Spec §4.2.
	seedFP := cache.SnapshotFingerprints()

	encPool, _ := newWorkerPool(ctx, workers)
	for _, p := range pairs {
		// Per-pair digest → meta lookup so the readBaseline retry closure
		// passes the real MediaType into cache.GetOrLoad. Without this map
		// the retry path strips MediaType, which would silently produce a
		// wrong-MediaType fingerprint if it ever survived the cache.
		metaByDigest := make(map[digest.Digest]BaselineLayerMeta, len(p.BaselineLayerMeta))
		for _, b := range p.BaselineLayerMeta {
			metaByDigest[b.Digest] = b
		}

		// Each pair builds its own Planner that defers all baseline reads
		// to the shared cache. Phase E1 has already primed every digest in
		// p.BaselineLayerMeta, so this closure should always hit the cache;
		// the fetch function is kept as a safety net for the (currently
		// impossible) case where a baseline digest reaches the planner
		// without having been primed.
		readBaseline := func(d digest.Digest) ([]byte, error) {
			meta, ok := metaByDigest[d]
			if !ok {
				meta = BaselineLayerMeta{Digest: d}
			}
			_, b, err := cache.GetOrLoad(ctx, meta,
				func(d digest.Digest) ([]byte, error) {
					return readBlobBytes(ctx, p.BaselineImageRef, p.SystemContext, d)
				}, fp)
			return b, err
		}
		planner := NewPlanner(p.BaselineLayerMeta, readBaseline, fp, level, windowLog)
		planner.SeedBaselineFingerprints(seedFP)

		for _, s := range p.Shipped {
			// pool.has() is an early-out optimization, not a correctness
			// gate. Two workers racing to encode the same digest is safe
			// because addIfAbsent is first-write-wins; a missed early-out
			// just means a duplicate encode whose output is then discarded
			// by addIfAbsent. Determinism is preserved by the
			// content-addressed pool, not by who arrives first.
			if pool.has(s.Digest) {
				continue
			}
			encPool.Submit(func() error {
				return encodeOneShipped(ctx, pool, p, s, planner, mode, rep, candidates)
			})
		}
	}
	return encPool.Wait()
}

// encodeOneShipped streams the bytes of a single shipped target layer,
// runs PlanShippedTopK against the primed planner, and writes the result
// (or a full-encoding fallback) into pool. Per-layer encode failures are
// swallowed and converted to full encoding to mirror Phase 3 fail-soft
// semantics; only target-bytes read errors abort the worker.
func encodeOneShipped(
	ctx context.Context, pool *blobPool, p *pairPlan, s diff.BlobRef,
	planner *Planner, mode string, rep progress.Reporter, candidates int,
) error {
	layer := rep.StartLayer(s.Digest, s.Size, string(s.Encoding))
	// The OCI-archive transport transparently decompresses tar+gzip layers
	// on GetBlob, so the streamed byte count can exceed the manifest-
	// declared s.Size. Cap progress reports to s.Size so the bar stops at
	// 100 % instead of overshooting.
	layerBytes, err := streamBlobBytes(ctx, p.TargetImageRef, p.SystemContext, s.Digest,
		cappedWriter(s.Size, layer.Written))
	if err != nil {
		layer.Fail(err)
		return fmt.Errorf("read shipped %s: %w", s.Digest, err)
	}
	if pool.refCount(s.Digest) > 1 || mode == modeOff {
		pool.addIfAbsent(s.Digest, layerBytes, fullBlobEntry(s))
		layer.Done()
		return nil
	}
	entry, payload, err := planner.PlanShippedTopK(ctx, s, layerBytes, candidates)
	if err != nil {
		log().Warn("patch encode failed, falling back to full",
			"pair", p.Name, "digest", s.Digest, "err", err)
		pool.addIfAbsent(s.Digest, layerBytes, fullBlobEntry(s))
		layer.Done()
		return nil
	}
	pool.addIfAbsent(s.Digest, payload, blobEntryFromPlanner(entry))
	layer.Done()
	return nil
}

// cappedWriter returns an onChunk callback that forwards up to total bytes to
// sink, clamping chunks that would cross the cap and dropping anything after.
func cappedWriter(total int64, sink func(int64)) func(int64) {
	remaining := total
	return func(n int64) {
		if remaining <= 0 {
			return
		}
		if n > remaining {
			n = remaining
		}
		sink(n)
		remaining -= n
	}
}

func blobEntryFromPlanner(entry diff.BlobRef) diff.BlobEntry {
	return diff.BlobEntry{
		Size: entry.Size, MediaType: entry.MediaType,
		Encoding: entry.Encoding, Codec: entry.Codec,
		PatchFromDigest: entry.PatchFromDigest,
		ArchiveSize:     entry.ArchiveSize,
	}
}

func fullBlobEntry(s diff.BlobRef) diff.BlobEntry {
	return diff.BlobEntry{
		Size: s.Size, MediaType: s.MediaType,
		Encoding: diff.EncodingFull, ArchiveSize: s.Size,
	}
}

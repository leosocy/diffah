package exporter

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/progress"
)

// encodeShipped streams every shipped target layer through the encoder
// pipeline and writes the result into pool. PR-3 splits the work into
// two phases driven by a bounded worker pool:
//
//   - E1: prime baselineSpool for the union of distinct baseline layers
//     across all pairs (in parallel; singleflight collapses duplicate
//     fetches). Blobs are streamed to <workdir>/baselines/<digest>.
//   - E2: per (pair, shipped) tuple, spool the target to disk via spoolBlob
//     (write-tmp + atomic rename) and run the planner against the primed
//     spool, then adopt the winning path into the blob pool via
//     pool.addEntryFromPath (also in parallel).
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
	workdir string,
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

	baselinesDir := filepath.Join(workdir, "baselines")
	if err := os.MkdirAll(baselinesDir, 0o700); err != nil {
		return fmt.Errorf("create baselines spool dir: %w", err)
	}
	spool := newBaselineSpool(baselinesDir)
	if err := primeBaselineSpool(ctx, pairs, spool, fp, workers); err != nil {
		return fmt.Errorf("prime baseline fingerprints: %w", err)
	}
	return encodeTargets(ctx, pool, pairs, mode, fp, rep, spool,
		level, windowLog, candidates, workers, workdir)
}

// primeBaselineSpool pre-fetches every distinct baseline layer digest
// referenced by pairs into spool, in parallel up to `workers`. Per-
// baseline fetch failures are swallowed (logged, not propagated) to
// mirror Planner.ensureBaselineFP's fail-soft contract: a missing or
// unreadable baseline layer must NOT abort encoding — the planner
// degrades that baseline to size-only/full fallback. Returning the error
// here would convert "one bad baseline" into "whole encode fails,"
// breaking TestEncodeShipped_WarningOnError_FallbackToFull and the
// matching real-world resilience guarantee. ctx-cancellation errors
// still propagate so cancellation flows.
func primeBaselineSpool(
	ctx context.Context, pairs []*pairPlan,
	spool *baselineSpool, fp Fingerprinter, workers int,
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
			fetch := func(d digest.Digest) (io.ReadCloser, error) {
				return streamBlobReader(ctx, ref, sys, d)
			}
			pool.Submit(func() error {
				if _, err := spool.GetOrSpool(ctx, b, fetch, fp); err != nil {
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

// encodeTargets runs phase E2: for every (pair, shipped) tuple it spools
// the target blob to disk, resolves the layer's encoding through
// PlanShippedTopK against the primed spool, and adopts the winning path
// into the blob pool, in parallel up to `workers`.
func encodeTargets(
	ctx context.Context, pool *blobPool, pairs []*pairPlan,
	mode string, fp Fingerprinter, rep progress.Reporter, spool *baselineSpool,
	level, windowLog, candidates, workers int,
	workdir string,
) error {
	// fingerprint snapshot taken once after Phase E1: every per-pair
	// Planner is seeded with the same map so a baseline shared across N
	// pairs is fingerprinted once (during E1), not N times (once per
	// Planner.ensureBaselineFP). Spec §4.2.
	seedFP := spool.SnapshotFingerprints()

	targetsDir := filepath.Join(workdir, "targets")
	blobsDir := filepath.Join(workdir, "blobs")
	for _, sub := range []string{targetsDir, blobsDir} {
		if err := os.MkdirAll(sub, 0o700); err != nil {
			return fmt.Errorf("create spool subdir %s: %w", sub, err)
		}
	}

	encPool, _ := newWorkerPool(ctx, workers)
	for _, p := range pairs {
		// Each pair builds its own Planner that defers all baseline reads
		// to the shared spool. Phase E1 has already primed every digest in
		// p.BaselineLayerMeta when fetching succeeds.
		//
		// Design choice (correction B): primeBaselineSpool swallows per-digest
		// fetch errors (mirrors fpCache fail-soft contract). Unprimed digests
		// are therefore possible when the baseline registry is unreachable or
		// returns a corrupt blob. Returning an error here lets PlanShippedTopK
		// propagate it upward, which encodeOneShipped converts to a full-encoding
		// fallback — consistent with the existing resilience guarantee. A panic
		// would break that guarantee by crashing the whole export.
		readBaselinePath := func(d digest.Digest) (string, error) {
			if e, ok := spool.lookup(d); ok {
				return e.Path, nil
			}
			return "", fmt.Errorf("baseline %s not primed (fetch may have failed during E1)", d)
		}
		planner := NewPlanner(p.BaselineLayerMeta, readBaselinePath, fp, level, windowLog)
		planner.SeedBaselineFingerprints(seedFP)

		for _, s := range p.Shipped {
			// pool.has() is an early-out optimization, not a correctness
			// gate. Two workers racing to encode the same digest is safe
			// because addEntryFromPath is first-write-wins via atomic rename;
			// a missed early-out just means a duplicate encode whose output is
			// then discarded. Determinism is preserved by the content-addressed
			// pool, not by who arrives first.
			if pool.has(s.Digest) {
				continue
			}
			encPool.Submit(func() error {
				return encodeOneShipped(ctx, pool, p, s, planner, mode, rep,
					candidates, targetsDir, blobsDir)
			})
		}
	}
	return encPool.Wait()
}

// encodeOneShipped spools a single shipped target layer to disk, runs
// PlanShippedTopK against the primed planner, and adopts the winning path
// into the blob pool via pool.addEntryFromPath. Per-layer encode failures
// are swallowed and converted to full encoding (fail-soft). Only target
// spool read errors abort the worker.
func encodeOneShipped(
	ctx context.Context, pool *blobPool, p *pairPlan, s diff.BlobRef,
	planner *Planner, mode string, rep progress.Reporter,
	candidates int, targetsDir, blobsDir string,
) error {
	layer := rep.StartLayer(s.Digest, s.Size, string(s.Encoding))

	targetPath := filepath.Join(targetsDir, s.Digest.Encoded())

	// The OCI-archive transport transparently decompresses tar+gzip layers
	// on GetBlob, so the streamed byte count can exceed the manifest-
	// declared s.Size. Cap progress reports to s.Size so the bar stops at
	// 100 % instead of overshooting.
	if _, err := spoolBlob(ctx, p.TargetImageRef, p.SystemContext, s.Digest, targetPath,
		cappedWriter(s.Size, layer.Written)); err != nil {
		// spoolBlob's committed-sentinel already cleaned up its tmp file; the
		// rename never happened, so targetPath does not exist. No Remove needed.
		layer.Fail(err)
		return fmt.Errorf("spool shipped %s: %w", s.Digest, err)
	}

	if pool.refCount(s.Digest) > 1 || mode == modeOff {
		// Force full encoding; adopt target spool as the blob.
		if err := pool.addEntryFromPath(s.Digest, targetPath, fullBlobEntry(s)); err != nil {
			layer.Fail(err)
			return err
		}
		layer.Done()
		return nil
	}

	entry, payloadPath, err := planner.PlanShippedTopK(ctx, s, targetPath, blobsDir, candidates)
	if err != nil {
		log().Warn("patch planning failed, falling back to full",
			"pair", p.Name, "digest", s.Digest, "err", err)
		if err := pool.addEntryFromPath(s.Digest, targetPath, fullBlobEntry(s)); err != nil {
			layer.Fail(err)
			return err
		}
		layer.Done()
		return nil
	}

	if err := pool.addEntryFromPath(s.Digest, payloadPath, blobEntryFromPlanner(entry)); err != nil {
		layer.Fail(err)
		return err
	}
	if payloadPath != targetPath {
		// Patch winner — target spool is now leftover.
		_ = os.Remove(targetPath)
	}
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

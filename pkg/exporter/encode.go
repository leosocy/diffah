package exporter

import (
	"context"
	"fmt"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/progress"
)

func encodeShipped(
	ctx context.Context, pool *blobPool, pairs []*pairPlan,
	mode string, fp Fingerprinter, rep progress.Reporter,
) error {
	if rep == nil {
		rep = progress.NewDiscard()
	}
	for _, p := range pairs {
		readBaseline := func(d digest.Digest) ([]byte, error) {
			return readBlobBytes(ctx, p.BaselineImageRef, d)
		}
		planner := NewPlanner(p.BaselineLayerMeta, readBaseline, fp)
		for _, s := range p.Shipped {
			if pool.has(s.Digest) {
				continue
			}
			layer := rep.StartLayer(s.Digest, s.Size, string(s.Encoding))
			// The OCI-archive transport transparently decompresses tar+gzip
			// layers on GetBlob, so the streamed byte count can exceed the
			// manifest-declared s.Size. Cap progress reports to s.Size so the
			// bar stops at 100 % instead of overshooting.
			layerBytes, err := streamBlobBytes(ctx, p.TargetImageRef, s.Digest,
				cappedWriter(s.Size, layer.Written))
			if err != nil {
				layer.Fail(err)
				return fmt.Errorf("read shipped %s: %w", s.Digest, err)
			}
			if pool.refCount(s.Digest) > 1 || mode == modeOff {
				pool.addIfAbsent(s.Digest, layerBytes, fullBlobEntry(s))
				layer.Done()
				continue
			}
			entry, payload, err := planner.PlanShipped(ctx, s, layerBytes)
			if err != nil {
				log().Warn("patch encode failed, falling back to full",
					"pair", p.Name, "digest", s.Digest, "err", err)
				pool.addIfAbsent(s.Digest, layerBytes, fullBlobEntry(s))
				layer.Done()
				continue
			}
			pool.addIfAbsent(s.Digest, payload, blobEntryFromPlanner(entry))
			layer.Done()
		}
	}
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

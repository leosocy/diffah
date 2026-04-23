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
	mode string, fp Fingerprinter, _ progress.Reporter,
) error {
	for _, p := range pairs {
		for _, s := range p.Shipped {
			if pool.has(s.Digest) {
				continue
			}
			layerBytes, err := readBlobBytes(ctx, p.TargetRef, s.Digest)
			if err != nil {
				return fmt.Errorf("read shipped %s: %w", s.Digest, err)
			}
			if pool.refCount(s.Digest) > 1 || mode == modeOff {
				pool.addIfAbsent(s.Digest, layerBytes, fullBlobEntry(s))
				continue
			}
			payload, entry, err := encodeSingleShipped(ctx, p, s, layerBytes, fp)
			if err != nil {
				log().Warn("patch encode failed, falling back to full",
					"pair", p.Name, "digest", s.Digest, "err", err)
				pool.addIfAbsent(s.Digest, layerBytes, fullBlobEntry(s))
				continue
			}
			pool.addIfAbsent(s.Digest, payload, entry)
		}
	}
	return nil
}

func encodeSingleShipped(
	ctx context.Context, p *pairPlan, s diff.BlobRef,
	target []byte, fp Fingerprinter,
) ([]byte, diff.BlobEntry, error) {
	readBlob := func(d digest.Digest) ([]byte, error) {
		if d == s.Digest {
			return target, nil
		}
		return readBlobBytes(ctx, p.BaselineRef, d)
	}
	entries, payloads, err := NewPlanner(p.BaselineLayerMeta, readBlob, fp).Run(ctx, []diff.BlobRef{s})
	if err != nil {
		return nil, diff.BlobEntry{}, err
	}
	if len(entries) == 0 {
		return nil, diff.BlobEntry{}, fmt.Errorf("planner returned no entries")
	}
	entry := entries[0]
	var payload []byte
	if entry.Encoding == diff.EncodingFull {
		payload = target
	} else {
		payload = payloads[entry.Digest]
	}
	bEntry := diff.BlobEntry{
		Size: entry.Size, MediaType: entry.MediaType,
		Encoding: entry.Encoding, Codec: entry.Codec,
		PatchFromDigest: entry.PatchFromDigest,
		ArchiveSize:     entry.ArchiveSize,
	}
	return payload, bEntry, nil
}

func fullBlobEntry(s diff.BlobRef) diff.BlobEntry {
	return diff.BlobEntry{
		Size: s.Size, MediaType: s.MediaType,
		Encoding: diff.EncodingFull, ArchiveSize: s.Size,
	}
}

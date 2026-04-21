package exporter

import (
	"sort"

	"github.com/opencontainers/go-digest"

	"github.com/leosocy/diffah/pkg/diff"
)

type blobPool struct {
	bytes    map[digest.Digest][]byte
	entries  map[digest.Digest]diff.BlobEntry
	shipRefs map[digest.Digest]int
}

func newBlobPool() *blobPool {
	return &blobPool{
		bytes:    make(map[digest.Digest][]byte),
		entries:  make(map[digest.Digest]diff.BlobEntry),
		shipRefs: make(map[digest.Digest]int),
	}
}

func (p *blobPool) addIfAbsent(d digest.Digest, data []byte, e diff.BlobEntry) {
	if _, ok := p.bytes[d]; ok {
		return
	}
	p.bytes[d] = data
	p.entries[d] = e
}

func (p *blobPool) setEntry(d digest.Digest, e diff.BlobEntry) {
	p.entries[d] = e
}

func (p *blobPool) has(d digest.Digest) bool {
	_, ok := p.bytes[d]
	return ok
}

func (p *blobPool) get(d digest.Digest) ([]byte, bool) {
	b, ok := p.bytes[d]
	return b, ok
}

func (p *blobPool) countShipped(d digest.Digest) {
	p.shipRefs[d]++
}

func (p *blobPool) refCount(d digest.Digest) int {
	return p.shipRefs[d]
}

func seedManifestAndConfig(p *blobPool, plan *pairPlan) {
	mfDigest := digest.FromBytes(plan.TargetManifest)
	p.addIfAbsent(mfDigest, plan.TargetManifest, diff.BlobEntry{
		Size: int64(len(plan.TargetManifest)), MediaType: plan.TargetMediaType,
		Encoding: diff.EncodingFull, ArchiveSize: int64(len(plan.TargetManifest)),
	})
	p.addIfAbsent(plan.TargetConfigDesc.Digest, plan.TargetConfigRaw, diff.BlobEntry{
		Size: plan.TargetConfigDesc.Size, MediaType: plan.TargetConfigDesc.MediaType,
		Encoding: diff.EncodingFull, ArchiveSize: plan.TargetConfigDesc.Size,
	})
}

func (p *blobPool) sortedDigests() []digest.Digest {
	out := make([]digest.Digest, 0, len(p.bytes))
	for d := range p.bytes {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

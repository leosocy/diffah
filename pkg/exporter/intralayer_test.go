package exporter

import (
	"bytes"
	"context"
	"math/rand/v2"
	"os/exec"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
)

// blobMap injects a deterministic read-blob function for tests. Digest →
// raw bytes.
type blobMap map[digest.Digest][]byte

func (m blobMap) read(d digest.Digest) ([]byte, error) {
	b, ok := m[d]
	if !ok {
		return nil, &missingBlobError{d: d}
	}
	return b, nil
}

type missingBlobError struct{ d digest.Digest }

func (e *missingBlobError) Error() string { return "missing " + e.d.String() }

// pseudoRandom returns deterministic pseudo-random bytes for test inputs.
// Constant-byte inputs compress via RLE into tiny zstd frames, making
// encoding-choice assertions flaky. Pseudo-random bytes do not compress
// without a dictionary — seeded randomness keeps the tests reproducible.
func pseudoRandom(seed uint64, size int) []byte {
	r := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func skipWithoutZstdCLI(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd CLI not found on $PATH; skipping")
	}
}

func TestPlanner_PicksFullWhenPatchLarger(t *testing.T) {
	skipWithoutZstdCLI(t)
	// Two unrelated pseudo-random blobs — the patch cannot exploit overlap,
	// so it must be larger than the full zstd frame. The planner degrades
	// to encoding=full.
	ref := pseudoRandom(1, 1<<15)
	target := pseudoRandom(2, 1<<15)

	refDigest := digest.FromBytes(ref)
	tgtDigest := digest.FromBytes(target)

	baseline := []BaselineLayerMeta{{Digest: refDigest, Size: int64(len(ref)), MediaType: "m"}}
	blobs := blobMap{refDigest: ref, tgtDigest: target}

	p := &Planner{baseline: baseline, readBlob: blobs.read}
	entries, payloads, err := p.Run(context.Background(), []diff.BlobRef{
		{Digest: tgtDigest, Size: int64(len(target)), MediaType: "m"},
	})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, diff.EncodingFull, entries[0].Encoding,
		"independent random pair should degrade to encoding=full")
	require.True(t, bytes.Equal(target, payloads[tgtDigest]),
		"full payload must be verbatim target bytes")
}

func TestPlanner_PicksPatchWhenBytesClose(t *testing.T) {
	skipWithoutZstdCLI(t)
	// Target is reference with a single byte flipped. Random reference
	// means full zstd cannot compress; dictionary-seeded patch produces a
	// tiny frame.
	ref := pseudoRandom(3, 1<<15)
	target := append([]byte(nil), ref...)
	target[0] ^= 0x42

	refDigest := digest.FromBytes(ref)
	tgtDigest := digest.FromBytes(target)

	baseline := []BaselineLayerMeta{{Digest: refDigest, Size: int64(len(ref)), MediaType: "m"}}
	blobs := blobMap{refDigest: ref, tgtDigest: target}

	p := &Planner{baseline: baseline, readBlob: blobs.read}
	entries, payloads, err := p.Run(context.Background(), []diff.BlobRef{
		{Digest: tgtDigest, Size: int64(len(target)), MediaType: "m"},
	})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, diff.EncodingPatch, entries[0].Encoding)
	require.Equal(t, "zstd-patch", entries[0].Codec)
	require.Equal(t, refDigest, entries[0].PatchFromDigest)
	require.Less(t, entries[0].ArchiveSize, entries[0].Size)
	require.Less(t, len(payloads[tgtDigest]), len(target)/2,
		"patch of a near-identical pair should be far smaller than half")
}

func TestPlanner_PicksSizeClosestReferenceDeterministically(t *testing.T) {
	skipWithoutZstdCLI(t)
	small := pseudoRandom(10, 1<<14)
	mid := pseudoRandom(11, 1<<15)
	big := pseudoRandom(12, 1<<16)
	target := append([]byte(nil), mid...) // byte-close to mid
	target[5] ^= 0x42

	baseline := []BaselineLayerMeta{
		{Digest: digest.FromBytes(small), Size: int64(len(small)), MediaType: "m"},
		{Digest: digest.FromBytes(mid), Size: int64(len(mid)), MediaType: "m"},
		{Digest: digest.FromBytes(big), Size: int64(len(big)), MediaType: "m"},
	}
	blobs := blobMap{
		digest.FromBytes(small):  small,
		digest.FromBytes(mid):    mid,
		digest.FromBytes(big):    big,
		digest.FromBytes(target): target,
	}

	p := &Planner{baseline: baseline, readBlob: blobs.read}
	entries, _, err := p.Run(context.Background(), []diff.BlobRef{
		{Digest: digest.FromBytes(target), Size: int64(len(target)), MediaType: "m"},
	})
	require.NoError(t, err)
	require.Equal(t, digest.FromBytes(mid), entries[0].PatchFromDigest,
		"planner must pick the size-closest baseline layer as patch reference")
}

func TestPlanner_EmptyBaselineProducesFullEntries(t *testing.T) {
	skipWithoutZstdCLI(t)
	target := pseudoRandom(20, 1<<10)
	tgtDigest := digest.FromBytes(target)
	blobs := blobMap{tgtDigest: target}
	p := &Planner{baseline: nil, readBlob: blobs.read}

	entries, payloads, err := p.Run(context.Background(), []diff.BlobRef{
		{Digest: tgtDigest, Size: int64(len(target)), MediaType: "m"},
	})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, diff.EncodingFull, entries[0].Encoding,
		"with no baseline layers to diff against, shipped entries must be full")
	require.True(t, bytes.Equal(target, payloads[tgtDigest]))
}

func TestPlanner_SizeTieBrokenByFirstSeen(t *testing.T) {
	skipWithoutZstdCLI(t)
	// Two baseline layers of identical size, both unrelated to target bytes.
	// The target is byte-close to "a" so patch wins; both baselines have the
	// same size so size-closest is a tie — must resolve to first-seen.
	a := pseudoRandom(30, 1<<15)
	b := pseudoRandom(31, 1<<15)
	target := append([]byte(nil), a...)
	target[0] ^= 0xCC

	baseline := []BaselineLayerMeta{
		{Digest: digest.FromBytes(a), Size: int64(len(a)), MediaType: "m"},
		{Digest: digest.FromBytes(b), Size: int64(len(b)), MediaType: "m"},
	}
	blobs := blobMap{
		digest.FromBytes(a):      a,
		digest.FromBytes(b):      b,
		digest.FromBytes(target): target,
	}
	p := &Planner{baseline: baseline, readBlob: blobs.read}
	entries, _, err := p.Run(context.Background(), []diff.BlobRef{
		{Digest: digest.FromBytes(target), Size: int64(len(target)), MediaType: "m"},
	})
	require.NoError(t, err)
	require.Equal(t, digest.FromBytes(a), entries[0].PatchFromDigest,
		"tie: first-seen baseline entry wins")
}

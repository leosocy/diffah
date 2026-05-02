package exporter

import (
	"bytes"
	"context"
	"io"
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
	// Exercises pickClosest's first-seen tie-break (the fallback path used
	// when pickSimilar delegates, typical for non-tar pseudo-random blobs).
	// For pickSimilar's digest-order tie-break see
	// TestPickSimilar_TiedScoreAndSize_BrokenByDigestOrder.
	//
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

// fakeFingerprinter serves a pre-canned fingerprint (or error) per blob,
// keyed by the SHA-256 digest of the blob bytes. Absent keys return an
// empty Fingerprint (not an error) — lets tests assert "this baseline
// fingerprinted, but with no shared content."
type fakeFingerprinter struct {
	fps  map[digest.Digest]Fingerprint
	errs map[digest.Digest]error
}

func (f *fakeFingerprinter) Fingerprint(
	_ context.Context, _ string, blob []byte,
) (Fingerprint, error) {
	key := digest.FromBytes(blob)
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	if fp, ok := f.fps[key]; ok {
		return fp, nil
	}
	return Fingerprint{}, nil
}

func (f *fakeFingerprinter) FingerprintReader(
	ctx context.Context, mediaType string, r io.Reader,
) (Fingerprint, error) {
	blob, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return f.Fingerprint(ctx, mediaType, blob)
}

func TestPlanner_EagerBaselineFingerprinting(t *testing.T) {
	// Two baselines: fp[a] has shared content, fp[b] is empty.
	aBlob := []byte("baseline-a-raw-bytes")
	bBlob := []byte("baseline-b-raw-bytes")
	sharedDigest := digest.FromBytes([]byte("shared-file-content"))

	blobs := blobMap{
		digest.FromBytes(aBlob): aBlob,
		digest.FromBytes(bBlob): bBlob,
	}
	fake := &fakeFingerprinter{
		fps: map[digest.Digest]Fingerprint{
			digest.FromBytes(aBlob): {sharedDigest: 1024},
			digest.FromBytes(bBlob): {},
		},
	}
	baseline := []BaselineLayerMeta{
		{Digest: digest.FromBytes(aBlob), Size: int64(len(aBlob)), MediaType: "m"},
		{Digest: digest.FromBytes(bBlob), Size: int64(len(bBlob)), MediaType: "m"},
	}

	p := NewPlanner(baseline, blobs.read, fake, 0, 0)
	// Run with empty shipped list to trigger ensureBaselineFP without
	// any per-layer work.
	_, _, err := p.Run(context.Background(), nil)
	require.NoError(t, err)

	require.NotNil(t, p.baselineFP)
	require.Equal(t,
		Fingerprint{sharedDigest: 1024},
		p.baselineFP[digest.FromBytes(aBlob)])
	require.Equal(t, Fingerprint{}, p.baselineFP[digest.FromBytes(bBlob)])
}

func TestPlanner_PlanShippedReusesBaselineFingerprint(t *testing.T) {
	skipWithoutZstdCLI(t)
	baseA := pseudoRandom(10, 1<<14)
	baseB := pseudoRandom(11, 1<<14)
	target1 := append([]byte(nil), baseA...)
	target1[0] ^= 0x01
	target2 := append([]byte(nil), baseB...)
	target2[0] ^= 0x02

	baseADigest := digest.FromBytes(baseA)
	baseBDigest := digest.FromBytes(baseB)

	reads := map[digest.Digest]int{}
	readBlob := func(d digest.Digest) ([]byte, error) {
		reads[d]++
		switch d {
		case baseADigest:
			return baseA, nil
		case baseBDigest:
			return baseB, nil
		}
		return nil, &missingBlobError{d: d}
	}

	baseline := []BaselineLayerMeta{
		{Digest: baseADigest, Size: int64(len(baseA)), MediaType: "m"},
		{Digest: baseBDigest, Size: int64(len(baseB)), MediaType: "m"},
	}
	p := NewPlanner(baseline, readBlob, nil, 0, 0)

	ctx := context.Background()
	tgt1 := diff.BlobRef{
		Digest: digest.FromBytes(target1), Size: int64(len(target1)), MediaType: "m",
	}
	tgt2 := diff.BlobRef{
		Digest: digest.FromBytes(target2), Size: int64(len(target2)), MediaType: "m",
	}

	_, _, err := p.PlanShipped(ctx, tgt1, target1)
	require.NoError(t, err)
	// Snapshot counts after first PlanShipped: ensureBaselineFP has read each
	// baseline once, plus pickSimilar has pulled the chosen ref for patching.
	afterFirst := map[digest.Digest]int{
		baseADigest: reads[baseADigest], baseBDigest: reads[baseBDigest],
	}

	_, _, err = p.PlanShipped(ctx, tgt2, target2)
	require.NoError(t, err)

	// The second call must NOT re-run baseline fingerprinting — at most one
	// extra baseline read may occur, for the patch reference it picks.
	delta := (reads[baseADigest] - afterFirst[baseADigest]) +
		(reads[baseBDigest] - afterFirst[baseBDigest])
	require.LessOrEqual(t, delta, 1,
		"second PlanShipped caused %d baseline reads (reads=%v, afterFirst=%v); "+
			"fingerprint cache should mean at most one read (for the patch ref)",
		delta, reads, afterFirst)
}

func TestPlanner_EagerFingerprinting_FailedBaselineIsNil(t *testing.T) {
	aBlob := []byte("baseline-a")
	blobs := blobMap{digest.FromBytes(aBlob): aBlob}
	fake := &fakeFingerprinter{
		errs: map[digest.Digest]error{
			digest.FromBytes(aBlob): ErrFingerprintFailed,
		},
	}
	baseline := []BaselineLayerMeta{
		{Digest: digest.FromBytes(aBlob), Size: int64(len(aBlob)), MediaType: "m"},
	}

	p := NewPlanner(baseline, blobs.read, fake, 0, 0)
	_, _, err := p.Run(context.Background(), nil)
	require.NoError(t, err)

	fp, ok := p.baselineFP[digest.FromBytes(aBlob)]
	require.True(t, ok, "key must be present even when fingerprinting failed")
	require.Nil(t, fp, "failed fingerprint must be recorded as nil")
}

func makeBlob(name string) []byte { return []byte("blob-" + name) }

func TestPickSimilar_TargetFpFails_UsesSizeClosest(t *testing.T) {
	target := makeBlob("target")
	near := makeBlob("near-size")
	far := makeBlob("far-size")
	baseline := []BaselineLayerMeta{
		{Digest: digest.FromBytes(near), Size: 100, MediaType: "m"},
		{Digest: digest.FromBytes(far), Size: 9999, MediaType: "m"},
	}
	blobs := blobMap{
		digest.FromBytes(target): target,
		digest.FromBytes(near):   near,
		digest.FromBytes(far):    far,
	}
	fake := &fakeFingerprinter{
		errs: map[digest.Digest]error{
			digest.FromBytes(target): ErrFingerprintFailed,
		},
		fps: map[digest.Digest]Fingerprint{
			digest.FromBytes(near): {digest.FromBytes([]byte("x")): 10},
			digest.FromBytes(far):  {digest.FromBytes([]byte("y")): 10},
		},
	}
	p := NewPlanner(baseline, blobs.read, fake, 0, 0)
	p.ensureBaselineFP(context.Background())

	got, ok := p.pickSimilar(nil, 120)
	require.True(t, ok)
	require.Equal(t, digest.FromBytes(near), got.Digest)
}

func TestPickSimilar_AllCandidateFpFail_UsesSizeClosest(t *testing.T) {
	target := makeBlob("target")
	near := makeBlob("near-size")
	far := makeBlob("far-size")
	_ = target
	baseline := []BaselineLayerMeta{
		{Digest: digest.FromBytes(near), Size: 100, MediaType: "m"},
		{Digest: digest.FromBytes(far), Size: 9999, MediaType: "m"},
	}
	blobs := blobMap{
		digest.FromBytes(near): near,
		digest.FromBytes(far):  far,
	}
	fake := &fakeFingerprinter{
		errs: map[digest.Digest]error{
			digest.FromBytes(near): ErrFingerprintFailed,
			digest.FromBytes(far):  ErrFingerprintFailed,
		},
	}
	p := NewPlanner(baseline, blobs.read, fake, 0, 0)
	p.ensureBaselineFP(context.Background())

	targetFP := Fingerprint{digest.FromBytes([]byte("z")): 10}
	got, ok := p.pickSimilar(targetFP, 120)
	require.True(t, ok)
	require.Equal(t, digest.FromBytes(near), got.Digest)
}

func TestPickSimilar_AllScoresZero_UsesSizeClosest(t *testing.T) {
	target := makeBlob("target")
	near := makeBlob("near-size")
	far := makeBlob("far-size")
	_ = target
	baseline := []BaselineLayerMeta{
		{Digest: digest.FromBytes(near), Size: 100, MediaType: "m"},
		{Digest: digest.FromBytes(far), Size: 9999, MediaType: "m"},
	}
	blobs := blobMap{
		digest.FromBytes(near): near,
		digest.FromBytes(far):  far,
	}
	xFile := digest.FromBytes([]byte("x"))
	yFile := digest.FromBytes([]byte("y"))
	zFile := digest.FromBytes([]byte("z"))
	fake := &fakeFingerprinter{
		fps: map[digest.Digest]Fingerprint{
			digest.FromBytes(near): {xFile: 10},
			digest.FromBytes(far):  {yFile: 10},
		},
	}
	p := NewPlanner(baseline, blobs.read, fake, 0, 0)
	p.ensureBaselineFP(context.Background())

	targetFP := Fingerprint{zFile: 100}
	got, ok := p.pickSimilar(targetFP, 120)
	require.True(t, ok)
	require.Equal(t, digest.FromBytes(near), got.Digest)
}

func TestPickSimilar_SingleWinnerByScore(t *testing.T) {
	near := makeBlob("near-size-content-disjoint")
	far := makeBlob("far-size-content-match")
	baseline := []BaselineLayerMeta{
		{Digest: digest.FromBytes(near), Size: 100, MediaType: "m"},
		{Digest: digest.FromBytes(far), Size: 9999, MediaType: "m"},
	}
	blobs := blobMap{
		digest.FromBytes(near): near,
		digest.FromBytes(far):  far,
	}
	sharedFile := digest.FromBytes([]byte("shared"))
	fake := &fakeFingerprinter{
		fps: map[digest.Digest]Fingerprint{
			digest.FromBytes(near): {digest.FromBytes([]byte("x")): 10},
			digest.FromBytes(far):  {sharedFile: 1_000_000},
		},
	}
	p := NewPlanner(baseline, blobs.read, fake, 0, 0)
	p.ensureBaselineFP(context.Background())

	targetFP := Fingerprint{sharedFile: 1_000_000}
	got, ok := p.pickSimilar(targetFP, 120)
	require.True(t, ok)
	require.Equal(t, digest.FromBytes(far), got.Digest,
		"content-match wins despite size-far being the size-closest trap")
}

func TestPickSimilar_TiedScore_BrokenBySize(t *testing.T) {
	nearCorrect := makeBlob("near-correct")
	farCorrect := makeBlob("far-correct")
	baseline := []BaselineLayerMeta{
		{Digest: digest.FromBytes(nearCorrect), Size: 100, MediaType: "m"},
		{Digest: digest.FromBytes(farCorrect), Size: 1000, MediaType: "m"},
	}
	blobs := blobMap{
		digest.FromBytes(nearCorrect): nearCorrect,
		digest.FromBytes(farCorrect):  farCorrect,
	}
	shared := digest.FromBytes([]byte("shared"))
	fake := &fakeFingerprinter{
		fps: map[digest.Digest]Fingerprint{
			digest.FromBytes(nearCorrect): {shared: 500},
			digest.FromBytes(farCorrect):  {shared: 500},
		},
	}
	p := NewPlanner(baseline, blobs.read, fake, 0, 0)
	p.ensureBaselineFP(context.Background())

	targetFP := Fingerprint{shared: 500}
	got, ok := p.pickSimilar(targetFP, 150)
	require.True(t, ok)
	require.Equal(t, digest.FromBytes(nearCorrect), got.Digest,
		"tie on score: size-closest wins")
}

func TestPickSimilar_TiedScoreAndSize_BrokenByDigestOrder(t *testing.T) {
	// Two baselines with equal size and equal score. Tie-break must be
	// the sorted-by-digest order (NewPlanner sorts baseline[] by Digest).
	first := makeBlob("first-digest-alpha")
	second := makeBlob("second-digest-beta")

	// Decide which is lexically smaller; pick the expected winner
	// from sorted order.
	dA := digest.FromBytes(first)
	dB := digest.FromBytes(second)
	expected := dA
	if dB < dA {
		expected = dB
	}

	baseline := []BaselineLayerMeta{
		{Digest: dA, Size: 100, MediaType: "m"},
		{Digest: dB, Size: 100, MediaType: "m"},
	}
	blobs := blobMap{dA: first, dB: second}
	shared := digest.FromBytes([]byte("shared"))
	fake := &fakeFingerprinter{
		fps: map[digest.Digest]Fingerprint{
			dA: {shared: 500},
			dB: {shared: 500},
		},
	}
	p := NewPlanner(baseline, blobs.read, fake, 0, 0)
	p.ensureBaselineFP(context.Background())

	targetFP := Fingerprint{shared: 500}
	got, ok := p.pickSimilar(targetFP, 100)
	require.True(t, ok)
	require.Equal(t, expected, got.Digest)
}

func TestPickSimilar_EmptyBaseline_ReturnsFalse(t *testing.T) {
	p := NewPlanner(nil, func(_ digest.Digest) ([]byte, error) { return nil, nil }, &fakeFingerprinter{}, 0, 0)
	p.ensureBaselineFP(context.Background())
	_, ok := p.pickSimilar(Fingerprint{digest.FromBytes([]byte("x")): 1}, 100)
	require.False(t, ok)
}

// TestPlanShippedTopK_PicksSmallestOfKPatches asserts that with k=2
// and two baselines — one nearly identical to the target, one totally
// foreign — the planner emits the smaller of the two encoded patches
// and labels PatchFromDigest with the better baseline.
func TestPlanShippedTopK_PicksSmallestOfKPatches(t *testing.T) {
	skipWithoutZstdCLI(t)
	target := pseudoRandom(50, 1<<15)
	closeRef := append([]byte(nil), target...) // tiny 2-byte diff
	closeRef[0] ^= 0xAA
	closeRef[1] ^= 0xBB
	farRef := pseudoRandom(51, 1<<15) // unrelated bytes

	dClose := digest.FromBytes(closeRef)
	dFar := digest.FromBytes(farRef)
	dT := digest.FromBytes(target)

	baseline := []BaselineLayerMeta{
		{Digest: dClose, Size: int64(len(closeRef)), MediaType: "m"},
		{Digest: dFar, Size: int64(len(farRef)), MediaType: "m"},
	}
	blobs := blobMap{dClose: closeRef, dFar: farRef, dT: target}
	shared := digest.FromBytes([]byte("shared-content"))
	foreign := digest.FromBytes([]byte("foreign"))
	fake := &fakeFingerprinter{
		fps: map[digest.Digest]Fingerprint{
			dClose: {shared: 4096},
			dFar:   {foreign: 4096},
			dT:     {shared: 4096},
		},
	}

	p := NewPlanner(baseline, blobs.read, fake, 0, 0)
	br := diff.BlobRef{Digest: dT, Size: int64(len(target)), MediaType: "m"}
	entry, payload, err := p.PlanShippedTopK(context.Background(), br, target, 2)
	require.NoError(t, err)
	require.Equal(t, diff.EncodingPatch, entry.Encoding)
	require.Equal(t, dClose, entry.PatchFromDigest, "smaller patch comes from closeRef")
	require.Less(t, int64(len(payload)), int64(len(target)),
		"patch payload (%d) should be smaller than target (%d)", len(payload), len(target))
}

// TestPlanShippedTopK_FallsBackToFullWhenAllPatchesExceedFull asserts
// that when no candidate produces a patch smaller than the full-zstd
// ceiling, the planner emits encoding=full with the target verbatim.
func TestPlanShippedTopK_FallsBackToFullWhenAllPatchesExceedFull(t *testing.T) {
	skipWithoutZstdCLI(t)
	// Two unrelated random blobs — no patch will beat full encoding.
	ref := pseudoRandom(60, 1<<15)
	target := pseudoRandom(61, 1<<15)
	dRef := digest.FromBytes(ref)
	dT := digest.FromBytes(target)

	baseline := []BaselineLayerMeta{
		{Digest: dRef, Size: int64(len(ref)), MediaType: "m"},
	}
	blobs := blobMap{dRef: ref, dT: target}
	fake := &fakeFingerprinter{
		fps: map[digest.Digest]Fingerprint{
			dRef: {digest.FromBytes([]byte("x")): 8},
			dT:   {digest.FromBytes([]byte("y")): 8},
		},
	}

	p := NewPlanner(baseline, blobs.read, fake, 0, 0)
	br := diff.BlobRef{Digest: dT, Size: int64(len(target)), MediaType: "m"}
	entry, _, err := p.PlanShippedTopK(context.Background(), br, target, 1)
	require.NoError(t, err)
	require.Equal(t, diff.EncodingFull, entry.Encoding,
		"unrelated random pair should fall back to full encoding")
}

// TestPlanShippedTopK_K1MatchesPlanShipped asserts byte-identical
// output between PlanShipped (legacy K=1) and PlanShippedTopK with k=1
// — the byte-identical-Phase-3 invariant for default --candidates=1.
func TestPlanShippedTopK_K1MatchesPlanShipped(t *testing.T) {
	skipWithoutZstdCLI(t)
	ref := pseudoRandom(70, 1<<15)
	target := append([]byte(nil), ref...)
	target[0] ^= 0x42
	dRef := digest.FromBytes(ref)
	dT := digest.FromBytes(target)

	baseline := []BaselineLayerMeta{
		{Digest: dRef, Size: int64(len(ref)), MediaType: "m"},
	}
	blobs := blobMap{dRef: ref, dT: target}
	shared := digest.FromBytes([]byte("shared"))
	fake := &fakeFingerprinter{
		fps: map[digest.Digest]Fingerprint{
			dRef: {shared: 16384},
			dT:   {shared: 16384},
		},
	}

	pa := NewPlanner(baseline, blobs.read, fake, 0, 0)
	pb := NewPlanner(baseline, blobs.read, fake, 0, 0)

	br := diff.BlobRef{Digest: dT, Size: int64(len(target)), MediaType: "m"}
	entryA, payloadA, err := pa.PlanShipped(context.Background(), br, target)
	require.NoError(t, err)
	entryB, payloadB, err := pb.PlanShippedTopK(context.Background(), br, target, 1)
	require.NoError(t, err)

	require.Equal(t, entryA.Encoding, entryB.Encoding)
	require.Equal(t, entryA.PatchFromDigest, entryB.PatchFromDigest)
	require.Equal(t, entryA.ArchiveSize, entryB.ArchiveSize)
	require.True(t, bytes.Equal(payloadA, payloadB),
		"payloads differ (lenA=%d, lenB=%d) — K=1 must match PlanShipped byte-for-byte",
		len(payloadA), len(payloadB))
}

// TestPickTopK_PrefersHighestScoreThenSizeClosest verifies that
// PickTopK orders candidates by content-similarity score (desc), with
// size-closest as the tie-break. Uses the existing blobMap +
// fakeFingerprinter helpers because they key fingerprints off
// digest.FromBytes(blob) — the same contract Planner.ensureBaselineFP
// honors when reading baselines through readBlob.
func TestPickTopK_PrefersHighestScoreThenSizeClosest(t *testing.T) {
	a := makeBlob("baseline-a")
	b := makeBlob("baseline-b")
	c := makeBlob("baseline-c")
	dA := digest.FromBytes(a)
	dB := digest.FromBytes(b)
	dC := digest.FromBytes(c)

	baseline := []BaselineLayerMeta{
		{Digest: dA, Size: 100, MediaType: "m"},
		{Digest: dB, Size: 200, MediaType: "m"},
		{Digest: dC, Size: 300, MediaType: "m"},
	}
	blobs := blobMap{dA: a, dB: b, dC: c}
	shared := digest.FromBytes([]byte("shared"))
	foreign := digest.FromBytes([]byte("foreign"))
	fake := &fakeFingerprinter{
		fps: map[digest.Digest]Fingerprint{
			dA: {shared: 50},                         // score 50
			dB: {shared: 80, foreign: 40},            // score 80 (highest)
			dC: {digest.FromBytes([]byte("x")): 100}, // score 0 (no overlap)
		},
	}
	p := NewPlanner(baseline, blobs.read, fake, 0, 0)
	target := Fingerprint{shared: 100}

	got := p.PickTopK(context.Background(), target, 150, 2)
	require.Len(t, got, 2)
	require.Equal(t, dB, got[0].Digest, "got[0] should be highest score")
	require.Equal(t, dA, got[1].Digest, "got[1] should be next-highest score")
}

// TestPickTopK_FallsBackToSizeClosestWhenNoFingerprint asserts that with
// targetFP=nil the planner ranks by size-closest only.
func TestPickTopK_FallsBackToSizeClosestWhenNoFingerprint(t *testing.T) {
	a := makeBlob("baseline-a")
	b := makeBlob("baseline-b")
	dA := digest.FromBytes(a)
	dB := digest.FromBytes(b)

	baseline := []BaselineLayerMeta{
		{Digest: dA, Size: 100, MediaType: "m"},
		{Digest: dB, Size: 250, MediaType: "m"},
	}
	blobs := blobMap{dA: a, dB: b}
	p := NewPlanner(baseline, blobs.read, &fakeFingerprinter{}, 0, 0)

	got := p.PickTopK(context.Background(), nil, 240, 2) // target size 240 → dB (250) is closer
	require.Len(t, got, 2)
	require.Equal(t, dB, got[0].Digest, "got[0] should be size-closest")
	require.Equal(t, dA, got[1].Digest)
}

// TestPickTopK_ClampsAtAvailableCount asserts that requesting more
// candidates than available returns whatever is present (no padding,
// no panic).
func TestPickTopK_ClampsAtAvailableCount(t *testing.T) {
	a := makeBlob("baseline-a")
	dA := digest.FromBytes(a)
	baseline := []BaselineLayerMeta{{Digest: dA, Size: 100, MediaType: "m"}}
	blobs := blobMap{dA: a}
	shared := digest.FromBytes([]byte("shared"))
	fake := &fakeFingerprinter{
		fps: map[digest.Digest]Fingerprint{dA: {shared: 50}},
	}
	p := NewPlanner(baseline, blobs.read, fake, 0, 0)

	got := p.PickTopK(context.Background(), Fingerprint{shared: 50}, 100, 5)
	require.Len(t, got, 1)
}

// TestPlanner_Run_PrefersContentMatchOverSizeClosest verifies the full
// Run path: target blob is non-tar (fingerprinter fails naturally with
// DefaultFingerprinter, but we inject content-aware fake), and the
// planner must choose the content winner rather than the size winner.
func TestPlanner_Run_PrefersContentMatchOverSizeClosest(t *testing.T) {
	skipWithoutZstdCLI(t)
	near := pseudoRandom(40, 1<<15)
	far := pseudoRandom(41, 1<<18)
	target := pseudoRandom(42, 1<<15) // byte-size near "near" but content-matches "far"
	shared := digest.FromBytes([]byte("shared-content-tag"))

	baseline := []BaselineLayerMeta{
		{Digest: digest.FromBytes(near), Size: int64(len(near)), MediaType: "m"},
		{Digest: digest.FromBytes(far), Size: int64(len(far)), MediaType: "m"},
	}
	blobs := blobMap{
		digest.FromBytes(target): target,
		digest.FromBytes(near):   near,
		digest.FromBytes(far):    far,
	}
	fake := &fakeFingerprinter{
		fps: map[digest.Digest]Fingerprint{
			digest.FromBytes(target): {shared: 1_000_000},
			digest.FromBytes(far):    {shared: 1_000_000},
			// "near" has an empty fingerprint → score 0.
			digest.FromBytes(near): {},
		},
	}

	p := NewPlanner(baseline, blobs.read, fake, 0, 0)
	entries, _, err := p.Run(context.Background(), []diff.BlobRef{
		{Digest: digest.FromBytes(target), Size: int64(len(target)), MediaType: "m"},
	})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	// Regardless of whether patch beats full on these random bytes, the
	// picked baseline digest must be "far" (the content winner).
	if entries[0].Encoding == diff.EncodingPatch {
		require.Equal(t, digest.FromBytes(far), entries[0].PatchFromDigest,
			"content-match baseline must be picked when scores disagree with size")
	}
}

func TestResolveWindowLog(t *testing.T) {
	tests := []struct {
		name      string
		userValue int
		layerSize int64
		want      int
	}{
		{"explicit override beats auto", 30, 64 << 20, 30},
		{"explicit override floor", 10, 1 << 30, 10},
		{"auto small", 0, 64 << 20, 27},
		{"auto medium boundary inclusive", 0, 128 << 20, 27},
		{"auto medium just over", 0, (128 << 20) + 1, 30},
		{"auto large", 0, 512 << 20, 30},
		{"auto large boundary inclusive", 0, 1 << 30, 30},
		{"auto huge", 0, (1 << 30) + 1, 31},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveWindowLog(tc.userValue, tc.layerSize)
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// countingFingerprinter wraps fakeFingerprinter and counts the number
// of Fingerprint() invocations per digest, so tests can assert that
// SeedBaselineFingerprints actually short-circuits re-fingerprinting.
type countingFingerprinter struct {
	inner *fakeFingerprinter
	calls map[digest.Digest]int
}

func (c *countingFingerprinter) Fingerprint(
	ctx context.Context, mt string, blob []byte,
) (Fingerprint, error) {
	c.calls[digest.FromBytes(blob)]++
	return c.inner.Fingerprint(ctx, mt, blob)
}

func (c *countingFingerprinter) FingerprintReader(
	ctx context.Context, mediaType string, r io.Reader,
) (Fingerprint, error) {
	blob, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return c.Fingerprint(ctx, mediaType, blob)
}

func TestPlanner_SeedBaselineFingerprintsAvoidsReFingerprinting(t *testing.T) {
	aBlob := []byte("baseline-alpha-bytes")
	bBlob := []byte("baseline-beta-bytes")
	aDigest := digest.FromBytes(aBlob)
	bDigest := digest.FromBytes(bBlob)

	blobs := blobMap{aDigest: aBlob, bDigest: bBlob}
	baseline := []BaselineLayerMeta{
		{Digest: aDigest, Size: int64(len(aBlob)), MediaType: "x"},
		{Digest: bDigest, Size: int64(len(bBlob)), MediaType: "x"},
	}

	counter := &countingFingerprinter{
		inner: &fakeFingerprinter{
			fps: map[digest.Digest]Fingerprint{
				aDigest: {"shared": 10},
				bDigest: {"shared": 5},
			},
		},
		calls: make(map[digest.Digest]int),
	}

	p := NewPlanner(baseline, blobs.read, counter, 0, 0)
	// Seed only one of the two — exercises both paths in ensureBaselineFP.
	p.SeedBaselineFingerprints(map[digest.Digest]Fingerprint{
		aDigest: {"shared": 10},
	})
	p.ensureBaselineFP(context.Background())

	require.Equal(t, 0, counter.calls[aDigest],
		"seeded baseline a must not be re-fingerprinted")
	require.Equal(t, 1, counter.calls[bDigest],
		"unseeded baseline b must be fingerprinted exactly once")
}

func TestPlanner_SeedBaselineFingerprintsHonorsNilSentinel(t *testing.T) {
	aBlob := []byte("baseline-prime-fail")
	aDigest := digest.FromBytes(aBlob)
	blobs := blobMap{aDigest: aBlob}
	baseline := []BaselineLayerMeta{
		{Digest: aDigest, Size: int64(len(aBlob)), MediaType: "x"},
	}

	counter := &countingFingerprinter{
		inner: &fakeFingerprinter{},
		calls: make(map[digest.Digest]int),
	}

	p := NewPlanner(baseline, blobs.read, counter, 0, 0)
	// nil sentinel = "fingerprint failed during E1; do not retry".
	p.SeedBaselineFingerprints(map[digest.Digest]Fingerprint{aDigest: nil})
	p.ensureBaselineFP(context.Background())

	require.Equal(t, 0, counter.calls[aDigest],
		"nil-seeded baseline must not be re-fingerprinted")
	require.Nil(t, p.baselineFP[aDigest],
		"nil sentinel must be preserved in baselineFP")
}

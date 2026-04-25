package exporter

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
)

func TestBlobPool_AddIfAbsentAndRefCount(t *testing.T) {
	p := newBlobPool()
	d := digest.Digest("sha256:aa")
	p.addIfAbsent(d, []byte("hi"), diff.BlobEntry{Size: 2, Encoding: diff.EncodingFull, ArchiveSize: 2})
	p.addIfAbsent(d, []byte("REPLACED"), diff.BlobEntry{Size: 8, Encoding: diff.EncodingFull, ArchiveSize: 8})
	bytes, ok := p.get(d)
	require.True(t, ok)
	require.Equal(t, "hi", string(bytes), "first write wins")

	p.countShipped(d)
	p.countShipped(d)
	require.Equal(t, 2, p.refCount(d))
}

func TestBlobPool_SeedManifestAndConfig(t *testing.T) {
	ctx := context.Background()
	p1, err := planPair(ctx, Pair{Name: "a", BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
		TargetRef: "oci-archive:../../testdata/fixtures/v2_oci.tar"}, &Options{Platform: "linux/amd64"})
	require.NoError(t, err)
	p2, err := planPair(ctx, Pair{Name: "b", BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
		TargetRef: "oci-archive:../../testdata/fixtures/v2_oci.tar"}, &Options{Platform: "linux/amd64"})
	require.NoError(t, err)

	pool := newBlobPool()
	seedManifestAndConfig(pool, p1)
	seedManifestAndConfig(pool, p2)

	mfDigest := digest.FromBytes(p1.TargetManifest)
	require.True(t, pool.has(mfDigest))
	require.True(t, pool.has(p1.TargetConfigDesc.Digest))
	require.Len(t, pool.sortedDigests(), 2, "same target → dedup to 2 unique blobs")
}

func TestEncodeShipped_ForcesFullOnCrossImageDup(t *testing.T) {
	ctx := context.Background()
	p1, err := planPair(ctx, Pair{Name: "a",
		BaselineRef: "oci-archive:../../testdata/fixtures/v2_oci.tar",
		TargetRef:   "oci-archive:../../testdata/fixtures/v3_oci.tar"}, &Options{Platform: "linux/amd64"})
	require.NoError(t, err)
	p2, err := planPair(ctx, Pair{Name: "b",
		BaselineRef: "oci-archive:../../testdata/fixtures/v2_oci.tar",
		TargetRef:   "oci-archive:../../testdata/fixtures/v3_oci.tar"}, &Options{Platform: "linux/amd64"})
	require.NoError(t, err)

	pool := newBlobPool()
	seedManifestAndConfig(pool, p1)
	seedManifestAndConfig(pool, p2)
	for _, p := range []*pairPlan{p1, p2} {
		for _, s := range p.Shipped {
			pool.countShipped(s.Digest)
		}
	}
	require.NoError(t, encodeShipped(ctx, pool, []*pairPlan{p1, p2}, "off", nil, nil, 0, 0, 0, 0))

	for _, s := range p1.Shipped {
		entry := pool.entries[s.Digest]
		require.Equal(t, diff.EncodingFull, entry.Encoding, "shared shipped must be full")
	}
}

func TestBlobPool_ConcurrentAddIsSafe(t *testing.T) {
	p := newBlobPool()
	const N = 64
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := digest.FromBytes([]byte(fmt.Sprintf("blob-%d", i)))
			p.addIfAbsent(d, []byte("x"), diff.BlobEntry{Size: 1})
		}()
	}
	wg.Wait()
	if got := len(p.sortedDigests()); got != N {
		t.Fatalf("digests = %d, want %d", got, N)
	}
}

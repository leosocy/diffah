package exporter

import (
	"context"
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
	p1, err := planPair(ctx, Pair{Name: "a", BaselinePath: "../../testdata/fixtures/v1_oci.tar",
		TargetPath: "../../testdata/fixtures/v2_oci.tar"}, "linux/amd64")
	require.NoError(t, err)
	p2, err := planPair(ctx, Pair{Name: "b", BaselinePath: "../../testdata/fixtures/v1_oci.tar",
		TargetPath: "../../testdata/fixtures/v2_oci.tar"}, "linux/amd64")
	require.NoError(t, err)

	pool := newBlobPool()
	seedManifestAndConfig(pool, p1)
	seedManifestAndConfig(pool, p2)

	mfDigest := digest.FromBytes(p1.TargetManifest)
	require.True(t, pool.has(mfDigest))
	require.True(t, pool.has(p1.TargetConfigDesc.Digest))
	require.Len(t, pool.sortedDigests(), 2, "same target → dedup to 2 unique blobs")
}

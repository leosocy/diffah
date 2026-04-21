package exporter

import (
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

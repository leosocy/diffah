package importer

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/types"
)

type fakeSrc struct {
	blobs map[digest.Digest][]byte
	hits  []digest.Digest
}

func (s *fakeSrc) Reference() types.ImageReference { return nil }
func (s *fakeSrc) Close() error                    { return nil }
func (s *fakeSrc) GetManifest(context.Context, *digest.Digest) ([]byte, string, error) {
	return nil, "", nil
}
func (s *fakeSrc) HasThreadSafeGetBlob() bool { return false }
func (s *fakeSrc) GetSignatures(_ context.Context, _ *digest.Digest) ([][]byte, error) {
	return nil, nil
}
func (s *fakeSrc) LayerInfosForCopy(_ context.Context, _ *digest.Digest) ([]types.BlobInfo, error) {
	return nil, nil
}
func (s *fakeSrc) GetBlob(_ context.Context, bi types.BlobInfo, _ types.BlobInfoCache) (io.ReadCloser, int64, error) {
	s.hits = append(s.hits, bi.Digest)
	b, ok := s.blobs[bi.Digest]
	if !ok {
		return nil, 0, io.ErrUnexpectedEOF
	}
	return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
}

func TestLazyBlobFetcher_FetchesOnlyRequestedDigests(t *testing.T) {
	digestA := digest.FromBytes([]byte("a"))
	digestB := digest.FromBytes([]byte("b"))
	src := &fakeSrc{blobs: map[digest.Digest][]byte{
		digestA: []byte("a"),
		digestB: []byte("b"),
	}}
	f := newLazyBlobFetcher(src)

	got, err := f.Fetch(context.Background(), digestA)
	require.NoError(t, err)
	require.Equal(t, []byte("a"), got)
	require.Equal(t, []digest.Digest{digestA}, src.hits)

	got, err = f.Fetch(context.Background(), digestB)
	require.NoError(t, err)
	require.Equal(t, []byte("b"), got)
	require.Equal(t, []digest.Digest{digestA, digestB}, src.hits)
}

func TestLazyBlobFetcher_MissingDigestBubbles(t *testing.T) {
	src := &fakeSrc{blobs: map[digest.Digest][]byte{}}
	f := newLazyBlobFetcher(src)
	_, err := f.Fetch(context.Background(), digest.FromBytes([]byte("x")))
	require.Error(t, err)
}

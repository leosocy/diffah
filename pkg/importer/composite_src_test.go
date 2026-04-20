package importer

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/types"
)

// fakeSource implements types.ImageSource. GetBlob returns a strings.Reader
// for digests in blobs; missing digests return an error wrapping os.ErrNotExist.
type fakeSource struct {
	blobs       map[digest.Digest]string
	manifestRaw []byte
	manifestMT  string
	closeCalls  int
}

func (f *fakeSource) Reference() types.ImageReference { return nil }
func (f *fakeSource) Close() error                    { f.closeCalls++; return nil }
func (f *fakeSource) GetManifest(_ context.Context, _ *digest.Digest) ([]byte, string, error) {
	return f.manifestRaw, f.manifestMT, nil
}
func (f *fakeSource) GetBlob(_ context.Context, info types.BlobInfo, _ types.BlobInfoCache) (io.ReadCloser, int64, error) {
	if data, ok := f.blobs[info.Digest]; ok {
		return io.NopCloser(strings.NewReader(data)), int64(len(data)), nil
	}
	return nil, 0, fmt.Errorf("blob %s: %w", info.Digest, os.ErrNotExist)
}
func (f *fakeSource) HasThreadSafeGetBlob() bool { return false }
func (f *fakeSource) GetSignatures(_ context.Context, _ *digest.Digest) ([][]byte, error) {
	return nil, nil
}
func (f *fakeSource) LayerInfosForCopy(_ context.Context, _ *digest.Digest) ([]types.BlobInfo, error) {
	return nil, nil
}

func TestComposite_GetBlob_PrefersDelta(t *testing.T) {
	delta := &fakeSource{blobs: map[digest.Digest]string{"sha256:a": "from-delta"}}
	baseline := &fakeSource{blobs: map[digest.Digest]string{"sha256:a": "from-baseline"}}
	c := NewCompositeSource(delta, baseline)

	r, sz, err := c.GetBlob(context.Background(), types.BlobInfo{Digest: "sha256:a"}, nil)
	require.NoError(t, err)
	require.Equal(t, int64(len("from-delta")), sz)
	defer r.Close()
	b, _ := io.ReadAll(r)
	require.Equal(t, "from-delta", string(b))
}

func TestComposite_GetBlob_FallsBackToBaseline(t *testing.T) {
	delta := &fakeSource{blobs: map[digest.Digest]string{}}
	baseline := &fakeSource{blobs: map[digest.Digest]string{"sha256:b": "from-baseline"}}
	c := NewCompositeSource(delta, baseline)

	r, _, err := c.GetBlob(context.Background(), types.BlobInfo{Digest: "sha256:b"}, nil)
	require.NoError(t, err)
	defer r.Close()
	b, _ := io.ReadAll(r)
	require.Equal(t, "from-baseline", string(b))
}

func TestComposite_GetBlob_MissingEverywhere(t *testing.T) {
	c := NewCompositeSource(&fakeSource{}, &fakeSource{})
	_, _, err := c.GetBlob(context.Background(), types.BlobInfo{Digest: "sha256:z"}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sha256:z")
}

func TestComposite_GetManifest_UsesDelta(t *testing.T) {
	delta := &fakeSource{manifestRaw: []byte("delta-manifest"), manifestMT: "application/delta"}
	baseline := &fakeSource{manifestRaw: []byte("baseline-manifest"), manifestMT: "application/baseline"}
	c := NewCompositeSource(delta, baseline)

	raw, mt, err := c.GetManifest(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "delta-manifest", string(raw))
	require.Equal(t, "application/delta", mt)
}

func TestComposite_Close_ClosesBoth(t *testing.T) {
	delta := &fakeSource{}
	baseline := &fakeSource{}
	c := NewCompositeSource(delta, baseline)
	require.NoError(t, c.Close())
	require.Equal(t, 1, delta.closeCalls)
	require.Equal(t, 1, baseline.closeCalls)
}

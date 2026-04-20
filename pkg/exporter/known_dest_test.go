package exporter

import (
	"context"
	"io"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/types"
)

// fakeDest is a minimal ImageDestination implementation recording calls to
// exercise KnownBlobsDest delegation and short-circuit behavior.
type fakeDest struct {
	tryReuseCalls int
	putBlobCalls  int
	putBlobInfos  []types.BlobInfo

	tryReuseReturnReused bool
	tryReuseReturnInfo   types.BlobInfo
	tryReuseReturnErr    error

	putBlobReturnInfo types.BlobInfo
	putBlobReturnErr  error

	closeCalled bool
}

func (f *fakeDest) Reference() types.ImageReference                  { return nil }
func (f *fakeDest) Close() error                                     { f.closeCalled = true; return nil }
func (f *fakeDest) SupportedManifestMIMETypes() []string             { return nil }
func (f *fakeDest) SupportsSignatures(context.Context) error         { return nil }
func (f *fakeDest) DesiredLayerCompression() types.LayerCompression  { return types.PreserveOriginal }
func (f *fakeDest) AcceptsForeignLayerURLs() bool                    { return false }
func (f *fakeDest) MustMatchRuntimeOS() bool                         { return false }
func (f *fakeDest) IgnoresEmbeddedDockerReference() bool             { return false }
func (f *fakeDest) HasThreadSafePutBlob() bool                       { return false }

func (f *fakeDest) PutBlob(_ context.Context, stream io.Reader, info types.BlobInfo, _ types.BlobInfoCache, _ bool) (types.BlobInfo, error) {
	f.putBlobCalls++
	f.putBlobInfos = append(f.putBlobInfos, info)
	// Drain the stream per contract (not strictly required in tests).
	if stream != nil {
		_, _ = io.Copy(io.Discard, stream)
	}
	return f.putBlobReturnInfo, f.putBlobReturnErr
}

func (f *fakeDest) TryReusingBlob(_ context.Context, _ types.BlobInfo, _ types.BlobInfoCache, _ bool) (bool, types.BlobInfo, error) {
	f.tryReuseCalls++
	return f.tryReuseReturnReused, f.tryReuseReturnInfo, f.tryReuseReturnErr
}

func (f *fakeDest) PutManifest(context.Context, []byte, *digest.Digest) error          { return nil }
func (f *fakeDest) PutSignatures(context.Context, [][]byte, *digest.Digest) error      { return nil }
func (f *fakeDest) Commit(context.Context, types.UnparsedImage) error                  { return nil }

func TestKnownBlobsDest_TryReusingBlob_ShortCircuitsKnownDigest(t *testing.T) {
	known := []digest.Digest{"sha256:a", "sha256:b"}
	base := &fakeDest{}
	wrapped := NewKnownBlobsDest(base, known)

	reused, info, err := wrapped.TryReusingBlob(context.Background(),
		types.BlobInfo{Digest: "sha256:a", Size: 100}, nil, false)
	require.NoError(t, err)
	require.True(t, reused)
	require.Equal(t, digest.Digest("sha256:a"), info.Digest)
	require.Equal(t, int64(100), info.Size)
	require.Zero(t, base.tryReuseCalls, "known digest must not hit underlying dest")
}

func TestKnownBlobsDest_TryReusingBlob_DelegatesUnknownDigest(t *testing.T) {
	base := &fakeDest{tryReuseReturnReused: false}
	wrapped := NewKnownBlobsDest(base, []digest.Digest{"sha256:a"})

	reused, _, err := wrapped.TryReusingBlob(context.Background(),
		types.BlobInfo{Digest: "sha256:z", Size: 1}, nil, false)
	require.NoError(t, err)
	require.False(t, reused)
	require.Equal(t, 1, base.tryReuseCalls)
}

func TestKnownBlobsDest_PutBlob_AlwaysDelegates(t *testing.T) {
	base := &fakeDest{putBlobReturnInfo: types.BlobInfo{Digest: "sha256:z", Size: 1}}
	wrapped := NewKnownBlobsDest(base, []digest.Digest{"sha256:a"})

	// Even a "known" digest must delegate PutBlob — PutBlob exists only when
	// copy.Image couldn't short-circuit via TryReusingBlob.
	_, err := wrapped.PutBlob(context.Background(), nil,
		types.BlobInfo{Digest: "sha256:a", Size: 1}, nil, false)
	require.NoError(t, err)
	require.Equal(t, 1, base.putBlobCalls)
}

func TestKnownBlobsDest_Close_Delegates(t *testing.T) {
	base := &fakeDest{}
	wrapped := NewKnownBlobsDest(base, nil)
	require.NoError(t, wrapped.Close())
	require.True(t, base.closeCalled)
}

func TestNewKnownBlobsDest_EmptyKnownSetDelegatesAll(t *testing.T) {
	base := &fakeDest{tryReuseReturnReused: false}
	wrapped := NewKnownBlobsDest(base, nil)

	_, _, err := wrapped.TryReusingBlob(context.Background(),
		types.BlobInfo{Digest: "sha256:x"}, nil, false)
	require.NoError(t, err)
	require.Equal(t, 1, base.tryReuseCalls)
}

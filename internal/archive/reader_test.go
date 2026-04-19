package archive

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeTarWithoutSidecar writes a bare tar (no diffah.json) for negative tests.
func writeTarWithoutSidecar(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "no_sidecar.tar")
	f, err := os.Create(out)
	require.NoError(t, err)
	defer f.Close()
	tw := tar.NewWriter(f)
	hdr := &tar.Header{Name: "only.txt", Mode: 0o644, Size: 5}
	require.NoError(t, tw.WriteHeader(hdr))
	_, err = tw.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	return out
}

func TestExtract_RoundTripWithPack(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o644))

	out := filepath.Join(t.TempDir(), "d.tar")
	require.NoError(t, Pack(src, []byte(`{"v":1}`), out, CompressNone))

	dest := t.TempDir()
	sidecar, err := Extract(out, dest)
	require.NoError(t, err)
	require.Equal(t, `{"v":1}`, string(sidecar))

	got, err := os.ReadFile(filepath.Join(dest, "a.txt"))
	require.NoError(t, err)
	require.Equal(t, "hello", string(got))
}

func TestExtract_AutoDetectsZstd(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "x"), []byte("y"), 0o644))
	out := filepath.Join(t.TempDir(), "d.tar.zst")
	require.NoError(t, Pack(src, []byte("{}"), out, CompressZstd))

	dest := t.TempDir()
	sidecar, err := Extract(out, dest)
	require.NoError(t, err)
	require.Equal(t, "{}", string(sidecar))
}

func TestReadSidecar_WithoutFullExtract(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "manifest.json"), []byte("manifest-body"), 0o644))
	out := filepath.Join(t.TempDir(), "d.tar")
	require.NoError(t, Pack(src, []byte(`{"version":"v1"}`), out, CompressNone))

	got, err := ReadSidecar(out)
	require.NoError(t, err)
	require.Equal(t, `{"version":"v1"}`, string(got))
}

func TestReadSidecar_ErrorWhenMissing(t *testing.T) {
	out := writeTarWithoutSidecar(t)
	_, err := ReadSidecar(out)
	require.Error(t, err)
	require.ErrorContains(t, err, "missing")
}

func TestExtract_ErrorWhenSidecarMissing(t *testing.T) {
	out := writeTarWithoutSidecar(t)
	dest := t.TempDir()
	_, err := Extract(out, dest)
	require.Error(t, err)
	require.ErrorContains(t, err, "missing")
}

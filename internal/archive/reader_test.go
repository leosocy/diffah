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

func TestSafeJoin(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "root")

	t.Run("accepts safe names", func(t *testing.T) {
		cases := []string{
			"file.txt",
			"sub/file.txt",
			"./file.txt",
			"a/b/../c/file.txt", // collapses to a/c/file.txt, still inside
		}
		for _, name := range cases {
			got, err := safeJoin(dest, name)
			require.NoError(t, err, "name %q should be accepted", name)
			require.Contains(t, got, dest)
		}
	})

	t.Run("rejects escaping names", func(t *testing.T) {
		cases := []string{
			"../etc/passwd",
			"..",
			"foo/../../etc",
			"/absolute/path",
			"a/b/../../../outside",
		}
		for _, name := range cases {
			_, err := safeJoin(dest, name)
			require.Error(t, err, "name %q must be rejected", name)
			require.ErrorContains(t, err, "escapes destination")
		}
	})
}

// TestExtract_RejectsPathTraversal crafts a tar whose entry name would escape
// the destination when naively joined. Extract must reject it before any file
// lands on disk.
func TestExtract_RejectsPathTraversal(t *testing.T) {
	out := filepath.Join(t.TempDir(), "evil.tar")
	f, err := os.Create(out)
	require.NoError(t, err)
	tw := tar.NewWriter(f)
	payload := []byte("pwn")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "../escape.txt",
		Mode: 0o644,
		Size: int64(len(payload)),
	}))
	_, err = tw.Write(payload)
	require.NoError(t, err)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "diffah.json", Mode: 0o644, Size: 2,
	}))
	_, err = tw.Write([]byte("{}"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, f.Close())

	dest := t.TempDir()
	_, err = Extract(out, dest)
	require.Error(t, err)
	require.ErrorContains(t, err, "escapes destination")

	// Nothing must have landed in the parent of dest.
	_, statErr := os.Stat(filepath.Join(filepath.Dir(dest), "escape.txt"))
	require.True(t, os.IsNotExist(statErr), "escape.txt must not exist outside dest")
}

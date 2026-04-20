package archive

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func listTarEntries(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		names = append(names, hdr.Name)
	}
	return names, nil
}

func TestPack_TarContainsDirAndSidecar(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "manifest.json"), []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(src, "version"), []byte("v"), 0o644))

	out := filepath.Join(t.TempDir(), "delta.tar")
	err := Pack(src, []byte(`{"version":"v1"}`), out, CompressNone)
	require.NoError(t, err)

	got, err := listTarEntries(out)
	require.NoError(t, err)
	require.Contains(t, got, "manifest.json")
	require.Contains(t, got, "version")
	require.Contains(t, got, "diffah.json")
}

func TestPack_AtomicRename_DoesNotLeaveTmpFile(t *testing.T) {
	src := t.TempDir()
	out := filepath.Join(t.TempDir(), "delta.tar")
	require.NoError(t, Pack(src, []byte("{}"), out, CompressNone))

	_, err := os.Stat(out + ".tmp")
	require.True(t, os.IsNotExist(err))
}

func TestPack_ZstdCompressionProducesValidStream(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "m.json"), []byte("{}"), 0o644))

	out := filepath.Join(t.TempDir(), "delta.tar.zst")
	require.NoError(t, Pack(src, []byte("{}"), out, CompressZstd))

	// Minimal zstd magic check: file starts with 0x28 B5 2F FD.
	f, err := os.Open(out)
	require.NoError(t, err)
	defer f.Close()
	buf := make([]byte, 4)
	_, err = f.Read(buf)
	require.NoError(t, err)
	require.Equal(t, []byte{0x28, 0xB5, 0x2F, 0xFD}, buf)
}

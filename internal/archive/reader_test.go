package archive

import (
	"archive/tar"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/diff"
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

func TestReadSidecarAndManifestBlobs_SingleBlob(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "bundle.tar")

	manifestBytes := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","layers":[]}`)
	manifestDigest := digest.FromBytes(manifestBytes)

	sc := minimalSidecarForBlobTest(t, manifestDigest, len(manifestBytes))
	scBytes, err := json.Marshal(sc)
	require.NoError(t, err)

	writeBundleTar(t, out, map[string][]byte{
		diff.SidecarFilename: scBytes,
		"blobs/" + manifestDigest.Algorithm().String() + "/" + manifestDigest.Encoded(): manifestBytes,
	})

	gotSidecar, gotBlobs, err := ReadSidecarAndManifestBlobs(out, []digest.Digest{manifestDigest})
	require.NoError(t, err)
	require.JSONEq(t, string(scBytes), string(gotSidecar))
	require.Len(t, gotBlobs, 1)
	require.Equal(t, manifestBytes, gotBlobs[manifestDigest])
}

func minimalSidecarForBlobTest(t *testing.T, mfDigest digest.Digest, mfSize int) *diff.Sidecar {
	t.Helper()
	return &diff.Sidecar{
		Version:     diff.SchemaVersionV1,
		Feature:     diff.FeatureBundle,
		Tool:        "diffah",
		ToolVersion: "v0-test",
		CreatedAt:   time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
		Platform:    "linux/amd64",
		Images: []diff.ImageEntry{{
			Name: "svc",
			Target: diff.TargetRef{
				ManifestDigest: mfDigest,
				ManifestSize:   int64(mfSize),
				MediaType:      "application/vnd.oci.image.manifest.v1+json",
			},
			Baseline: diff.BaselineRef{
				ManifestDigest: digest.Digest("sha256:" + strings.Repeat("b", 64)),
				MediaType:      "application/vnd.oci.image.manifest.v1+json",
			},
		}},
		Blobs: map[digest.Digest]diff.BlobEntry{
			mfDigest: {Size: int64(mfSize), MediaType: "application/vnd.oci.image.manifest.v1+json", Encoding: diff.EncodingFull, ArchiveSize: int64(mfSize)},
		},
	}
}

func writeBundleTar(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	tw := tar.NewWriter(f)
	for name, data := range entries {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: name, Size: int64(len(data)), Mode: 0o644, Format: tar.FormatPAX,
		}))
		_, err := tw.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
}

func TestReadSidecarAndManifestBlobs_MultipleBlobs(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "bundle.tar")

	mf1 := []byte(`{"schemaVersion":2,"layers":[]}`)
	mf2 := []byte(`{"schemaVersion":2,"layers":[{"digest":"sha256:abc","size":7}]}`)
	d1 := digest.FromBytes(mf1)
	d2 := digest.FromBytes(mf2)

	sc := minimalSidecarForBlobTest(t, d1, len(mf1))
	sc.Images = append(sc.Images, diff.ImageEntry{
		Name: "svc-2",
		Target: diff.TargetRef{
			ManifestDigest: d2, ManifestSize: int64(len(mf2)),
			MediaType: "application/vnd.oci.image.manifest.v1+json",
		},
		Baseline: diff.BaselineRef{
			ManifestDigest: digest.Digest("sha256:" + strings.Repeat("c", 64)),
			MediaType:      "application/vnd.oci.image.manifest.v1+json",
		},
	})
	sc.Blobs[d2] = diff.BlobEntry{
		Size: int64(len(mf2)), MediaType: "application/vnd.oci.image.manifest.v1+json",
		Encoding: diff.EncodingFull, ArchiveSize: int64(len(mf2)),
	}
	scBytes, err := json.Marshal(sc)
	require.NoError(t, err)

	writeBundleTar(t, out, map[string][]byte{
		diff.SidecarFilename: scBytes,
		"blobs/" + d1.Algorithm().String() + "/" + d1.Encoded(): mf1,
		"blobs/" + d2.Algorithm().String() + "/" + d2.Encoded(): mf2,
	})

	_, blobs, err := ReadSidecarAndManifestBlobs(out, []digest.Digest{d1, d2})
	require.NoError(t, err)
	require.Equal(t, mf1, blobs[d1])
	require.Equal(t, mf2, blobs[d2])
}

func TestReadSidecarAndManifestBlobs_MissingBlobReturnsError(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "bundle.tar")

	mf := []byte(`{"schemaVersion":2,"layers":[]}`)
	d := digest.FromBytes(mf)
	sc := minimalSidecarForBlobTest(t, d, len(mf))
	scBytes, err := json.Marshal(sc)
	require.NoError(t, err)

	writeBundleTar(t, out, map[string][]byte{diff.SidecarFilename: scBytes})

	_, _, err = ReadSidecarAndManifestBlobs(out, []digest.Digest{d})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found in archive")
}

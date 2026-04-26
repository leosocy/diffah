//go:build integration

package cmd_test

import (
	"archive/tar"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/pkg/diff"
)

// TestApplyCLI_MissingPatchSourceB1 proves the apply command surfaces the B1
// hint phrase when the baseline lacks a layer that a shipped patch references
// via patch_from_digest. The synthetic baseline is built by stripping the
// patch source layer (sha256:436177174c310a41e... in v1) from a copy of the
// v1 OCI fixture.
//
// Expected end-to-end behavior:
//   - exit 4 (CategoryContent)
//   - stderr contains the static phrase from
//     ErrMissingPatchSource.NextAction(): "re-run 'diffah diff' against this
//     baseline".
func TestApplyCLI_MissingPatchSourceB1(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()

	// 1. Build a v1->v2 delta. v1->v2 ships exactly one patch
	//    (a58c8ca7...) whose patch_from_digest is the v1 layer
	//    436177174c310a41e... — that is our B1 victim.
	deltaPath := filepath.Join(tmp, "delta.tar")
	buildDelta(t, bin, root, deltaPath)

	// 2. Discover the patch_from_digest from the sidecar so the test fails
	//    early if the fixture changes shape, instead of silently testing
	//    nothing.
	sc := readSidecarFromArchive(t, deltaPath)
	patchSource := firstPatchFromDigest(t, sc)

	// 3. Strip that layer from the v1 baseline.
	baselinePath := filepath.Join(root, "testdata/fixtures/v1_oci.tar")
	strippedBaseline := filepath.Join(tmp, "v1_baseline_no_patch_source.tar")
	stripLayerFromOCIArchive(t, baselinePath, strippedBaseline, patchSource)

	// 4. Apply must reach the B1 path: exit 4 + B1 hint on stderr.
	restoredArchive := filepath.Join(tmp, "restored.tar")
	_, stderr, exit := runDiffahBin(t, bin,
		"apply",
		deltaPath,
		"oci-archive:"+strippedBaseline,
		"oci-archive:"+restoredArchive,
	)

	require.Equal(t, 4, exit, "expected exit 4 (content) for missing patch source; stderr=%q", stderr)
	require.Contains(t, stderr, "re-run 'diffah diff' against this baseline",
		"expected B1 hint phrase from ErrMissingPatchSource.NextAction(); stderr=%q", stderr)
}

// TestApplyCLI_MissingBaselineReuseLayerB2 proves the apply command surfaces
// the B2 hint phrase when the baseline lacks a layer that the target manifest
// references but that the delta did not ship (a "baseline-only-reuse" layer).
//
// In v1->v2, the layer sha256:f3f445cb429b9458a... appears in BOTH manifests
// (it's reused unchanged), so the diff phase emits no blob for it; apply
// fetches it directly from the baseline. Stripping it from the baseline
// triggers B2.
//
// Expected end-to-end behavior:
//   - exit 4 (CategoryContent)
//   - stderr contains the static phrase from
//     ErrMissingBaselineReuseLayer.NextAction(): "re-run diff with a wider
//     baseline".
func TestApplyCLI_MissingBaselineReuseLayerB2(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()

	// 1. Build the same v1->v2 delta.
	deltaPath := filepath.Join(tmp, "delta.tar")
	buildDelta(t, bin, root, deltaPath)

	// 2. Find a target layer whose digest is NOT in sidecar.Blobs — that
	//    is the baseline-only-reuse layer.
	sc := readSidecarFromArchive(t, deltaPath)
	reuseLayer := firstBaselineOnlyReuseLayer(t, root, sc)

	// 3. Strip that layer from the v1 baseline.
	baselinePath := filepath.Join(root, "testdata/fixtures/v1_oci.tar")
	strippedBaseline := filepath.Join(tmp, "v1_baseline_no_reuse_layer.tar")
	stripLayerFromOCIArchive(t, baselinePath, strippedBaseline, reuseLayer)

	// 4. Apply must reach the B2 path.
	restoredArchive := filepath.Join(tmp, "restored.tar")
	_, stderr, exit := runDiffahBin(t, bin,
		"apply",
		deltaPath,
		"oci-archive:"+strippedBaseline,
		"oci-archive:"+restoredArchive,
	)

	require.Equal(t, 4, exit, "expected exit 4 (content) for missing baseline reuse layer; stderr=%q", stderr)
	require.Contains(t, stderr, "re-run diff with a wider baseline",
		"expected B2 hint phrase from ErrMissingBaselineReuseLayer.NextAction(); stderr=%q", stderr)
}

// readSidecarFromArchive reads and parses the sidecar from a delta archive.
func readSidecarFromArchive(t *testing.T, archivePath string) *diff.Sidecar {
	t.Helper()
	raw, err := archive.ReadSidecar(archivePath)
	require.NoError(t, err, "ReadSidecar(%s)", archivePath)
	sc, err := diff.ParseSidecar(raw)
	require.NoError(t, err, "ParseSidecar")
	return sc
}

// firstPatchFromDigest returns the patch_from_digest of any encoding=patch
// blob in the sidecar. Tests assume at least one such blob exists; if the
// fixture stops carrying patches, the test should fail loudly rather than
// silently no-op.
func firstPatchFromDigest(t *testing.T, sc *diff.Sidecar) digest.Digest {
	t.Helper()
	for _, entry := range sc.Blobs {
		if entry.Encoding == diff.EncodingPatch && entry.PatchFromDigest != "" {
			return entry.PatchFromDigest
		}
	}
	t.Fatalf("sidecar has no encoding=patch blob; cannot exercise B1 path")
	return ""
}

// firstBaselineOnlyReuseLayer returns the first layer of the target manifest
// whose digest is NOT present in sidecar.Blobs — that layer must come from
// the baseline at apply time.
func firstBaselineOnlyReuseLayer(t *testing.T, root string, sc *diff.Sidecar) digest.Digest {
	t.Helper()
	require.Len(t, sc.Images, 1, "test assumes single-image bundle")
	targetDigest := sc.Images[0].Target.ManifestDigest

	// Read the target manifest from the v2 fixture (it lives there in full).
	v2Path := filepath.Join(root, "testdata/fixtures/v2_oci.tar")
	manifest := readManifestFromOCIArchive(t, v2Path, targetDigest)

	for _, layer := range manifest.Layers {
		if _, shipped := sc.Blobs[layer.Digest]; !shipped {
			return layer.Digest
		}
	}
	t.Fatalf("v2 target manifest has no baseline-only-reuse layer; cannot exercise B2 path")
	return ""
}

// ociManifest is a minimal projection of the OCI image manifest JSON for the
// fields our tests need. We deliberately avoid pulling in
// github.com/opencontainers/image-spec just for these tests.
type ociManifest struct {
	Layers []ociDescriptor `json:"layers"`
}

type ociDescriptor struct {
	Digest digest.Digest `json:"digest"`
}

// readManifestFromOCIArchive opens an OCI tar archive and returns the manifest
// blob with the requested digest.
func readManifestFromOCIArchive(t *testing.T, archivePath string, want digest.Digest) ociManifest {
	t.Helper()
	blobs := readBlobsFromOCIArchive(t, archivePath)
	raw, ok := blobs[blobPathFor(want)]
	require.Truef(t, ok, "blob %s not found in %s", want, archivePath)

	var m ociManifest
	require.NoError(t, json.Unmarshal(raw, &m), "unmarshal manifest %s", want)
	return m
}

// readBlobsFromOCIArchive reads every entry of an OCI archive into memory and
// returns a map keyed by tar header name (e.g. "blobs/sha256/<hex>").
func readBlobsFromOCIArchive(t *testing.T, archivePath string) map[string][]byte {
	t.Helper()
	f, err := os.Open(archivePath)
	require.NoError(t, err, "open %s", archivePath)
	defer f.Close()

	out := make(map[string][]byte)
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err, "tar.Next on %s", archivePath)
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		buf := make([]byte, hdr.Size)
		_, err = io.ReadFull(tr, buf)
		require.NoError(t, err, "read entry %s from %s", hdr.Name, archivePath)
		out[hdr.Name] = buf
	}
	return out
}

// blobPathFor returns the OCI image-layout path of a blob for the given digest.
func blobPathFor(d digest.Digest) string {
	return filepath.Join("blobs", d.Algorithm().String(), d.Encoded())
}

// stripLayerFromOCIArchive copies srcPath to dstPath while omitting the blob
// whose digest equals omit. The manifest and index.json are NOT rewritten:
// the importer pre-flight verifies the baseline manifest digest matches what
// the sidecar recorded, so rewriting the manifest would trip that check
// before the apply pipeline reaches GetBlob/servePatch — exactly the paths
// these tests must exercise.
//
// Leaving the manifest reference dangling is the intent: at apply time the
// importer asks the baseline source for a layer (B2) or for a patch_from
// blob (B1), the source returns an os.ErrNotExist on the missing file, and
// our isBlobNotFound predicate wraps it into the appropriate sentinel.
//
// The function is intentionally small and uncompressed-tar-only: the v1
// fixtures ship as plain tar; no step here touches large blob contents.
func stripLayerFromOCIArchive(t *testing.T, srcPath, dstPath string, omit digest.Digest) {
	t.Helper()
	entries := readBlobsFromOCIArchive(t, srcPath)
	require.Contains(t, entries, "index.json", "src archive has no index.json")
	require.Contains(t, entries, "oci-layout", "src archive has no oci-layout")
	require.Containsf(t, entries, blobPathFor(omit),
		"omit digest %s not found in src archive", omit)

	rewritten := make(map[string][]byte, len(entries))
	for name, payload := range entries {
		if name == blobPathFor(omit) {
			continue
		}
		rewritten[name] = payload
	}

	writeOCIArchive(t, dstPath, rewritten)
}

// writeOCIArchive writes a flat map of header-name -> bytes as a plain tar
// archive at dstPath. Names are written in sorted order so the resulting tar
// is deterministic — useful when tests are reused across runs.
func writeOCIArchive(t *testing.T, dstPath string, entries map[string][]byte) {
	t.Helper()
	out, err := os.Create(dstPath)
	require.NoError(t, err, "create %s", dstPath)
	defer out.Close()

	tw := tar.NewWriter(out)
	defer tw.Close()

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	// Skip directory headers entirely — oci-archive does not require them and
	// readers create parent directories implicitly when a regular file is
	// extracted.
	for _, name := range names {
		payload := entries[name]
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(payload)),
			Typeflag: tar.TypeReg,
		}
		require.NoError(t, tw.WriteHeader(hdr), "WriteHeader %s", name)
		_, err := tw.Write(payload)
		require.NoErrorf(t, err, "Write %s", name)
	}
}

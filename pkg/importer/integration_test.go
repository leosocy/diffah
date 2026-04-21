//go:build integration

package importer_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/manifest"
	"go.podman.io/image/v5/types"

	"github.com/leosocy/diffah/internal/archive"
	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/diff"
	"github.com/leosocy/diffah/pkg/exporter"
	"github.com/leosocy/diffah/pkg/importer"
)

// buildDeltaWithFingerprinter runs Export with a custom Fingerprinter,
// useful for driving the content-similarity matcher into specific
// branches (e.g., empty fingerprinter → all-zero scores → size-closest).
func buildDeltaWithFingerprinter(
	t *testing.T, transport, targetTar, baselineTar string, fp exporter.Fingerprinter,
) string {
	t.Helper()
	ctx := context.Background()
	target, err := imageio.ParseReference(transport + ":" + filepath.Join(repoRoot(t), "testdata/fixtures", targetTar))
	require.NoError(t, err)
	baseline, err := imageio.ParseReference(transport + ":" + filepath.Join(repoRoot(t), "testdata/fixtures", baselineTar))
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "delta.tar")
	require.NoError(t, exporter.ExportWithFingerprinter(ctx, exporter.Options{
		TargetRef:         target,
		LegacyBaselineRef: baseline,
		OutputPath:        out,
		ToolVersion:       "test",
		IntraLayer:        "auto",
	}, fp))
	return out
}

func TestImport_Matrix(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name            string
		targetFixture   string
		baselineFixture string
		sourceTransport string // "oci-archive" or "docker-archive"
		outputFormat    string
		allowConvert    bool
		wantConflict    bool
	}{
		{"oci→docker-archive rejected", "v2_oci.tar", "v1_oci.tar", "oci-archive", "docker-archive", false, true},
		{"oci→docker-archive with allow", "v2_oci.tar", "v1_oci.tar", "oci-archive", "docker-archive", true, false},
		{"oci→oci-archive match", "v2_oci.tar", "v1_oci.tar", "oci-archive", "oci-archive", false, false},
		{"oci→auto", "v2_oci.tar", "v1_oci.tar", "oci-archive", "", false, false},
		{"oci→dir", "v2_oci.tar", "v1_oci.tar", "oci-archive", "dir", false, false},
		{"schema2→docker-archive match", "v2_s2.tar", "v1_s2.tar", "docker-archive", "docker-archive", false, false},
		{"schema2→auto", "v2_s2.tar", "v1_s2.tar", "docker-archive", "", false, false},
		{"schema2→oci-archive rejected", "v2_s2.tar", "v1_s2.tar", "docker-archive", "oci-archive", false, true},
		{"schema2→oci-archive with allow", "v2_s2.tar", "v1_s2.tar", "docker-archive", "oci-archive", true, false},
		{"schema2→dir", "v2_s2.tar", "v1_s2.tar", "docker-archive", "dir", false, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			root := repoRoot(t)

			var delta string
			if tc.sourceTransport == "oci-archive" {
				delta = buildDelta(t, tc.targetFixture, tc.baselineFixture)
			} else {
				delta = buildDeltaS2(t, tc.targetFixture, tc.baselineFixture)
			}

			baselineRefStr := tc.sourceTransport + ":" + filepath.Join(root, "testdata/fixtures", tc.baselineFixture)
			baselineRef, err := imageio.ParseReference(baselineRefStr)
			require.NoError(t, err)

			out := filepath.Join(t.TempDir(), fmt.Sprintf("out-%s", tc.outputFormat))
			err = importer.Import(ctx, importer.Options{
				DeltaPath:         delta,
				LegacyBaselineRef: baselineRef,
				OutputFormat:      tc.outputFormat,
				OutputPath:        out,
				AllowConvert:      tc.allowConvert,
			})
			if tc.wantConflict {
				var conflict *diff.ErrIncompatibleOutputFormat
				require.ErrorAs(t, err, &conflict)
				_, statErr := os.Stat(out)
				require.True(t, os.IsNotExist(statErr))
				return
			}
			require.NoError(t, err)

			info, err := os.Stat(out)
			require.NoError(t, err)
			if tc.outputFormat == "dir" {
				require.True(t, info.IsDir())
			} else {
				require.Greater(t, info.Size(), int64(0))
			}
		})
	}
}

// TestImport_V4ContentMatchBeatsSizeTrap verifies the v4 fixture's
// divergent case is resolved correctly: for every target layer shipped
// as a patch, the chosen baseline must NOT be the size-closest trap
// (bSizeTrap) — only bAppV1/bDataV1/bSmall carry shared content.
//
// The fixture's divergence is deliberate: bSizeTrap's compressed byte
// size is the closest to tApp's, but its content overlap is zero.
// Size-closest would pick bSizeTrap; content-match picks the correct
// bAppV1.
func TestImport_V4ContentMatchBeatsSizeTrap(t *testing.T) {
	ctx := context.Background()
	delta := buildDeltaIntraLayerAuto(t, "oci-archive", "v4_target_oci.tar", "v4_baseline_oci.tar")

	// Read the sidecar to inspect planner decisions.
	sc := readSidecarFromDelta(t, delta)
	require.NotEmpty(t, sc.ShippedInDelta)

	// Identify the bSizeTrap digest from the baseline fixture's
	// manifest. It is the baseline layer that, by compressed size, is
	// closest to tApp — and whose decompressed tar contains only
	// unique/ entries (no shared/ entries) and has at least 8 files.
	baselineRef, err := imageio.ParseReference("oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v4_baseline_oci.tar"))
	require.NoError(t, err)
	sizeTrapDigest := findSizeTrapBaselineDigest(t, ctx, baselineRef)
	require.NotEmpty(t, sizeTrapDigest, "bSizeTrap must be identifiable")

	// For every patch entry, patch-from must not point at sizeTrap.
	patchCount := 0
	for _, entry := range sc.ShippedInDelta {
		if entry.Encoding != diff.EncodingPatch {
			continue
		}
		patchCount++
		require.NotEqual(t, sizeTrapDigest, entry.PatchFromDigest,
			"target layer %s was matched against size-closest bSizeTrap; content-match must win",
			entry.Digest)
	}
	require.Greater(t, patchCount, 0, "expected at least one patch entry in v4 delta")

	// Round-trip: import produces a target-byte-exact image. Assert the
	// reconstructed manifest digest equals the original target's — a
	// stricter bar than "file exists" that proves no layer was silently
	// rewritten by the content-match path.
	out := filepath.Join(t.TempDir(), "v4_out.tar")
	err = importer.Import(ctx, importer.Options{
		DeltaPath:         delta,
		LegacyBaselineRef: baselineRef,
		OutputPath:        out,
		OutputFormat:      "oci-archive",
	})
	require.NoError(t, err)

	targetRef, err := imageio.ParseReference(
		"oci-archive:" + filepath.Join(repoRoot(t), "testdata/fixtures/v4_target_oci.tar"))
	require.NoError(t, err)
	outRef, err := imageio.ParseReference("oci-archive:" + out)
	require.NoError(t, err)
	require.Equal(t,
		readManifestDigest(ctx, t, targetRef),
		readManifestDigest(ctx, t, outRef),
		"round-trip must produce byte-exact manifest digest")
}

// TestExport_ContentMatchStrictlySmallerThanSizeOnly forces a size-only
// fingerprinter (empty fingerprint → score 0 on every candidate →
// pickSimilar's branch C → size-closest). The resulting archive must
// be strictly LARGER than the default content-match archive. Guards
// against the matcher silently becoming a no-op.
func TestExport_ContentMatchStrictlySmallerThanSizeOnly(t *testing.T) {
	contentArchive := buildDeltaIntraLayerAuto(t, "oci-archive", "v4_target_oci.tar", "v4_baseline_oci.tar")
	sizeOnlyArchive := buildDeltaWithFingerprinter(t, "oci-archive", "v4_target_oci.tar", "v4_baseline_oci.tar", emptyFingerprinter{})

	fiContent, err := os.Stat(contentArchive)
	require.NoError(t, err)
	fiSize, err := os.Stat(sizeOnlyArchive)
	require.NoError(t, err)

	require.Less(t, fiContent.Size(), fiSize.Size(),
		"content-match archive (%d B) must be strictly smaller than size-only archive (%d B)",
		fiContent.Size(), fiSize.Size())
}

// emptyFingerprinter is a test-only fingerprinter that always returns
// an empty (non-nil) Fingerprint, forcing pickSimilar's branch C
// (max score 0 → size-closest).
type emptyFingerprinter struct{}

func (emptyFingerprinter) Fingerprint(
	_ context.Context, _ string, _ []byte,
) (exporter.Fingerprint, error) {
	return exporter.Fingerprint{}, nil
}

// readSidecarFromDelta opens the given delta archive, reads its
// sidecar bytes, parses them, and returns the typed LegacySidecar.
func readSidecarFromDelta(t *testing.T, deltaPath string) *diff.LegacySidecar {
	t.Helper()
	raw, err := archive.ReadSidecar(deltaPath)
	require.NoError(t, err)
	sc, err := diff.ParseLegacySidecar(raw)
	require.NoError(t, err)
	return sc
}

// findSizeTrapBaselineDigest walks the baseline image's layers and
// returns the digest of the layer that matches the bSizeTrap profile:
// its decompressed tar contains ONLY entries under "unique/" (no
// "shared/" entries) and has at least 8 regular files. This distinguishes
// it from bDecoy which has only 5 unique files.
func findSizeTrapBaselineDigest(
	t *testing.T, ctx context.Context, ref types.ImageReference,
) digest.Digest {
	t.Helper()
	src, err := ref.NewImageSource(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = src.Close() }()

	mBytes, mt, err := src.GetManifest(ctx, nil)
	require.NoError(t, err)

	m, err := manifest.FromBlob(mBytes, mt)
	require.NoError(t, err)

	for _, li := range m.LayerInfos() {
		blob, _, err := src.GetBlob(ctx, li.BlobInfo, nil)
		require.NoError(t, err)
		data, err := io.ReadAll(blob)
		_ = blob.Close()
		require.NoError(t, err)

		if isSizeTrapLayer(t, data) {
			return li.Digest
		}
	}
	return ""
}

// isSizeTrapLayer returns true iff the given gzipped tar contains only
// files under "unique/" prefix — no "shared/" entries at all — AND has
// at least 8 regular files. The file-count guard distinguishes bSizeTrap
// (10 files) from bDecoy (5 files), both of which are all-unique layers.
func isSizeTrapLayer(t *testing.T, compressed []byte) bool {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return false
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	hasShared := false
	fileCount := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		fileCount++
		if strings.HasPrefix(hdr.Name, "shared/") {
			hasShared = true
		}
	}
	return !hasShared && fileCount >= 8
}

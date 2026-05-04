//go:build big

package exporter_test

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/leosocy/diffah/pkg/exporter"
)

// TestScaleBench_2GiBLayer exports a deterministic 2 GiB-layer fixture and
// asserts that the output bundle is non-empty and decodable (functional
// correctness). Peak RSS is measured externally by the CI workflow via
// /usr/bin/time -v, which gates the ≤ 8 GiB ceiling (spec §13).
//
// Gated by DIFFAH_BIG_TEST=1 (in addition to the `big` build tag) so an
// accidental `go test -tags=big` on a developer's laptop doesn't burn 45
// minutes. Set DIFFAH_BIG_TEST=1 together with -tags=big to actually run.
func TestScaleBench_2GiBLayer(t *testing.T) {
	if os.Getenv("DIFFAH_BIG_TEST") != "1" {
		t.Skip("set DIFFAH_BIG_TEST=1 to run")
	}

	// Build the fixture pair via the fixture-builder script so we produce a
	// proper OCI archive (manifest + config + layer blob) that oci-archive:
	// transports can consume. The script is in scripts/build_fixtures; run
	// from repo root so the module is resolvable.
	fixtureDir := t.TempDir()
	buildFixtures(t, fixtureDir, 2<<30)

	// Export the pair. Use conservative settings (Workers=2, Candidates=1) so
	// spool disk usage stays within ≈ 4 GiB on the CI runner.
	out := filepath.Join(fixtureDir, "bundle.tar")
	opts := exporter.Options{
		Pairs: []exporter.Pair{
			{
				Name:        "scale",
				BaselineRef: "oci-archive:" + filepath.Join(fixtureDir, "baseline.tar"),
				TargetRef:   "oci-archive:" + filepath.Join(fixtureDir, "target.tar"),
			},
		},
		Platform:      "linux/amd64",
		OutputPath:    out,
		ToolVersion:   "bench",
		CreatedAt:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Workers:       2,
		Candidates:    1,
		ZstdLevel:     22,
		ZstdWindowLog: 0,
	}

	require.NoError(t, exporter.Export(context.Background(), opts))

	// Assert the output bundle is non-empty and parseable as a tar archive.
	assertBundleDecodable(t, out)
}

// buildFixtures invokes scripts/build_fixtures with -scale=<bytes> to produce
// baseline.tar and target.tar in outDir. Runs from repo root so the module
// path resolves correctly.
func buildFixtures(t *testing.T, outDir string, scaleBytes int64) {
	t.Helper()

	// Resolve repo root: this test file lives in pkg/exporter/, so "../.."
	// is the repo root. Matches the depth used in determinism_test.go for
	// fixture references (oci-archive:../../testdata/fixtures/...).
	repoRoot := filepath.Join("..", "..")

	cmd := exec.Command(
		"go", "run",
		"-tags=containers_image_openpgp",
		"./scripts/build_fixtures",
		"-scale="+strconv.FormatInt(scaleBytes, 10),
		"-out="+outDir,
	)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	t.Logf("build_fixtures output:\n%s", out)
	require.NoError(t, err, "build scale fixtures failed")
}

// assertBundleDecodable verifies the bundle is a valid tar and non-empty.
func assertBundleDecodable(t *testing.T, path string) {
	t.Helper()

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	// Compute sha256 while reading.
	h := sha256.New()
	tr := tar.NewReader(io.TeeReader(f, h))

	entryCount := 0
	for {
		_, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err, "malformed bundle tar")
		entryCount++
		_, err = io.Copy(io.Discard, tr)
		require.NoError(t, err, "read bundle entry")
	}

	require.Greater(t, entryCount, 0, "bundle tar is empty")
	digest := hex.EncodeToString(h.Sum(nil))
	t.Logf("bundle sha256=%s entries=%d", digest, entryCount)
}

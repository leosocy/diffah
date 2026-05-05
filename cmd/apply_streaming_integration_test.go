//go:build integration

package cmd_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestApply_OutputIsByteIdenticalAcrossWorkerCounts proves that PR5's
// admission-pool-driven importEachImage (replacing the serial loop) does
// not perturb apply output. Driving the same `diffah apply` across
// {1, 2, 4, 8} workers must produce identical reconstructed image
// content — every blob in the OCI image layout (manifest, config, every
// layer) has identical bytes for the same digest.
//
// Determinism contract: admission only changes WHEN images run, not
// WHAT they produce. PR3 spool (singleflight + atomic rename), PR4
// per-call scratch suffix + path-backed verifyingReadCloser, and PR5's
// post-Wait sortResultsByApplyList all collaborate to keep the
// reconstructed content stable.
//
// Why content-hash, not full-archive-hash:
// The oci-archive: transport's outer tar wrapper carries non-content
// metadata (per-entry mtime defaulted to time.Now() inside
// containers-image's archive writer, plus index.json with a
// timestamp-stamped annotation). Two consecutive runs of the same
// apply with the same --workers produce different full-archive sha256s
// — the variance is in the wrapper, not the image content. Hashing the
// concatenation of every content-addressed blob (sorted by tar header
// name for stability) drops that wrapper noise and tests what the
// admission pool actually controls.
//
// Caveat: the canonical fixtures (v1_oci.tar / v2_oci.tar) describe a
// single image per bundle, so each worker count submits exactly one
// task to the pool. The test proves "the pool wrapping doesn't change
// output" — not "parallelism doesn't reorder anything." Multi-image
// determinism is covered by sortResultsByApplyList unit tests and will
// get a fixture-grade gate when multi-image bundles land.
func TestApply_OutputIsByteIdenticalAcrossWorkerCounts(t *testing.T) {
	root := findRepoRoot(t)
	bin := integrationBinary(t)
	tmp := t.TempDir()

	// Build the delta once; reuse across worker counts so the only
	// variable is --workers.
	delta := filepath.Join(tmp, "delta.tar")
	{
		cmd := exec.Command(bin,
			"diff",
			"oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
			"oci-archive:"+filepath.Join(root, "testdata/fixtures/v2_oci.tar"),
			delta,
		)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "diff: %s", string(out))
	}

	workerCounts := []int{1, 2, 4, 8}
	digests := make(map[int]string, len(workerCounts))

	for _, w := range workerCounts {
		w := w
		t.Run(fmt.Sprintf("workers=%d", w), func(t *testing.T) {
			restored := filepath.Join(tmp, fmt.Sprintf("restored-w%d.tar", w))
			cmd := exec.Command(bin,
				"apply",
				"--workers", fmt.Sprintf("%d", w),
				"--memory-budget", "0",
				delta,
				"oci-archive:"+filepath.Join(root, "testdata/fixtures/v1_oci.tar"),
				"oci-archive:"+restored,
			)
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "apply --workers=%d: %s", w, string(out))

			digests[w] = hashOCIBlobs(t, restored)
		})
	}

	// Compare every digest against workers=1 (the strict-serial reference).
	require.NotEmpty(t, digests[1], "serial reference run was skipped")
	for _, w := range workerCounts {
		require.Equal(t, digests[1], digests[w],
			"restored image content sha256 differs across worker counts: %v", digests)
	}
}

// hashOCIBlobs returns sha256 of the concatenation of every
// content-addressed blob in the OCI archive at archivePath, ordered by
// tar header name. All entries under "blobs/sha256/" are included; the
// outer wrapper (oci-layout / index.json / tar headers) is ignored
// because those carry non-content metadata that mutates between runs
// even for byte-identical reconstructed images.
func hashOCIBlobs(t *testing.T, archivePath string) string {
	t.Helper()
	entries := readBlobsFromOCIArchive(t, archivePath)
	names := make([]string, 0, len(entries))
	for name := range entries {
		if strings.HasPrefix(name, "blobs/") {
			names = append(names, name)
		}
	}
	require.NotEmpty(t, names, "archive %s has no blobs/ entries", archivePath)
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		// Include the name in the hash so a renamed-but-same-bytes
		// blob (impossible under content addressing, but defensive
		// against future changes to the layout convention) is caught.
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(entries[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}

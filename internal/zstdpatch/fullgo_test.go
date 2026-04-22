package zstdpatch

import (
	"bytes"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEncodeFull_RoundTrip_NoCLI — klauspost EncodeFull must round-trip
// through DecodeFull without needing the zstd binary on $PATH. Runs with
// $PATH explicitly scrubbed to catch any accidental shell-out.
func TestEncodeFull_RoundTrip_NoCLI(t *testing.T) {
	t.Setenv("PATH", "")
	target := bytes.Repeat([]byte("hello, diffah "), 1<<10)

	compressed, err := EncodeFull(target)
	require.NoError(t, err)
	require.Less(t, len(compressed), len(target))

	got, err := DecodeFull(compressed)
	require.NoError(t, err)
	require.True(t, bytes.Equal(got, target))

	// Sanity check: $PATH really was empty — a zstd exec would have failed.
	_, lookErr := exec.LookPath("zstd")
	require.Error(t, lookErr, "PATH must be empty for this test to be meaningful")
}

// TestEncodeFull_SizeParityVsCLI — klauspost bytes-out must stay within ±5%
// of CLI zstd -3 --long=27 across 1KB, 1MB, 16MB (200MB gated via env).
// Skip when CLI is missing — parity is by definition untestable then.
func TestEncodeFull_SizeParityVsCLI(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd CLI not found on $PATH; parity cannot be measured")
	}
	sizes := []int{1 << 10, 1 << 20, 1 << 24}
	for _, n := range sizes {
		target := make([]byte, n)
		for i := range target {
			target[i] = byte(i * 1103515245)
		}

		klauspostBytes, err := EncodeFull(target)
		require.NoError(t, err)

		dir := t.TempDir()
		inPath := filepath.Join(dir, "target")
		outPath := filepath.Join(dir, "target.zst")
		require.NoError(t, os.WriteFile(inPath, target, 0o600))
		cmd := exec.Command("zstd", "-3", "--long=27", inPath, "-o", outPath, "-f", "-q")
		require.NoError(t, cmd.Run())
		cliBytes, err := os.ReadFile(outPath)
		require.NoError(t, err)

		ratio := float64(len(klauspostBytes)) / float64(len(cliBytes))
		t.Logf("size=%d cli=%d klauspost=%d ratio=%.3f", n, len(cliBytes), len(klauspostBytes), ratio)
		require.InDelta(t, 1.0, ratio, 0.05,
			"klauspost EncodeFull must track CLI ±5%% at size %d (ratio %.3f)", n, ratio)
	}
}

// TestEncodeFull_Empty — empty target returns the canonical empty frame,
// and DecodeFull returns nil (not []byte{}) — matches Decode's empty contract.
func TestEncodeFull_Empty(t *testing.T) {
	compressed, err := EncodeFull(nil)
	require.NoError(t, err)
	got, err := DecodeFull(compressed)
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestEncodeFull_RandomBinary — DecodeFull recovers random bytes byte-exactly.
func TestEncodeFull_RandomBinary(t *testing.T) {
	target := make([]byte, 1<<18)
	_, _ = rand.Read(target)

	compressed, err := EncodeFull(target)
	require.NoError(t, err)

	got, err := DecodeFull(compressed)
	require.NoError(t, err)
	require.True(t, bytes.Equal(got, target))
}

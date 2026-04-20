package zstdpatch

import (
	"bytes"
	"crypto/rand"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func skipWithoutZstd(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd CLI not found on $PATH; skipping")
	}
}

// TestRoundTrip_Empty covers the degenerate 0-byte case.
func TestRoundTrip_Empty(t *testing.T) {
	skipWithoutZstd(t)
	ref := []byte("unused reference bytes")
	patch, err := Encode(ref, nil)
	require.NoError(t, err)
	got, err := Decode(ref, patch)
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestRoundTrip_SmallDelta — 1 MB target that overlaps 1 MB of reference
// except for a 1-byte change. Patch must decode byte-exactly.
func TestRoundTrip_SmallDelta(t *testing.T) {
	skipWithoutZstd(t)
	ref := make([]byte, 1<<20)
	_, _ = rand.Read(ref)
	target := append([]byte(nil), ref...)
	target[len(target)/2] ^= 0xFF

	patch, err := Encode(ref, target)
	require.NoError(t, err)
	require.Less(t, len(patch), len(target)/2,
		"patch of a 1-byte delta should be far smaller than half the target")

	got, err := Decode(ref, patch)
	require.NoError(t, err)
	require.True(t, bytes.Equal(got, target), "decoded bytes differ from target")
}

// TestDecode_WrongReference — swapping the reference at decode time must
// either return an error or return bytes that are detectably not the target.
func TestDecode_WrongReference(t *testing.T) {
	skipWithoutZstd(t)
	// Use random data so zstd's patch-from actually references the dictionary
	// bytes. Repetitive patterns (e.g. bytes.Repeat) get encoded via zstd's
	// internal repeat coding, making the patch independent of the reference.
	refA := make([]byte, 1<<20)
	_, _ = rand.Read(refA)
	refB := make([]byte, 1<<20)
	_, _ = rand.Read(refB)
	target := append([]byte(nil), refA...)
	target[0] ^= 0xFF

	patch, err := Encode(refA, target)
	require.NoError(t, err)

	got, err := Decode(refB, patch)
	if err == nil {
		// Decode may succeed but must not return the original target bytes.
		require.False(t, bytes.Equal(got, target),
			"decoding with the wrong reference returned target bytes silently")
	}
}

// TestEncodeFull_RoundTrip — EncodeFull is a plain zstd encode with no
// reference; DecodeFull must recover the target.
func TestEncodeFull_RoundTrip(t *testing.T) {
	skipWithoutZstd(t)
	target := bytes.Repeat([]byte("hello, diffah "), 1<<10)

	compressed, err := EncodeFull(target)
	require.NoError(t, err)
	require.Less(t, len(compressed), len(target))

	got, err := DecodeFull(compressed)
	require.NoError(t, err)
	require.True(t, bytes.Equal(got, target))
}

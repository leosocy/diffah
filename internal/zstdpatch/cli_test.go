package zstdpatch

import (
	"bytes"
	"context"
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
	patch, err := Encode(context.Background(), ref, nil)
	require.NoError(t, err)
	got, err := Decode(context.Background(), ref, patch)
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

	patch, err := Encode(context.Background(), ref, target)
	require.NoError(t, err)
	require.Less(t, len(patch), len(target)/2,
		"patch of a 1-byte delta should be far smaller than half the target")

	got, err := Decode(context.Background(), ref, patch)
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

	patch, err := Encode(context.Background(), refA, target)
	require.NoError(t, err)

	got, err := Decode(context.Background(), refB, patch)
	if err == nil {
		// Decode may succeed but must not return the original target bytes.
		require.False(t, bytes.Equal(got, target),
			"decoding with the wrong reference returned target bytes silently")
	}
}

// TestEncode_PreCancelledCtx_ReturnsErrorIsCanceled — if the caller's
// ctx is already cancelled when Encode is invoked, exec.CommandContext
// refuses to spawn the subprocess and Encode's error chain surfaces
// context.Canceled via the %w wrap in cli.go's encode error return.
//
// This test deliberately does NOT verify mid-flight subprocess kill —
// that would require a goroutine + timing synchronization. It only
// guarantees the pre-start cancellation path is wired correctly.
func TestEncode_PreCancelledCtx_ReturnsErrorIsCanceled(t *testing.T) {
	skipWithoutZstd(t)
	// Tiny payload — subprocess never spawns, so size doesn't matter.
	// Empty target would hit the empty-frame short-circuit in Encode and
	// bypass the CommandContext path entirely, so use one non-zero byte.
	ref := []byte{0x00}
	target := []byte{0x01}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Encode(ctx, ref, target)
	require.ErrorIs(t, err, context.Canceled, "Encode must surface ctx cancellation")
}

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
	patch, err := Encode(context.Background(), ref, nil, EncodeOpts{})
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

	patch, err := Encode(context.Background(), ref, target, EncodeOpts{})
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

	patch, err := Encode(context.Background(), refA, target, EncodeOpts{})
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

	_, err := Encode(ctx, ref, target, EncodeOpts{})
	require.ErrorIs(t, err, context.Canceled, "Encode must surface ctx cancellation")
}

// TestEncodeOptsDefaults_RoundTripsViaDecode — zero-valued EncodeOpts
// must round-trip cleanly via Decode. The argv-identity claim (today's
// argv is `-3 --long=27`, which is exactly what `EncodeOpts{}.levelArg()`
// and `windowArg()` produce) is asserted separately at the helper layer
// in TestEncodeOpts_ArgvDefaults.
func TestEncodeOptsDefaults_RoundTripsViaDecode(t *testing.T) {
	skipWithoutZstd(t)
	ref := bytes.Repeat([]byte("rrrr"), 1024)
	target := append(append([]byte{}, ref...), bytes.Repeat([]byte("nnnn"), 256)...)

	got, err := Encode(context.Background(), ref, target, EncodeOpts{})
	require.NoError(t, err)
	require.NotEmpty(t, got, "encode returned empty patch")

	back, err := Decode(context.Background(), ref, got)
	require.NoError(t, err)
	require.True(t, bytes.Equal(back, target), "round-trip mismatch")
}

// TestEncodeDecode_LargeWindowRoundTrip — exercises a window > 27 to
// prove the lifted decode cap (--long=31) admits Phase-4 frames whose
// matches sit beyond the historical 128 MiB window.
func TestEncodeDecode_LargeWindowRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("large fixture (200 MiB ref/target); run with -short=false")
	}
	skipWithoutZstd(t)
	const sz = 200 << 20 // 200 MiB
	ref := make([]byte, sz)
	for i := range ref {
		ref[i] = byte(i % 251) // pseudo-random pattern, not really random
	}
	target := make([]byte, sz)
	copy(target, ref)
	// Tweak a region near the end so encoding has to find it.
	for i := sz - 1024; i < sz; i++ {
		target[i] ^= 0xff
	}
	patch, err := Encode(context.Background(), ref, target,
		EncodeOpts{Level: 3, WindowLog: 30})
	require.NoError(t, err)
	back, err := Decode(context.Background(), ref, patch)
	require.NoError(t, err)
	require.True(t, bytes.Equal(back, target), "round-trip mismatch")
}

// TestEncodeOptsLevelAndWindowAreApplied — level=22 must produce a
// patch no larger than level=1 for any non-trivial payload.
func TestEncodeOptsLevelAndWindowAreApplied(t *testing.T) {
	skipWithoutZstd(t)
	ref := bytes.Repeat([]byte("aaaa"), 4096)
	target := append(append([]byte{}, ref...), bytes.Repeat([]byte("bbbb"), 1024)...)

	low, err := Encode(context.Background(), ref, target,
		EncodeOpts{Level: 1, WindowLog: 27})
	require.NoError(t, err)
	high, err := Encode(context.Background(), ref, target,
		EncodeOpts{Level: 22, WindowLog: 27})
	require.NoError(t, err)
	require.LessOrEqual(t, len(high), len(low),
		"level=22 patch (%d) should be <= level=1 patch (%d)", len(high), len(low))

	for label, p := range map[string][]byte{"low": low, "high": high} {
		back, err := Decode(context.Background(), ref, p)
		require.NoErrorf(t, err, "decode %s", label)
		require.Truef(t, bytes.Equal(back, target), "%s round-trip mismatch", label)
	}
}

// TestEncodeOpts_ArgvDefaults — anchors spec §6.2 byte-identical guarantee
// at the argv layer: zero-valued EncodeOpts must yield exactly "-3" /
// "--long=27", which is the literal Phase-3 argv. Explicit values must
// pass through unchanged.
func TestEncodeOpts_ArgvDefaults(t *testing.T) {
	tests := []struct {
		name   string
		opts   EncodeOpts
		level  string
		window string
	}{
		{"zero defaults to -3 --long=27", EncodeOpts{}, "-3", "--long=27"},
		{"explicit level=22 window=30", EncodeOpts{Level: 22, WindowLog: 30}, "-22", "--long=30"},
		{"explicit level=12 window=27", EncodeOpts{Level: 12, WindowLog: 27}, "-12", "--long=27"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.opts.levelArg(); got != tc.level {
				t.Errorf("levelArg() = %q, want %q", got, tc.level)
			}
			if got := tc.opts.windowArg(); got != tc.window {
				t.Errorf("windowArg() = %q, want %q", got, tc.window)
			}
		})
	}
}

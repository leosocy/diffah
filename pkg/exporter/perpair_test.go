package exporter

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSpoolReader_ConcurrentWritersToSamePath_AtomicRename verifies that N
// concurrent spoolReader calls targeting the same dstPath produce exactly one
// complete, non-interleaved payload. Each goroutine writes a distinct pattern
// so interleaving is detectable (under a direct-write mutation, concurrent
// O_TRUNC opens can produce a file whose bytes alternate between two patterns).
// The payload is 1 MiB so each writer spans 16 × 64-KiB chunks, giving many
// interleave windows. No leftover .tmp files must remain.
func TestSpoolReader_ConcurrentWritersToSamePath_AtomicRename(t *testing.T) {
	dir := t.TempDir()
	dstPath := filepath.Join(dir, "spool")

	const (
		N       = 8
		size    = 1 << 20 // 1 MiB — 16 chunks per writer at 64-KiB buffer
		pattern = size / 4
	)
	// Build N payloads, each filled with a distinct repeating byte value so any
	// interleaving (bytes from >1 writer) can be detected.
	payloads := make([][]byte, N)
	for i := 0; i < N; i++ {
		payloads[i] = bytes.Repeat([]byte{byte('A' + i)}, size)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rc := io.NopCloser(bytes.NewReader(payloads[i]))
			_, _ = spoolReader(context.Background(), rc, dstPath, nil) // concurrent losers may fail on rename; ignore
		}()
	}
	close(start)
	wg.Wait()

	got, err := os.ReadFile(dstPath)
	require.NoError(t, err)
	require.Len(t, got, size, "dstPath must be exactly %d bytes (one full payload)", size)

	// The file must consist entirely of a single byte value — one writer's
	// complete, uninterleaved payload. Any mix of values indicates interleaving.
	first := got[0]
	var matched bool
	for _, p := range payloads {
		if p[0] == first {
			matched = bytes.Equal(got, p)
			break
		}
	}
	require.True(t, matched,
		"dstPath must be one complete payload (all bytes == %q); got mixed bytes", first)

	// No leftover .tmp files.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		require.False(t, strings.Contains(e.Name(), ".tmp."),
			"no leftover tmp files; got %s", e.Name())
	}
}

func TestPlanPair_ClassifiesLayers(t *testing.T) {
	p, err := planPair(context.Background(), Pair{
		Name: "svc", BaselineRef: "oci-archive:../../testdata/fixtures/v1_oci.tar",
		TargetRef: "oci-archive:../../testdata/fixtures/v2_oci.tar",
	}, &Options{Platform: "linux/amd64"})
	require.NoError(t, err)
	require.Equal(t, "svc", p.Name)
	require.NotEmpty(t, p.TargetManifest)
	require.NotEmpty(t, p.Shipped, "v2 differs from v1 by at least one layer")
	require.NotEmpty(t, p.Required, "shared base layer required from baseline")
}
